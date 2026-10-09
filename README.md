# Musubi

A local-first personal knowledge base. It mines meetings (from Tomoe), calendar,
email, chat and CRM records into a graph of people, accounts, topics, decisions
and commitments, and will surface it live in a side panel during meetings.

- Design: [docs/architecture.md](docs/architecture.md)
- Measuring quality: [docs/evaluation.md](docs/evaluation.md)
- Findings: [docs/research-log.md](docs/research-log.md)

## Use

Needs Go, a C compiler (cgo), and [Ollama](https://ollama.com) for embeddings
(`ollama pull nomic-embed-text`).

```sh
make build
bin/musubi update                       # ingest Tomoe sessions + inbox, process
bin/musubi search "renewal pricing"     # owner scope
bin/musubi search --scope reply:someone@company.com "renewal pricing"
bin/musubi graph "Acme"                 # an entity's items and connections
bin/musubi show tomoe:<id>              # an item's chunks, labels, mentions
bin/musubi stats
bin/musubi forget <item id>
make test eval                          # synthetic corpus; fails on any leak
```

Config: `~/.config/musubi/config.toml` (owner email and internal domains at
least). Calendar, mail and chat arrive as JSON envelopes in
`~/.local/share/musubi/inbox/<source>/` (format in
`internal/source/mcpdrop`).

Proprietary; all rights reserved. See [NOTICE](NOTICE).
