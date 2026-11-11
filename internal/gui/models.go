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

// A model group is one choice in Settings → Models, written to every
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

// SlotView is one model choice in a group. A choice is "gw:<id>" on the
// gateway, "local:<name>" in Ollama, "custom" (as the config has it,
// unchanged) or "" (none).
type SlotView struct {
	Kind     string   `json:"kind"`
	Sections []string `json:"sections"`
	Current  string   `json:"current"`
	// CustomText describes a "custom" model.
	CustomText  string `json:"customText,omitempty"`
	Fallback    string `json:"fallback"`
	HasFallback bool   `json:"hasFallback"` // the slot takes one
	Effort      string `json:"effort"`
}

type GroupView struct {
	Key   string     `json:"key"`
	Title string     `json:"title"`
	Does  string     `json:"does"`
	Slots []SlotView `json:"slots"`
}

type LocalModel struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"` // bytes; 0 not downloaded
	Have     bool   `json:"have"`
	Required bool   `json:"required"`
	Why      string `json:"why"`
}

type ModelsView struct {
	Groups []GroupView  `json:"groups"`
	Local  []LocalModel `json:"local"`
	Ollama bool         `json:"ollama"` // reachable
	Pulls  []string     `json:"pulls"`  // downloads in progress
}

// optionalLocal are local models worth offering though nothing requires
// them: the 27B answers Ask when the hosted model fails.
var optionalLocal = map[string]string{"qwen3.8:27b": "a stronger local fallback for Ask (about 17 GB)"}

