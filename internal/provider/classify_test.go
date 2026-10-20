package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One request carries every question; the reply's answers come back by id
// and the input tokens are metered.
func TestJevClassify(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad", 401)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"answers":{"sens":{"type":"choice","choice":"pay","probabilities":{"pay":0.9,"none":0.1},"confidence":0.9},
			"quiet":{"type":"noul","noul":0.2}},"usage":{"input_tokens":1000000,"output_tokens":3}}`)
	}))
	defer srv.Close()
	ledger := filepath.Join(t.TempDir(), "spend.jsonl")
	t.Setenv("MUSUBI_SPEND_LEDGER", ledger)
	t.Setenv("K", "k")
	j := NewJev(srv.URL, "", "env:K", 0)
	a, err := j.Classify(context.Background(), map[string]string{"statement": "Their salary is 300."}, map[string]Question{
		"sens":  {Type: "choice", Instructions: "Sensitive?", Options: map[string]string{"pay": "pay terms", "none": ""}},
		"quiet": {Type: "noul", Instructions: "Gone quiet?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a["sens"].Choice != "pay" || a["quiet"].Noul != 0.2 {
		t.Errorf("answers %+v", a)
	}
	qs := got["questions"].(map[string]any)
	if c := qs["sens"].(map[string]any)["criteria"].(map[string]any); c["none"] != nil || c["pay"] != "pay terms" {
		t.Errorf("criteria sent %v", c)
	}
	if got["model"] != "jev-latest" {
		t.Errorf("model %v", got["model"])
	}
	b, _ := os.ReadFile(ledger)
	if !strings.Contains(string(b), `"usd":0.042`) || !strings.Contains(string(b), `"kind":"classify"`) {
		t.Errorf("ledger %s", b)
	}
}

type scriptedDecider map[string]map[string]float64

func (s scriptedDecider) Decide(_ context.Context, msgs []Message, _ []string) (map[string]float64, error) {
	for k, v := range s {
		if strings.Contains(msgs[0].Content, k) {
			return v, nil
		}
	}
	return nil, fmt.Errorf("no script for %q", msgs[0].Content)
}

// The same questions asked of a language model, one forward pass each.
func TestDeciderClassifier(t *testing.T) {
	d := DeciderClassifier{D: scriptedDecider{
		"Sensitive?":  {"pay": 0.7, "none": 0.3},
		"Gone quiet?": {"yes": 0.25, "no": 0.75},
		"How much?":   {"1": 0.5, "2": 0, "3": 0.5},
	}}
	a, err := d.Classify(context.Background(), "x", map[string]Question{
		"sens":  {Type: "choice", Instructions: "Sensitive?", Options: map[string]string{"pay": "", "none": ""}},
		"quiet": {Type: "noul", Instructions: "Gone quiet?"},
		"worth": {Type: "score", Instructions: "How much?", Levels: []string{"low", "mid", "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a["sens"].Choice != "pay" || a["quiet"].Noul != 0.25 || a["worth"].Score != 2 {
		t.Errorf("answers %+v", a)
	}
}
