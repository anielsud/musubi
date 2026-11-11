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

// ProviderView is one custom provider in Settings → AI providers.
type ProviderView struct {
	Name    string            `json:"name"`
	Format  string            `json:"format"`
	URL     string            `json:"url"`
	KeyRef  string            `json:"keyRef"`
	HasKey  bool              `json:"hasKey"`
	Headers map[string]string `json:"headers"`
	InUse   []string          `json:"inUse"` // sections that use it
}

var providerNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

func (a *App) Providers() []ProviderView {
	if a.eng == nil {
		return nil
	}
	cfg := a.eng.Cfg
	uses := map[string][]string{}
	for _, mg := range modelGroups {
		for _, s := range mg.Slots {
			for _, sec := range s.Sections {
				if l, ok := cfg.Section(sec).(*config.LLM); ok {
					if l.Via != "" {
						uses[l.Via] = append(uses[l.Via], sec)
					}
					if l.Fallback != nil && l.Fallback.Via != "" {
						uses[l.Fallback.Via] = append(uses[l.Fallback.Via], sec+".fallback")
					}
				}
			}
		}
	}
	var out []ProviderView
	for name, p := range cfg.Providers {
		ref := p.KeyRef(name)
		_, err := readKey(ref)
		out = append(out, ProviderView{Name: name, Format: p.Format, URL: p.URL, KeyRef: ref, HasKey: err == nil, Headers: p.Headers, InUse: uses[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SaveProvider writes [providers.<name>] and, when a key is given, keeps
// it in the keychain under the provider's key reference.
func (a *App) SaveProvider(name, format, url string, headers map[string]string, key string) error {
	name, url, key = strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(url), "/"), strings.TrimSpace(key)
	if !providerNameRe.MatchString(name) {
		return fmt.Errorf("name: lower-case letters, digits and '-', starting with a letter")
	}
	if format != "openai" && format != "anthropic" {
		return fmt.Errorf("format must be openai or anthropic")
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
	edits := []config.Edit{{Section: sec, Key: "format", Value: format}, {Section: sec, Key: "url", Value: url}}
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
		if p.Name == name && len(p.InUse) > 0 {
			return fmt.Errorf("%s is used by %s; choose other models there first", name, strings.Join(p.InUse, ", "))
		}
	}
	if err := config.EditFile(a.cfgPath, []config.Edit{{Section: "providers." + name, Delete: true}}); err != nil {
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

// TestProvider lists the models of a custom provider ("gateway" for the AI
// Gateway's Claude route).
func (a *App) TestProvider(name string) TestResult {
	t0 := time.Now()
	ids, err := a.listModels(name, true)
	r := TestResult{OK: err == nil, Ms: time.Since(t0).Milliseconds(), Models: len(ids)}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

var (
	providerModelsMu sync.Mutex
	providerModels   = map[string][]string{}
)

// ProviderModels lists a custom provider's models for the model menus.
func (a *App) ProviderModels(name string) ([]string, error) { return a.listModels(name, false) }

func (a *App) listModels(name string, fresh bool) ([]string, error) {
	providerModelsMu.Lock()
	if ids, ok := providerModels[name]; ok && !fresh {
		providerModelsMu.Unlock()
		return ids, nil
	}
	providerModelsMu.Unlock()
	var p config.Provider
	if name == "gateway" {
		g := a.gatewayConfig()
		p = config.Provider{Format: "anthropic", URL: g.URL + "/anthropic-shim", APIKey: g.APIKey}
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
	if p.Format == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
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
