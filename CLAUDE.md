# Musubi — project instructions

Local-first knowledge base: ingest → immutable raw store → items → versioned
derive stages → SQLite (FTS5 + sqlite-vec + graph tables) → scoped reads for
the side panel, explorer and any automatic use. Design: `docs/architecture.md`.
Keep that doc current when the design changes.

## Rules

- **Private and proprietary.** No license file; `NOTICE` reserves all rights.
  Never add GPL, AGPL or LGPL dependencies (check before `go get` / `npm i`).
  Never copy code from Tomoe (GPLv3); integrate only through its session files
  and its live stream.
- **No real data in git.** Meetings, email, chat, names and labelled references
  live in `testdata/private/` (gitignored) or `~/.local/share/musubi/`. Tests
  use synthetic data with invented names. Stage files by name; check
  `git diff --cached --name-only` before committing.
- **Every read is scoped.** `store` must not expose a query that takes no
  `Scope`. New derived tables carry provenance and an inherited sensitivity
  label. Unknown sensitivity fails closed (`confidential`).
- **Raw is immutable.** Stages write stamped rows `(stage, stage_version,
  input_hash)`; fix a stage by bumping its version and re-running (for the
  knowledge stages, `musubi knowledge --redo <stage>`: a new version is
  never applied to earlier items silently; see architecture.md, "Derive
  stages").
- **New behaviour gets a config key** that restores the previous behaviour.
- **Measure before changing defaults;** write findings, dated and with numbers,
  into `docs/`.

## Conventions

- Go, all packages under `internal/`; platform code in `_darwin.go` /
  `_linux.go`. Build and test specific package paths. `make vet test` cover
  everything except the Wails app (`cmd/musubi-gui`, `internal/gui`), which
  needs a webview; `make build-gui` builds it (frontend in `frontend/`,
  React + TypeScript + Vite, embedded from `cmd/musubi-gui/frontend/dist`).
- The app only shows data through scoped reads; the live panel's scope
  follows the screen-safe toggle. The approved hosted endpoints (Baseten,
  Vertex) may read any data, meeting text included (owner's decision,
  2026-10-16), and so may Claude through Claude Code via `musubi mcp`
  (2026-11-06; results labelled, not filtered: docs/mcp-design.md); the
  code still defaults to local models, and points, threads and statement
  labels use a hosted one only with `knowledge.allow_hosted`.
- Config is TOML at `~/.config/musubi/config.toml`, loaded over
  `DefaultConfig()`. Data lives under `~/.local/share/musubi/`.
- Comments explain why, not what.
