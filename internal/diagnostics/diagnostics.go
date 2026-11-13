// Package diagnostics writes a plain-text report someone can paste to
// support (Status → Copy diagnostics, `musubi diagnostics`;
// docs/data-safety.md, item 9). It is built from versions, counts,
// booleans, model names and fixed strings; the few free-text lines it
// takes (health problems, recent errors) go through Scrub. No meeting
// content, titles, people's names, email addresses or keys.
package diagnostics

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/store"
)

// Input is what a report is made from; the caller fills what it knows.
type Input struct {
	App      string // this build's version
	Recorder string // the recorder's version; "" when it isn't running
	OS       string // OSVersion() unless set
	Cfg      config.Config
	Store    *store.Store
	// Queue is the knowledge layer's waiting work; nil leaves it out.
	Queue *pipeline.Queue
	// Checks are connection and health lines, worded by the caller with
	// fixed strings.
	Checks []Check
	// RecorderHealth is the recorder's problem lines (scrubbed here).
	RecorderHealth []string
	// Errors are recent error messages known in memory (scrubbed here).
	Errors []string
	// Logs are log files whose recent error lines are taken (scrubbed).
	Logs []string
	Now  time.Time
}

// Check is one connection's state.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Build writes the report.
func Build(in Input) string {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if in.OS == "" {
		in.OS = OSVersion()
	}
	// The words of this dataset's titles and names, removed from every
	// free-text line.
	deny := Vocabulary(in.Store, in.Cfg)
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	p("Musubi diagnostics, %s", in.Now.UTC().Format("2006-01-02 15:04 UTC"))
	p("Versions, counts and settings only: no meeting content, titles, names, email addresses or keys.")
	p("")
	p("Versions")
	p("  app: %s", word(in.App, "unknown"))
	p("  recorder: %s", word(in.Recorder, "not running"))
	p("  system: %s", in.OS)
	if in.Store != nil {
		if v, err := in.Store.DBVersions(); err == nil {
			p("  database: schema %d, format %d (this build: schema %d, format %d)", v.Schema, v.Format, v.BuildSchema, v.BuildFormat)
		} else {
			p("  database: versions unreadable")
		}
	}

	if in.Store != nil {
		p("")
		p("Data")
		if cs, err := in.Store.ItemCounts(); err == nil {
			var parts []string
			for _, c := range cs {
				parts = append(parts, fmt.Sprintf("%s %s %d", word(c.Source, "other"), word(c.Kind, "other"), c.N))
			}
			p("  items: %s", strings.Join(parts, ", "))
		}
		if k, err := in.Store.KnowledgeCounts(); err == nil {
			p("  statements %d, claims %d; extracted %d of %d items", k.Statements, k.Claims, k.Extracted, k.Extractable)
		}
		if th, placed, err := in.Store.ThreadCounts(); err == nil {
			p("  threads %d, statements in threads %d", th, placed)
		}
		if st, err := in.Store.Stats(); err == nil {
			p("  passages %d; meetings linked to their calendar event %d of %d", st.Chunks, st.MeetingsLinked, st.Meetings)
			p("  passages by sensitivity: %s", counts(st.ChunkLevels))
			p("  vectors: %s", counts(st.Vectors))
		}
	}
	if q := in.Queue; q != nil {
		p("  work queue: threaded %d; new to extract %d, to thread %d; backlog to extract %d, extracted %d; recorded twice %d",
			q.Threaded, q.NewExtract, q.NewThread, q.OlderExtract, q.OlderThread, q.Duplicates)
		u := q.Upgrades
		p("  waiting from upgrades: passages %d, extraction %d, summaries %d, statuses %d, names %d (about $%.2f)",
			u.Passages, u.Extract, u.Digest, u.Statuses, u.Names, u.USD)
	}

	p("")
	p("Connections and health")
	for _, c := range in.Checks {
		mark := "ok"
		if !c.OK {
			mark = "NOT OK"
		}
		line := fmt.Sprintf("  %s: %s", c.Name, mark)
		// Fixed strings from the caller; scrubbed in case one isn't.
		if d := Scrub(c.Detail); d != "" {
			line += " (" + d + ")"
		}
		p("%s", line)
	}
	for _, h := range in.RecorderHealth {
		if s := ScrubWith(h, deny); s != "" {
			p("  recorder: %s", s)
		}
	}

	p("")
	p("Models (job: format, model, where)")
	for _, m := range Models(in.Cfg) {
		p("  %s", m)
	}

	p("")
	p("Settings")
	for _, s := range Settings(in.Cfg) {
		p("  %s", s)
	}

	var errs []string
	for _, e := range in.Errors {
		if s := ScrubWith(e, deny); s != "" {
			errs = append(errs, s)
		}
	}
	for _, path := range in.Logs {
		for _, l := range RecentErrors(path, 10) {
			if s := ScrubWith(l, deny); s != "" {
				errs = append(errs, s)
			}
		}
	}
	p("")
	p("Recent errors (shortened: quoted text, names and addresses removed)")
	if len(errs) == 0 {
		p("  none found")
	}
	seen := map[string]bool{}
	for _, e := range errs {
		if !seen[e] {
			seen[e] = true
			p("  %s", e)
		}
	}
	return b.String()
}

