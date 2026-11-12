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

## 2026-10-11 — Rare-word candidates, extract x8, supersession checks

**Repeated views in separate claims were a retrieval problem, not a
judging one.**
- The judge was right whenever it said "same": a narrow "same point?"
  check confirmed 23 of its 32 "same" verdicts, and those had joined.
- The pairs that should have joined mostly never met. In the busiest
  meeting, the earlier "throwaway R&D" claim ranked ninth by similarity
  behind claims merely about the same partner, and the cap of 6 cut it.
- Now up to 3 claims that share a rare word (used by at most 2% of claims)
  and are at least 0.55 similar are judged as well.
- Pilot claims 8/10 → 9/10: the three statements of that view from one
  meeting form one claim. Across meetings it is still split.

**Extract x8: preconditions.** The prompt now keeps conditions with the
decision they govern ("won't sign until counsel has looked") and treats a
point repeated in broken speech as meant.
- 9 meetings: 418 statements (was 439), 358 grounded, 33 below
  `min_worth`; extraction 19m33s against 14m03s.
- The conditional refusal is now extracted, and both topic thread cases
  pass: topics 1/3 → 3/3.
- One regression: the ASIC cost fact is labelled "requirement".
- One reply was cut off at the token limit and its passage skipped.
- Synthetic 17 → 16/18: the reworded load-balancer statements no longer
  pair. Those two disproof cases flip with wording.

