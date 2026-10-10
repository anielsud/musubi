# Musubi

A local-first personal knowledge base. It mines meetings (from Tomoe), calendar,
email, chat and CRM records into a graph of people, accounts, topics, decisions
and commitments, and will surface it live in a side panel during meetings.

- Design: [docs/architecture.md](docs/architecture.md)
- Measuring quality: [docs/evaluation.md](docs/evaluation.md)
- Findings: [docs/research-log.md](docs/research-log.md)

## Use

Needs Go, a C compiler (cgo), Node, and [Ollama](https://ollama.com) with
`ollama pull nomic-embed-text qwen3:8b` (embeddings and the local chat model).

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
bin/musubi live --replay <tomoe session id> --speed 20   # print live suggestions and briefs
make test eval                          # synthetic corpus; fails on any leak
```

Config: `~/.config/musubi/config.toml` (owner email and internal domains at
least). Calendar, mail and chat arrive as JSON envelopes in
`~/.local/share/musubi/inbox/<source>/` (format in
`internal/source/mcpdrop`).

Proprietary; all rights reserved. See [NOTICE](NOTICE).
