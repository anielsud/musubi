# Musubi architecture

Musubi (結び, "tying together") is a local-first personal knowledge base. It
ingests meetings (from Tomoe), calendar, email, chat and CRM records, distills
them into people, accounts, topics, decisions and commitments linked to their
sources, and serves that knowledge two ways:

- **Live:** a side panel that surfaces relevant cards while a meeting is in
  progress, driven by Tomoe's live transcript.
- **Explore:** a graph, timeline, topic map and trend view across time, people,
  business area, customer and topic.

Status (2026-10-16):
- **Built:**
  - ingest (Tomoe sessions, inbox drops) and calendar linking;
  - classify / chunk / embed / resolve, with scoped search and graph;
  - the knowledge layer: statements or points, claims and belief, topics,
    and threads as the alternative grouping;
  - topic cards in the live panel;
  - the desktop app (live lane, Q&A, explorer);
  - eval harnesses for retrieval and leaks, knowledge, extraction
    ([insights-eval.md](insights-eval.md)) and linking
    ([linking-benchmark.md](linking-benchmark.md)).
- **Design only:** trends, signals, email and chat connectors (beyond inbox
  drops), and everything in [scaling.md](scaling.md) past today's
  measurements.

Findings: [research-log.md](research-log.md).

## Principles

1. **Local first.** Storage and default processing run on this machine. Cloud
   providers are pluggable and opt-in per source (see Sensitivity).
2. **Raw is immutable; everything else is rebuildable.** Every input is kept
   exactly as it arrived. All derived data can be regenerated from raw, so
   better extractors and models are applied by re-running, not migrating.
3. **Every derived row knows where it came from.** Provenance makes citations,
   reprocessing, deletion ("forget this thread") and access control possible.
4. **Sensitivity is part of the data model, and every read is scoped.** There
   is no unscoped query API.
5. **Measure before changing defaults.** Replay past meetings and score what the
   panel would have shown; record findings in `docs/`.
6. **New behavior gets a config switch** that restores the previous behavior.
7. **Private and proprietary.** No GPL/AGPL/LGPL dependencies; no code copied
   from Tomoe (GPLv3). Integration with Tomoe is through its files and its live
   stream only.

## Flow

```
 sources                   cold lane (batch, re-runnable)                 hot lane (live)
 ───────                   ──────────────────────────────                 ───────────────
 Tomoe sessions ─┐                                                        Tomoe live stream
 calendar        │  ┌───────────┐  ┌───────────┐  ┌────────────────────┐        │
 email           ├─▶│ raw store │─▶│ normalize │─▶│ derive stages       │        ▼
 Teams chat      │  │ immutable │  │ → items   │  │ classify → chunk →  │   spotter (names,
 Salesforce      │  └───────────┘  └───────────┘  │ embed → mentions →  │   terms) → scoped
 others ─────────┘                                │ resolve → facts →   │   retrieval → cards
                                                  │ edges               │        │
                                                  └─────────┬──────────┘        ▼
                                                            ▼               side panel
                                        periodic analysis: topics, clusters,
                                        trends, signals, centrality
```

### 1. Sources and the raw store

A source connector (`internal/source/<name>`) fetches new data since its last
cursor and writes it to the raw store unchanged: content-addressed blob
(`raw/<sha256>`) plus a manifest row (source, source ref, fetched at, cursor,
content type). Connectors do no interpretation.

Planned connectors, in order:

| Connector | Access |
|---|---|
| `tomoe` | reads `~/.local/share/tomoe/sessions/*/session.json` |
| `calendar` | ICS or Graph; gives meetings their titles and attendees |
| `mcpdrop` | an inbox directory that scheduled Claude Code jobs fill from the approved M365 / Salesforce / Zoom connectors, as JSON |
| `graph` | direct Microsoft Graph (mail, chat), once an Entra app registration is approved |

### 2. Items

A per-source translator turns raw records into **items**, the one shape all
later stages consume:

```
item: id, source, source_ref, raw_hash, kind (utterance|email|chat|event|record),
      occurred_at, thread, participants[], text, metadata{}, native_labels{}
```

`native_labels` keeps any sensitivity marking the source already carries
(Purview label, calendar "private" flag, Salesforce record type).

Adding a source never touches stages beyond the translator.

### 3. Derive stages

Each stage is a named, versioned processor. Every row it writes is stamped with
`(stage, stage_version, input_hash)`. A job queue in the database runs whatever
is stale; bumping a stage's version reprocesses exactly what it produced.

