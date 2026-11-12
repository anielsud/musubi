package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// ErrNotRunning means nothing is serving the control socket: the
// recorder is not running.
var ErrNotRunning = errors.New("the recorder is not running")

// Client calls the recorder. It dials per call, so a recorder restart
// never leaves it holding a dead connection; calls are rare and local.
type Client struct {
	Path    string
	Timeout time.Duration // per call; 30 s if 0 (deleting or learning can take a while)

	mu   sync.Mutex
	next int64
}

func NewClient(path string) *Client { return &Client{Path: path} }

// Call sends op with args (nil for none) and decodes the result into out
// (nil to discard it).
func (c *Client) Call(ctx context.Context, op string, args, out any) error {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return ErrNotRunning
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	c.mu.Lock()
	c.next++
	id := c.next
	c.mu.Unlock()

	req := Request{V: Version, ID: id, Op: op}
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		req.Args = b
	}
	line, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("recorder: %w", err)
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20) // clips and tiles come back inline
	for sc.Scan() {
		var rep Reply
		if err := json.Unmarshal(sc.Bytes(), &rep); err != nil {
			return fmt.Errorf("recorder: bad reply: %w", err)
		}
		if rep.ID != id {
			continue
		}
		if !rep.OK {
			return fmt.Errorf("recorder: %s", rep.Error)
		}
		if out == nil || len(rep.Result) == 0 {
			return nil
		}
		return json.Unmarshal(rep.Result, out)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("recorder: %w", err)
	}
	return errors.New("recorder: closed without a reply")
}
