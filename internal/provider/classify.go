package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Classifier answers several typed questions about one input in one call:
// a choice from named options, a score on ordered levels, or a yes/no
// probability ("noul"). It is the shape of a typed classification model
// (TypeSafe's Jev) and of the same questions asked of a language model one
// forward pass each (DeciderClassifier), so the pipeline never depends on
// one vendor.
type Classifier interface {
	Classify(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, error)
	Name() string
}

// Question is one typed question. Choice needs Options (name → description,
// "" for none); Score needs Levels, lowest first.
type Question struct {
	Type         string            `json:"type"` // choice, score, noul
	Instructions string            `json:"instructions"`
	Options      map[string]string `json:"-"`
	Levels       []string          `json:"-"`
}

// Answer is a question's answer: Choice (with Probabilities over the
// options), Score (the probability-weighted level, 1 = lowest) or Noul (the
// probability of yes); Confidence as the model reports it.
type Answer struct {
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// Jev calls TypeSafe's System One endpoint, or Cloudflare's Clef, which
// takes the same questions and gives the same answers (NewClef). Input is
// metered for the spend ledger (output is free).
type Jev struct {
	URL, Model string
	APIKey     string // keychain:<service> or env:<VARIABLE>
	PriceIn    float64
	// Endpoint is where requests go: URL's /v1/systemone, or Clef's URL.
	Endpoint string
	Headers  map[string]string
	// kind names the provider in errors ("jev", "clef").
	kind    string
	keyOnce sync.Once
	key     string
	keyErr  error
	client  *http.Client
}

func NewJev(url, model, apiKey string, priceIn float64) *Jev {
	u := strings.TrimRight(cmpOr(url, "https://api.typesafe.ai"), "/")
	return &Jev{URL: u, Endpoint: u + "/v1/systemone", Model: cmpOr(model, "jev-latest"), APIKey: apiKey,
		PriceIn: cmpOrF(priceIn, 0.042), kind: "jev", client: &http.Client{Timeout: 60 * time.Second}}
}

// NewClef calls Cloudflare's Clef at url, its Workers AI /ai/run address
// (https://api.cloudflare.com/client/v4/accounts/<account>/ai/run/@cf/cloudflare/clef),
// with a Cloudflare or AI Gateway token. It is Jev's request; the answer
// comes wrapped in Cloudflare's {"result": ...}. Input is $0.24 a million
// tokens (clef-flash $0.09), output free.
func NewClef(url, model, apiKey string, priceIn float64, headers map[string]string) *Jev {
	u := strings.TrimRight(url, "/")
	model = cmpOr(model, "clef")
	price := 0.24
	if model == "clef-flash" {
		price = 0.09
	}
	return &Jev{URL: u, Endpoint: u, Model: model, APIKey: apiKey, PriceIn: cmpOrF(priceIn, price),
		Headers: headers, kind: "clef", client: &http.Client{Timeout: 60 * time.Second}}
}

// NewClassifier makes the classifier a config names: "jev" or "clef".
func NewClassifier(provider, url, model, apiKey string, priceIn float64, headers map[string]string) (*Jev, error) {
	switch provider {
	case "jev":
		j := NewJev(url, model, apiKey, priceIn)
		j.Headers = headers
		return j, nil
	case "clef":
		if url == "" {
			return nil, fmt.Errorf("clef needs url: its /ai/run address (…/accounts/<account>/ai/run/@cf/cloudflare/clef)")
		}
		return NewClef(url, model, apiKey, priceIn, headers), nil
	}
	return nil, fmt.Errorf("unknown classifier provider %q", provider)
}

func (j *Jev) Name() string { return j.Model }

func (j *Jev) Classify(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, error) {
	// Read once, by whichever of the concurrent callers comes first.
	j.keyOnce.Do(func() { j.key, j.keyErr = readKey(j.APIKey) })
	if j.keyErr != nil {
		return nil, j.keyErr
	}
	if err := spendAllowed(); err != nil {
		return nil, err
	}
	qs := map[string]any{}
	for id, q := range questions {
		m := map[string]any{"type": q.Type, "instructions": q.Instructions}
		switch q.Type {
		case "choice":
			c := map[string]any{}
			for name, d := range q.Options {
				if d == "" {
					c[name] = nil
				} else {
					c[name] = d
				}
			}
			m["criteria"] = c
		case "score":
			m["criteria"] = q.Levels
		}
		qs[id] = m
	}
	b, _ := json.Marshal(map[string]any{"model": j.Model, "state": state, "questions": qs})
	var lastErr error
	for try := 0; try < 4; try++ {
		if try > 0 {
			select {
			case <-time.After(time.Duration(1<<try) * 500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		t0 := time.Now()
		req, err := http.NewRequestWithContext(ctx, "POST", j.Endpoint, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+j.key)
		for k, v := range j.Headers {
			req.Header.Set(k, v)
		}
		resp, err := j.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Rate limited or overloaded: back off and try again.
		if resp.StatusCode == 429 || resp.StatusCode == 529 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s: %s", j.kind, resp.Status)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: %s: %s", j.kind, resp.Status, strings.TrimSpace(string(body)))
		}
		var out struct {
			Answers map[string]struct {
				Choice        string             `json:"choice"`
				Probabilities map[string]float64 `json:"probabilities"`
				Score         float64            `json:"score"`
				Noul          float64            `json:"noul"`
				Confidence    float64            `json:"confidence"`
			} `json:"answers"`
			Usage struct {
				In  int `json:"input_tokens"`
				Out int `json:"output_tokens"`
			} `json:"usage"`
		}
		// Cloudflare wraps the answer: {"result": {...}, "success": true}.
		var wrap struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(body, &wrap) == nil && len(wrap.Result) > 0 && wrap.Result[0] == '{' {
			body = wrap.Result
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("%s reply: %w", j.kind, err)
		}
		recordRow(j.Model, out.Usage.In, out.Usage.Out, float64(out.Usage.In)*j.PriceIn/1e6, time.Since(t0), "classify")
		ans := map[string]Answer{}
		for id, a := range out.Answers {
			ans[id] = Answer{Choice: a.Choice, Probabilities: a.Probabilities, Score: a.Score, Noul: a.Noul, Confidence: a.Confidence}
		}
		for id := range questions {
			if _, ok := ans[id]; !ok {
				return nil, fmt.Errorf("%s: no answer to %q", j.kind, id)
			}
		}
		return ans, nil
	}
	return nil, fmt.Errorf("%s: 4 tries: %w", j.kind, lastErr)
}

// DeciderClassifier asks each question of a language model as one forward
// pass (Decider): the same typed questions without a classification model.
type DeciderClassifier struct {
	D    Decider
	Desc string
}

func (d DeciderClassifier) Name() string { return d.Desc }

func (d DeciderClassifier) Classify(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, error) {
	in, _ := json.MarshalIndent(state, "", " ")
	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := map[string]Answer{}
	for _, id := range ids {
		q := questions[id]
		var choices []string
		var lines []string
		switch q.Type {
		case "choice":
			for name := range q.Options {
				choices = append(choices, name)
			}
			sort.Strings(choices)
			for _, c := range choices {
				if q.Options[c] != "" {
					lines = append(lines, "- "+c+": "+q.Options[c])
				} else {
					lines = append(lines, "- "+c)
				}
			}
		case "score":
			for i, l := range q.Levels {
				choices = append(choices, strconv.Itoa(i+1))
				lines = append(lines, fmt.Sprintf("- %d: %s", i+1, l))
			}
		case "noul":
			choices = []string{"yes", "no"}
		default:
			return nil, fmt.Errorf("unknown question type %q", q.Type)
		}
		sys := q.Instructions
		if len(lines) > 0 {
			sys += "\n\nAnswer with one of:\n" + strings.Join(lines, "\n")
		} else {
			sys += "\n\nAnswer yes or no."
		}
		p, err := d.D.Decide(ctx, []Message{{Role: "system", Content: sys}, {Role: "user", Content: string(in) + "\nOne word:"}}, choices)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		a := Answer{Probabilities: p}
		switch q.Type {
		case "noul":
			a.Noul = p["yes"]
			a.Confidence = math.Max(p["yes"], p["no"])
		case "score":
			for i, c := range choices {
				a.Score += float64(i+1) * p[c]
			}
		}
		best := 0.0
		for _, c := range choices {
			if p[c] > best {
				a.Choice, best = c, p[c]
			}
		}
		if q.Type != "noul" {
			a.Confidence = best
		}
		out[id] = a
	}
	return out, nil
}

// readKey reads a key named as keychain:<service> or env:<VARIABLE>.
func readKey(ref string) (string, error) {
	var k string
	switch {
	case strings.HasPrefix(ref, "env:"):
		k = os.Getenv(strings.TrimPrefix(ref, "env:"))
	case strings.HasPrefix(ref, "keychain:"):
		out, err := exec.Command("security", "find-generic-password", "-s", strings.TrimPrefix(ref, "keychain:"), "-w").Output()
		if err != nil {
			return "", fmt.Errorf("reading key %s: %w", ref, err)
		}
		k = strings.TrimSpace(string(out))
	default:
		return "", fmt.Errorf("api_key %q: use keychain:<service> or env:<VARIABLE>", ref)
	}
	if k == "" {
		return "", fmt.Errorf("no key found at %s", ref)
	}
	return k, nil
}

// spendAllowed refuses a metered call once the ledger reaches the cap
// (MUSUBI_SPEND_CAP), as hosted chat calls are refused.
func spendAllowed() error {
	path, capS := os.Getenv("MUSUBI_SPEND_LEDGER"), os.Getenv("MUSUBI_SPEND_CAP")
	if path == "" || capS == "" {
		return nil
	}
	cp, err := strconv.ParseFloat(capS, 64)
	if err != nil {
		return nil
	}
	ledgerMu.Lock()
	total := ledgerTotal(path)
	ledgerMu.Unlock()
	if total >= cp {
		return fmt.Errorf("spend cap reached: $%.2f of $%.2f", total, cp)
	}
	return nil
}

// recordRow appends one metered call to the spend ledger.
func recordRow(model string, in, out int, usd float64, took time.Duration, kind string) {
	path := os.Getenv("MUSUBI_SPEND_LEDGER")
	if path == "" {
		return
	}
	row, _ := json.Marshal(map[string]any{"tag": cmpOr(os.Getenv("MUSUBI_SPEND_TAG"), "musubi") + "-go", "model": model,
		"in": in, "out": out, "usd": usd, "s": took.Round(10 * time.Millisecond).Seconds(), "finish": "stop", "kind": kind})
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.Write(append(row, '\n'))
		f.Close()
	}
}
