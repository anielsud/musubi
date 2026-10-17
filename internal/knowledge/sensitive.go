package knowledge

import (
	"context"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// LabelVersion changes with the statement sensitivity prompt.
const LabelVersion = "sl2"

const labelStatementPrompt = `You check statements extracted from a senior executive's meetings for sensitive content about people, so it is only shown where it may be.

Pick one word:
- people: what is said about a specific person's performance, standing, conduct, discipline, improvement plan, exit, replacement, layoff or dismissal; anything about a job candidate (their background, current job, interview, assessment, offer); a hiring or firing decision about a named person
- pay: a person's salary, bonus, equity or offer terms
- medical: anyone's health, illness, injury, treatment or medical appointment
- legal: a legal dispute, investigation, harassment or discrimination complaint, employment-law advice
- private: anyone's life outside work, the speaker's own included: family, children, home, possessions, hobbies, holidays, travel incidents, beliefs
- none: everything else: business plans, products, customers, money in aggregate, team or headcount numbers, roles and org structure, and routine mentions of people at work (who attended, who is responsive, who has access, who will meet whom)

Reply with one word.`

var labelChoices = []string{"none", "people", "pay", "medical", "legal", "private"}

var labelCategory = map[string]sensitivity.Categories{
	"people": sensitivity.HR, "pay": sensitivity.Compensation, "medical": sensitivity.Health,
	"legal": sensitivity.Legal, "private": sensitivity.Personal,
}

// StatementSensitivity returns the category a statement's text reveals
// about a person (0 for none) and the probability it does. Passage rules
// (sensitivity.ClassifyText) catch vocabulary; this catches the statement
// that names a person's exit or a candidate's assessment in plain words,
// which the backfill audits found unlabelled (research log, 2026-10-16).
func StatementSensitivity(ctx context.Context, d provider.Decider, text string) (sensitivity.Categories, float64, error) {
	p, err := d.Decide(ctx, []provider.Message{
		{Role: "system", Content: labelStatementPrompt},
		{Role: "user", Content: "Statement: " + text + "\nOne word:"},
	}, labelChoices)
	if err != nil {
		return 0, 0, err
	}
	best, bp := "none", 0.0
	for _, c := range labelChoices[1:] {
		if p[c] > bp {
			best, bp = c, p[c]
		}
	}
	if p["none"] >= 1-p["none"] {
		return 0, 1 - p["none"], nil
	}
	return labelCategory[best], 1 - p["none"], nil
}
