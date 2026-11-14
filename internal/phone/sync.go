package phone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/phonedetails"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// A sync (docs/phone.md, part 2, "A sync"): hello → list → for each
// recording the Mac lacks or hasn't finished, fetch the missing chunks,
// join them into the recorder's inbox and ask the recorder to import it →
// state for each recording whose state changed → agenda → bye.

const (
	maxListed     = 1000
	maxReported   = 64
	maxAppVersion = 40
)

// Agenda is what the agenda op pushes.
type Agenda struct {
	Events   []Event   `json:"events"`
	Drafting int       `json:"drafting"`
	MadeAt   time.Time `json:"made_at"`
}

// errDamaged: a chunk arrived other than meta.json says, or the phone
// couldn't send it. The recording is fetched again at the next sync.
type errDamaged struct {
	n    int
	code string
}

func (e *errDamaged) Error() string {
	if e.code != "" {
		return fmt.Sprintf("the phone couldn't send chunk %d (%s)", e.n, e.code)
	}
	return fmt.Sprintf("chunk %d arrived damaged", e.n)
}

// syncStats is a sync's outcome, for the log and Settings.
type syncStats struct {
	recordings, fetched, imported, skipped, events int
	agendaSent, agendaErr                          bool
}

func (st syncStats) String() string {
	s := fmt.Sprintf("%d recordings on the phone, %d fetched, %d imported", st.recordings, st.fetched, st.imported)
	if st.skipped > 0 {
		s += fmt.Sprintf(", %d unreadable", st.skipped)
	}
	switch {
	case st.agendaSent:
		return s + fmt.Sprintf("; agenda of %d events sent", st.events)
	case st.agendaErr:
		return s + "; agenda not sent"
	}
	return s
}

// sync runs a sync on fc with the paired phone fp.
func (s *Syncer) sync(ctx context.Context, fc *frameConn, fp string) {
	fp16 := fp[:16]
	now := s.cfg.Now()
	s.mu.Lock()
	s.synced[fp16] = now
	s.mu.Unlock()
	name := "phone"
	if d, ok := s.pins.get(fp); ok {
		name = d.Name
	}
	var stats syncStats
	err := s.syncSteps(ctx, fc, fp, &name, &stats)
	outcome := "synced: " + stats.String()
	if refusedByPhone(err) {
		s.phoneRefused(fp, name, err)
		return
	}
	if err != nil {
		var pe *phoneError
		if errors.As(err, &pe) {
			outcome = "failed: the phone answered " + pe.code
		} else if errors.Is(err, errProtocol) {
			outcome = "failed: the phone's answer didn't fit the protocol"
		} else {
			outcome = "failed: the connection broke off"
		}
		s.cfg.Logf("phone: sync with %q stopped: %v (%s)", name, err, stats)
	} else {
		s.cfg.Logf("phone: synced with %q: %s", name, stats)
	}
	s.pins.update(fp, func(d *Device) {
		d.LastSync, d.LastOutcome = now, outcome
	})
	s.cfg.OnChange()
}

// refusedOutcome: the phone no longer trusts this Mac (unpaired there,
// or paired with another Mac); only pairing again helps.
const refusedOutcome = "refused by the phone: it no longer has this Mac paired (pair again)"

