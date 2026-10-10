// Package ask answers questions from the knowledge base: scoped retrieval,
// then a local model writes a short answer citing the passages it used.
package ask

import (
	"context"
	"fmt"
	"strings"

	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

type Answer struct {
	Text    string
	Sources []store.Hit
}

const system = `You answer questions about the user's own meetings, email and chats, using only the numbered excerpts provided. Transcripts come from speech recognition and contain errors; "You" is the user.
Rules:
- Answer in a few sentences or a short list. Lead with the answer.
- Cite excerpts inline like [1] or [2][4] after the facts they support.
- Say when excerpts disagree, preferring the most recent date.
- If the excerpts don't contain the answer, say so plainly and mention what they do cover. Never invent facts.`

// Ask retrieves passages visible in sc and, if a chat model is configured
// and allowed to see them, writes an answer. Tokens stream to onToken.
func Ask(ctx context.Context, e *engine.Engine, sc sensitivity.Scope, question string, k int, exclude []string, onToken func(string)) (*Answer, error) {
	hits, err := e.Search(ctx, sc, question, k, exclude)
	if err != nil {
		return nil, err
	}
	ans := &Answer{Sources: hits}
	if e.Chat == nil {
		return ans, nil
	}
	// A cloud model may only read what the cloud scope allows.
	if !e.Chat.Local() {
		var allowed []store.Hit
		for _, h := range hits {
			if sensitivity.Visible(h.Label, nil, sensitivity.Cloud()) {
				allowed = append(allowed, h)
			}
		}
		hits = allowed
	}
	if len(hits) == 0 {
		ans.Text = "Nothing in your knowledge base matches that."
		if onToken != nil {
			onToken(ans.Text)
		}
		return ans, nil
	}
	msgs := []provider.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: Excerpts(hits) + "\nQuestion: " + question},
	}
	text, err := e.Chat.Complete(ctx, msgs, provider.ChatOptions{Temperature: 0.2, MaxTokens: 600}, onToken)
	if err != nil {
		return ans, err
	}
	ans.Text = strings.TrimSpace(text)
	return ans, nil
}

// Excerpts formats hits as numbered context for a prompt.
func Excerpts(hits []store.Hit) string {
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s — %s (%s)\n%s\n\n", i+1, h.OccurredAt.Format("Mon 2 Jan 2006"), h.Title, h.Kind, clip(h.Text, 1800))
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
