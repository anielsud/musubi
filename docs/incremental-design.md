# Incremental knowledge: working locally on an evolving graph

Status: proposal (2026-10-19). Decided so far: "current" is to be derived
from the relations between statements, not carried forward by replay.
The rest is the direction this implies, and the order to build it in.

## The problem

The knowledge layer is built by stages, and most of them already work one
item at a time with input hashes: ingest, chunk, classify, embed, extract,
the statement labels, and thread statuses. Threading does not. It is a
replay:
- each meeting's statements are matched against the threads as they stood
  on that date;
- which statements are current is state carried forward from one meeting
  to the next.

So any change to the past costs a replay from that point to the present:
- a corrected statement, a re-extracted meeting;
- an older meeting added (a backfill), a meeting deleted.

The cost is about $0.03 a meeting on the hosted model, and four hours for
165 meetings. It grows with history, and corrections should be routine,
not a full rebuild.

Smaller versions of the same problem:

| Stage | What breaks locality |
|---|---|
| Points extraction | reads a facts sheet built from other meetings: a registry change re-extracts broadly |
| Claims | a rules change rebuilds every claim |
| Thread links, centroids, clusters, topic map | recomputed for the whole graph each time |
| Card index, thread finder | rebuilt whole on every change |
| Run planning | scans every item |

## The model: a subject graph

The core object is a graph that grows as meetings arrive.

Nodes:
- statements (with their sources);
- threads (subjects);
- entities (people, companies, projects, places);
- meetings and other items.

Edges:
- statement → thread (belongs to);
- statement → statement (replaces, completes, answers; and agrees,
  contradicts if wanted later);
- statement → entity (said by, about, mentions);
- entity → meeting (attended);
- thread → thread (related, split from, merged into).

Everything shown is a view over the graph, computed or cached with the
inputs it was computed from:
- **Current.** A statement is current if nothing later in its thread
  replaces, completes or answers it, and it hasn't lapsed. No state is
  carried forward.
- **Statuses** are cached views of a thread's current items and recent
  changes; they work this way already (`thread_status.input_hash`).
- **Cards, the topic map, clusters and Prep** are views over threads,
  entities and their edges.

## Provenance and authority

Every node and edge records how it came to be:
- created by a model, a rule, or the owner;
- which stage and version;
- from which inputs.

Authority decides conflicts: the owner over a model, and a model over a
rule. Corrections, judgements (verified, disputed, false) and "these two
are the same subject" become graph edits that override model edges and
spread locally, not side tables.

## Invalidation: a dependency graph

Each derived row lists what it was built from: statement ids, an entity's
aliases, a stage version. A change marks what depends on it as stale, and
only that is recomputed, as in a build system. The status input hash is a
small version of this. The same mechanism covers:
- corrections and re-extraction;
- labels raised (statuses already take raised labels without a rewrite);
- deletions;
- a stage version bump, migrated gradually rather than all at once.

## Two kinds of time

Each fact has the time it was said (valid time) and the time the system
came to know it (processing time). Backfills and corrections pull these
apart. Keeping both gives two things without reprocessing:
- **replay:** "what was known at this meeting", which Live already needs;
- **audit:** "what did the system believe on 1 October".

## Local operations

Each change to the graph is a named operation with a defined reach:

| Operation | Touches |
|---|---|
| Insert a statement (a new meeting, or a backfill) | assign it to threads by name, scope and nearest items (time barely matters); judge it against its thread's neighbours in time, both earlier and later; re-derive "current" for that thread |
| Correct text or speaker | re-embed; re-judge its edges in its threads; re-derive; refresh affected statuses |
| Delete | drop its edges; re-derive its threads; refresh statuses (real forgetting) |
| Split a thread | when it grows past a bound, or keeps receiving unrelated items |
| Merge threads | when two keep receiving the same items, or a born thread matches an older one |
| Rename, retire | dormant threads leave the candidate sets |

A statement in one thread can retire one in another (cross-thread
retirement, t4). The neighbourhood for that is bounded by similarity to
the changed statement, not by time.

## Vocabulary and entities

Misheard names ("DAKA" for Dhaka), codenames, aliases and mixed-up speaker
labels are one problem. Aliases should be learned from the corpus, not
hand-curated:
- the same name in calendars and email;
- spellings that recur;
- an entity's neighbourhood in the graph.

The owner confirms them. A confirmed alias:
- feeds extraction, search and display;
- spreads as a correction through the dependency graph;
- doesn't change thread structure, because membership is by statement
  id.

## Work queue

A background queue replaces batch runs:
- **Events:** a new item, a correction, a deletion, a version bump.
- **Jobs:** each event is turned into the jobs that touch only what
  depends on it.
- **Priority:** recent and often-read subjects first, old backlog last.
- **Budget:** a cost and time budget for hosted calls.

The app's update then only adds events. A full run becomes an occasional
maintenance job.

## Consistency sweeps and equivalence tests

Local updates drift, so sweeps measure it:
- a sample of threads is rebuilt from scratch (a full replay of one
  thread), or audited by a model;
- the result is compared with the incremental graph.

Every local operation also gets a test: applying a change locally should
give nearly what a rebuild gives, on synthetic data and on a sample of
real data. This is what makes the incremental path safe.

## Consequences

- **Deletion and forgetting** follow from operations plus provenance.
- **Multi-user, later:** a shared item processed once fits naturally.
  Shared nodes carry provenance, and each person sees them through their
  scope.
- **Speed:** the work is proportional to the change, not the history.

## Order

1. **Check whether stored relations already explain "current".** On the
   ELT year, derive "current" from the stored relations and compare it
   with the stored current flags. Agreement means the relation model
   already holds; disagreement shows where carried-forward state matters.
2. **Thread operations:** insert, correct and delete on relation-derived
   "current", with equivalence tests; then backfill by insertion.
3. **Provenance on edges and the dependency graph,** owner edits
   included.
4. **The work queue,** replacing `knowledge` runs in the app.
5. **The vocabulary and alias layer,** with corrections on top.
6. **Two kinds of time.**