// refusedByPhone reports whether err is the phone refusing this Mac: a TLS
// alert (with TLS 1.3 the phone checks the Mac's certificate after the
// Mac's side of the handshake is done, so the alert arrives on the first
// read), or "not_paired" from a phone that has pairing open.
func refusedByPhone(err error) bool {
	if err == nil {
		return false
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Op == "remote error" {
		return true
	}
	var pe *phoneError
	return errors.As(err, &pe) && pe.code == "not_paired"
}

// phoneRefused records a refusal: Settings says so, and the log says why.
func (s *Syncer) phoneRefused(fp, name string, err error) {
	s.cfg.Logf("phone: %q refused this Mac (%v): it no longer has this Mac paired; pair again", name, err)
	s.pins.update(fp, func(d *Device) { d.LastOutcome = refusedOutcome })
	s.cfg.OnChange()
}

func (s *Syncer) syncSteps(ctx context.Context, fc *frameConn, fp string, name *string, stats *syncStats) error {
	var hello struct {
		V          int      `json:"v"`
		DeviceName string   `json:"device_name"`
		AppVersion string   `json:"app_version"`
		Addresses  []string `json:"addresses"`
	}
	if err := fc.call(map[string]any{"op": "hello", "v": 2, "mac_name": s.cfg.MacName}, &hello); err != nil {
		return err
	}
	if hello.V != 2 || len(hello.Addresses) > maxReported {
		return protoErr("hello v%d with %d addresses", hello.V, len(hello.Addresses))
	}
	*name = cleanName(hello.DeviceName)
	addrs := reportedAddrs(hello.Addresses)
	s.pins.update(fp, func(d *Device) {
		d.Name = *name
		if len(addrs) > 0 {
			d.Addresses = addrs
		}
	})

	var list struct {
		Recordings []struct {
			Meta  json.RawMessage `json:"meta"`
			State *string         `json:"state"`
		} `json:"recordings"`
		// TooLarge: recordings whose meta.json alone is more than a frame;
		// the phone skips them rather than stop listing there.
		TooLarge []string `json:"too_large"`
	}
	if err := fc.call(map[string]any{"op": "list"}, &list); err != nil {
		return err
	}
	if len(list.Recordings) > maxListed {
		return protoErr("%d recordings listed", len(list.Recordings))
	}
	stats.recordings = len(list.Recordings)
	if len(list.TooLarge) > maxReported {
		return protoErr("%d recordings too large", len(list.TooLarge))
	}
	for _, r := range list.TooLarge {
		if id, ok := parseRID(r); ok {
			s.cfg.Logf("phone: recording %s is too large for the phone to list (its meta.json is over 64 KB); skipped", id)
		}
	}
	for _, r := range list.Recordings {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		phoneState := ""
		if r.State != nil {
			phoneState = *r.State
		}
		if err := s.syncRecording(ctx, fc, fp, r.Meta, phoneState, stats); err != nil {
			return err
		}
	}

	if err := s.pushAgenda(ctx, fc, stats); err != nil {
		return err
	}
	return fc.call(map[string]any{"op": "bye"}, nil)
}

// validState: the states the Mac reports.
func validState(st string) bool {
	switch st {
	case StateReceived, control.ImportWaiting, control.ImportTranscribing, control.ImportImported, control.ImportFailed:
		return true
	}
	return false
}

// syncRecording brings one listed recording forward and reports its state
// when the phone's differs. Only a broken connection is an error.
func (s *Syncer) syncRecording(ctx context.Context, fc *frameConn, fp string, raw json.RawMessage, phoneState string, stats *syncStats) error {
	m, start, chunks, err := parseMeta(raw, "")
	if err != nil {
		stats.skipped++
		s.cfg.Logf("phone: a listed recording was skipped: %v", err)
		return nil
	}
	if !validState(phoneState) {
		phoneState = ""
	}
	var details *phonedetails.MetaDetails
	if s.cfg.Details {
		if details, err = metaDetails(raw); err != nil {
			s.cfg.Logf("phone: recording %s: details skipped: %v", m.ID, err)
		}
	}
	if phoneState == control.ImportImported && details == nil {
		return nil // reported before; the phone drops the audio
	}
	rid := m.ID
	defer s.recLock(rid)()
	dir := s.recDir(rid)
	st, err := readState(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.cfg.Logf("phone: recording %s: state unreadable: %v", rid, err)
		return nil
	}
	if st != nil && st.Phone != "" && st.Phone != fp {
		s.cfg.Logf("phone: recording %s is another phone's; skipped", rid)
		return nil
	}
	eventID := ""
	if m.Event != nil {
		eventID = m.Event.ID
	}
	st, err = s.advance(ctx, fc, fp, rid, start, eventID, chunks, st, stats)
	if err != nil {
		return err
	}
	if st.State == control.ImportImported && phoneState != control.ImportImported {
		stats.imported++
	}
	// The phone lists an imported recording again only for newer details.
	detailsDone := false
	if details != nil {
		if detailsDone, err = s.applyDetails(fc, rid, st, details); err != nil {
			return err
		}
	}
	if st.State == "" || (st.State == phoneState && !detailsDone) {
		return nil
	}
	req := map[string]any{"op": "state", "rid": rid, "state": st.State}
	if detailsDone {
		req["details_rev"] = st.DetailsRev
	}
	if st.State == control.ImportImported && st.Session != "" {
		req["session_id"] = st.Session
	}
	if st.State == control.ImportFailed && st.Error != "" {
		req["error"] = st.Error
	}
	err = fc.call(req, nil)
	var pe *phoneError
	if errors.As(err, &pe) {
		s.cfg.Logf("phone: recording %s: state not taken (%s)", rid, pe.code)
		return nil
	}
	return err
}

// advance moves a recording on as far as it goes now: fetch, join, import,
// or ask the recorder how its import is going. st is nil for one the Mac
// hasn't seen.
func (s *Syncer) advance(ctx context.Context, fc *frameConn, fp, rid string, start time.Time, eventID string, chunks []metaChunk, st *recState, stats *syncStats) (*recState, error) {
	dir := s.recDir(rid)
	inbox := filepath.Join(control.InboxDir(s.cfg.RecorderData), rid+".aac")
	if st == nil {
		st = &recState{Phone: fp}
		// Imported before, its staging folder since lost: the recorder
		// says so without anything to import (no inbox file to queue).
		if _, err := os.Lstat(inbox); errors.Is(err, fs.ErrNotExist) && s.cfg.Import != nil {
			is, err := s.cfg.Import(ctx, control.ImportArgs{ID: rid, Path: inbox, StartedAt: start, EventID: eventID})
			if err == nil && is.State == control.ImportImported {
				st.State, st.Session = control.ImportImported, is.Session
			}
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		s.save(rid, st)
	}
	st.Phone = fp
	switch {
	case st.State == control.ImportImported:
		return st, nil
	case st.State == control.ImportWaiting || st.State == control.ImportTranscribing:
		s.askImport(ctx, rid, st)
		return st, nil
	case st.State == control.ImportFailed && !st.Retry:
		return st, nil
	}

	n, err := s.fetch(fc, rid, dir, chunks)
	stats.fetched += min(n, 1)
	var dmg *errDamaged
	if errors.As(err, &dmg) {
		s.cfg.Logf("phone: recording %s: %v", rid, err)
		st.State, st.Error, st.Retry = control.ImportFailed, dmg.Error(), true
		s.save(rid, st)
		return st, nil
	}
	if err != nil {
		return nil, err // resumed at the next sync
	}
	st.State, st.Error, st.Retry = StateReceived, "", false
	st.StartedAt, st.EventID = start, eventID
	s.save(rid, st)

	frames, err := joinChunks(dir, chunks, inbox)
	if err != nil {
		s.cfg.Logf("phone: recording %s: joining: %v", rid, err)
		st.State, st.Error, st.Retry = control.ImportFailed, "the audio couldn't be joined", true
		if errors.Is(err, errNotADTS) {
			st.Error, st.Retry = "the audio isn't ADTS AAC", false
		}
		s.save(rid, st)
		return st, nil
	}
	st.Inbox = inbox
	s.cfg.Logf("phone: recording %s: %d chunks, %d frames; importing", rid, len(chunks), frames)
	s.askImport(ctx, rid, st)
	return st, nil
}

func (s *Syncer) save(rid string, st *recState) {
	st.Updated = s.cfg.Now()
	if err := writeState(s.recDir(rid), st); err != nil {
		s.cfg.Logf("phone: recording %s: state not saved: %v", rid, err)
	}
}

// askImport asks the recorder for rid's import (queueing it the first
// time, otherwise reading where it stands) and records the answer.
func (s *Syncer) askImport(ctx context.Context, rid string, st *recState) {
	prev := st.State
	if s.cfg.Import == nil {
		st.State = control.ImportWaiting
	} else {
		is, err := s.cfg.Import(ctx, control.ImportArgs{ID: rid, Path: st.Inbox, StartedAt: st.StartedAt, EventID: st.EventID})
		switch {
		case errors.Is(err, control.ErrNotRunning):
			st.State = control.ImportWaiting // the recorder takes it when it is back
		case err != nil:
			s.cfg.Logf("phone: recording %s: import: %v", rid, err)
			st.State, st.Error, st.Retry = control.ImportFailed, "the recorder refused the import", true
		default:
			if !validState(is.State) || is.State == StateReceived {
				is.State = control.ImportWaiting
			}
			st.State, st.Session, st.Error, st.Retry = is.State, is.Session, "", false
			if is.State == control.ImportFailed {
				// The recorder's reason stays in the log; the phone is
				// told only that it failed.
				s.cfg.Logf("phone: recording %s: the recorder's import failed: %s", rid, cleanText(is.Error, maxOutcome))
				st.Error = "the import failed"
			}
		}
	}
	if st.State == control.ImportImported {
		dropChunks(s.recDir(rid))
		if prev != control.ImportImported {
			s.cfg.Logf("phone: recording %s imported as session %s", rid, st.Session)
		}
	}
	if st.State == control.ImportFailed && prev != control.ImportFailed {
		s.cfg.Logf("phone: recording %s failed", rid)
	}
	s.save(rid, st)
}

// fetch gets the chunks not yet staged, each checked against meta.json's
// size and SHA-256 before it is kept; it returns how many it fetched.
// Chunks fetched before an interruption are kept.
func (s *Syncer) fetch(fc *frameConn, rid, dir string, chunks []metaChunk) (int, error) {
	fetched := 0
	for _, c := range chunks {
		path := filepath.Join(dir, chunkName(c.n))
		if sum, size, err := fileSHA256(path); err == nil && sum == c.sum && size == c.size {
			continue
		}
		os.Remove(path)
		if err := fc.send(map[string]any{"op": "chunk", "rid": rid, "n": c.n}, nil, 0); err != nil {
			return fetched, err
		}
		var rep struct {
			SHA256 string `json:"sha256"`
		}
		n, err := fc.recv(frameTimeout, &rep, true)
		var pe *phoneError
		if errors.As(err, &pe) {
			return fetched, &errDamaged{n: c.n, code: pe.code}
		}
		if err != nil {
			return fetched, err
		}
		tmp, err := os.CreateTemp(dir, ".chunk-*")
		if err != nil {
			return fetched, err
		}
		h := sha256.New()
		err = fc.readBody(io.MultiWriter(tmp, h))
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp.Name())
			return fetched, err
		}
		if n != c.size || strings.ToLower(rep.SHA256) != c.sum || hex.EncodeToString(h.Sum(nil)) != c.sum {
			os.Remove(tmp.Name())
			return fetched, &errDamaged{n: c.n}
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return fetched, err
		}
		fetched++
	}
	return fetched, nil
}

