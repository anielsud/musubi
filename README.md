# Musubi

A local-first personal knowledge base. It mines meetings (from Tomoe), calendar,
email, chat and CRM records into people, accounts, threads, decisions and
commitments, and surfaces them live in a side panel during meetings as
topic cards.

Docs:
- **Start with [overview.md](docs/overview.md):** the thesis, approach,
  the system at a glance, where it stands, the roadmap and the research
  highlights
- [setup.md](docs/setup.md): install, keys, hosted models, building a
  dataset, and costs per meeting
- [architecture.md](docs/architecture.md): the design, as built
- [incremental-design.md](docs/incremental-design.md): the subject graph,
  local operations, entities, aliases and correction rules
- [scaling.md](docs/scaling.md): growth to tens of thousands of meetings,
  email and chat; how it is measured and made cheaper
- [topic-cards.md](docs/topic-cards.md): cards in the live panel
- [insights-direction.md](docs/insights-direction.md): what knowledge to
  learn and surface
- Measuring quality: [evaluation.md](docs/evaluation.md) (retrieval, leaks,
  knowledge cases), [insights-eval.md](docs/insights-eval.md) (extraction),
  [linking-benchmark.md](docs/linking-benchmark.md) (grouping and links)
- [local-models-2026-10.md](docs/local-models-2026-10.md),
  [tomoe-live-feed.md](docs/tomoe-live-feed.md)
- Findings, dated, with numbers: [research-log.md](docs/research-log.md)

## Use

**Setup, hosted models (GLM on Baseten, Jev), keys, building a dataset and
what processing costs: [docs/setup.md](docs/setup.md).** In short: Go, a
C compiler (cgo), Node, and [Ollama](https://ollama.com) with `ollama pull
nomic-embed-text qwen3:8b`; hosted models are configured per dataset, with
keys in the macOS keychain.

### Desktop app

```sh
make install-gui-mac    # builds /Applications/Musubi.app and adds it to the Dock
```

Live briefings need Tomoe's live feed: in Tomoe, Settings → Sharing → Live
feed (or `[meeting] live_feed = true`). Without a meeting, the Live tab can
replay a past one. The app starts Ollama if it isn't running.

### CLI

```sh
make build
bin/musubi update                       # ingest Tomoe sessions + inbox, process
bin/musubi search "renewal pricing"     # owner scope
bin/musubi search --scope reply:someone@company.com "renewal pricing"
bin/musubi graph "Acme"                 # an entity's items and connections
bin/musubi show tomoe:<id>              # an item's chunks, labels, mentions
bin/musubi stats
bin/musubi forget <item id>
bin/musubi ask "what did we decide about X?"
bin/musubi live --replay <tomoe session id> --stepped --events out.jsonl   # replay a meeting through Live, speed-independent
bin/musubi knowledge                    # extract, link, and build topics or threads
bin/musubi card "renewal pricing"       # the card the live panel would show
bin/musubi topics; bin/musubi topic <id>; bin/musubi claims "pricing"
make test eval                          # synthetic corpus; fails on any leak
```

Config: `~/.config/musubi/config.toml` (owner email and internal domains at
least). Calendar, mail and chat arrive as JSON envelopes in
`~/.local/share/musubi/inbox/<source>/` (format in
`internal/source/mcpdrop`). `MUSUBI_CONFIG` points the CLI and the app at
another config, for experiment instances beside the real one. The app lists
datasets from `~/.config/musubi/datasets.toml` in a dropdown and can open
two side by side:

```toml
[[dataset]]
key = "best-glm"
name = "Best GLM"
config = "/path/to/datasets/best-glm/config.toml"
note = "what is in it"
```

Knowledge extraction and threading run best on a larger local model
served by llama.cpp (`brew install llama.cpp`); the measured settings are
in architecture.md ("Points", "Threads") and docs/local-models-2026-10.md.
Thread grouping is `[knowledge] grouping = "threads"` with
`[knowledge.threads.model]` pointing at that server.

Proprietary; all rights reserved. See [NOTICE](NOTICE).