| Stage | Output |
|---|---|
| `classify` | sensitivity label for each item (see below) |
| `chunk` | retrieval passages (meeting turns grouped by topic shift; email bodies without quoted history) |
| `embed` | passage vectors (local ONNX embedding model by default) |
| `mentions` | spans naming people, organisations, products, projects, terms |
| `resolve` | mentions → canonical entities |
| `facts` | decisions, commitments (owner, due), claims; each with valid time and source spans |
| `edges` | typed, weighted, timestamped links between items, entities, facts and topics |

**Resolution is seeded from registries** (`internal/registry`): people from
the Entra directory (email is the canonical ID), customers from Salesforce
accounts, business areas from the org tree. Tomoe speaker names resolve to the
same people. Matching against known lists is far more reliable than discovering
entities from scratch; unmatched mentions become provisional entities for
review.

### 4. Periodic analysis

Reads derived data; never edits it.

- **Topics:** cluster passage vectors, label clusters, and track topic identity
  across runs so a topic keeps its ID as it drifts.
- **Trends:** weekly counts per entity and topic; burst detection; newly
  appearing entities.
- **Signals:** overdue commitments, customers going quiet or spiking, topics
  spreading across business areas.
- **Importance:** recency-weighted centrality on the graph.

**Calendar linking.** A Tomoe recording is linked to the calendar event
that contains its start (15 minutes' slack) and overlaps at least 30% of the
shorter of the two. All-day, longer-than-4-hour, free and cancelled entries
are never linked: a recording linked to a focus block would inherit the
block's tiny attendee list and look less sensitive than it is. The linked
event gives the meeting its title and attendees.

**Ambiguous names.** When a first name fits several people, the one who has
shared the most meetings with this item's participants wins, if the evidence
is clear (at least two shared meetings, twice the runner-up; the owner is left
out of the count). Otherwise the mention stays ambiguous with its candidates.

### 5. Hot lane

The live path never writes. Tomoe streams lines over an opt-in local socket (a
small Tomoe PR, off by default). A spotter matches names and terms against the
registry and entity tables, runs scoped retrieval, and ranks cards (who this is,
recent decisions, open commitments, related threads). Slow enrichment, if any,
arrives after the cards. After the meeting, Tomoe's saved session enters the
cold lane like any other source.

### Desktop app (as built)

`cmd/musubi-gui` is a Wails app (`internal/gui` backend, React frontend in
`frontend/`), installed with `make install-gui-mac`.

- **Live** (`internal/live`): follows Tomoe's feed ([tomoe-live-feed.md](tomoe-live-feed.md)),
  keeps the meeting so far, and every `suggest_every` seconds of new speech
  turns the last `window` seconds into suggestions from three sources:
  entities named (unambiguous aliases only), phrases that are rare in the
  knowledge base but present in it, and the local chat model. The top
  `auto_brief` suggestions are looked up (the meeting itself excluded) and
  summarised in two cited sentences; the model answers NONE when the results
  aren't about the topic and the brief is dropped. The title comes from the
  calendar event under way. When Tomoe saves the meeting, Musubi ingests it.
  A replay mode plays a saved meeting through the same path for testing.
- **Topic cards** (`live.topic_cards`): when a line says a topic's trigger
  term, or a lookup's nearest claims are on a topic, the panel shows that
  topic's card (see "Topic cards" below), rebuilt under the current scope
  whenever the scope changes. A replay sees only what was known when the
  replayed meeting happened.
- **Screen-safe**: a toggle switches the panel's scope to `shared-screen`.
  Detecting the owner's own screen share is not built (Tomoe can see a
  meeting app's sharing state but doesn't send it); restricted and HR cards
  are collapsed to their title meanwhile.
- **Ask** (`internal/ask`): scoped hybrid retrieval, then the local model
  answers citing excerpts; citations open the source under the same scope.
- **Explore**: passage search, entity lookup, and a radial "threads" graph of
  an entity's connections with its timeline.
- Only a local model ever reads meeting text; with a cloud model configured,
  Q&A filters passages to the cloud scope and live suggestions are off.

## Knowledge layer

Passages are what was said; knowledge is what it amounts to. Each source
mixes facts, opinions, hypotheses, decisions and commitments, held by
different people, with different authority, and they change over time:
reinforced, clarified, superseded, disproven, or simply going stale.