func (a *App) Models() (ModelsView, error) {
	var v ModelsView
	if a.eng == nil {
		return v, fmt.Errorf("no dataset open")
	}
	cfg := a.eng.Cfg
	g := a.gatewayConfig()
	for _, mg := range modelGroups {
		gv := GroupView{Key: mg.Key, Title: mg.Title, Does: mg.Does}
		for _, s := range mg.Slots {
			sv := SlotView{Kind: s.Kind, Sections: s.Sections, HasFallback: s.FallbackAt != "-"}
			sv.Current, sv.CustomText, sv.Effort = describe(cfg.Section(s.Sections[0]), g)
			if sv.HasFallback {
				if l := fallbackOf(&cfg, s); l != nil && l.Provider != "" {
					sv.Fallback, _, _ = describe(l, g)
				}
			}
			gv.Slots = append(gv.Slots, sv)
		}
		v.Groups = append(v.Groups, gv)
	}
	have, err := ollamaTags(a.ollamaURL())
	v.Ollama = err == nil
	required := map[string]string{}
	if cfg.Embed.Provider == "ollama" && cfg.Embed.Model != "" {
		required[cfg.Embed.Model] = "embeddings for search"
	}
	if cfg.LLM.Provider == "ollama" && cfg.LLM.Model != "" {
		required[cfg.LLM.Model] = "Live suggestions and summaries"
	}
	seen := map[string]bool{}
	add := func(name, why string, req bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		size, ok := have[name]
		v.Local = append(v.Local, LocalModel{Name: name, Size: size, Have: ok, Required: req, Why: why})
	}
	for _, n := range sortedKeys(required) {
		add(n, required[n], true)
	}
	for _, n := range sortedKeys(optionalLocal) {
		add(n, optionalLocal[n], false)
	}
	var others []string
	for n := range have {
		// Each model is in the map twice, with and without ":latest".
		if !strings.HasSuffix(n, ":latest") {
			others = append(others, n)
		}
	}
	sort.Strings(others)
	for _, n := range others {
		add(n, "", false)
	}
	pullMu.Lock()
	for n := range pulling {
		v.Pulls = append(v.Pulls, n)
	}
	pullMu.Unlock()
	return v, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fallbackOf(cfg *config.Config, s modelSlot) *config.LLM {
	if s.FallbackAt != "" {
		l, _ := cfg.Section(s.FallbackAt).(*config.LLM)
		return l
	}
	if l, ok := cfg.Section(s.Sections[0]).(*config.LLM); ok {
		return l.Fallback
	}
	return nil
}

// describe names a section's model as a choice.
func describe(sec any, g config.Gateway) (choice, custom, effort string) {
	switch m := sec.(type) {
	case *config.LLM:
		if m == nil || m.Provider == "" || m.Provider == "none" {
			return "", "", ""
		}
		effort = m.ReasoningEffort
		if m.Provider == "ollama" {
			return "local:" + m.Model, "", effort
		}
		if g.URL != "" && (m.URL == g.URL+"/anthropic-shim" || m.URL == g.URL+"/workers-ai-shim") {
			return "gw:" + m.Model, "", effort
		}
		return "custom", fmt.Sprintf("%s %s at %s", m.Provider, m.Model, host(m.URL)), effort
	case *config.Classifier:
		if m == nil || m.Provider == "" {
			return "", "", ""
		}
		cat := g.CatalogURL()
		switch {
		case cat != "" && m.Provider == "jev" && m.URL == cat:
			return "gw:typesafe/jev", "", ""
		case cat != "" && m.Provider == "clef" && strings.HasPrefix(m.URL, cat+"/"):
			return "gw:" + strings.TrimPrefix(m.URL, cat+"/"), "", ""
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

// SetModels writes a group's choice to every section it covers, then
// reopens the dataset so the next call uses it. Choices are as in
// SlotView; "custom" leaves a section as it is. Effort ("", "low",
// "medium", "high") is for hosted chat models.
func (a *App) SetModels(group string, choices, fallbacks []string, effort string) error {
	if a.eng == nil {
		return fmt.Errorf("no dataset open")
	}
	var mg *modelGroup
	for i := range modelGroups {
		if modelGroups[i].Key == group {
			mg = &modelGroups[i]
		}
	}
	if mg == nil || len(choices) != len(mg.Slots) || len(fallbacks) != len(mg.Slots) {
		return fmt.Errorf("unknown model group %q", group)
	}
	if effort != "" && effort != "low" && effort != "medium" && effort != "high" {
		return fmt.Errorf("thinking level %q", effort)
	}
	g := a.gatewayConfig()
	var models []GatewayModel
	needGW := false
	for i := range mg.Slots {
		needGW = needGW || strings.HasPrefix(choices[i], "gw:") || strings.HasPrefix(fallbacks[i], "gw:")
	}
	if needGW {
		var err error
		if models, err = a.GatewayModels(); err != nil {
			return fmt.Errorf("the gateway's models: %w", err)
		}
		if a.eng.Cfg.Gateway.URL == "" {
			return fmt.Errorf("connect the AI Gateway first (Settings → Optimizely AI Gateway)")
		}
	}
	var edits []config.Edit
	for i, s := range mg.Slots {
		for _, sec := range s.Sections {
			e, err := slotEdits(sec, s.Kind, choices[i], effort, g, models)
			if err != nil {
				return err
			}
			edits = append(edits, e...)
		}
		switch {
		case s.FallbackAt == "-" || choices[i] == "custom" && fallbacks[i] == "custom":
		case fallbacks[i] == "custom":
		case fallbacks[i] == "":
			at := s.FallbackAt
			if at == "" {
				for _, sec := range s.Sections {
					edits = append(edits, config.Edit{Section: sec + ".fallback", Delete: true})
				}
			} else {
				edits = append(edits, config.Edit{Section: at, Delete: true})
			}
		default:
			ats := []string{s.FallbackAt}
			if s.FallbackAt == "" {
				ats = nil
				for _, sec := range s.Sections {
					ats = append(ats, sec+".fallback")
				}
			}
			for _, at := range ats {
				e, err := slotEdits(at, "chat", fallbacks[i], effort, g, models)
				if err != nil {
					return err
				}
				edits = append(edits, e...)
			}
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

// slotEdits are the config keys that point one section at a model. Keys
// the gateway supplies (api_key, headers) are removed, so an old
// provider's key or headers don't override it.
func slotEdits(sec, kind, choice, effort string, g config.Gateway, models []GatewayModel) ([]config.Edit, error) {
	set := func(k string, v any) config.Edit { return config.Edit{Section: sec, Key: k, Value: v} }
	del := func(k string) config.Edit { return config.Edit{Section: sec, Key: k, Delete: true} }
	switch {
	case choice == "custom":
		return nil, nil
	case choice == "":
		return []config.Edit{{Section: sec, Delete: true}}, nil
	case strings.HasPrefix(choice, "local:"):
		if kind == "decision" {
			return nil, fmt.Errorf("%s takes a decision model, not a local chat model", sec)
		}
		return []config.Edit{set("provider", "ollama"), set("url", "http://127.0.0.1:11434"), set("model", strings.TrimPrefix(choice, "local:")),
			del("api_key"), del("headers"), del("price_in"), del("price_out"), del("reasoning_effort")}, nil
	case strings.HasPrefix(choice, "gw:"):
		id := strings.TrimPrefix(choice, "gw:")
		var m *GatewayModel
		for i := range models {
			if models[i].ID == id {
				m = &models[i]
			}
		}
		if m == nil {
			return nil, fmt.Errorf("the gateway doesn't list %s", id)
		}
		if kind == "local" || (kind == "decision") != (m.Kind == "decision") {
			return nil, fmt.Errorf("%s can't take %s", sec, id)
		}
		out := []config.Edit{del("api_key"), del("headers")}
		switch m.Provider {
		case "jev":
			return append(out, set("provider", "jev"), set("url", g.CatalogURL()), del("model")), nil
		case "clef":
			return append(out, set("provider", "clef"), set("url", g.CatalogURL()+"/"+id), del("model"), set("price_in", m.PriceIn)), nil
		case "anthropic":
			out = append(out, set("provider", "anthropic"), set("url", g.URL+"/anthropic-shim"))
		default:
			out = append(out, set("provider", "openai"), set("url", g.URL+"/workers-ai-shim"))
		}
		out = append(out, set("model", id), set("price_in", m.PriceIn), set("price_out", m.PriceOut))
		if effort == "" {
			out = append(out, del("reasoning_effort"))
		} else {
			out = append(out, set("reasoning_effort", effort))
		}
		return out, nil
	}
	return nil, fmt.Errorf("model choice %q", choice)
}

// Local models: Ollama on this Mac.

func (a *App) ollamaURL() string {
	if a.eng != nil && a.eng.Chat != nil {
		return a.eng.Chat.URL
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
		out[m.Name] = m.Size
		// "nomic-embed-text" is listed as "nomic-embed-text:latest".
		out[strings.TrimSuffix(m.Name, ":latest")] = m.Size
	}
	return out, nil
}

var (
	pullMu  sync.Mutex
	pulling = map[string]bool{}
)

// PullModel downloads a local model in the background; "models:pull"
// events report progress ({name, status, pct}), then "models:changed".
func (a *App) PullModel(name string) error {
	if strings.ContainsAny(name, " /\\\"'") || name == "" {
		return fmt.Errorf("model name %q", name)
	}
	pullMu.Lock()
	if pulling[name] {
		pullMu.Unlock()
		return nil
	}
	pulling[name] = true
	pullMu.Unlock()
	go func() {
		err := ollamaPull(a.ctx, a.ollamaURL(), name, func(status string, pct float64) {
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

// RemoveModel deletes a local model that nothing requires.
func (a *App) RemoveModel(name string) error {
	v, err := a.Models()
	if err != nil {
		return err
	}
	for _, m := range v.Local {
		if m.Name == name && m.Required {
			return fmt.Errorf("%s is needed for %s", name, m.Why)
		}
	}
	b, _ := json.Marshal(map[string]string{"model": name})
	req, _ := http.NewRequest("DELETE", strings.TrimRight(a.ollamaURL(), "/")+"/api/delete", bytes.NewReader(b))
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

// pullRequired downloads the local models this dataset needs and doesn't
// have (llm.pull_missing), once Ollama is up.
func (a *App) pullRequired() {
	if a.eng == nil || !a.eng.Cfg.LLM.PullMissing {
		return
	}
	v, err := a.Models()
	if err != nil || !v.Ollama {
		return
	}
	for _, m := range v.Local {
		if m.Required && !m.Have {
			a.PullModel(m.Name)
		}
	}
}
