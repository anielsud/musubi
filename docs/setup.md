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
| Typed classifier | sensitive-statement labels, Live relevance | the chat model's yes/no | Jev (TypeSafe), or Clef (Cloudflare) |

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
[knowledge.digest]         # meeting summaries and to-dos (one whole-meeting read each)
enabled = true
todos = true               # the To-do tab reads them instead of commitment statements
[knowledge.digest.model]   # same fields as above; omitted: the threads model
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

Through the Optimizely AI Gateway, the same models take one personal
gateway token. **Settings → Optimizely AI Gateway** sets it up: paste the
token, and Musubi checks it with Cloudflare, keeps it in the Keychain and
writes the gateway to the config:

```toml
[gateway]
url = "https://<gateway worker>"
account = "<account>"
api_key = "keychain:ai-gateway-token"
token_id = "<token id>"
```

Every model whose `url` is on the gateway (its worker, or the account's
`/ai/run` catalog) then takes the gateway's `api_key` and headers for
whatever it doesn't set itself, so a model needs only `provider`, `url`,
`model` and prices. The card also lists the models the gateway serves,
with live prices and Cloudflare's planned retirement dates. By hand,
without the app:

1. **Get a token** (`cfat_…`) from the gateway's mint workflow (a one-time
   link). It needs both of the permissions personal tokens are minted
   with, *AI Gateway Run* and *Workers AI Read*: without the second,
   Claude works but GLM and Clef answer 401 "Authentication error".
   A re-mint keeps a token's permissions, so a token missing one is
   revoked and minted again.
2. **Put it in the keychain** (Musubi only reads it; it prompts for the
   token, so it never lands in a file or the shell history):
   `security add-generic-password -s ai-gateway-token -a "$USER" -w`
3. **Look up its `token_id`**, which isn't secret, for the header that
   charges calls to you (without it they land as "unknown"):
   ```sh
   curl -s https://api.cloudflare.com/client/v4/accounts/<account>/tokens/verify \
     -H "Authorization: Bearer $(security find-generic-password -s ai-gateway-token -w)" | jq -r .result.id
   ```
4. **Set the models** as below. `cf-aig-collect-log-payload: false` keeps
   the prompt and the reply out of the gateway's logs (cost, tokens and
   the `token_id` stay); send it until the gateway sets it on every
   request itself. `cf-aig-skip-cache: true` skips the gateway's
   5-minute response cache, so a retry after a bad reply gets a new one
   rather than the same.

```toml
[knowledge.threads.model]  # and the other hosted models alike
provider = "openai"
url = "https://<gateway worker>/workers-ai-shim"
model = "@cf/zai-org/glm-5.3-flash"
api_key = "keychain:ai-gateway-token"
headers = { "cf-aig-metadata" = '{"token_id":"<token id>","harness":"musubi","setup_version":"custom"}', "cf-aig-collect-log-payload" = "false", "cf-aig-skip-cache" = "true" }
price_in = 0.15
price_out = 0.50

[knowledge.label.classifier] # Jev through the gateway (the organisation's key)
provider = "jev"
url = "https://api.cloudflare.com/client/v4/accounts/<account>/ai/run"
api_key = "keychain:ai-gateway-token"
headers = { "cf-aig-gateway-id" = "ai-gw", "cf-aig-collect-log-payload" = "false", "cf-aig-metadata" = '{"token_id":"<token id>","harness":"musubi","setup_version":"custom"}' }
# [live.relevance] takes the same provider, url, api_key and headers.
```

Claude models go through the gateway's `/anthropic-shim` with the same
token and headers (`provider = "anthropic"`). Claude Haiku 5.5 at medium
thinking matched GLM-5.3-Flash on Musubi's prompts, two to five times
faster (research log, 2026-11-11):