```
passages ─▶ statements ─▶ claims ◀── relations ──▶ claims ─▶ topics
            (who said what,  (one assertion across     (clusters of claims,
             of what kind)    sources; belief          related to each other)
                              computed when read)
```

### Statements (`extract` stage)

A local model reads each passage once and lists what it asserts. Each
statement has:
- **kind**: fact, opinion, hypothesis, decision, commitment, requirement, or
  question;
- **text**: self-contained, with names made explicit;
- **holder**: who said it, resolved to a person through the speaker labels;
- **certainty** as expressed, from 0 to 1;
- **said_at**;
- **value**: the date, amount, number, percentage or duration it turns on,
  as said ("Q4", "mid November"). It is normalised in code into bounds
  (Oct–Dec; Nov 10–20), so values compare deterministically: within,
  contains, same, or differs. "Mid November" within "Q4" refines;
  "22 Sep" against "29 Sep" differs.
- **line**: the passage line it came from, so it inherits that passage's
  sensitivity and can be cited exactly.

Small talk and procedure are skipped. The stage is versioned by model and
prompt like any other, so improving the prompt means re-running it.

Checks after extraction, all cheap and stored with the statement:
- **quote and grounding**: the statement carries the words it came from;
  code checks the quote is in the cited line, its figures are in the
  source, and its negation agrees ("three years instead of two" counts as
  the contrast "three years, not two"). Ungrounded statements weigh less
  and never join a claim without the judge.
- **worth**: one yes/no forward pass per statement ("work knowledge, or
  small talk, a joke taken literally, garble?"), read as a probability
  from the model's next token rather than generated. Statements below
  `knowledge.min_worth` (0.2) stay out of claims but are kept, so a
  different threshold re-links without re-extracting. On the pilot about
  9% scored below 0.2, nearly all noise.
- **search text**: the statement plus the digits of spelled-out numbers
  ("forty five days" is found by "45").

An optional passage gate (`knowledge.gate`, off) asks the same kind of
question per passage before extraction; on meetings it skipped too little to
pay (see the research log).

### Points (`knowledge.extract = "points"`, meetings)

Statements are taken one passage at a time, so they miss what the next
passage clarifies and lean on "it" and "the agreement". Points are the
meeting-level alternative, measured on the pilot at about 23.5 of 24 key
points and 1–2% wrong (Qwen3.8-27B thinking, on llama-server). Email and
chat stay on statements.

1. **Known facts**, written in code: the owner and their company, and each
   invitee's organisation by email domain. An address that isn't
   first.last is shown as it is, never turned into a guessed name.
2. **Brief**, one call without thinking: purpose, who's who (only as far
   as the transcript shows), subjects, and a glossary of vague references.
3. **Segments**, `points.segment` (5) passages per call,
   `points.parallel` (4) at once, with thinking (`points.think`). Each
   sees the known facts, the brief, its passages, and one passage either
   side marked `[context]`, from which it may complete a point but not
   take one. The prompt carries rules and two invented worked examples of
   past mistakes (expanded acronyms, a client's ask stated as practice, a
   relayed demand attributed to the relayer, a garbled line turned into a
   position).
4. **Checks in code**: every name and figure in a point must appear in the
   meeting or the known facts (not the brief, which is model-written), and
   each quote must be in the line it cites (or the one before). A failing
   point is kept, marked ungrounded with the reason. Post-hoc model
   verification was tried and caught at most half the errors (research
   log, 2026-10-12), so none runs here.
5. **Merge in code**: near-identical points (word overlap ≥ 0.8) from
   neighbouring segments are kept once. A model rollup was tried and lost
   decisions.

A point is stored as a statement, so claims, belief, topics and views
work unchanged. It adds a **status** (decided, agreed, proposed, open,
answered, rejected, stated), **who** it belongs to, and **evidence** (every
line it rests on; the first anchors it). Its kind is mapped onto the
statement kinds by status: a proposed decision is an opinion, an answered
question a fact. Points skip the worth score, which drops personal
details.

**Personal details are kept**, attributed to the person they are about,
and labelled `personal` (restricted): the owner sees them, nothing else
does. A point's own label also takes the most sensitive passage it rests
on and whatever its text matches in the sensitivity rules; reads combine
it with the passage label.

The extraction model is `knowledge.points.model` (provider `openai` for
llama-server; empty uses `[llm]`) and must be local. llama-server is run
with the measured settings: `--spec-type draft-mtp -np 4 -c 98304
--cache-type-k q8_0 --cache-type-v q8_0 --reasoning-budget 3000`.

