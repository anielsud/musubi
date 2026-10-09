# Musubi architecture

Musubi (結び, "tying together") is a local-first personal knowledge base. It
ingests meetings (from Tomoe), calendar, email, chat and CRM records, distills
them into people, accounts, topics, decisions and commitments linked to their
sources, and serves that knowledge two ways:

- **Live:** a side panel that surfaces relevant cards while a meeting is in
  progress, driven by Tomoe's live transcript.
- **Explore:** a graph, timeline, topic map and trend view across time, people,
  business area, customer and topic.

Status: design. Nothing below is implemented yet unless a package says so.

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

### 5. Hot lane

The live path never writes. Tomoe streams lines over an opt-in local socket (a
small Tomoe PR, off by default). A spotter matches names and terms against the
registry and entity tables, runs scoped retrieval, and ranks cards (who this is,
recent decisions, open commitments, related threads). Slow enrichment, if any,
arrives after the cards. After the meeting, Tomoe's saved session enters the
cold lane like any other source.

## Storage

One SQLite file (`~/.local/share/musubi/musubi.db`, WAL mode) holds all
derived data. The raw store is a directory of blobs beside it.

| Need | Mechanism |
|---|---|
| Keyword search | FTS5 |
| Semantic search, topic clustering | sqlite-vec |
| Graph | `entities` and `edges` tables (typed, timestamped, weighted, with provenance); traversal by recursive CTEs |
| Facets across dimensions | person, customer, business area, project and topic are all entity types; a facet is a join through `edges`, with time as a filter |
| Trends | time-bucketed aggregate tables; DuckDB can attach the SQLite file for heavier analysis |

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
  another, even at `internal`.

Each label records its origin (`native`, `rule`, `model`, `user`), confidence
and reason. Precedence: `user` > `native` > `rule` > `model`. An item with no
confident label is treated as `confidential`; the default is to fail closed.

### Classification (the `classify` stage)

1. **Native labels:** Purview/MIP sensitivity labels on mail, calendar private
   flags, Salesforce object types.
2. **Rules:** source defaults (1:1s and HR-team meetings start higher), sender
   or recipient groups, subject and keyword patterns, external participants.
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
- **Entities and edges** carry labels too: an entity's existence can itself be
  sensitive (a project codename, an acquisition target). An entity created only
  from restricted sources is restricted.
- **Aggregates and topics** compute per scope, or carry the highest label of
  their inputs, so counts and trend lines can't leak what the rows hide.

### Scoped reads

`store` exposes no query without a `Scope`:

```
scope: purpose, audience[], max_level, allowed_categories[], provider (local|cloud:<name>)
```

| Purpose | Scope |
|---|---|
| Owner explores alone | everything, with labels shown |
| Side panel, normal | everything, with cards marked when other attendees weren't party to the source |
| Side panel, screen shared | `internal` and below, no categories; Tomoe already detects sharing windows |
| Drafted reply to X | items where X is in the audience, plus `public` / `internal` with no categories |
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
