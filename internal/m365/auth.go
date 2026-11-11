// Package m365 reads the owner's Microsoft 365 data (calendar, Teams
// transcripts, directory) through Microsoft Graph, read-only.
//
// It signs in as Microsoft Graph Command Line Tools, the public client
// behind Microsoft's Graph PowerShell module, which the tenant has already
// consented: no app registration of Musubi's own is needed. The sign-in is
// the standard browser flow (authorization code with PKCE, a loopback
// redirect), the same one the PowerShell module runs, so Entra records it
// the same way. Only the refresh token is kept, in the Keychain; access
// tokens live in memory. See docs/teams-design.md.
package m365

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ClientID is Microsoft Graph Command Line Tools.
const ClientID = "14d82eec-204b-4c2f-b7e8-296a70dab67e"

const graphResource = "https://graph.microsoft.com"

// graphScopes are the read scopes Musubi uses. Entra issues every scope the
// tenant consented to this client whatever is asked for (write scopes too),
// which is why the client refuses anything but GET on an allow-list.
var graphScopes = []string{
	"Calendars.Read", "OnlineMeetings.Read", "OnlineMeetingTranscript.Read.All",
	"Chat.Read", "Mail.Read", "Files.Read.All", "Sites.Read.All", "User.Read", "User.ReadBasic.All", "User.Read.All", "People.Read",
}

var (
	// ErrNotConnected: no sign-in stored.
	ErrNotConnected = errors.New("not connected to Microsoft 365")
	// ErrAuthRequired: the stored sign-in no longer works (expired, revoked,
	// or a policy asks for a fresh sign-in). Connect again.
	ErrAuthRequired = errors.New("Microsoft 365 sign-in required")
)

// Account is who is signed in.
type Account struct {
	Username    string    `json:"username"`
	Name        string    `json:"name"`
	TenantID    string    `json:"tenantId"`
	ConnectedAt time.Time `json:"connectedAt"`
}

type stored struct {
	Account
	Refresh string `json:"refresh"`
}

type accessToken struct {
	value   string
	expires time.Time
}

// Auth signs in and hands out access tokens.
type Auth struct {
	// Authority is the Entra endpoint; tests point it at a fake.
	Authority string
	Secrets   Secrets
	HTTP      *http.Client
	// OpenBrowser shows the sign-in page.
	OpenBrowser func(string) error

	mu     sync.Mutex
	st     *stored
	loaded bool
	tokens map[string]accessToken
}

// NewAuth signs in against Entra and keeps the sign-in in the Keychain.
func NewAuth() *Auth {
	return &Auth{Authority: "https://login.microsoftonline.com", Secrets: Keychain{Service: "Musubi.M365.v2", Account: "default"}, HTTP: &http.Client{Timeout: 60 * time.Second}, OpenBrowser: openBrowser}
}

func (a *Auth) load() (*stored, error) {
	if a.loaded {
		return a.st, nil
	}
	b, err := a.Secrets.Get()
	if errors.Is(err, ErrNotConnected) {
		a.loaded = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s stored
	if err := json.Unmarshal(b, &s); err != nil || s.Refresh == "" {
		a.loaded = true
		return nil, nil // unreadable: treat as signed out
	}
	a.st, a.loaded = &s, true
	return a.st, nil
}

func (a *Auth) save(s *stored) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := a.Secrets.Put(b); err != nil {
		return err
	}
	a.st, a.loaded = s, true
	return nil
}

// Account is the stored sign-in's account, or nil.
func (a *Auth) Account() (*Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := a.load()
	if err != nil || s == nil {
		return nil, err
	}
	acc := s.Account
	return &acc, nil
}

// SignOut forgets the sign-in: the Keychain entry and any access tokens.
// The refresh token can't be revoked from here; the account's sign-in
// sessions page ends it at Microsoft's end.
func (a *Auth) SignOut() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st, a.loaded, a.tokens = nil, true, nil
	if err := a.Secrets.Delete(); err != nil && !errors.Is(err, ErrNotConnected) {
		return err
	}
	return nil
}