### Imported items (experiments)

`musubi import-items --item <id> --file items.json` loads knowledge items
extracted outside Musubi (another model or prompt) as an item's
statements:
- **input:** kind, status, text, who, and the meeting lines each item
  cites;
- **checks:** they go through the same checks and labelling as points.
  Names and figures must appear in the meeting; the label is the most
  sensitive passage cited; personal items are owner-only;
- **kinds:** insight kinds are kept (metric, framing, rule, disagreement,
  outside_fact, sensitivity_cue, personal), with their own belief weights
  and half-lives;
- **keeping them:** `knowledge.extract = "none"` stops Musubi's own
  extraction, so imported statements are kept and only embedding,
  linking and topics run;
- **proposed plans** (`knowledge.import_keep_proposed`): a proposed
  decision or commitment keeps its kind with status `proposed` instead of
  becoming an opinion; dates and figures are read from the text into typed
  values (`DatesIn` reads "October 1st", "two weeks after that", "end of
  the year"); and the check accepts the meeting's own date and known
  people's full names, which extractors write without them being said.

Experiment instances run beside the real one: `MUSUBI_CONFIG` points the
CLI and the app at another config, and so at another data directory.
Inbox drops gain a `meeting` record (title, start, attendees, turns with
speaker, offset and text), for transcripts from other recorders.

### Beyond points: threads, metrics, framings, rules

Where the knowledge layer is heading (threads with state, metrics with
definitions, disagreements, framings, rules, commitments, outside facts,
people) and how items surface by situation: `docs/insights-direction.md`.

### Claims (`link` stage)

A claim is one assertion across sources. Statements are linked in the order
they were made:
1. Embed the new statement and find the nearest claims by cosine similarity.
2. A near-identical match (≥ 0.97, same kind, same figures and values, and a
   grounded statement) joins without asking the model.
3. Up to six plausible matches (≥ 0.65) are judged one pair at a time: one
   forward pass gives a probability for each of same, contradicts, refines,
   supersedes, answers, unrelated; a relation needs 0.5. Code then applies
   rules the model is unreliable at: a contradiction of a question answers
   it, a "newer version" with the same date or figure confirms it (same),
   relations that make no sense for the kinds are dropped, and statements
   about different customers or projects are never related. (`knowledge.judge
   = "json"` restores the earlier judge, which wrote verdicts for three
   candidates at once.)
4. Otherwise the statement starts a new claim.

With `knowledge.strict_links` (the default), three more rules stop
linking from retiring items that are still live:
- within one meeting, "supersedes" needs a spoken correction ("actually",
  "correcting", "revised"); otherwise it is "refines" (a status and the
  walk-through of it are complementary, said in the same minute);
- one owner's commitment never supersedes or refines another owner's;
- a commitment to find out doesn't answer a question: it is "pending", and
  the question stays open.

Claims are cheap to rebuild from stored statements, so when the linking
rules, model, `min_worth` or worth scorer change, the next run clears them
and links every statement again; extraction is untouched.

Every statement belongs to exactly one claim (what it says). Relations
between claims record how it bears on others:
- contradicts;
- refines;
- supersedes: a decision changed;
- answers: resolves an open question.

A claim's centroid is the mean of its statements' vectors.

### Belief (computed when read, never stored)

Belief is worked out from the visible statements of a claim and its
relations. Each statement contributes evidence

    e = w(kind) × w(certainty) × w(source) × decay(age, half-life(kind))

and evidence is combined per holder first (repetition by one person adds
little), then across holders, so independent people count. Contradicting
claims count against it.

Half-lives:

| kind | half-life |
|---|---|
| question | 14 days |
| hypothesis | 30 days |
| opinion | 60 days |
| fact | 90 days |
| requirement | 180 days |
| decision | none; it changes by being superseded |

Written sources (email, documents) weigh more than speech.

The owner's judgement overrides: verified, disputed or false. Judgements are
kept like the forget list (not derived), keyed by the statement judged, so
they survive rebuilds.

Statuses:

| status | when |
|---|---|
| established | high belief and (two or more independent holders, a written source, or verified) |
| likely | |
| contested | substantial support and substantial contradiction |
| speculative | mostly hypotheses or opinions |
| stale | evidence decayed past two half-lives |
| superseded | a newer visible claim supersedes it |
| disproven | judged false, or contradicted by stronger, newer evidence |

Because nothing decayed is stored, belief drifts with time on its own, and
changing the formula re-scores everything.

### Topics (`topics` step)

Topics are cross-linked, not nested. A commitment to one customer is linked:
- to other topics about the same customer;
- to the same kind of commitment for other customers ("parallel");
- to the topics it is discussed alongside.

Claims naming different organisations or projects are pushed apart while
clustering, so those become separate, linked topics.

1. Claims are clustered by centroid (agglomerative, average link).
2. Each cluster is labelled by the model, with a one-line summary.
3. Topics keep their ids across runs: a new cluster takes the id of the
   previous topic whose claims it mostly shares.
4. Topics relate to each other, as:
   - **same**: they share a customer or project;
   - **together**: they are discussed in the same items;
   - **parallel**: alike in meaning, about different customers or projects;
   - **similar**.

   Similar but never discussed together is shown as a possible connection
   nobody has made.
5. Each rebuild records lifecycle events: born, grew, shrank, merged, split,
   absorbed, dissolved.

### Threads (`knowledge.grouping = "threads"`)

The alternative to pairwise linking and clustering, chosen by the linking
benchmark ([linking-benchmark.md](linking-benchmark.md)): grouping
precision 0.86–0.95 against 0.73, recall 0.43–0.53 against 0.21, and 0.5–2.5%
of live items wrongly retired against 22%. Items are threaded one at a time,
oldest first, with a local model (`knowledge.threads.model`; it reads
meeting text, so a hosted model is refused):

1. **Assign.** The item's statements, 70 at a time (`threads.chunk`), with
   a list of candidate threads: the 40 nearest by embedding
   (`threads.candidates`; a thread's centroid is the mean of its members'
   statement vectors), the 15 most recently active (within three weeks),
   and those born while threading this item, each with its latest current
   statement. The model puts each statement in one or two specific threads
   (an initiative, deal, programme, metric, issue or event; never a broad
   theme), in a new thread it names, or in none (logistics, small talk). It
   replies with groups of ids per thread code (`T12`, or `n1` for a new
   one), not one entry per item. A reply that leaves items out is asked
   again for them.
2. **Reconcile.** For every thread the item touched, one call with up to
   25 of the thread's current statements (`threads.state_cap`, latest
   first) and its new ones. The reply lists only effects: which new
   statement supersedes, corrects, completes, answers or contradicts which
   earlier one, promises to find out (pending), or repeats it (same).
   Code keeps the current list: the previous one plus the new statements,
   less those retired.
