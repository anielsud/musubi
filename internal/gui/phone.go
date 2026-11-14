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

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/phone"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Settings → Phone (docs/phone.md, part 2): phone sync runs while the
// dataset is open and [phone] enabled is set. The Mac looks for phones on
// the local network and dials them (it listens on no port), fetches their
// recordings, which the recorder imports, and pushes the owner's agenda
// with its briefs.

type phoneState struct {
	mu   sync.Mutex
	sync *phone.Syncer
	err  string
	// ran is closed when the last syncer has stopped (its dns-sd
	// children and connections ended); a reopen and the app's exit wait
	// for it.
	ran chan struct{}
	// The drafting syncs start.
	draft phone.Drafter
}

// waitPhone waits, at most a few seconds, for the last syncer to stop.
func (a *App) waitPhone() {
	a.phone.mu.Lock()
	ran := a.phone.ran
	a.phone.mu.Unlock()
	if ran == nil {
		return
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		log.Printf("phone: the previous phone sync is still stopping")
	}
}

// startPhone opens phone sync for e and runs it until ctx ends, when
// [phone] enabled is set.
func (a *App) startPhone(ctx context.Context, e *engine.Engine) {
	a.waitPhone()
	a.phone.mu.Lock()
	a.phone.sync, a.phone.err = nil, ""
	a.phone.mu.Unlock()
	if !e.Cfg.Phone.Enabled {
		return
	}
	s, err := a.openPhone(ctx, e)
	ran := make(chan struct{})
	a.phone.mu.Lock()
	a.phone.sync = s
	if err != nil {
		a.phone.err = err.Error()
	} else {
		a.phone.ran = ran
	}
	a.phone.mu.Unlock()
	if err != nil {
		log.Printf("phone: not started: %v", err)
		return
	}
	go func() {
		defer close(ran)
		s.Run(ctx)
		a.emit("phone:changed", nil)
	}()
}

// openPhone opens phone sync's folder (certificate, paired phones)
// without starting it.
func (a *App) openPhone(ctx context.Context, e *engine.Engine) (*phone.Syncer, error) {
	return phone.New(phone.Config{
		Dir:          e.Cfg.PhoneDir(),
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
		Details:  e.Cfg.Phone.Details,
		// New details are taken in as a saved meeting is.
		OnDetails: func() {
			if e.Cfg.Live.UpdateOnSave {
				go a.update("a phone recording's details arrived")
			}
		},
		Logf:          log.Printf,
		OnChange:      func() { a.emit("phone:changed", nil) },
		OnPairRequest: a.askPair,
	})
}

// askPair brings the window forward, whatever screen is open, and asks
// the owner to Allow or Deny a phone's pairing.
func (a *App) askPair(p phone.PairPrompt) {
	if a.ctx != nil && a.emitFn == nil {
		wruntime.Show(a.ctx)
		wruntime.WindowUnminimise(a.ctx)
		wruntime.WindowShow(a.ctx)
	}
	a.emit("phone:pair", p)
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
	Enabled bool `json:"enabled"`
	// Looking: the Mac is looking for phones on the local network;
	// LookError says why not, when it should be.
	Looking   bool           `json:"looking"`
	LookError string         `json:"lookError,omitempty"`
	Error     string         `json:"error,omitempty"`
	Devices   []phone.Device `json:"devices"`
	// OfRecord: Musubi takes in the recorder's meetings, so imported
	// phone recordings reach it ([recorder] of_record = "musubi").
	OfRecord bool `json:"ofRecord"`
}

func (a *App) PhoneState() (PhoneView, error) {
	if a.eng == nil {
		return PhoneView{}, a.notReady()
	}
	v := PhoneView{Enabled: a.eng.Cfg.Phone.Enabled, OfRecord: a.eng.Cfg.MusubiRecords(), Devices: []phone.Device{}}
	a.phone.mu.Lock()
	s := a.phone.sync
	v.Error = a.phone.err
	a.phone.mu.Unlock()
	if s != nil {
		v.Looking, v.LookError = s.Looking()
	} else {
		s, _ = a.phoneDevicesOnly()
	}
	if s != nil {
		v.Devices = s.Devices()
	}
	return v, nil
}

// phoneDevicesOnly opens the phone folder while sync is off, to list or
// unpair phones paired before; nil when none ever were.
func (a *App) phoneDevicesOnly() (*phone.Syncer, error) {
	if _, err := os.Stat(filepath.Join(a.eng.Cfg.PhoneDir(), "phones.json")); err != nil {
		return nil, nil
	}
	return phone.New(phone.Config{Dir: a.eng.Cfg.PhoneDir()})
}

// SetPhoneEnabled turns phone sync on or off ([phone] enabled) and
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

// PhonePairRequest is the pairing waiting for the owner's answer, or nil.
func (a *App) PhonePairRequest() *phone.PairPrompt {
	a.phone.mu.Lock()
	s := a.phone.sync
	a.phone.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.PairPrompt()
}

// PhoneAnswerPair is the owner's Allow (allow true) or Deny of a pairing;
// the phone is paired once its owner has confirmed there too.
func (a *App) PhoneAnswerPair(id string, allow bool) error {
	a.phone.mu.Lock()
	s := a.phone.sync
	a.phone.mu.Unlock()
	if s == nil {
		return errors.New("phone sync is off")
	}
	return s.AnswerPair(id, allow)
}

// PhoneRevoke unpairs a phone: its pin is removed, so its connections
// fail. What is already on the phone stays there.
func (a *App) PhoneRevoke(id string) error {
	if a.eng == nil {
		return a.notReady()
	}
	a.phone.mu.Lock()
	s := a.phone.sync
	a.phone.mu.Unlock()
	if s == nil {
		var err error
		if s, err = a.phoneDevicesOnly(); err != nil || s == nil {
			return fmt.Errorf("no such phone")
		}
	}
	return s.Unpair(id)
}
