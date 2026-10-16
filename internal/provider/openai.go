package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/anielsud/musubi/internal/config"
)

// OpenAI is a chat model behind an OpenAI-compatible API. It is meant for
// llama.cpp's llama-server, which serves the same weights as Ollama but
// batches parallel requests and runs multi-token prediction: on the pilot,
// thinking extraction ran 1.7x faster there than on Ollama's MLX engine,
// which also cannot constrain output to a schema.
type OpenAI struct {
	URL, Model string
	client     *http.Client
}

func NewOpenAI(c config.LLM) *OpenAI {
	// No client timeout: calls carry their own (withRetry), and a thinking
	// pass legitimately runs for minutes.
	return &OpenAI{URL: strings.TrimRight(c.URL, "/"), Model: c.Model, client: &http.Client{}}
}

func (o *OpenAI) ModelName() string { return o.Model }

func (o *OpenAI) Local() bool {
	return strings.Contains(o.URL, "127.0.0.1") || strings.Contains(o.URL, "localhost")
}

// Complete returns the reply. Streaming isn't implemented: onToken gets the
// whole reply at once.
func (o *OpenAI) Complete(ctx context.Context, msgs []Message, opt ChatOptions, onToken func(string)) (string, error) {
	out, err := withRetry(ctx, "openai", opt, func(ctx context.Context) (string, error) { return o.complete(ctx, msgs, opt) })
	if err == nil && onToken != nil {
		onToken(out)
	}
	return out, err
}

func (o *OpenAI) complete(ctx context.Context, msgs []Message, opt ChatOptions) (string, error) {
	body := map[string]any{
		"model":       o.Model,
		"messages":    msgs,
		"temperature": opt.Temperature,
		// llama-server reads this into the chat template; Qwen's template
		// thinks by default, which answers this short don't want.
		"chat_template_kwargs": map[string]any{"enable_thinking": opt.Think},
	}
	if opt.TopP > 0 {
		body["top_p"] = opt.TopP
	}
	if opt.TopK > 0 {
		body["top_k"] = opt.TopK
	}
	if opt.MaxTokens > 0 {
		body["max_tokens"] = opt.MaxTokens
	}
	switch {
	case len(opt.Schema) > 0:
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "out", "schema": opt.Schema}}
	case opt.JSON:
		body["response_format"] = map[string]any{"type": "json_object"}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := o.post(ctx, body, opt.Session, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai: empty reply")
	}
	c := out.Choices[0]
	if c.FinishReason == "length" {
		return c.Message.Content, fmt.Errorf("openai: reply cut off at the token limit")
	}
	return c.Message.Content, nil
}

// Decide reads the next-token distribution, as Chat.Decide does.
func (o *OpenAI) Decide(ctx context.Context, msgs []Message, choices []string) (map[string]float64, error) {
	if err := distinctChoices(choices); err != nil {
		return nil, err
	}
	body := map[string]any{
		"model": o.Model, "messages": msgs, "temperature": 0, "max_tokens": 1,
		"logprobs": true, "top_logprobs": 20,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	var out struct {
		Choices []struct {
			Logprobs struct {
				Content []struct {
					Top []struct {
						Token   string  `json:"token"`
						Logprob float64 `json:"logprob"`
					} `json:"top_logprobs"`
				} `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	if err := o.post(ctx, body, "", &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 || len(out.Choices[0].Logprobs.Content) == 0 {
		return nil, fmt.Errorf("openai: no logprobs in reply (needs a server that supports them)")
	}
	mass := map[string]float64{}
	for _, t := range out.Choices[0].Logprobs.Content[0].Top {
		if ch := matchChoice(t.Token, choices); ch != "" {
			mass[ch] += math.Exp(t.Logprob)
		}
	}
	return shares(mass, choices), nil
}

func (o *OpenAI) post(ctx context.Context, body map[string]any, session string, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", o.URL+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		h := sha256.Sum256([]byte(session))
		req.Header.Set("x-session-affinity", hex.EncodeToString(h[:16]))
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("openai %s: %s", resp.Status, e.Error.Message)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("openai: %w", err)
	}
	return nil
}