func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", word(k, "other"), m[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// plainWord is a value safe to show as it is: a short identifier (a
// source, kind, version, model name or setting).
var plainWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+\-]{0,63}$`)

// word is s when it is a plain identifier without an email address, else
// alt.
func word(s, alt string) string {
	if s == "" || !plainWord.MatchString(s) || strings.Contains(s, "@") {
		return alt
	}
	return s
}

// modelSections are the jobs a model can be set for, as in Settings →
// Processing.
var modelSections = []struct{ Sec, Label string }{
	{"llm", "Live suggestions (and the default)"},
	{"knowledge.lean.model", "Extraction (lean)"}, {"knowledge.points.model", "Extraction (points)"},
	{"knowledge.threads.model", "Threading"}, {"knowledge.status.model", "Statuses"},
	{"knowledge.digest.model", "Summaries"}, {"live.pick.brief", "Live briefs"},
	{"ask.model", "Ask"}, {"ask.fallback", "Ask (fallback)"},
	{"knowledge.label.model", "Labels (local)"}, {"knowledge.label.classifier", "Labels"},
	{"live.relevance", "Live relevance"},
}

// Models says which provider and model each job uses, and where it runs:
// on this Mac, through the AI Gateway, through a named provider, or
// another hosted endpoint. No addresses, keys or headers.
func Models(cfg config.Config) []string {
	where := func(provider, url, via string) string {
		g := strings.TrimRight(cfg.Gateway.URL, "/")
		switch {
		case provider == "ollama" || strings.Contains(url, "localhost") || strings.Contains(url, "127.0.0.1"):
			return "this Mac"
		case via != "":
			return "provider " + word(via, "(named)")
		case g != "" && strings.HasPrefix(url, g+"/"):
			return "AI Gateway"
		case cfg.Gateway.Account != "" && strings.Contains(url, "/accounts/"+cfg.Gateway.Account+"/"):
			return "AI Gateway (catalog)"
		}
		return "hosted"
	}
	var out []string
	for _, s := range modelSections {
		switch m := cfg.Section(s.Sec).(type) {
		case *config.LLM:
			if m == nil || m.Provider == "" {
				continue
			}
			if m.Provider == "none" {
				out = append(out, s.Label+": none")
				continue
			}
			line := fmt.Sprintf("%s: %s, %s, %s", s.Label, word(m.Provider, "other"), word(m.Model, "(custom)"), where(m.Provider, m.URL, m.Via))
			if f := m.Fallback; f != nil && f.Provider != "" {
				line += fmt.Sprintf("; fallback %s, %s, %s", word(f.Provider, "other"), word(f.Model, "(custom)"), where(f.Provider, f.URL, f.Via))
			}
			out = append(out, line)
		case *config.Classifier:
			if m == nil || m.Provider == "" {
				continue
			}
			out = append(out, fmt.Sprintf("%s: %s, %s, %s", s.Label, word(m.Provider, "other"), word(m.Model, "-"), where(m.Provider, m.URL, "")))
		}
	}
	if cfg.Embed.Provider != "" {
		out = append(out, fmt.Sprintf("Search: %s, %s, %s", word(cfg.Embed.Provider, "other"), word(cfg.Embed.Model, "(custom)"), where(cfg.Embed.Provider, cfg.Embed.URL, "")))
	}
	return out
}

// Settings are the switches and choices that shape what runs: booleans,
// numbers and fixed choices only.
func Settings(cfg config.Config) []string {
	k := cfg.Knowledge
	on := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	return []string{
		fmt.Sprintf("knowledge: extract %s, grouping %s, updates %s, hold_upgrades %s, on_update %s, auto_max %d, allow_hosted %s",
			word(k.Extract, "(custom)"), word(k.Grouping, "(custom)"), word(k.Updates, "(custom)"), on(k.HoldUpgrades), on(k.OnUpdate), k.AutoMax, on(k.AllowHosted)),
		fmt.Sprintf("knowledge: statuses %s, summaries %s, labels %s, name_meetings %s, stitch %s, propose_aliases %s, apply_spellings %s",
			on(k.Status.Enabled), on(k.Digest.Enabled), on(k.Label.Enabled), on(k.NameMeetings), on(k.Stitch), on(k.ProposeAliases), on(k.ApplySpellings)),
		fmt.Sprintf("microsoft 365: sync %s, calendar %s", on(cfg.M365.Enabled), on(cfg.M365.Calendar)),
		fmt.Sprintf("recorder: of record %s, tomoe setting %s, relabel %s", word(cfg.Recorder.OfRecord, "(custom)"), word(cfg.Recorder.Tomoe, "(default)"), on(cfg.Recorder.Relabel)),
		fmt.Sprintf("store: backup_before_upgrade %s", on(cfg.Store.BackupBeforeUpgrade)),
		fmt.Sprintf("diagnostics: profiling %s, resources %s", on(cfg.Diagnostics.Profiling), on(cfg.Diagnostics.Resources)),
		fmt.Sprintf("sources: tomoe sessions %s, bluedot folder %s", on(cfg.TomoeSessions != ""), on(cfg.BluedotDir != "")),
	}
}

// OSVersion is the system's name, version and architecture.
func OSVersion() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
			return "macOS " + word(strings.TrimSpace(string(out)), "?") + " (" + runtime.GOARCH + ")"
		}
	}
	return runtime.GOOS + " (" + runtime.GOARCH + ")"
}

// errorWord marks a log line worth reporting.
var errorWord = regexp.MustCompile(`(?i)\b(error|errors|failed|fail|panic|fatal|refused|denied|timeout|timed out)\b`)

// RecentErrors are the last n lines of a log file (its last 256 KB) that
// report an error, unscrubbed; nil when it can't be read.
func RecentErrors(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 256<<10 {
		f.Seek(fi.Size()-256<<10, io.SeekStart)
	}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if l := sc.Text(); errorWord.MatchString(l) {
			out = append(out, l)
		}
	}
	return out[max(0, len(out)-n):]
}

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+`)
	reURL   = regexp.MustCompile(`[a-z]+://[^\s"'<>)]*`)
	// File paths: folder names hold people's names (the home folder),
	// session and file names hold titles.
	rePath   = regexp.MustCompile(`(~|\.{1,2})?(/[^\s/:"'<>]+){2,}/?`)
	reQuoted = regexp.MustCompile("\"[^\"]*\"?|“[^”]*”?|‘[^’]*’?|«[^»]*»?|`[^`]*`?|(^|[\\s(:=\\[{,])'[^']*'?")
	reParen  = regexp.MustCompile(`\([^()]*\)|\[[^\[\]]*\]|\{[^{}]*\}?`)
	// After a word like these comes what something is called or says: the
	// rest of the line goes.
	reNamed = regexp.MustCompile(`(?i)\b(title|titled|name|named|names|subject|speaker|speakers|person|people|attendee|attendees|organizer|participant|participants|meeting|topic|thread|query|question|answer|text|body|holder|label|content|prompt|reply|said|says|quote|account|user|owner|email|mail|chat|sender|recipient|caller|summary|statement|item)s?\b[\s:=]*`)
	// A token kept as it is: lowercase words, numbers and punctuation.
	rePlain = regexp.MustCompile(`^[a-z0-9_.:%=<>+\-,;!?#~*&|…/]*$`)
	// Long runs of letters and digits: ids, hashes, keys.
	reID = regexp.MustCompile(`[A-Za-z0-9_\-]{20,}`)
	// Clock times and dates at the start of a log line.
	reStamp = regexp.MustCompile(`^\d{4}[/-]\d\d[/-]\d\d[ T]\d\d:\d\d:\d\d(\.\d+)?Z?\s*`)
)

