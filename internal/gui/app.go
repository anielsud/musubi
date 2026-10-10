// Package gui is the desktop app's backend: the methods the frontend calls,
// and the events it listens to.
package gui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/live"
	"github.com/anielsud/musubi/internal/store"
)

type App struct {
	ctx  context.Context
	eng  *engine.Engine
	live *live.Live
	err  string

	mu         sync.Mutex
	updating   bool
	lastUpdate time.Time
	ollamaUp   bool
	askCancel  context.CancelFunc
}

func NewApp() *App { return &App{} }

func (a *App) emit(ev string, data any) {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, ev, data)
	}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	e, err := engine.Open(config.Path())
	if err != nil {
		a.err = err.Error()
		return
	}
	a.eng = e
	a.live = live.New(e, a.emit, func() { a.Update() })
	go func() {
		up := e.EnsureOllama(ctx)
		a.mu.Lock()
		a.ollamaUp = up
		a.mu.Unlock()
		a.emit("app:status", a.Status())
	}()
	go a.live.Run(ctx)
}

func (a *App) Shutdown(ctx context.Context) {
	if a.eng != nil {
		a.eng.Close()
	}
}

// AppStatus is shown in the header and the status view.
type AppStatus struct {
	Error      string            `json:"error,omitempty"`
	Owner      string            `json:"owner"`
	OllamaUp   bool              `json:"ollamaUp"`
	ChatModel  string            `json:"chatModel"`
	EmbedModel string            `json:"embedModel"`
	Updating   bool              `json:"updating"`
	LastUpdate time.Time         `json:"lastUpdate"`
	Stats      *store.Stats      `json:"stats,omitempty"`
	Live       live.Status       `json:"live"`
	Config     map[string]string `json:"config"`
}

func (a *App) Status() AppStatus {
	st := AppStatus{Error: a.err}
	if a.eng == nil {
		return st
	}
	a.mu.Lock()
	st.OllamaUp, st.Updating, st.LastUpdate = a.ollamaUp, a.updating, a.lastUpdate
	a.mu.Unlock()
	st.Owner = a.eng.Cfg.Owner.Name
	if a.eng.Chat != nil {
		st.ChatModel = a.eng.Chat.Model
	}
	if a.eng.Embedder != nil {
		st.EmbedModel = a.eng.Embedder.Model()
	}
	st.Stats, _ = a.eng.Store.Stats()
	st.Live = a.live.Status()
	st.Config = map[string]string{
		"config":        config.Path(),
		"data":          a.eng.Cfg.DataDir,
		"tomoe socket":  a.eng.Cfg.Live.TomoeSocket,
		"suggest every": fmt.Sprintf("%ds", a.eng.Cfg.Live.SuggestEvery),
		"auto briefs":   fmt.Sprint(a.eng.Cfg.Live.AutoBrief),
	}
	return st
}

// Update ingests new data in the background; "update:done" follows.
func (a *App) Update() {
	if a.eng == nil {
		return
	}
	a.mu.Lock()
	if a.updating {
		a.mu.Unlock()
		return
	}
	a.updating = true
	a.mu.Unlock()
	a.emit("app:status", a.Status())
	go func() {
		st, err := a.eng.Update(a.ctx, nil)
		a.mu.Lock()
		a.updating = false
		a.lastUpdate = time.Now()
		a.mu.Unlock()
		msg := fmt.Sprintf("%d new or changed", st.Changed)
		if err != nil {
			msg = err.Error()
		} else {
			a.live.Reload()
		}
		a.emit("update:done", msg)
		a.emit("app:status", a.Status())
	}()
}

func (a *App) Search(query, scope string, k int) ([]store.Hit, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	return a.eng.Search(a.ctx, sc, query, k, nil)
}

// Ask answers in the background, streaming "ask:token" then "ask:done".
func (a *App) Ask(id, question, scope string) error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	if a.askCancel != nil {
		a.askCancel()
	}
	a.askCancel = cancel
	a.mu.Unlock()
	go func() {
		defer cancel()
		ans, err := ask.Ask(ctx, a.eng, sc, question, 8, nil, func(t string) {
			a.emit("ask:token", map[string]string{"id": id, "text": t})
		})
		res := map[string]any{"id": id}
		if err != nil {
			res["error"] = err.Error()
		}
		if ans != nil {
			res["text"] = ans.Text
			res["sources"] = ans.Sources
		}
		a.emit("ask:done", res)
	}()
	return nil
}

func (a *App) StopAsk() {
	a.mu.Lock()
	if a.askCancel != nil {
		a.askCancel()
	}
	a.mu.Unlock()
}

// Entities finds visible entities by name.
func (a *App) Entities(q, scope string) ([]store.EntityRef, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready")
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	return a.eng.Store.FindEntities(sc, strings.TrimSpace(q))
}

func (a *App) Graph(entityID, scope string) (*store.Graph, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready")
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	return a.eng.Store.EntityGraph(sc, entityID, []string{a.eng.OwnerEntity()}, 24)
}

// Item returns a source item's visible passages.
func (a *App) Item(itemID, scope string) (*store.ItemView, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready")
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	return a.eng.Store.Item(sc, itemID)
}

func (a *App) LiveStatus() live.Status { return a.live.Status() }

// SetSharing narrows the live panel to what may be on a shared screen.
func (a *App) SetSharing(on bool) error {
	if on {
		return a.live.SetScope("shared-screen")
	}
	return a.live.SetScope("owner")
}

// LookUp briefs on a query now (a clicked suggestion or a typed one).
func (a *App) LookUp(query, why, kind string) {
	scope := a.live.Status().Scope
	go a.live.Brief(a.ctx, live.Suggestion{Query: query, Why: why, Kind: kind}, nil, scope)
}

// Meetings lists recordings for the replay picker.
func (a *App) Meetings() ([]store.MeetingRef, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready")
	}
	return a.eng.Store.Meetings(60)
}

func (a *App) Replay(ref string, speed float64) error { return a.live.Replay(a.ctx, ref, speed) }

// ReplayWithAudio starts a replay that follows the audio player.
func (a *App) ReplayWithAudio(ref string) error { return a.live.ReplayWithAudio(ref) }

// ReplayClock reports the audio player's position, in seconds.
func (a *App) ReplayClock(t float64) { a.live.ReplayClock(t) }

// HasAudio reports whether a saved meeting has its recording.
func (a *App) HasAudio(ref string) bool {
	_, ok := a.live.SessionAudio(ref)
	return ok
}

// AudioHandler serves /replay-audio/<session id> to the app's own webview
// for audio replays. http.ServeContent handles range requests, so seeking
// works. Nothing but Tomoe session audio can be served.
func (a *App) AudioHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := strings.CutPrefix(r.URL.Path, "/replay-audio/")
		if !ok || a.live == nil {
			http.NotFound(w, r)
			return
		}
		p, ok := a.live.SessionAudio(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "audio/mp4")
		http.ServeContent(w, r, "audio.m4a", st.ModTime(), f)
	})
}
func (a *App) StopReplay() { a.live.StopReplay() }

func (a *App) SetOnTop(on bool) { wruntime.WindowSetAlwaysOnTop(a.ctx, on) }
