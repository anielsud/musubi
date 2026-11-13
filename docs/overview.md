# Musubi: overview

Start here. This is the thesis, how we work, the system at a glance, where it
stands, and what comes next, with pointers to the detailed documents. Written
2026-10-21, brought up to date 2026-11-12; the numbers come from
[research-log.md](research-log.md), which has the dates and methods. To use
the app, see the [user guide](user-guide.md).

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
   invites and meeting audio or transcripts. The directory helps when it is
   there (names and teams) but nothing depends on it, and there are no
   per-company rules. It has to be cheap enough per meeting for a thousand
   meetings a person and a whole company.

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
  leadership meetings of the year plus every meeting since September, and
  since 2026-11-09 the year's other meetings for search.
- **Model placement by measurement.** Each step runs where it measured best
  for quality, then cost, then latency (below). The code's defaults stay
  local; hosted choices live in the dataset's config.

## Who said it: speaker labels

A statement is only as good as its speaker: "we'll cut the price" means
something different from the customer than from us. Most meeting tools
take speakers from the platform, which knows accounts, not voices: a
meeting room's microphone or a dial-in is one "speaker" however many
people are behind it, and a recording made outside the platform has no
names at all. Musubi records on the owner's Mac and names speakers
itself ([recorder-screens.md](recorder-screens.md)):

- **Diarization on the device.** Voices are told apart as the meeting
  goes (regrouped every 10 s), and settled when it ends.
