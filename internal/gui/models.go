package gui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

// A model group is one choice in Settings → Processing, written to every
// config section it covers. Kind says what can fill it: "chat" (hosted or
// local), "decision" (a probability per option) or "local" (on this Mac).
type modelSlot struct {
	Kind     string
	Sections []string
	// FallbackAt is where the fallback goes for the first section ("": the
	// section's own [<section>.fallback]; "-": none).
	FallbackAt string
}

type modelGroup struct {
	Key, Title, Does string
	Slots            []modelSlot
}

var modelGroups = []modelGroup{
	{"extract", "Extraction and threading", "Reads each meeting into statements, then links them into subjects.",
		[]modelSlot{{Kind: "chat", Sections: []string{"knowledge.lean.model", "knowledge.threads.model"}}}},
	{"write", "Statuses, summaries and Live briefs", "Writes each subject's status, meeting summaries and to-dos, and the cards' short briefs.",
		[]modelSlot{{Kind: "chat", Sections: []string{"knowledge.status.model", "knowledge.digest.model", "live.pick.brief"}}}},
	{"ask", "Ask", "Answers your questions from the knowledge base.",
		[]modelSlot{{Kind: "chat", Sections: []string{"ask.model"}, FallbackAt: "ask.fallback"}}},
	{"labels", "Labels", "Marks each statement's sensitivity.",
		[]modelSlot{{Kind: "decision", Sections: []string{"knowledge.label.classifier"}, FallbackAt: "-"}}},
	{"live", "Live", "Suggestions and summaries during a meeting run on this Mac; relevance picks the cards.",
		[]modelSlot{{Kind: "local", Sections: []string{"llm"}, FallbackAt: "-"}, {Kind: "decision", Sections: []string{"live.relevance"}, FallbackAt: "-"}}},
}

// usedSections names, for "used by", every section a model can be set in.
var usedSections = []struct{ Sec, Label string }{
	{"knowledge.lean.model", "Extraction"}, {"knowledge.threads.model", "Threading"},
	{"knowledge.status.model", "Statuses"}, {"knowledge.digest.model", "Summaries"},
	{"live.pick.brief", "Live briefs"}, {"ask.model", "Ask"}, {"ask.fallback", "Ask (fallback)"},
	{"knowledge.label.classifier", "Labels"}, {"knowledge.label.model", "Labels"},
	{"live.relevance", "Live relevance"}, {"llm", "Live suggestions"},
}

// A choice names a model as "<source>|<format>|<model>": the source is
// "gateway", "ollama" or a [providers] name; the format "anthropic",
// "openai", "ollama", or for decision models "jev" or "clef". "custom" is
// a section set by hand to something else (left as it is); "" none.
func choice(source, format, model string) string { return source + "|" + format + "|" + model }

func splitChoice(c string) (source, format, model string, ok bool) {
	p := strings.SplitN(c, "|", 3)
	if len(p) != 3 {
		return "", "", "", false
	}
	return p[0], p[1], p[2], true
}

// describe names a section's model as a choice; custom describes one set
// by hand.
func describe(sec any, g config.Gateway) (ch, custom, effort string) {
	switch m := sec.(type) {
	case *config.LLM:
		if m == nil || m.Provider == "" || m.Provider == "none" {
			return "", "", ""
		}
		effort = m.ReasoningEffort
		switch {
		case m.Provider == "ollama":
			return choice("ollama", "ollama", m.Model), "", ""
		case m.Via != "":
			return choice(m.Via, m.Provider, m.Model), "", effort
		case g.URL != "" && (m.URL == g.URL+"/anthropic-shim" || m.URL == g.URL+"/workers-ai-shim"):
			return choice(config.GatewayProvider, m.Provider, m.Model), "", effort
		}
		return "custom", fmt.Sprintf("%s %s at %s", m.Provider, m.Model, host(m.URL)), effort
	case *config.Classifier:
		if m == nil || m.Provider == "" {
			return "", "", ""
		}
		cat := g.CatalogURL()
		switch {
		case cat != "" && m.Provider == "jev" && m.URL == cat:
			return choice(config.GatewayProvider, "jev", "typesafe/jev"), "", ""
		case cat != "" && m.Provider == "clef" && strings.HasPrefix(m.URL, cat+"/"):
			return choice(config.GatewayProvider, "clef", strings.TrimPrefix(m.URL, cat+"/")), "", ""
		}
		return "custom", fmt.Sprintf("%s at %s", m.Provider, host(m.URL)), ""
	}
	return "", "", ""
}

