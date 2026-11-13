package gui

import (
	"github.com/anielsud/musubi/internal/diagnostics"
)

// Diagnostics is the report Status → Copy diagnostics puts on the
// clipboard (internal/diagnostics): versions, counts, connections,
// settings and scrubbed recent errors; no meeting content, titles, names
// or email addresses.
func (a *App) Diagnostics() string {
	if a.eng == nil {
		return diagnostics.Build(diagnostics.Input{App: Version, Errors: []string{"opening the dataset: " + a.err}})
	}
	in := a.eng.DiagnosticsInput(a.ctx, Version)
	if q, err := a.cachedQueue(); err == nil {
		in.Queue = &q
	}
	ls := a.live.Status()
	in.Checks = append(in.Checks, diagnostics.Check{Name: "live feed", OK: ls.Connected, Detail: map[bool]string{true: "connected", false: "not connected"}[ls.Connected]})
	for _, p := range a.Providers() {
		if p.Kind == "gateway" && p.Configured {
			in.Checks = append(in.Checks, diagnostics.Check{Name: "AI Gateway token", OK: p.Ready, Detail: map[bool]string{true: "in the Keychain", false: "missing"}[p.Ready]})
		}
	}
	return diagnostics.Build(in)
}