3. **Rules in code** (`threadRule`): "corrects" is stored as "supersedes";
   "completes" applies only to a commitment or decision from an earlier
   item; an in-meeting "supersedes" needs a spoken correction. Measured on
   the local 27B, these removed mostly wrong relations (it had called 84
   pairs "completes", 70 of them on views and facts).

The candidate list and the state cap keep a meeting's cost flat however
long the history: over a 20-meeting replay the prompts stayed at 7.6–8.3k
(assign) and 1.8–2.4k (reconcile) tokens. Skipping reconcile for threads
born in the meeting, or asking assign for the effects, both failed: 64% of
real updates are within one meeting.

Storage: threads are rows of `topics` (name and scope as label and
summary), so Explore and cards read them as topics. `thread_items` holds
membership (a statement can be in two threads) and which statements are
current; `thread_runs` which items are done under which rules. Relations
are claim relations. In thread mode the pairwise judge is off: only
near-identical statements join a claim, and linking takes seconds. A
thread is as sensitive as the statements it was named from. A change of
prompts (`ThreadsVersion`), model or link rules rethreads everything.

Model settings: Qwen's non-thinking sampling (temperature 0.7, top-p 0.8,
top-k 20) and token caps of 6k (assign) and 3k (reconcile). At temperature
0 the local 27B looped to 16k tokens on long JSON replies. On the M5 Pro
four ELT meetings take 34 minutes (8 parallel slots).

### Topic cards (`knowledge.CardIndex`, `Views.Card`)

What someone needs about a subject at the moment it comes up
(`docs/topic-cards.md`). Built from claims, with no model at read time
(~30 ms), so it fits the live panel's fast lane.