- **The meeting window.** About once a second the recorder looks at the
  Teams or Zoom window: who is highlighted as speaking, and their name
  (read on the device; Zoom's from its accessibility information). A
  misread name snaps to the closest invitee's spelling.
- **Voice profiles** (on by default). Fingerprints, never audio, learnt
  from moments when a person's own tile was lit or the owner named them;
  they name voices the window can't: people in a room, on the phone, or
  in a meeting with no window. On saved meetings: 112 of 119 voices
  named, all correctly; 1 of 57 voices without a profile misnamed.
- **Meeting rooms** (on by default): a room's audio is split into its
  people; on a labelled room meeting, speakers right went from 94.7% to
  98.1%.
- **The owner's own microphone** is "you", except stretches that sound
  like someone else beside them.
- **The owner's word wins.** Renaming a speaker teaches the profile;
  renaming a profile relabels its past meetings without a model call,
  with undo.

Against Tomoe on a live 19-minute Teams call, the recorder's text
differed by 3.6% of words and gave the same speaker for 100% of 2,862
words ([recorder-parity.md](recorder-parity.md), 2026-11-12). When a
meeting's Teams transcript arrives, it names the recording's speakers
nobody named yet, if its text lines up with the recording and one name
clearly covers each speaker (`[m365] teams_names`); and in meetings
known only from a Teams transcript, its speakers count as the meeting's
people.

## The system at a glance

Details: [architecture.md](architecture.md).

```
 meetings: Musubi's recorder (or Tomoe) — audio → text → speakers named
 Microsoft 365: calendar, Teams transcripts and meeting chats, directory
   (mail and Teams chats built, off by default)
 inbox drops: events, email, chat, meetings as JSON
      │
      ▼
 raw store (immutable) → items → derive stages (stamped, re-runnable)
      │   classify (sensitivity) · chunk · embed · mentions/entities
      │   extract statements → label (sensitive categories)
      │   thread: place → reconcile → consolidate; relations derive "current"
      │   statuses: where each thread stands, what changed, what is open
      │   meeting summaries and to-dos
      ▼
 SQLite (FTS5, sqlite-vec, graph tables), every read scoped by sensitivity
      │
      ├─ Live (during a meeting): the recorder's live feed → names, terms,
      │  relevance → lookups, topic cards with briefs, the meeting's prep card
      ├─ Ask: threads the question is about (status + current items) + passages
      ├─ Meetings, To-Do, Prep: summaries, commitments and their state, what to bring
      ├─ Explore: topics, people, voices, search, corrections
      └─ musubi mcp: the same scoped reads for Claude Code, labelled by sensitivity
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
- **Upgrades keep data.** A database is copied before a new version
  changes it, and a version refuses a database newer than it
  ([upgrades.md](upgrades.md), [data-safety.md](data-safety.md)).

Where each step runs ("Use recommended", 2026-11-12):

| Step | Model | Why |
|---|---|---|
| Extraction, threading, statuses, summaries, Ask, Live briefs | Claude Haiku 5.5 through the AI Gateway; GLM-5.3-Flash on Baseten as fallback | Haiku: about 27 s and 0.50¢ a call against GLM's 50 s and 0.37¢ (2026-11-11); before it, GLM beat the local 27B on Ask 17–4 and 18–5, blind |
| Statement labels, Live relevance | Jev (typed classifier, through the gateway) | 86% of sensitive statements found against 71%, same precision; relevance ranked as graders do (AUC 0.88), 0.13 s a call |
| Live suggestions | qwen3:8b (local) | latency: first token in 0.4–0.7 s |
| Embeddings | nomic-embed-text (local) | |
| Ask fallback | Qwen3.8-27B (local, on demand) | |
| Transcription, speakers | on the device, in the recorder (sherpa-onnx, ONNX Runtime) | nothing recorded leaves the Mac |

Without "Use recommended", every model is local (Ollama). Jev is cleared
for testing, not for the product; the product path is the same
classifier interface over a chat model's yes/no.

What it costs (measured, Haiku, thinking not counted): about $0.04 a
meeting hour to read a meeting into threads, $0.02 for statuses and
summaries, under 1¢ a question (2026-11-11). With GLM before it: 4–6¢ a
meeting to build, and about 13¢ a meeting hour for Live relevance as
built ([setup.md](setup.md) §8).

## Where it stands (2026-11-12)

Built and in daily use:
- **Recording** with Musubi's own recorder, standalone from Tomoe:
  speakers named on the device, voice profiles, a watchdog that restarts
  it.
- **Microsoft 365**: calendar, Teams transcripts (also of meetings the
  recorder missed, stitched into the recording's gaps), meeting chats and
  the directory.
- **The knowledge layer**: statements, threads and statuses, meeting
  summaries and to-dos, Ask, Prep, Explore with corrections, and Live with
  relevance-chosen hints and briefs.
- **Claude Code** reads the knowledge base through `musubi mcp`.
- **Installable by others**: a DMG built on GitHub, with the recorder
  inside, a six-week first sync, and upgrades that copy the database
  first.

The "All" dataset holds about 1,300 meetings (about 390 read into
threads, the rest searchable), about 39,800 statements and 1,240
threads; the store's reads got 3–10 times faster on 2026-11-12, and
vectors take half the space.

Known gaps:
- **Teams names on recordings** are new (2026-11-12): how many speakers
  they name, and how often wrongly, isn't measured yet.
- **Reconcile misses** inside threads are the larger share of stale
  "current" items.
- **Backfill**: the year before September is searchable but not threaded.
- **Mail and Teams chats** are built but off until their extraction is
  tried on real messages.
- **The corpus relabel** with Jev is deferred; new statements are labelled
  with it.
- **Data safety** for others' datasets: backups that cover the recorder's
  folder, automatic snapshots and restore are built; the rest is
  proposed ([data-safety.md](data-safety.md)).

## Roadmap

In order of value now:

1. **Safe to give to others.** The data-safety items, in their order
   (backups of everything, restore, release discipline, count checks);
   a first run on a clean Mac.
2. **Usable hints in a meeting.**
   - Step 2: hints that answer the moment. When someone asks a question,
     states a figure or makes a commitment, write one cited line that
     answers or checks it.
   - Bring the product path (a chat model's yes/no) to Jev's quality on
     the graded set.
   - Measure end-to-end latency in live meetings (replays: 1.9 and 3.5 s
     from words said to lookup shown).
   - Cut Live relevance's cost: judge only what changed, and re-check what
     is shown less often.
3. **Who said it.** Measure Teams names on recordings, voice profiles and
   room splitting on more meetings (all on by default since 2026-11-12).
4. **Correct current state.** Reduce reconcile misses inside threads; keep
   the owner's corrections cheap (rules, merges, splits, statement edits).
5. **Coverage.** Thread the rest of the year once, oldest first; relabel
   with the typed classifier and check the filtering end to end.
6. **Scale and cost** ([scaling.md](scaling.md) §8), and **long-history
   quality** (§8 C1–C2).
7. **Mail and chat.** Triage, conversation-level extraction, their
   references.

Parked by the owner: recogniser vocabulary hints, feeding confirmed
spellings to the recogniser.

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

**Recorder and speakers**
- The recorder's output matched Tomoe's byte for byte on four replayed
  meetings, and live beside it differed by 3.6% of words with the same
  speaker for 100% of words (2026-11-10/12).
- Voice profiles named 112 of 119 voices on saved meetings, all
  correctly; splitting a meeting room took speakers right from 94.7% to
  98.1% on a labelled room meeting.

**Store**
- Memory-mapped reads, a read pool and a read cache: the most-mentioned
  entity's graph 42.7 s → 14.0 s first time; 20 cards 10.0 s → 0.97 s; a
  read during a backup 2.3 s → 78 ms. Float16 vectors: the file 763 → 421
  MB, search results unchanged (2026-11-12).

**Live**
- Words said to lookup shown: median 1.9 s and 3.5 s on two replayed
  meetings (2026-10-29).
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
| [user-guide.md](user-guide.md) | Installing and using the app, with screenshots |
| [upgrades.md](upgrades.md) | Upgrading a dataset; moving over from Tomoe |
| [data-safety.md](data-safety.md) | Keeping others' data through upgrades (proposed) |
| [setup.md](setup.md) | Install from source, keys, hosted models, building a dataset, and what processing costs |
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
