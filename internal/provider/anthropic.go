package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/config"
)

// Anthropic is a Claude model behind an Anthropic Messages endpoint: the
// Optimizely AI Gateway's /anthropic-shim, which forwards to Vertex. It
// shares the hosted client's key, headers, prices and spend ledger.
//
// Claude Haiku 5.5 matched GLM-5.3-Flash on Musubi's prompts at medium
// thinking, two to five times faster (research log 2026-11-11), so medium
// is the default. Sampling settings are not sent: Haiku 5.5 refuses
// non-default ones.
type Anthropic struct {
	// o holds the shared settings. Not embedded: OpenAI's Decide speaks the
	// OpenAI shape, and an Anthropic client must not look like a Decider.
	o *OpenAI
}

var errRefused = errors.New("anthropic: the model declined the request")

// anthropicMaxTokens is the model's own output limit: the cap is required,
// and set where it never cuts a reply (thinking counts against it).
const anthropicMaxTokens = 128000

func NewAnthropic(c config.LLM) *Anthropic { return &Anthropic{NewOpenAI(c)} }

func (a *Anthropic) ModelName() string { return a.o.Model }
func (a *Anthropic) Local() bool       { return a.o.Local() }

func (a *Anthropic) Complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	var lastErr error
	for try := 0; try < hostedTries+hostedThrottleTries; try++ {
		out, err := a.stream(ctx, msgs, opt, onToken)
		switch {
		case err == nil:
			return out, nil
		case ctx.Err() != nil:
			return "", ctx.Err()
		case errors.Is(err, ErrCutOff), errors.Is(err, errRefused):
			// Asking again gets the same answer.
			return "", err
		case !rateLimited(err) && try >= hostedTries-1:
			return "", err
		}
		lastErr = err
		select {
		case <-time.After(time.Duration(5<<min(try, 4)) * time.Second):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

// effort: the configured level (one per model group) wins over a call's
// own hint, which was tuned for other models; medium when neither says.
func (a *Anthropic) effortFor(opt ChatOptions) string {
	return cmpOr(a.o.effort, cmpOr(opt.Effort, "medium"))
}

func (a *Anthropic) stream(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	if err := a.o.checkSpend(); err != nil {
		return "", err
	}
	started := time.Now()
	var system []string
	var turns []map[string]string
	for _, m := range msgs {
		if m.Role == "system" {
			system = append(system, m.Content)
			continue
		}
		turns = append(turns, map[string]string{"role": m.Role, "content": m.Content})
	}
	oc := map[string]any{"effort": a.effortFor(opt)}
	if len(opt.Schema) > 0 {
		var sc any
		if err := json.Unmarshal(opt.Schema, &sc); err != nil {
			return "", err
		}
		oc["format"] = map[string]any{"type": "json_schema", "schema": closeSchema(sc)}
	}
	body := map[string]any{"model": a.o.Model, "max_tokens": anthropicMaxTokens, "stream": true,
		"thinking": map[string]any{"type": "adaptive"}, "output_config": oc, "messages": turns}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	b, _ := json.Marshal(body)
	cctx, cancel := context.WithTimeout(ctx, max(hostedWall, opt.Timeout))
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "POST", a.o.URL+"/v1/messages", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	// Cloudflare refuses some clients' default User-Agent (error 1010).
	req.Header.Set("User-Agent", "musubi")
	if key, err := a.o.resolveKey(); err != nil {
		return "", err
	} else if key != "" {
		req.Header.Set("x-api-key", key)
	}
	for k, v := range a.o.headers {
		req.Header.Set(k, v)
	}
	resp, err := a.o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e bytes.Buffer
		e.ReadFrom(resp.Body)
		return "", fmt.Errorf("anthropic: %s: %s", resp.Status, strings.TrimSpace(e.String()))
	}
	var text strings.Builder
	var used usage
	stop := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(l, "data:") {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *anthropicUsage `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(l, "data:"))), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			used.Prompt = ev.Message.Usage.input()
			used.PromptDetails.Cached = ev.Message.Usage.CacheRead
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				text.WriteString(ev.Delta.Text)
				if onToken != nil {
					onToken(ev.Delta.Text)
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stop = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				used.Completion = ev.Usage.Output
			}
		case "error":
			if ev.Error != nil {
				// Overloaded and rate limits read as such to Complete's retry.
				code := map[string]string{"overloaded_error": "529 ", "rate_limit_error": "429 "}[ev.Error.Type]
				return "", fmt.Errorf("anthropic: %s%s: %s", code, ev.Error.Type, ev.Error.Message)
			}
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("anthropic: stream: %w", err)
	}
	a.o.record(used, stop, time.Since(started), opt.Kind)
	switch stop {
	case "end_turn", "stop_sequence":
	case "max_tokens":
		return "", fmt.Errorf("anthropic: %w at %d tokens", ErrCutOff, anthropicMaxTokens)
	case "refusal":
		return "", errRefused
	case "":
		return "", fmt.Errorf("anthropic: stream ended without a stop reason")
	}
	out := text.String()
	if opt.JSON && len(opt.Schema) == 0 {
		out = stripFence(out)
	}
	return out, nil
}

type anthropicUsage struct {
	Input       int `json:"input_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`
	CacheCreate int `json:"cache_creation_input_tokens"`
	Output      int `json:"output_tokens"`
}

// input is every prompt token, cached or not, as the ledger counts them.
func (u anthropicUsage) input() int { return u.Input + u.CacheRead + u.CacheCreate }

// stripFence takes JSON out of a ```json block: without a schema, Claude
// sometimes fences the JSON it was asked for.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimPrefix(t, "json")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
}
