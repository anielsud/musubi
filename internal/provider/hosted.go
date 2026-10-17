package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Hosted endpoints (an OpenAI-compatible API that isn't on this machine,
// such as Baseten's Model APIs). Lessons from the hosted prototypes
// (tools/insights-eval/bt.py, research log 2026-10-15): a long reply that
// isn't streamed can hang for many minutes, so replies stream, with a cap
// on the wait for each chunk and on the whole reply, then a fresh try;
// reasoning counts against the token cap, so a reply cut off there is
// asked again with twice the room; strict schemas need every object closed.
const (
	hostedChunkWait = 120 * time.Second
	hostedWall      = 180 * time.Second
	hostedTries     = 3
	// hostedMinTokens: hosted reasoning models spend thousands of tokens
	// thinking; caps sized for a local model that doesn't would cut them
	// off.
	hostedMinTokens = 8000
)

// resolveKey reads the key named by api_key once.
func (o *OpenAI) resolveKey() (string, error) {
	if o.apiKey == "" || o.key != "" {
		return o.key, nil
	}
	switch {
	case strings.HasPrefix(o.apiKey, "env:"):
		o.key = os.Getenv(strings.TrimPrefix(o.apiKey, "env:"))
	case strings.HasPrefix(o.apiKey, "keychain:"):
		out, err := exec.Command("security", "find-generic-password", "-s", strings.TrimPrefix(o.apiKey, "keychain:"), "-w").Output()
		if err != nil {
			return "", fmt.Errorf("openai: reading key %s: %w", o.apiKey, err)
		}
		o.key = strings.TrimSpace(string(out))
	default:
		return "", fmt.Errorf("openai: api_key %q: use keychain:<service> or env:<VARIABLE>", o.apiKey)
	}
	if o.key == "" {
		return "", fmt.Errorf("openai: no key found at %s", o.apiKey)
	}
	return o.key, nil
}

func (o *OpenAI) completeHosted(ctx context.Context, msgs []Message, opt ChatOptions) (string, error) {
	maxTokens := max(opt.MaxTokens, hostedMinTokens)
	var lastErr error
	for try := 0; try < hostedTries; try++ {
		out, finish, err := o.stream(ctx, msgs, opt, maxTokens)
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case err != nil:
			lastErr = err
		case finish == "length":
			lastErr = fmt.Errorf("openai: reply cut off at %d tokens", maxTokens)
			maxTokens *= 2
		default:
			return out, nil
		}
	}
	return "", fmt.Errorf("openai: %d tries: %w", hostedTries, lastErr)
}

// Spend metering for hosted calls: with MUSUBI_SPEND_LEDGER set, every call
// appends a row (tag, model, tokens, dollars) to that JSONL file, the
// ledger the hosted prototypes keep (tools/insights-eval/bt.py), and with
// MUSUBI_SPEND_CAP set, a call is refused once the ledger's total reaches
// it.
var ledgerMu sync.Mutex

func ledgerTotal(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var total float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r struct {
			USD float64 `json:"usd"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			total += r.USD
		}
	}
	return total
}

func (o *OpenAI) checkSpend() error {
	path, capS := os.Getenv("MUSUBI_SPEND_LEDGER"), os.Getenv("MUSUBI_SPEND_CAP")
	if path == "" || capS == "" {
		return nil
	}
	var limit float64
	fmt.Sscan(capS, &limit)
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if t := ledgerTotal(path); t >= limit {
		return fmt.Errorf("openai: spend cap $%.2f reached ($%.2f in the ledger)", limit, t)
	}
	return nil
}

type usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Details    struct {
		Reasoning int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	PromptDetails struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (o *OpenAI) record(u usage, finish string, took time.Duration) {
	path := os.Getenv("MUSUBI_SPEND_LEDGER")
	if path == "" {
		return
	}
	usd := (float64(u.Prompt)*o.priceIn + float64(u.Completion)*o.priceOut) / 1e6
	row, _ := json.Marshal(map[string]any{"tag": cmpOr(os.Getenv("MUSUBI_SPEND_TAG"), "musubi") + "-go", "model": o.Model,
		"in": u.Prompt, "cached": u.PromptDetails.Cached, "out": u.Completion, "reasoning": u.Details.Reasoning,
		"usd": usd, "s": took.Round(100 * time.Millisecond).Seconds(), "finish": finish})
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Write(append(row, '\n'))
		f.Close()
	}
}

func (o *OpenAI) stream(ctx context.Context, msgs []Message, opt ChatOptions, maxTokens int) (string, string, error) {
	if err := o.checkSpend(); err != nil {
		return "", "", err
	}
	started := time.Now()
	var used usage
	body := map[string]any{
		"model": o.Model, "messages": msgs, "temperature": opt.Temperature, "max_tokens": maxTokens,
		"stream": true, "stream_options": map[string]any{"include_usage": true},
	}
	if opt.TopP > 0 {
		body["top_p"] = opt.TopP
	}
	if e := cmpOr(opt.Effort, o.effort); e != "" {
		body["reasoning_effort"] = e
	}
	switch {
	case len(opt.Schema) > 0:
		var sc any
		if err := json.Unmarshal(opt.Schema, &sc); err != nil {
			return "", "", err
		}
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "out", "schema": closeSchema(sc), "strict": true}}
	case opt.JSON:
		body["response_format"] = map[string]any{"type": "json_object"}
	}
	b, _ := json.Marshal(body)
	cctx, cancel := context.WithTimeout(ctx, hostedWall)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "POST", o.URL+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key, err := o.resolveKey(); err != nil {
		return "", "", err
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if opt.Session != "" {
		req.Header.Set("x-session-affinity", sessionHash(opt.Session))
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e bytes.Buffer
		e.ReadFrom(resp.Body)
		return "", "", fmt.Errorf("openai: %s: %s", resp.Status, strings.TrimSpace(e.String()))
	}
	lines := make(chan string)
	errc := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-cctx.Done():
				return
			}
		}
		errc <- sc.Err()
		close(lines)
	}()
	var text strings.Builder
	finish := ""
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				if err := <-errc; err != nil {
					return "", "", fmt.Errorf("openai: stream: %w", err)
				}
				o.record(used, finish, time.Since(started))
				return text.String(), finish, nil
			}
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(l, "data:"))
			if data == "[DONE]" {
				o.record(used, finish, time.Since(started))
				return text.String(), finish, nil
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *usage `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			if ev.Usage != nil {
				used = *ev.Usage
			}
			for _, c := range ev.Choices {
				text.WriteString(c.Delta.Content)
				if c.FinishReason != "" {
					finish = c.FinishReason
				}
			}
		case <-time.After(hostedChunkWait):
			return "", "", fmt.Errorf("openai: no reply chunk for %s", hostedChunkWait)
		case <-cctx.Done():
			return "", "", fmt.Errorf("openai: reply still streaming after %s", hostedWall)
		}
	}
}

// closeSchema makes a JSON schema strict: every object lists all its
// properties as required and allows no others.
func closeSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = closeSchema(val)
		}
		if out["type"] == "object" {
			if props, ok := out["properties"].(map[string]any); ok {
				var req []string
				for k := range props {
					req = append(req, k)
				}
				out["required"] = sortedStrings(req)
			}
			out["additionalProperties"] = false
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = closeSchema(val)
		}
		return out
	}
	return v
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
