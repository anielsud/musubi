package phone

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fakePhone is a phone's side of the protocol (docs/phone.md, "Protocol"):
// a TLS server that requires a client certificate, accepts the pinned
// Mac's for everything and any during its pairing window for the pairing
// ops only, and serves synthetic recordings.
type fakePhone struct {
	t    *testing.T
	cert tls.Certificate
	fp   []byte
	ln   net.Listener
	port uint16

	mu        sync.Mutex
	pinnedMac []byte
	pairing   bool
	// confirm answers pair_confirm: "" (ok) or an error code.
	confirm string
	// hangUpAfterRequest closes the connection after pair_request.
	hangUpAfterRequest bool
	code               string
	cancels            int
	name               string
	addresses          []string
	recs               map[string]*fakeRec
	order              []string
	states             map[string]string
	reports            []map[string]any
	agendas            []Agenda
	ops                []string
	// fault: "rid/n" → "sha" (wrong sha256 in the reply), "bytes" (other
	// bytes), "short" (fewer bytes); dropAfter closes the connection after
	// that many chunks.
	fault     map[string]string
	dropAfter int
	served    int
	conns     int
}

type fakeRec struct {
	meta   []byte
	chunks [][]byte
	files  map[string][]byte
}

func selfSigned(t *testing.T, cn string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, sum[:]
}

func newFakePhone(t *testing.T) *fakePhone {
	t.Helper()
	cert, fp := selfSigned(t, "fake phone")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePhone{t: t, cert: cert, fp: fp, ln: ln, port: uint16(ln.Addr().(*net.TCPAddr).Port), name: "Test phone",
		addresses: []string{"192.168.1.20"}, recs: map[string]*fakeRec{}, states: map[string]string{}, fault: map[string]string{}}
	go p.serve()
	t.Cleanup(func() { ln.Close() })
	return p
}

func (p *fakePhone) fpHex() string { return hex.EncodeToString(p.fp) }

func (p *fakePhone) addr() netip.AddrPort {
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), p.port)
}

func (p *fakePhone) add(rid string, chunks ...[]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recs[rid] = &fakeRec{meta: metaJSON(rid, chunks...), chunks: chunks}
	p.order = append(p.order, rid)
}

func (p *fakePhone) opList() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.ops...)
}

