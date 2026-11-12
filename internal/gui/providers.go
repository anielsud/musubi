package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

// ProviderView is one card in Settings → AI providers: the AI Gateway,
// each custom provider, and Ollama on this Mac, shown alike.
type ProviderView struct {
	Name  string `json:"name"`  // "gateway", "ollama" or the provider's name
	Kind  string `json:"kind"`  // "gateway", "custom" or "ollama"
	Title string `json:"title"` // what the card is called
	URL   string `json:"url"`
	// Ready: set up and reachable as far as is known without a call (a
	// key in the keychain; Ollama answering).
	Ready   bool              `json:"ready"`
	KeyRef  string            `json:"keyRef,omitempty"`
	HasKey  bool              `json:"hasKey"`
	Formats []string          `json:"formats"`
	Headers map[string]string `json:"headers,omitempty"`
	UsedBy  []string          `json:"usedBy"`
	// The gateway's: whether [gateway] is in the config, and the token id.
	Configured bool   `json:"configured"`
	TokenID    string `json:"tokenId,omitempty"`
	// Ollama's: [llm] start_ollama and pull_missing.
	StartOllama bool `json:"startOllama"`
	PullMissing bool `json:"pullMissing"`
}

var providerNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

func (a *App) Providers() []ProviderView {
	if a.eng == nil {
		return nil
	}
	cfg := a.eng.Cfg
	g := a.gatewayConfig()
	bySource := map[string][]string{}
	for k, labels := range usage(&cfg, g) {
		src, _, _ := strings.Cut(k, "|")
		bySource[src] = append(bySource[src], labels...)
	}
	for k := range bySource {
		sort.Strings(bySource[k])
	}
	_, gwKeyErr := readKey(g.APIKey)
	out := []ProviderView{{Name: config.GatewayProvider, Kind: "gateway", Title: "Optimizely AI Gateway", URL: g.URL, KeyRef: g.APIKey,
		HasKey: gwKeyErr == nil, Ready: gwKeyErr == nil && cfg.Gateway.URL != "", Configured: cfg.Gateway.URL != "", TokenID: g.TokenID,
		Formats: []string{"anthropic", "openai"}, UsedBy: bySource[config.GatewayProvider]}}
	names := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := cfg.Providers[n]
		ref := p.KeyRef(n)
		_, err := readKey(ref)
		out = append(out, ProviderView{Name: n, Kind: "custom", Title: n, URL: p.URL, KeyRef: ref, HasKey: err == nil, Ready: err == nil,
			Formats: []string{"openai", "anthropic"}, Headers: p.Headers, UsedBy: bySource[n]})
	}
	url := ollamaURL(&cfg)
	_, err := ollamaTags(url)
	out = append(out, ProviderView{Name: "ollama", Kind: "ollama", Title: "Ollama on this Mac", URL: url, Ready: err == nil,
		Formats: []string{"ollama"}, UsedBy: bySource["ollama"], StartOllama: cfg.LLM.StartOllama, PullMissing: cfg.LLM.PullMissing})
	return out
}

// SaveProvider writes [providers.<name>] and, when a key is given, keeps
// it in the keychain under the provider's key reference.
func (a *App) SaveProvider(name, url string, headers map[string]string, key string) error {
	name, url, key = strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(url), "/"), strings.TrimSpace(key)
	if !providerNameRe.MatchString(name) || name == config.GatewayProvider || name == "ollama" {
		return fmt.Errorf("name: lower-case letters, digits and '-', starting with a letter (not gateway or ollama)")
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://127.0.0.1") && !strings.HasPrefix(url, "http://localhost") {
		return fmt.Errorf("the address must start with https:// (or be on this Mac)")
	}
	url = strings.TrimSuffix(url, "/v1")
	ref := config.Provider{}.KeyRef(name)
	if p, ok := a.eng.Cfg.Providers[name]; ok {
		ref = p.KeyRef(name)
	}
	if key != "" {
		if !strings.HasPrefix(ref, "keychain:") {
			return fmt.Errorf("this provider's key is read from %s; set it there", ref)
		}
		if !tokenRe.MatchString(key) {
			return fmt.Errorf("that doesn't look like an API key")
		}
		if err := writeKeychain(keychainService(ref), key); err != nil {
			return err
		}
	}
	sec := "providers." + name
	edits := []config.Edit{{Section: sec, Key: "url", Value: url}}
	if len(headers) > 0 {
		edits = append(edits, config.Edit{Section: sec, Key: "headers", Value: headers})
	} else {
		edits = append(edits, config.Edit{Section: sec, Key: "headers", Delete: true})
	}
	if err := config.EditFile(a.cfgPath, edits); err != nil {
		return err
	}
	providerModelsMu.Lock()
	delete(providerModels, name)
	providerModelsMu.Unlock()
	return a.reopen("providers:changed")
}

