// Package gui is the desktop app's backend: the methods the frontend calls,
// and the events it listens to.
package gui

import (
	"context"
	"fmt"
	"github.com/anielsud/musubi/internal/pipeline"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/anielsud/musubi/internal/ask"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/live"
	"github.com/anielsud/musubi/internal/store"
)

type App struct {
	ctx context.Context
	// qcache: see queueCache.
	qcache queueCache
	eng    *engine.Engine
	live   *live.Live
	err    string

	// cfgPath is the open dataset's config; liveCancel stops its live lane
	// when another dataset is opened in this window.
	cfgPath    string
	liveCancel context.CancelFunc
	narrowW    int // the side-panel size to return to from wide
	narrowH    int

	mu         sync.Mutex
	updating   bool
	running    string // a knowledge run started from the Status page
	lastUpdate time.Time
	ollamaUp   bool
	askCancel  context.CancelFunc
	// spellMu runs one refresh after a spelling rule at a time.
	spellMu sync.Mutex
	// m365Connecting: a sign-in is open in the browser; m365AutoAt: when
	// the app last opened one by itself.
	m365Connecting bool
	m365AutoAt     time.Time
	// updateAgain: a meeting was edited during an update, which may have
	// read it before the edit.
	updateAgain bool
	rec         recorderState
	// emitFn replaces the Wails event bus in tests.
	emitFn func(string, any)
}

func NewApp() *App { return &App{} }

func (a *App) emit(ev string, data any) {
	if a.emitFn != nil {
		a.emitFn(ev, data)
		return
	}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, ev, data)
	}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	if err := a.openConfig(startConfig()); err != nil {
		a.err = err.Error()
	}
}

// openConfig opens a dataset's config in this window, closing the one open
// before (its live lane stops and its store closes).
func (a *App) openConfig(path string) error {
	if a.liveCancel != nil {
		a.liveCancel()
		a.liveCancel = nil
	}
	if a.eng != nil {
		a.eng.Close()
		a.eng, a.live = nil, nil
	}
	e, err := engine.Open(path)
	if err != nil {
		return err
	}
	a.eng, a.cfgPath, a.err = e, path, ""
	a.live = live.New(e, a.emit, func() { a.Update() })
	lctx, cancel := context.WithCancel(a.ctx)
	a.liveCancel = cancel
	wruntime.WindowSetTitle(a.ctx, "Musubi — "+datasetName(path))
	go func() {
		up := e.EnsureOllama(lctx)
		a.mu.Lock()
		a.ollamaUp = up
		a.mu.Unlock()
		a.emit("app:status", a.Status())
		if up {
			a.pullRequired()
		}
	}()
	go a.live.Run(lctx)
	go a.followRecorder(lctx, e.Cfg.RecorderLiveSocket())
	if e.Cfg.Prep.AutoBrief && e.AskLLM != nil {
		go a.draftBriefs(lctx)
	}
	if n := e.Cfg.Live.UpdateEvery; n > 0 {
		go func() {
			t := time.NewTicker(time.Duration(n) * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-lctx.Done():
					return
				case <-t.C:
					a.Update() // skips itself while another is going
				}
			}
		}()
	}
	// Build the topic map now, so Explore opens without waiting for every
	// claim's evidence to load (seconds on a large corpus). The result is
	// cached until the data, the scope or the day changes.
	go func() {
		for _, name := range []string{"owner", "shared-screen"} {
			if sc, err := e.Scope(name); err == nil {
				knowledge.Views{Store: e.Store, Scope: sc, Now: time.Now()}.Map()
			}
		}
	}()
	return nil
}

func (a *App) Shutdown(ctx context.Context) {
	if a.liveCancel != nil {
		a.liveCancel()
	}
	if a.eng != nil {
		a.eng.Close()
	}
}

// AppStatus is shown in the header and the status view.
type AppStatus struct {
	Error      string       `json:"error,omitempty"`
	Owner      string       `json:"owner"`
	OllamaUp   bool         `json:"ollamaUp"`
	ChatModel  string       `json:"chatModel"`
	EmbedModel string       `json:"embedModel"`
	Updating   bool         `json:"updating"`
	LastUpdate time.Time    `json:"lastUpdate"`
	Stats      *store.Stats `json:"stats,omitempty"`
	Live       live.Status  `json:"live"`
	Dataset    string       `json:"dataset"`
	// What "Check for new meetings" does: sync Microsoft 365 (when on),
	// then process up to AutoMax new meetings (0: all) when OnUpdate; and
	// how often it runs by itself (minutes, 0: never).
	M365        bool `json:"m365"`
	OnUpdate    bool `json:"onUpdate"`
	AutoMax     int  `json:"autoMax"`
	UpdateEvery int  `json:"updateEvery"`
}