func (p *fakePhone) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func writeMsg(w io.Writer, v any, body []byte) error {
	b, _ := json.Marshal(v)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(append(hdr[:], b...)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func readMsg(r io.Reader) (map[string]any, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxJSON {
		return nil, nil, fmt.Errorf("frame of %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, nil, err
	}
	var body []byte
	if f, ok := m["body"].(float64); ok {
		body = make([]byte, int(f))
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, nil, err
		}
	}
	return m, body, nil
}

func (p *fakePhone) handle(raw net.Conn) {
	defer raw.Close()
	// As the app does: a client certificate other than the pinned one
	// fails the handshake (an alert), except while pairing is open.
	c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{p.cert}, ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12,
		VerifyPeerCertificate: func(certs [][]byte, _ [][]*x509.Certificate) error {
			sum := sha256.Sum256(certs[0])
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.pairing || (p.pinnedMac != nil && bytes.Equal(p.pinnedMac, sum[:])) {
				return nil
			}
			return fmt.Errorf("not the paired Mac")
		}})
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	if err := c.Handshake(); err != nil {
		return
	}
	raw.SetDeadline(time.Time{})
	mac := sha256.Sum256(c.ConnectionState().PeerCertificates[0].Raw)
	p.mu.Lock()
	p.conns++
	paired := p.pinnedMac != nil && bytes.Equal(p.pinnedMac, mac[:])
	pairing := p.pairing
	p.mu.Unlock()
	if !paired && !pairing {
		return
	}
	var macNonce []byte
	for {
		m, body, err := readMsg(c)
		if err != nil {
			return
		}
		op, _ := m["op"].(string)
		p.mu.Lock()
		p.ops = append(p.ops, op)
		p.mu.Unlock()
		ok := func(kv ...any) error {
			r := map[string]any{"ok": true}
			for i := 0; i+1 < len(kv); i += 2 {
				r[kv[i].(string)] = kv[i+1]
			}
			return writeMsg(c, r, nil)
		}
		fail := func(code string) error { return writeMsg(c, map[string]any{"ok": false, "error": code}, nil) }
		pairOp := op == "pair_request" || op == "pair_confirm" || op == "pair_cancel"
		if !paired && !pairOp {
			fail("not_paired")
			return
		}
		switch op {
		case "pair_request":
			macNonce, _ = base64.StdEncoding.DecodeString(m["nonce"].(string))
			phoneNonce := make([]byte, 16)
			rand.Read(phoneNonce)
			p.mu.Lock()
			p.code = PairCode(mac[:], p.fp, macNonce, phoneNonce)
			hang := p.hangUpAfterRequest
			p.mu.Unlock()
			ok("device_name", p.name, "nonce", base64.StdEncoding.EncodeToString(phoneNonce))
			if hang {
				return
			}
		case "pair_confirm":
			p.mu.Lock()
			code := p.confirm
			if code == "" {
				p.pinnedMac, p.pairing = mac[:], false
			}
			p.mu.Unlock()
			if code != "" {
				fail(code)
			} else {
				ok()
			}
			return
		case "pair_cancel":
			p.mu.Lock()
			p.cancels++
			p.mu.Unlock()
			ok()
			return
		case "hello":
			if m["v"] != float64(2) || m["mac_name"] == "" {
				return
			}
			ok("v", 2, "device_name", p.name, "app_version", "0.3.0", "addresses", p.addresses)
		case "list":
			p.mu.Lock()
			var recs []map[string]any
			for _, rid := range p.order {
				var st any
				if s, ok := p.states[rid]; ok {
					st = s
				}
				recs = append(recs, map[string]any{"meta": json.RawMessage(p.recs[rid].meta), "state": st})
			}
			p.mu.Unlock()
			ok("recordings", recs)
		case "chunk":
			rid, _ := m["rid"].(string)
			n := int(m["n"].(float64))
			p.mu.Lock()
			r := p.recs[rid]
			if r == nil || n >= len(r.chunks) {
				p.mu.Unlock()
				fail("unknown_recording")
				continue
			}
			b := append([]byte{}, r.chunks[n]...)
			f := p.fault[fmt.Sprintf("%s/%d", rid, n)]
			p.served++
			drop := p.dropAfter > 0 && p.served >= p.dropAfter
			p.mu.Unlock()
			hash := sum(b)
			switch f {
			case "sha":
				hash = sum([]byte("other"))
			case "bytes":
				b[len(b)-1] ^= 0xFF
			case "short":
				b = b[:len(b)-3]
			}
			writeMsg(c, map[string]any{"ok": true, "sha256": hash, "body": len(b)}, b)
			if drop {
				return
			}
		case "file":
			rid, _ := m["rid"].(string)
			name, _ := m["name"].(string)
			p.mu.Lock()
			r := p.recs[rid]
			var b []byte
			if r != nil {
				b = append([]byte{}, r.files[name]...)
			}
			f := p.fault[rid+"/"+name]
			p.mu.Unlock()
			if r == nil || r.files[name] == nil || f == "gone" {
				fail("gone")
				continue
			}
			if f == "bytes" {
				b[0] ^= 0xFF
			}
			writeMsg(c, map[string]any{"ok": true, "sha256": sum(r.files[name]), "body": len(b)}, b)
		case "state":
			p.mu.Lock()
			p.reports = append(p.reports, m)
			p.states[m["rid"].(string)] = m["state"].(string)
			p.mu.Unlock()
			ok()
		case "agenda":
			var a Agenda
			if err := json.Unmarshal(body, &a); err != nil {
				return
			}
			p.mu.Lock()
			p.agendas = append(p.agendas, a)
			p.mu.Unlock()
			ok()
		case "bye":
			ok()
			return
		default:
			return
		}
	}
}
