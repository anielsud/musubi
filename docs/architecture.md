# Musubi architecture

Musubi (結び, "tying together") is a local-first personal knowledge base. It
ingests meetings (from Tomoe), calendar, email, chat and CRM records, distills
them into people, accounts, topics, decisions and commitments linked to their
sources, and serves that knowledge two ways:

- **Live:** a side panel that surfaces relevant cards while a meeting is in
  progress, driven by Tomoe's live transcript.
- **Explore:** a graph, timeline, topic map and trend view across time, people,
  business area, customer and topic.

Status (2026-10-09): the first milestone is built: Tomoe and inbox ingest,
calendar linking, classify / chunk / embed / resolve, scoped search and
graph from the CLI, and the eval harness ([evaluation.md](evaluation.md)).
The desktop app (2026-10-10) adds the live lane, Q&A and an explorer; see
"Desktop app" below. Facts, topics and trends are still design. Findings:
[research-log.md](research-log.md).

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
- **Screen-safe**: a toggle switches the panel's scope to `shared-screen`.
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
- **certainty** as expressed: high, medium or low;
- **said_at**, and **when**: the time it refers to ("ship on the 22nd");
- **line**: the passage line it came from, so it inherits that passage's
  sensitivity and can be cited exactly.

Small talk and procedure are skipped. The stage is versioned by model and
prompt like any other, so improving the prompt means re-running it.

### Claims (`link` stage)

A claim is one assertion across sources. Statements are linked in the order
they were made:
1. Embed the new statement and find the nearest claims by cosine similarity.
2. A near-identical match (≥ 0.92) joins without asking the model.
3. For plausible matches (≥ 0.70), the model judges each candidate as one
   of: same, supports, contradicts, refines, supersedes, answers, unrelated.
4. Otherwise the statement starts a new claim.

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

1. Claims are clustered by centroid (agglomerative, average link).
2. Each cluster is labelled by the model, with a one-line summary.
3. Topics keep their ids across runs: a new cluster takes the id of the
   previous topic whose claims it mostly shares.
4. Topics relate to each other by meaning (centroid similarity) and by being
   discussed together (sharing items). Similar in meaning but never
   discussed together is shown as a possible connection nobody has made.

### Sensitivity in the knowledge layer

- A statement is visible exactly when its passage is.
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
- `topic`: claims grouped together.

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
the derived tables and the next update refills them.

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
  `financial`, `deal-terms`, `customer-pii`, `health`, `strategy`. A category can
  restrict an item regardless of level.
- **Audience**: the principals who were party to the source (meeting attendees,
  email recipients, chat members). This is "need to know" taken from provenance:
  something learned in a 1:1 with one person is not automatically shareable with
  another.

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
| Side panel, screen shared | `internal` and below, no categories; Tomoe already detects sharing windows |
| Drafted reply to X | items where X is in the audience, plus uncategorised `internal` (X an employee) or `public` (X external) |
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

As built, with names that differ from the plan: per-item stages live in
`internal/stages`; resolution and mentions are one stage (`resolve`).


```
cmd/musubi/          CLI: ingest, process, search, ask, graph, trends, replay, forget
cmd/musubi-gui/      Wails app: side panel and explorer (later)
internal/
  config/            TOML config over defaults
  source/            connector interface; tomoe/, calendar/, mcpdrop/, graph/
  raw/               immutable blob store and manifest
  item/              item shape and per-source translators
  pipeline/          stage runner, job queue, version stamping
  stages/            classify, chunk, embed, mentions, resolve, facts, edges
  sensitivity/       labels, rules, propagation, scopes
  registry/          people, accounts, org (reference lists for resolution)
  store/             SQLite schema, migrations, FTS, vectors, graph queries
  analysis/          topics, trends, signals, centrality
  live/              Tomoe stream client, spotter, card ranking
  provider/          embedder and LLM interfaces; local and Claude
  replay/, eval/     scoring against private references
frontend/            React + TypeScript (later)
testdata/private/    real data and references, gitignored
```

## First milestone

Ingest the Tomoe sessions and calendar; run `classify`, `chunk`, `embed`,
`mentions` and `resolve`; answer `musubi search` and `musubi graph <entity>`
from the CLI with scopes enforced. That validates the schema and the
sensitivity model before any UI exists.
