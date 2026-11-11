package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

// The Optimizely AI Gateway, as the owner would set it up: prefilled in
// Settings; the config's [gateway] wins once connected.
const (
	defaultGatewayURL     = "https://ai-gateway-ots.common-infrastructure.workers.dev"
	defaultGatewayAccount = "c78fffa1c891319c48494bca14c7ae0b"
	gatewayKeychain       = "ai-gateway-token"
)

// GatewayView is the AI Gateway card in Settings.
type GatewayView struct {
	URL       string `json:"url"`
	Account   string `json:"account"`
	TokenID   string `json:"tokenId"`
	Connected bool   `json:"connected"` // a token is in the keychain
	// Configured: the config has a [gateway] section (else the address and
	// account are the prefilled ones).
	Configured bool `json:"configured"`
}

// GatewayModel is one model the gateway serves that Musubi can use.
type GatewayModel struct {
	ID string `json:"id"`
	// Kind: "chat" (Musubi writes with it) or "decision" (labels and
	// relevance: a probability per option).
	Kind string `json:"kind"`
	// Provider is the config's provider for it: "anthropic", "openai",
	// "jev" or "clef".
	Provider string  `json:"provider"`
	PriceIn  float64 `json:"priceIn"`  // dollars a million tokens; 0 unknown
	PriceOut float64 `json:"priceOut"` // 0 for decision models
	Context  int     `json:"context"`  // tokens; 0 unknown
	Retires  string  `json:"retires"`  // YYYY-MM-DD, or ""
	Note     string  `json:"note,omitempty"`
}

// claudeModels: the gateway serves Claude through /anthropic-shim, which
// doesn't list them; prices from the gateway's pricing/model-pricing.csv
// (2026-11-11), Haiku's for prompts under 100K tokens.
var claudeModels = []GatewayModel{
	{ID: "claude-haiku-5-5", PriceIn: 0.10, PriceOut: 0.50, Context: 1000000, Note: "prompts over 100K tokens cost 5x"},
	{ID: "claude-sonnet-5-5", PriceIn: 2, PriceOut: 10, Context: 1000000},
	{ID: "claude-opus-5-5", PriceIn: 4, PriceOut: 20, Context: 1000000},
}

func (a *App) gatewayConfig() config.Gateway {
	g := config.Gateway{URL: defaultGatewayURL, Account: defaultGatewayAccount, APIKey: "keychain:" + gatewayKeychain}
	if a.eng == nil {
		return g
	}
	c := a.eng.Cfg.Gateway
	if c.URL != "" {
		g.URL = c.URL
	}
	if c.Account != "" {
		g.Account = c.Account
	}
	if c.APIKey != "" {
		g.APIKey = c.APIKey
	}
	g.TokenID = c.TokenID
	return g
}

func keychainService(ref string) string { return strings.TrimPrefix(ref, "keychain:") }

func (a *App) Gateway() GatewayView {
	g := a.gatewayConfig()
	v := GatewayView{URL: g.URL, Account: g.Account, TokenID: g.TokenID}
	v.Configured = a.eng != nil && a.eng.Cfg.Gateway.URL != ""
	_, err := readKeychain(keychainService(g.APIKey))
	v.Connected = err == nil
	return v
}

