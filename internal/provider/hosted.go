package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// hostedWall bounds a reply that keeps streaming; a stalled one is
	// cut by hostedChunkWait. At 3 minutes, slow but steady replies were
	// cut and restarted from scratch (research log 2026-10-19).
	hostedWall  = 6 * time.Minute
	hostedTries = 3
	// hostedMinTokens: hosted reasoning models spend thousands of tokens
	// thinking; caps sized for a local model that doesn't would cut them
	// off. Of 5,771 threading calls on GLM-5.3-Flash, reasoning reached
	// 7.2k at p99 and 12.7k at p99.9; 8k cut 2% of calls, which then cost
	// a second, longer call (research log 2026-10-19). A cap costs nothing
	// unless it is used.
	hostedMinTokens = 16000
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

func (o *OpenAI) completeHosted(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	maxTokens := max(opt.MaxTokens, hostedMinTokens)
	var lastErr error
	cut := false
	for try := 0; try < hostedTries; try++ {
		o2 := opt
		if cut {
			// The cap doesn't change how long the model reasons; it only
			// cuts it off, and no reply has finished past ~15k. A reply
			// cut at the cap is past everything that converged (research
			// log 2026-10-19), so it is asked once more with low reasoning
			// effort, which shortens the reasoning, and then given up.
			o2.Effort = "low"
		}
		out, finish, err := o.hedged(ctx, msgs, o2, maxTokens, onToken)
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case err != nil:
			lastErr = err
		case finish == "length" && cut:
			return "", fmt.Errorf("openai: %w at %d tokens, also with low reasoning effort", ErrCutOff, maxTokens)
		case finish == "length":
			lastErr = fmt.Errorf("openai: reply cut off at %d tokens", maxTokens)
			cut = true
		default:
			return out, nil
		}
	}
	return "", fmt.Errorf("openai: %d tries: %w", hostedTries, lastErr)
}

// ErrCutOff: the reply ran to its cap twice. Callers that can do without
// one reply (one thread's update) skip it rather than fail.
var ErrCutOff = errors.New("reply cut off")

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

// stream sends one request and reads the reply as it streams, passing
// each piece of text to onToken (nil: none).
func (o *OpenAI) stream(ctx context.Context, msgs []Message, opt ChatOptions, maxTokens int, onToken func(string)) (string, string, error) {
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
	if maxTokens <= 0 {
		delete(body, "max_tokens")
	}
	// A long reply needs time: about 55 tokens a second hosted.
	wall := max(hostedWall, time.Duration(maxTokens/40)*time.Second)
	if o.Local() {
		// llama-server: the same settings as a call that isn't streamed.
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": opt.Think}
		if opt.TopK > 0 {
			body["top_k"] = opt.TopK
		}
		wall = max(wall, opt.Timeout)
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
	cctx, cancel := context.WithTimeout(ctx, wall)
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
	// A stream cut off (a backup request won, or the caller gave up) is
	// billed for what it generated: estimate it from the chunks seen (about
	// one token each, reasoning included) and the request's size.
	chunks, recorded := 0, false
	defer func() {
		if !recorded && chunks > 0 {
			o.record(usage{Prompt: len(b) / 4, Completion: chunks}, "cut", time.Since(started))
		}
	}()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				if err := <-errc; err != nil {
					return "", "", fmt.Errorf("openai: stream: %w", err)
				}
				o.record(used, finish, time.Since(started))
				recorded = true
				return text.String(), finish, nil
			}
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(l, "data:"))
			chunks++
			if data == "[DONE]" {
				o.record(used, finish, time.Since(started))
				recorded = true
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
				if onToken != nil && c.Delta.Content != "" {
					onToken(c.Delta.Content)
				}
				if c.FinishReason != "" {
					finish = c.FinishReason
				}
			}
		case <-time.After(hostedChunkWait):
			return "", "", fmt.Errorf("openai: no reply chunk for %s", hostedChunkWait)
		case <-cctx.Done():
			if ctx.Err() != nil {
				return "", "", ctx.Err() // the caller stopped it
			}
			return "", "", fmt.Errorf("openai: reply still streaming after %s", wall)
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

// hedged sends a request and, if it hasn't finished after hedgeAfter, the
// same request again, taking whichever finishes first: a few calls run
// many times the median (research log 2026-10-19), and a meeting's round
// of calls waits for its slowest. Off (hedgeAfter 0) or when streaming to
// a caller, it is one request.
func (o *OpenAI) hedged(ctx context.Context, msgs []Message, opt ChatOptions, maxTokens int, onToken func(string)) (string, string, error) {
	if o.hedgeAfter <= 0 || onToken != nil {
		return o.stream(ctx, msgs, opt, maxTokens, onToken)
	}
	type res struct {
		out, finish string
		err         error
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops the loser
	results := make(chan res, 2)
	send := func() {
		go func() {
			out, finish, err := o.stream(cctx, msgs, opt, maxTokens, nil)
			results <- res{out, finish, err}
		}()
	}
	send()
	timer := time.NewTimer(o.hedgeAfter)
	defer timer.Stop()
	inFlight, hedgedOnce := 1, false
	var failed *res
	for {
		select {
		case <-timer.C:
			if !hedgedOnce {
				hedgedOnce = true
				inFlight++
				send()
			}
		case r := <-results:
			inFlight--
			if r.err == nil && r.finish != "length" {
				return r.out, r.finish, nil
			}
			if failed == nil {
				failed = &r
			}
			// Failed (or cut off at the cap): the caller retries; wait only
			// if a backup is still running.
			if inFlight == 0 {
				return failed.out, failed.finish, failed.err
			}
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
}
