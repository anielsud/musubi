package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// socketDir is a short temporary folder: a Unix socket path is limited
// to about 100 bytes, which t.TempDir on macOS can pass.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// fakeHandler answers a few ops the way the recorder would.
func fakeHandler(ctx context.Context, op string, args json.RawMessage) (any, error) {
	switch op {
	case OpStatus:
		return Status{Version: "test", Recording: true, Mic: Source{Kind: "mic", Name: "Desk Mic"}}, nil
	case OpRenameSpeaker:
		var a SpeakerArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if a.Session == "missing" {
			return nil, errors.New("no such session")
		}
		return nil, nil
	case OpLearnSaved:
		return Count{N: 3}, nil
	case "panic":
		panic("boom")
	case "slow":
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
		}
		return Count{N: 1}, nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownOp, op)
}

func startServer(t *testing.T) (*Server, *Client) {
	t.Helper()
	srv, err := Listen(SocketPath(socketDir(t)), fakeHandler)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	c := NewClient(srv.Path())
	c.Timeout = 5 * time.Second
	return srv, c
}

func TestServerAnswersThroughClient(t *testing.T) {
	_, c := startServer(t)
	ctx := context.Background()

	var st Status
	if err := c.Call(ctx, OpStatus, nil, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Recording || st.Mic.Name != "Desk Mic" {
		t.Errorf("status = %+v", st)
	}

	if err := c.Call(ctx, OpRenameSpeaker, SpeakerArgs{Session: "s1", Label: "Person 2", Name: "Ana"}, nil); err != nil {
		t.Errorf("rename: %v", err)
	}
	err := c.Call(ctx, OpRenameSpeaker, SpeakerArgs{Session: "missing", Label: "Person 2", Name: "Ana"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no such session") {
		t.Errorf("rename of a missing session: err = %v", err)
	}

	var n Count
	if err := c.Call(ctx, OpLearnSaved, nil, &n); err != nil || n.N != 3 {
		t.Errorf("learn_saved = %+v, %v", n, err)
	}
}

func TestServerUnknownOpAndPanicAreErrors(t *testing.T) {
	_, c := startServer(t)
	ctx := context.Background()
	if err := c.Call(ctx, "no_such_op", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown operation") {
		t.Errorf("unknown op: err = %v", err)
	}
	if err := c.Call(ctx, "panic", nil, nil); err == nil {
		t.Error("a panicking handler gave no error")
	}
	// Still serving after the panic.
	if err := c.Call(ctx, OpStatus, nil, nil); err != nil {
		t.Errorf("after a panic: %v", err)
	}
}

// Raw protocol: replies carry the request's id and v, several requests
// on one connection are answered in order, and a bad line or a newer
// protocol version is an error reply, not a dropped connection.
func TestServerRawProtocol(t *testing.T) {
	srv, _ := startServer(t)
	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, `{"v":1,"id":7,"op":"status"}`+"\n"+
		"not json\n"+
		`{"v":2,"id":8,"op":"status"}`+"\n"+
		`{"v":1,"id":9,"op":"learn_saved"}`+"\n")
	sc := bufio.NewScanner(conn)
	var reps []Reply
	for len(reps) < 4 && sc.Scan() {
		var r Reply
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad reply %q: %v", sc.Text(), err)
		}
		reps = append(reps, r)
	}
	if len(reps) != 4 {
		t.Fatalf("got %d replies: %v", len(reps), sc.Err())
	}
	if r := reps[0]; r.V != Version || r.ID != 7 || !r.OK || len(r.Result) == 0 {
		t.Errorf("status reply = %+v", r)
	}
	if r := reps[1]; r.OK || !strings.Contains(r.Error, "bad request") {
		t.Errorf("bad line reply = %+v", r)
	}
	if r := reps[2]; r.ID != 8 || r.OK || !strings.Contains(r.Error, "version") {
		t.Errorf("v2 reply = %+v", r)
	}
	if r := reps[3]; r.ID != 9 || !r.OK || string(r.Result) != `{"n":3}` {
		t.Errorf("learn_saved reply = %+v (%s)", r, r.Result)
	}
}

func TestServerSocketIsPrivateAndCleanedUp(t *testing.T) {
	srv, _ := startServer(t)
	info, err := os.Stat(srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("socket mode %o, want 600", mode)
	}
	if _, err := Listen(srv.Path(), fakeHandler); err == nil {
		t.Error("a second server on a served socket started")
	}
	srv.Close()
	if _, err := os.Stat(srv.Path()); !os.IsNotExist(err) {
		t.Errorf("socket left after Close: %v", err)
	}
	if err := NewClient(srv.Path()).Call(context.Background(), OpStatus, nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("call after Close: %v, want ErrNotRunning", err)
	}
}

// A socket file left by a recorder that crashed is replaced.
func TestServerReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(socketDir(t), SocketName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	srv, err := Listen(path, fakeHandler)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer srv.Close()
	if err := NewClient(path).Call(context.Background(), OpStatus, nil, nil); err != nil {
		t.Error(err)
	}
}

// Connections are served concurrently: a slow request on one doesn't
// hold up another.
func TestServerConnectionsAreConcurrent(t *testing.T) {
	_, c := startServer(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Call(ctx, "slow", nil, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	began := time.Now()
	if err := c.Call(ctx, OpStatus, nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d > 150*time.Millisecond {
		t.Errorf("status waited %v behind a slow request", d)
	}
	wg.Wait()
}