// capitalised words kept: product names and protocol terms, never names
// of people or meetings.
var keepWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`HTTP HTTPS API EOF TLS URL JSON SQL SQLite SQLITE_BUSY Ollama Musubi Tomoe Teams Graph Microsoft M365
		Keychain macOS GET POST PUT OK ID AI Gateway Jev Clef Anthropic Claude Haiku GLM Baseten Vertex MCP ONNX Wails WebKit
		Error Failed Warning Panic Post Get Put Delete Unauthorized Forbidden Not Found Bad Request Internal Server Too Many Requests
		Service Unavailable Gateway Timeout Status CPU GPU MB GB KB UTC`) {
		keepWords[w] = true
	}
}

// Scrub shortens a free-text line to what can be shown without content:
// email addresses, web addresses, quoted or bracketed text, and whatever
// follows a word like "title" or "name" are removed; then
// only lowercase words, numbers and a short list of product terms stay,
// since people's names and meeting titles are capitalised. Long ids
// become <id>. Lines that keep nothing useful come back empty.
func Scrub(s string) string { return ScrubWith(s, nil) }

// ScrubWith is Scrub that also removes every word of deny (Vocabulary):
// the words of this dataset's titles and names, so even a lowercase one
// doesn't get through.
func ScrubWith(s string, deny map[string]bool) string {
	s = strings.TrimSpace(reStamp.ReplaceAllString(s, ""))
	if len(s) > 600 {
		s = s[:600]
	}
	s = reEmail.ReplaceAllString(s, "<address>")
	s = reURL.ReplaceAllString(s, "<url>")
	s = rePath.ReplaceAllString(s, "<path>")
	s = reQuoted.ReplaceAllString(s, "$1…")
	for i := 0; i < 3; i++ {
		s = reParen.ReplaceAllString(s, "(…)")
	}
	if loc := reNamed.FindStringIndex(s); loc != nil {
		s = s[:loc[1]] + "…"
	}
	var out []string
	kept := 0
	for _, tok := range strings.Fields(s) {
		switch {
		case denied(tok, deny):
			out = append(out, "…")
		case reID.MatchString(tok):
			out = append(out, "<id>")
		case strings.Count(tok, "/") >= 2:
			out = append(out, "<path>")
		case rePlain.MatchString(tok):
			out = append(out, tok)
			if strings.ContainsAny(tok, "abcdefghijklmnopqrstuvwxyz") {
				kept++
			}
		case keepWords[strings.TrimRight(tok, ".,:;!?")]:
			out = append(out, tok)
			kept++
		default:
			out = append(out, "…")
		}
	}
	// Runs of removed words become one.
	var b []string
	for _, t := range out {
		if t == "…" && len(b) > 0 && strings.HasSuffix(b[len(b)-1], "…") {
			continue
		}
		b = append(b, t)
	}
	if kept == 0 {
		return ""
	}
	r := strings.Join(b, " ")
	if len(r) > 240 {
		r = r[:240] + "…"
	}
	return r
}