// pushAgenda sends the agenda (the next defaultDays days, owner scope) and
// starts drafting the briefs it lacks, which the next sync carries.
func (s *Syncer) pushAgenda(ctx context.Context, fc *frameConn, stats *syncStats) error {
	if s.cfg.Agenda == nil {
		stats.agendaErr = true
		return nil
	}
	now := s.cfg.Now()
	evs, err := s.cfg.Agenda(ctx, defaultDays)
	if err != nil {
		s.cfg.Logf("phone: agenda: %v", err)
		stats.agendaErr = true
		return nil
	}
	if evs == nil {
		evs = []Event{}
	}
	a := Agenda{Events: evs, MadeAt: now}
	if s.cfg.Drafting != nil {
		a.Drafting = s.cfg.Drafting(now.Add(defaultDays * 24 * time.Hour))
	}
	b, err := json.Marshal(a)
	if err != nil || len(b) > maxBody {
		stats.agendaErr = true
		return nil
	}
	if err := fc.send(map[string]any{"op": "agenda", "body": len(b)}, bytes.NewReader(b), int64(len(b))); err != nil {
		return err
	}
	if _, err := fc.recv(frameTimeout, nil, false); err != nil {
		var pe *phoneError
		if errors.As(err, &pe) {
			s.cfg.Logf("phone: agenda not taken (%s)", pe.code)
			stats.agendaErr = true
			return nil
		}
		return err
	}
	stats.events, stats.agendaSent = len(evs), true
	return nil
}

// recLock serializes the work on one recording.
func (s *Syncer) recLock(rid string) func() {
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

func (s *Syncer) recDir(rid string) string { return filepath.Join(s.cfg.Dir, "recordings", rid) }
