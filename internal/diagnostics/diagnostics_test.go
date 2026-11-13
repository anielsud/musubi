package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/store"
)

// Planted in the dataset, the config and the logs; none may reach the
// report. All invented.
var planted = []string{
	"Dana", "Ruiz", "dana", "ruiz", "Ilse", "Varga", "ilse", "varga", "Robin Vale", "robin", "vale",
	"dana.ruiz@example.com", "example.com", "@", "Candor", "candor", "Pricing Review", "pricing",
	"renewal", "Halberd", "halberd", "quarterly forecast", "forecast", "AirPods",
	"sk-live-4f9a8b7c6d5e4f3a2b1c", "tok-7e6d5c4b3a291807", "keychain:robin-secret",
	"leaving the company", "layoffs", "secret plan", "/Users/robinvale",
}

func TestReportHasNoContent(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	items := []item.Item{
		{ID: "tomoe:s-1", Source: "tomoe", Ref: "s-1", RawHash: "r1", Kind: item.KindMeeting, Title: "Pricing Review with Candor Labs", OccurredAt: at,
			Participants: []item.Participant{{Name: "Dana Ruiz", Email: "dana.ruiz@example.com", Role: "speaker"}, {Name: "Robin Vale", Email: "robin.vale@example.com", Owner: true}},
			Segments:     []item.Segment{{Speaker: "Dana Ruiz", Text: "Dana is leaving the company; the secret plan covers layoffs."}, {Speaker: "Ilse Varga", Text: "The quarterly forecast for Halberd slips."}}},
		{ID: "teams:m-2", Source: "teams", Ref: "m-2", RawHash: "r2", Kind: item.KindMeeting, Title: "halberd renewal sync", OccurredAt: at.Add(time.Hour),
			Segments: []item.Segment{{Speaker: "Ilse Varga", Text: "renewal terms"}}},
		{ID: "mail:e-3", Source: "mail", Ref: "e-3", RawHash: "r3", Kind: item.KindEmail, Title: "Re: quarterly forecast", Thread: "Quarterly forecast", OccurredAt: at,
			Participants: []item.Participant{{Name: "Ilse Varga", Email: "ilse.varga@example.com", Role: "from"}}, Segments: []item.Segment{{Text: "forecast attached"}}},
	}
	for _, it := range items {
		if _, err := st.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.NewThread("Candor renewal", "the Candor Labs renewal", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemTitle("tomoe:s-1", "Halberd forecast", "generated", "x"); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Owner.Name, cfg.Owner.Email = "Robin Vale", "robin.vale@example.com"
	cfg.Gateway = config.Gateway{URL: "https://gw.example.com", Account: "acct123", APIKey: "keychain:robin-secret", TokenID: "tok-7e6d5c4b3a291807"}
	cfg.Knowledge.Threads.Model = config.LLM{Provider: "anthropic", Model: "claude-haiku-5-5", URL: "https://gw.example.com/anthropic-shim",
		APIKey: "keychain:robin-secret", Headers: map[string]string{"x-key": "sk-live-4f9a8b7c6d5e4f3a2b1c"}}
	cfg.Knowledge.Status.Model = config.LLM{Provider: "openai", Model: "dana.ruiz@example.com", URL: "https://api.example.com/v1"}

	log := filepath.Join(dir, "app.log")
	lines := []string{
		`2026/11/12 10:00:01 error: name tomoe:s-1: "Pricing Review with Candor Labs" failed: context deadline exceeded`,
		`2026/11/12 10:00:02 status T3 (Candor renewal): reply: invalid character 'D' looking for beginning of value`,
		`2026/11/12 10:00:03 m365: sync for dana.ruiz@example.com failed: 401 Unauthorized`,
		`2026/11/12 10:00:04 error: open /Users/robinvale/Library/Application Support/musubi.db: permission denied`,
		`2026/11/12 10:00:05 lean tomoe:s-1 failed: model said {"text": "Dana is leaving the company"}`,
		`2026/11/12 10:00:06 extract failed for Dana Ruiz and Ilse Varga`,
		`2026/11/12 10:00:07 failed: halberd renewal pricing forecast timeout`,
		`2026/11/12 10:00:08 gateway error: POST https://gw.example.com/anthropic-shim?key=sk-live-4f9a8b7c6d5e4f3a2b1c: 429 Too Many Requests`,
		`2026/11/12 10:00:09 error: auth token tok-7e6d5c4b3a291807 expired`,
		`2026/11/12 10:00:10 recorded fine, nothing wrong here about Dana`,
		`2026/11/12 10:00:11 knowledge failed: context deadline exceeded`,
	}
	if err := os.WriteFile(log, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	q := pipeline.Queue{Threaded: 2, NewExtract: 1, Upgrades: pipeline.Upgrades{Passages: 3, USD: 0.14}}
	rep := Build(Input{
		App: "v1.2.3", Recorder: "v1.2.3", OS: "macOS 26.1 (arm64)", Cfg: cfg, Store: st, Queue: &q,
		Checks:         []Check{{Name: "Microsoft 365", OK: false, Detail: "sign-in lapsed for dana.ruiz@example.com"}},
		RecorderHealth: []string{`mic "Dana's AirPods" is silent`, "screen recording permission missing"},
		Errors:         []string{"microsoft 365: refresh for Robin Vale (robin.vale@example.com) failed: invalid_grant"},
		Logs:           []string{log, filepath.Join(dir, "missing.log")},
		Now:            at,
	})
	for _, p := range planted {
		if strings.Contains(rep, p) {
			t.Errorf("report contains %q:\n%s", p, rep)
		}
	}
	// What it is for is still there.
	for _, want := range []string{
		"app: v1.2.3", "schema 4", "tomoe meeting 1", "teams meeting 1", "mail email 1", "threads 1",
		"claude-haiku-5-5, AI Gateway", "passages 3", "context deadline exceeded", "401 Unauthorized",
		"permission denied", "429 Too Many Requests", "screen recording permission missing", "invalid_grant",
		"hold_upgrades on",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if t.Failed() {
		return
	}
	t.Log("\n" + rep)
}

func TestScrub(t *testing.T) {
	for in, want := range map[string]string{
		"dial tcp 127.0.0.1:11434: connect: connection refused": "dial tcp 127.0.0.1:11434: connect: connection refused",
		`open "Board Minutes.docx": no such file`:               `open …: no such file`,
		"Dana Ruiz": "",
	} {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}
