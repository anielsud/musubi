# Musubi: overview

Start here. This is the thesis, how we work, the system at a glance, where it
stands, and what comes next, with pointers to the detailed documents. Written
2026-10-21; the numbers come from [research-log.md](research-log.md), which
has the dates and methods.

## Thesis

People in an organisation carry its working knowledge in their heads: where
each subject stands, what was decided and why, who owes what, which ideas are
landing, and what was learned the hard way. Almost none of it is written down,
and what is written goes stale. Meetings are where most of it is said.

Musubi (結び, "tying together") turns what is said into a memory that is
current, cited and scoped, and puts the right part of it in front of you when
it matters: before a meeting, during it, and when you ask.

What that takes:

1. **State over time, not facts.** A subject has a current position, a history
   of how it got there, open questions and commitments. "Where are we on X?"
   needs the latest word and what it replaced, not every sentence that
   mentioned X.
2. **Learned, not curated.** Associations, approaches, narratives and lessons
   come from the conversations themselves, not from a catalogue or a
   rulebook. Inferences are shown as cited precedents, never as verdicts.
3. **Wrong is worse than missing.** A misattributed position or an outdated
   figure shown as current poisons everything built on it. Precision,
   provenance and the owner's corrections come before coverage.
4. **Right time, right amount.** The value is in what is surfaced at the
   moment it is useful, in a form readable at a glance.
5. **Productizable.** It has to work from what every company has: calendar
   invites and meeting transcripts. No directory lookups, no per-company
   rules. It has to be cheap enough per meeting for a thousand meetings a
   person and a whole company.

The owner's priorities, in order: the most value soonest ("minimum viable
area under the curve"), then the right data at the right time, then low
cost, then universality. Cost cuts are taken only when quality holds.

## Why this shape

The design makes one bet: a memory of conversation should be built from
self-contained statements (a point that stands on its own, with its speaker,
subject and time), reconstructed along several dimensions (person, customer,
project, topic, thread), with time as a dimension the graph reasons over
rather than a label on a meeting.

**Why time cannot be a snapshot.** Code has an authority for "current": the
file as it is now, with git holding the history. A snapshot of a codebase is
cheap to rebuild and correct by construction. Conversation has no such
source. The only record is the order in which things were said, so what is
current has to be derived from how later statements relate to earlier ones:
corrections, supersessions, reversals, lapses. A design that stores each
meeting's topics and decisions as a container, and links meetings by subject
or by series, can answer "what was discussed about X" but cannot say whether
a decision still stands.

**What each design answers.**

| Question | Meeting-level (summaries, decisions per meeting) | Statements over time |
|---|---|---|
| What was discussed about X? | Yes, directly | Yes |
| Where does X stand now? | Only by reading every meeting about it | Yes: the latest word and what it replaced |
| What changed since a given date? | No | Yes |
| What was superseded or dropped? | No | Yes |
| Who owes what, and is it still open? | Per meeting | Across meetings |
| Is a figure shown as current actually current? | Not tracked | Tracked, with provenance |

**What it costs.** Extraction, threading and status derivation take several
passes per meeting where a meeting summary takes one. This is why cost is
managed by model placement and by running each stage once (see Approach and
the model table below). Statements also lose context when written too small:
the first atomic form lost its referents, so statements are written per few
passages with a brief of the meeting (2026-10-11).

**What it does not do.** It has no model of code structure, and it does not
replace a meeting summary where one is all that is needed.

**Status of the claim.** The pieces are measured: threads beat pairwise
linking, and "current" derived from relations fixed part of the audited
missed updates (see Research highlights). The overall claim, that this
beats a meeting-level design on state questions, has not been tested head to
head. The test is the same meetings indexed both ways and answered against a
fixed set of status, change, supersession and commitment questions, graded
blind. Until it runs, treat the claim as a hypothesis.

## Approach

- **Measure before changing defaults.** Every change is checked on real
  meetings, and the finding, with numbers and the date, goes into the
  research log. New behaviour arrives behind a config key that restores
  the old one.
- **Fix, then validate.** A presumptive fix is checked on the problem
  cases, then by one regression run; we don't sweep unrelated data.
- **Blind grading.** Where quality is a judgement (answers, statuses,
  hints), two Opus graders judge blind against references or the
  conversation, and we report both.
- **Replay instead of waiting for meetings.** Past meetings are replayed
  through the live path; the stepped replay makes this independent of
  speed and repeatable.
- **Hand-picked real corpora, not sweeps.** Experiments run on named local
  datasets (`~/.config/musubi/datasets.toml`); the basis is "All": the
  leadership meetings of the year plus every September meeting (165).
- **Model placement by measurement.** Each step runs where it measured best
  for quality, then cost, then latency (below). The code's defaults stay
  local; hosted choices live in the dataset's config.

## The system at a glance

Details: [architecture.md](architecture.md).

