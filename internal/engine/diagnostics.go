package engine

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/diagnostics"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// DiagnosticsInput gathers what a diagnostics report shows that the
// engine can find out itself: the database, the recorder's version and
// health, Microsoft 365, Ollama, the gateway's setup and the logs. The app
// adds what it knows better (internal/gui).
func (e *Engine) DiagnosticsInput(ctx context.Context, app string) diagnostics.Input {
	in := diagnostics.Input{App: app, Cfg: e.Cfg, Store: e.Store}

	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var st control.Status
	switch err := control.NewClient(e.Cfg.RecorderControlSocket()).Call(rctx, control.OpStatus, nil, &st); {
	case err == nil:
		in.Recorder, in.RecorderHealth = st.Version, st.Health
		in.Checks = append(in.Checks, diagnostics.Check{Name: "Musubi recorder", OK: true, Detail: "running"})
	case errors.Is(err, control.ErrNotRunning):
		in.Checks = append(in.Checks, diagnostics.Check{Name: "Musubi recorder", OK: !e.Cfg.MusubiRecords(), Detail: "not running"})
	default:
		in.Checks = append(in.Checks, diagnostics.Check{Name: "Musubi recorder", OK: false, Detail: "not answering"})
	}
	in.Checks = append(in.Checks, diagnostics.Check{Name: "recorder of record", OK: true, Detail: map[bool]string{true: "musubi", false: "tomoe"}[e.Cfg.MusubiRecords()]})

	ms := e.M365State()
	m := diagnostics.Check{Name: "Microsoft 365", OK: ms.Connected && !ms.NeedsSignIn}
	switch {
	case ms.NeedsSignIn:
		m.Detail = "sign-in lapsed"
	case ms.Connected && ms.Enabled:
		m.Detail = "connected, syncing"
	case ms.Connected:
		m.Detail = "connected, syncing off"
	default:
		m.Detail, m.OK = "not connected", !ms.Enabled
	}
	if !ms.LastSync.IsZero() {
		m.Detail += ", last sync " + ms.LastSync.UTC().Format("2006-01-02 15:04 UTC")
	}
	in.Checks = append(in.Checks, m)
	if ms.LastError != "" {
		in.Errors = append(in.Errors, "microsoft 365: "+ms.LastError)
	}

	up := e.ollamaAnswers(ctx)
	in.Checks = append(in.Checks, diagnostics.Check{Name: "Ollama", OK: up, Detail: map[bool]string{true: "answering", false: "not answering"}[up]})
	in.Checks = append(in.Checks, diagnostics.Check{Name: "AI Gateway", OK: true, Detail: map[bool]string{true: "set up", false: "not set up"}[e.Cfg.Gateway.URL != ""]})

	if home, err := os.UserHomeDir(); err == nil {
		in.Logs = append(in.Logs, filepath.Join(home, "Library", "Logs", "Musubi", "musubi-gui.log"))
	}
	if e.Cfg.Recorder.Data != "" {
		in.Logs = append(in.Logs, filepath.Join(e.Cfg.Recorder.Data, "logs", "recorder.log"))
	}
	return in
}

// ollamaAnswers: the configured Ollama server answers (it isn't started).
func (e *Engine) ollamaAnswers(ctx context.Context) bool {
	url := e.Cfg.Embed.URL
	if e.Chat != nil && e.Cfg.LLM.Provider == "ollama" {
		url = e.Chat.URL
	}
	if url == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(url, "/")+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