// Connect runs the browser sign-in and stores the result. It waits until
// the browser comes back to the loopback address or ctx ends.
func (a *Auth) Connect(ctx context.Context) (*Account, error) {
	verifier, err := randomString(48)
	if err != nil {
		return nil, err
	}
	state, err := randomString(24)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	// The client's registered redirect is http://localhost; Entra accepts
	// any port on it for a public client.
	redirect := fmt.Sprintf("http://localhost:%d/", ln.Addr().(*net.TCPAddr).Port)

	type result struct{ code, err string }
	got := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "unexpected sign-in response", http.StatusBadRequest)
			return
		}
		res := result{code: q.Get("code")}
		if e := q.Get("error"); e != "" {
			res.err = e + ": " + q.Get("error_description")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		msg := "Musubi is connected to Microsoft 365. You can close this tab."
		if res.err != "" {
			msg = "Sign-in didn't complete: " + res.err
		}
		fmt.Fprintf(w, "<!doctype html><title>Musubi</title><body style=\"font-family:-apple-system,sans-serif;padding:3em\"><p>%s</p></body>", html.EscapeString(msg))
		select {
		case got <- res:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	hint := ""
	a.mu.Lock()
	if s, _ := a.load(); s != nil {
		hint = s.Username
	}
	a.mu.Unlock()
	q := url.Values{
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"response_mode":         {"query"},
		"scope":                 {scopeString(graphResource, graphScopes)},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if hint != "" {
		q.Set("login_hint", hint)
	}
	if err := a.OpenBrowser(a.Authority + "/organizations/oauth2/v2.0/authorize?" + q.Encode()); err != nil {
		return nil, fmt.Errorf("open the sign-in page: %w", err)
	}

	var res result
	select {
	case res = <-got:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != "" {
		return nil, fmt.Errorf("sign-in: %s", res.err)
	}
	tr, err := a.tokenRequest(ctx, "organizations", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
		"scope":         {scopeString(graphResource, graphScopes)},
	})
	if err != nil {
		return nil, err
	}
	claims := idClaims(tr.IDToken)
	s := &stored{Account: Account{Username: claims.PreferredUsername, Name: claims.Name, TenantID: claims.TID, ConnectedAt: time.Now()}, Refresh: tr.RefreshToken}
	if s.Refresh == "" {
		return nil, errors.New("sign-in returned no refresh token")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.save(s); err != nil {
		return nil, err
	}
	a.tokens = map[string]accessToken{graphResource: {tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}}
	acc := s.Account
	return &acc, nil
}

// Token is an access token for resource ("https://graph.microsoft.com" or
// a SharePoint origin), refreshed from the stored sign-in when needed.
func (a *Auth) Token(ctx context.Context, resource string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[resource]; ok && time.Until(t.expires) > 2*time.Minute {
		return t.value, nil
	}
	s, err := a.load()
	if err != nil {
		return "", err
	}
	if s == nil {
		return "", ErrNotConnected
	}
	scope := resource + "/.default offline_access"
	if resource == graphResource {
		scope = scopeString(graphResource, graphScopes)
	}
	tr, err := a.tokenRequest(ctx, s.TenantID, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.Refresh}, "scope": {scope}})
	if err != nil {
		return "", err
	}
	if tr.RefreshToken != "" && tr.RefreshToken != s.Refresh {
		ns := *s
		ns.Refresh = tr.RefreshToken
		if err := a.save(&ns); err != nil {
			return "", err
		}
	}
	if a.tokens == nil {
		a.tokens = map[string]accessToken{}
	}
	a.tokens[resource] = accessToken{tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)}
	return tr.AccessToken, nil
}

// forget drops a cached access token (after a 401) so the next call
// refreshes it.
func (a *Auth) forget(resource string) {
	a.mu.Lock()
	delete(a.tokens, resource)
	a.mu.Unlock()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (a *Auth) tokenRequest(ctx context.Context, tenant string, form url.Values) (*tokenResponse, error) {
	if tenant == "" {
		tenant = "organizations"
	}
	form.Set("client_id", ClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Authority+"/"+url.PathEscape(tenant)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("token response (%s): %w", resp.Status, err)
	}
	if tr.Error != "" || tr.AccessToken == "" {
		switch tr.Error {
		case "invalid_grant", "interaction_required", "login_required", "consent_required", "invalid_client":
			return nil, fmt.Errorf("%w (%s)", ErrAuthRequired, firstLine(tr.Description))
		}
		return nil, fmt.Errorf("token request: %s %s", tr.Error, firstLine(tr.Description))
	}
	return &tr, nil
}

type claims struct {
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	TID               string `json:"tid"`
}

// idClaims reads the ID token's display claims. It isn't verified: it
// came straight from the token endpoint over TLS, and only names the
// account and its tenant.
func idClaims(tok string) claims {
	var c claims
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return c
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func scopeString(resource string, scopes []string) string {
	out := make([]string, 0, len(scopes)+3)
	for _, s := range scopes {
		out = append(out, resource+"/"+s)
	}
	return strings.Join(append(out, "offline_access", "openid", "profile"), " ")
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
