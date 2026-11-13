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
			a.update("after Microsoft 365 sign-in")
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

// PersonProfile is a person's directory entry with their managers, direct
// reports and how often the owner meets them (Explore). Owner scope only:
// who the owner meets is the owner's own business.
func (a *App) PersonProfile(entityID, scope string) (*engine.PersonProfile, error) {
	if a.eng == nil || scope != "owner" {
		return nil, nil
	}
	return a.eng.PersonProfile(entityID)
}

// People lists the people the owner meets, most met first (Explore →
// People). Owner scope only.
func (a *App) People(scope string) ([]engine.PersonRow, error) {
	if a.eng == nil || scope != "owner" {
		return nil, nil
	}
	return a.eng.People()
}

// PersonPage is a person's page (Explore). Owner scope only.
func (a *App) PersonPage(entityID, scope string) (*engine.PersonPage, error) {
	if a.eng == nil || scope != "owner" {
		return nil, nil
	}
	return a.eng.PersonPage(entityID)
}

// Headshot is a person's headshot for the people tiles (Explore), chosen
// now if it hasn't been, except during a live meeting. Owner scope only.
func (a *App) Headshot(entityID, scope string) (string, error) {
	if a.eng == nil || scope != "owner" {
		return "", nil
	}
	return a.eng.Headshot(entityID, a.live.Status().Mode != "live")
}

// PeopleNames maps names (normalised) that mean exactly one person to that
// person, so names shown anywhere can open the person. Owner scope only.
func (a *App) PeopleNames(scope string) (map[string]string, error) {
	if a.eng == nil || scope != "owner" {
		return nil, nil
	}
	return a.eng.PeopleNames()
}
