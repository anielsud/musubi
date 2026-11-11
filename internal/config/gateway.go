package config

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Gateway is the Optimizely AI Gateway: one personal token for every
// hosted model, set up from Settings. A model whose url is on the gateway
// (its worker, or the account's Cloudflare model catalog) takes the
// gateway's key and headers for whatever it doesn't set itself, so a new
// token changes one key here rather than every model. Empty: models are
// configured one by one, the earlier behaviour.
type Gateway struct {
	// URL is the gateway's worker, without a route
	// ("https://….workers.dev"); models use its /workers-ai-shim and
	// /anthropic-shim routes.
	URL string `toml:"url"`
	// Account is the Cloudflare account whose model catalog (/ai/run) and
	// token check the gateway uses.
	Account string `toml:"account"`
	// APIKey says where the token is kept ("keychain:ai-gateway-token").
	APIKey string `toml:"api_key"`
	// TokenID charges calls to the token's owner (cf-aig-metadata); it
	// isn't secret.
	TokenID string `toml:"token_id"`
}

// CatalogURL is the account's model catalog, for decision models.
func (g Gateway) CatalogURL() string {
	if g.Account == "" {
		return ""
	}
	return "https://api.cloudflare.com/client/v4/accounts/" + g.Account + "/ai/run"
}

// owns: the url is on this gateway; catalog: on its account's catalog.
func (g Gateway) owns(url string) (on, catalog bool) {
	switch {
	case url == "":
		return false, false
	case g.URL != "" && strings.HasPrefix(url, strings.TrimRight(g.URL, "/")+"/"):
		return true, false
	case g.Account != "" && strings.HasPrefix(url, strings.TrimSuffix(g.CatalogURL(), "/ai/run")+"/"):
		return true, true
	}
	return false, false
}

// Headers are what the gateway wants with a call: who to charge, prompts
// and replies kept out of its logs, and for chat models no response cache
// (a retry after a bad reply gets a new one). Catalog calls name the
// gateway, or Cloudflare answers 402.
func (g Gateway) Headers(catalog bool) map[string]string {
	h := map[string]string{"cf-aig-collect-log-payload": "false"}
	if g.TokenID != "" {
		meta, _ := json.Marshal(map[string]string{"token_id": g.TokenID, "harness": "musubi", "setup_version": "app"})
		h["cf-aig-metadata"] = string(meta)
	}
	if catalog {
		h["cf-aig-gateway-id"] = "ai-gw"
	} else {
		h["cf-aig-skip-cache"] = "true"
	}
	return h
}

// applyGateway gives every model on the gateway the gateway's key and the
// headers it doesn't set itself.
func (c *Config) applyGateway() {
	g := c.Gateway
	if g.URL == "" && g.Account == "" {
		return
	}
	fill := func(url string, key *string, headers *map[string]string) {
		on, catalog := g.owns(url)
		if !on {
			return
		}
		if *key == "" {
			*key = g.APIKey
		}
		for k, v := range g.Headers(catalog) {
			if _, set := (*headers)[k]; set {
				continue
			}
			if *headers == nil {
				*headers = map[string]string{}
			}
			(*headers)[k] = v
		}
	}
	walk(reflect.ValueOf(c).Elem(), func(v reflect.Value) {
		switch m := v.Addr().Interface().(type) {
		case *LLM:
			fill(m.URL, &m.APIKey, &m.Headers)
			if f := m.Fallback; f != nil {
				fill(f.URL, &f.APIKey, &f.Headers)
			}
		case *Classifier:
			fill(m.URL, &m.APIKey, &m.Headers)
		}
	})
}

// walk calls f on every struct field of v, nested ones included, so a
// model section added later is covered without listing it here.
func walk(v reflect.Value, f func(reflect.Value)) {
	for i := 0; i < v.NumField(); i++ {
		fv := v.Field(i)
		if fv.Kind() != reflect.Struct || !v.Type().Field(i).IsExported() {
			continue
		}
		f(fv)
		walk(fv, f)
	}
}

// Section is the model settings at a table's dotted name ("ask.model",
// "knowledge.label.classifier", "llm"): a *LLM or *Classifier, else nil.
func (c *Config) Section(name string) any {
	v := reflect.ValueOf(c).Elem()
	for _, part := range strings.Split(name, ".") {
		found := false
		for i := 0; i < v.NumField(); i++ {
			if strings.Split(v.Type().Field(i).Tag.Get("toml"), ",")[0] == part {
				v, found = v.Field(i), true
				break
			}
		}
		if !found || v.Kind() != reflect.Struct {
			return nil
		}
	}
	switch p := v.Addr().Interface().(type) {
	case *LLM, *Classifier:
		return p
	}
	return nil
}