func host(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i > 0 {
		u = u[:i]
	}
	return u
}

// modelAt is the model settings at a section, [<section>.fallback] included.
func modelAt(cfg *config.Config, sec string) any {
	if base, ok := strings.CutSuffix(sec, ".fallback"); ok && sec != "ask.fallback" {
		if l, ok := cfg.Section(base).(*config.LLM); ok && l.Fallback != nil {
			return l.Fallback
		}
		return nil
	}
	return cfg.Section(sec)
}

// usage is what each model is used for: "<source>|<model>" → labels. A
// fallback is labelled as one.
func usage(cfg *config.Config, g config.Gateway) map[string][]string {
	out := map[string][]string{}
	add := func(sec any, label string) {
		ch, _, _ := describe(sec, g)
		if src, _, model, ok := splitChoice(ch); ok {
			k := src + "|" + model
			for _, l := range out[k] {
				if l == label {
					return
				}
			}
			out[k] = append(out[k], label)
		}
	}
	for _, s := range usedSections {
		switch {
		case s.Sec == "knowledge.lean.model" && cfg.Knowledge.Extract != "lean":
			continue
		case s.Sec == "knowledge.label.model" && cfg.Knowledge.Label.Classifier.Provider != "":
			continue // the classifier labels instead
		}
		sec := cfg.Section(s.Sec)
		add(sec, s.Label)
		if l, ok := sec.(*config.LLM); ok && l.Fallback != nil {
			add(l.Fallback, s.Label+" (fallback)")
		}
	}
	if cfg.Embed.Provider == "ollama" && cfg.Embed.Model != "" {
		k := "ollama|" + cfg.Embed.Model
		out[k] = append(out[k], "Search")
	}
	return out
}

// SlotView is one model choice in a group (choices as above).
type SlotView struct {
	Kind     string   `json:"kind"`
	Sections []string `json:"sections"`
	Current  string   `json:"current"`
	// CustomText and FallbackText describe a "custom" model.
	CustomText     string `json:"customText,omitempty"`
	Effort         string `json:"effort"`
	Fallback       string `json:"fallback"`
	FallbackText   string `json:"fallbackText,omitempty"`
	FallbackEffort string `json:"fallbackEffort"`
	HasFallback    bool   `json:"hasFallback"` // the slot takes one
}

// Rate is a group's typical tokens per unit of work (a meeting hour, a
// question): measured with Claude Haiku 5.5 on three meetings (about two
// hours) on 2026-11-11, Claude's tokenizer, visible output only (research
// log). Thinking is not in it.
type Rate struct {
	In  int    `json:"in"`
	Out int    `json:"out"`
	Per string `json:"per"`
}

var groupRates = map[string]Rate{
	"extract": {In: 255000, Out: 30000, Per: "meeting hour"},
	"write":   {In: 145000, Out: 19000, Per: "meeting hour"},
	"ask":     {In: 7000, Out: 300, Per: "question"},
	"labels":  {In: 32000, Per: "meeting hour"},
}

type GroupView struct {
	Key   string     `json:"key"`
	Title string     `json:"title"`
	Does  string     `json:"does"`
	Slots []SlotView `json:"slots"`
	Rate  *Rate      `json:"rate,omitempty"`
}

// Groups are Processing's rows: what each group uses now.
func (a *App) Groups() ([]GroupView, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("no dataset open")
	}
	cfg := a.eng.Cfg
	g := a.gatewayConfig()
	var out []GroupView
	for _, mg := range modelGroups {
		gv := GroupView{Key: mg.Key, Title: mg.Title, Does: mg.Does}
		if r, ok := groupRates[mg.Key]; ok {
			gv.Rate = &r
		}
		for _, s := range mg.Slots {
			sv := SlotView{Kind: s.Kind, Sections: s.Sections, HasFallback: s.FallbackAt != "-"}
			sv.Current, sv.CustomText, sv.Effort = describe(cfg.Section(s.Sections[0]), g)
			if sv.HasFallback {
				at := s.FallbackAt
				if at == "" {
					at = s.Sections[0] + ".fallback"
				}
				if m := modelAt(&cfg, at); m != nil {
					sv.Fallback, sv.FallbackText, sv.FallbackEffort = describe(m, g)
				}
			}
			gv.Slots = append(gv.Slots, sv)
		}
		out = append(out, gv)
	}
	return out, nil
}

