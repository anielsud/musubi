// Package phone is the Musubi app's phone access server (docs/phone.md,
// part 2): over HTTPS, from private addresses only, paired phones upload
// the meetings they recorded and fetch the owner's agenda with its briefs.
// It is off unless [phone] enabled is set.
package phone

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// DefaultPort is the port the server listens on unless [phone] port says
// otherwise.
const DefaultPort = 47615

// Config is what the server needs from the app.
type Config struct {
	// Dir is the server's folder in the Musubi data folder: certificate,
	// devices, recordings.
	Dir  string
	Port int
	// RecorderData is the recorder's data folder, whose inbox the joined
	// audio is written to.
	RecorderData string
	// Import asks the recorder to import (control.OpImport); an error
	// wrapping control.ErrNotRunning leaves the recording waiting.
	Import func(ctx context.Context, args control.ImportArgs) (control.ImportStatus, error)
	// Agenda lists the events in the next days days; Drafting starts the
	// briefs missing up to until drafting in the background (unless a
	// meeting is live) and says how many are left.
	Agenda   func(ctx context.Context, days int) ([]Event, error)
	Drafting func(until time.Time) int
	// Logf logs (never a token or a code); OnChange tells Settings that
	// the devices or the pairing window changed.
	Logf     func(format string, args ...any)
	OnChange func()
	Now      func() time.Time
}

// Server is the phone access server. Its devices and pairing work
// whether or not it is listening.
type Server struct {
	cfg  Config
	cert tls.Certificate
	fp   string
	devs *devices

	recMu sync.Mutex
	recs  map[string]*sync.Mutex

	refusedLog, authLog everyMinute
}

// New opens the server's folder, making the certificate on first use.
func New(cfg Config) (*Server, error) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func() {}
	}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "recordings"), 0o700); err != nil {
		return nil, err
	}
	cert, err := loadOrCreateCert(cfg.Dir, cfg.Now())
	if err != nil {
		return nil, err
	}
	devs, err := openDevices(cfg.Dir)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, cert: cert, fp: Fingerprint(cert), devs: devs, recs: map[string]*sync.Mutex{}}, nil
}

// Fingerprint is the certificate's SHA-256 fingerprint (hex).
func (s *Server) Fingerprint() string { return s.fp }

// Devices lists the paired phones.
func (s *Server) Devices() []Device { return s.devs.List() }

// Pair opens a pairing window and returns what Settings shows.
func (s *Server) Pair() (Pairing, error) {
	code, exp, err := s.devs.open(s.cfg.Now())
	if err != nil {
		return Pairing{}, err
	}
	s.cfg.Logf("phone: pairing window opened (5 minutes)")
	return Pairing{Code: code, Expires: exp, Fingerprint: ShortFingerprint(s.fp), Addresses: LocalAddresses(), Port: s.cfg.Port}, nil
}

// CancelPairing closes the pairing window.
func (s *Server) CancelPairing() { s.devs.close() }

// PairingOpen reports whether a pairing window is open, and until when.
func (s *Server) PairingOpen() (bool, time.Time) { return s.devs.windowOpen(s.cfg.Now()) }

// Revoke unpairs a device.
func (s *Server) Revoke(id string) error {
	d, err := s.devs.revoke(id)
	if err != nil {
		return err
	}
	s.cfg.Logf("phone: device %s (%s) revoked", d.ID, d.Name)
	s.cfg.OnChange()
	return nil
}

// Serve listens on the port until ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(s.cfg.Port))
	if err != nil {
		return err
	}
	return s.serveOn(ctx, ln)
}

