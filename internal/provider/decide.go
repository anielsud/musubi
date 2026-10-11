package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
)

// Decider answers a closed question with a probability per choice, read
// from the model's next-token distribution in one forward pass instead of
// generating JSON. The probabilities are calibrated enough to threshold on
// (fail closed below a cut-off), which a generated label is not.
type Decider interface {
	Decide(ctx context.Context, msgs []Message, choices []string) (map[string]float64, error)
}

// Choices must start with distinct first tokens; plain words ("yes", "no",
// "same", "unrelated") do. Matching is on the lower-cased, trimmed token, so
// "Yes", " yes" and "yes" all count towards "yes".
func (c *Chat) Decide(ctx context.Context, msgs []Message, choices []string) (map[string]float64, error) {
	if err := distinctChoices(choices); err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":        c.Model,
		"messages":     msgs,
		"stream":       false,
		"think":        false,
		"keep_alive":   "30m",
		"logprobs":     true,
		"top_logprobs": 20,
		"options":      map[string]any{"temperature": 0, "num_ctx": 8192, "num_predict": 1},
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Error    string
		Logprobs []struct {
			Top []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"logprobs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		return nil, fmt.Errorf("ollama %s: %s", resp.Status, out.Error)
	}
	if len(out.Logprobs) == 0 {
		return nil, fmt.Errorf("ollama: no logprobs in reply (needs a server that supports them)")
	}
	mass := map[string]float64{}
	for _, t := range out.Logprobs[0].Top {
		if ch := matchChoice(t.Token, choices); ch != "" {
			mass[ch] += math.Exp(t.Logprob)
		}
	}
	return shares(mass, choices), nil
}

func distinctChoices(choices []string) error {
	if len(choices) < 2 {
		return fmt.Errorf("decide: need at least two choices")
	}
	for i, a := range choices {
		for _, b := range choices[i+1:] {
			if strings.HasPrefix(strings.ToLower(a), strings.ToLower(b)) || strings.HasPrefix(strings.ToLower(b), strings.ToLower(a)) {
				return fmt.Errorf("decide: choices %q and %q are ambiguous", a, b)
			}
		}
	}
	return nil
}

// matchChoice maps a token to the choice it starts. A token shorter than the
// choice ("un" for "unrelated") counts when it is an unambiguous prefix.
func matchChoice(tok string, choices []string) string {
	t := strings.ToLower(strings.TrimSpace(tok))
	if t == "" {
		return ""
	}
	found := ""
	for _, ch := range choices {
		if strings.HasPrefix(strings.ToLower(ch), t) || strings.HasPrefix(t, strings.ToLower(ch)) {
			if found != "" {
				return ""
			}
			found = ch
		}
	}
	return found
}

// shares renormalises over the choices. When none of the top tokens is a
// choice the model did not answer the question; every choice gets 0 so a
// threshold on any of them fails closed.
func shares(mass map[string]float64, choices []string) map[string]float64 {
	total := 0.0
	for _, v := range mass {
		total += v
	}
	p := make(map[string]float64, len(choices))
	for _, ch := range choices {
		if total > 0 {
			p[ch] = mass[ch] / total
		} else {
			p[ch] = 0
		}
	}
	return p
}

// Decide replays (and records) decisions like Complete.
func (c *CachedLLM) Decide(ctx context.Context, msgs []Message, choices []string) (map[string]float64, error) {
	k := key(c.Model, "decide", msgs, choices)
	if v, ok := c.file.get(k); ok {
		var p map[string]float64
		return p, json.Unmarshal([]byte(v), &p)
	}
	d, ok := c.Inner.(Decider)
	if !c.Record || !ok {
		return nil, fmt.Errorf("no recorded decision for this request (key %s); run `make eval-record`", k)
	}
	p, err := d.Decide(ctx, msgs, choices)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(p)
	c.file.put(k, string(b))
	return p, nil
}