// ModelRow is one model in Settings → Models.
type ModelRow struct {
	Source  string   `json:"source"` // "gateway", "ollama" or a provider's name
	ID      string   `json:"id"`
	Formats []string `json:"formats"`
	// Kind: "chat" or "decision" (a probability per option: labels and
	// relevance).
	Kind          string   `json:"kind"`
	PriceIn       float64  `json:"priceIn"`  // dollars a million tokens; 0 unknown or free
	PriceOut      float64  `json:"priceOut"` // 0 for decision models
	Context       int      `json:"context"`  // tokens; 0 unknown
	Efforts       []string `json:"efforts"`  // thinking levels it takes (nil: none, or unknown)
	DefaultEffort string   `json:"defaultEffort"`
	Retires       string   `json:"retires"`
	Note          string   `json:"note,omitempty"`
	UsedBy        []string `json:"usedBy"`
	Size          int64    `json:"size"` // bytes, on this Mac
}

// ModelCatalog is every model the dataset can use, from every source.
type ModelCatalog struct {
	Rows   []ModelRow        `json:"rows"`
	Errors map[string]string `json:"errors"` // source → why it isn't listed
	Pulls  []string          `json:"pulls"`  // downloads in progress
}

func (a *App) Models() (ModelCatalog, error) {
	cat := ModelCatalog{Errors: map[string]string{}}
	if a.eng == nil {
		return cat, fmt.Errorf("no dataset open")
	}
	cfg := a.eng.Cfg
	g := a.gatewayConfig()
	used := usage(&cfg, g)
	add := func(r ModelRow) {
		r.UsedBy = used[r.Source+"|"+r.ID]
		cat.Rows = append(cat.Rows, r)
	}
	if gm, err := a.GatewayModels(); err != nil {
		cat.Errors[config.GatewayProvider] = err.Error()
	} else {
		for _, m := range gm {
			add(ModelRow{Source: config.GatewayProvider, ID: m.ID, Kind: m.Kind, PriceIn: m.PriceIn, PriceOut: m.PriceOut, Context: m.Context,
				Efforts: m.Efforts, DefaultEffort: m.DefaultEffort, Retires: m.Retires, Note: m.Note, Formats: []string{m.Provider}})
		}
	}
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		ids, err := a.listModels(n, false)
		if err != nil {
			cat.Errors[n] = err.Error()
			continue
		}
		for _, id := range ids {
			// Providers don't list thinking levels: offered, unverified.
			add(ModelRow{Source: n, ID: id, Kind: "chat", Formats: []string{"openai", "anthropic"}, Efforts: []string{"low", "medium", "high"}, Note: "thinking levels unverified"})
		}
	}
	if have, err := ollamaTags(ollamaURL(&cfg)); err != nil {
		cat.Errors["ollama"] = "Ollama isn't running"
	} else {
		for _, n := range sortedNames(have) {
			add(ModelRow{Source: "ollama", ID: n, Kind: "chat", Formats: []string{"ollama"}, Size: have[n]})
		}
	}
	// A model in use that its source doesn't list (not downloaded, or the
	// listing failed) is shown all the same.
	listed := map[string]bool{}
	for _, r := range cat.Rows {
		listed[r.Source+"|"+r.ID] = true
	}
	for k, labels := range used {
		if !listed[k] {
			src, id, _ := strings.Cut(k, "|")
			cat.Rows = append(cat.Rows, ModelRow{Source: src, ID: id, Kind: "chat", UsedBy: labels, Note: "not available now"})
		}
	}
	pullMu.Lock()
	for n := range pulling {
		cat.Pulls = append(cat.Pulls, n)
	}
	pullMu.Unlock()
	return cat, nil
}

