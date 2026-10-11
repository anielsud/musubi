package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/anielsud/musubi/internal/provider"
)

// GateVersion stamps the gate's prompt; extraction re-runs when it changes
// (only while the gate is on).
const GateVersion = "g1"

const gatePrompt = `You decide whether a passage from a work conversation is worth mining for knowledge.

Answer yes if it contains any of: a decision, a commitment or action item, a fact about a customer, deal, project, product, contract, money, dates or people's roles, a plan, an open question someone needs answered, or a considered opinion about work.

Answer no if it is only: greetings, small talk, personal life (holidays, family, hobbies), jokes or banter, meeting logistics (who is joining, can you hear me, scheduling), or garbled text with no clear meaning.

Reply with one word: yes or no.`

// Worth returns the probability that a passage holds knowledge worth
// extracting. One forward pass, no generated text, so it costs a fraction
// of an extraction and can skip the passages that only ever yield noise.
func Worth(ctx context.Context, d provider.Decider, title string, lines []Line) (float64, error) {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "Source: %s\n\n", title)
	}
	for _, l := range lines {
		if l.Speaker != "" {
			fmt.Fprintf(&b, "%s: %s\n", l.Speaker, l.Text)
		} else {
			fmt.Fprintln(&b, l.Text)
		}
	}
	b.WriteString("\nIs this passage worth mining? yes or no.")
	p, err := d.Decide(ctx, []provider.Message{
		{Role: "system", Content: gatePrompt},
		{Role: "user", Content: b.String()},
	}, []string{"yes", "no"})
	if err != nil {
		return 0, err
	}
	return p["yes"], nil
}