**Supersession.** The judge calls a changed position ("won't sign until
counsel", later "willing to sign") a contradiction.
- A fallback that asked "is this a later update that replaces it?"
  whenever the contradiction check failed produced 18 supersessions on
  the pilot, and about half were wrong. A wrong supersession hides a valid
  claim, so the fallback was removed.
- Direct "supersedes" verdicts are now checked by the same question; that
  can only remove relations.
- The signing thread is linked as a contradiction, which belief doesn't
  yet weigh enough to change the earlier claim's status.

**Pilot private cases now:** statements 6/7, claims 9/10, topics 3/3,
leaks 0 (run 2: 5/7, 5/10, 1/3, 2 leaks).

## 2026-10-11 — Prototype: meeting-level points instead of atomic statements

Reading the pilot output, the atomic statements are low quality in ways
the private cases did not measure: referents unresolved ("the
agreement"), context lost or inverted (an option talked through, recorded
as a decision), question-and-answer exchanges split into fragments, and
clarifications in the next passage invisible. Worth scores are 1.0 on
these, since they are work-related fragments. The keyword cases rose
(claims 5/10 → 9/10) while usefulness did not; the eval needs checks for
standalone-ness and coverage.

Prototype (Python against Ollama, qwen3:8b; not in the pipeline), on three
pilot meetings (15, 34 and 40 passages):
1. A brief of the whole meeting: purpose, participants, subjects, glossary.
2. Points per 5 passages, with the brief and the neighbouring passages as
   context. A point is a complete note: question with its answer, proposal
   with its outcome, conditions kept, names explicit.
3. A rollup that merges points within the meeting.

Findings:
- Step 2 is a clear step up. Points stand on their own, name people and
  documents, and carry outcomes ("retained, off the list, move to SRE
  when a spot opens"). About 4–6 points per segment; 3–4 min per
  40-passage meeting, comparable to atomic extraction.
- The brief must be short text. Passing it as 5 kB of JSON made the model
  return empty point lists in 6 of 7 segments.
- The rollup is the weak step on an 8B model:
  - asked to consolidate, it dropped the counsel call's key decision
    (clarify with the other side's counsel) and an unrelated deal, both
    present in the segment points;
  - asked to merge without dropping, its output overran 3,500 tokens;
  - asked for merge groups only, it over-merged (12 points into one blob,
    losing the cover-note step) and overran again on the 40-passage
    meeting.
- Accuracy is still the 8B model's:
  - conflation ("today's SLA is 45 days", where 45 days was the client's
    ask);
  - nearly everything marked "decided";
  - roles guessed in the brief.
- Coverage of the hand-read key points: about 4/5, 3/6 and 5/9 for the
  three meetings.

Direction: segment points with brief and context become the unit; merging
within a meeting moves to code (similarity plus the existing "same point"
check) instead of a model rewrite; and a larger local model should be
tried for this one-time extraction step.

## 2026-10-11 — Small-model bake-off for meeting-level points

Same three pilot meetings and the same prototype (a brief, then points per
5 passages with neighbouring context; no model rollup). Graded by hand
against a 24-item key-point checklist built from the hand-read summaries
(private). Single rater, so read the numbers as rough.

| Model | Key points covered (6 + 9 + 9) | Wrong or dubious points | Personal-life points | Time, 3 meetings |
|---|---|---|---|---|
| qwen3:8b (baseline) | 4 + 6 + 7 = 17 (71%) | ~9 | yes | 10.2 min |
| qwen3.5:4b, brief guessed | 6 + 7 + 7.5 = 20.5 (85%) | ~14, most from one invented company name | some | 6.2 min |
| qwen3.5:4b, brief given known facts | (one segment failed); similar coverage | invented name gone | many (pool, kids' ages, ethnicity) | 6.2 min |
| qwen3.5:9b, brief given known facts | 6 + 6.5 + 6.5 = 19 (79%) | ~6 | none | 9.9 min |

Findings:
- The newer generation matters more than size. qwen3.5:4b covers more
  than qwen3:8b at 60% of the time. qwen3.5:9b is about as fast as the
  old 8B, has the fewest errors and the best attribution (organisations
  on every point), and kept no personal-life points.
- **The brief is a single point of failure.** Left to guess, the 4B
  invented a company name and made an Optimizely lawyer a Microsoft
  executive, and both spread into a dozen points. Giving the brief the
  facts we already hold (the owner and their company, invitees and their
  organisations from the calendar, by email domain) removed this for
  both models.
- All three models conflate the client's asks with current practice in
  the patch-management meeting ("45 days instead of the original 90").
  That is either a genuinely ambiguous transcript or a gap in the
  checklist; a larger model would be the test.
- The small model does not follow "skip personal life"; the stored worth
  score after extraction is the guard, and it is needed.

Decision for now: qwen3.5:9b for extraction (it runs once per item; best
accuracy at today's cost); test qwen3.5:4b for the high-volume yes/no
calls and live suggestions against the eval. The 35B model is not
justified by this test.

## 2026-10-11 — Flagging unsure points, and few-shot examples

Hand labels for the 79 qwen3.5:9b points from the bake-off: 12 wrong (15%).
- Misreadings of garbled or dense speech: 4.
- Conflations (a client's ask against current practice; an unrelated deal
  mixed in): 3.
- Inventions (acronym expansions, titles, an unsaid figure): 3.
- Misattributions: 2.
One more point was true but came from outside knowledge (a law firm's full
name). Labelling also found errors in my own key-point checklist: the
December delay does concern the retained staff, and a 90-day term was
genuinely in play.

**Flagging, by signal** (detecting the 12 wrong points among 79):
- **Deterministic** (names and figures in the point must appear in the
  meeting or the known facts): 10 flags, 4 real errors. It catches the
  inventions; acronym expansions cause most false flags.
- **Yes/no "do the cited lines support this note?"**, from qwen3.5:4b or
  9b reading token probabilities: AUC 0.39–0.40, worse than chance.
  Attribution and status questions were no better.
- **Agreement with other models' points** (similarity to the nearest
  qwen3.5:4b or qwen3:8b point): AUC 0.36. The errors are shared: every
  small model misreads the same passages the same way, so consistency
  cannot detect them.
- **How garbled the cited lines are** (filler, repeats, fragments): AUC
  0.61; weak.
Conclusion: at this size, a model cannot check its own kind of mistake.
Detecting misreadings needs a different signal: a stronger model as
verifier, or the owner.

**Few-shot** (rules from the observed errors, plus two invented Northwind
examples covering ask vs practice vs agreement, relayed demands, a garbled
line skipped, and a personal detail kept), qwen3.5:9b:
- Coverage: 20/24 key points, against 19/24 zero-shot. The client-ask
  distinction now comes out right in the patch-management meeting.
- Wrong points: about 11/84 (13%), against 12/79. Inventions disappeared
  (no expanded acronyms or outside names); misreadings and conflations
  remain.
- Fewer points per meeting (12 against 18 on the staffing meeting), with
  some duplicates.

## 2026-10-11 — Ceiling check: large hosted models, and the local 35B

Baseten (Optimizely R&D instance, approved for this data) gives an
OpenAI-compatible endpoint for large open-weight models; none of them fit
a 48 GB Mac. The local candidate is qwen3.6:35b-a3b (mixture of experts,
~3B active; 22 GB).

**Synthetic hard meetings** (noisy; 30 key points, 22 traps), all with
the example prompt:

| Model | Key points | Traps | Invented |
|---|---|---|---|
| DeepSeek-V4.1-Flash (hosted) | 30 | 0 | 0 |
| GLM-5.3 (hosted) | 29 | 1 | 1 |
| Kimi-K3 (hosted) | 29 | 2 | 1 |
| gpt-oss-120b (hosted) | 24 | 4 | 2 |
| qwen3:8b | 26 | 3 | 0 |
| qwen3.6:35b-a3b | 23 | 3 | 8 |
| qwen3.5:9b | 23 | 1 | 1 |
| qwen3.5:4b | 14 | 0 | 3 |

**Real pilot meetings** (hand-graded against the key-point checklist):

| Model | Coverage | Wrong points | Time |
|---|---|---|---|
| qwen3.5:9b, examples | 20/24 | ~12% | ~8 min |
| qwen3.6:35b-a3b, examples | ~22/24 | ~12% | 4.6 min |
| GLM-5.3 (hosted), examples | 24/24 | ~1–3% | ~5 min |

GLM-5.3:
- found details no local model did: the deadline driving the counsel
  call, the incumbent the client is replacing, today's patch turnaround;
- kept the unrelated deal separate and the negation right;
- attributed nearly every point correctly.
Cost for the three meetings: 78k input and 40k output tokens, about $0.28.

**It also corrected the checklist.** Optimizely had offered 90 days, so
"45 days instead of 90" is right, not a conflation. And two points I had
labelled wrong ("no SOC 2 of its own", "sabotage") are in the
transcript. Hand labels carry errors; a strong model is a useful second
reader.

Takeaways:
- The local 35B finds more and runs faster than the 9B (MoE), but its
  error rate is the same; inventions (acronym expansions) persist despite
  the rules.
- The gap between local and the hosted ceiling is large: about 12% wrong
  against about 2%. Prompting has not closed it.
- Options: extract on the hosted model (about $0.10 per meeting);
  distil — use the hosted model's points on real meetings to fine-tune a
  local model; or extract locally and send only flagged or high-stakes
  points to the hosted model.

## 2026-10-11 — Thinking mode on the local 35B

Every local run so far had thinking off, while the hosted ceiling models
reasoned heavily (DeepSeek: ~145k hidden tokens over three meetings).
qwen3.6:35b-a3b with thinking on, same prompt and examples, three real
meetings, hand-graded:

| | Coverage | Wrong points | Time |
|---|---|---|---|
| thinking off | ~22/24 | ~12% | 4.6 min |
| thinking on | ~22/24 | ~6% | 26 min |
| GLM-5.3 (hosted) | 24/24 | ~2% | ~5 min |

With thinking on, the 35B:
- found the deadline every non-thinking local model missed;
- kept the unrelated deal separate;
- read counsel's hedged answer consistently;
- stopped expanding acronyms.
It still added a law firm's full name from outside knowledge.

Thinking halves the error rate, about 60% of the way to the hosted
ceiling, at about 5.5× the time (~10 min for a 40-passage meeting on an
M5 Pro through Ollama's llama.cpp engine at ~70 tok/s).

Speed on this machine (M5 Pro, 48 GB, Ollama 0.34, GGUF Q4): ~74 tok/s
generation, ~1,200 tok/s prompt reading. Ollama runs safetensors models on
MLX, reported 1.5–2× faster, but Ollama issue #17933 reports structured
output silently ignored on the MLX backend; to verify before switching.

## 2026-10-11 — Qwen3.8-27B on MLX

Ollama serves safetensors models on Apple's MLX engine. `qwen3.8:27b-mlx`
(dense 27B, 4-bit, Apache-2.0) on the M5 Pro:
- Speed: ~31 tok/s generation and ~363 tok/s prompt reading. Dense, so
  slower than the 35B-A3B MoE (~74 and ~1,200 on llama.cpp).
- Memory: 22 GB at a 16k context; 31–35 GB at 24–32k with thinking. With
  the usual desktop apps open that pushed the machine into heavy swap
  (23 GB). Context size matters as much as weights on 48 GB.
- Capabilities on Ollama's MLX engine:
  - token log-probabilities work;
  - thinking works;
  - structured output is refused (HTTP 501). The log names the cause:
    the Homebrew build lacks the xgrammar library the MLX engine uses.
    The schema went in the prompt instead, with lenient parsing and one
    retry: 1 JSON failure in 39 calls, recovered by the retry.

Three real meetings, the example prompt, hand-graded:

| Model | Coverage | Wrong points | Time |
|---|---|---|---|
| qwen3.6:35b-a3b, no thinking | ~22/24 | ~12% | 4.6 min |
| qwen3.6:35b-a3b, thinking | ~22/24 | ~6% | 26 min |
| qwen3.8:27b-mlx, no thinking | 24/24 | ~6–7% | ~17 min |
| qwen3.8:27b-mlx, thinking | ~22.5/24 | ~3% | ~64 min |
| GLM-5.3 (hosted) | 24/24 | ~2% | ~5 min |

Without thinking, Qwen3.8-27B already covers every key point; its errors
cluster in small talk (e.g. "Kevin: it'll take four to five years" turned
into the owner's children's ages). With thinking, its points on the first
two meetings read like GLM-5.3's, nuances included, at about 0–1 doubtful
points per meeting. But a 34-passage meeting took 26 minutes.

With thinking, the third meeting also had almost no wrong points, but it
dropped two key points the non-thinking run had (a deadline, and counsel's
"no changes needed if purely R&D"). Thinking trades a little coverage for
precision. Its errors are rare and mostly honest: it quotes a garbled line
rather than inventing a meaning.

Where this leaves local extraction on a 48 GB M5 Pro:
- **Qwen3.8-27B with thinking:** about 3% wrong, close to the hosted
  ceiling, but ~20 min per long meeting. The 81 recorded meetings would
  take most of a day.
- **Qwen3.8-27B without thinking:** full coverage at about 6–7% wrong in
  ~6 min per meeting. Its errors concentrate in small talk.
- **None of this fits ordinary consumer hardware.** A dense 27B plus
  context needs 22–35 GB. For other users, extraction needs a pluggable
  backend (a company-approved endpoint) or a much smaller trained model.

## 2026-10-11 — Extract fast, then check? No.

Idea: extract with Qwen3.8-27B without thinking (fast, full coverage,
~9% wrong points by hand label), then check each point against its cited
lines (±3) with a judge, in groups of six. Scored against hand labels on
the same 99 points:

| Judge | Errors caught (of 9) | Correct points flagged | Time |
|---|---|---|---|
| GLM-5.3 (hosted) | 5 | 5 | 1.7 min |
| Qwen3.8-27B, no thinking | 4 | 9 | 5.5 min |
| Qwen3.8-27B, thinking | 3 | 7 | 34 min |

All judges share two blind spots:
- inventions that the meeting brief (written by the same extractor)
  appears to "establish";
- facts stated outside the cited window, flagged as unsupported
  ("Ignite", the unrelated deal's name).
The thinking judge cost more than half the time of thinking-on
extraction and caught the fewest errors.

Conclusion: reason during extraction rather than verify afterwards.

## 2026-10-11 — Making thinking-mode extraction fast: llama.cpp, budget, sampling

Goal: Qwen3.8-27B thinking-on quality (~3% wrong) at less than 64 min
per three meetings. Hugging Face is blocked on this network, so only the
Ollama registry and PyPI were usable.

| Setup (Qwen3.8-27B, thinking on) | 3 meetings | Coverage | Wrong |
|---|---|---|---|
| Ollama MLX, sequential, greedy, unbounded thinking | 63.6 min | ~22.5/24 | ~3% |
| llama.cpp (Ollama's GGUF) + native MTP + 4 parallel sections + 3k thinking budget + Qwen sampling (T=0.6, top-p 0.95, top-k 20); brief without thinking | **38.4 min** | **~23.5/24** | **~1–2%** |
| GLM-5.3 (hosted ceiling) | ~5 min | 24/24 | ~2% |

What mattered, in order:
1. **Greedy decoding with thinking loops.** At temperature 0, sections
   ran to 8k+ thinking tokens, and the meeting brief to ~14k (which was
   also the 4–7 min brief time on MLX). With Qwen's recommended sampling
   and a 3,000-token reasoning budget (`--reasoning-budget`, with a "time
   is up" message), sections settle at ~3–4k generated tokens. Quality
   held or improved.
2. **The brief needs no thinking.** Now 1–1.5 min instead of 4–7.
3. **Parallel sections.** llama.cpp's continuous batching gives ~39 tok/s
   aggregate across 4 slots, against ~31 single-stream on MLX.
4. **Native MTP** (`--spec-type draft-mtp`; Ollama's GGUF ships the
   head). +33% single-stream, ~1.14× aggregate; draft acceptance rises
   from 49% (greedy) to 71–75% with sampling.
5. llama.cpp enforces the JSON schema (0 parse failures) and returns
   logprobs, so the yes/no checks keep working.

Dead ends, all measured:
- **Ollama MLX:** no batching (open PR); no structured output in the
  Homebrew build (missing xgrammar).
- **MTPLX on MLX:** the Ollama MLX blobs are NVFP4 with the MTP head
  included. Converting them to MLX affine 4-bit took 9 s and loads, but
  ran 24 tok/s with or without MTP and gained nothing from batching.
  MTPLX's own published weights (88 tok/s on M5 Max) are on Hugging Face.
- **Post-hoc verification:** see the previous entry.

Machine notes:
- An M5 Pro has about half an M5 Max's memory bandwidth; published Max
  numbers roughly halve here.
- Leftover servers holding 19 GB caused a GPU out-of-memory in
  llama.cpp. One model server at a time.

## 2026-10-12 — Points ported to Musubi; spelled-number bug

The points prototype is now in Musubi behind `knowledge.extract = "points"`
(default stays `"statements"` until the Go port has been run on the pilot).
Ported as measured (known facts, brief without thinking, segments of 5
with neighbouring context, thinking with Qwen sampling, few-shot rules,
name/figure/quote checks in code, merge in code), with three deliberate
changes, none yet measured:
- The rule "skip … personal life" is gone; the few-shot rule already said
  to keep personal details, so the prompt contradicted itself.
- Points carry `personal: true|false`, which labels them `personal`
  (restricted, owner only, never in replies).
- Quotes are now checked (in the cited line or the one before); the
  prototype checked only names and figures.

**Bug found while porting:** the number reader trimmed zeros from whole
numbers, so "forty" read as 4, "ninety" as 9, "ten" as 1 and "two hundred
and fifty thousand" as 25. Since grounding was added (ae906b9, 2026-10-10)
this made it flag faithful
statements whose figures were spelled out in the source ("90 days" from
"ninety days" was "figure not in source"), and the search text added the
wrong digits (a search for "90 days" missed "ninety days"). Fixed; search
text is recomputed for every statement (`SearchVersion` s2, no model).
Stored grounding flags keep the old error until statements are
re-extracted. The synthetic eval is unchanged (claims 8/10, the same two
known failures before and after).

## 2026-10-12: Insight extraction, reference and prompt check (4 leadership meetings)

The corpus is four consecutive weekly leadership meetings: Teams transcripts, about 1,900 turns. The design is in docs/insights-direction.md.

**Reference.** Four Opus sub-agents each built an exhaustive reference for one meeting:
- 670 items across 12 kinds (decision, commitment, fact, metric, position, disagreement, framing, rule, outside_fact, question, sensitivity_cue, personal), each with lines, quotes, who, thread, use-when cues and sensitivity;
- a speaker for every turn.

Their speaker calls agree with 242 of 244 turns labelled by hand beforehand.

**Prompt check.** Raw chat calls on Baseten GLM-5.3, reasoning off, with nothing in the context but our prompts:
- speaker repair, given the company directory (name, title, office);
- a meeting brief;
- extraction over 50-line segments with 8 lines of context either side, using an invented worked example.

An Opus judge per meeting graded every item. On a 20-item sample it agreed with my blind grades on 17, and was at least as strict on the rest.

| Measure | Result |
|---|---|
| Speakers right (named reference turns, high/medium confidence) | 92–98% per meeting (raw Teams labels: 24–75%) |
| Reference items found / partly / missed | 560 / 71 / 39 of 670 (84% / 11% / 6%) |
| Extracted items correct | 583 of 661 (88%) |
| Wrong person | 50 (7.6%) |
| Wrong | 15 (2.3%) |
| Invented | 7 (1.1%) |
| Trivial | 6 |
| Cost | ~$3 for the successful run (total spend including dead ends: $4.48) |

Findings:
- **Attribution is the main remaining error.**
  - Most wrong-person items trace to speaker repair on unidentified or merged voices: an unnamed voice was given a colleague's name, and a presenter speaking through a room device was credited to someone else.
  - On the meeting with merged multi-speaker blocks, 28 of 174 items name the wrong person.
- **The directory helps and hurts.**
  - It fixed device labels that carry another executive's voice.
  - The model copies titles and surnames from it into items, which the judge counts as invented. Prompt fix: the directory is for identifying speakers only.
- **The wrong items are real reading errors:** two similar renewals conflated, a counter-argument reversed, part of a definition read as a count, a restated target read as a forecast, a suggestion recorded as a decision.
- **Reasoning on Baseten GLM-5.3 is all or nothing,** and with it on, calls loop until the token limit. All runs above had reasoning off.
- **Reference quality.** The judge overruled the reference twice in one meeting (who said a line). The reference is good but not exact.

**Clean ceiling (same day).** GLM-5.3 extraction was re-run on the reference speakers, as if the upstream speaker identification were right. Low-confidence speakers were passed as unknown. One prompt fix was added: the directory is only for recognising names.

| Measure | Repaired speakers | Reference speakers |
|---|---|---|
| Reference items found / partly / missed | 84 / 11 / 6% | **88 / 7 / 5%** |
| Extracted items correct | 88% | **94.5%** (630 of 667) |
| Wrong person | 7.6% | 1.5% |
| Wrong | 2.3% | 1.8% |
| Invented | 1.1% | 1.0% |
| Trivial | 0.9% | 1.2% |

Wall time was 2 min 50 s with all segments in parallel (the earlier sequential run took ~15 min). Cost ~$1.2.

What's left:
- **Wrong person:** almost all are unknown-speaker lines where the device label was still shown, and the model used the label's name. Fix: show only "unknown".
- **Invented:** surnames and background the model knows but nobody said ("Atrium, a Salesforce consulting partner"), despite the rule.
- **Wrong:** similar names confused (Gradial/Qualified, CMP/CMS), two renewals merged, a suggestion stated as a decision, a quarter computed wrongly.

Wrong plus invented is 2.8%. This is the ceiling cheaper models are compared against.

## 2026-10-12: Cheap grader, lean prompt, model sweep

**Cheap grader.** Gemini 3.8 Flash on Vertex, reasoning low. It works in four steps:
1. Pair each extracted item with nearby reference items by cited line.
2. Ask a side-by-side "same / partly / none" question in batches.
3. Check the items left unmatched against their transcript lines.
4. Check, in code, who said it against the reference speakers.

A reverse pass ("is this reference item covered by the items nearby?") measures recall. It costs about $0.20 per ELT meeting and takes about a minute. On 8 meeting-runs that Opus had graded, it agreed with Opus item by item on ~90%, ranked runs the same way, and read about 1.5 points stricter on wrong + invented. Opus is kept for audits.

**Lean prompt for Flash** (Sep 23 first, then four meetings):
- Dropping quotes, use_when, thread and sensitivity from the output (~45% of output tokens) costs no quality.
- An exhaustiveness rule (one item per distinct point, never merge; "12–20 items per 50 lines") cut misses from 48 to 15 on Sep 23. Across four meetings it plateaued at 17% missed, with 27% only partly found.
- The brief is worth about 3 points fewer misses for ~35% more cost.
- 30-line segments: marginal gain. 100-line segments: worse.
- Cutting segments by size instead of line count: a wash overall.
- Default reasoning: small recall gain for ~3x cost.

**Model sweep.** Lean exhaustive prompt with brief, Sep 23 + Oct 7 (295 reference items), reference speakers, reasoning low.

| Model | Found / partly / missed | Wrong + invented | Extraction cost per ELT meeting |
|---|---|---|---|
| GLM-5.3-Flash (Baseten) | 85 / 10 / 5% | 6.3% | ~$0.02 |
| Gemini 3.8 Flash (Vertex) | 46 / 30 / 24% | 2.7% | ~$0.12 |
| Qwen3.8-27B local, no reasoning | 44 / 33 / 23% | 6.7% | free; ~12 min per meeting at 8 parallel requests |
| gpt-oss-120b (Baseten) | 32 / 34 / 34% | 14.6% | ~$0.02 |
| DeepSeek V4.1-Flash | failed: malformed JSON on the brief, twice | – | – |
| *GLM-5.3, older prompt* | *~83 / 11 / 7%* | *~4.5%* | *~$0.30* |

Local speed for this run: 155k tokens in, 37k out, about 25 tok/s generated in aggregate. Prefill stays at ~220 tok/s.

Infrastructure lessons:
- llama-server with 8 slots splits its context; long whole-meeting prompts need `-kvu` (a unified KV cache).
- Vertex's OpenAI-compatible endpoint needs every schema field marked required, or Flash drops fields.

**Grader choice.** GLM-5.3-Flash as the cheap grader agreed with Opus item by item about as well as Gemini Flash did (88% candidates, 90% references), and its recall numbers matched Opus more closely, at about $0.03 per meeting against $0.19. **But it is lenient on its own output.** On GLM-5.3-Flash extractions it passed 22 items Gemini failed; reading them, Gemini was right on 16: proposals stated as decisions, small additions, guessed speakers. Rule: GLM-5.3-Flash grades other models; Gemini grades GLM-5.3-Flash.

**GLM-5.3-Flash prompt v2.** Added to the lean, exhaustive prompt:
- status discipline: a suggestion stays proposed;
- nothing not said: no surnames, titles, departments or explanations; no items built on misheard words;
- an unknown speaker stays unknown, shown without its device label;
- cite every line the item draws on.

All four meetings, reference speakers, graded by Gemini Flash:

| | v1 | v2 |
|---|---|---|
| Items | 979 | 1,014 |
| Wrong + invented | 7.3% | **4.6%** |
| Wrong person | 0.4% | 0% |
| References found / partly / missed | 86 / 10 / 3.4% | 86 / 10 / 3.9% |
| Extraction cost, 4 meetings | ~$0.10 | ~$0.10 (325k tokens in, 77k out) |

For comparison, GLM-5.3 with the older prompt scored 4.5% wrong + invented and 83 / 11 / 7% by the same grader, at ~$1.20 for four meetings. GLM-5.3-Flash v2 matches it on precision and beats it on recall, at about a twelfth of the cost: ~2.5 cents per ELT-length meeting, under a cent for an average meeting.

## 2026-10-12/13: Local speed on the M5 Pro (llama.cpp, Qwen3.8-27B Q4_K_M)

Benchmark: one real meeting (34 passages) with its brief and 7 thinking segments run in parallel, fixed seeds. Measured with llama.cpp 0.6.0 (build 11429); tensor API reported on. Scripts are in testdata/private/insights-eval/points (`bench.py`, `run_bench*.sh`).

| Config | Aggregate tok/s | Per stream | Prefill tok/s | Draft accepted | Wall |
|---|---|---|---|---|---|
| q8_0 KV, ubatch 512, 4 slots, MTP | 32.3 | 9.9 | 277 | 72% | 917 s |
| f16 KV, 4 slots, MTP | 34.9 | 10.5 | 255 | 75% | 887 s |
| + ubatch 2048 | 28.1 | 8.5 | 215 | 74% | 1087 s |
| + n-gram speculation stacked on MTP | 36.6 | 11.2 | 227 | 61% | 823 s |
| f16 KV, ubatch 2048, 8 slots, MTP | 39.9 | 6.4 | 254 | 74% | 773 s |
| f16 KV, 8 slots, MTP | 38.0 | 6.3 | 276 | 73% | 800 s |
| f16 KV, 8 slots, DFlash2 draft (n_max 6) | **41.2** | 7.0 | 267 | 54% | **746 s** |
| f16 KV, 4 slots, DFlash2 draft | 29.0 | 8.8 | 256 | 56% | 1034 s |

Findings:
- **DFlash2 (z-lab Q8 draft, 2 GB) gains only ~8% with 8 slots** and loses with 4. Its draft acceptance falls to ~55% under thinking-mode sampling (T=0.6). The published 2–3x is for a single stream with greedy decoding; re-test it if extraction runs without thinking.
- **ubatch 2048 is slower.**
- **Prefill is stuck at ~220–280 tok/s whatever the settings.** This is likely how llama.cpp handles this hybrid architecture on Metal. It is the main remaining lever for long prompts.
- **Ceiling here:** about 40 tok/s aggregate for the 27B model.

In the 2026-10-12 sweep, extraction without thinking ran at ~25 tok/s aggregate, so ~12 minutes per ELT meeting with 8 parallel requests.

Not tried yet, per the local-model research (docs/local-models-2026-10.md):
- MLX servers with continuous batching (oMLX, vllm-mlx); neither documents schema-constrained output;
- Gemma 4 31B / 26B-A4B;
- Nemotron 3.5 Lightning 30B-A3B;
- Qwen3.8-27B at 6–8 bits.

## 2026-10-13: Opus audit of GLM-5.3-Flash prompt v2 (Sep 30, 285 items)

| | Opus | Gemini grader |
|---|---|---|
| Correct | 261 (91.6%) | – |
| Wrong + invented | **14 (4.9%)**: 13 wrong, 1 invented | 17 (6.0%) |
| Misattributed | 1 | – |
| Trivial | 9 (3.2%) | – |
| Reference found / partly / missed | 92 / 7 / 0.6% | 92 / 8 / 0.6% |

Opus is only a point more lenient than the Gemini grader here. An earlier estimate of "2–3% in Opus terms" was too optimistic. Sep 30 is the most number-heavy meeting, and most errors are numeric:
- a garbled figure read as a result;
- deal values summed wrongly;
- a number tied to the wrong total;
- two cohorts merged;
- "19 hours" made out of "had 19 go through".

There is also one example credited to the wrong person. Next lever: a code check that a figure in an item appears in its cited lines, not just somewhere in the meeting.

## 2026-10-13: Experiment instance, linking run, and a stamp bug

**Setup.** The four ELT meetings were loaded into a separate Musubi instance (`MUSUBI_CONFIG=testdata/private/elt-exp/config.toml`) as meeting drops with reference speakers. The 1,014 GLM-5.3-Flash v2 items were imported with `musubi import-items`, and `knowledge.extract = "none"`.
- The import check (names and figures must appear in the meeting) flagged 58 items (5.7%).
- Linking (local qwen3:8b decider) took 47.5 min for 1,014 statements: 979 claims, 35 joins, 1,011 judged pairs.
- Relations: 334 (224 refines, 53 supersedes, 41 answers, 15 contradicts).
- Topics: 95.

**Observations:**
- Topics thread across weeks: Limitless, the Microsoft programme, Q4 bookings and churn each span 3–4 meetings.
- Claims almost never merge across meetings (1 claim spans two). Weekly items are phrased differently and link by relations instead.
- One catch-all topic ("AI strategy and goals", 118 claims).

**Bug, fixed in 6fcdc77.**
- **What:** claim linking and calendar linking shared the global stamp "link", so every `musubi update` made the next `musubi knowledge` rebuild all claims from scratch.
- **Fix:** the claim stamp is now "claims", and it is recorded before linking, so an interrupted run resumes instead of clearing its progress.
- **Cost:** it threw away an earlier run's 757 linked statements (90 minutes). The installed app needs a rebuild to pick up the fix.

## 2026-10-14: Topic cards, strict linking, import values

Worked example: one confidential HR programme, raised in a different
meeting (`docs/topic-cards.md`; real data in
`testdata/private/`, the programme walkthrough).

**Finding.** The extracted items were good enough for a card, but the live
panel never read the knowledge layer, and three things hid what was there:
- belief order (corroboration) put a passing compliment first and a
  programme lead's dated milestones last ("speculative");
- the judge called complementary points from the same minute
  "supersedes", retiring the board target, the bucket status and an
  owner's commitment;
- a promise to find out "answered" the question it promised to answer.

**Changes** (each behind a config key that restores the old behaviour):
- `knowledge.strict_links` (link rules k16): see architecture.md, Claims.
- `knowledge.import_keep_proposed`: proposed commitments keep their kind;
  dates and figures read from text into typed values (71 of 1,014 items);
  the import check accepts the meeting date and known full names
  (ungrounded 58 → 39; most of the rest are names the extractor spelled
  from its own knowledge).
- Sensitivity rules r4: reduction-programme vocabulary ("terminations",
  "exit names", "headcount reductions"). HR-labelled passages 2 → 5 of 39;
  the passage with the exit list never uses such words, which needs the
  model-based classify stage.
- `live.topic_cards`: topic cards (architecture.md, Topic cards).

**Relink (k16, same model and statements as k15).** 46 min; 979 claims,
35 joins (unchanged); relations 334 → 341: supersedes 53 → 29, refines
224 → 252, answers 41 → 38, pending 4, contradicts 15 → 16. On the walkthrough's pairs all
five same-meeting "supersedes" became "refines" and the promise became
"pending"; one "answers" the reference calls wrong is unchanged (a hope
about where savings go, judged to answer whether they may fund hires).

**Card eval** (`testdata/private/card-cases.toml`, 10 cases: the gold card,
3 paraphrases, a replay date, 3 leak scopes, 2 lookalikes):

| | pass | expected lines found | leaks |
|---|---|---|---|
| k15 links, old import | 7/10 | 51% | 0 |
| k16 links, new import, card fixes | 8/10 | 78% | 0 |

The gold card has 10 of 11 lines; the miss is the linking error above.
The other failure is a paraphrase ("use the savings to backfill hires")
whose nearest claim is right but at 0.61, below the 0.66 floor; lowering
it is untested against lookalikes. A card takes ~30 ms to build and
appears 6 ms after its term is said on a scripted replay.

**Linking is ungraded on these meetings.** 93% of judge calls return
"unrelated" at ≈1.00, claims barely merge (35 of 1,014), and the pairwise
judge has no context; a thread-level reconciliation (one call per topic
touched per meeting, with the topic's current state) is the alternative to
benchmark next, on a stratified sample of pairs plus reference thread
states.

## 2026-10-14 (later): Linking benchmark, baseline

Method: `docs/linking-benchmark.md`. Reference by five Opus sub-agents:
227 pairs labelled blind (11% low confidence), and 15 subjects across the
four meetings with 833 member items, 182 updates and 156 near misses.

**Current linker (k16, qwen3:8b per-pair judge):**

| Measure | k15 | k16 |
|---|---|---|
| Its supersedes the reference agrees with | 3/29 | 2/29 |
| Its answers the reference agrees with | 21/38 | 21/38 |
| Its contradicts the reference agrees with | 3/16 | 3/16 |
| Its same / refines agreed | 25/35, 33/40 | 25/35, 34/40 |
| Sampled pairs it wrongly retires (of those to keep current) | 25% | 22% |
| Reference updates it relates at all | 25% | 26% |
| Reference updates that retire, retired | 26% | 25% |
| Reference-current items (after 10-07) it retired | 7.3% | 4.0% |
| Grouping, B-cubed precision / recall (topics vs subjects) | 0.73 / 0.21 | 0.73 / 0.21 |

(Pairs are stratified, so per-verdict rates describe the verdicts, not the
corpus.) What the wrong verdicts really were: of 27 wrong supersedes, 10
were unrelated, 10 the same subject and 6 refinements; of 17 wrong answers,
14 were replies to a different question on the same subject. Pairs it never
compared were rarely missed links (2/30): the misses are updates between
items it did compare and called unrelated or refines, and updates across
meetings whose items sit in different topics.

**Grouping.** Subjects fragment: a programme or deal holds together (one
topic holds 71–84%), but a running analysis needs 9–12 topics to hold 80% of
it, and near misses share a topic with members in 2/9 to 10/15 cases. The
conference subject and the marketing workstream's brand packs met in one
topic on the word "events".

**Search and cards** (36 queries over the 15 subjects): top-5 passages
69% on-subject, claims 85%, card lines 73%; cards show 15% of a subject's
current items. Paraphrases open the wrong card in 4 of 36 queries (e.g.
"brand packs" → a CRM-migration topic).

**Card fixes from this:** a catch-all topic (discussion runs covering more
than 40% of a meeting) hosts nothing; a term spread over topics opens a
card of the claims that say it, with at least 4 claims, and not when the
word ends a longer name ("New York").

## 2026-10-14 (later): Linking candidates on the benchmark

Same 1,014 statements, same reference. Pairwise runs relink with a
different judge; the thread run is `tools/link-bench/threads.py` (assign
each meeting's items to specific threads, then reconcile each touched
thread with one call), loaded into a database copy for cards and search.

| | 8B pairwise (k16) | 4B pairwise | 1.7B pairwise | Threads, GLM-5.3-Flash |
|---|---|---|---|---|
| Sampled pairs wrongly retired | 22% | 3.5% | 1.5% | **1.0%** |
| Retiring pairs left current | **21%** | 45% | 86% | 38% |
| Reference updates related at all | 26% | 20% | 36% | 34% |
| Retiring updates retired | 25% | 15% | 6% | **38%** |
| Grouping, B-cubed P / R | 0.73 / 0.21 | 0.73 / 0.21 | 0.21 / 0.81 | **0.93 / 0.48** |
| Current items wrongly retired | 4.0% | 1.3% | 2.6% | 2.6% |
| Card lines on subject (36 queries) | 76% | | | **83%** |
| Time, cost | 46 min local | 31 min local | 21 min local | 15 min, 135 calls, $0.12 |

- **1.7B** calls most pairs "same": 243 claims, 23 topics. Unusable.
- **4B** is a better pairwise judge than 8B on what hurts most (false
  retires 22% → 3.5%) at two thirds of the time, but finds fewer updates.
- **Threads** keep subjects together (precision 0.93, recall 0.48 against
  0.73 / 0.21), almost never hide a live item, and retire half again as
  many real updates. Conference anecdotes split by what they were about
  (partnership, event presence, positioning), and brand packs sit in the
  marketing programme. Paraphrased card lookups that went wrong mostly go
  right ("brand packs", "credits in the order form", "Q4 pipeline goal",
  "Mark credit consumption"). Still weak: 38% of retiring updates are
  missed; items raised during a subject's discussion but not naming it
  (a sensitivity cue about an offshore team) land elsewhere, so the programme card has 7 of 11
  expected lines (11 of 11 with topics plus companions); a one-meeting deep
  dive looks like a catch-all to the card index, so a product name opens the
  wrong card.
- First thread run: the assign step's output collapsed on 6 of 16 chunks
  (no or few assignments), leaving 36% of items unthreaded. Asking again
  for the missing items fixed it (987 of 1,014 assigned).
- Card index: requiring two meetings of wide coverage before a topic counts
  as a catch-all cost 13 points of on-subject card lines on k16 and 9 on
  threads; the share cap for common terms made no difference.

**Direction:** grouping and update tracking move to the thread level; the
pairwise judge stays only for near-duplicate joins, if at all. Next: the
assign step should see each item's neighbours (to keep discussion
companions with their subject), reconcile should run with the local
Qwen3.8-27B for comparison, and the card index should anchor on threads
instead of inferring companions.

## 2026-10-14 (night): Threads in Go, on the local 27B

`knowledge.grouping = "threads"` (architecture.md, Threads) ported from the
prototype, run on Qwen3.8-27B (llama-server, 8 slots) over the four ELT
meetings: 68 min, 106 calls, 68 threads, 978 of 1,014 statements threaded.

- **Decoding loops.** At temperature 0 the 27B looped on reconcile replies
  to the 16k-token cap (20 min a call, `finish: length`). Qwen's
  non-thinking sampling (0.7 / 0.8 / 20) with caps of 6k (assign) and 3k
  (reconcile) tokens stopped it; a cut-off reply is retried once.
- **GPU contention.** An earlier attempt crawled at 0.9 tokens/s while
  Ollama sweep runs shared the GPU; alone it runs at 23–27 tokens/s with a
  JSON schema.
- **Relation rules.** The 27B called 84 pairs "completes", 70 of them on
  views and facts. Rules measured per kind on both thread runs:

  | Rule | GLM: dropped / real | 27B: dropped / real |
  |---|---|---|
  | completes only a commitment or decision | 2 / 1 | 70 / 5 |
  | completes not in the meeting that made it | 5 / 0 | 11 / 0 |
  | in-meeting supersedes needs a spoken correction | 18 / 5 | 27 / 3 |
  | answers only a question (not kept) | 8 / 4 | 5 / 2 |

  Kept the first three (`threadRule`, threads version t2).

| | 8B pairwise (k16) | Threads, GLM-Flash, rules | Threads, Go, 27B, rules |
|---|---|---|---|
| Sampled pairs wrongly retired | 22% | 0.5% | 2.0% |
| Retiring updates retired | 25% | 32% | 25% |
| Reference updates related at all | 26% | 30% | 19% |
| Grouping, B-cubed P / R | 0.73 / 0.21 | 0.93 / 0.48 | 0.94 / 0.43 |
| Current items wrongly retired | 4.0% | 1.3% | 4.0% |
| Card lines on subject (36 queries) | 75% | 83% | 78% |
| Card cases (corrected), pass / line recall | 9/10, 0.80 | 8/10, 0.73 | 8/10, 0.73 |
| Time | 46 min | 15 min, $0.12 | 68 min, local |

Local threads keep the grouping gain and the low false-retire rate, and
cards are better on the broad queries; they find fewer updates than the
hosted model, and the hand-written programme card loses the items raised
during its discussion that the threader put elsewhere (with topics, the
card index pulled them in as companions). Cards show about 15% of a
subject's current items in every setup: the section caps, not grouping,
limit that.

## 2026-10-15/16: Lean threading on hosted GLM-5.3-Flash

Cost cuts to the thread design (`tools/link-bench/threads2.py`, "lean"):
grouped assign output with thread codes; reconcile returns only effects,
with the current list kept in code under `threadRule`; candidate threads
per assign call (40 nearest by embedding, 15 recently active, and those
born this meeting); at most 25 current items per reconcile; and no
reconcile for threads born this meeting. Two new benchmark tiers for
iteration: a synthetic set (250 invented items, 12 threads, 142 pairs,
planted failures; `testdata/synthetic/link-bench/`) and a real subset (395
items, 6 threads, 77 pairs).

**Synthetic set** (GLM-5.3-Flash; original design with the relation rules
applied afterwards):

| | Original | Lean | Lean, low reasoning |
|---|---|---|---|
| Grouping, B-cubed P / R | 0.96 / 0.82 | 0.97 / 0.84 | 0.96 / 0.84 |
| Live items wrongly retired (pairs) | 0% | 1.1% | 1.1% |
| Retiring updates retired | 69% | 65% | 55% |
| Calls | 116 | 83 | 75 |
| Cost | $0.034 | $0.027 | $0.015 |
| Output tokens (of them reasoning) | 42.6k (33.4k) | 32.2k (26.1k) | 10.5k (5.1k) |

- **Reasoning is most of GLM's output** (about 80%), so a compact answer
  format cuts cost less than call counts suggest. Low reasoning effort
  halves the cost and keeps grouping, but retires fewer real updates.
- **Skipping reconcile for new threads failed on real data.** On the full
  set it retired 7% of the reference's retiring updates (original: 32%).
  64% of the real reference's updates are within one meeting (answers 49,
  contradicts 35, corrections 12, of 182); on the synthetic set only 20%
  are, which is why it looked harmless there. The synthetic set
  under-represents in-meeting updates.
- **Next try:** the assign call also reports effects among the items it
  is grouping (same meeting), and reconcile runs only for threads with
  history.

**Hosted GLM failures, fixed in `tools/insights-eval/bt.py`:**
- Non-streamed replies that take a while to generate hung for 5–30 min
  (or never returned); the same requests streamed in seconds. Replies now
  stream (`BT_TIMEOUT` is the wait per chunk).
- Some replies looped in reasoning at about 2 tokens/s until the token
  cap; a reply still streaming after `BT_WALL` (180 s) is abandoned and
  asked again. Calls cut off at the cap are retried with twice the room.

**Inline effects failed.** Asked to report effects among the items it is
grouping, the assign call returned almost none on real data (3 relations;
0 of the real subset's 66 reference updates). It looked fine on the
synthetic set (65% of retiring updates retired, no false retires, $0.030),
which again under-represents in-meeting updates. Updates are found by the
per-thread reconcile call; the cost cuts must keep it.

**A hand-picked real subset misleads.** The original design run on the
subset alone (the members and near misses of 6 subjects) found 15% of
those subjects' updates; the same design run on all items found 29% of the
same updates. Taking the surrounding items away changes what the model
does. A small real tier must be whole meetings.

Costs here are from the spend ledger per run tag; the scripts' printed
"$" is the change in total spend and includes concurrent runs.

**Lean with every touched thread reconciled** (compact outputs, candidate
threads, capped state; full real set, two runs each to see the noise):

| | Original + rules | Lean |
|---|---|---|
| Grouping precision | 0.93, 0.95 | 0.87, 0.86 |
| Grouping recall | 0.48, 0.46 | 0.51, 0.53 |
| Retiring updates retired | 32%, 24% | 35%, 30% |
| Updates related at all | 30%, 20% | 28%, 24% |
| Live items wrongly retired (pairs) | 0.5%, 1.0% | 2.5%, 1.5% |
| Current items wrongly retired | 1.3%, 0.7% | 2.0%, 1.3% |
| Threads | 88, 99 | 59, 61 |
| Cost (ledger) | $0.152, $0.125 | $0.093, $0.092 |

- **Run-to-run noise is 5–10 points** on the update measures at the hosted
  default temperature (0.7); single runs can't separate designs that
  close. Grouping precision and thread count differ consistently.
- Lean is 30–40% cheaper and update tracking is the same within noise;
  it groups more broadly (a few umbrella threads, one of 65 items), not by
  merging lookalikes: near misses sharing a member's thread are 42 and 50
  of 153, against 51 and 36.
- Reasoning is 85–90% of GLM's output in both designs.

**Flat prompts over a long history.** A 20-meeting replay (the four
meetings five times, four weeks apart): assign prompts stayed at 7.6–8.3k
tokens and reconcile prompts at 1.8–2.4k across 13 meetings, while the
original's assign prompt grew from 5.2k to 10.2k in four. The thread count
levelled at about 64 (the replay repeats its subjects, which were reused
rather than duplicated).

**Two more variants** (full real set, one run each; read against the noise
above):

| | Lean | + "same specific subject" wording | + wording, low-reasoning assign |
|---|---|---|---|
| Grouping precision / recall | 0.87 / 0.51, 0.86 / 0.53 | 0.86 / 0.49 | 0.87 / 0.53 |
| Retiring updates retired | 35%, 30% | 29% | 33% |
| Live items wrongly retired | 2.5%, 1.5% | 1.0% | 1.5% |
| Threads | 59, 61 | 68 | 56 |
| Cost | $0.093, $0.092 | $0.102 | **$0.074** |

- The wording didn't raise grouping precision: dropped.
- Low reasoning for assign calls only (reconcile keeps the default) cost
  20% less with no measurable loss: assign reasoning tokens halved. Reconcile
  is now two thirds of the cost, 94% of it reasoning. Low reasoning for
  reconcile lost real updates on the synthetic set, so it stays.
- **Lean against the original:** $0.074 against $0.125–0.152 per four
  meetings (about half), about 2.5 cents per ELT-length meeting over a long
  history, update tracking the same within noise, grouping precision 0.87
  against 0.93–0.95.

**Ported to Go** (`internal/knowledge/threads.go`, ThreadsVersion t3):
codes and groups in assign replies; candidate threads (40 nearest by the
statement vectors' thread centroids, 15 active in the last three weeks,
those born this item); effects-only reconcile for every touched thread,
the current list kept in code under `threadRule`; 25 current statements
per reconcile. Config `knowledge.threads.candidates` / `state_cap`.

**Lean Go threads on the local 27B** (four meetings, llama-server, 8 slots):

| | Threads t2 | Lean t3 |
|---|---|---|
| Time | 68 min | **34 min** |
| Grouping, B-cubed P / R | 0.94 / 0.43 | 0.90 / 0.43 |
| Retiring updates retired | 25% | 21% |
| Live items wrongly retired (pairs) | 2.0% | 1.5% |
| Current items wrongly retired | 4.0% | 3.3% |
| Card lines on subject (36 queries) | 78% | 78% → **84%** with phrase lookup |

Per meeting: 8, 4.5, 11.5 and 10 min (was 17, 13, 20 and 17). The update
measures are within the run-to-run noise.

**Phrase lookup for cards.** A short query ("brand packs") embeds poorly;
its nearest-claims vote opened another thread's card although the item was
in the right thread. When the query's words (2–5 content words) appear
verbatim in at most 12 claims, most of them in one thread, that thread's
card opens. On-subject card lines 78% → 84% on the lean threads; 75% on
topics, unchanged.

## 2026-10-16: Datasets for side-by-side comparison

The app now opens any instance listed in `~/.config/musubi/datasets.toml`
and can show two side by side. The experiment instances were replaced by
datasets (linking-benchmark.md, "Datasets"): two references built from the
Opus outputs (the 670 reference items, grouped by their own thread labels;
and the GLM items in the 15 reference subjects with the 182 reference
updates), the best GLM setup (lean threads on GLM), an unbuilt best-local
slot (needs local extraction of all four meetings), candidate slots, and a
base copy for rebuilding. All were assembled from existing outputs with no
model calls. The old real instance (81 recordings, 9 pilot meetings on the
old statement prompt) and the earlier experiment instances were deleted;
their scores are above and their outputs remain as system files.

## 2026-10-16: Topic map, thread links and dataset comparison

**What was wrong.** Thread mode never wrote topic centroids or relations,
so every thread sat at the same point on the topic map and no topic page
had related topics. Where the map did work (topic mode), it was a scatter
by meaning with no links drawn; links existed only as lists on topic
pages. And comparing two datasets side by side was hard because their
topics don't line up.

**Thread links** (`LinkThreads`, no model call). Centroids from member
claims and relations by meaning or a shared organisation or project.
Sharing meetings is not stored for threads: on four meetings, 1,523 of
1,540 thread pairs shared two of them.

**Links and clusters when read** (`Views.Map`). Discussed back to back (two
or more times), shared statements, the same organisation or project, and
meaning above the dataset's top fifth of pairs. A fixed meaning threshold
(0.62) made 187 of 210 links "similar"; the relative one, 95 of 154.
Louvain over those links:

| Dataset | Topics | Links | Clusters | Not linked | Time |
|---|---|---|---|---|---|
| Best GLM | 56 | 154 | 7 | 1 | 80 ms |
| Reference · Opus items (no vectors) | 153 | 147 (all "together") | 15 | 13 | 37 ms |

The clusters read as subjects: one holds the Salesforce partnership,
Dreamforce, the Slack integration, the Marketo migration and the Q2R
migration. Another holds a confidential programme with the HR and
litigation threads. Not evaluated against a reference grouping yet.

**Comparison** (`CompareDatasets`):

| Left → right | Matched by | Same | Split | Merged | Partial | Only left | Only right |
|---|---|---|---|---|---|---|---|
| Best GLM → reference subjects | statements (83% shared) | 4 | 5 | 22 | 6 | 19 | 0 |
| Opus items → Best GLM | wording | 26 | 8 | 12 | 57 | 50 | 2 |

"Merged" dominates the first row because the reference has 15 broad
subjects and Best GLM 56 narrower threads; "only left" is mostly threads
outside those 15 subjects. Wording matches are weaker evidence than
shared statements; the "partial" band (similarity 0.25–0.5) needs
reading, not counting.

## 2026-10-16: Cost measures (scaling.md §8, A1) and low reasoning for reconcile (A2)

**A1, measured without new calls.** The best GLM run (lean, low-reasoning
assign) replayed exactly from the call cache with a trace
(`threads2.py TRACE=`), scored by `tools/link-bench/costs.py` against the
spend ledger and the reference subjects:

| Step | Calls | Input | Served from cache | Output | Reasoning | $ | Share |
|---|---|---|---|---|---|---|---|
| assign | 18 | 110k | 0% | 17k | 52% | $0.025 | 30% |
| reconcile | 77 | 119k | 2% | 78k | 94% | $0.057 | 70% |

Fitted prices: $0.15 per million input tokens, $0.50 per million output.

- **Reconcile calls that change nothing are cheap.** 59% of reconcile
  calls kept no effect after the rules (39% returned none), but they are
  the small ones: with 1–2 new items, 90% kept nothing; with 10 or more,
  21%. The no-effect calls cost $0.014 of $0.082 (17%). Gating them
  (A3) saves at most that; batching the 1–2 item calls (A4) at most
  $0.003.
- **Prompt caching is worth little here.** Baseten served 0–2% of input
  from its cache. 5% of assign prompts and 19% of reconcile prompts repeat
  an earlier call's start (the system prompt); a candidate list in stable
  order raises assign to 16%. Caching all of it would save about 6%.
- **Candidate recall can't be measured on four meetings**: 56 threads
  fit in the 40 + 15 candidates, so every item saw every thread (100%).
  When a thread holding the item's reference subject was offered, the item
  joined one 55% of the time; 257 of the 285 others started a new thread
  in the same call, mostly a finer split than the reference's 15 broad
  subjects. Needs the long real history.
- **Where the money is:** reconcile's reasoning, 94% of its output and
  about 45% of the whole bill.

**A2: low reasoning for reconcile, on the real set** (it had been ruled
out on the synthetic set only). Assign stays low; three runs each:

| Reconcile effort | $ per 4 meetings | Reconcile $ | Retiring updates retired | Updates found | Live items wrongly retired | Grouping P / R |
|---|---|---|---|---|---|---|
| default (= medium) | $0.082, $0.085, $0.085 | $0.057–0.059 | 31%, 30%, 31% | 25%, 25%, 26% | 1.5%, 1.0%, 0.5% | 0.87–0.92 / 0.50–0.53 |
| low | $0.052, $0.052, $0.056 | $0.024 | 33%, 24%, 26% | 26%, 20%, 21% | 1.5%, 1.5%, 1.0% | 0.87–0.92 / 0.48–0.52 |

- "medium" costs the same as sending no effort: it is the default.
- Low reasoning cuts reconcile's output from 78k to 16k tokens (reasoning
  94% → 72%) and **total cost by 35–37%**, with no replies cut off at the
  token cap (3–4 per run at the default).
- Update recall drops about 3 points on average (28% against 31% retired;
  22% against 25% found), within one run's noise but consistent across
  three; wrong retirements don't rise; grouping is unchanged.
- With assign also at low, threading costs about 1.3¢ per ELT meeting,
  down from 2.1¢. This is the hosted prototype; the Go threader runs the
  local 27B without thinking, so the setting applies when it gets a
  hosted model.

Spend for A1 and A2: about $0.33.

**A1 for extraction** (the GLM-5.3-Flash v2 run behind every GLM dataset;
ledger, and its prompts rebuilt at their real sizes without calls):

| Part | Calls | Input | Output (reasoning) | $ | Share |
|---|---|---|---|---|---|
| Brief (whole transcript, once per meeting) | 4 | ~72k | ~10k (13%) | ~$0.024 | ~24% |
| Segments (50 lines + 8 of context each side) | 40 | 245k | 77k (6%) | $0.075 | ~76% |

- Extraction is about $0.10 per four meetings against $0.053–0.082 for
  threading: now the larger part of the bill.
- Reasoning is already low (6% of segment output); the output is the
  items themselves. No reply hit the token cap.
- **Each segment prompt starts with 11.8k characters that are the same
  for every segment of the meeting** (instructions 5.0k, company
  directory and brief 6.8k), 42% of all segment input. Baseten cached
  none of it, because all segments were sent at once. Earlier runs that
  sent calls one at a time had about 29% of their input served from cache,
  at about a fifth of the price (~$0.03 per million tokens, implied by
  the ledger). Sending each meeting's first segment alone and the rest
  after it should save about $0.012 per four meetings (~12% of
  extraction), for one call's wait per meeting.
- The brief costs about a quarter of extraction and was measured
  (2026-10-12) at about 3 points fewer missed items. Dropping it is the
  other lever; segment size (30 or 100 lines) and reasoning were measured
  then too and stay.

**Cost-only changes, verified (owner: no quality loss accepted, so low
reconcile effort and dropping the brief are not adopted).** Fresh hosted
runs, the prompts unchanged:

| Change | Before | After |
|---|---|---|
| Extraction: one meeting's segments sent with the same `x-session-affinity` header, first segment alone | 0% of segment input cached, $0.075 | 21% cached, $0.071 |
| Same, without the header (first segment alone only) | | 1% cached: Baseten's cache is per replica, and parallel calls spread over replicas |
| Reconcile: first attempt at 8,000 tokens instead of 4,000 | 3–4 replies cut off and rerun per run | 0–1; threading $0.081–0.082 against $0.082–0.085 |
| Reconcile: the same header | | 0% cached: its shared start (the instructions, ~500 tokens) is likely below the cacheable minimum |

Threading quality on the two runs: retiring updates retired 33% and 28%,
live items wrongly retired 1.0% and 2.0%, grouping 0.88–0.89 / 0.48–0.51,
all within the three default runs' range. Together about 4% off the
total. Only half the shareable extraction prefix was served from cache;
why is not known. Spend for these checks: about $0.36.

The header is in `bt.py` (`session=`) and in the Go provider
(`ChatOptions.Session`, sent as `x-session-affinity`, hashed); points and
threads send it per meeting and per item, and with
`knowledge.warm_cache` (default on) send the first segment and the first
reconcile alone.

## 2026-10-16: More context for extraction (no gain)

Question: with prompt caching, does giving extraction more context improve
quality? GLM-5.3-Flash, prompt v2, temperature 0, low reasoning, four ELT
meetings (670 reference items), graded by Gemini Flash. Two variants
(`extract_lean.py`):
- **rich**: the brief in full (every role, the subjects in order, every
  glossary term) instead of a few truncated lines;
- **full**: the whole transcript at the start of every call (shared by a
  meeting's calls, so cached), the part to cover named at the end; no
  brief.

| Run | Items | Reference found / partly / missed | Wrong + invented | Extraction $ | Input cached |
|---|---|---|---|---|---|
| base (original) | 1,014 | 85.7 / 10.4 / 3.9% | 4.6% | ~$0.10 | 0% |
| base (rerun) | 999 | 84.8 / 11.5 / 3.7% | 4.3% | $0.073 + brief | 10% |
| rich | 912 | 84.6 / 11.3 / 4.0% | 5.2% | $0.077–0.081 + brief | 12–20% |
| full | 899 | **80.7 / 13.4 / 5.8%** | 5.2% | $0.110–0.112 | **71–72%** |

The second rich run and the first full run were cut short by the grading
spend cap; on the meetings they covered they agree with the table (rich:
295 found, 7 missed on the first two meetings against the baseline's 295
and 9; full: 414 found, 24 missed on the first three against 449 and 10).

- **The whole transcript makes extraction worse**: fewer items, 4–5
  points fewer references found, about 50% more missed. Given 900 lines,
  the model extracts less from its 50; the narrow excerpt keeps it
  exhaustive.
- **The full brief changes nothing** within the noise, with fewer items
  and slightly more wrong ones.
- Kept: today's prompt (brief cut short, 50-line excerpts).
- On caching: a meeting-wide shared start was 71–72% cached with session
  affinity, so 4.6 times the input cost only 50% more. Caching works
  when the shared part is most of the prompt.

Spend: about $0.55 on extraction and $4.83 on grading.

## 2026-10-16: Reads at scale

`tools/scale-bench` generates a corpus shaped like the ELT meetings (per
meeting about 120 passages, 250 statements; subjects from a pool growing
as meetings^0.7, so 1,617 threads at 1,600 meetings) with random vectors,
and times the app's read paths. Speed and memory only: the text means
nothing.

**What broke:** the topic list and map failed at 400 meetings (100k
claims; any corpus past ~32k claims, about 130 ELT meetings). Claim
evidence was read with every id in one SQL statement, over SQLite's
variable cap, and with a query per claim and per statement.

At 1,600 meetings (192k passages, 400k claims, 1,617 threads):

| Read path | Before | After |
|---|---|---|
| Vector / hybrid search | 300 ms | 7–8 ms (index load 0.2 s, once) |
| Card lookup by meaning | 356 ms | 12–15 ms |
| Card index memory | 1,351 MB | 101 MB + ~38 MB of claim codes |
| Card index build | 3.8 s | 3.0 s |
| Thread links | 7.7 s | 2.1 s |
| Topic list, cold | failed past ~32k claims (7.2 s once batched) | 4.2 s |
| Topic map, cold / cached | 16 s / – | 1.2 s after the list / instant |
| Topic page | ~5 s (list + map each time) | 0.3 s |
| Keyword search | 1 ms | 1 ms |

How:
- **Evidence:** batched, and read in one pass when most claims are asked
  for. Statements are read once, not twice; kinds, topics and verdicts in
  bulk. Keywords come from a topic's last 200 claims.
- **Cache:** the topic list, map and pages share one evidence load,
  cached until the database changes (`data_version` and
  `total_changes`, which take constant time; counting rows took 70 ms at
  100k), the scope or the day. The app builds the map in the background
  when a dataset opens.
- **1-bit code index** (`internal/vindex`, sign bits, 96 bytes a vector,
  Hamming scan in memory): it proposes candidates, which are re-ranked
  exactly in SQL under the scope. sqlite-vec's own `vec0` bit index
  scanned the same codes at 52 ms for 192k (in Go: a few ms).
  - Recall: on synthetic vectors, 400 candidates held all of the exact
    top 10. On the 1,503 real nomic vectors, 7.5 candidates per result
    held 97% (98% with mean-centred codes, not adopted: codes would depend
    on a moving mean). Search takes twenty per result, at least 1,000.
  - Scope: a test with the nearest passages all confidential found the
    shared-screen results came from the few visible candidates, not the
    nearest visible ones. Fixed: the candidates must hold twenty visible
    per result, or the set widens to an exact scan.
- **Thread links:** claim centroids are streamed. Each thread is compared
  with its 50 nearest by code and with threads naming the same
  organisation or project (unless over 100 do). On Best GLM: 1,515
  relations against 1,523, the 8 missing just above the 0.62 threshold.

## 2026-10-16: Backfill: does the memory hold up? (No.)

Two datasets built the way Best GLM was (GLM-5.3-Flash prompt v2
extraction, lean threads on GLM; `tools/link-bench/backfill.py`), with
the four benchmark meetings keeping their benchmark extraction:
- **elt-year:** every leadership meeting, January to October: 24 meetings,
  5,643 statements, 353 threads.
- **sept-all:** every meeting of one month: 145 meetings (leadership, 1:1s,
  product syncs, customer calls, interviews, one personal appointment),
  12,328 statements, 650 threads.

Cost: $3.27 hosted ($1.50 extraction, $0.56 and $1.21 threading).
Threading took 28 minutes for the ELT year and about 1.8 hours for the
month.

**Benchmark carry-over** (the reference, scored on the benchmark meetings
inside the larger corpus; `carryover.py`):

| | Grouping P / R, 4 meetings | Grouping P / R, the 3 September meetings | Retiring updates retired | Live items wrongly retired |
|---|---|---|---|---|
| Best GLM (the 4 meetings alone) | 0.87 / 0.53 | 0.86 / 0.66 | 29–33% | 0.5–1.5% |
| elt-year (+20 earlier meetings) | 0.92 / 0.36 | 0.95 / 0.48 | 30% | 1.0% |
| sept-all (+141 other meetings) | – | 0.96 / 0.58 | – | – |

Subjects fragment as history grows. Candidate recall isn't the cause: a
thread holding the item's subject was offered 97% of the time, but the
item joined one 24% of the time (55% on the 4 meetings).

**Audits.** Opus read 76 threads end to end against the source lines
(`dossier.py`; findings in each dataset's `audit/`):

| | elt-year (49 threads) | sept-all (27 threads) |
|---|---|---|
| Items still "current" | 86% | 86% |
| Card quality (1–5) | 3.1 (2.7 for 50+ items) | 2.7 |
| Missed updates | 59 | 100 |
| Stale current items | 84 | 127 |
| Wrong retirements | 40 | 52 |
| Items in the wrong thread | 70 | 229 |
| Threads of several subjects or a theme | 15 | 8 |
| Fragmented subjects that should partly merge | 11 of 12 | – |
| Wrong speaker | 8 | 50 |
| Noise (small talk, trivia) | – | 178 |
| Sensitive items | 48 | 339, of which 67 sit in threads with ordinary business items from other meetings |

**Mechanisms**, the same across both datasets:
1. **Reconcile doesn't see what it should retire.** It is shown a thread's
   25 latest current items, so in a long thread a September outcome never
   meets the March plan it settles. Most of the 86% current comes from
   this.
2. **Updates land in another thread.** Later wording starts a new thread or
   picks a sibling. Outcomes (actuals, a deal lost, a name chosen) never
   reach the forecasts and plans they settle.
3. **Nothing expires.** Dated plans, event preparation, "next week"
   commitments and quarterly forecasts stay current after their dates.
4. **No rule for quantities:** a later figure for the same quantity and
   period doesn't replace the earlier one.
5. **Wrong retirements:** the newest statement retired while an older one
   stays; contradictions or added context treated as replacements;
   duplicates retired inconsistently.
6. **Grouping follows wording and agendas:**
   - fragments: one meeting's discussion split by the angle of each
     sentence; a spelling variant of a name splits an account;
   - merges: shared names ("the all-company call"), generic words
     ("migration", "value"), a single bridging item;
   - people: threads about a person absorb everything mentioning them;
   - whole meetings: an interview or a 1:1 becomes one thread of 100+
     items whose business content never reaches the business threads.
7. **Sensitivity isn't recognised in the text:**
   - named individuals' performance, improvement-plan and termination
     details sit in a programme thread beside leadership items;
   - candidate assessments, health remarks, a harassment account and pay
     figures carry no label;
   - only the meeting's level (confidential by default) protects them.

**What works:** updates within one meeting (answers, corrections,
duplicates), joining a specifically named subject across rooms (a
programme, a resignation, a search across 9–27 meetings), and speaker
attribution in leadership meetings.

## 2026-10-17: Fixing the memory: threads t4, statement labels

Scored on the ELT-year audit findings: whether the audited missed updates
and stale items are now retired, and whether the audited wrong retirements
are now current. Statement ids don't change when a dataset is rethreaded
(`tools/link-bench/memory_eval.py`), and the benchmark reference guards
against retiring everything. All runs: hosted GLM-5.3-Flash, `threads2.py`
with switches, one run each (runs vary by 5–10 points).

| Run | Threads | Current | Missed fixed | Stale fixed | Wrong fixed | Live items wrongly retired | Retiring updates retired | Grouping P/R | $ |
|---|---|---|---|---|---|---|---|---|---|
| Baseline (t3 lean) | 353 | 86% | 0/71 | 0/108 | 1/45 | 1.0% | 30% | 0.92/0.36 | 0.56 |
| R1 reconcile fixes | 352 | 86% | 15 | 16 | 25 | 1.0% | 30% | 0.93/0.44 | 0.57 |
| R2 + cross-thread | 342 | 85% | 18 | 21 | 29 | 1.0% | 28% | 0.91/0.39 | 0.72 |
| R3 + assign wording + merge | 292 | 84% | 19 | 16 | 26 | 0.5% | 31% | 0.93/0.54 | 0.78 |
| R3 with a 60-day lapse (applied after) | 292 | 78% | 25 | 31 | 26 | 0.5% | 31% | 0.93/0.54 | – |
| R4 + consolidation + lapse | 258 | **68%** | **44** | **68** | 22 | 2.0% | 33% | 0.90/0.52 | 0.81 |
| R5 = R4, retirement needs a replacing item | 284 | 69% | 43 | 61 | 20 | 3.0% | 26% | 0.90/0.54 | 1.15 |

What each part does:
- **Reconcile fixes:**
  - the history reconcile sees is the current items nearest the new ones
    plus the latest five, not the latest 25;
  - its prompt says what replaces what;
  - an item can't be retired by an older one;
  - "completes" also takes positions and proposals.

  These fix most wrong retirements (25 of 45), but they barely move the
  share of items current: reconcile only judges what the new items do.
- **Cross-thread context** (related current items of other threads, which
  reconcile may retire): a few more fixes.
- **Assign wording and merge:**
  - assign prefers continuing a subject; a person or a meeting is not a
    subject;
  - candidates show size, dates and latest items;
  - a cheap call merges same-subject threads born in an item.

  Together they fix fragmentation: grouping recall goes from 0.36 back to
  0.54 (the 4-meeting level) at the same precision, and 353 threads become
  292.
- **Consolidation** (each touched thread's current list reviewed once) does
  most of the work on staleness: 62% of audited missed updates and 63% of
  stale items fixed. It costs about 1 point of live items wrongly retired.
  - Retiring "past" items without a replacing item retired valid positions
    and framings, so a replacing item is now required.
  - The extra false retirements came from replacements inside one
    meeting's walk-through, which reconcile's rules forbid; R6 applies the
    same rules.
- **Lapse** (open commitments and questions untouched for 60 days) is
  free. On its own it fixes 31 of 108 stale items with no false
  retirements.

**Statement labels** (local 27B, one forward pass per statement, 1 h 50 m
for 12,328 statements):
- 18% of September's statements carry a sensitivity label;
- 73% of the items the auditors flagged sensitive are labelled (248 of
  338);
- 88% of those that sat beside business items from other meetings are
  labelled (60 of 68).

**R6** (R5 with reconcile's relation rules applied to consolidation): 287
threads, 71% current, missed 40/71, stale 60/108, wrong 22/45, live items
wrongly retired 2.0%, retiring updates retired 32%, grouping 0.93/0.51,
$0.98. Within noise of R4; kept as the configuration, since every
retirement then names the later item and passes the same rules.

**Re-audit of R6** (same rubric, 46 threads including the same 12
fragmented subjects):

| | t3 (first audit) | t4 (R6) |
|---|---|---|
| Items current | 86% | 73% |
| Missed updates | 59 | 37 |
| Stale current | 84 | 65 |
| Fragmented subjects: mixed / sensible / one thread | 11 / 1 / 0 | 6 / 4 / 2 |
| Wrong retirements | 40 | **78** |
| Misplaced items | 70 | 91 |
| Card quality | 3.1 | 3.1 |

Of the 78 wrong retirements:
- 54 came from one rule: when reconcile calls a new item the "same" as an
  earlier one, the code dropped the new item (no relation recorded), so
  the newest statement vanished;
- 13 are lapsed commitments and questions;
- 6 were retired by an item in another thread.

Fix: keep the newer wording and retire the earlier one (`SAME_LATER`; in
Go, with `audit_fixes`). R7 measures it.

Still open, from the re-audit:
- year-long catch-all threads (a pipeline, a retention, a strategy
  thread) keep absorbing items;
- updates still don't cross between split halves of a subject;
- near-duplicates pad the cards;
- debate threads keep every position current.

**R7** (R6 with the newer duplicate kept): 275 threads, 73% current,
missed 51/71, stale 69/108, wrong 35/45, live items wrongly retired 1.0%,
retiring updates retired 33%, grouping 0.91/0.50, $1.14. On the re-audit's
own findings for R6, it makes 50 of its 83 wrong retirements current again,
retires 19 of its 43 missed updates, and retires 23 of its 87 stale items.
This is the configuration; the Go t4 threader matches it.

**Final audit of R7** (same rubric, explaining the new retirement kinds:
a later repeat shown instead, a 60-day lapse, a retirement by another
thread's item; 41 threads):

| Per 100 audited items | t3 | t4 R6 | t4 R7 |
|---|---|---|---|
| Missed updates | 3.3 | 1.7 | **0.5** |
| Stale current | 4.6 | 2.9 | **1.3** |
| Wrong retirements | 2.2 | 3.5 | **0.5** |
| Misplaced | 3.9 | 4.1 | 4.5 |

| | t3 | t4 R6 | t4 R7 |
|---|---|---|---|
| Card quality | 3.1 (eleven 2s, fifteen 4s) | 3.1 | **3.3** (six 2s, nineteen 4s) |
| Fragmented subjects: mixed / sensible / one thread | 11 / 1 / 0 | 6 / 4 / 2 | 7 / 2 / 3 |

Left for the next round, all grouping:
- **catch-all threads:** a year of pipeline in one 354-item thread, an HR
  catch-all, a "strategy" thread;
- **single-meeting fragments** that should have joined their parent;
- **cross-month updates in different words** (a later rule set that doesn't
  retire the old amounts);
- **the same item current in one thread and retired in another.**

The next lever is a split pass for threads over a size or span bound; the
merge pass only looks at threads born in the item.

**R7 on September** (145 meetings of every kind; recent threads by 21
days):
- 655 threads became 466; items current 87% → 82% (the 60-day lapse can't
  act within one month);
- on the September audit's findings: missed updates fixed 9/104 → 59/104,
  stale 1/130 → 71/130, wrong retirements fixed 6/54 → 36/54;
- grouping on the two benchmark meetings fully threaded: 0.95/0.53 →
  0.93/0.58.

The run hit its spend cap on the evening of 29 September: about 13 of the
last meetings, including the 30 September benchmark meeting, went
unthreaded, so its benchmark carry-over isn't comparable. The full run
(below) replaces it.

Spend for the fixes:
- $9.63 on the hosted ledger (R1–R7, the September run, the cache
  checks);
- plus an estimated $0.25 for a stopped Go t4 check on GLM: the Go
  provider doesn't write bt.py's ledger.

## 2026-10-17 (later): September in full, t4 in Go, a split pass

**R7 on all of September.** The run started again from the start, at the
same settings, through the call cache. The replay drifted part way: a
call's key is its whole prompt, and a parallel retry that lands
differently changes every later prompt. So much of the run was paid for
again.

| September (144 meetings) | t3 | R7, full |
|---|---|---|
| Threads | 655 | 484 |
| Items current | 87% | 82% |
| Missed updates fixed | 9/104 | 62/104 |
| Stale fixed | 1/130 | 71/130 |
| Wrong retirements fixed | 6/54 | 32/54 |
| Live items wrongly retired | 0.0% | 1.0% |
| Retiring updates retired | 18.8% | 21.4% |
| Grouping (all benchmark meetings) | 0.74/0.50 | 0.72/0.49 |

The cost was $1.99 of new spend on the ledger (the run counts $4.59 with
cache hits), in 101 minutes. The sept-all-t4 dataset now holds this
run, with its sl2 labels kept.

**The Go t4 threader on ELT-year** (`knowledge --redo threads`, hosted
GLM-5.3-Flash, metered through the shared ledger):

| ELT-year | Python R7 | Go t4 |
|---|---|---|
| Threads | 275 | 188 |
| Items current | 73% | 73% |
| Missed updates fixed | 51/71 | 43/71 |
| Stale fixed | 69/108 | 63/108 |
| Wrong retirements fixed | 35/45 | 34/45 |
| Live items wrongly retired | 1.0% | 1.0% |
| Grouping | 0.91/0.50 | 0.90/0.56 |

The cost was $1.40, in 69 minutes. Go makes fewer, larger threads, with
better grouping recall. It retires 8 fewer of the audited missed updates.
`system_from_db.py` now writes threads, so `memory_eval.py` scores an
instance database directly.

**Threading speed.** About 1.1 minutes per hour of meeting content in
Python and 1.6 in Go (ELT-year: about 42 hours of content). Meetings run
one after another, since each depends on the state the last one left.
Within a meeting, calls are parallel (8). Each hosted call takes 10–30
seconds, mostly reasoning, and calls cut off at 8,000 tokens are retried.

**Split pass (R8 = R7 + `SPLIT=1`, threads2.py only).** After each
meeting, a touched thread of 60 or more items is checked, if it has grown
by half since its last check: one call divides it into its specific
subjects. Groups of fewer than three items are left in place. A thread of
more than 160 items is shown as a sample: an even spread plus its latest
80.

| ELT-year | R7 | R8 split |
|---|---|---|
| Threads | 275 | 353 |
| Largest threads | 354, 177, 140 | 143, 128, 104 |
| Items in threads of 60+ | 43% | 20% |
| Missed updates fixed | 51/71 | 47/71 |
| Stale fixed | 69/108 | 70/108 |
| Wrong retirements fixed | 35/45 | 37/45 |
| Live items wrongly retired | 1.0% | 1.5% |
| Grouping | 0.91/0.50 | 0.93/0.47 |

The cost was about $1.19 of new spend (the run counts $3.53 with cache
hits): calls after a split miss the cache.

Opus audited the six splits that made the most new threads (49 threads,
audit-r8/):
- 5 splits were better than one thread, 1 was worse (one subject cut by
  angle);
- 42 threads are one subject, 5 are themes, 2 mix several;
- 6 pairs were over-split;
- 99 items are in the wrong thread. Most were left in the parent but
  belong in a split-off thread. Many of these were never shown to the
  model, because of the 160-item sample.

Split is kept as an option, off by default. Next: show the whole thread
in chunks rather than a sample; a group must name a subject other than
the parent's (against cutting by angle). Then port it to Go.

Spend in this round:
- $4.58 on the ledger ($37.51 → $42.09; the cap is $47.50);
- of which $1.40 for the Go check and $3.18 for September and the split
  together: they share a tag prefix, and their split is estimated from
  the ledger growth while September ran alone.


## 2026-10-17 (evening): Thread statuses, Ask from threads, meeting prep

Ask used to search raw passages, take the top 8, and have the local model
summarise them. It never saw threads or what is current, so "where are we
on X" got whichever old passages matched the words best.

**What changed** (architecture.md, "Thread statuses", "Ask", "Prep"):
- each thread gets a written status, rewritten when its items change;
- Ask answers from the matching threads' statuses and current items plus
  passages;
- questions about a person go through the prep data;
- the topic page leads with the status;
- the Prep tab gathers what to bring to a meeting.

**Statuses** (GLM-5.3-Flash, hosted):
- ELT year: 275 threads, 2 minutes, $0.18;
- September: 519 statuses for 484 threads, 3½ minutes.

On reading, the statuses were specific (figures, owners, due dates,
overdue promises) and flagged conflicts between items.

**Question set** (private, testdata/private/ask-eval/):
- 28 questions on September, drafted by Opus from the transcripts: 10
  status, 5 change, 7 lookup, 4 prep, 2 the record can't answer;
- each has a reference answer as of 30 Sept and the facts an answer must
  give.

Answered with the local Qwen3.8-27B on llama-server, then graded blind by
Opus (tools/ask-eval/grade.md):

| | Passages only (before) | Threads + passages |
|---|---|---|
| Wins | 5 | 22 (1 tie) |
| Must facts | 32/105 | 63/105 |
| Current position | 13/28 | 24/28 |
| Wrong statements | 10 | 13 |
| Invented | 2 | 0 |
| Useful (0–3) | 1.54 | 2.46 |
| Says when the record has no answer | 2/2 | 2/2 |

By type (must facts, before → after):

| Type | Before | After |
|---|---|---|
| Status | 11/40 | 24/40 |
| Change | 3/21 | 15/21 |
| Lookup | 12/22 | 16/22 |
| Prep | 4/18 | 4/18 |

Prep questions ("what's open with X?") now go through the prep data.
That moved them from 5/18 to 8/18 must facts on a second grading, winning
3 of 4. They are still the weakest type: what is open in a 1:1 sits in
threads the prep ranking doesn't always reach.

Of the wrong statements, two came from a status leading with an earlier
recommendation (a migration recommendation, a hiring shortlist).

**Speed.** With the local 27B, an answer takes about a minute; prep-style
answers take about two (10–15k tokens of context). Two questions at a
time is fine; eight at once timed out at the earlier 2-minute limit, now
5 minutes.

**Sharing suggestions** (prep).
- The first version suggested sharing an item from the reduction plan
  with VPs: the labeller hadn't marked that line.
- The fix leaves out any thread where 15% or more of the items are
  labelled. On September:
  - people subjects (the reduction plan, a resignation, the SRE
    reorganisation) ran 38–49% labelled;
  - most work subjects ran 4–6%;
  - a deal and a team build-out ran about 25%, and are left out.

**Also fixed.** The app crashed opening September: a bare "'s" in a
statement made an empty word, and indexing it panicked on a background
goroutine. The app couldn't start, as it reopens the last dataset. The
live index now turns a panic into an error.

**The combined dataset ("all-t4").**
- Contents: the ELT year (24 meetings, January to 7 October) and all of
  September (145), 165 meetings in all with the shared September ELT
  meetings counted once; 16,895 statements.
- Labels: sl2, on the local 27B, in 2 h 11 min.
- Threading: rethreaded in time order by the Go t4 threader on
  GLM-5.3-Flash, in 4 h 12 min for $4.41. The result is 546 threads and
  573 statuses.
- On the benchmark meetings it holds: grouping 0.90/0.52, live items
  wrongly retired 1.5%, updates retired 31%. The separate sets scored
  0.91/0.50 (ELT) and 0.72/0.49 (September).

Threading follows time, so adding meetings older than those already
threaded needs `--redo threads`; `musubi knowledge` now says so. A
backfill should be extracted in full and threaded once, or brought in
oldest first.

## 2026-10-19: Ask on the combined dataset, and how noisy the question set is

The 28 September questions were answered on all-t4 (the Go threader
over the ELT year and September) and graded blind against the
September-only answers (Python R7 threads), with credit for correct
October news. A second September run was graded against the first, to
measure noise.

| | Sept r1 | Sept r2 | All |
|---|---|---|---|
| Must facts | 65–67/105 | 64/105 | 58/105 |
| Wrong | 6–8 | 6 | 10 |
| Current | 25/28 | 26/28 | 24/28 |
| Useful | 2.1–2.2 | 2.11 | 2.04 |
| Wins | r1 10, r2 8, tie 10 | | Sept 18, All 8, tie 2 |

Noise:
- the same system against itself splits wins about evenly and moves by
  1–2 must facts;
- grading the same answers twice moved them by 2.

So All's gap is real. It is in change questions (16 → 12 of 21) and
person questions (8 → 4 of 18); status and lookup are even.

From the lost questions, the causes are:
- **Broader Go threads:** the SRE reorganisation joined the thread about
  its leader's departure, so "where does the SRE reorg stand" got the
  departure.
- **A longer history competing in the person ranking:** a year of ELT
  subjects outranks the 1:1 ones.

Next:
- close the Go threader's gap to R7 on the same data;
- weight recency and two-person meetings in prep's ranking.

Ask's answer cap rose from 1,200 to 2,000 tokens: a long person answer
was cut off.

**Prep ranking.** Two changes:
- meetings are weighted by size, min(3, 6/people), so a 1:1 counts three
  times a six-person meeting and an ELT meeting under half;
- items that mention the person count, and a thread named after them
  counts double.

On All, the person questions went from 4 to 6 of 18 must facts, winning
3 of 4 (September had 8).

**Go threader against R7 on the ELT year** (same statements):

| | Go t4 | Python R7 |
|---|---|---|
| Threads | 188 | 275 |
| Items in threads of 60+ | 60% | 43% |
| Threads (10+ items) merging 3+ of the other's subjects | 22% | 13% |
| Statements in no thread | 10.3% | 2.9% |

The same gap shows elsewhere:
- All (Go) leaves 8.5% of statements in no thread; September (Python)
  leaves 4.8%.
- The loss is uneven. Most meetings lose about 1%; a few lose 40–50%
  (one ELT meeting 80 of 161), decisions and commitments among them.
- Neither run logged an error. The retry rule is the same as Python's.

A likely cause: groups whose thread reference Go can't resolve (a name,
or "T12: name") were counted as placed, so no retry asked for them, then
dropped. Go now:
- resolves names and those forms;
- asks again for items in unresolved groups;
- logs each unresolved reference, and how many statements no thread took.

The next hosted run will confirm or rule this out.

**Ask: status first, and a smaller context.**
- The top subject's written status now shows as soon as it's found (well
  under a second), and the answer streams below it.
- A smaller context, on September with the local 27B, two questions at a
  time: 8 items per thread instead of 12, 4 passages instead of 6, 2
  citations per status point instead of 3, and 3 changes and 4 open points
  instead of 4 and 6.

| | Full | Small |
|---|---|---|
| Median answer time | 83 s | 56 s |
| Must facts | 66/105 | 67/105 |
| Wrong | 6 | 8 |
| Useful | 2.29 | 2.21 |
| Wins | 13 | 7 (8 ties) |

The facts are the same, but the small context loses a little more often
than noise does (noise: 10–8). It stays off by default
(`ask.small_context`).

**Interviews by title (sensitivity rules r6).** Of the 338 items the
September audit called sensitive, the labeller marked 73%. Of the 90 it
missed:

| Meeting | Missed |
|---|---|
| Candidate interviews | 49 |
| 1:1s | 36 |
| Other | 5 |

An interview's statements ("his company chose LaunchDarkly") read as
business facts one at a time. A title rule now marks a meeting HR when
its title names an interview, candidate, hiring panel or reference check
(not a customer or market interview). A board "debrief" was a false hit
and is left out. On All:
- 6 meetings are marked, all interviews;
- 297 of 338 audited items are labelled (88%), all 97 interview items
  among them.

The rest are mostly 1:1s. Those are already confidential, and prep never
suggests sensitive subjects. Statuses now take their items' raised labels
without a rewrite.

**Hosted threading speed and the token cap.** On four ELT meetings:

| Variant | Time |
|---|---|
| A: 8 parallel, no backup requests, warm-up on | failed on meeting 2 |
| B: 16 parallel, backup at 25 s, no warm-up | 21 min |
| C: 8 parallel, backup at 25 s, no warm-up | 20 min |

A failed because a slow but steady reply was cut at the 3-minute limit
three times. Two meetings took 6–10 minutes in B and C, waiting on a few
long calls. The parallel and backup settings hardly mattered.

The long calls were reasoning cut off at the 8k-token cap and retried at
16k. Across 5,771 successful threading calls on GLM-5.3-Flash:
- reasoning was 532 tokens at the median, 3.2k at p90, 7.2k at p99,
  12.7k at p99.9 and 14.9k at most;
- of 42 calls cut at 8k and given 16k, 33 succeeded (10–15k tokens of
  reasoning); 9 hit 16k again.

The cap doesn't change how long the model reasons; it only cuts it off.
It costs nothing unless used, and a cut call is billed in full. So the
cap now starts at 16k (it was 8k), and the time limit grows with it.

A call cut at 16k is past everything that has converged, so it isn't
doubled; there's no evidence more room helps. It is asked once more at
16k with low reasoning effort, the setting that actually shortens
reasoning. If that is cut too, the call is given up:
- an assign leaves its items to the retries;
- a reconcile leaves that thread's new items current;
- the run goes on; before, one such call aborted it.

Backup requests are off. A call that reasons for minutes gains nothing
from a second start 25 seconds later, and the losers' tokens are billed:
backups took thrown-away spend from 9–11% to 18–24%. Thrown-away spend
across all Go runs so far: $0.83 of $7.85.

## 2026-10-19 (later): Toward local updates: derived current, insert, forget, correct; aliases; Live

**Derived "current"** (`knowledge.Derivation`, `musubi threads-check`).
A statement in a thread is current as of a time unless:
- a statement said then or later (and by that time) replaces, completes,
  answers or repeats it; or
- it is an open commitment or question more than 60 days older.

| Data | Derived and stored agree | Gap |
|---|---|---|
| All, Go threader as first run | 91.3% | 1,345 retirements without a recorded reason (mostly repeats) |
| ELT year, run with every reason recorded | 98.8% | 13 explained below; 49 by semantics |

The 13: statements sharing a claim, whose claim-level relation had
nowhere to go. Relations are now stored between statements too
(`statement_relations`).

The 49: an item retired in one thread but current in another. An
outdated item is now retired in every thread it is in, as the derivation
has it. Simulated offline on that run, this retires 49 more memberships,
none of them audited items, and the audit scores don't move.

Every membership and relation now records:
- the rules version;
- the item being threaded;
- the processing time;
- why an item stopped being current.

**Local operations**, each tested offline against derived "current":

| Operation | What it does | Test |
|---|---|---|
| Insert | places an item's statements against today's threads, whenever it happened; judges them against their nearest neighbours there, earlier and later; derives current | an older meeting inserted after a newer one: its question, answered later, isn't current |
| Forget | the item and everything from it go; current is derived again where it was; empty threads go | the answer forgotten, the question is current again |
| Correct | text or speaker changed and recorded; re-embedded; relations judged again; current derived | the corrected statement no longer answers, so the question is current again |

`musubi insert-check` takes items out of a threaded copy and inserts them
again, to compare with the replay. It will run on two meetings.

**Consolidation window.** On the ELT year with window 24, compared with
the first Go run:

| | First Go run | Window 24 |
|---|---|---|
| Time | 69 min | 57 min |
| Statements in no thread | 10% | 2% |
| Stale items fixed | 63/108 | 53/108 |
| Missed updates fixed | 43/71 | 36/71 |

Stale pairs far from anything new were never seen again. A thread is now
reviewed whole each time its current set grows by half (at 10, 15, 22,
33… items). That run is checking it.

**Aliases** (`docs/incremental-design.md`, "Entities and aliases").
`tools/entity-bench/aliases.py` proposes candidates from spelling,
frequency, the invite form, and two vetoes for two people. Of 174 pairs:
- variants rank high;
- 14 are vetoed as two people;
- ordinary words are dropped.

What failed:
- **Context similarity** hardly separates anything within one company's
  notes.
- **Co-reference with attendees** rarely applies: most variants come up
  in talk about absent people.

So the owner decides, helped by the impact preview. In practice:
- one first-name pair would have tied 17 threads together; its sample
  showed a well-known public figure with a similar name;
- another's sample showed one name was a customer's CMO, not the
  colleague.

Confirmed spellings apply only when reading: Ask, Prep and Live lines.
Nothing is rewritten. The review screen is Explore → Corrections.

**Speaker attribution** (`tools/entity-bench/speakers.py`). Six meetings
have one voice: one label has 90% or more of the passages in a meeting
of two or more. Five are recent 1:1s with no label for the owner at all,
so the owner's words are credited to the other person. It's a speaker
separation problem upstream (Tomoe) and can't be fixed from the text.
Unknown labels (14) are guests presenting at ELT, which is expected.

**Live.** A lookup that matches a thread at 0.80 or above shows its
written status at once, with no model call; the 8B summary is skipped.
Calibration on All:
- specific lookups (a programme, a vendor pilot, the board deck) matched
  their thread at 0.81–0.86;
- vague ones matched a loosely related thread at 0.77–0.78;
- the 28 questions' top matches ran 0.71–0.93, and questions with no
  answer still matched a nearby thread at 0.76–0.85. A score alone can't
  say a subject is absent.

Topic cards lead with the status's first sentence. A replay shows only
statuses written by then.

## All rethread under the current rules (2026-10-19)

One full rethread of All (165 meetings, 16,895 statements) with
everything since the first All run on: statement-level relations,
retiring everywhere, the re-ask for unresolved thread references,
consolidation window 24 with full reviews at 1.5× growth, and the 16k
cap with one low-effort retry.

| Measure | Before | After |
|---|---|---|
| Statements in no thread | 8% | 3% |
| Derived "current" agrees with stored | 91% | 99% |
| Retired with no relation explaining it | 1,424 | 155 |
| ELT year audit: missed updates fixed | 45/71 | 47/71 |
| ELT year audit: stale fixed | 68/108 | 68/108 |
| ELT year audit: wrong retirements fixed | 31/45 | 34/45 |
| September audit: missed updates fixed | 53/94 | 57/94 |
| September audit: stale fixed | 62/122 | 64/122 |
| September audit: wrong retirements fixed | 28/52 | 35/52 |

Current stayed at 77%.

Cost and time: 6h4m wall clock, $4.14 over 3,838 calls. By kind:

| Kind | Calls | $ | Call time |
|---|---|---|---|
| reconcile | 1,592 | 1.51 | 389 min |
| consolidate | 1,133 | 1.44 | 396 min |
| assign | 334 | 0.63 | 59 min |
| status | 637 | 0.50 | 96 min |
| merge | 142 | 0.07 | 18 min |

The run took 1.5× the first All run (247 min), from the re-asks and
the full reviews. One consolidation (one thread) timed out three times
and was skipped. Reconcile and consolidate are the time cost, so a
narrower reconcile is the speed lever.

99% agreement means the stored relations explain "current", which local
insertion and forgetting rely on. `insert_older` is now on for All:
meetings older than the latest threaded go through local insertion
instead of a rethread.

Ask on the rethreaded All (28 questions, local 27B, two blind Opus
graders) against the earlier All answers: 14 wins to 13, one tie. Must
facts 65/105 against 67/105, wrong 9 each, invented 0 against 2, current
23/28 against 25/28, useful 1.89 against 2.11. That is within the noise
of the earlier self-comparison (10–8). By type, lookups gained (16 → 18
of 22) and change questions lost (13 → 10 of 21); two of the losses
lead with an older position. The rethread fixed the graph without
changing Ask answers much; better change answers would come from the
statuses, not the threading.

## 2026-10-20: Prep, Ask retrieval and the slow tail

**Prep: open items in time.** A group prep showed commitments three weeks
old ("tell them later today") as open, because nothing later in the
thread closed them. Open items now carry the date they were last raised;
those with nothing said for 21 days (`prep.quiet_days`) are listed after
the live ones as gone quiet, and a due date that has passed is marked
overdue. No model.

**Prep: for the owner, not the room.** In a prep for a meeting of six,
the status lines of personnel subjects (a resignation, pay, one
attendee's friction with another) sat at the top. The labels already
tell those subjects apart: on that prep's eight subjects, the six about
people ran 33–63% sensitive items, the two about ordinary work 0% and
6%. Subjects at or over the sharing rule's 15% are now marked for the
owner alone and, with more than one other person in the meeting, listed
last (`prep.mark_private`). No model.

**Ask: a subject's latest turn in another thread.** Of the questions the
rethreaded answers lost, the two that missed the latest developments
(a role question; a programme question) had them in a sibling thread
created later (the role's comp talks; a strategy thread). Ask reads the
threads within 0.08 of the best match; the comp thread scored 0.088
below. With `ask.find_newer`, one more thread within 0.12 is read when
its latest item is later than every thread found. Measured without a
model, as the share of each question's reference source passages that
sit in the threads read (28 questions, 383 passages):

| | Passages in threads read |
|---|---|
| Before | 249 |
| Name words weighted by rarity, short names counted | 242 |
| One newer thread within 0.12 | 263 |
| Both | 263 |

The role question went from 9 to 15 of 17. Rarity weighting was
dropped.

Answers didn't follow. The 17 questions whose threads changed, rerun on
the local 27B and graded blind by two Opus graders: before won 8 and 7,
the rule 5 and 5 (4 and 5 ties); must facts 33 → 31 and 34 → 35 of 66;
wrong statements 6 → 8 and 8 → 9. On the role question the comp thread
was read but given one line, last, and its raise stated as done. The
model leans on the threads it is given first; `ask.find_newer` is off by
default. A later-active thread may need leading with, as the latest
turn, rather than adding.

**The slow tail is stalled streams.** In the All rethread (6h4m), the
three slowest meetings took 31–43 minutes each, a third of the run, and
the ten slowest 42%. 136 reconcile and consolidate calls ran over a
minute, some to the 400-second limit. They weren't long replies: calls
over two minutes had a median of 632 output tokens at about 4 tokens a
second, against 127 a second for the rest; 7 of 77 were cut off at the
token cap. Calls now get a backup request when, 30 seconds in, they have
streamed fewer than 20 tokens a second (`llm.stall_after`,
`llm.stall_rate`), the first reply used. A steadily streaming long
reply is left alone, so this costs about one extra call in fifty and
doesn't change any reply. Its effect on wall clock comes with the next
hosted run.

**Owner edits survive upgrades.** A schema upgrade drops every table
not on the store's kept list, and the kept list had only the forget
list, the audit log and judgments: the owner's statement corrections and
decided aliases would have gone with the derived tables. They are kept
now, with the new thread edits; corrections and edits name statements by
item and extracted text, so they find them again after a rebuild.

**A typed classifier (TypeSafe's Jev) for labels and relations.** One
call carries several typed questions (choice, score, yes/no) about one
input and returns distributions; about 0.1–0.3 s a call, input metered
at $0.042 per million tokens. `provider.Classifier` is that shape, with
Jev and an emulation over the one-token decider, so nothing depends on
the vendor.

*Statement labels*, on the seeded sample of 200 statements the
September auditors flagged sensitive and 200 they didn't. The auditors'
flags were checked by a blind Opus judge on every statement any model
disagreed on (190): it overturned 61 of the first 138, mostly
statements the auditors hadn't flagged (a resignation, candidates for a
role, who replaces whom). Against that truth (207 sensitive of 400):

| | Recall | Precision | Missed | False flags | 400 statements |
|---|---|---|---|---|---|
| 27B, one-token decision (sl2) | 0.71 | 0.91 | 59 | 15 | ~3½ min |
| Jev, the same rubric, statement only | 0.90 | 0.79 | 21 | 50 | 17–28 s, $0.01 |
| Jev v2, ≥ 0.5 | 0.86 | 0.91 | 30 | 18 | 17 s, $0.01 |
| Jev v2, ≥ 0.3 | 0.91 | 0.86 | 18 | 32 | |
| 27B or Jev v2 ≥ 0.5 (labels only add) | 0.89 | 0.87 | 23 | 28 | |

v2 gives the meeting's title (a candidate's background reads as ordinary
work unless the meeting is known to be an interview) and splits "none"
into work, a role being hired for, and routine mentions of people (the
one "none" drew false flags to roles, hiring processes and praise of
teams). It halves the 27B's misses at the same precision.
`knowledge.label.classifier` (off by default) labels new statements with
it; statements labelled earlier keep their labels (labels only add, so
relabelling would give the union of two labellers, which nobody would
run going forward). On All, Jev labels from the October meetings on; the
earlier corpus keeps its 27B labels.

To do, later: relabel the whole corpus with the labeller in use
(`musubi knowledge --redo label`, which replaces rather than adds once a
reset of labeller labels exists) and check filtering end to end, so the
corpus is on one labeller and its quality is measured as a whole. GLM-5.3-Flash on Baseten returns no log-probabilities and
ignores "thinking off", so the one-token decider can't run there.

*Relations*, on the linking benchmark's 227 reference pairs, one call per
pair (both items, the reference's relation set as options):

| | False retire | Missed retire | Related pairs connected |
|---|---|---|---|
| Jev, retire ≥ 0.5 | 20.2% | 0.0% | 91% |
| Jev, retire ≥ 0.9 | 6.1% | 20.7% | 91% |
| GLM threads (current) | 0.5% | 41.4% | 84% |
| 8B pairwise (k16) | 22.2% | 20.7% | 96% |

Pairwise, Jev is the 8B's shape at any threshold: it finds the
retirements threads miss but hides live items far too often to decide
alone (the worst failure). It doesn't replace reconcile. It may serve as
a hint: a pair it retires with high confidence that a thread kept
current is a candidate for the consolidation review, which applies the
rules. Not tried.

**Is retirement a classification problem? Slots, measured.** The idea:
each subject has slots (one quantity for one period, one decision, one
question, one deal's status); a fast classifier files each statement in
a slot with its role (states where it stands, adds detail, asks,
answers, commits, reports done), and a rule derives "current": within a
slot, a later latest word retires what came before. Upper bound first:
GLM defined each reference subject's slots having seen all its
statements (15 calls, 298 slots, about 2.8 statements a slot); Jev filed
the 833 members (28 s). Then rule variants on the same filing, no more
calls. Against the linking benchmark ($0.11 in all):

| | Retiring updates retired | Reference-current items retired | Pairs: false retire |
|---|---|---|---|
| GLM threads (current) | 32% | 1.3% | 0.5% |
| Slots, Jev's role | 43% | 44% | 24% |
| Slots, same extracted kind | 13% | 24% | 20% |
| Slots, same kind and Jev's "latest" | 7% | 21% | 14% |
| Slots, decisions and metrics, later meetings only | 2% | 0% | 0.5% |

No rule separates the two: loose ones retire live items by the score,
tight ones retire nothing. Jev called 65% of statements "where it stands
now"; a slot mixes facts and positions that both stay current; and many
retirements happen within one meeting (an answer minutes after the
question). Whether an item is still the latest word turns on what the
later one says about it, which a statement read alone doesn't carry,
even filed in the right slot with hindsight. Retirement stays a
whole-thread judgment; the classifier's place is around it (labels,
routing, triage), not in it.

**Reconcile and consolidate as one call** (`knowledge.threads.merged`,
threads version t4m, off by default). They ask one question, split by
what each may see: reconcile, the current items nearest the new ones
(new against old); consolidate, the whole current list (old against
old). Merged, reconcile is shown the whole list (up to 80) and also
lists what a later current item replaced; consolidation is skipped.
Four ELT meetings, both from the same base, threaded in parallel
(scored from statement_relations; the base's old linker relations in
claim_relations skew pair scores for databases built from it):

| | Two calls (t4) | One call (t4m) |
|---|---|---|
| Retiring updates retired | 33.0% (37/112) | 30.4% (34/112) |
| Reference-current items retired | 3.3% (5/151) | 1.3% (2/151) |
| Pairs: false / missed retire | 1.0% / 31.0% | 1.5% / 34.5% |
| Calls, call-minutes, cost | 140, 27.1, $0.133 | 108, 21.5, $0.109 |
| Wall clock, meetings 1–3 | 7m39s | 6m38s |

Quality is within run-to-run noise (assign alone gave 16 and 26 new
threads in the first meeting). Merged retires a little less, rightly
and wrongly; "supersedes" fell from 27 to 12: the review of old items
among themselves, a secondary instruction in one call, is done less.
The saving is about a fifth, partly spent on longer calls. Over months
that review is what keeps staleness down, which four meetings can't
show; only the ELT year's audit can. Parked.

Both runs hit Baseten's rate limit together (429): hosted calls now
wait it out (10 s doubling to 80 s, about 12 minutes in all) without
using up a try.

**Retirement by population: small chains.** Of the benchmark's 100
retiring updates (in its subjects), 59 happen within one meeting, a
median of one statement apart (mostly an answer straight after the
question); 41 cross meetings (supersedes 17, completes 15, answers 9),
and those are not close in wording (the 5 nearest by meaning hold the
retired item for 62% of all updates). Per statement, a chain: the
previous 5 statements of its meeting and the 10 nearest earlier
statements of its subject from earlier meetings (the reference's own
subjects: an upper bound on candidates; the chain holds the retired item
for 71 of 92). One question per chain: which does it answer, settle or
replace?

| 4 meetings | Retiring updates retired | Reference-current retired | Pairs false retire | Cross-meeting found |
|---|---|---|---|---|
| t4 threads (reconcile + consolidate) | 33.0% | 3.3% | 1.0% | 6 / 41 |
| Jev on chains, ≥ 0.9 | 11.6% | 3.3% | 1.5% | |
| GLM on chains, every relation | 48.2% | 10.6% | 8.6% | 16 / 41 |
| GLM chains: answers in-meeting, all across meetings | 39.3% | 3.3% | 4.0% | 16 / 41 |
| t4 + GLM chains across meetings | 44.6% | 3.3% | 2.5% | 16 / 41 |

GLM's chain "supersedes" was the noise (122 made, 12 matching the
reference, 79 within one meeting: a walk-through read as replacement);
its "answers" were precise (61 made, 34 matching, 52 within a meeting).
Threads catch few of the cross-meeting retirements, which is the
staleness the audits keep finding; chains find nearly three times as
many. Chain calls are small (2 s median, 118 reasoning tokens, against
535 and 1,280 for reconcile and consolidate): 964 of them, $0.25, 7m43s
for 4 meetings, against $0.13 for t4. Next: candidates from the
system's own threads instead of the reference's, then the ELT year.

**What bounds threading wall clock.** Four ELT meetings, one change at a
time from the same base, single runs:

| | Wall clock | Note |
|---|---|---|
| parallel 8 (default) | 7m50s | |
| parallel 16 | 8m35s | queueing isn't the bound |
| hedge_after 30 s | 7m42s | 12 calls hedged; the backups took as long (48–63 s) |
| consolidate_every 2 | call time −33%, quality within noise | young threads: most reviews are growth-triggered and still run |

A meeting's critical path is assign (chunks in turn: 54 s for 254
statements) → merge → the slowest reconcile → the slowest consolidate;
on the fourth meeting 54 + 15 + 83 + 99 s of its 4m11s. The slowest calls
are the biggest threads' prompts, reasoning 6–7k tokens; a fresh sample
of the same prompt is as slow, so hedging doesn't help, and they stream
normally, so the stall rule doesn't either. What does: work off the
critical path (labels on a classifier; rewind and append, not insert;
consolidation overlapped with the next meeting).

`knowledge.threads.rewind` (default 3): when new items are older than at
most that many threaded items, those are unthreaded (no model) and
threaded again after them, so the batch appends in time order.
`knowledge.threads.consolidate_every` (off): review a thread every N
touches, counted in the store, plus the growth-triggered full reviews;
to be judged on the ELT year, where threads are mature.

**Overlapped consolidation** (`knowledge.threads.overlap`, now default
on). Each meeting's review runs while the next meeting is placed and
reconciled, waiting per thread. Four ELT meetings, with phase times
logged: fronts summed 7m44s, reviews summed 3m44s (11m28s serial);
actual 8m40s, every review but the last hidden. Over a long batch the
saving approaches consolidation's share of wall clock, a quarter to a
third. Quality within noise (retiring updates retired 39.3%,
reference-current retired 2.0%, against 33.0% and 3.3% for the serial
run; derived and stored agree on 97.0% against 97.0–97.7% across the
other four-meeting runs). The race detector found one race, older than
this: reconcile's workers read other threads for cross context while
finished threads were written back; that context is now taken before
the workers start.

**Ask on GLM-5.3-Flash (hosted) instead of the local 27B.** Same 28
questions, same data and retrieval (the copy from before October), only
the answering model changed; two blind Opus graders, all 28 each:

| | Grader 1: 27B → GLM | Grader 2: 27B → GLM |
|---|---|---|
| Wins (27B / GLM / tie) | 4 / 17 / 7 | 5 / 18 / 5 |
| Must facts (of 105) | 59 → 67 | 63 → 71 |
| Wrong | 7 → 5 | 7 → 4 |
| Useful (0–3) | 2.07 → 2.39 | 1.89 → 2.21 |
| Prep questions, must facts (of 18) | 8 → 11 | 8 → 11 |

Answers took 3 s median (10 s at most) against 170 s for the 27B (four
at a time), and $0.027 for the 28. `[ask.fallback]` names a model that
answers when the hosted one fails (checked with the hosted address
broken: the local 27B answered in 63 s). On All, Ask and talking points
are hosted with the 27B as fallback; the code's defaults stay local.

**Thread fragmentation, sized (no model calls).** The audit's worry was
that a subject split over two threads leaves updates unlinked, so stale
items stay current. Against the benchmark's gold labels, mapped onto All
by meeting and text, 29 labelled retirements had both statements
threaded. 28 sit in one thread: 18 linked, 4 unlinked with the earlier
item still current, 2 cross-meeting unlinked, and 4 unlinked but retired
by another relation. One is split across threads (same meeting). The
missed retirements are mostly inside one thread, a reconcile miss, not
a fragmentation one. Against the 15 reference subjects (4+ statements),
All needs 1 thread to hold 80% of 6 subjects, 2 for 4, 3 for 3, and 4–6
for 3. The broad ones split into their programmes, which is mostly
sensible granularity: the reference subjects are coarser than threads
should be. Across All's own relations, 15% of cross-item relations join
statements now in different threads (they were linked, then one side
moved), and 240 statements are in more than one thread. Not worth a
cross-thread linking pass now; the reconcile misses inside threads are
the larger share.

**Ask: the later thread first.** Following up `ask.find_newer`, which
added the later-active thread last and saw the model lean on the others:
`ask.newer_first` reads it first, headed as the subject's latest turn.
All, Ask on GLM-5.3-Flash, the 18 questions find_newer changes; the 15
whose threads differed graded blind by two Opus graders, who agreed on
every question:

| | Before (newer off) | Newer first |
|---|---|---|
| Wins (grader 1 / grader 2) | 5 / 5 | 4 / 4 (6 ties each) |
| Must facts (of 58) | 34 / 36 | 31 / 32 |
| Wrong | 3 / 3 | 1 / 1 |
| Current | 13 / 13 | 13 / 14 |
| Useful (0–3) | 2.13 / 2.33 | 2.07 / 2.20 |

Fewer wrong statements, fewer facts: the thread found as "later" is
often a different subject that happens to be newer (a programme thread
for a departure question), and leading with it crowds out the right one.
Both options stay off; a later thread would have to be shown to be the
same subject first. $0.04.

**Statuses: no talk about the notes (ts2).** 175 of 690 statuses on All
had the writer's working in the reader's text ("Note: this item is
marked RETIRED but no later item…", "item 3796", "not recorded in the
items"), mostly in open items, which Prep shows. The prompt now says the
CURRENT/RETIRED marks and ids are for the writer only, that a retired
item that still looks open is decided without comment, and that one
open entry is one item. Rewritten on a copy (736 calls, 0 failed,
4m32s, $0.58):

| | ts1 | ts2 |
|---|---|---|
| Statuses with such talk | 175 | 5 |
| Open items | 2,688 (172 ch) | 2,682 (157 ch) |
| Open items packing several (2+ semicolons or 300+ ch) | 145 | 77 |
| Changed points | 1,295 | 1,283 |
| Now, mean length | 684 ch | 643 ch |

Same coverage, read side by side on samples; applied to All (the ts1
rows kept in the dataset folder).

**Live baseline before in-meeting hints work.** One 61-minute meeting
from today (391 lines), replayed through `musubi live --replay` at 1×
against All, with the shipped Live settings (qwen3:8b suggesting
continuously, two auto-briefs, fast lane, topic cards). No cost.

- Speed is fine: passages show a median 1.8 s after the phrase is said
  (66 ms median for the lookup itself; 2.4 s p90), and the brief's
  summary takes a further 2.0 s median (2.4 s p90).
- Volume is the problem: 353 distinct queries briefed in the hour (6 a
  minute), 253 with a summary kept, 40 summaries dropped as off-topic,
  60 thread statuses shown. One subject discussed for ten minutes
  (support escalations) was briefed 15+ times under slightly different
  queries, each a new brief.
- Small talk still triggers lookups: the opening travel chat produced
  12 queries, all empty, despite "skip small talk" in the prompt.
- Some matches miss: a status shown for a role-change thread on an
  escalations query; a venue-logistics passage for a mention of the
  conference.

So usable hints are a matter of fewer, steadier ones: one card per
thread, updated as the talk on it continues, rather than a brief per
query; a gate on whether the talk is about work; and a bar for showing
a thread's status.

## 2026-10-21: Corrections as rules

The corrections screen hid what a decision did: one button whose effect
depended on how many uses were checked (some: those statements; all: a
rule for every use, new meetings too), a proposal answered with a typed
form listed as the owner's rejection, and nothing editable once decided.
It now offers three rules that say what they do (docs/incremental-design.md,
"Rules, and what a rule changes"), a Rules tab where every rule and
statement edit can be opened, changed or undone, and a menu on selected
text (ask about it, correct its spelling, edit the statement, open or
copy the source). Of All's 15 pairs marked rejected, 5 were proposals
answered with a typed form; they are now replaced.

Rules now reach what is shown and written, not only matching. Checked on
a copy of All with the rule for one surname (15 statements, 5 threads):
before, the 5 threads' statuses named the variant 7 times and the kept
form 9; after the background refresh (re-embedding the 15, their threads'
statuses written again), 0 and 16. The first run found possessives never
matched a rule ("…'s team" was one word to the matcher, in Live and prep
too); fixed. The first refresh after upgrading rewrites every status a
confirmed rule touches (81 on All, $0.10, 81 s); later ones only their
own threads.

## 2026-10-21: Live, step 1 (focus) measured with a stepped replay

**Stepped replay.** The timed replay wasn't speed-independent: the
suggestion model runs back to back on the wall clock, and "looked up
recently", the card refresh and summary staleness were wall-clock timers,
so a faster replay looked things up differently. `musubi live --replay ID
--stepped` plays the meeting line by line on the meeting's clock, doing
each line's work before the next. Two runs of the first 5 minutes agreed
on the lookups and cards in order, differing only in the local model's
wording (2m51s and 2m42s). The whole 61-minute meeting took 40m56s and
looked up 359 queries an hour, against 353 for the real-time replay the
night before. One difference from live: every summary is written, where
live skips one queued over 25 s.

**Focus (`live.focus`).** Each card cut to its 3 lines closest to the
last 90 s, with the line that raised it; two cards open, the rest one
line each; two lookups open; old subjects marked. Measured on the same
replay, 20 moments 3 minutes apart, the old panel (all cards in full, the
last 8 lookups) against the focused one, every item graded blind by two
Opus graders per half (helpful now, given the last 90 s; clear at a
glance), agreeing on 295 of 317:

| | Old panel | Focused |
|---|---|---|
| Items on screen (median) | 12 | 4 |
| Characters on screen (median) | 13,544 | 2,563 |
| Helpful (grader sets 1 / 2) | 30% / 26% | 28% / 24% |
| Clear | 51% / 42% | 79% / 59% |
| Helpful items on screen per moment | 3.5 / 3.0 | 1.1 / 0.9 |
| Moments with nothing helpful | 0 / 0 | 7 / 8 of 20 |
| Characters per helpful item (median) | ~4,300–4,600 | ~2,250 |
| Distinct items an hour | 457 | 453 |

Full cards were clear 2% of the time, focused cards 65%. Relevance didn't
move: a quarter of what is shown helps either way, and keeping the newest
two of each drops helpful and unhelpful items alike. Churn is unchanged.
What gets shown is the remaining problem: candidates need judging for
relevance before they take a place. The 317 graded items are the test set
for that.

**Judging relevance before showing (offline, on the 317 graded items).**
Each item, with the last 90 s, asked of Jev as one yes/no question
("would glancing at it give them something relevant to what is being
discussed right now?") and of GLM-5.3-Flash as a 0–3 rating; zero-shot,
634 calls, 38 s, $0.06. Against the graders (helpful by both: 24% of
items), Jev ranks at AUC 0.88, GLM 0.79. Choosing the panel's four items
by score instead of by recency:

| Choice of four | Helpful items on screen per moment | Moments with nothing helpful |
|---|---|---|
| Newest (focus as shipped) | 1.0 | 7 of 20 |
| Top 4 by Jev | 2.45 | 1 of 20 |
| Top 4 by GLM | 2.62 | 1 of 20 |
| Old panel, 12 items | about 3.2 | 0 of 20 |

As a gate, Jev ≥ 0.7 keeps 30% of items, 63% of them helpful; GLM's 3
keeps 17%, 66% helpful. One meeting so far: to be confirmed on another.
Jev is cleared for testing, not for the product, where GLM (or the
decider emulation behind provider.Classifier) is the path.

## 2026-10-23: Lean extraction in the app

The measured meeting prompt (lean_ex_v2, until now `tools/insights-eval`)
now runs in the Go pipeline (`knowledge.extract = "lean"`), with the same
prompts, parts (50 lines, 8 of context), brief and schema; items become
statements through the import path. Speaker placeholders (Tomoe's
"Person 3", a room's device, a provisional "Ana?") are shown to the model
as "unknown speaker", as the Python tool showed unknown labels.

Parity, two September meetings on GLM-5.3-Flash, same directory file, each
meeting's title as its description (as the September run): the Q4 roadmap
meeting gave 91 items (Python 87), the same mix of kinds, 84% of the Python
items with a close counterpart (word overlap ≥ 0.4); a 1:1 gave 35 (Python
41), 59%. Sampling isn't deterministic (temperature 0.7), so some spread is
expected; no kind or attribution pattern differed. $0.034 for both, about
4 minutes each.

## 2026-10-26: Where a batch run's time goes

The October rethread (lean extraction of 108 meetings, then threading
about 300 meetings oldest first, GLM-5.3-Flash on Baseten) at the point
119 meetings were threaded:

| Phase | Wall clock | Note |
|---|---|---|
| Extract 108 meetings | 1h24m | 2 meetings at a time (`--workers` default 2) |
| Thread 119 meetings | 3h56m | 2.0 min a meeting, strictly in turn |
| — place (assign, merge) | 1h28m | 37% |
| — reconcile | 2h13m | 57% |
| — review | 1h50m | overlapped with the next meeting, mostly hidden |

1,600 threading calls, 8.8 s each on average; a meeting waits on its
slowest call. Already measured and not a lever: parallel 16, hedging,
the stall rule (2026-10-20/21). Low reconcile effort was rejected on
quality (2026-10-16).

Not yet done, to do before the next batch run:
1. Extract more meetings at once. The CLI's `--workers` default of 2
   dates from the local model; hosted, 8–16 would have taken the
   extraction to about 10–15 minutes. No change in output.
2. Thread while extraction runs: thread the oldest meeting as soon as it
   is extracted, instead of after the whole batch. No change in output.
3. Pipeline meetings: place meeting N+1 while N reconciles. Placement then
   sees thread summaries one reconcile behind; needs a quality check on
   the four ELT meetings. Up to about a third of threading.
4. Thread short consecutive meetings in one pass (fewer slow-call waits).
   Needs the same quality check.
5. Serving: the slowest calls stream at about 4 tokens a second on the
   shared endpoint. Measure a dedicated deployment or another approved
   provider; a narrower reconcile (2026-10-20) is still unbuilt.

## 2026-10-28: Paper test of topic card content

One 45-minute 1:1 (picked at random) through a stepped replay with the
shipped settings (focus, Jev relevance, `keep_topics`). 26 cards opened;
12 sampled. At each, the user graded three unlabelled versions against the
last 90 s of talk:

- **A**: as shipped (the raising line + the 3 lines closest to the talk).
- **B**: the status sentence + 3 lines on the topic that the last 5 minutes
  had not covered, open/due items first (embedding novelty).
- **C**: a GLM-5.3-Flash headline (where it stands, ≤18 words) + one
  "raise" line (what to raise or ask in this meeting), one call per card
  from its own lines. 97 cards, $0.02.

And at three points, the menu of earlier cards: recency (as shipped)
against C's raise lines ranked by a 0–3 "worth" score.

| | helpful | some | no |
|---|---|---|---|
| A | 0 | 3 | 5 |
| B | 0 | 2 | 5 |
| C | 3 | 1 | 4 |

(8 moments graded per version; the other 4 got notes only.)

Findings:
1. **Topic choice is the bigger problem.** 5 of 12 moments were the wrong
   topic altogether (every version "no"): the talk was about something
   with no card raised, or a broad theme (a product's escalations, the
   team's workload, a reorg) that the matcher mapped to one peripheral
   card. One was a noisy cluster. No card content fixes these.
2. **When the topic is right, C wins**: helpful 3 of 4, A and B never.
   Tight text preferred throughout; A's lines read as too much text.
3. **What the user wanted instead** was situational, across topics:
   "escalations I've heard about recently for this product", "this
   person's current priorities as I understand them", "the proposals this
   person has made". That is retrieval by situation (see 2026-10-1x
   insights direction), not one topic's card.
4. **Person cards** worked as branching structure: nearby topics worth
   getting into with the person in the room.
5. **Menu: the raise list won 3 of 3** ("I really like the menu",
   especially the conceptual questions), but it is too long: 24–27 items.
   The per-card worth score does not thin it: 52 of 77 scored 3, the rest
   2. Scored one card at a time without the talk, everything looks worth
   raising.

Next: thin the menu by ranking the candidates together against the talk
and attendees and showing ~5; put C's headline + raise line on cards.
Topic choice (finding 1) is the larger, separate problem.

## 2026-10-28: Topic choice: a picker over candidates

Why the paper test's cards were off-topic, and a first fix measured
offline on the same meeting.

**A replay leak** (fixed, f977874). Replays and `card --at` found topics
in the whole knowledge base, including topics threaded from the replayed
meeting itself. The most-matched topic in the paper test's replay was
built from that meeting: empty as of the meeting, so never shown, while
it took the lookups from the right earlier topic. The index is now built
from what was said before the meeting.

**Per-query matching.** Each lookup (up to 3 a cycle) opens the topic of
its nearest claims at similarity ≥ 0.66. Similarity doesn't separate good
from bad (good 0.69–0.78, bad 0.69–0.80), and the relevance check passed
wrong cards at 0.56–0.89: judged one at a time, and shown the very words
that matched, almost any card looks relevant.

**The test.** 45 one-minute windows (the last 3 minutes of talk each),
with the index as of the meeting. Candidates per window: the 12 topics
most of that stretch's lookups, trigger terms and the talk itself point
at. Reference: an Opus labeller, blind to the picks, marked each window's
candidates right (what the conversation is about), adjacent (worth
raising next) or neither, calibrated on the user's paper-test notes. 42
windows have a right candidate; 3 don't.

| Method | Cards | Right | Right or adjacent | Top pick right | Silent when it shouldn't be | Shows when nothing fits | Latency (median / p90) |
|---|---|---|---|---|---|---|---|
| Today's rule (two most recent) | 90 | 12% | 13% | 4/42 | 0/42 | 3/3 | — |
| Jev choice, p ≥ 0.3 | 46 | 80% | 83% | 34/42 | 2/42 | 2/3 | 0.13 s / 0.17 s |
| Jev choice, p ≥ 0.4 | 37 | 84% | 86% | 30/42 | 8/42 | 2/3 | |
| Jev choice, p ≥ 0.5 | 29 | 90% | 93% | 26/42 | 15/42 | 2/3 | |
| GLM-5.3-Flash picks ≤ 2 | 78 | 69% | 79% | 32/42 | 0/42 | 2/3 | 5.3 s / 9.8 s |

Jev: one choice question, the candidates (title, last discussed, status
sentence) plus "none"; $0.004 for 45 windows. GLM: the same list, picks
up to two and names a theme ($0.016). Today's rule is approximated
without the relevance gate, which in the replay passed nearly
everything.

Findings:
1. Choosing among candidates against the conversation, not matching each
   query, is the fix: 12% → 80–90% right.
2. Jev beats GLM on precision at a fraction of the latency; GLM nearly
   always picks two. GLM's theme names read well ("Graph escalations &
   SaaS CMS feedback") but are not needed to choose.
3. The ceiling is now the candidate list: the right topic was sometimes
   missing in windows where it was being discussed, and several subjects
   had no topic at all. Old topics (board meetings, a roadshow) and
   name-only matches crowd the 12.
4. Caveats: one meeting, one labeller (a model), and only 3 windows where
   nothing fits, so "shows when nothing fits" is barely tested.

Next: a Jev picker in Live behind a config key, then the user grades a
second meeting.

## Live suggestion model: qwen3.5 vs qwen3:8b (2026-10-29, first pass)

40 real suggestion prompts, evenly spread and deduplicated from the two
replayed meetings (about 520 prompt tokens each), run one at a time through
Ollama with thinking off, JSON mode, temperature 0.3, 300 token cap. Today's
`suggestPrompt` as the system prompt. Streamed, so TTFT is the first token.
Machine was not idle (Tomoe recording, load about 3). The qwen3.5 models had
not been tried for Live before; Live's choice of qwen3:8b came from a table
that only covered the qwen3 family.

| | TTFT p50 / p90 | total p50 / p90 | decode | valid JSON | empty | queries naming the user |
|---|---|---|---|---|---|---|
| qwen3:8b (now) | 0.40 / 0.58 s | 0.93 / 1.09 s | 47 tok/s | 40/40 | 1 | 0 of 117 |
| qwen3.5:9b | 0.74 / 0.90 s | 1.36 / 1.79 s | 42 tok/s | 40/40 | 1 | 4 of 117 |
| qwen3.5:4b | 0.44 / 0.52 s | 0.85 / 0.98 s | 68 tok/s | 40/40 | 1 | 16 of 116 |

- The 4B matches the 8B on speed (a better p90, 1.45x decode) but names the
  user in 14% of its queries despite the "never look up the user" line; the
  9B does so 3% of the time and is about 1.8x slower to first token. The 8B
  never did. `dropOwner` removes these afterwards, but each one costs a slot
  of the three.
- Format held for all three. Queries differ in quality (the 3.5 models
  tend to join a person's name to a topic); not graded yet.
- Not tested: quality of queries against the knowledge base, briefs, loaded
  machine, other quants and engines, Gemma 4.
- Raw results and the sample are in the session scratchpad (live-bench/).

## Statement search for Live: which kinds come back (2026-10-29)

Experiment in the pipeline playground (`cmd/lab`, untracked): rank statements
(points) by the query's embedding (nomic-embed-text, cosine) plus 0.1 × the
share of the query's content words they contain, scoped and dated before the
meeting. 60 queries sampled from 2,341 distinct queries the Live model wrote
on two ELT meetings; top 5 each (300 results). Share of results by kind, against
the share of the corpus (20,879 points):

| kind | corpus | top 5 | ratio | #1 of 60 |
|---|---|---|---|---|
| fact | 44.7% | 52.0% | 1.16 | 30 |
| position | 32.3% | 20.7% | 0.64 | 12 |
| commitment | 5.6% | 10.3% | 1.84 | 4 |
| question | 4.6% | 3.7% | 0.81 | 2 |
| metric | 2.9% | 9.0% | 3.08 | 10 |
| decision | 2.6% | 1.7% | 0.64 | 1 |
| rule | 2.3% | 1.3% | 0.59 | 0 |
| framing | 2.0% | 0.7% | 0.34 | 1 |
| personal | 1.8% | 0.7% | 0.37 | 0 |
| outside_fact, disagreement, sensitivity_cue | 1.3% | 0% | 0 | 0 |

Search doesn't over-favour facts and positions (73% of results, 77% of the
corpus); metrics and commitments come back well above their share, decisions,
rules and framings below. This measures what comes back, not what helps.
A per-kind score boost is in the playground; on one query a +0.15 decision
boost filled the top 5 with decisions at cosine 0.56–0.60, about where
unrelated statements sit, so boosts likely need a similarity floor.

## Live lookups: statements vs passages, boosts, pooled choice (2026-10-29)

Setup (playground, `cmd/lab`): 16 moments evenly spread over two ELT
meetings (8 each), the three queries Live's model actually wrote at each (48
queries). Candidates per query: top 15 statements (points) by similarity plus
the top 5 under eight boost probes (kind, status, recency), and Live's top 6
passages (hybrid search). 1,026 items judged 0/1/2 against the 90 s
conversation by four Claude judges (shuffled, no kind/rank/score shown):
79% 0, 17% 1, 4% 2 (39 items rated 2). One judging pass; treat as
directional.

**Statements beat passages on the same queries.** Per query, top 5:

| | gain (sum of 0–2) | useful (2s) | top result useful |
|---|---|---|---|
| passages (Live today) | 1.48 | 0.12 | 8% |
| statements, similarity + 0.1 × word overlap | 2.33 | 0.65 | 21% |

Moments with a useful item in some query's top 5: statements 9/16,
passages 3/16.

**Boosts don't generalise.** Coordinate ascent over word weight, a similarity
floor, recency (weight, half-life), kind and status boosts, tuned on one
meeting: the other meeting got slightly worse both ways (gain 2.33 → 2.25 and
2.33 → 2.21). Useful rate by kind: metric 15% (n 72), commitment 6%, fact 4%,
position 1%, decision 0% (n 38), agreed 0% (n 72). By age: under 14 days 0%
(n 38), 14–60 days 5%, older 2–4%: the latest news is what the room already
knows. Ranking isn't the bottleneck: most statements rated 2 are already in
the top 5 (29 of 62), most of the rest in the top 15.

**Pooled Jev choice over statements** (all three queries, 5 statements each,
one `choice` call, 16 calls): where a useful statement was among the options
(9 moments), Jev's top choice was useful in 6. With Live's picker bar (p ≥ 0.4)
only 3 were shown, because probability spreads over near-duplicate
statements; gating on p(none) < 0.3 and showing the top choice would show a
useful item at 6 moments, an on-topic one at 7, an off-topic one at 1, and
nothing at 2 (both without anything useful). The same pooled choice over
passages showed one useful item in 16 moments.

**What's left is the queries.** 5 of 16 moments had nothing useful in any
pool: small talk (correctly empty) and "who is the contact for X" questions,
which statements rarely answer (entity cards might). Some useful statements
sat at ranks 37–124 behind poorly worded queries ("… definition").

## Live lookups end to end: statements vs passages on replay (2026-10-29)

Question: does `live.lookups = "statements"` (pooled classifier choice over
statements, shown when P(none) < 0.3) put more useful things on the panel
than passage lookups, in the real pipeline rather than on captured moments?

Method: stepped replays of the two meetings used above (about 61 and 38
minutes), with the focus snapshot, `live.pick` on and Jev relevance.
Baseline: the passage-lookup replays of 2026-10-21 (before the picker). Every
lookup the panel opened was paired with the last 90 s of conversation at that
moment; the 294 items (both sources, shuffled, source hidden) were labelled
0/1/2 by four Claude judges on the usual rubric (2 = would help right now).

Bug found first: with `live.pick` on, shown lookups were never judged again
(the picker change skipped `rescore`), so the first four lookups held every
place for the whole meeting: run 1 showed 5 and 4 lookups. This also affects
passage lookups in the installed build. Fixed (`rejudge`, test
`TestRejudgeWithPicker`); the numbers below are from run 2, after the fix.

| | shown | useful (2) | 1 or 2 | noise (0) | mean |
|---|---|---|---|---|---|
| passages, meeting 1 | 37 | 5 (14%) | 73% | 10 | 0.86 |
| statements, meeting 1 | 127 | 33 (26%) | 73% | 34 | 0.99 |
| passages, meeting 2 | 68 | 11 (16%) | 44% | 38 | 0.60 |
| statements, meeting 2 | 62 | 11 (18%) | 60% | 25 | 0.77 |
| passages, both | 105 | 16 (15%) | 54% | 48 | 0.70 |
| statements, both | 189 | 44 (23%) | 69% | 59 | 0.92 |

- Useful items per 10 minutes: meeting 1 rose from 0.8 to 5.4; meeting 2
  stayed at 2.9, with a third less noise (25 vs 38).
- Latency is unchanged: median 1.9 s (meeting 1) and 3.5 s (meeting 2) from
  the words being said to the lookup being shown, against 2.4 s and 2.9 s.
  The statement choice takes about 80 ms; statement search about 50 ms.
- Pools are mostly one query: 327 of 346 choices in meeting 1 had a single
  query's five statements, because the near-repeat lock lets about one new
  query through per cycle. Pooling across cycles is untested.
- Still a third of what is shown is noise (rated 0). The P(none) bar (0.3)
  and the four places are the next levers.

Caveats: the baseline ran without the picker and on older code, so panel
competition differs; two meetings; judges are one model family.

Decision (owner, 2026-10-29): statements are the default for Live lookups
when relevance is on (`live.lookups = "passages"` restores the old path).

## 2026-11-02: To-dos from a whole-meeting pass vs from statements (side by side)

Question: does one narrow whole-meeting call that returns only to-dos give a
better list than the to-do view's source today (extracted statements of kind
commitment, plus proposals)? Five all-t4 meetings (42 to 142 chunks; one
Bluedot with 241 speakerless lines), prompt tuned once on one meeting (the
only change: resolve a range or approximate timing to its later or last day).
B = GLM-5.3-Flash, whole transcript in one call, low reasoning, structured
fields (owner, requester, agreed/asked/floated/declined, due text and date,
quoted evidence). Reference lists written blind by Opus from the transcript
alone; two Opus graders judged both lists blind (sides randomised and
unlabelled) against the transcript. Owner marks in owner_links were not used
(all three were undone beforehand; backup in the dataset folder).

| Mean of two graders (131 A items, 104 B items) | A: statements | B: whole-meeting pass |
|---|---|---|
| Good, stated correctly | 68% | 88% |
| Flawed (wrong owner, figure or state) | 18% | 8% |
| Not a to-do | 13% | 3% |
| Duplicates within the meeting | 17% | 4% |
| Owner right | 87% | 96% |
| Agreed/floated state right | 90% | 98% |
| Reference to-dos found | 57-64% | 68-71% |
| Cost per meeting | n/a (already extracted) | 0.2-1.1¢ (2 h meeting: 1.1¢) |

- Largest gap on the longest meeting (142 chunks): B found about 80% of the
  reference against about 48% for A.
- Due dates (B only; A has no field): of 100 matched items, 79 equal the
  reference, 13 differ or are missing on one side, mostly the last-day
  convention for approximate periods; one real error ("for January" dated 31
  December).
- Caveats: A's duplicates are partly folded by the view's repeat relations;
  A includes proposals, which the view hides by default and the graders
  often judged not to-dos; B's prompt and the reference share my definition
  of a to-do; the link from B's items to existing statements (state from
  relations) is not measured yet; one prompt tuning step, no variance runs.
- Spend: GLM about $0.03 in total; graders and references were Opus
  subagents.

## 2026-11-02: Meeting summaries: sectioned with citations vs plain paragraph

Question: should a meeting summary be stored, and in what form? Same five
meetings, GLM-5.3-Flash, one whole-transcript call per summary, low
reasoning. Arm S: an overview plus one section per subject (title, 2-4
sentences, people, cited line ranges, sensitive categories). Arm P: one or
two paragraphs, at most 250 words. Two Opus graders judged blind (sides
randomised) against the transcript. Topic links were read from existing data:
the threads of the meeting's current statements, weighted by statement count.

| Two graders together | S: sectioned | P: plain |
|---|---|---|
| Preferred (10 judgements) | 10 | 0 |
| Major subjects covered (93) | 100% | 85% |
| Errors per claim (approximate counts) | 2.2% (20 / 898) | 3.4% (12 / 354) |
| Main error type | wrong attribution, 13 of 20 | overstated certainty, 6 of 12 |
| Cost per meeting | 0.25-0.67¢ | 0.14-0.23¢ |

- Section citations contained what the section said in 97% of cases (146 of
  150), but the ranges are wide (e.g. lines 14-92), so they are loose
  evidence, not tight.
- Sensitive flags: 93% right, 7% over-flagged, none missed (err on the
  safe side).
- Topic links from statement membership (126 judgements): 58% belong, 31%
  partly, 11% not here. Weight predicts it: median statements from the
  meeting 9 (belongs), 3-4 (partly), 1 (not here). A minimum weight of
  about 3 to 5 would drop most of the weak links. 3 to 6 major subjects in
  all five meetings had no matching topic.
- Caveats: claim counts are approximate and error counts small; one
  Bluedot meeting has unreliable speaker labels (graders went by labels as
  written); sensitive flags not yet compared with the statements' own
  labels; no test of whether summaries help Ask.
- Spend: GLM about $0.03; graders were Opus subagents.

## 2026-11-02: Meeting digests on the whole dataset

Built as stages (`knowledge.digest`, docs/architecture.md "Meeting digests")
and run over every meeting in all-t4 (335, the 84 recorded twice counted
once): 56 minutes, 4 at a time, GLM-5.3-Flash, none failed; about half a
cent a meeting by token counts (the Go provider doesn't log these calls to
the ledger).

- Stored: 2,727 sections, 3,081 to-dos (2,360 agreed, 418 asked, 299
  floated, 4 declined), 768 with a date. 94.7% of to-dos matched a
  statement (2,916); all cite at least one line; 29 meetings have none (one
  of 60 or more passages).
- The code's own check (names and figures against the meeting) marks 20% of
  sections and 5% of to-dos "check wording". The blind graders found about
  2% wrong per claim, so most flags are misheard or misspelt names and
  acronyms: a prompt to look at, not an error count.
- Two model habits found on real meetings, both now asked again once (long
  meetings only): an empty to-do list for a 470-line meeting with a dozen
  commitments (the first run; a rerun found 13, all matched), and to-dos
  with the evidence field left empty (a 650-line meeting; also seen in the
  five-meeting test). A reply is kept only if it cites more.
- Re-running the same prompt on a meeting does not give the same list: the
  stage is stamped so it runs once per meeting, and a rerun is by
  `--redo digest`.

## Live suggestion model: qwen3.6-35B-A3B builds (2026-11-05)

Same 40 prompts and `suggestPrompt` as the 2026-10-29 run; thinking off,
temperature 0.3, 300-token cap, one model loaded at a time, Tomoe idle (checked
before every prompt). Ollama runs used JSON mode, except nvfp4: Ollama's MLX
engine rejects `format` ("structured output is unavailable"). The two MLX
builds ran on mlx_lm.server 0.32.2 (OpenAI-compatible, no JSON mode). Memory is
the server process's physical footprint, so it reads higher than `ollama ps`.
qwen3:8b was re-run as today's baseline; its TTFT is slower than on 10-29.

| model | TTFT p50 / p90 | total p50 / p90 | decode | valid JSON | queries naming the user | memory |
|---|---|---|---|---|---|---|
| qwen3:8b (Live today) | 0.67 / 0.93 s | 1.18 / 1.49 s | 47 tok/s | 40/40 | 0 of 117 | 11 GB |
| qwen3.6:35b-a3b Q4_K_M (Ollama, llama.cpp) | 0.68 / 1.31 s | 1.56 / 3.68 s | 46 tok/s | 40/40 | 0 of 117 | 30 GB |
| qwen3.6:35b-a3b-nvfp4 (Ollama MLX engine) | 0.46 / 0.60 s | 0.79 / 1.04 s | 92 tok/s | 39/40 | 0 of 122 | 29 GB |
| OptiQ-4bit-REAP-19B (mlx-lm; half the experts pruned) | 0.26 / 0.31 s | 0.77 / 0.86 s | 64 tok/s | 39/40 | 0 of 117 | 13 GB |
| unsloth UD-MLX-3bit (mlx-lm) | 0.28 / 0.34 s | 0.83 / 0.94 s | 67 tok/s | 39/40 | 0 of 117 | 17 GB |

- The engine matters more than the model: on MLX the 35B-A3B reaches first
  token in about 0.3 s, against 0.67 s for qwen3:8b on llama.cpp. Prefill is
  where it wins, which is what Live needs.
- None of the qwen3.6 builds named the user (qwen3.5:4b did in 14%).
- Without JSON mode, format still held: the only miss in each MLX run was a
  bare `[]` on the near-empty first prompt.
- REAP-19B is the lightest (13 GB, about qwen3:8b plus 2 GB) but drops 21
  points on MMLU per its card; UD-MLX-3bit keeps every expert for 4 GB more.
- Not measured: query quality (ungraded), behaviour under meeting load, cold
  load time. Adopting either MLX build needs Live's chat client to accept an
  OpenAI-compatible server (it is Ollama-only today) and a JSON fallback.
- Scripts and raw results: session scratchpad `bench/` (bench2.py, run-all.sh,
  analyse.py, results2.json).

### Query quality, blind (2026-11-05)

The three light candidates' queries from the run above (qwen3:8b, OptiQ
REAP-19B, UD-MLX-3bit; 351 queries), graded blind by two independent Claude
graders with separately shuffled set order: 2 = useful, 1 = okay/generic,
0 = not useful (off-topic, small talk, ungrounded, or the user). Per-query
agreement between graders 76%.

| model | useful, grader 1 / 2 | not useful, 1 / 2 | prompts with a useful query, 1 / 2 | best set, 1 / 2 |
|---|---|---|---|---|
| qwen3:8b | 36% / 28% | 7% / 8% | 25 / 21 of 40 | 6 / 5 |
| OptiQ REAP-19B | 46% / 37% | 11% / 9% | 30 / 25 of 40 | 4 / 5 |
| UD-MLX-3bit | 50% / 35% | 10% / 11% | 30 / 25 of 40 | 10 / 4 |

- Both qwen3.6 builds give more useful queries than qwen3:8b under both
  graders (+7 to +14 points) and a useful query on 4-5 more prompts of 40,
  with slightly more misses (+2-4 points of 0s).
- Pruning half the experts cost nothing measurable here: REAP-19B and
  UD-MLX-3bit are within grader noise of each other.
- "Best set" votes are mostly ties and don't separate the models.
- Same failure in all three: queries on small talk (one prompt where all
  sets should have been empty). Small sample (40 prompts); usefulness here
  is judged from the conversation alone, not from what the search returns.

## Live card brief: GLM vs local qwen3.6 (2026-11-05)

Can a local model replace GLM-5.3-Flash for `live.pick.brief` (card headline
+ up to 3 things to raise)? 32 distinct cards from the two 2026-10-29
stepped replays. Prompts rebuilt with the playground's `/brief` (exact
`cardBriefPrompt`, card and status as of meeting start; window = segments in
the last 180 s before the brief, speakers so far), so all three systems got
identical prompts. GLM was called fresh through `/brief`; the local builds
ran on mlx_lm.server, thinking off, temperature 0.3. Two independent blind
Claude graders, set order shuffled separately; errors = statements not
supported by the notes or conversation (invented, wrong number/date/person,
overstated status).

| system | latency p50 / p90 | briefs with an error (g1 / g2) | errors | headline mean (0-2) | raise mean (0-2) | best brief (of 32) |
|---|---|---|---|---|---|---|
| GLM-5.3-Flash (Baseten) | 8.0 / 20.6 s | 11 / 12 | 16 / 15 | 1.59 / 1.62 | 1.74 / 1.41 | 30 / 29 |
| UD-MLX-3bit (local) | 2.7 / 3.7 s | 24 / 25 | 34 / 37 | 0.44 / 0.59 | 0.90 / 0.81 | 1 / 2 |
| OptiQ REAP-19B (local) | 3.7 / 6.4 s | 25 / 24 | 33 / 31 | 0.59 / 0.53 | 1.09 / 0.89 | 1 / 1 |

- GLM stays. Both local builds put an error in about three briefs of four
  (GLM: one in three) and lose "best brief" 29-30 of 32 under both graders.
  Typical local errors: invented deadlines, statuses overstated ("resolved",
  "finalized"), numbers from one account attached to another.
- Local is 3-5x faster and has no long tail; GLM's p90 of ~21 s is the real
  Live problem, but this doesn't solve it without a quality loss (see
  "no quality trade-offs").
- Some errors are in the input, not the model: on one card all three systems
  attached another account's sponsor and go-live to the topic, and several
  local errors conflate two accounts that share a card. Worth checking card
  grouping for mixed accounts.
- Even GLM errs on about a third of briefs; error types: misdated stances,
  figures from adjacent months, "opportunities" called "accounts".
- Data: session scratchpad `brief/` (briefs.json with prompts and outputs,
  grade packets, keys, grades, score.py).

### Why some card briefs mixed up accounts (2026-11-05)

Follow-up on the brief comparison above: the graders flagged three cards
where every system attached one customer's facts to another. Checked the
cards, the picker's trace and the threads in the playground database. The
model is not the main cause; four separate problems:

1. **Picker accepts the nearest card when the account has none.** A
   customer discussed for the first time (zero prior statements) got a
   CMS integration card for a different customer, at p = 0.48 (min_p 0.4).
   The picker's own queries named the right customer; the chosen card
   never mentions it. Every system then wrote the new customer's sponsor and
   ARR into the other customer's brief.
2. **The 3-minute window spans a change of subject.** When the meeting moved
   to the next customer, the window still held the previous customer's
   sponsor discussion, and the briefs attributed it to the new one.
3. **Theme threads carry one account's name or status.** The card titled
   for one partner account (128 current statements) is really the CMS–CMP
   integration theme: 17 statements mention that account, 49 are about the
   integration in general. Another theme card ("customer onboarding &
   adoption", 144 statements) has a status line entirely about one
   customer.
4. **One account's statements are split between a theme thread and its own
   thread.** That customer has its own card (138 statements), and 57 more
   about it sit in the theme card above; the two share none. So neither card
   has the whole story, and the theme card reads as that account's card.

Problems 1-2 are Live (picker, brief window); 3-4 are threading and
labelling. Not yet measured: how many cards have problems 3-4.

### Card fit check and split window: results (2026-11-05)

Fixes for problems 1-2 above (`internal/live/pick.go`):
- **Fit check.** The brief model first answers `fits`: false when the
  conversation is about a customer, partner, person or project the notes
  never mention. The card is then taken down and not picked again for
  10 minutes (`live.pick.keep_unfit = true` keeps it). The brief also sees
  the lookups that put the card forward: in the worst case the other
  account was named 182 s before the pick, outside the 3 minutes, and
  only a lookup ("<account> PaaS CMS onboarding") still carried its name.
- **Split window.** The 3 minutes are split where the card was first put
  forward, less 15 s, and the earlier part is marked as possibly another
  subject (`live.pick.full_window = true` restores one window). Lookups
  trail the words that prompted them by 11 s median, 29 s p75, 57 s p90
  (464 lookups, both replays). A 60 s lead still reached the previous
  account's sponsor discussion; 15 s did not.

Same 32 cards, GLM-5.3-Flash, one run; old vs new graded blind by two
Claude graders (order shuffled separately), who also judged each card's
fit without seeing either system's verdict:

| | grader 1 | grader 2 |
|---|---|---|
| cards graded fit yes / partly / no | 11 / 13 / 8 | 11 / 13 / 8 |
| taken down by the new brief | 8 | 8 |
| take-downs judged right | 8 of 8 | 8 of 8 |
| errors, old briefs (cards with one) | 35 (26 of 32) | 31 (22 of 32) |
| errors, new briefs (cards with one) | 16 (14 of 24) | 12 (12 of 24) |
| better: new / old / tie | 18 / 2 / 12 | 19 / 3 / 10 |

- No useful card was taken down. Both mixed-up account cards from above
  are now taken down; the third (the next account, after the subject
  changed) keeps its card and no longer borrows the previous account's
  sponsor.
- Three cards both graders call unfit still pass: the conversation is on
  a different subject (support numbers against an adoption card, a
  support roadmap against an engineering staffing card), not an
  unmentioned account. The check is worded for accounts, people and
  projects; widening it to subjects is the picker's job and untested.
- Theme cards still carry one account's slant (problem 3): a card kept
  as fitting the general subject still headlines that account. That
  belongs to threading (`docs/incremental-design.md`, "Subject shape").

## 2026-11-06: MCP server, first run on all-t4

`musubi mcp` (docs/mcp-design.md) driven over stdio against all-t4, reads
only. Latency per call: meeting 0.03 s, thread 0.8 s, threads list 0.6 s,
gather 0.3 s (30–50k characters of context), search 0.05 s.

Two labelling faults found and fixed before use:

- **Digest to-dos read as public.** `DigestCommitments` gave each to-do the
  digest's words but not its label; ten of the owner's open to-dos came back
  `public`. Each now carries its digest row's and item's label (all ten
  became party-only), and the server combines every result with its item's
  own label as a second guard.
- **Owner-only was too common.** With restricted rated owner-only, 27 of the
  40 latest meeting overviews and 36 of 50 thread statuses were owner-only,
  nearly all for HR or financial material rather than personal details, and
  the verdict disagreed with `fits` for attendees. Rated as the reply rule
  rates it (restricted may go to those party to it), owner-only is 5 of 40
  overviews, 4 of 167 sections (20 meetings), 12 of 50 statuses and 31 of
  856 current items (20 threads); the rest is party-only but 4 internal
  items.

Nearly everything in all-t4 is party-only: it is leadership meetings, which
default to confidential. A consumer writing for anyone outside a meeting
will find little that fits, which is the point.

## 2026-11-09: Microsoft 365 connection, first calendar sync on all-t4

- Signed in with `musubi m365 connect`, the Graph Command Line Tools client.
  The refresh token is kept in the Keychain.
- First sync, 30 days back and 14 ahead: 201 events written. The update
  then linked 79 Tomoe recordings to their events; before, 0 were linked,
  because the dataset had no calendar.
- `calendar.json` was written next to all 79 recordings. The registry grew
  from 778 to 1,836 entities, mostly invitees.
- Labels: 19 of 1,464 items changed, nearly all meetings now labelled from
  their real invitee list. 17 became less restricted and 1 more. 15 of the
  19 have digests, which the next knowledge run redoes (a few cents on
  GLM-Flash).
- **Link audit against Tomoe's window titles** (looks.jsonl), for the 79
  linked recordings:
  - 56 agree;
  - 10 have no meeting window to compare (Zoom and other calls);
  - 13 disagree, mostly ad-hoc calls whose window names a person, which
    were linked to a large event under way at the same time.
- Fix, two parts:
  - Tomoe titles recordings from the call window (Tomoe PR);
  - linking refuses an event when the recording's own title names another
    meeting, and otherwise prefers the event the title names, then the ones
    the owner organized or accepted.
- Recordings saved before the Tomoe change keep generic titles, and so keep
  the time-only link until they are retitled.

## 2026-11-09: Teams transcripts and meeting chats, first run on all-t4

- `update`, with `transcripts` and `meeting_chat` on and `unattended` off,
  30 days back:
  - 97 past Teams meetings;
  - 28 transcripts kept and 3 passed over (the owner neither recorded nor
    spoke);
  - 45 meeting chats.
- Against Tomoe (size of text):
  - 2 transcripts are meetings Tomoe has no recording of;
  - 3 have a Tomoe copy under 80% of the Teams copy;
  - 23 are second copies, with named speakers.
- Linking went from 79 to 105 meetings (the Teams copies link too).
- Labels changed on 2 passages of 3,958.
- Processing the 74 new items (chunks, labels, local embeddings) took 7 min
  at background priority.

## 2026-11-09: incident — retitling deleted statements; fixed and recovered

What happened:
- Linking meetings to their calendar events (first calendar sync, #9)
  changed their passages' title. The title is part of the chunk hash, so
  280 meetings were re-chunked.
- `ReplaceChunks` deleted and re-inserted every passage, and the delete
  cascades: statements went from 34,970 to 26,313 and thread items from
  34,224 to 25,468. The Musubi app's automatic updates repeated it as more
  meetings linked.
- Raw data and the inbox were untouched. No owner edits had been made
  since the backup taken just before the sync.

Fix (#9):
- `ReplaceChunks` updates titles in place when the passages are otherwise
  identical (sequence, segments, times, speakers, text hash). A real
  change of text still replaces them.
- The FTS table gets the AFTER UPDATE trigger it lacked; without it an
  in-place update corrupted the index.

Recovery:
- Restored the 10:51 backup and re-ran `update` with the fix. Calendar,
  Teams and directory data were re-ingested from the inbox.
- Statements are back at 34,970 and thread items at 34,224, with 106 links.
  `integrity_check` and the FTS integrity check pass.
- Knowledge runs made in between (a few hundred statements for new Teams
  copies, a few digests) are redone by the next automatic run.

## 2026-11-09: names for meetings with no calendar event

- 182 Tomoe recordings: 78 linked to a calendar event. 104 had a generic
  title and no event; 95 of those had a digest.
- `musubi name-meetings` (GLM-5.3-Flash, the threads model) named 99 in
  two runs (60 + 39), about 1 minute each, from the digest's overview and
  section subjects, or the first 4,000 characters without one. 5 had too
  little text.
- Names are stored in `item_titles`. Every title shown now uses one rule:
  the calendar event's subject, else the given name, else the item's own
  title.
- Passage titles follow it in place: the next update retitled 99 meetings
  (2,286 passages) and statements stayed at 34,970.
- `title.json` is written next to the 99 Tomoe recordings (Tomoe #86 shows
  it).

## 2026-11-09: People tab and person pages

- **People** (Explore, owner scope): 1,558 person entities with a meeting or
  a directory entry, most met first. A recording linked to a calendar event
  counts once (as the event). The list builds in under a second.
- **Person page** for the 12 most-met people:
  - directory details for 11 (one has no directory entry);
  - recent and upcoming meetings, topics and open to-dos for all 12;
  - a headshot for 11 and a voice clip (8–15 s) for 11.
- Topics come from the threads holding the most of what the person said
  (current items, by `holder_id` or the written holder). `topic_entities`
  is empty in all-t4.
- **Headshots:**
  - the active-speaker tile of a Tomoe look whose name read is the person;
  - one tile only, at least 120×90 in window pixels, with a thumbnail;
  - cropped from the 320×200 thumbnail, largest tile first.
  - A camera-off tile (a picture on a plain background: border luminance
    standard deviation under 12) is passed over for one with video. If all
    are camera-off, the middle square (half the tile's height) is used. On
    3 sample people: 2 camera-on crops, 1 tight profile-picture crop.
- **Voice clip:** the longest 5–15 s turn attributed to the person, from the
  most recent Tomoe recording with audio. Played from `/replay-audio/`
  between its start and end.
- **Directory lookup v2** adds business and mobile phones, city, country and
  hire date. Older entries are looked up again: 978 in four syncs.
- Names across the app are links when they mean exactly one person
  (`PeopleNames`): speakers in transcripts, digest people and owners, to-do
  owners, prep attendees, statement holders.
- **Faces (later the same day).** Shaf's headshot was a profile picture
  that wasn't his face. Two causes:
  - The 6 largest tiles were all full-screen camera-off views. Of his 1,421
    tiles, Vision finds a face in 1,286, and 856 are live video (face height
    ≥ 28% of the tile), all in smaller gallery tiles.
  - The camera-on check also passed shared slides.
- Now:
  - Up to 60 tiles are taken, spread evenly across tile sizes, preferring
    direct name reads ("read … under the ring") over carried-over ones.
  - One-to-one calls are skipped: there the name is the window title,
    whoever is highlighted.
  - macOS Vision finds faces (`VNDetectFaceCaptureQualityRequest`) and
    compares them (`VNGenerateImageFeaturePrintRequest`). Live faces are
    preferred when there are two or more. The face nearest the others on
    average (the one that recurs) wins over slides or someone else shown
    under the name; among faces as close as the median, the best capture
    quality.
  - The headshot is framed around the face at 1.9× its size.
- The choice takes about 4 s per person and is kept on disk for a week
  (`data/headshots`); a cached page opens in 0.6 s. Without Vision (not on
  macOS) the tile heuristic is kept.

## 2026-11-09: coverage and search before retiring the old transcript search and Bluedot

Question: can the old hosted transcript search (a personal Supabase vector index over the
Bluedot folder) and Bluedot itself go, now that Musubi serves Claude Code (`musubi mcp`)?

**Search: January–September added to all-t4 for search only.** `bluedot_since` moved from
2026-10-01 to 2026-01-01 (config and database backed up first). `musubi update` imported 785
meetings in 10.5 min at background priority (passages, rule labels, names, nomic-embed-text
vectors; no paid model). `hold_older` keeps them out of extraction, threads and digests until a
run is started by hand, so nothing was spent. Re-classifying the 463 meetings already present
changed no label (1,586 passages compared), so no digest is invalidated.

13 questions with a known meeting (date + title), phrased as topics rather than titles:
Musubi hybrid search had the meeting in its top 5 for 12; the 13th returned the same recurring
series on other dates. The hosted index answered none: timeouts, then HTTP 500 "could not query
the database for the schema cache" (the project's database is paused or down).

**Coverage: what the transcript folder holds.** 1,062 files: 836 Bluedot recordings and 226 Teams
transcripts (fetched from Teams, including meetings the owner was invited to but did not join).
Since 2 October, 17 meetings had a folder copy but no Tomoe recording starting within 15 min:
- 9 Teams transcripts of meetings the owner wasn't in (0 turns by the owner; Tomoe's window
  log shows the owner in another meeting throughout);
- 1 Tomoe did record (start 25 min earlier, same meeting window throughout);
- 1 partial, already patched from Teams;
- 2 overnight trainings, not attended live;
- 3 real misses, all on one day: Tomoe crashed (SIGSEGV on the main thread, just after a save) on
  14 Oct and wasn't relaunched until 18 Oct. It is the only crash in the log since 2 Oct; the
  autorelease-pool fixes of 20–22 Oct may cover it, which one instance can't confirm.

The suspected "back-to-back meetings merged into one recording" problem does not exist: in every
case the meeting window kept one title for the whole recording.

**Bluedot's online archive vs the folder.** 939 recordings online; 104 never downloaded (the sync
stops at the first known recording, and a second recording with the same title on the same day
overwrites the first file; 37 such pairs online). Of the 104: 32 under 5 min, 51 covered ≥ 80% by
a Tomoe recording, 21 only online (most have a same-named file in the folder, i.e. the other copy
of a same-day pair or the Teams transcript).

Tomoe follow-up: its app log now stamps every line with the time (Tomoe 95a8ddc), so the next
miss can be lined up with the detector.

## 2026-11-05: Items that fail every passage stop the knowledge run

Setup: local only, `qwen3:8b` via Ollama for extraction, an experiment
dataset of about 800 email, chat and event items, `musubi knowledge`.

What happened: some passages got cut-off replies (`extract: model reply
isn't JSON: unexpected end of JSON input`; extraction asks for at most 700
tokens). Most items lost only that passage, as designed. But 11 small items
(8 emails of 2.8–5 KB, 2 chat days, one of them 994 characters, and 1 more
email) failed on every passage, and at temperature 0 they fail the same way
every run. The first such item became the run's error after extraction:
`knowledge: 612 items (1099 passages … 12.3s per passage), 2310 statements
(0 embedded …) claims 0 new … 0 topics`, exit 1. Embedding, linking, claims
and threads never ran for any item, and the next run retried the same items
and stopped again; forgetting one item only moved the stop to the next.

Hypothesis (not verified): a reply over 700 tokens for an input under 1 KB
suggests the model repeats itself until the token limit, so a higher limit
alone may not help.

Change: `knowledge.skip_failed_items` (default false, the earlier
behaviour). On, such an item is logged and left unstamped, the run goes on,
and the summary counts it; a run where no item extracts still fails.
Default off until it has been run on a real dataset.

## 2026-11-11: Decision models through the AI Gateway (labels)

Question: can statement labels move to the Optimizely AI Gateway, and
which decision model then? The labels sample (the 400 audited statements
of 2026-10-20) was rerun on 385 of them still in the dataset, with their
meeting titles, the production question (`labelQuestion`) and the same
scorer (the 2026-10-20 adjudications as truth). All runs the same day.

| At 0.5 | Recall | Precision | Missed | False flags | Unjudged disagreements |
|---|---|---|---|---|---|
| Jev via the gateway (jev-1.13.0) | 0.86 | 0.90 | 30 | 19 | 0 |
| Jev direct, `jev-latest` (jev-1.14.0) | 0.81 | 0.92 | 40 | 15 | 0 |
| Jev, October (same 385) | 0.86 | 0.91 | 30 | 18 | 0 |
| Clef | 0.75 | 0.92 | 51 | 14 | 4 |
| Clef Flash | 0.80 | 0.82 | 42 | 36 | 23 |
| Clef Omni (30B, 3B active) | 0.59 | 0.90 | 84 | 13 | 22 |

- TypeSafe's `jev-latest` moved to 1.14.0 and misses a third more
  sensitive statements at the same precision; the gateway's catalog still
  serves 1.13.0. Neither version is pinned, so this can change again.
- Clef is behind Jev at every threshold from 0.3 to 0.9; Flash finds about
  as many as Jev with twice the false flags (many unjudged, so that number
  is soft); Omni is furthest behind. About 12 s per 385 calls each.
- Cost per 385: Jev ~1¢, Clef ~5¢, Clef Flash ~2¢ (input only).
- Labels move to Jev through the gateway (`provider = "jev"`, catalog
  `url`), with `cf-aig-collect-log-payload: false` so the gateway logs
  hold no statement text.

## 2026-11-11: GLM-5.3-Flash vs Haiku 5.5 vs DeepSeek V4.1 Flash, one call at a time

Question: what replaces GLM-5.3-Flash on Cloudflare Workers AI (retired
2026-11-20), and why the gateway calls were slow. A first bake-off the same
morning ran four models at up to 16 calls at once with the 16k token cap and
mixed provider defaults; its times were not usable. Redone with one request
per call, no retries or hedging, no token cap, on Musubi's own prompts
(written out by a dump tool, then sent by one-off scripts): brief, one
extraction chunk, meeting summary, to-dos.

**Thinking levels Musubi sends.** Only the digest (summary, to-dos) and
thread assignment hardcode `reasoning_effort: low`; everything else sends
none, so the provider's default applies. Cloudflare's GLM-5.3-Flash
defaults to max, Baseten's thinks far less: on one extraction chunk,
20,141 output tokens (192 s) on Cloudflare vs 2,706 (19 s) on Baseten.
Moving from Baseten to the gateway therefore also raised extraction,
threading, status and Ask from Baseten's level to max; that, not serving
speed (105 vs 141 tokens/s), is most of the 12 s → 76 s extraction calls.

**Token cap.** The 16k floor cut 3 of 7 extraction calls on Cloudflare GLM;
without `max_tokens` the gateway returns full replies. Removed (this branch).

**DeepSeek V4.1 Flash** (Cloudflare, low): 120–145 tokens/s but about 80% of
its output is thinking even at low (brief 40 s, extraction chunk 31 s,
summary 47 s); its to-dos call failed twice with an empty to-do and once
with Workers AI's own timeout (HTTP 408, code 3046, at 235 s).
**GLM-4.7-Flash** (Cloudflare): 50–67 tokens/s; calls that think past about
5 minutes came back empty. Neither is a candidate.

**GLM on Baseten vs Haiku 5.5** (gateway → Vertex, adaptive thinking at
`low` and `medium`). Three meetings (a weekly 1:1, a vendor call, a VP staff
sync): extraction on two, summary and to-dos on three, 8 answers per setup.
Answers judged blind against the transcripts (labels shuffled; one reviewer
per meeting) by error type; major = would store a wrong fact or owner.

| Major / minor errors | GLM on Baseten | Haiku medium | Haiku low |
|---|---|---|---|
| Extraction (2) | 0 / 6 | 0 / 7 | 2 / 13 |
| Summary (3) | 5 / 15 | 8 / 26 | 11 / 18 |
| To-dos (3) | 2 / 10 | 2 / 8 | 3 / 10 |
| Total | 7 / 31 | 10 / 41 | 16 / 41 |
| Per call, mean | ~50 s, ~0.37¢ | ~27 s, ~0.50¢ | ~15 s, ~0.31¢ |

- Haiku low broke the extraction rule on context lines in all three
  meetings (6, 1 and 5 items from `[context]` lines) and misattributes most.
- Haiku medium equals Baseten GLM on extraction and to-dos, is a little
  worse on summaries (wrong owners); about twice as fast, a third dearer.
- All three credit to the owner some vendor lines the transcript labels as
  the owner's (the vendor call), and record a floated December message as
  agreed: transcript problems no model choice fixes.
- Prices per M tokens (gateway `pricing/model-pricing.csv`): Haiku 5.5
  $0.10 / $0.50, GLM-5.3-Flash $0.15 / $0.50, DeepSeek V4.1 Flash
  $0.30 / $1.20. Claude's tokenizer counts about 1.5x the input tokens.
- Musubi has no Anthropic-format client, so Haiku needs one before it
  can be used.

**The other jobs: GLM on Baseten vs Haiku medium.** Musubi's own requests,
captured through a pass-through recorder on a copy of the database (Ask:
3 questions; statuses: 3 threads of 18–66 statements; Live card brief:
3 cards; threading: assign, merge, two thread updates), then sent once
each to both models, one at a time, and judged blind as above.

| Major / minor errors | GLM on Baseten | Haiku medium |
|---|---|---|
| Ask (3) | 0 / 2 | 0 / 2 |
| Statuses (3) | 2 / 10 | 0 / 14 |
| Live brief (3) | 0 / 7 | 0 / 4 |
| Threading (4) | 0 / 6 | 0 / 3 |
| Total time, 13 calls | 574 s | 104 s |
| Total cost, 13 calls | 1.9¢ | 1.8¢ |

- Statuses: GLM invented a delivery that never happened and misattributed
  a person, and leaked item numbers into reader-facing text against the
  prompt; Haiku's errors were framing ("from X to Y") and inferred owners.
- Live brief: GLM went over the 18-word headline twice, Haiku once; Haiku's
  lines were more often what to raise now. Ask: even.
- Threading: no major errors either way; Haiku won assign and one thread
  update, GLM the other, merge identical.
- Baseten's speed swung from 4 to 117 tokens/s (one brief 136 s, one
  thread update 253 s); Haiku held 155–240.
- Musubi hedges threading calls: 5 of 14 were sent twice.

## 2026-11-11: tokens per meeting hour, by job (for Settings' cost lines)

From the Haiku 5.5 runs above (medium thinking, Claude's tokenizer) on the
three meetings, 22, 34 and 63 minutes (1.97 h; 422 statements; 30 threads
touched). Output is the visible answer only: thinking isn't counted, and
some models spend many times more on it.

| Job | In / meeting hour | Out / meeting hour |
|---|---|---|
| Extraction (brief + chunks; one meeting, full run) | ~118K | ~28K |
| Threading (assign, merge, thread updates; one meeting) | ~137K | ~2K |
| Statuses (~15 threads an hour, ~6.4K in, ~0.9K out each) | ~96K | ~14K |
| Meeting summary + to-dos (three meetings) | ~49K | ~5K |
| Labels (Jev, ~214 statements an hour) | ~32K | — |
| Ask (per question) | ~7K | ~0.3K |

Settings groups them as extraction and threading ~255K in / 30K out,
statuses and summaries ~145K / 19K: with Haiku 5.5 about $0.04 and $0.02 a
meeting hour. One-meeting figures (extraction, threading) are rough.
