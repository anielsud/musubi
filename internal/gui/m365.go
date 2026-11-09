package gui

import (
	"context"
	"errors"
	"time"

	"github.com/anielsud/musubi/internal/engine"
)

// M365View is the Microsoft 365 card in Settings.
type M365View struct {
	engine.M365State
	Connecting bool `json:"connecting"`
}

// M365 reports the Microsoft 365 connection.
func (a *App) M365() M365View {
	if a.eng == nil {
		return M365View{}
	}
	a.mu.Lock()
	connecting := a.m365Connecting
	a.mu.Unlock()
	return M365View{M365State: a.eng.M365State(), Connecting: connecting}
}

// M365Connect opens the Microsoft sign-in in the browser; when it completes
// the app syncs. "m365:changed" follows either way.
func (a *App) M365Connect() {
	if a.eng == nil {
		return
	}
	a.mu.Lock()
	if a.m365Connecting {
		a.mu.Unlock()
		return
	}
	a.m365Connecting = true
	a.mu.Unlock()
	a.emit("m365:changed", nil)
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
		defer cancel()
		_, err := a.eng.M365Connect(ctx)
		a.mu.Lock()
		a.m365Connecting = false
		a.mu.Unlock()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			a.emit("m365:error", err.Error())
		}
		a.emit("m365:changed", nil)
		if err == nil {
			a.Update()
		}
	}()
}

// M365SignOut forgets the sign-in. What was already read stays.
func (a *App) M365SignOut() error {
	if a.eng == nil {
		return nil
	}
	err := a.eng.M365SignOut()
	a.emit("m365:changed", nil)
	return err
}

// maybeReconnectM365 opens the sign-in again when a sync found it lapsed
// (m365.reconnect): not while Tomoe is recording, and by itself at most
// every six hours, so a sign-in the owner ignores doesn't keep coming back.
func (a *App) maybeReconnectM365() {
	if a.eng == nil || !a.eng.Cfg.M365.Reconnect || !a.eng.M365State().NeedsSignIn {
		return
	}
	if a.live != nil && a.live.Status().Mode == "live" {
		a.emit("m365:changed", nil) // Settings shows "sign in needed"
		return
	}
	a.mu.Lock()
	if time.Since(a.m365AutoAt) < 6*time.Hour {
		a.mu.Unlock()
		return
	}
	a.m365AutoAt = time.Now()
	a.mu.Unlock()
	a.M365Connect()
}
