package config

import (
	_ "embed"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Provider is a hosted endpoint that serves models in the OpenAI or the
// Anthropic shape: Baseten, say. Its key is in the keychain, under
// musubi-provider-<name> unless APIKey says where.
type Provider struct {
	Format  string            `toml:"format"` // "openai" or "anthropic"
	URL     string            `toml:"url"`    // without /v1
	APIKey  string            `toml:"api_key"`
	Headers map[string]string `toml:"headers"`
}

// KeyRef is where the provider's key is kept.
func (p Provider) KeyRef(name string) string {
	if p.APIKey != "" {
		return p.APIKey
	}
	return "keychain:musubi-provider-" + name
}

// applyProviders fills each model that names a provider (via) with what it
// doesn't set itself.
func (c *Config) applyProviders() error {
	var err error
	fill := func(l *LLM) {
		if l == nil || l.Via == "" || err != nil {
			return
		}
		p, ok := c.Providers[l.Via]
		if !ok {
			err = fmt.Errorf("a model uses provider %q, which [providers] doesn't have", l.Via)
			return
		}
		if l.Provider == "" {
			l.Provider = p.Format
		}
		if l.URL == "" {
			l.URL = p.URL
		}
		if l.APIKey == "" {
			l.APIKey = p.KeyRef(l.Via)
		}
		for k, v := range p.Headers {
			if _, set := l.Headers[k]; set {
				continue
			}
			if l.Headers == nil {
				l.Headers = map[string]string{}
			}
			l.Headers[k] = v
		}
	}
	walk(reflect.ValueOf(c).Elem(), func(v reflect.Value) {
		if l, ok := v.Addr().Interface().(*LLM); ok {
			fill(l)
			fill(l.Fallback)
		}
	})
	return err
}

// recommendedTOML is the recommended setup for Optimizely: hosted models
// through the AI Gateway (Claude Haiku 5.5 at medium thinking, research log
// 2026-11-11), GLM on Baseten behind them, local models for Live and
// search. No keys or personal ids: those come from Settings.
//
//go:embed recommended.toml
var recommendedTOML string

// modelKeys are the keys that say which model a section uses; applying a
// setup removes the ones it doesn't set, so an earlier provider's key or
// headers can't linger.
var modelKeys = []string{"provider", "via", "url", "model", "api_key", "headers", "price_in", "price_out", "reasoning_effort"}

// RecommendedEdits are the edits that bring a config to the recommended
// setup: its model sections, providers and gateway. Other keys, and a
// gateway token id, are left as they are.
func RecommendedEdits() ([]Edit, error) {
	var tree map[string]any
	if err := toml.Unmarshal([]byte(recommendedTOML), &tree); err != nil {
		return nil, err
	}
	var edits []Edit
	var walkT func(section string, t map[string]any)
	walkT = func(section string, t map[string]any) {
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		model := false
		for _, k := range keys {
			if k == "provider" || k == "via" {
				model = true
			}
		}
		for _, k := range keys {
			switch v := t[k].(type) {
			case map[string]any:
				if k == "headers" {
					h := map[string]string{}
					for hk, hv := range v {
						h[hk] = fmt.Sprint(hv)
					}
					edits = append(edits, Edit{Section: section, Key: k, Value: h})
					continue
				}
				walkT(strings.TrimPrefix(section+"."+k, "."), v)
			case int64:
				edits = append(edits, Edit{Section: section, Key: k, Value: int(v)})
			default:
				edits = append(edits, Edit{Section: section, Key: k, Value: v})
			}
		}
		if model {
			for _, k := range modelKeys {
				if _, set := t[k]; !set {
					edits = append(edits, Edit{Section: section, Key: k, Delete: true})
				}
			}
			if _, has := t["fallback"]; !has && !strings.HasSuffix(section, ".fallback") {
				edits = append(edits, Edit{Section: section + ".fallback", Delete: true})
			}
		}
	}
	walkT("", tree)
	return edits, nil
}
