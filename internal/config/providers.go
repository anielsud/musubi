package config

import (
	_ "embed"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Provider is a hosted endpoint: Baseten, say. One address serves both
// formats (/v1/chat/completions and /v1/messages); which a model speaks is
// the model's own `provider` ("openai" or "anthropic"). Its key is in the
// keychain, under musubi-provider-<name> unless APIKey says where.
// "gateway" is built in: the AI Gateway ([gateway]), whose two routes
// serve the two formats.
type Provider struct {
	URL     string            `toml:"url"` // without /v1
	APIKey  string            `toml:"api_key"`
	Headers map[string]string `toml:"headers"`
}

// GatewayProvider is the AI Gateway's name for `via`.
const GatewayProvider = "gateway"

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
		if l.Provider == "" {
			// The format: Claude speaks Anthropic's; the rest OpenAI's.
			l.Provider = "openai"
			if strings.HasPrefix(l.Model, "claude-") {
				l.Provider = "anthropic"
			}
		}
		if l.Via == GatewayProvider {
			if c.Gateway.URL == "" {
				err = fmt.Errorf("a model uses the AI Gateway, which [gateway] doesn't set up")
				return
			}
			if l.URL == "" {
				l.URL = strings.TrimRight(c.Gateway.URL, "/") + map[string]string{"anthropic": "/anthropic-shim"}[l.Provider]
				if l.Provider != "anthropic" {
					l.URL = strings.TrimRight(c.Gateway.URL, "/") + "/workers-ai-shim"
				}
			}
			return // its key and headers come from [gateway]
		}
		p, ok := c.Providers[l.Via]
		if !ok {
			err = fmt.Errorf("a model uses provider %q, which [providers] doesn't have", l.Via)
			return
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

// Value is the setting at a table's dotted name and key ("live",
// "suggest_every"), as loaded: a bool, int, float64 or string; nil when
// there is no such setting.
func (c *Config) Value(section, key string) any {
	v := reflect.ValueOf(c).Elem()
	parts := []string{key}
	if section != "" {
		parts = append(strings.Split(section, "."), key)
	}
	for _, part := range parts {
		if v.Kind() != reflect.Struct {
			return nil
		}
		found := false
		for i := 0; i < v.NumField(); i++ {
			if strings.Split(v.Type().Field(i).Tag.Get("toml"), ",")[0] == part {
				v, found = v.Field(i), true
				break
			}
		}
		if !found {
			return nil
		}
	}
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int64:
		return int(v.Int())
	case reflect.Float64:
		return v.Float()
	case reflect.String:
		return v.String()
	}
	return nil
}
