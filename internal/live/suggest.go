package live

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/stages"
	"github.com/anielsud/musubi/internal/store"
)

// Suggestion is a query the conversation suggests looking up.
type Suggestion struct {
	Query    string  `json:"query"`
	Why      string  `json:"why"`
	Kind     string  `json:"kind"` // llm, entity, phrase
	EntityID string  `json:"entityId,omitempty"`
	Score    float64 `json:"score"`
}

// Suggester finds what in the recent conversation is worth looking up.
type Suggester struct {
	matcher *stages.Matcher
	names   map[string][2]string
	df      map[string]int
	docs    int
}

func NewSuggester(st *store.Store) (*Suggester, error) {
	as, err := st.AllAliases()
	if err != nil {
		return nil, err
	}
	names, err := st.EntityNames()
	if err != nil {
		return nil, err
	}
	texts, err := st.ChunkTexts()
	if err != nil {
		return nil, err
	}
	s := &Suggester{matcher: stages.NewMatcher(as), names: names, df: map[string]int{}, docs: len(texts)}
	for _, t := range texts {
		seen := map[string]bool{}
		for _, w := range words(t) {
			if !seen[w] {
				seen[w] = true
				s.df[w]++
			}
		}
	}
	return s, nil
}

func words(t string) []string {
	return strings.FieldsFunc(strings.ToLower(t), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' })
}

// Spotted is an entity named in the conversation.
type Spotted struct {
	ID, Name, Type string
	Count          int
}

// Entities returns entities named in text, most mentioned first. First or
// last names alone count only when they fit exactly one person; live text
// is too noisy to guess between namesakes.
func (s *Suggester) Entities(text string) []Spotted {
	cased := strings.IndexFunc(text, unicode.IsUpper) >= 0
	counts := map[string]int{}
	for _, c := range s.matcher.Find(text, cased) {
		ids := map[string]string{}
		for _, a := range c.Aliases {
			ids[a.EntityID] = a.Kind
		}
		if len(ids) != 1 {
			continue
		}
		for id := range ids {
			counts[id]++
		}
	}
	var out []Spotted
	for id, n := range counts {
		nt := s.names[id]
		if nt[1] == "group" {
			continue
		}
		out = append(out, Spotted{ID: id, Name: nt[0], Type: nt[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about above after again against all also am an and any are aren't as at be because been before being below between both but by can can't cannot could couldn't did didn't do does doesn't doing don't down during each few for from further get got gonna had hadn't has hasn't have haven't having he he'd he'll he's her here here's hers herself him himself his how how's i i'd i'll i'm i've if in into is isn't it it's its itself just kind know let's like lot me more most mustn't my myself no nor not now of off oh okay on once one only or other ought our ours ourselves out over own really right said same say says see shan't she she'd she'll she's should shouldn't so some something sort such than that that's the their theirs them themselves then there there's these they they'd they'll they're they've thing things think this those through to too um uh under until up us very want was wasn't way we we'd we'll we're we've well were weren't what what's when when's where where's which while who who's whom why why's will with won't would wouldn't yeah yes you you'd you'll you're you've your yours yourself yourselves actually basically going mean maybe probably sure guess need make going thank thanks good great time today tomorrow yesterday week next last also still even back much many little bit stuff guys hey hi hello mm hmm`) {
		stop[w] = true
	}
}

// PhraseScore is a distinctive phrase with its score and how often it was
// said in the window.
type PhraseScore struct {
	Phrase string
	Score  float64
	Count  int
}

// Phrases returns distinctive phrases: words and short runs that are rare in
// the knowledge base (so likely specific) yet present in it (so a search can
// find something), weighted by how often they were just said.
func (s *Suggester) Phrases(text string, n int) []string {
	var out []string
	for _, p := range s.PhraseScores(text, n) {
		out = append(out, p.Phrase)
	}
	return out
}

func (s *Suggester) PhraseScores(text string, n int) []PhraseScore {
	if s.docs == 0 {
		return nil
	}
	ws := words(text)
	tf := map[string]int{}
	score := map[string]float64{}
	idf := func(w string) (float64, bool) {
		df := s.df[w]
		if df == 0 || float64(df)/float64(s.docs) > 0.08 || len(w) < 3 || stop[w] {
			return 0, false
		}
		return math.Log(float64(s.docs) / float64(df)), true
	}
	for i := range ws {
		for k := 1; k <= 3 && i+k <= len(ws); k++ {
			gram := ws[i : i+k]
			if stop[gram[0]] || stop[gram[k-1]] {
				continue
			}
			total, ok := 0.0, true
			for _, w := range gram {
				if stop[w] {
					continue
				}
				v, good := idf(w)
				if !good {
					ok = false
					break
				}
				total += v
			}
			if !ok {
				continue
			}
			p := strings.Join(gram, " ")
			tf[p]++
			score[p] = total
		}
	}
	type ps struct {
		p string
		s float64
	}
	var all []ps
	for p, v := range score {
		// A lone word is rarely a useful query ("end"): it must be longer
		// and said more than once to count.
		if !strings.Contains(p, " ") && (len(p) < 5 || tf[p] < 2) {
			continue
		}
		all = append(all, ps{p, v * math.Log(1+float64(tf[p]))})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].p < all[j].p
	})
	var out []PhraseScore
	for _, x := range all {
		dup := false
		for _, o := range out {
			if strings.Contains(o.Phrase, x.p) || strings.Contains(x.p, o.Phrase) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, PhraseScore{Phrase: x.p, Score: math.Round(x.s*100) / 100, Count: tf[x.p]})
		}
		if len(out) == n {
			break
		}
	}
	return out
}

const suggestPrompt = `You help someone during a live meeting by suggesting what to look up in their own knowledge base (past meeting transcripts, email, chat, calendar). Below is the last part of the conversation as live speech recognition heard it: expect missing punctuation and misheard words. "You" is the user.

Suggest up to 3 short search queries (3-8 words each) that would surface useful background right now: what was said or decided before about the people, customers, projects, problems or commitments being discussed. Use words actually spoken; prefer specific names and topics over generic ones. A person's name is a person, not a project. Skip small talk. If nothing is worth looking up, return an empty list.

Reply as JSON: {"queries":["...","..."]}`

// FromLLM asks the chat model for queries. It also returns the model's raw
// reply and the prompt it was given, for the diagnostics view.
func (s *Suggester) FromLLM(ctx context.Context, chat *provider.Chat, owner, recent string, spotted []Spotted) ([]Suggestion, string, string, error) {
	// Saying what each name is keeps the model from inventing, say, a
	// "Reyes project" out of a person's surname.
	var names []string
	for i, e := range spotted {
		if i == 8 {
			break
		}
		kind := e.Type
		if kind == "org" {
			kind = "organisation"
		}
		names = append(names, e.Name+" ("+kind+")")
	}
	user := "Conversation:\n" + recent
	if owner != "" {
		// Speech recognition mangles the user's own name too ("Anil"); left
		// unsaid, the model takes it for a third person worth looking up.
		user += "\nThe user (\"You\") is " + owner + "; recognition may misspell the name. Never suggest looking up the user."
	}
	if len(names) > 0 {
		user += "\nKnown names mentioned: " + strings.Join(names, ", ")
	}
	out, err := chat.Complete(ctx, []provider.Message{{Role: "system", Content: suggestPrompt}, {Role: "user", Content: user}},
		provider.ChatOptions{JSON: true, Temperature: 0.3, MaxTokens: 300}, nil)
	if err != nil {
		return nil, out, user, err
	}
	// Queries come as plain strings: generating a reason for each roughly
	// doubled the reply, and reply length is nearly all of the model's time
	// (measured 2026-10-10: 1.8 s with reasons, about 1 s without).
	// Objects with "q" are accepted too.
	var parsed struct {
		Queries []json.RawMessage `json:"queries"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, out, user, nil
	}
	var res []Suggestion
	for _, raw := range parsed.Queries {
		var q string
		if json.Unmarshal(raw, &q) != nil {
			var o struct {
				Q string `json:"q"`
			}
			json.Unmarshal(raw, &o)
			q = o.Q
		}
		q = strings.TrimSpace(q)
		if q == "" || len(res) == 3 {
			continue
		}
		res = append(res, Suggestion{Query: q, Why: "model", Kind: "llm", Score: 3 - float64(len(res))*0.1})
	}
	return res, out, user, nil
}

// dropOwner removes queries about the owner. Recognition misspells their
// name ("Anil" for "Aniel"), so a word within one edit of a name part of
// four or more letters counts, as does an exact match of a shorter part.
func dropOwner(qs []Suggestion, owner string) (keep []Suggestion, dropped []string) {
	parts := words(owner)
	if len(parts) == 0 {
		return qs, nil
	}
	for _, q := range qs {
		hit := false
		for _, w := range words(q.Query) {
			w = strings.TrimSuffix(w, "'s")
			for _, p := range parts {
				if w == p || (len(p) >= 4 && len(w) >= 3 && editDistance(w, p) <= 1) {
					hit = true
				}
			}
		}
		if hit {
			dropped = append(dropped, q.Query)
		} else {
			keep = append(keep, q)
		}
	}
	return keep, dropped
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
