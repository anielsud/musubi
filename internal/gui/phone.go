package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/phone"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Settings → Phone (docs/phone.md, part 2): the phone access server runs
// while the dataset is open and [phone] enabled is set. Paired phones
// upload their recordings, which the recorder imports, and fetch the
// owner's agenda with its briefs.

type phoneState struct {
	mu        sync.Mutex
	srv       *phone.Server
	listening bool
	err       string
	pairing   *phone.Pairing
	// The drafting agenda requests start.
	draft phone.Drafter
}

// startPhone opens the phone server for e and serves until ctx ends,
// when [phone] enabled is set.
func (a *App) startPhone(ctx context.Context, e *engine.Engine) {
	a.phone.mu.Lock()
	a.phone.srv, a.phone.listening, a.phone.err, a.phone.pairing = nil, false, "", nil
	a.phone.mu.Unlock()
	if !e.Cfg.Phone.Enabled {
		return
	}
	srv, err := a.openPhone(ctx, e)
	a.phone.mu.Lock()
	a.phone.srv = srv
	if err != nil {
		a.phone.err = err.Error()
	}
	a.phone.mu.Unlock()
	if err != nil {
		log.Printf("phone: not started: %v", err)
		return
	}
	go func() {
		// A reopen stops the old server just before this one starts, so
		// the port can still be held for a moment.
		for try := 0; ctx.Err() == nil; try++ {
			a.phone.mu.Lock()
			a.phone.listening, a.phone.err = true, ""
			a.phone.mu.Unlock()
			a.emit("phone:changed", nil)
			err := srv.Serve(ctx)
			a.phone.mu.Lock()
			a.phone.listening = false
			if err != nil {
				a.phone.err = fmt.Sprintf("can't listen on port %d: %v", e.Cfg.Phone.Port, err)
			}
			a.phone.mu.Unlock()
			a.emit("phone:changed", nil)
			if err == nil || try >= 5 {
				if err != nil {
					log.Printf("phone: %v", err)
				}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}

// openPhone opens the phone server's folder (certificate, devices)
// without listening.
func (a *App) openPhone(ctx context.Context, e *engine.Engine) (*phone.Server, error) {
	return phone.New(phone.Config{
		Dir:          e.Cfg.PhoneDir(),
		Port:         e.Cfg.Phone.Port,
		RecorderData: e.Cfg.Recorder.Data,
		Import: func(ctx context.Context, args control.ImportArgs) (control.ImportStatus, error) {
			var st control.ImportStatus
			err := control.NewClient(e.Cfg.RecorderControlSocket()).Call(ctx, control.OpImport, args, &st)
			return st, err
		},
		// The owner's own agenda: the phone carries what the owner sees.
		Agenda: func(_ context.Context, days int) ([]phone.Event, error) {
			return phone.BuildAgenda(e.Store, sensitivity.Owner(), time.Now(), days)
		},
		Drafting: func(until time.Time) int { return a.phoneDrafting(ctx, e, until) },
		Logf:     log.Printf,
		OnChange: func() { a.emit("phone:changed", nil) },
	})
}

// phoneDrafting starts drafting the briefs missing before until in the
// background, the weekly drafting's way, and says how many are left.
// Off with prep.auto_brief, as the weekly drafting is.
func (a *App) phoneDrafting(ctx context.Context, e *engine.Engine, until time.Time) int {
	if !e.Cfg.Prep.AutoBrief || e.AskLLM == nil {
		return 0
	}
	a.phone.mu.Lock()
	d := &a.phone.draft
	if d.Every == 0 {
		d.Every = briefEvery
	}
	a.phone.mu.Unlock()
	return d.Start(time.Now(), func() []string {
		targets, err := a.PrepTargets()
		if err != nil || a.live == nil || a.live.Status().Mode == "live" {
			return nil
		}
		now := time.Now()
		var missing []string
		for _, t := range targets {
			if !t.Upcoming || !t.At.After(now) || !t.At.Before(until) {
				continue
			}
			if b, err := e.Store.PrepBriefFor(sensitivity.Owner(), t.ItemID); err == nil && b == nil {
				missing = append(missing, t.ItemID)
			}
		}
		return missing
	}, func(visited func(string)) {
		a.draftBriefsUntil(ctx, until, visited)
	})
}

// PhoneView is Settings → Phone.
type PhoneView struct {
	Enabled   bool           `json:"enabled"`
	Port      int            `json:"port"`
	Listening bool           `json:"listening"`
	Error     string         `json:"error,omitempty"`
	Devices   []phone.Device `json:"devices"`
	Pairing   *phone.Pairing `json:"pairing,omitempty"`
	// OfRecord: Musubi takes in the recorder's meetings, so imported
	// phone recordings reach it ([recorder] of_record = "musubi").
	OfRecord bool `json:"ofRecord"`
}

func (a *App) PhoneState() (PhoneView, error) {
	if a.eng == nil {
		return PhoneView{}, a.notReady()
	}
	v := PhoneView{Enabled: a.eng.Cfg.Phone.Enabled, Port: a.eng.Cfg.Phone.Port, OfRecord: a.eng.Cfg.MusubiRecords(), Devices: []phone.Device{}}
	a.phone.mu.Lock()
	srv := a.phone.srv
	v.Listening, v.Error = a.phone.listening, a.phone.err
	if p := a.phone.pairing; p != nil && srv != nil {
		if open, _ := srv.PairingOpen(); open {
			v.Pairing = p
		} else {
			a.phone.pairing = nil
		}
	}
	a.phone.mu.Unlock()
	if srv == nil {
		srv, _ = a.phoneDevicesOnly()
	}
	if srv != nil {
		v.Devices = srv.Devices()
	}
	return v, nil
}

// phoneDevicesOnly opens the phone folder while the server is off, to
// list or revoke devices paired before; nil when none ever were.
func (a *App) phoneDevicesOnly() (*phone.Server, error) {
	if _, err := os.Stat(filepath.Join(a.eng.Cfg.PhoneDir(), "devices.json")); err != nil {
		return nil, nil
	}
	return phone.New(phone.Config{Dir: a.eng.Cfg.PhoneDir(), Port: a.eng.Cfg.Phone.Port})
}

// SetPhoneEnabled turns the phone server on or off ([phone] enabled) and
// reopens the dataset so it takes effect.
func (a *App) SetPhoneEnabled(on bool) error {
	if a.eng == nil {
		return a.notReady()
	}
	if err := editConfig(a.cfgPath, []config.Edit{{Section: "phone", Key: "enabled", Value: on}}); err != nil {
		return err
	}
	return a.reopen("phone:changed")
}

// PhonePair opens a pairing window: the code, its expiry, the addresses
// and the certificate fingerprint to compare.
func (a *App) PhonePair() (*phone.Pairing, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	a.phone.mu.Lock()
	defer a.phone.mu.Unlock()
	if a.phone.srv == nil || !a.phone.listening {
		return nil, errors.New("turn the phone server on first")
	}
	p, err := a.phone.srv.Pair()
	if err != nil {
		return nil, err
	}
	a.phone.pairing = &p
	return &p, nil
}

// PhoneCancelPairing closes the pairing window.
func (a *App) PhoneCancelPairing() {
	a.phone.mu.Lock()
	defer a.phone.mu.Unlock()
	if a.phone.srv != nil {
		a.phone.srv.CancelPairing()
	}
	a.phone.pairing = nil
}

// PhoneRevoke unpairs a phone: its token stops working at once. What is
// already on the phone stays there.
func (a *App) PhoneRevoke(id string) error {
	if a.eng == nil {
		return a.notReady()
	}
	a.phone.mu.Lock()
	srv := a.phone.srv
	a.phone.mu.Unlock()
	if srv == nil {
		var err error
		if srv, err = a.phoneDevicesOnly(); err != nil || srv == nil {
			return fmt.Errorf("no such device")
		}
	}
	return srv.Revoke(id)
}
