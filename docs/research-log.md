# Research log

Dated findings with numbers. Real data stays local; participants are
described by role, never by name.

## 2026-10-09: first run on real data

Data: 81 Tomoe recordings (28 Sep – 9 Oct; about a dozen are test
recordings under a minute) and 74 calendar events, read from the M365
connector into the inbox. Processing all of it: 56 s, most of it embedding
1,815 chunks with two models. Incremental runs take under a second unless a
stage version or the registry changes.

### Calendar linking

70 of 81 recordings matched an event on the first pass. Linking v1 let
all-day blocks win: a "no internal meetings" all-day entry scored a perfect
overlap with every recording that day, beating the real events. Three
recordings were affected; one was a call with two external participants that
the block's three-person internal attendee list had made **internal**. v2
skips all-day, longer-than-4-hour, free and cancelled entries; the affected
recordings now link to their real events (one stays unlinked and fails
closed). Synthetic scenario e9 covers it.

### Registry noise

The first registry built 1,201 "people" from attendee lists. Fixes, all
measured by the share of ambiguous mentions:

| Change | Mentions | Ambiguous |
|---|---|---|
| v1 | 2,031 | 1,023 (50%) |
| addresses that aren't first.last become groups (92: team lists, shared mailboxes); first/last aliases need 3+ letters; tool and sister-company domains aren't customers; "… Room" speakers are rooms | 1,764 | 960 (54%) |
| casing decided per item, not per segment; all-caps tokens don't match surnames; names that are English words ("all", "line", "per") need capitals mid-sentence; ties broken by shared meetings with the item's participants (owner excluded; needs ≥2 and twice the runner-up) | 1,400 | 514 (37%) |

The tie-break resolved 256 mentions at confidence 0.55. Whether they are
right needs the owner's labels; the remaining ambiguity is mostly very common
first names (one has five namesakes in the directory, another ten).

### Sensitivity

Private set: 7 leak cases, 9 classification cases (drafts, to be reviewed).

| | v1 rules | after |
|---|---|---|
| leak cases passing | 5/7 | 7/7 |
| invariant violations | 0 | 0 |
| classification exact | 0/9 (harness bug: id prefixes) | 5/9 |
| classification under-labelled | — | 4/9 |

Two policy gaps found, both fixed:

1. **Small internal groups were labelled internal.** A three-person
   conversation about specific staff moves was visible to a colleague who
   wasn't there. Group size doesn't predict sensitivity (the executive team
   meeting is a group too). Now only broadcasts (15+ or a company list) are
   internal; working groups are confidential.
   `[sensitivity] internal_min_audience = 3` restores v1.
2. **Internal meant "anyone".** A reply addressed to a customer contact could
   include an internal finance sync. A scope's open level now drops to public
   when any recipient is outside the company.

