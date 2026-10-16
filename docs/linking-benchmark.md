# Linking benchmark

How we measure whether Musubi groups and links knowledge correctly: the
layer between extracted items and what people see (cards, search,
answers). Started 2026-10-14, after a programme card worked and an event
card didn't (a conference's anecdotes clustered with an unrelated
marketing workstream because both mentioned events). Results go in
`research-log.md`. Tools are in `tools/link-bench/`. Data stays in
`testdata/private/link-bench/` and is never committed.

## What can go wrong

Failures found by hand so far, by layer. The benchmark must catch each.

| Layer | Failure | Seen as |
|---|---|---|
| Relations | a point said in the same minute "supersedes" another | live items hidden |
| Relations | a promise to find out "answers" the question | open question closed |
| Relations | one owner's commitment replaces another's | commitment hidden |
| Relations | an update in a later meeting never compared (not a candidate) | stale state shown as current |
| Claims | repeats across meetings never join (35 of 1,014) | no corroboration |
| Topics | a catch-all of 118 claims | every card or search near it is noise |
| Topics | two subjects merged on a shared word ("events") | wrong items on a card |
| Topics | one subject spread over 4–16 topics | incomplete cards |
| Cards | a term mapped to a catch-all; companions inherited from it | 60-topic card, collapsed |
| Search | vector results pad a rare name's few exact hits | "odd findings" |

## Reference

Built by Opus sub-agents from the extracted items (and, where in doubt,
the transcripts), as for the extraction reference (`insights-eval.md`).

1. **Pairs.** A stratified sample of statement pairs, labelled blind
   (the linker's verdict is hidden, strata are shuffled):
   - every relation the linker made of the rare kinds (supersedes,
     answers, pending, contradicts, same) and a sample of refines;
   - pairs it judged unrelated, at high (≥ 0.80) and middling
     (0.65–0.80) similarity;
   - pairs it never judged: across meetings, 0.55–0.80 similar, sharing a
     rare capitalised word or a figure, where missed updates hide.

   Each gets a relation (same, refines, supersedes, completes, answers,
   pending, contradicts, same_subject, unrelated), whether the earlier
   item stops being current, a confidence and a note.
2. **Threads.** Fifteen subjects across the four meetings, of different
   shapes: a confidential programme, a metric that changes weekly, a
   commercial rule set, a deal, a departure, a product bet, an analysis,
   a capacity problem, launches, adoption, internal spend, onboarding, a
   transformation programme, and an event people tell stories from. Each
   has:
   - every member item (exhaustive), and near misses that look related
     but aren't;
   - its state after each meeting, as the items that state it;
   - every update: a later item that supersedes, completes, answers,
     corrects or contradicts an earlier one, or promises to find out;
   - what is still open at the end.

## What is scored

A system's output is two things over statement ids, whatever produced
it: groups (which statements form a claim, which claims a topic or
thread) and relations (later statement, earlier statement, relation).
`tools/link-bench/system_from_db.py` reads them from an instance;
alternatives write the same shape.

- **Relations (pairs).** Agreement per relation, and above all:
  - **false retire**: the system retires an item the reference keeps
    current (supersedes, answers, completes where the reference says
    not). This hides live items, the worst failure;
  - **missed retire**: the reference retires, the system doesn't (stale
    state shown);
  - **connected**: the reference relates the pair in any way (including
    same_subject); does the system connect them (a relation, the same
    claim, or the same topic)?
- **Updates (threads).** Recall of the reference's updates: is there a
  system relation between the two items, and does it retire when the
  reference retires?
- **Grouping (threads).** For the statements in reference threads,
  B-cubed precision and recall of the system's topics against the
  threads; fragmentation (topics needed to hold 80% of a thread); and
  near-miss contamination (near misses that share a topic with members).
- **State.** After the last meeting, the reference's current items that
  the system shows as retired, and retired items it shows as current.
- **Search and cards.** Per thread, a few queries a person would type or
  say; the share of the top results (passages, claims) and of card lines
  that are thread members.
- **Cost.** Model calls, tokens and wall time.

## Tiers

