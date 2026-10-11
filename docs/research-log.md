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