Still open: the rules miss conversational HR content. All four under-labels
are talk about named people (moving staff between teams, a performance
history, headcount cuts, someone's leave). The keyword rules only know formal
terms ("performance review", "PIP"). The passages are confidential by item, so
they are hidden from anyone who wasn't there, but a reply to an attendee would
include them. Next: a local-model classifier stage (`origin = model`) that
may raise, never lower, a label, measured on this set.

Category counts on real data (rules r2): security 43 (mostly product
vulnerability-management talk, not incidents), financial 22 (mostly
"bookings"), deal-terms 6, hr 4, strategy 6. Precision isn't measured yet.

### Retrieval

Private set: 8 queries worded like the transcript, 7 paraphrases. hit@5 /
MRR:

| | keyword | vector | hybrid |
|---|---|---|---|
| nomic-embed-text, chunks without titles | 0.80 / 0.68 | 0.87 / 0.74 | 0.80 / 0.58 |
| nomic-embed-text, titles in FTS and embeddings | 0.80 / 0.71 | 0.93 / 0.72 | 0.93 / 0.71 |
| qwen3-embedding:0.6b, titles | 0.80 / 0.71 | 0.87 / 0.72 | **1.00 / 0.78** |

Putting the calendar title in every chunk (searchable, and as context when
embedding) was the largest gain. qwen3 is ahead in hybrid by one query of 15;
too few to change the default (nomic, 274 MB vs 639 MB). Re-run when the
private set is larger and reviewed. Keyword search alone fails paraphrases,
as expected; the synthetic corpus is too small to separate the models (both
1.0).

## 2026-10-10: live panel, first replay

Replayed past meetings through the live path (`musubi live --replay`, and the
app's replay) at 8–30× speed; local model qwen3:8b on Apple silicon.

- A brief (search + two-sentence summary) takes about 2 s; a question in Ask
  about 6–7 s for an 8-passage answer. Suggestions every 20 s of new speech.
- Model suggestions are mostly specific and useful (a partner agreement's
  terms, a benchmark's earlier runs, an evaluation endpoint request). Two
  failure modes seen: a person's surname turned into an invented "project"
  (the prompt now says which names are people and which organisations), and
  queries about things never discussed before; the NONE rule drops most of
  those briefs (2 of the first 10 were dropped).
- Briefs on people can surface personnel matters from other meetings. That
  is correct for the owner in private mode, and the reason the screen-safe
  toggle exists; measuring what a brief should show about a person, when,
  is open.
- Not yet measured: whether suggestions arrive early enough to be useful,
  and how often the top two are the right two. Next: score replays against
  a hand-marked list of "moments worth a lookup" per meeting.

## 2026-10-10: replay timing model and diagnostics

Replay now reconstructs when each version of a line would have reached
Musubi live (`internal/live/schedule.go`). The constants come from Tomoe's
settings and measurements:
- drafts refresh every 0.5 s, rebuilt from the saved word timings (lower
  case, unpunctuated, and without the live model's mishearings);
- a turn closes after a 2 s pause, or when the other side starts talking;
- Cohere then decodes it in about 7.9% of its length.

On one replay with audio:
- final text reached the panel 2.1–2.4 s after speech ended;
- the model took 1.1–2.2 s to suggest queries and 0.8–1.4 s to summarise;
- a brief was ready a median 22–24 s after its topic was first said.

That last figure is dominated by the 20 s suggestion cadence plus "the
window must have changed". It is the obvious lever if hints feel late.

The diagnostics view (⚙︎ in the app) shows each cycle step by step:
- what was read, and the model's prompt and reply;
- which names and phrases were recognised;
- the ranking, with when each topic was said, and why each suggestion was
  or wasn't looked up;
- the search results with scores and labels;
- the summary verdict.

## 2026-10-10: a couple of seconds, not twenty

The 20 s cycle is gone. What changed:

| | before | after |
|---|---|---|
| trigger | a timer every 20 s | every line; the model reruns on new final text, or on drafts every 1.5 s |
| model reply | queries with a reason each | queries only |
| lookups | every rephrasing a new lookup | skipped if half its content words overlap a recent one |
| summary | before anything showed | after the passages are on screen |

Model timing (qwen3:8b, Apple silicon, measured with the suggestion
prompt): reply length is nearly all of it (prompt reading 0.01 s).

| | reasons in reply | short reasons | queries only |
|---|---|---|---|
| qwen3:8b | 1.8 s | 1.2 s | 0.5–0.6 s |
| qwen3:1.7b | 0.7 s | 0.65 s | not tried: queries vaguer |
| qwen3:4b-instruct | 1.8 s | 1.1 s | not tried: broke the JSON format 2 times out of 3 |

Search is under 0.05 s; a summary is 1.5–2 s.

Measured from the latest mention of a topic to its passages on screen,
replaying two meetings in real time:

| | before | after |
|---|---|---|
| new topics (looked up within 10 s of being said) | — | median ~3 s (1–1.5 s often) |
| all lookups | median 25 s (cycle-bound) | median 3.8–4.0 s |
| summary | +2 s | median +2 s after the passages |

- **The long tail is not delay.** It is the model returning to an earlier
  thread with a new angle ("previous discussions on X", 13 s after the
  mention). The diagnostics view now reports new topics and follow-up
  angles separately.
- **The floor is speech recognition:** a word reaches Musubi as a draft
  about 0.5–1 s after it is said, final text about 2.3 s after.
- **The fast lane (names in a single line) rarely fires on real meetings.**
  People are mostly named by first name, and first names are ambiguous in
  the directory; unambiguous customer names would fire it.
- **Phrase lookups from single lines were mostly fragments** ("moves the
  bar", "nine must occur"), so they are off (`phrase_lookups`).
- **Queries about the owner** (with their name misrecognised) are now
  dropped by name and near-misspellings.

## 2026-10-10: knowledge layer, first build on the synthetic company

Statements → claims → topics, with belief computed when a claim is read
(architecture: "Knowledge layer"). Model: qwen3:8b; statement vectors:
nomic-embed-text. The synthetic corpus now includes:
- a hypothesis later ruled out;
- one person's guess later contradicted by a customer's email;
- a question later answered.

The decision that changed, and the fact that is restricted in one source but
public in another, were already in it.

**Statement vectors are close together.** A typical pair scores 0.59, one in
ten pairs exceeds 0.70, and "the fix ships September 22" vs "...September 29"
scores 0.99. So similarity can't decide sameness:
- v1 auto-joined near-identical statements (≥ 0.92) and merged the old and
  new ship dates into one claim. Now only ≥ 0.97 with the same kind and
  identical numbers and dates joins without the model.
- The judge (v1) related almost everything ("answers" to statements that
  weren't questions; "supersedes" across unrelated items: 39 relations among
  51 claims). v2 says that sharing a subject isn't a relation, and rejects
  relations that don't fit the kinds. That brought it to 21–25 relations,
  mostly right.

**Extraction:**
- Announcements phrased as congratulations were dropped. The model labelled
  them "announcement", which isn't one of the seven kinds, and validation
  threw them away. Near-miss kinds now map onto the seven.
- Outside speakers are annotated with their organisation ("Tom Becker (Acme
  Freight)"), so "we decided" resolves to the customer. That made the
  customer's two-year decision contradict the three-year guess.

**Topics:** the first clustering (nearest running centroid) let one topic
swallow 43 of 53 claims. Average-linkage clustering, stopping at 0.70 (the
95th percentile of claim pairs), gives 13 sensible topics: Bluegill SSO,
Acme renewal, webhook fix, batching release, the acquisition, the promotion,
the Q4 kickoff and others.

| | v1 | now |
|---|---|---|
| statement cases | — | 4/4 |
| claim cases (status, relations, evidence by scope, absence) | — | 9/9 |
| topic cases (grouping, labels not leaking) | — | 3/3 |
| leaks | 0 | 0 |

Real data is next. Thresholds (0.97 / 0.72 / 0.70) will need checking
there: the synthetic corpus is small and tidy.

## 2026-10-10: typed statements, cross-linked topics, judge fixes

Changes, on the synthetic company:

- **Typed extraction.** Certainty is a number from 0 to 1, and each
  statement has a value (type plus raw words), normalised in Go: "Q4" is
  Oct–Dec and "mid November" is Nov 10–20. Comparisons are deterministic:
  same, within, contains, or differs. The JSON schema constrains the reply,
  and "about" and "when" were dropped from the output (fewer tokens).
- **Topics as a cross-linked graph.** Claims naming different organisations
  or projects are pushed apart (−0.15 similarity). Topics link as same
  customer or project, discussed together, parallel, or similar, and each
  rebuild records lifecycle events.
- **Judgements anchored to the source line**, so they survive re-extraction.

Extraction drift observed. Priya's "it is not the load balancer, we ruled
that out" became "The index migration did not cause the load balancer to be
the issue": the negation was attached to the wrong thing. The prompt now says
to keep negation and hedging exactly as said. Drift like this is the main
risk to linking, and it can only be measured on real data.

Judge failures and fixes:

| Failure | Fix |
|---|---|
| answered against the wrong claim index, skipped the rest | asked for one verdict per earlier claim, schema-enforced |
| misses plain negations | a deterministic note when exactly one statement negates and they share key words |
| "contradicts" an open question | remapped to "answers" |
| relates statements about different customers | dropped when both name organisations or projects with none in common |

Relations (36) hand-checked: about 28 sensible, about 8 wrong ("Dana will
review" contradicts "Priya owns it"; a status update "contradicts" an
unrelated fact). So expect roughly 75–80% relation precision from qwen3:8b
until measured on real data.

| | before | now |
|---|---|---|
| statement cases | 4/4 | 4/4 |
| claim cases | 9/9 | 10/10 (adds a value refined: Q4 → mid November) |
| topic cases | 3/3 | 4/4 (adds parallel customers: separate topics, linked) |
| leaks | 0 | 0 |

## 2026-10-10: pilot on real meetings, run 1 (extract x6)

Nine real meetings (179 passages) in three recurring threads:
- a partner engagement over a week, in five meetings;
- one customer-requirements meeting;
- three staffing meetings.

Hand-read ground truth and private cases are in `testdata/private/`.

**Speed and volume:**
- 7.2 s per passage with 2 parallel requests on qwen3:8b; the full corpus
  (1,741 passages) would take about 3.5 h.
- 680 statements, about 4 per passage. About a third were trivial ("the link
  was sent"), and about 15–20% were distorted.

**The key facts did come through:**
- a customer's deadlines, percentages and counts, with typed values;
- the signing sequence across the partner meetings;
- the staffing decisions.

**The problem is confident rewriting of casual or garbled speech:**
- negations inverted ("we're not giving away X" became "acceptable to give
  away X");
- jokes and figures of speech made literal ("fired up" became "is fired");
- a question turned into a decision ("sounds like we signed?" became "the
  document has been signed");
- misheard words stated as fact;
- a demand attributed to the person relaying it;
- "the speaker" used instead of names;
- personal life extracted.

**Robustness:** one reply cut off at the token limit aborted the run before
linking. A bad passage is now skipped instead.

**A sensitivity gap, separate from extraction:** a recording linked to a
calendar event takes the event's attendees as its audience. A side
conversation recorded after most attendees had left was therefore exposed to
all of them. To fix: base the audience on who was actually heard, or fail
closed when the speakers seen don't match the invite.

**Response (extract x7, not yet run on real data):**
- extract selectively: usually 0–3 statements per passage, and skip rather
  than guess;
- each statement carries a verbatim quote;
- quote, figures and negation are checked against the source in code;
- ungrounded statements weigh 0.3 and never auto-join a claim.

Next: re-run the same nine meetings and compare with this run.

## 2026-10-10 — Pilot run 2 (extract x7), and one-pass decisions

Same nine meetings (179 passages), qwen3:8b, Ollama 0.34.4, `OLLAMA_NUM_PARALLEL=1`.

**Extraction x7:**
- 439 statements, 2.45 per passage, down from 680 (3.8 per passage) in run 1;
  6.4 s per passage. 378 (86%) pass the deterministic grounding check.
- Four of the five distortions written up as cases are gone: the inverted
  negation, "fired up" as fired, the joke taken literally, and the question
  turned into a decision.
- Private cases: statements 5/7, claims 5/10, topics 1/3. The misses:
  - a conditional refusal to sign ("not until outside counsel has looked")
    wasn't extracted, so the signing thread can't show its progression;
  - spoken numbers stay words ("forty five days"), so a search for "45"
    misses them;
  - an opinion repeated by three people over three meetings stays
    "speculative", because belief only promotes facts and decisions;
  - personal life is still extracted, from a passage that is otherwise about work.
- Linking took 9 min against 14 min of extraction (380 judge calls for
  439 statements). The judge now costs more than half as much as
  extraction, so it needs the same scrutiny.
- The audience gap from run 1 now fails a case: a side conversation's
  remark about a colleague is visible to an attendee who had left.

**Ollama hang:** after hours of use, the runner (one slot, context shift
on) stopped serving queued requests while reporting idle; restarting
Ollama fixed it. Musubi caps every call's output, so a 10-minute client
timeout is the only guard. Worth a watchdog on long runs.

**One-pass decisions (Jev-style).** Ollama returns token log-probabilities,
so a yes/no question costs one forward pass. `provider.Decider` reads the
answer's probability instead of generating JSON.

- *Passage gate* (`knowledge.gate`, off by default): 0.24 s per passage.
  Scores are bimodal: 154 passages ≥ 0.9, 17 < 0.1, 8 in between. The 17
  passages below 0.1 held 24 statements, nearly all small talk or personal
  life; the arguable losses were two opinions about a client contact.
  The net saving is only about 6%, because x7 already skips
  empty passages and meetings are mostly work. It may pay off on email
  and chat, which carry more automated and social traffic. It can't
  catch personal life inside a work passage.
- *Statement check* (prototype, not wired in): "is this work knowledge
  worth keeping?" on each extracted statement, 0.1 s each (about 4% of
  extraction). 40 of 439 statements scored below 0.2; by hand, about 37
  are noise (metaphors taken literally, holidays, garbled ASR) and 3 are
  real losses, including a conditional acceptance of a deadline. Scores
  are bimodal too (396 ≥ 0.8).

Conclusion: keep the statement check's probability as a stored field and
apply the threshold when reading (hide or down-weight), instead of
deleting. A wrong threshold then costs a query change, not a re-extraction.

## 2026-10-10 (late) — Sensitivity fix, worth scores, per-pair judge with verification

All without re-extracting: the pilot's 439 x7 statements were reused.

**Audience of linked recordings.** A recording now takes as its audience
the owner plus the people heard speaking, matched to invitees by name. The
invite list still sets the level. This closes the side-conversation leak
from run 1, and the private case now passes. 48 of 69 linked recordings
have at least one unidentified voice, so "fall back to the invite list
when someone is unidentified" would have left most of them open.

**Silent failures found.**
- The synthetic replay cache was never re-recorded for x7, and the new
  "skip a bad passage" path swallowed every miss. That gave 0 statements
  while the test still passed. A run where every passage of an item fails
  is now an error.
- `SplitLines` read anything before the first ": " as a speaker, so in mail
  and chat a sentence like "… Agenda: roadmap" became a speaker. Meetings
  were unaffected; checked on all 179 pilot passages. One participant's
  display name carries a "•.•" decoration that a naive punctuation rule
  rejected, so prose is detected by punctuation followed by a space.
- New claims were created without their statement's value, so value rules
  never fired within a run.
- `knowledge.LinkVersion` was never read, so changing linking rules didn't
  re-link. A global link stamp now clears and rebuilds claims when rules,
  model, `min_worth` or the scorer change.
- A schema version bump drops statements, which is hours of model time at
  full scale. New statement columns are now added in place.

**Ollama stalls.** Two kinds. A runner that serves nothing while
reporting idle; restarting with `OLLAMA_MAX_LOADED_MODELS=3` stopped it
recurring. And generation crawling at about 2.5 s per token for minutes,
seen while Go builds saturated the CPU. Schema-constrained sampling runs
on the CPU. Calls now time out after 2 minutes and retry once.

**Worth scores** (`min_worth` 0.2, default on): 41 of 439 pilot statements
below the threshold. This fixed the remaining personal-life case.

**Per-pair judge** (`judge = "decide"`, default on). One forward pass per
candidate pair gives probabilities over the six relations, for up to 6
candidates at ≥ 0.65.
- On the pilot the six-way choice over-claims, with probabilities near 1
  either way: 131 "contradicts", 58 "answers" and 167 "refines" in nine
  meetings. About 15% of a hand-checked sample of contradictions held.
- A narrower yes/no question confirms each proposed relation. Tested
  offline it kept answers 4/58 (all correct in the sample), refines 76/167
  (about 8/10 correct) and contradicts 56/131 (about half correct).
  Contradiction remains the weak relation for an 8B model on
  conversational text.
- The verifier first rejected a disproved hypothesis ("might be the load
  balancer" against "the load balancer was ruled out", 0.25). The question
  now says a guess the other statement rules out counts.
- **Prompt caching matters more than call count.** Ollama keeps one cached
  prompt prefix per slot. Alternating the judge's and verifier's system
  prompts made every call re-read about 400 tokens: 464 ms against 106 ms
  per call. A second slot (`OLLAMA_NUM_PARALLEL=2`) didn't help. One shared
  system prompt, with the question in the user message, brought linking
  from 22 min to 9m52s (0.19 s per call), level with the JSON judge's
  9m12s.
- Final, on the same statements: 92 relations, against 305 with the JSON
  judge and 357 with the unverified per-pair judge.
- Synthetic: 17/18; the JSON judge with the same fixes scored 16/18. The
  miss is a prediction ("will renew for three years") against a later
  decision ("signed two-year"); the verifier says they can both be true.

**Private cases after these changes** (run 2 → now): statements 5/7 → 6/7,
claims 5/10 → 8/10, topics 1/3 → 1/3, leaks 2 → 0. What remains:
- the conditional refusal to sign is not extracted; this needs a prompt
  change and re-extraction, batched with the next extraction change;
- the same view held by several people across meetings lands in separate
  claims (the judge calls them "refines" or worse, not "same"), so
  repetition never accumulates;
- topics still miss the two thread cases, which depend on the claims above.

Open: one run spent 7 min before the knowledge step started (the step
itself took 1 min), cause not yet found. The desktop app was open on the
same database.