| Tier | Where | Size | Use |
|---|---|---|---|
| Synthetic | `testdata/synthetic/link-bench/` (committed; invented company; README lists the planted patterns) | 250 items, 12 threads, 142 pairs | every change; minutes, cents |
| Real, full | `testdata/private/link-bench/` | 1,014 items, 15 threads, 227 pairs | before a default changes; two runs each |
| Long replay | `testdata/private/link-bench-long/` (the four meetings five times, ids offset by 100000) | 5,070 items | prompt size and cost over a long history |

Two lessons on the tiers themselves (research log, 2026-10-16):
- **The synthetic set under-represents in-meeting updates** (20% of its
  updates against 64% of the real reference's), so a change that only
  affects in-meeting updates must be checked on real data.
- **A hand-picked real subset misleads.** Running a design on only the
  members and near misses of six subjects halved its update detection
  against running it on everything. A small real tier must be whole
  meetings.

**Noise.** Hosted runs sample (temperature 0.7) and vary by 5–10 points on
the update measures from run to run. A decision closer than that needs two
runs per variant (`CACHE_SALT` reruns the same prompts afresh).

## Running it

Export an instance's statements, the linker's verdicts and stratified
pairs (once per instance; the reference is labelled from this):
```
go run -tags sqlite_fts5 ./tools/link-bench/export -db <db> -log <knowledge -v log> -out testdata/private/link-bench
```

Score a Musubi instance (pairwise linker and topics, or Go threads), with
the relation rules applied afterwards if wanted:
```
python3 tools/link-bench/system_from_db.py <db> <name> <bench>/statements.jsonl > system-<name>.json
python3 tools/link-bench/guard.py <bench> system-<name>.json > system-<name>-rules.json
python3 tools/link-bench/score.py <bench> system-*.json
```

Run the thread prototypes (hosted through `tools/insights-eval/bt.py`;
spend is logged per call with the `RUN` tag, and dollars per run come
from that ledger, not from the printed total, which includes concurrent
runs):
```
RUN=base SPEND_CAP=25 MODEL=zai-org/GLM-5.3-Flash python3 tools/link-bench/threads.py <bench> system-base.json
RUN=lean SPEND_CAP=25 SKIP_NEW=0 INLINE=0 ASSIGN_EFFORT=low ASSIGN_MAX=8000 REC_MAX=4000 \
  python3 tools/link-bench/threads2.py <bench> <db with statement vectors, or - to embed via Ollama> system-lean.json
```
`threads2.py` options: `CAND` (40), `RECENT` (15), `CAP` (25), `CHUNK` (70),
`SKIP_NEW` and `INLINE` (both measured worse; set 0), `SPECIFIC`
(measured no effect), `ASSIGN_EFFORT` (low: measured cheaper, no loss).

Cards and search end to end: load a thread system into a copy of the
instance and score queries per subject:
```
python3 tools/link-bench/apply_threads.py system-lean.json <copy of db>
python3 tools/link-bench/retrieval.py <bench> <bench>/queries.json bin/musubi <config> <db>
```

## Results so far

Full real set; ranges are over repeated runs and variants (details and
every variant: research log, 2026-10-14 to 2026-10-16):

| | Pairwise 8B (k16) | Threads, original | Threads, lean (hosted) | Threads, lean (Go, local 27B) |
|---|---|---|---|---|
| Grouping, B-cubed P / R | 0.73 / 0.21 | 0.93–0.95 / 0.46–0.48 | 0.86–0.87 / 0.49–0.53 | 0.90 / 0.43 |
| Retiring updates retired | 25% | 24–32% | 29–35% | 21% |
| Sampled live items wrongly retired | 22% | 0.5–1.0% | 1.0–2.5% | 1.5% |
| Current items wrongly retired | 4.0% | 0.7–1.3% | 1.3–2.0% | 3.3% |
| Card lines on subject | 75% | 83% | | 84% (with phrase lookup) |
| Cost, four meetings | 46 min local | $0.125–0.152 | $0.074 (low-reasoning assign) to $0.093 | 34 min local |

Smaller pairwise judges: 4B wrongly retires far less than 8B (3.5%) but
finds fewer updates; 1.7B calls most pairs "same" and is unusable.

What the benchmark doesn't measure yet, and the plan for it: candidate
recall, coherence over a long history, and email and chat
([scaling.md](scaling.md), §5).
