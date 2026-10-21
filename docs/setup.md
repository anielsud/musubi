# Setup, and what processing costs

For someone setting Musubi up on a Mac, from nothing to a working
dataset with hosted models. Written 2026-10-21. Costs are measured from
the spend ledger; the research log has the runs behind them.

## 1. What runs where

Musubi uses three kinds of model. Each step's choice lives in the
dataset's config; the code's defaults are all local.

| Kind | Used for | Default (local) | Used on the "All" dataset |
|---|---|---|---|
| Embeddings | search, threads, card lines | Ollama `nomic-embed-text` | the same |
| Chat models | extraction, threading, statuses, Ask, prep, Live suggestions and summaries | Ollama `qwen3:8b` | GLM-5.3-Flash on Baseten for all but Live; Live stays on `qwen3:8b` for latency |
| Typed classifier | sensitive-statement labels, Live relevance | the chat model's yes/no | Jev (TypeSafe) |

Jev is cleared for testing, not for the product. The product path is the
same `provider.Classifier` interface over GLM or the local model
(`provider = "decider"`).

## 2. Install

Needs macOS (Apple silicon), Go, a C compiler (Xcode command-line tools),
Node and [Ollama](https://ollama.com).

```sh
ollama pull nomic-embed-text qwen3:8b
ollama pull qwen3.8:27b          # optional: Ask's local fallback (~16 GB in memory while loaded)
make build                       # bin/musubi (CLI)
make install-gui-mac             # /Applications/Musubi.app, added to the Dock
```

The app starts `ollama serve` if it isn't running (`llm.start_ollama`)
and never stops it.

For Live during meetings, Tomoe's live feed must be on: in Tomoe,
Settings → Sharing → Live feed (`[meeting] live_feed = true` in Tomoe's
config). Musubi reads `~/.local/share/tomoe/live.sock`.

## 3. Keys

Hosted models read the key named by `api_key`: `keychain:<service>` (the
macOS keychain) or `env:<VARIABLE>`. Nothing is stored in config files.

```sh
security add-generic-password -a "$USER" -s baseten-api-key -w   # prompts for the key
security add-generic-password -a "$USER" -s jev-api-key -w
```

## 4. Config

`~/.config/musubi/config.toml` is loaded over the defaults; missing keys
keep them. The minimum is the owner:

```toml
[owner]
email = "you@company.com"
name = "Your Name"
internal_domains = ["company.com"]
```

Hosted models only read meeting text when the owner allows it:

```toml
[knowledge]
allow_hosted = true        # hosted models may read meeting text (points, threads, labels, Live relevance)
grouping = "threads"

[knowledge.threads.model]  # threading: place, reconcile, consolidate
provider = "openai"        # any OpenAI-compatible endpoint
url = "https://inference.baseten.co"
model = "zai-org/GLM-5.3-Flash"
api_key = "keychain:baseten-api-key"
price_in = 0.15            # $ per million tokens, for the spend ledger
price_out = 0.502

[knowledge.status.model]   # thread statuses: same fields as above
[ask.model]                # Ask and prep talking points: same fields
[ask.fallback]             # when the hosted model fails
provider = "ollama"
url = "http://127.0.0.1:11434"
model = "qwen3.8:27b"

[knowledge.label]
enabled = true
[knowledge.label.classifier]
provider = "jev"
api_key = "keychain:jev-api-key"

[live.relevance]           # Live: what fits the conversation takes the panel's places
provider = "jev"
api_key = "keychain:jev-api-key"
min_p = 0.5
```

The full set of keys, each with what it does and how to restore the
earlier behaviour, is in `internal/config/config.go`.

## 5. Data

- **Tomoe sessions** are read from `~/.local/share/tomoe/sessions`
  (`tomoe_sessions`); `musubi update` ingests new ones, and the app does
  when Tomoe saves a meeting (`live.update_on_save`).
- **Calendar, email, chat and CRM** arrive as JSON envelopes in
  `~/.local/share/musubi/inbox/<source>/` (format: `internal/source/mcpdrop`).
- **Datasets.** Experiments run as separate instances, each a directory
  with its own `config.toml` and `data/`, listed in
  `~/.config/musubi/datasets.toml`; the app switches between them and opens
  two side by side. `MUSUBI_CONFIG` points the CLI at one.

## 6. Building a dataset

```sh
bin/musubi update                    # ingest and process (local models unless configured)
bin/musubi knowledge --plan          # what a run would do
bin/musubi knowledge                 # thread, label and write statuses
```

**Extraction for the "All" dataset runs outside the app**, from meeting
transcripts with the measured prompt (`lean_ex_v2`, GLM-5.3-Flash):

```sh
cd tools/insights-eval
python3 transcripts.py drops ../../path/to/data/inbox/meetings <transcripts…>
python3 transcripts.py extract <items dir> "<one-line description>" <transcripts…>
# then, per meeting: bin/musubi import-items --item <id> --file <items.json>
# with [knowledge] extract = "none", then bin/musubi knowledge
```

`tools/link-bench/backfill.py` runs the whole sequence for a dataset and
resumes where it stopped. In-app extraction (`knowledge.extract =
"points"` or `"statements"`) was measured on local models; bringing
`lean_ex_v2` into the Go pipeline is open work.

## 7. Spend ledger and caps

Every hosted call is metered when `MUSUBI_SPEND_LEDGER` names a file
(JSON lines: model, step, tokens, $). `MUSUBI_SPEND_CAP` refuses hosted
calls once the ledger's total reaches it; `MUSUBI_SPEND_TAG` labels the
run.

```sh
export MUSUBI_SPEND_LEDGER=~/musubi-spend.jsonl MUSUBI_SPEND_CAP=20 MUSUBI_SPEND_TAG=first-build
```

## 8. What processing costs

Prices: GLM-5.3-Flash on Baseten $0.15 per million input tokens, $0.502
per million output; Jev $0.042 per million input, output free. Local
steps cost nothing but time and memory.

**Per meeting, building the memory** (measured on 165–169 meetings of
every kind: leadership meetings, 1:1s, product syncs, customer calls):

| Step | Model | Cost per meeting | Notes |
|---|---|---|---|
| Extraction | GLM-5.3-Flash | ~0.9¢ on average; ~2.5¢ for a long leadership meeting (~250 items) | $1.50 for the leadership year (24) and a month of every meeting (145) |
| Threading (place, reconcile, consolidate) | GLM-5.3-Flash | ~2.7¢ | $4.41 for 165 meetings in time order, with their first statuses |
| Statuses | GLM-5.3-Flash | ~0.1¢ a thread written; a meeting touches a handful | rewriting all 690: $0.58 |
| Labels | Jev | ~0.004¢ a statement, ~0.4–1¢ a meeting | |
| Embeddings | local | free | |
| **Total** | | **about 4–6¢ a meeting** | |

At that rate a person's thousand meetings cost about $40–60 to build,
once; after that each new meeting costs its own few cents.

**Using it:**

| Use | Model | Cost |
|---|---|---|
| Ask | GLM-5.3-Flash | ~$0.001 a question (28 questions: $0.027), ~3 s |
| Prep talking points | GLM-5.3-Flash | about the same as a question |
| A correction rule's refresh | GLM-5.3-Flash + local | a few status rewrites, under 1¢ |
| Live suggestions and summaries | local `qwen3:8b` | free |
| Live relevance | Jev | **~13¢ a meeting hour** as built (~3,500 calls an hour, ~100 ms each) |

Live relevance is the largest running cost today: it judges every card and
lookup again as the conversation moves. Judging only what changed, and
fewer re-checks of what is shown, should cut it several times over; it's
on the roadmap.

**What the local models cost instead:** time and memory. Local extraction
on the 27B took about 12 minutes a meeting on an M5 Pro; the 27B holds
about 16 GB while loaded, which caused memory stalls during meetings when
it stayed resident.

## 9. Checking it works

```sh
make vet test                        # synthetic corpus; fails on any leak
bin/musubi ask "where are we on <subject>?"
bin/musubi live --replay <tomoe session id> --stepped --events out.jsonl   # a past meeting through Live
```

In the app: Live replays a past meeting from its picker; Explore →
Corrections reviews proposed spellings; the ⚙︎ button shows the live
pipeline's trace.