```
 recorded meetings (Musubi's recorder or Tomoe), Microsoft 365 (calendar,
 Teams transcripts, meeting chats, directory), inbox drops (email, chat, CRM)
      │
      ▼
 raw store (immutable) → items → derive stages (stamped, re-runnable)
      │   classify (sensitivity) · chunk · embed · mentions/entities
      │   extract statements → label (sensitive categories)
      │   thread: place → reconcile → consolidate; relations derive "current"
      │   statuses: where each thread stands, what changed, what is open
      ▼
 SQLite (FTS5, sqlite-vec, graph tables), every read scoped by sensitivity
      │
      ├─ Live (during a meeting): the recorder's live feed → names, terms, model
      │  suggestions → lookups, topic cards, thread statuses → the panel
      ├─ Ask: threads the question is about (status + current items) + passages
      ├─ Prep: for an upcoming meeting's attendees: subjects, what's open, what to share
      └─ Explore: topic map, thread pages, corrections (rules), comparison
```

Key ideas:
- **Threads hold state.** Statements are placed into threads; relations
  between statements (supersedes, answers, completes, repeats) derive which
  items are current. A thread's status is written from its current items
  and recent history.
- **Local operations.** Insert, forget and correct mend the graph locally
  instead of replaying everything; the owner's merges, splits and
  corrections are kept to the side and re-applied after a rebuild.
- **Sensitivity is in the data model.** Every row carries a level and
  categories; every read takes a scope; unknown fails closed. A
  screen-safe toggle narrows the live panel.
- **Corrections are rules.** A misheard or misspelled name is decided once
  (always, these uses only, or both are right) and applied wherever text
  is read or written from; nothing stored is rewritten.

Where each step runs (dataset "All", 2026-10-21):

| Step | Model | Why |
|---|---|---|
| Extraction, threading, statuses, meeting digests, Ask, prep drafts | GLM-5.3-Flash (hosted, Baseten) | Ask beat the local 27B 17–4 and 18–5 (blind), 3 s vs 170 s an answer |
| Statement labels | Jev (TypeSafe, typed classifier) | 86% of sensitive statements found against 71%, same precision |
| Live relevance | Jev | ranks hints as graders do (AUC 0.88), ~100 ms a call |
| Live suggestions and summaries | qwen3:8b (local) | latency (0.6 s vs ~1.4 s hosted median) |
| Embeddings | nomic-embed-text (local) | |
| Ask fallback | Qwen3.8-27B (local, on demand) | |

Jev is cleared for testing, not for the product; the product path is the
same classifier interface over GLM or the local model.

Since 2026-11-11 the hosted chat steps (and Live briefs) run on Claude
Haiku 5.5 through the AI Gateway, with GLM-5.3-Flash on Baseten as
fallback ([setup.md](setup.md), "Use recommended").

What it costs (measured; [setup.md](setup.md) has the breakdown): about
4–6¢ a meeting to build the memory (extraction ~1¢, threading ~2.7¢,
labels and statuses under 1¢), about $0.001 a question, and, as built,
about 13¢ a meeting hour for Live relevance, the cost to cut next.

## Where it stands (2026-10-21)

Built and in daily use: ingest, scoped search, the knowledge layer with
threads and statuses, Ask, Prep, Explore with corrections, and Live with
focused, relevance-chosen hints. The "All" dataset holds 165 meetings,
about 20,900 statements and 690 threads.

Known gaps:
- **Live relevance** is validated on one meeting; a second is being graded.
- **Reconcile misses** inside threads are the larger share of stale
  "current" items (thread fragmentation turned out to be small).
- **Backfill** of the rest of the corpus is designed (thread once, oldest
  first) but not run.
- **Email and chat** arrive only as inbox drops; no connectors.
- **The corpus relabel** with Jev is deferred; new statements are labelled
  with it.

Since then (2026-11-12): Musubi has its own meeting recorder and its
screens (recording, voices, speakers, settings), which can stand in for
Tomoe (`[recorder] of_record`, default still Tomoe;
[recorder-screens.md](recorder-screens.md)); Microsoft 365 through Graph
gives calendar, Teams transcripts, meeting chats and the directory, with
mail and chats built but off by default ([teams-design.md](teams-design.md));
`musubi mcp` gives Claude Code the knowledge base
([mcp-design.md](mcp-design.md)); and the store reads faster (page cache,
memory-mapped reads, a read pool) and keeps vectors as float16
(architecture.md, "Storage").

## Roadmap

In order of value now:

1. **Usable hints in a meeting.**
   - Validate relevance on a second meeting and settle the bar.
   - Step 2: hints that answer the moment. When someone asks a question,
     states a figure or makes a commitment, write one cited line that
     answers or checks it.
   - Bring the product path (GLM, or the local model's yes/no) to Jev's
     quality on the graded set.
   - Measure end-to-end latency in live meetings.
   - Cut Live relevance's cost (~13¢ a meeting hour as built): judge only
     what changed, and re-check what is shown less often.
2. **Correct current state.** Reduce reconcile misses inside threads; keep
   the owner's corrections cheap (rules, merges, splits, statement edits).
3. **Coverage.** Backfill the corpus once, oldest first; relabel with the
   typed classifier and check the filtering end to end.
4. **Scale and cost** ([scaling.md](scaling.md) §8): reads at scale (B2),
   then the remaining low-priority cost items; process shared meetings once
   when there is more than one person's data.
5. **Long-history quality** (scaling.md §8 C1–C2): a long real history,
   referenced and scored on duplicates, purity over time and update recall
   across long gaps; merge, split and dormancy.
6. **New sources.** Email and chat: triage, conversation-level extraction,
   their references.

Parked by the owner: recogniser vocabulary hints in Tomoe (promising, list
coverage open), feeding confirmed spellings to the recogniser.

## Research highlights

Each with its research-log entry. Methods and caveats are there.

**Extraction and models**
- Atomic statements lost their referents and context; points written per
  few passages, with a brief of the meeting, stand on their own
  (2026-10-11). Local extraction on a 27B takes ~12 min a meeting; hosted
  GLM-5.3-Flash ~2.5¢ (2026-10-12/13). An Opus audit of GLM's extraction
  found 91.6% correct and 4.9% wrong or invented, mostly figures
  (2026-10-13).
- More context for extraction gave no gain (2026-10-16).

**Threads and current state**
- Threads beat pairwise linking on the benchmark: grouping B-cubed
  precision/recall 0.93/0.48 against 0.73/0.21, and 0.5% of sampled pairs
  wrongly retired against 22% (2026-10-14).
- The first backfill showed the memory didn't hold up over time; threads
  t4 fixed part of the audited misses (up to 25 of 71 missed updates and
  31 of 108 stale items, without retiring more live ones) (2026-10-16/17).
- 91% of "current" is explained by stored relations; current is now
  derived from them (2026-10-19).
- Fragmentation is small: of the labelled retirements, 1 in 29 spans two
  threads; misses are reconcile misses (2026-10-20).
- Threading speed: overlapping consolidation with the next item, backoff,
  hedging stalled streams; the slow tail was stalled streams (2026-10-20).

**Ask and Prep**
- Ask from threads beat passages alone 22–5 (2026-10-17); on GLM it beat
  the local 27B 17–4 and 18–5, at 3 s a question (2026-10-20).
- Reading a later thread first: fewer wrong statements, fewer facts; not
  adopted (2026-10-20).
- Statuses stopped leaking the writer's working ("marked RETIRED…"): 175 of
  690 statuses to 5, for $0.58 (2026-10-20).

**Corrections**
- Rules reach what is shown and written: a surname rule took its threads'
  statuses from 7 mentions of the variant to 0 (2026-10-21).

**Live**
- The stepped replay reproduces the real-time one (359 lookups an hour vs
  353) and repeats itself (2026-10-21).
- Focus cut the panel from 13,500 characters to 2,600 and made cards
  readable (clear 2% → 65%), but not more relevant (2026-10-21).
- Choosing by relevance (Jev) instead of recency: 2.45 helpful items on
  screen a moment instead of 1.0; moments with nothing helpful 7 → 1 of 20
  (offline, 2026-10-21).

## Documents

| Document | What it covers |
|---|---|
| [setup.md](setup.md) | Install, keys, hosted models, building a dataset, and what processing costs |
| [architecture.md](architecture.md) | The design as built: stages, knowledge layer, storage, sensitivity, app |
| [incremental-design.md](incremental-design.md) | The subject graph, local operations, provenance, entities, aliases and rules |
| [scaling.md](scaling.md) | Growth to tens of thousands of meetings; cost; order of work |
| [insights-direction.md](insights-direction.md) | What knowledge to learn and surface |
| [topic-cards.md](topic-cards.md) | Cards in the live panel |
| [evaluation.md](evaluation.md), [insights-eval.md](insights-eval.md), [linking-benchmark.md](linking-benchmark.md) | How quality is measured |
| [local-models-2026-10.md](local-models-2026-10.md) | Local model measurements |
| [tomoe-live-feed.md](tomoe-live-feed.md) | The live feed protocol from Tomoe (and Musubi's recorder) |
| [recorder-screens.md](recorder-screens.md) | Musubi's own recorder in the app: recording, voices, speakers, settings, standalone |
| [recorder-parity.md](recorder-parity.md) | The recorder checked against Tomoe; installing it |
| [recorder-provenance.md](recorder-provenance.md) | Which recorder code came from Tomoe, and the rules |
| [teams-design.md](teams-design.md) | Microsoft 365 through Graph: calendar, Teams transcripts, chats, directory |
| [mcp-design.md](mcp-design.md) | `musubi mcp`: the knowledge base for Claude Code |
| [research-log.md](research-log.md) | Every finding, dated, with numbers |

Related: Tomoe (the transcriber, a fork of an open-source project) carries
its own research on transcription and speaker attribution in its `docs/`.
