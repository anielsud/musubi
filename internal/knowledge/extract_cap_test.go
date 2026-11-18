package knowledge

import (
	"context"
	"testing"

	"github.com/anielsud/musubi/internal/provider"
)

type capLLM struct{ got int }

func (c *capLLM) Local() bool       { return false }
func (c *capLLM) ModelName() string { return "fake" }
func (c *capLLM) Complete(_ context.Context, _ []provider.Message, opt provider.ChatOptions, _ func(string)) (string, error) {
	c.got = opt.MaxTokens
	return `{"statements":[]}`, nil
}

func TestExtractMaxTokens(t *testing.T) {
	lines := []Line{{Speaker: "Ilse Novak", Text: "We ship the Northwind import on Friday."}}
	for _, tc := range []struct{ in, want int }{{0, DefaultExtractMaxTokens}, {1500, 1500}} {
		llm := &capLLM{}
		if _, err := Extract(context.Background(), llm, "Owner", "", lines, nil, tc.in); err != nil {
			t.Fatal(err)
		}
		if llm.got != tc.want {
			t.Errorf("maxTokens %d: sent %d, want %d", tc.in, llm.got, tc.want)
		}
	}
}