// denied: a word of the token is one of deny's.
func denied(tok string, deny map[string]bool) bool {
	if len(deny) == 0 {
		return false
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(tok), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if deny[w] {
			return true
		}
	}
	return false
}

// common words never removed for being in a title or name: they say what
// went wrong and identify nobody.
var common = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be by can could did do does for from has have in into is it its no not of on or so that the this to was were will with
		error errors failed failure fail timeout timed out context deadline exceeded canceled cancelled connection connect refused reset closed broken pipe
		permission denied allowed access missing found such file directory database locked busy disk space full read write open
		sync update updates model models request response status invalid unexpected end eof parse json server client network offline online
		token key expired sign signed again retry retrying start started stop stopped running recording recorder live feed socket
		new old version schema format upgrade backup copy local hosted gateway ollama audio mic microphone screen system`) {
		common[w] = true
	}
}

// Vocabulary is every word of this dataset's titles, names, email
// addresses and thread names (lowercase, but the common words above), for
// ScrubWith. Read here and used here: none of it is written out.
func Vocabulary(st *store.Store, cfg config.Config) map[string]bool {
	deny := map[string]bool{}
	add := func(s string) {
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if len(w) > 1 && !common[w] {
				deny[w] = true
			}
		}
	}
	add(cfg.Owner.Name)
	add(cfg.Owner.Email)
	if st == nil {
		return deny
	}
	if items, err := st.AllItems(); err == nil {
		for _, it := range items {
			add(it.Title)
			add(it.Thread)
			for _, p := range it.Participants {
				add(p.Name)
				add(p.Email)
			}
			seen := map[string]bool{}
			for _, sg := range it.Segments {
				if !seen[sg.Speaker] {
					seen[sg.Speaker] = true
					add(sg.Speaker)
					add(sg.Email)
				}
			}
		}
	}
	if given, err := st.GivenTitles(); err == nil {
		for _, t := range given {
			add(t.Title)
		}
	}
	if threads, err := st.Threads(); err == nil {
		for _, th := range threads {
			add(th.Name)
		}
	}
	return deny
}