func (s *Server) serveOn(ctx context.Context, ln net.Listener) error {
	pl := privateListener{Listener: ln, refused: func(a net.Addr) {
		if s.refusedLog.ok(s.cfg.Now()) {
			s.cfg.Logf("phone: refused a connection from %s (not a private address)", a)
		}
	}}
	tl := tls.NewListener(pl, &tls.Config{
		Certificates: []tls.Certificate{s.cert},
		MinVersion:   tls.VersionTLS12,
	})
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// A 64 MB chunk over a slow venue Wi-Fi link.
		ReadTimeout:    15 * time.Minute,
		WriteTimeout:   15 * time.Minute,
		IdleTimeout:    time.Minute,
		MaxHeaderBytes: 16 << 10,
		// TLS handshake noise from scanners stays out of the app's log.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	s.cfg.Logf("phone: listening on port %d", s.cfg.Port)
	err := srv.Serve(tl)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type ctxKey struct{}

// Handler is the API, for Serve and for tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.Handle("GET /v1/agenda", s.authed(s.handleAgenda))
	mux.Handle("PUT /v1/recordings/{rid}/chunks/{n}", s.authed(s.handleChunk))
	mux.Handle("POST /v1/recordings/{rid}/complete", s.authed(s.handleComplete))
	mux.Handle("GET /v1/recordings/{rid}", s.authed(s.handleState))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

// authed requires a paired device's bearer token.
func (s *Server) authed(next func(http.ResponseWriter, *http.Request, Device)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		dev, found := Device{}, false
		if ok {
			dev, found = s.devs.auth(strings.TrimSpace(tok), s.cfg.Now())
		}
		if !found {
			if s.authLog.ok(s.cfg.Now()) {
				s.cfg.Logf("phone: refused a request without a valid token")
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="musubi"`)
			writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
			return
		}
		next(w, r, dev)
	})
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// readBody reads at most limit bytes of r's body; more is an error.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if r.ContentLength > limit {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody("too large"))
		return nil, false
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody("too large"))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody("bad request"))
		}
		return nil, false
	}
	return b, true
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, 4<<10)
	if !ok {
		return
	}
	var req struct {
		Code       string `json:"code"`
		DeviceName string `json:"device_name"`
	}
	if json.Unmarshal(b, &req) != nil {
		writeJSON(w, http.StatusBadRequest, errBody("bad request"))
		return
	}
	dev, token, err := s.devs.pair(strings.TrimSpace(req.Code), req.DeviceName, s.cfg.Now())
	switch {
	case errors.Is(err, errPairLocked):
		s.cfg.Logf("phone: pairing window closed after %d wrong codes", pairMaxWrong)
		s.cfg.OnChange()
		writeJSON(w, http.StatusForbidden, errBody("pairing failed"))
	case errors.Is(err, errPairWrong):
		s.cfg.Logf("phone: wrong pairing code")
		writeJSON(w, http.StatusForbidden, errBody("pairing failed"))
	case errors.Is(err, errPairClosed):
		writeJSON(w, http.StatusForbidden, errBody("pairing failed"))
	case err != nil:
		s.cfg.Logf("phone: pairing: %v", err)
		writeJSON(w, http.StatusInternalServerError, errBody("server error"))
	default:
		s.cfg.Logf("phone: paired device %s (%s)", dev.ID, dev.Name)
		s.cfg.OnChange()
		writeJSON(w, http.StatusOK, map[string]string{"device_id": dev.ID, "token": token})
	}
}

func (s *Server) handleAgenda(w http.ResponseWriter, r *http.Request, _ Device) {
	days := defaultDays
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || len(v) > 4 {
			writeJSON(w, http.StatusBadRequest, errBody("bad days"))
			return
		}
		days = clampDays(n)
	}
	if s.cfg.Agenda == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("not ready"))
		return
	}
	evs, err := s.cfg.Agenda(r.Context(), days)
	if err != nil {
		s.cfg.Logf("phone: agenda: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, errBody("not ready"))
		return
	}
	out := Agenda{Events: evs}
	if s.cfg.Drafting != nil {
		out.Drafting = s.cfg.Drafting(s.cfg.Now().Add(time.Duration(days) * 24 * time.Hour))
	}
	writeJSON(w, http.StatusOK, out)
}

// recLock serializes the requests on one recording.
func (s *Server) recLock(rid string) func() {
	s.recMu.Lock()
	m := s.recs[rid]
	if m == nil {
		m = &sync.Mutex{}
		s.recs[rid] = m
	}
	s.recMu.Unlock()
	m.Lock()
	return m.Unlock
}

func (s *Server) recDir(rid string) string { return filepath.Join(s.cfg.Dir, "recordings", rid) }

// ownRecording is rid's state when dev made it; nil (and a 404 written)
// for one that isn't there or is another device's.
func (s *Server) ownRecording(w http.ResponseWriter, rid string, dev Device) *recState {
	st, err := readState(s.recDir(rid))
	if err != nil || st.Device != dev.ID {
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return nil
	}
	return st
}

func (s *Server) handleChunk(w http.ResponseWriter, r *http.Request, dev Device) {
	rid, ok1 := parseRID(r.PathValue("rid"))
	n, ok2 := parseN(r.PathValue("n"))
	if !ok1 || !ok2 {
		writeJSON(w, http.StatusBadRequest, errBody("bad recording or chunk"))
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "audio/aac" {
		writeJSON(w, http.StatusUnsupportedMediaType, errBody("audio/aac only"))
		return
	}
	want := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Sha256")))
	if !shaPattern.MatchString(want) {
		writeJSON(w, http.StatusBadRequest, errBody("X-Sha256 needed"))
		return
	}
	if r.ContentLength > maxChunkBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody("too large"))
		return
	}
	dir := s.recDir(rid)
	path := filepath.Join(dir, chunkName(n))

	unlock := s.recLock(rid)
	st, err := readState(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		st = &recState{Device: dev.ID, State: StateReceived, Updated: s.cfg.Now()}
		err := os.MkdirAll(dir, 0o700)
		if err == nil {
			err = writeState(dir, st)
		}
		if err != nil {
			unlock()
			s.fail(w, "storing a chunk", err)
			return
		}
	case err != nil || st.Device != dev.ID:
		unlock()
		writeJSON(w, http.StatusNotFound, errBody("not found"))
		return
	}
	if done, same := s.haveChunk(path, want); done {
		unlock()
		if same {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, http.StatusConflict, errBody("a different chunk is already stored"))
		}
		return
	}
	if st.State != StateReceived && st.State != control.ImportFailed {
		unlock()
		writeJSON(w, http.StatusConflict, errBody("recording already complete"))
		return
	}
	unlock()

	// The body goes to a temporary file outside the lock (it can take
	// minutes), hashed as it arrives.
	tmp, err := os.CreateTemp(dir, ".chunk-*")
	if err != nil {
		s.fail(w, "storing a chunk", err)
		return
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), http.MaxBytesReader(w, r.Body, maxChunkBytes))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody("too large"))
		} else {
			writeJSON(w, http.StatusBadRequest, errBody("upload interrupted"))
		}
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		writeJSON(w, http.StatusBadRequest, errBody("X-Sha256 doesn't match the body"))
		return
	}
	defer s.recLock(rid)()
	if done, same := s.haveChunk(path, want); done {
		// Another upload of the same chunk got there first.
		if same {
			w.WriteHeader(http.StatusNoContent)
		} else {
			writeJSON(w, http.StatusConflict, errBody("a different chunk is already stored"))
		}
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		s.fail(w, "storing a chunk", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// haveChunk reports whether the chunk is stored, and whether with the
// given hash.
func (s *Server) haveChunk(path, want string) (exists, same bool) {
	sum, _, err := fileSHA256(path)
	if err != nil {
		return false, false
	}
	return true, sum == want
}

// fail logs what went wrong and tells the phone only that it did.
func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	s.cfg.Logf("phone: %s: %v", what, err)
	writeJSON(w, http.StatusInternalServerError, errBody("server error"))
}

// stateReply is GET /v1/recordings/{rid} and complete's reply.
type stateReply struct {
	State   string `json:"state"`
	Session string `json:"session_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

func reply(st *recState) stateReply {
	r := stateReply{State: st.State, Error: st.Error}
	if st.State == control.ImportImported {
		r.Session = st.Session
	}
	return r
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request, dev Device) {
	rid, ok := parseRID(r.PathValue("rid"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("bad recording"))
		return
	}
	b, ok := readBody(w, r, maxMetaBytes)
	if !ok {
		return
	}
	defer s.recLock(rid)()
	st := s.ownRecording(w, rid, dev)
	if st == nil {
		return
	}
	switch st.State {
	case control.ImportWaiting, control.ImportTranscribing, control.ImportImported:
		writeJSON(w, http.StatusAccepted, reply(st)) // asked again: nothing to redo
		return
	}
	m, start, chunks, err := parseMeta(b, rid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	dir := s.recDir(rid)
	bad, err := verifyChunks(dir, chunks)
	if err != nil {
		s.fail(w, "checking chunks", err)
		return
	}
	if len(bad) > 0 {
		s.cfg.Logf("phone: recording %s: %d chunks missing or damaged", rid, len(bad))
		writeJSON(w, http.StatusConflict, map[string]any{"error": "chunks missing or damaged", "chunks": bad})
		return
	}
	if err := writePrivate(filepath.Join(dir, metaFile), b); err != nil {
		s.fail(w, "storing meta.json", err)
		return
	}
	inbox := filepath.Join(control.InboxDir(s.cfg.RecorderData), rid+".aac")
	frames, err := joinChunks(dir, chunks, inbox)
	if errors.Is(err, errNotADTS) {
		s.cfg.Logf("phone: recording %s: %v", rid, err)
		st.State, st.Error, st.Updated = control.ImportFailed, "the audio isn't ADTS AAC", s.cfg.Now()
		writeState(dir, st)
		writeJSON(w, http.StatusAccepted, reply(st))
		return
	}
	if err != nil {
		s.fail(w, "joining chunks", err)
		return
	}
	st.StartedAt, st.Inbox, st.Error = start, inbox, ""
	st.EventID = ""
	if m.Event != nil {
		st.EventID = m.Event.ID
	}
	s.cfg.Logf("phone: recording %s complete: %d chunks, %d frames; importing", rid, len(chunks), frames)
	s.askImport(r.Context(), rid, st, true)
	writeJSON(w, http.StatusAccepted, reply(st))
}

// askImport asks the recorder for rid's import (queueing it, or with
// retry false only reading where it stands) and records the answer.
// Caller holds rid's lock.
func (s *Server) askImport(ctx context.Context, rid string, st *recState, retry bool) {
	dir := s.recDir(rid)
	prev := st.State
	if s.cfg.Import == nil {
		st.State = control.ImportWaiting
	} else {
		is, err := s.cfg.Import(ctx, control.ImportArgs{ID: rid, Path: st.Inbox, StartedAt: st.StartedAt, EventID: st.EventID, Retry: retry})
		switch {
		case errors.Is(err, control.ErrNotRunning):
			st.State = control.ImportWaiting // the recorder takes it when it is back
		case err != nil:
			s.cfg.Logf("phone: recording %s: import: %v", rid, err)
			st.State, st.Error = control.ImportFailed, "the recorder refused the import"
		default:
			st.State, st.Session = is.State, is.Session
			st.Error = ""
			if is.State == control.ImportFailed {
				st.Error = is.Error
			}
		}
	}
	st.Updated = s.cfg.Now()
	if st.State == control.ImportImported {
		dropChunks(dir)
		if prev != control.ImportImported {
			s.cfg.Logf("phone: recording %s imported as session %s", rid, st.Session)
		}
	}
	if st.State == control.ImportFailed && prev != control.ImportFailed {
		s.cfg.Logf("phone: recording %s failed", rid)
	}
	if err := writeState(dir, st); err != nil {
		s.cfg.Logf("phone: recording %s: state not saved: %v", rid, err)
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request, dev Device) {
	rid, ok := parseRID(r.PathValue("rid"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("bad recording"))
		return
	}
	defer s.recLock(rid)()
	st := s.ownRecording(w, rid, dev)
	if st == nil {
		return
	}
	if st.State == control.ImportWaiting || st.State == control.ImportTranscribing {
		s.askImport(r.Context(), rid, st, false)
	}
	writeJSON(w, http.StatusOK, reply(st))
}