// SetModels writes a group's choice to every section it covers, then
// reopens the dataset so the next call uses it. Efforts (one per slot, for
// the preferred and the fallback model; "" the model's own) are written
// for hosted chat models.
func (a *App) SetModels(group string, choices, fallbacks, efforts, fallbackEfforts []string) error {
	if a.eng == nil {
		return fmt.Errorf("no dataset open")
	}
	var mg *modelGroup
	for i := range modelGroups {
		if modelGroups[i].Key == group {
			mg = &modelGroups[i]
		}
	}
	if mg == nil {
		return fmt.Errorf("unknown model group %q", group)
	}
	n := len(mg.Slots)
	if len(choices) != n || len(fallbacks) != n || len(efforts) != n || len(fallbackEfforts) != n {
		return fmt.Errorf("model group %q has %d choices", group, n)
	}
	for i := range choices {
		if !validEffort[efforts[i]] || !validEffort[fallbackEfforts[i]] {
			return fmt.Errorf("thinking level %q / %q", efforts[i], fallbackEfforts[i])
		}
	}
	cfg := a.eng.Cfg
	var models []GatewayModel
	for i := range choices {
		if strings.HasPrefix(choices[i], config.GatewayProvider+"|") || strings.HasPrefix(fallbacks[i], config.GatewayProvider+"|") {
			var err error
			if models, err = a.GatewayModels(); err != nil {
				return fmt.Errorf("the gateway's models: %w", err)
			}
			break
		}
	}
	var edits []config.Edit
	for i, s := range mg.Slots {
		for _, sec := range s.Sections {
			e, err := slotEdits(&cfg, sec, s.Kind, choices[i], efforts[i], models)
			if err != nil {
				return err
			}
			edits = append(edits, e...)
		}
		if s.FallbackAt == "-" || fallbacks[i] == "custom" {
			continue
		}
		ats := []string{s.FallbackAt}
		if s.FallbackAt == "" {
			ats = nil
			for _, sec := range s.Sections {
				ats = append(ats, sec+".fallback")
			}
		}
		for _, at := range ats {
			e, err := slotEdits(&cfg, at, "chat", fallbacks[i], fallbackEfforts[i], models)
			if err != nil {
				return err
			}
			edits = append(edits, e...)
		}
	}
	if len(edits) == 0 {
		return nil
	}
	if err := config.EditFile(a.cfgPath, edits); err != nil {
		return err
	}
	return a.reopen("models:changed")
}

