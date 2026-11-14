package phone

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

// The protocol (docs/phone.md, "Protocol"): each message is a 4-byte
// big-endian length, that many bytes of UTF-8 JSON (at most maxJSON), then,
// when the JSON has "body": n, n raw bytes (at most maxBody). The Mac sends
// one request at a time and reads the phone's answer.

const (
	maxJSON = 64 << 10
	maxBody = 64 << 20

	// frameTimeout bounds every frame's header and JSON; a body gets
	// bodyRate on top (a 64 MB chunk over slow venue Wi-Fi).
	frameTimeout = 30 * time.Second
	bodyRate     = 128 << 10 // bytes per second, at least
	// confirmWait: after Allow the phone answers pair_confirm once its
	// owner has tapped Confirm, at most 2 minutes later.
	confirmWait = 2*time.Minute + 15*time.Second
)

// errProtocol: the phone sent something outside the protocol. The
// connection is closed; the detail is only for the log.
var errProtocol = errors.New("protocol error")

func protoErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errProtocol, fmt.Sprintf(format, args...))
}

// phoneError is a reply {"ok": false, "error": code}.
type phoneError struct{ code string }

func (e *phoneError) Error() string { return "the phone answered " + e.code }

// errorCode is a phone's error code fit to log and show: short, plain.
func errorCode(s string) string {
	if s == "" || len(s) > 40 {
		return "unknown"
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return "unknown"
		}
	}
	return strings.ToLower(s)
}

// frameConn is one connection to a phone, with a deadline on every frame.
type frameConn struct {
	c  net.Conn
	br *bufio.Reader
	// pending is the size of the body still to be read from the last
	// reply; nothing else may be read before it.
	pending int64
}

func newFrameConn(c net.Conn) *frameConn {
	return &frameConn{c: c, br: bufio.NewReaderSize(c, 32<<10)}
}

func bodyTime(n int64) time.Duration {
	return time.Duration(n/bodyRate) * time.Second
}

// send writes a request and, when body is not nil, its n bytes.
func (f *frameConn) send(req any, body io.Reader, n int64) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(b) > maxJSON || n < 0 || n > maxBody {
		return errors.New("request too large")
	}
	f.c.SetWriteDeadline(time.Now().Add(frameTimeout + bodyTime(n)))
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := f.c.Write(append(hdr[:], b...)); err != nil {
		return err
	}
	if body != nil {
		if _, err := io.CopyN(f.c, body, n); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads one message's JSON (checked: length, UTF-8, an object)
// within wait. A body it announces is left for readBody.
func (f *frameConn) readFrame(wait time.Duration) ([]byte, error) {
	if f.pending != 0 {
		return nil, errors.New("a body is still unread")
	}
	f.c.SetReadDeadline(time.Now().Add(wait))
	var hdr [4]byte
	if _, err := io.ReadFull(f.br, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n < 2 || n > maxJSON {
		return nil, protoErr("frame of %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(f.br, b); err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		return nil, protoErr("frame isn't UTF-8")
	}
	return b, nil
}

// replyHead is what every reply carries.
type replyHead struct {
	OK    *bool  `json:"ok"`
	Error string `json:"error"`
	Body  *int64 `json:"body"`
}

// recv reads a reply into out (when ok), returning the size of the body
// it announces, which the caller must read with readBody (bodyOK) or
// which is a protocol error. A reply {"ok": false} is a *phoneError.
func (f *frameConn) recv(wait time.Duration, out any, bodyOK bool) (int64, error) {
	b, err := f.readFrame(wait)
	if err != nil {
		return 0, err
	}
	var h replyHead
	if err := json.Unmarshal(b, &h); err != nil {
		return 0, protoErr("reply isn't a JSON object")
	}
	if h.OK == nil {
		return 0, protoErr("reply without ok")
	}
	var n int64
	if h.Body != nil {
		n = *h.Body
		if n < 0 || n > maxBody || !bodyOK || !*h.OK {
			return 0, protoErr("unexpected body of %d bytes", n)
		}
	}
	if !*h.OK {
		return 0, &phoneError{code: errorCode(h.Error)}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return 0, protoErr("reply doesn't fit its op")
		}
	}
	f.pending = n
	return n, nil
}

// readBody copies the pending body to w.
func (f *frameConn) readBody(w io.Writer) error {
	n := f.pending
	f.pending = 0
	f.c.SetReadDeadline(time.Now().Add(frameTimeout + bodyTime(n)))
	_, err := io.CopyN(w, f.br, n)
	return err
}

// call sends a request without a body and reads its reply into out.
func (f *frameConn) call(req any, out any) error {
	if err := f.send(req, nil, 0); err != nil {
		return err
	}
	_, err := f.recv(frameTimeout, out, false)
	return err
}
