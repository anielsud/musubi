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
	"sync"
	"time"
)

// Handler carries out one request: op with its raw args, returning the
// result to encode (nil for none) or an error for the reply.
type Handler func(ctx context.Context, op string, args json.RawMessage) (any, error)

// ErrUnknownOp is what a handler returns for an op it doesn't know.
var ErrUnknownOp = errors.New("unknown operation")

// Server serves the control socket.
type Server struct {
	ln      net.Listener
	path    string
	handler Handler

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// maxRequest bounds one request line: requests are small (ids, names),
// so anything larger is a confused or hostile client.
const maxRequest = 1 << 20

// Listen serves path (see SocketPath) with permissions 0600, calling
// handler for each request. A socket file left by a recorder that didn't
// shut down cleanly is replaced; one still being served is an error.
func Listen(path string, handler Handler) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("control: %w", err)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := listenPrivate(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ln: ln, path: path, handler: handler, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Path is the socket's path.
func (s *Server) Path() string { return s.path }

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			fmt.Printf("control: accept: %v\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

// serve answers one connection's requests in order until it closes.
// Requests on one connection run one at a time, so a client sees its
// replies in the order it asked; separate connections run concurrently.
func (s *Server) serve(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		conn.Close()
	}()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 4096), maxRequest)
	w := bufio.NewWriter(conn)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		rep := s.answer(line)
		b, err := json.Marshal(rep)
		if err != nil {
			b, _ = json.Marshal(Reply{V: Version, ID: rep.ID, Error: "encoding the reply: " + err.Error()})
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

// answer turns one request line into its reply.
func (s *Server) answer(line []byte) Reply {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Reply{V: Version, Error: "bad request: " + err.Error()}
	}
	rep := Reply{V: Version, ID: req.ID}
	if req.V > Version {
		rep.Error = fmt.Sprintf("protocol version %d isn't supported (the recorder speaks %d)", req.V, Version)
		return rep
	}
	if req.Op == "" {
		rep.Error = "no op given"
		return rep
	}
	result, err := s.call(req)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			rep.Error = "encoding the result: " + err.Error()
			return rep
		}
		rep.Result = b
	}
	rep.OK = true
	return rep
}

// call runs the handler, turning a panic into an error reply: one bad
// request must not take the recorder (and a meeting being recorded)
// down with it.
func (s *Server) call(req Request) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Printf("control: %s panicked: %v\n", req.Op, p)
			result, err = nil, fmt.Errorf("%s failed inside the recorder", req.Op)
		}
	}()
	return s.handler(s.ctx, req.Op, req.Args)
}

// Close stops serving, ends open connections (a handler still running
// sees its context cancelled) and removes the socket file.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.cancel()
	err := s.ln.Close()
	s.wg.Wait()
	if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}

// listenPrivate creates the socket in a fresh 0700 directory beside path,
// narrows it to 0600 there and only then renames it into place, so no
// other user can connect even for an instant (net.Listen takes no mode,
// and changing the umask would be process-wide). The live feed does the
// same.
func listenPrivate(path string) (net.Listener, error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".ctl-")
	if err != nil {
		return nil, fmt.Errorf("control: %w", err)
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", tmp)
	if err != nil {
		return nil, fmt.Errorf("control: %w", err)
	}
	// After the rename the original name is gone, so Close removes path
	// itself.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(tmp, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("control: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		ln.Close()
		return nil, fmt.Errorf("control: %w", err)
	}
	return ln, nil
}

// removeStale removes a socket file at path that nothing is serving.
func removeStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("control: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("control: %s exists and isn't a socket", path)
	}
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("control: %s is already being served (is another recorder running?)", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("control: removing stale socket: %w", err)
	}
	return nil
}