```toml
[knowledge.threads.model]
provider = "anthropic"
url = "https://<gateway worker>/anthropic-shim"
model = "claude-haiku-5-5"
api_key = "keychain:ai-gateway-token"
headers = { "cf-aig-metadata" = '{"token_id":"<token id>","harness":"musubi","setup_version":"custom"}', "cf-aig-collect-log-payload" = "false", "cf-aig-skip-cache" = "true" }
reasoning_effort = "medium"  # the default; it wins over a call's own level
price_in = 0.10
price_out = 0.50
```

A Jev `url` ending in `/ai/run` is Cloudflare's model catalog: the request
names `typesafe/jev`, and without `cf-aig-gateway-id` Cloudflare answers 402.
The catalog's Jev version floats like TypeSafe's own `jev-latest`, and
the two needn't match (on 2026-11-11 the gateway served 1.13.0 and
TypeSafe 1.14.0). Clef (`provider = "clef"`, `url` its own `/ai/run/@cf/
cloudflare/clef` address) takes the same questions; on the labels sample
it found fewer sensitive statements than Jev (research log, 2026-11-11).

The full set of keys, each with what it does and how to restore the
earlier behaviour, is in `internal/config/config.go`.

## 5. Data

- **Tomoe sessions** are read from `~/.local/share/tomoe/sessions`
  (`tomoe_sessions`); `musubi update` ingests new ones, and the app does
  when Tomoe saves a meeting (`live.update_on_save`) and, with
  `live.update_every` (minutes), on a timer.
- **Bluedot transcripts** (markdown) are read from a folder (`bluedot_dir`,
  from `bluedot_since`): for meetings Tomoe missed. A meeting both recorded
  is knowledge once: Tomoe's copy, unless it holds under 80% of Bluedot's
  words (a broken capture). One knowledge run at a time per dataset, across
  the app and the CLI (`knowledge.lock` in the data folder).
- **Calendar, email, chat and CRM** arrive as JSON envelopes in
  `~/.local/share/musubi/inbox/<source>/` (format: `internal/source/mcpdrop`).
- **Datasets.** Experiments run as separate instances, each a directory
  with its own `config.toml` and `data/`, listed in
  `~/.config/musubi/datasets.toml`; the app switches between them.
  `MUSUBI_CONFIG` points the CLI at one.

## 6. Building a dataset

```sh
bin/musubi update                    # ingest and process (local models unless configured)
bin/musubi knowledge --plan          # what a run would do
bin/musubi knowledge                 # thread, label and write statuses
```

**Extraction runs in the app** with the measured meeting prompt
(`lean_ex_v2`, GLM-5.3-Flash), when the dataset asks for it:

```toml
[knowledge]
extract = "lean"           # meetings: the measured prompt; email and chat: statements

[knowledge.lean]
# directory = "…/directory.json"   # optional {"people": [[name, title, office]]},
                                    # only for recognising who is meant
[knowledge.lean.model]     # same fields as [knowledge.threads.model]
```

`bin/musubi extract-lean --item <id> --out items.json` extracts one
meeting without storing anything, to check it. (The Python tools in
`tools/insights-eval` that did this before still work, with
`extract = "none"` and `import-items`.)

**What runs on its own, and what waits.** After each meeting Tomoe saves,
the app ingests it and, with `knowledge.on_update`, extracts and threads
it, but only meetings newer than everything threaded, at most
`knowledge.auto_max` (5) at a time. Threading is only right in time order,
so an older meeting (a backlog) waits until everything is rethreaded in
order (`knowledge.threads.hold_older`). The Status tab's **Work queue**
shows what's waiting, with estimates, and starts the two runs updates
leave to someone: **Catch up** (a big batch of new meetings) and
**Rethread everything in time order** (to take in a backlog; it stops at
one and a half times its estimate). A meeting recorded twice (a Tomoe
session and the meeting app's transcript) becomes knowledge once
(`knowledge.prefer_source`, default Tomoe; outside a rethread the copy
already extracted stays, so threads don't churn, and a rethread switches to
the preferred one, retiring the other's statements).

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