- **Index**, learned from the knowledge layer and rebuilt with it:
  - trigger terms: capitalised words that most of their claims' topic
    uses and other topics mostly don't. Words also written in lower case
    are ordinary words; names (directory aliases, transcript speakers,
    words seen as the one who "said" or "asked", one letter off a known
    name) are people, not terms;
  - companions: small topics raised during a topic's discussion (most of
    their statements fall between two of the topic's, within 10 minutes,
    in the same meeting), or using its terms. A companion's host is the
    topic whose card it appears on;
  - catch-alls: a topic whose discussion runs cover more than 40% of a
    meeting hosts no companions and anchors no term;
  - term cards: a term whose claims are spread over topics (an event people
    tell stories from) opens a card of the claims that say it, with at
    least 4 claims, unless the word usually ends a longer name ("New
    York");
  - lookup order: a trigger term in the text; else the query's words (2–5
    content words) found verbatim in at most 12 claims, most of them in one
    topic; else the query's embedding against claim centroids, where the
    topic of most of the nearest six wins if the nearest is ≥ 0.66. Short
    queries embed poorly, so exact words come first.
- **In thread mode** a topic is a thread: its card holds the thread's
  members (including those it shares with another thread) and shows only
  the ones the thread marks current; no companions are inferred.
- **Card**, read under a scope, as of a time: the topic's and its
  companions' visible claims, minus superseded, disproven and answered
  ones and lines about running the meeting; placed in sections (Watch,
  Now, Due, Yours, Open, Rules) and ranked by salience: kind, a figure,
  a date, recency (45-day half-life), grounding; reported views ("X
  thinks") rank below state. Commitments show when they fall due. The
  card's label is the narrowest of its lines; restricted, HR, pay,
  health, legal or personal cards are collapsed to their title.
- `musubi card <words>` prints the card the panel would show; `--at`
  replays a date; `--terms` lists what the index learned. Eval cases of
  kind `card` check lines by section, lines that must stay off, collapse,
  and that scopes which mustn't see a card get none.

### Cost and scale

Growth from today's four benchmark meetings to tens of thousands, email and
chat: what breaks first, how it is measured, and the order of work is in
[scaling.md](scaling.md). The design rules below still hold.

The final corpus is much larger than today's:
- meetings back to late last year;
- a year of email;
- six months of chat;
- SharePoint.

We optimise for two things, lookup time and storage, and recomputing must
stay cheap.

- **One expensive step, run once.** Extraction (a model writing statements)
  is the only costly stage. Everything after it is cheap and recomputes from
  statements in minutes:
  - classification rules;
  - value normalisation (in Go);
  - entity matching;
  - linking (incremental);
  - topics;
  - belief (computed at read time).

  Its prompt version changes only when unavoidable.
- **Taxonomy changes without re-extraction.** A new kind is added by
  re-classifying the affected statements (one word each, many per call), not
  by re-extracting. Statement ids, claims and judgements stay put.
  Judgements are anchored to the source line, not the wording.
- **Extraction by need.**
  - Meetings and direct email or chat are extracted as they arrive.
  - The long tail (old mail, newsletters, SharePoint) is chunked, embedded
    and searchable at once, and extracted lazily, the first time it is
    retrieved or a topic needs it.
- **Compact output.** The model writes only what Musubi can't derive:
  statement, kind, holder, certainty, and a raw value. Entities, normalised
  values and sensitivity are derived in code. The JSON schema keeps replies
  well-formed, so a smaller model can be tried.
- **Vectors are kept full** (float32). Re-embedding is the second costliest
  recompute, and every compact form can be derived from full vectors without
  it:
  - Matryoshka truncation;
  - int8;
  - binary codes for a first-pass scan with re-ranking.

  Search stays exact over the scope-filtered rows up to a few hundred
  thousand chunks. Beyond that, a binary prefilter (96 bytes per vector,
  tens of milliseconds per million) feeds a float re-rank of the top few
  hundred. The scope filter still applies before ranking, never after.
- **Topics at scale.** Full average-link clustering is O(n²). It is fine to
  roughly 20,000 claims; past that, claims are assigned incrementally to
  their nearest topic, and re-clustered within organisation and project
  partitions. Threads avoid the problem by design: each item is placed as
  it arrives.

### Sensitivity in the knowledge layer

- A statement is visible exactly when its passage is and its own label
  allows (a point's: personal, or a more sensitive passage it rests on).
- A claim is visible when any of its statements is, and it shows the text of
  a visible one.
- **Belief is computed only from visible statements**, so a confidence or
  source count can't reveal a hidden meeting.
- A topic's label records the highest level of the statements used to write
  it. In a narrower scope the label is replaced by keywords drawn from the
  visible claims.

### Evaluation

Synthetic scenarios (each with leak checks):
- a decision changed later (supersedes);
- a hypothesis later disproven;
- one person's opinion contradicted by a written fact;
- a fact confirmed both in a restricted meeting and in the open, whose
  evidence count must differ by scope;
- an open question later answered.

Case kinds:
- `statement`: kind, holder and text extracted from a passage;
- `claim`: status, relation and evidence count under a scope;
- `topic`: claims grouped together;
- `card`: the card a query opens, its lines by section, lines that must
  stay off, collapse, and no card in scopes that mustn't see it.

Grouping and linking quality (groups, relations, current state, search and
cards per subject) is measured by the linking benchmark, outside the eval
harness: [linking-benchmark.md](linking-benchmark.md).

CI replays recorded model replies and embeddings (`testdata/synthetic/llm-cache/`);
`make eval-record` re-records them locally when prompts change. Model quality
(extraction precision, linking accuracy) is measured locally on private
cases.

## Storage

One SQLite file (`~/.local/share/musubi/musubi.db`, WAL mode) holds all
derived data. The raw store is a directory of blobs beside it.

| Need | Mechanism |
|---|---|
| Keyword search | FTS5 |
| Semantic search, topic clustering | sqlite-vec; exact cosine over the scope-filtered rows (no approximate index, so the scope filter can't be bypassed; milliseconds at this scale) |
| Graph | `entities` and `edges` tables (typed, timestamped, weighted, with provenance); traversal by recursive CTEs |
| Facets across dimensions | person, customer, business area, project and topic are all entity types; a facet is a join through `edges`, with time as a filter |
| Trends | time-bucketed aggregate tables; DuckDB can attach the SQLite file for heavier analysis |

Chunks carry their item's title (for a meeting, the calendar subject). It is
searchable and is prepended when embedding, so a passage keeps the context of
the meeting it came from.

The schema has a version. Because everything except the forget list and the
audit log is rebuilt from the raw store, an incompatible schema change drops
the derived tables and the next update refills them. That includes the
extracted statements, which at full scale cost hours of model time, so new
columns on them are added in place (`addColumns`) instead of bumping the
version.

A dedicated graph database is not warranted at this scale (thousands of items
and entities). The schema is plain relational behind a `store` interface so it
can move to Postgres + pgvector (and Apache AGE if needed) without redesign.

Visualization (Wails frontend): graph explorer (sigma.js or Cytoscape), timeline,
2D topic map from projected vectors, trend charts.

## Sensitivity

Every item gets a label; every derived row inherits from its sources; every
read declares a scope and is filtered in SQL. This is what makes automatic uses
(drafted replies, shared-screen panels, cloud LLM calls) safe to add later.

### Label

A label has three parts:

- **Level** (ordered): `public` < `internal` < `confidential` < `restricted`.
- **Categories** (any of): `hr`, `compensation`, `legal`, `security`,
  `financial`, `deal-terms`, `customer-pii`, `health`, `strategy`, `personal`.
  A category can restrict an item regardless of level. `personal` (someone's
  life outside work) is set on knowledge points, not items; drafted replies
  never allow it.
- **Audience**: the principals who were party to the source (meeting attendees,
  email recipients, chat members). This is "need to know" taken from provenance:
  something learned in a 1:1 with one person is not automatically shareable with
  another. For a recording matched to a calendar event, the audience is the
  owner and the people heard speaking, not the invite list: a recording can
  run on after most invitees leave. Silent attendees and unidentified voices
  lose access, which only narrows reads. The invite list still sets the
  level, so a broad meeting stays visible to its attendees through
  `open_level`. `sensitivity.audience_from_speakers = false` restores the
  invite list.

**Default levels.** Only broadcasts (an audience of 15 or more, or a company
list) are `internal`. One-to-ones, working groups, anything with an external
participant, and anything with unidentified participants are `confidential`.
Group size is a poor proxy for sensitivity (an executive team meeting is a
group), so working groups default closed; `internal_min_audience` changes the
threshold.

Each label records its origin (`native`, `rule`, `model`, `user`), confidence
and reason. Precedence: `user` > `native` > `rule` > `model`. An item with no
confident label is treated as `confidential`; the default is to fail closed.

### Classification (the `classify` stage)

1. **Native labels:** Purview/MIP sensitivity labels on mail, calendar private
   flags, Salesforce object types.
2. **Rules:** audience size and makeup, external participants, subject and
   keyword patterns. Keyword rules catch formal terms well and conversation
   about people poorly; see the research log.
3. **Local model:** a classifier over item text for level and categories,
   applied where rules are silent or to raise a level, never to lower one.
4. **User overrides** from the UI, stored as labels with origin `user`.

Classification is a versioned stage like any other, so improving it means
re-running it. A hand-labelled set in `testdata/private/` measures it; recall
on `restricted` and on the categories matters more than precision.

### Propagation

- Chunks, mentions and embeddings inherit their item's label.
- A **fact** supported by several sources can be disclosed in a scope if *any*
  supporting source is visible in that scope. Each disclosure cites only the
  visible sources.
- **Entities** carry a level too: an entity's existence can itself be
  sensitive (a project codename, an acquisition target), set in the curated
  registry. An entity not in the registry or directory is visible only where
  some visible item links to it.
- **Aggregates and topics** compute per scope, or carry the highest label of
  their inputs, so counts and trend lines can't leak what the rows hide.

### Scoped reads

`store` exposes no query without a `Scope`:

```
scope: name, max_level, allowed_categories, audience[], open_level
```

`open_level` is what an audience may see from sources they weren't party to:
`internal` when every recipient is an employee, `public` as soon as anyone
outside the company is included.

| Purpose | Scope |
|---|---|
| Owner explores alone | everything, with labels shown |
| Side panel, normal | everything, with cards marked when other attendees weren't party to the source |
| Side panel, screen shared | `internal` and below, no categories; set by the screen-safe toggle |
| Drafted reply to X | items where X is in the audience, plus uncategorised `internal` (X an employee) or `public` (X external); never `personal` |
| Sending text to a cloud provider | the per-source egress policy, e.g. no email to cloud |

Egress to a cloud LLM is just another scope, so provider policy and
sensitivity are enforced by the same code. Scoped reads made for outbound
purposes (replies, cloud calls) are logged to an audit table.

## Providers

`provider.Embedder` and `provider.LLM` interfaces with local implementations
(ONNX Runtime embeddings; llama.cpp or Ollama for extraction) and a Claude
implementation. A stage asks for a provider by capability; the scope decides
which providers it may receive data for.

## Deletion

Because every derived row points at its raw source, forgetting a source (one
email thread, one meeting, a whole connector) deletes the raw blob and cascades
through items, chunks, vectors, facts and edges, then marks affected aggregates
stale.

## Evaluation

- **Replay:** play a past meeting's live text at real speed, record which cards
  appear and when, and score them against hand judgements in
  `testdata/private/`.
- **Classification:** precision and recall per level and category on a
  labelled set.
- **Resolution:** mention-to-entity accuracy on a labelled set.

Findings go in `docs/` research notes, dated, with the numbers.

## Packages

```
cmd/musubi/            CLI: update, search, ask, graph, show, stats, forget, knowledge,
                       claims, topics, topic, card, judge, gate, import-items, live, eval, version
cmd/musubi-gui/        Wails app (frontend embedded from frontend/dist)
internal/
  config/              TOML config over defaults
  source/              connectors: tomoe/, mcpdrop/ (inbox drops)
  raw/                 immutable blob store and manifest
  item/                item shape and per-source translators
  pipeline/            stage runner, version stamps, the knowledge run (extract, link,
                       topics or threads)
  stages/              classify, chunk, embed, resolve (mentions included)
  sensitivity/         labels, rules, scopes
  registry/            people and organisations for resolution
  store/               SQLite schema, scoped reads, FTS, vectors, graph, knowledge,
                       cards and threads tables
  knowledge/           statements, points, imports, values, linking, belief, topics,
                       threads, card index and cards
  engine/              opens the store and providers; search; card lookup
  ask/                 Q&A over scoped retrieval
  live/                Tomoe feed, replay, suggestions, briefs, topic cards
  provider/            embedders and chat models (Ollama, OpenAI-compatible), decider,
                       recorded-reply cache
  eval/                case harness (search, leak, classify, resolve, graph,
                       knowledge, card)
  gui/                 Wails backend
frontend/              React + TypeScript
tools/insights-eval/   extraction benchmark scripts (hosted and local models)
tools/link-bench/      linking benchmark: export, scorer, retrieval, thread prototypes
testdata/synthetic/    invented corpora and cases (committed)
testdata/private/      real data and references, gitignored
```

## First milestone

Ingest the Tomoe sessions and calendar; run `classify`, `chunk`, `embed`,
`mentions` and `resolve`; answer `musubi search` and `musubi graph <entity>`
from the CLI with scopes enforced. That validates the schema and the
sensitivity model before any UI exists.
