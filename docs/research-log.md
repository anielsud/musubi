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