var (
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,200}$`)
	serviceRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// GatewayConnect checks the token with Cloudflare, keeps it in the
// keychain, and writes the gateway (address, account, token id) to the
// config; the dataset then reopens so every model on the gateway uses it.
func (a *App) GatewayConnect(url, account, token string) error {
	token, url, account = strings.TrimSpace(token), strings.TrimRight(strings.TrimSpace(url), "/"), strings.TrimSpace(account)
	if !tokenRe.MatchString(token) {
		return fmt.Errorf("that doesn't look like a gateway token (cfat_…)")
	}
	if !strings.HasPrefix(url, "https://") || account == "" {
		return fmt.Errorf("the gateway address must start with https:// and the account can't be empty")
	}
	id, err := verifyGatewayToken(a.ctx, account, token)
	if err != nil {
		return err
	}
	g := a.gatewayConfig()
	if err := writeKeychain(keychainService(g.APIKey), token); err != nil {
		return err
	}
	if err := config.EditFile(a.cfgPath, []config.Edit{
		{Section: "gateway", Key: "url", Value: url},
		{Section: "gateway", Key: "account", Value: account},
		{Section: "gateway", Key: "api_key", Value: g.APIKey},
		{Section: "gateway", Key: "token_id", Value: id},
	}); err != nil {
		return err
	}
	return a.reopen("gateway:changed")
}

// GatewayDisconnect deletes the token from the keychain; the config keeps
// the gateway's address, so connecting again needs only a token. Models on
// the gateway fail until then.
func (a *App) GatewayDisconnect() error {
	g := a.gatewayConfig()
	if err := exec.Command("security", "delete-generic-password", "-s", keychainService(g.APIKey)).Run(); err != nil {
		return fmt.Errorf("deleting the token from the keychain: %w", err)
	}
	if a.eng != nil && a.eng.Cfg.Gateway.URL != "" {
		if err := config.EditFile(a.cfgPath, []config.Edit{{Section: "gateway", Key: "token_id", Delete: true}}); err != nil {
			return err
		}
	}
	return a.reopen("gateway:changed")
}

// reopen loads the edited config: the engine and its models are built
// from it at open.
func (a *App) reopen(event string) error {
	a.mu.Lock()
	busy := a.updating || a.running != ""
	a.mu.Unlock()
	if busy {
		a.emit(event, nil)
		return fmt.Errorf("saved; it takes effect after the update that is running (or restart Musubi)")
	}
	err := a.openConfig(a.cfgPath)
	a.emit(event, nil)
	a.emit("app:status", a.Status())
	return err
}

var (
	gwModelsMu   sync.Mutex
	gwModels     []GatewayModel
	gwModelsAt   time.Time
	gwModelsFrom string
)

// GatewayModels lists what Musubi can use on the gateway, with live
// prices, context sizes and planned retirement from Cloudflare's catalog
// (cached ten minutes).
func (a *App) GatewayModels() ([]GatewayModel, error) {
	g := a.gatewayConfig()
	gwModelsMu.Lock()
	defer gwModelsMu.Unlock()
	if gwModels != nil && gwModelsFrom == g.Account && time.Since(gwModelsAt) < 10*time.Minute {
		return gwModels, nil
	}
	token, err := readKeychain(keychainService(g.APIKey))
	if err != nil {
		return nil, fmt.Errorf("not connected")
	}
	cat, err := gatewayCatalog(a.ctx, g.Account, token)
	if err != nil {
		return nil, err
	}
	var out []GatewayModel
	for _, m := range claudeModels {
		m.Kind, m.Provider = "chat", "anthropic"
		out = append(out, m)
	}
	out = append(out, cat...)
	out = append(out, GatewayModel{ID: "typesafe/jev", Kind: "decision", Provider: "jev", Note: "not in the catalog's listing; labels and relevance"})
	gwModels, gwModelsAt, gwModelsFrom = out, time.Now(), g.Account
	return out, nil
}

// gatewayCatalog reads the account's Workers AI text models: chat models
// (OpenAI-shaped through /workers-ai-shim) and Cloudflare's decision models.
func gatewayCatalog(ctx context.Context, account, token string) ([]GatewayModel, error) {
	var out []GatewayModel
	for page := 1; page <= 5; page++ {
		url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/ai/models/search?task=Text%%20Generation&per_page=100&page=%d", account, page)
		var r struct {
			Success bool `json:"success"`
			Result  []struct {
				Name       string `json:"name"`
				Properties []struct {
					ID    string          `json:"property_id"`
					Value json.RawMessage `json:"value"`
				} `json:"properties"`
			} `json:"result"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := cfGet(ctx, url, token, &r); err != nil {
			return nil, err
		}
		if !r.Success {
			return nil, fmt.Errorf("Cloudflare's model catalog: %v", r.Errors)
		}
		for _, m := range r.Result {
			gm := GatewayModel{ID: m.Name, Kind: "chat", Provider: "openai"}
			if strings.HasPrefix(m.Name, "@cf/cloudflare/clef") || m.Name == "@cf/cloudflare/clairic" {
				gm.Kind, gm.Provider = "decision", "clef"
			}
			if m.Name == "@cf/cloudflare/clairic" {
				gm.Note = "not yet tried with Musubi"
			}
			for _, p := range m.Properties {
				var s string
				json.Unmarshal(p.Value, &s)
				switch p.ID {
				case "context_window":
					gm.Context, _ = strconv.Atoi(s)
				case "planned_deprecation_date":
					gm.Retires = s
				case "price":
					var prices []struct {
						Unit  string  `json:"unit"`
						Price float64 `json:"price"`
					}
					json.Unmarshal(p.Value, &prices)
					for _, pr := range prices {
						switch pr.Unit {
						case "per M input tokens":
							gm.PriceIn = pr.Price
						case "per M output tokens":
							gm.PriceOut = pr.Price
						}
					}
				}
			}
			// LoRA bases and guards aren't models to write with.
			if strings.HasSuffix(gm.ID, "-lora") || strings.Contains(gm.ID, "llama-guard") {
				continue
			}
			out = append(out, gm)
		}
		if len(r.Result) < 100 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// verifyGatewayToken asks Cloudflare whether the token is active, and
// returns its id (which charges calls to its owner).
func verifyGatewayToken(ctx context.Context, account, token string) (string, error) {
	var r struct {
		Success bool `json:"success"`
		Result  struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := cfGet(ctx, "https://api.cloudflare.com/client/v4/accounts/"+account+"/tokens/verify", token, &r); err != nil {
		return "", err
	}
	if !r.Success || r.Result.ID == "" {
		return "", fmt.Errorf("Cloudflare doesn't accept this token for account %s", account)
	}
	if r.Result.Status != "active" {
		return "", fmt.Errorf("the token is %s, not active", r.Result.Status)
	}
	return r.Result.ID, nil
}

func cfGet(ctx context.Context, url, token string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "musubi")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reaching Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("Cloudflare refused the token (%s)", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func readKeychain(service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeKeychain keeps the token through security's command mode on stdin,
// so it never appears in a process list. The token is checked against
// tokenRe first, so it needs no quoting.
func writeKeychain(service, token string) error {
	if !serviceRe.MatchString(service) {
		return fmt.Errorf("keychain service %q: use letters, digits, '.', '_' or '-'", service)
	}
	// The account is the Mac user, as docs/setup.md has it made by hand, so
	// -U replaces that entry rather than adding a second one.
	acct := "musubi"
	if u, err := user.Current(); err == nil && serviceRe.MatchString(u.Username) {
		acct = u.Username
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", service, acct, token))
	if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "error") {
		return fmt.Errorf("saving the token in the keychain: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