var validEffort = map[string]bool{"": true, "none": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// slotEdits are the config keys that point one section at a model. Keys a
// provider supplies (url, api_key, headers) are removed, so an earlier
// provider's can't linger.
func slotEdits(cfg *config.Config, sec, kind, ch, effort string, models []GatewayModel) ([]config.Edit, error) {
	set := func(k string, v any) config.Edit { return config.Edit{Section: sec, Key: k, Value: v} }
	del := func(k string) config.Edit { return config.Edit{Section: sec, Key: k, Delete: true} }
	thinking := func() config.Edit {
		if effort == "" {
			return del("reasoning_effort")
		}
		return set("reasoning_effort", effort)
	}
	if ch == "custom" {
		return nil, nil
	}
	if ch == "" {
		return []config.Edit{{Section: sec, Delete: true}}, nil
	}
	src, format, model, ok := splitChoice(ch)
	if !ok {
		return nil, fmt.Errorf("model choice %q", ch)
	}
	switch {
	case src == "ollama":
		if kind == "decision" {
			return nil, fmt.Errorf("%s takes a decision model, not a local chat model", sec)
		}
		return []config.Edit{set("provider", "ollama"), set("url", ollamaURL(cfg)), set("model", model),
			del("via"), del("api_key"), del("headers"), del("price_in"), del("price_out"), del("reasoning_effort")}, nil
	case kind == "local":
		return nil, fmt.Errorf("%s takes a model on this Mac", sec)
	case src == config.GatewayProvider:
		var m *GatewayModel
		for i := range models {
			if models[i].ID == model {
				m = &models[i]
			}
		}
		if m == nil {
			return nil, fmt.Errorf("the gateway doesn't list %s", model)
		}
		if (kind == "decision") != (m.Kind == "decision") {
			return nil, fmt.Errorf("%s can't take %s", sec, model)
		}
		g := cfg.Gateway
		out := []config.Edit{del("via"), del("api_key"), del("headers")}
		switch format {
		case "jev":
			return append(out, set("provider", "jev"), set("url", g.CatalogURL()), del("model")), nil
		case "clef":
			return append(out, set("provider", "clef"), set("url", g.CatalogURL()+"/"+model), del("model"), set("price_in", m.PriceIn)), nil
		}
		return []config.Edit{set("via", config.GatewayProvider), set("provider", format), set("model", model), del("url"), del("api_key"), del("headers"),
			set("price_in", m.PriceIn), set("price_out", m.PriceOut), thinking()}, nil
	}
	if _, ok := cfg.Providers[src]; !ok {
		return nil, fmt.Errorf("no provider %q", src)
	}
	if kind != "chat" {
		return nil, fmt.Errorf("%s takes a %s model", sec, kind)
	}
	out := []config.Edit{set("via", src), set("provider", format), set("model", model), del("url"), del("api_key"), del("headers"), thinking()}
	// Providers don't list prices: the ones set for this model stay.
	if old, ok := modelAt(cfg, sec).(*config.LLM); !ok || old == nil || old.Via != src || old.Model != model {
		out = append(out, del("price_in"), del("price_out"))
	}
	return out, nil
}

// ApplyRecommended brings the dataset's models, providers and gateway to
// the recommended setup (internal/config/recommended.toml); the rest of
// the config stays as it is.
func (a *App) ApplyRecommended() error {
	if a.eng == nil {
		return fmt.Errorf("no dataset open")
	}
	edits, err := config.RecommendedEdits()
	if err != nil {
		return err
	}
	if err := config.EditFile(a.cfgPath, edits); err != nil {
		return err
	}
	return a.reopen("models:changed")
}

// Local models: Ollama on this Mac.

// ollamaURL is where Ollama is: [llm]'s url when [llm] uses it, else
// [embed]'s, else the default.
func ollamaURL(cfg *config.Config) string {
	switch {
	case cfg.LLM.Provider == "ollama" && cfg.LLM.URL != "":
		return cfg.LLM.URL
	case cfg.Embed.Provider == "ollama" && cfg.Embed.URL != "":
		return cfg.Embed.URL
	}
	return "http://127.0.0.1:11434"
}

func ollamaTags(url string) (map[string]int64, error) {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(strings.TrimRight(url, "/") + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, m := range r.Models {
		// "nomic-embed-text:latest" is the "nomic-embed-text" configs name.
		out[strings.TrimSuffix(m.Name, ":latest")] = m.Size
	}
	return out, nil
}

func sortedNames(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	pullMu  sync.Mutex
	pulling = map[string]bool{}
)

// PullModel downloads a local model in the background; "models:pull"
// events report progress ({name, status, pct}), then "models:changed".
func (a *App) PullModel(name string) error {
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, " \\\"'") || name == "" {
		return fmt.Errorf("model name %q", name)
	}
	pullMu.Lock()
	if pulling[name] {
		pullMu.Unlock()
		return nil
	}
	pulling[name] = true
	pullMu.Unlock()
	url := ollamaURL(&a.eng.Cfg)
	go func() {
		err := ollamaPull(a.ctx, url, name, func(status string, pct float64) {
			a.emit("models:pull", map[string]any{"name": name, "status": status, "pct": pct})
		})
		pullMu.Lock()
		delete(pulling, name)
		pullMu.Unlock()
		if err != nil {
			a.emit("models:pull", map[string]any{"name": name, "status": "failed: " + err.Error(), "pct": 0})
		}
		a.emit("models:changed", nil)
	}()
	return nil
}

func ollamaPull(ctx context.Context, url, name string, progress func(string, float64)) error {
	b, _ := json.Marshal(map[string]any{"model": name, "stream": true})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(url, "/")+"/api/pull", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("%s", ev.Error)
		}
		pct := 0.0
		if ev.Total > 0 {
			pct = 100 * float64(ev.Completed) / float64(ev.Total)
		}
		progress(ev.Status, pct)
	}
	return sc.Err()
}

// RemoveModel deletes a local model nothing uses.
func (a *App) RemoveModel(name string) error {
	cfg := a.eng.Cfg
	if by := usage(&cfg, a.gatewayConfig())["ollama|"+name]; len(by) > 0 {
		return fmt.Errorf("%s is used by %s", name, strings.Join(by, ", "))
	}
	b, _ := json.Marshal(map[string]string{"model": name})
	req, _ := http.NewRequest("DELETE", strings.TrimRight(ollamaURL(&cfg), "/")+"/api/delete", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Ollama: %s", resp.Status)
	}
	a.emit("models:changed", nil)
	return nil
}

// pullRequired downloads the local models in use that Ollama doesn't
// have ([llm] pull_missing); fallbacks only by hand, since they may be
// large and aren't needed until something fails.
func (a *App) pullRequired() {
	if a.eng == nil || !a.eng.Cfg.LLM.PullMissing {
		return
	}
	cfg := a.eng.Cfg
	have, err := ollamaTags(ollamaURL(&cfg))
	if err != nil {
		return
	}
	for k, labels := range usage(&cfg, a.gatewayConfig()) {
		name, ok := strings.CutPrefix(k, "ollama|")
		if _, got := have[name]; !ok || got {
			continue
		}
		for _, l := range labels {
			if !strings.HasSuffix(l, "(fallback)") {
				a.PullModel(name)
				break
			}
		}
	}
}
