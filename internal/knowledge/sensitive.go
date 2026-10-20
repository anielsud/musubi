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

// LabelVersionTyped stamps labels from a typed classifier (Classifier).
const LabelVersionTyped = "sl3t"

// The labelling question for a typed classifier. The negative options are
// split (work, a role being hired for, routine mentions) and the meeting's
// title is given: on the audited sample, one "none" drew most false flags
// to roles and teams, and candidates' backgrounds read as ordinary work
// without knowing the meeting was an interview (research log 2026-10-20).
var labelQuestion = provider.Question{Type: "choice",
	Instructions: "`statement` was extracted from `meeting`, one of a senior executive's meetings. Which of these does the statement say? Sensitive content is only shown where it may be.",
	Options: map[string]string{
		"people":    "a specific named person's performance, standing, conduct, discipline, improvement plan, resignation, exit, replacement, layoff or dismissal, or a hiring or firing decision about them",
		"candidate": "anything about a job candidate: their background, past jobs and teams, experience, location, how they work, their interview, assessment or offer (in an interview meeting, what the candidate says about themselves counts)",
		"pay":       "a person's salary, bonus, equity or offer terms",
		"medical":   "anyone's health, illness, injury, treatment or medical appointment",
		"legal":     "a legal dispute, investigation, harassment or discrimination complaint, employment-law advice",
		"private":   "anyone's life outside work: family, children, home, possessions, hobbies, holidays, personal travel, beliefs",
		"work":      "business plans, products, customers, deals, costs and tools, money in aggregate, team or headcount numbers, org structure and who owns what",
		"role":      "a role or opening being hired for (what it covers, its timeline, the interview process) without assessing a named candidate",
		"routine":   "routine mentions of people at work: who attended, owns, works on, is responsive, will meet whom; praise or description of a team; someone's management or working style",
	}}

var typedCategory = map[string]sensitivity.Categories{
	"people": sensitivity.HR, "candidate": sensitivity.HR, "pay": sensitivity.Compensation, "medical": sensitivity.Health,
	"legal": sensitivity.Legal, "private": sensitivity.Personal,
}

// StatementSensitivityTyped is StatementSensitivity with a typed
// classifier: the category with the most probability among the sensitive
// ones, and the probability of any.
func StatementSensitivityTyped(ctx context.Context, cl provider.Classifier, text, meeting string) (sensitivity.Categories, float64, error) {
	a, err := cl.Classify(ctx, map[string]string{"statement": text, "meeting": meeting}, map[string]provider.Question{"sensitive": labelQuestion})
	if err != nil {
		return 0, 0, err
	}
	pr := a["sensitive"].Probabilities
	p, best, bp := 0.0, "", 0.0
	for c := range typedCategory {
		p += pr[c]
		if pr[c] > bp {
			best, bp = c, pr[c]
		}
	}
	if best == "" {
		return 0, p, nil
	}
	return typedCategory[best], p, nil
}