func (a *App) Status() AppStatus {
	st := AppStatus{Error: a.err, Dataset: datasetName(a.cfgPath)}
	if a.eng == nil {
		return st
	}
	a.mu.Lock()
	st.OllamaUp, st.Updating, st.LastUpdate = a.ollamaUp, a.updating, a.lastUpdate
	a.mu.Unlock()
	st.Owner = a.eng.Cfg.Owner.Name
	st.M365, st.OnUpdate, st.AutoMax = a.eng.Cfg.M365.Enabled, a.eng.Cfg.Knowledge.OnUpdate, a.eng.Cfg.Knowledge.AutoMax
	st.UpdateEvery = a.eng.Cfg.Live.UpdateEvery
	if a.eng.Chat != nil {
		st.ChatModel = a.eng.Chat.Model
	}
	if a.eng.Embedder != nil {
		st.EmbedModel = a.eng.Embedder.Model()
	}
	st.Stats, _ = a.eng.Store.Stats()
	st.Live = a.live.Status()
	return st
}

// Update ingests new data in the background; "update:done" follows.
func (a *App) Update() {
	if a.eng == nil {
		return
	}
	a.mu.Lock()
	if a.updating || a.running != "" {
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
		if st.M365 != "" {
			msg = st.M365 + "; " + msg
		}
		if err != nil {
			msg = err.Error()
		} else if a.eng.Cfg.Knowledge.OnUpdate {
			// New items join their threads, and the threads' statuses are
			// rewritten, before the views reload.
			a.emit("update:done", msg+"; updating topics…")
			if ks, kerr := a.eng.Knowledge(a.ctx, io.Discard, pipeline.KnowledgeOptions{Auto: true}); kerr != nil {
				msg += "; topics: " + kerr.Error()
			} else if ks.Threads.Items > 0 || ks.Statuses > 0 {
				msg += fmt.Sprintf("; %d threaded, %d statuses rewritten", ks.Threads.Items, ks.Statuses)
			}
		}
		if err == nil {
			a.live.Reload()
		}
		a.maybeReconnectM365()
		a.emit("update:done", msg)
		a.emit("app:status", a.Status())
		a.emit("queue:changed", nil)
		a.mu.Lock()
		again := a.updateAgain
		a.updateAgain = false
		a.mu.Unlock()
		if again {
			a.Update()
		}
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
		ans, err := ask.AskWith(ctx, a.eng, sc, question, 8, nil, ask.Hooks{
			Found: func(f *ask.Answer) {
				a.emit("ask:found", map[string]any{"id": id, "sources": f.Sources, "threads": f.Threads})
			},
			Token: func(t string) {
				a.emit("ask:token", map[string]string{"id": id, "text": t})
			},
		})
		res := map[string]any{"id": id}
		switch {
		case ctx.Err() != nil:
			res["stopped"] = true // what was written so far stays
		case err != nil:
			res["error"] = err.Error()
		}
		if ans != nil {
			res["text"] = ans.Text
			res["sources"] = ans.Sources
			res["threads"] = ans.Threads
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

// Traces returns the live panel's recent processing steps.
func (a *App) Traces() []live.Trace { return a.live.Traces() }

// SetDiagnostics widens the window to make room for the diagnostics view,
// or narrows it back.
func (a *App) SetDiagnostics(on bool) {
	w, h := wruntime.WindowGetSize(a.ctx)
	const extra = 560
	if on {
		w += extra
	} else if w-extra >= 360 {
		w -= extra
	}
	wruntime.WindowSetSize(a.ctx, w, h)
}

// SetWide switches the window between the side panel and a landscape
// layout most of the screen wide, and back to the size it had.
func (a *App) SetWide(on bool) {
	if on {
		a.narrowW, a.narrowH = wruntime.WindowGetSize(a.ctx)
		w, h := 1600, 1000
		if ss, err := wruntime.ScreenGetAll(a.ctx); err == nil {
			for _, s := range ss {
				if s.IsCurrent {
					w, h = min(s.Size.Width*85/100, 2400), s.Size.Height*85/100
				}
			}
		}
		wruntime.WindowSetSize(a.ctx, w, h)
		wruntime.WindowCenter(a.ctx)
		return
	}
	w, h := a.narrowW, a.narrowH
	if w == 0 || w >= 1500 {
		w, h = 1080, 900
	}
	wruntime.WindowSetSize(a.ctx, w, h)
}

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
// works. Nothing but Tomoe session audio can be served, and the
// recorder's voice clips and window tiles under /recorder-media/
// (recorderMedia).
func (a *App) AudioHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/recorder-media/") && a.eng != nil {
			a.recorderMedia(w, r)
			return
		}
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
