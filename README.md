# Musubi

A local-first personal knowledge base. It mines meetings (recorded by its own
recorder or by Tomoe, plus Teams transcripts), calendar, email, chat and
CRM records into people, accounts, threads, decisions and commitments, and
surfaces them live in a side panel during meetings as topic cards.

Docs:
- **New here? [user-guide.md](docs/user-guide.md):** quickstart and a tour of the app, with screenshots
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
- [local-models-2026-10.md](docs/local-models-2026-10.md): local model
  measurements
- [tomoe-live-feed.md](docs/tomoe-live-feed.md): the live feed protocol
  (Tomoe's, and Musubi's recorder's)
- [recorder-screens.md](docs/recorder-screens.md): Musubi's own recorder in
  the app: recording, voices, speakers, settings, standalone from Tomoe
- [recorder-parity.md](docs/recorder-parity.md): checks that the recorder
  gives the same results as Tomoe, and how to install it
- [recorder-provenance.md](docs/recorder-provenance.md): which recorder code
  came from Tomoe, and under what rules
- [teams-design.md](docs/teams-design.md): Microsoft 365 through Graph
  (calendar, Teams transcripts, meeting chats, directory, mail and chats)
- [mcp-design.md](docs/mcp-design.md): `musubi mcp`, Musubi's knowledge for
  Claude Code
- Findings, dated, with numbers: [research-log.md](docs/research-log.md)

## Use

**Setup, hosted models (Claude Haiku through the AI Gateway, GLM on
Baseten, Jev), keys, building a dataset and what processing costs:
[docs/setup.md](docs/setup.md).** In short: Go, a C compiler (cgo), Node,
and [Ollama](https://ollama.com) with `ollama pull nomic-embed-text
qwen3:8b`; hosted models are configured per dataset, with keys in the
macOS keychain.

### Desktop app

To install without building, use the DMG from
[Releases](https://github.com/anielsud/musubi/releases): see the
[user guide](docs/user-guide.md). From source:

```sh
make install-gui-mac    # builds /Applications/Musubi.app and adds it to the Dock
make recorder-app && scripts/install-recorder.sh   # the meeting recorder, started at login
```

Meetings and Live briefings come from the recorder of record, `[recorder]
of_record`: `"tomoe"` (the default, unless the first launch found no
Tomoe; Live needs Tomoe's live feed, in Tomoe
Settings → Sharing → Live feed, or `[meeting] live_feed = true`) or
`"musubi"` (Musubi's own recorder; Settings → Recording). Without a
meeting, the Live tab can replay a past one. The app starts Ollama if it
isn't running. Details: [docs/setup.md](docs/setup.md).

### CLI

```sh
make build
bin/musubi update                       # ingest meetings (recorder of record) + inbox, process
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
bin/musubi m365 connect                 # sign in to Microsoft 365 (then: status, sync, signout)
bin/musubi mcp                          # MCP server for Claude Code (docs/mcp-design.md)
make test eval                          # synthetic corpus; fails on any leak
```

Config: `~/.config/musubi/config.toml` (owner email and internal domains at
least). Calendar, mail and chat arrive as JSON envelopes in
`~/.local/share/musubi/inbox/<source>/` (format in
`internal/source/mcpdrop`). `MUSUBI_CONFIG` points the CLI and the app at
another config, for experiment instances beside the real one. The app opens
the dataset last opened from `~/.config/musubi/datasets.toml`, else its
first entry:

```toml
[[dataset]]
key = "all"
name = "All"
config = "/path/to/datasets/all/config.toml"
note = "what is in it"
```

Knowledge extraction and threading run best on a larger local model
served by llama.cpp (`brew install llama.cpp`); the measured settings are
in architecture.md ("Points", "Threads") and docs/local-models-2026-10.md.
Thread grouping is `[knowledge] grouping = "threads"` with
`[knowledge.threads.model]` pointing at that server.

Proprietary; all rights reserved. See [NOTICE](NOTICE).