// DeleteProvider removes [providers.<name>] (not its keychain entry) when
// no model uses it.
func (a *App) DeleteProvider(name string) error {
	for _, p := range a.Providers() {
		if p.Name == name && p.Kind == "custom" && len(p.UsedBy) > 0 {
			return fmt.Errorf("%s is used by %s; choose other models there first", name, strings.Join(p.UsedBy, ", "))
		}
	}
	if err := config.EditFile(a.cfgPath, []config.Edit{{Section: "providers." + name, Delete: true}}); err != nil {
		return err
	}
	return a.reopen("providers:changed")
}

// SetOllama sets where Ollama is (every section that uses it), and whether
// the app starts it and downloads the models in use.
func (a *App) SetOllama(url string, start, pull bool) error {
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("the address must start with http:// (Ollama's default is http://127.0.0.1:11434)")
	}
	cfg := a.eng.Cfg
	edits := []config.Edit{{Section: "llm", Key: "start_ollama", Value: start}, {Section: "llm", Key: "pull_missing", Value: pull}}
	if cfg.Embed.Provider == "ollama" {
		edits = append(edits, config.Edit{Section: "embed", Key: "url", Value: url})
	}
	for _, s := range usedSections {
		secs := []string{s.Sec}
		if l, ok := cfg.Section(s.Sec).(*config.LLM); ok && l.Fallback != nil {
			secs = append(secs, s.Sec+".fallback")
		}
		for _, sec := range secs {
			if l, ok := modelAt(&cfg, sec).(*config.LLM); ok && l != nil && l.Provider == "ollama" {
				edits = append(edits, config.Edit{Section: sec, Key: "url", Value: url})
			}
		}
	}
	if err := config.EditFile(a.cfgPath, edits); err != nil {
		return err
	}
	return a.reopen("providers:changed")
}

// TestResult is a provider check: it lists the models, which costs nothing.
type TestResult struct {
	OK     bool   `json:"ok"`
	Ms     int64  `json:"ms"`
	Models int    `json:"models"`
	Error  string `json:"error,omitempty"`
}

// TestProvider lists a provider's models: the gateway's Claude route, a
// custom provider's /v1/models, or Ollama's.
func (a *App) TestProvider(name string) TestResult {
	t0 := time.Now()
	var n int
	var err error
	if name == "ollama" {
		var have map[string]int64
		have, err = ollamaTags(ollamaURL(&a.eng.Cfg))
		n = len(have)
	} else {
		var ids []string
		ids, err = a.listModels(name, true)
		n = len(ids)
	}
	r := TestResult{OK: err == nil, Ms: time.Since(t0).Milliseconds(), Models: n}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

var (
	providerModelsMu sync.Mutex
	providerModels   = map[string][]string{}
)

func (a *App) listModels(name string, fresh bool) ([]string, error) {
	providerModelsMu.Lock()
	if ids, ok := providerModels[name]; ok && !fresh {
		providerModelsMu.Unlock()
		return ids, nil
	}
	providerModelsMu.Unlock()
	var p config.Provider
	format := "openai"
	if name == config.GatewayProvider {
		g := a.gatewayConfig()
		p, format = config.Provider{URL: g.URL + "/anthropic-shim", APIKey: g.APIKey}, "anthropic"
	} else {
		var ok bool
		if p, ok = a.eng.Cfg.Providers[name]; !ok {
			return nil, fmt.Errorf("no provider %q", name)
		}
	}
	key, err := readKey(p.KeyRef(name))
	if err != nil {
		return nil, fmt.Errorf("no key at %s", p.KeyRef(name))
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", p.URL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "musubi")
	req.Header.Set("Authorization", "Bearer "+key)
	if format == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %s", p.URL, resp.Status)
	}
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("reading the model list: %w", err)
	}
	ids := make([]string, 0, len(r.Data))
	for _, m := range r.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	providerModelsMu.Lock()
	providerModels[name] = ids
	providerModelsMu.Unlock()
	return ids, nil
}

// readKey reads a key reference: keychain:<service> or env:<VARIABLE>.
func readKey(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "keychain:"):
		return readKeychain(keychainService(ref))
	case strings.HasPrefix(ref, "env:") && os.Getenv(strings.TrimPrefix(ref, "env:")) != "":
		return os.Getenv(strings.TrimPrefix(ref, "env:")), nil
	}
	return "", fmt.Errorf("no key at %s", ref)
}
