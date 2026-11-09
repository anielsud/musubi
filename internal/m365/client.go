package m365

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// allowed are the only requests the client makes: GETs on these paths.
// The token can do far more (see graphScopes), so anything else is
// refused before it is sent.
var allowed = []*regexp.Regexp{
	regexp.MustCompile(`^/v1\.0/me$`),
	regexp.MustCompile(`^/v1\.0/me/calendarView$`),
	regexp.MustCompile(`^/v1\.0/me/onlineMeetings$`),
	regexp.MustCompile(`^/v1\.0/me/onlineMeetings/[^/]+/transcripts$`),
	regexp.MustCompile(`^/v1\.0/me/onlineMeetings/[^/]+/transcripts/[^/]+/content$`),
	regexp.MustCompile(`^/v1\.0/chats/[^/]+/messages$`),
	regexp.MustCompile(`^/v1\.0/shares/[^/]+/driveItem$`),
	regexp.MustCompile(`^/v1\.0/users/[^/]+$`),
	regexp.MustCompile(`^/v1\.0/users/[^/]+/manager$`),
	regexp.MustCompile(`^/v1\.0/users/[^/]+/photos/[^/]+/\$value$`),
	// SharePoint's own API, for transcripts kept with a recording.
	regexp.MustCompile(`^/_api/v2\.1/drives/[^/]+/items/[^/]+/media/transcripts$`),
	regexp.MustCompile(`^/_api/v2\.1/drives/[^/]+/items/[^/]+/media/transcripts/[^/]+/streamContent$`),
}

// Allowed reports whether a GET of rawURL is one the client may make.
func Allowed(rawURL string, graphBase string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" && !strings.HasPrefix(graphBase, "http://") {
		return false
	}
	g, _ := url.Parse(graphBase)
	isGraph := g != nil && u.Host == g.Host
	isSharePoint := strings.HasSuffix(u.Host, ".sharepoint.com")
	if !isGraph && !isSharePoint {
		return false
	}
	p := u.EscapedPath()
	if isGraph != strings.HasPrefix(p, "/v1.0/") {
		return false
	}
	for _, re := range allowed {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// Client makes read-only Graph and SharePoint requests.
type Client struct {
	Auth *Auth
	// Graph is the Graph origin; tests point it at a fake.
	Graph string
	HTTP  *http.Client
}

func NewClient(a *Auth) *Client {
	return &Client{Auth: a, Graph: graphResource, HTTP: &http.Client{Timeout: 120 * time.Second}}
}

// Error is a Graph error response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("graph %d %s: %s", e.Status, e.Code, e.Message) }

// Get fetches path (relative to Graph, or a full Graph or SharePoint URL)
// and returns the body. 429 and 503 are retried after the time the
// service asks for, up to three times; a 401 refreshes the token once.
func (c *Client) Get(ctx context.Context, path string, header http.Header) ([]byte, error) {
	full := path
	if strings.HasPrefix(path, "/") {
		full = c.Graph + path
	}
	if !Allowed(full, c.Graph) {
		return nil, fmt.Errorf("m365: request not allowed: %s", redactQuery(full))
	}
	u, _ := url.Parse(full)
	resource := graphResource
	if u.Host != hostOf(c.Graph) {
		resource = "https://" + u.Host // SharePoint, whose API takes its own token
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		tok, err := c.Auth.Token(ctx, resource)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode < 300:
			return body, nil
		case resp.StatusCode == http.StatusUnauthorized && !refreshed:
			c.Auth.forget(resource)
			refreshed = true
			continue
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < 3:
			wait := 5 * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s < 120 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		ge := &Error{Status: resp.StatusCode}
		var env struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(body, &env) == nil {
			ge.Code, ge.Message = env.Error.Code, env.Error.Message
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w (%s)", ErrAuthRequired, ge.Message)
		}
		return nil, ge
	}
}

// GetJSON fetches path and decodes it into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	b, err := c.Get(ctx, path, http.Header{"Prefer": {`outlook.timezone="UTC"`}})
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// Pages fetches path and every @odata.nextLink after it, calling each
// with the page's raw value array.
func (c *Client) Pages(ctx context.Context, path string, each func(json.RawMessage) error) error {
	for path != "" {
		var page struct {
			Value []json.RawMessage `json:"value"`
			Next  string            `json:"@odata.nextLink"`
		}
		if err := c.GetJSON(ctx, path, &page); err != nil {
			return err
		}
		for _, v := range page.Value {
			if err := each(v); err != nil {
				return err
			}
		}
		path = page.Next
	}
	return nil
}

// IsNotFound reports a Graph 404, or a Teams lookup refusal (3003/3004),
// which mean "nothing to read here" rather than a failure.
func IsNotFound(err error) bool {
	var ge *Error
	if !errors.As(err, &ge) {
		return false
	}
	return ge.Status == http.StatusNotFound || ge.Status == http.StatusForbidden || strings.HasPrefix(ge.Message, "3003") || strings.HasPrefix(ge.Message, "3004")
}

func hostOf(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Host
}

func redactQuery(s string) string {
	if i := strings.IndexByte(s, '?'); i >= 0 {
		return s[:i]
	}
	return s
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}
