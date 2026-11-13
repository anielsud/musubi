# Data safety: upgrading other people's datasets

Status: approved 2026-11-12 and being built. Built so far: §3 items 1,
4, 5 and 6.

## 1. What changed

Until now Musubi ran on one dataset, ours, and whoever changed the code
also ran the upgrade. A loss cost a rebuild from the raw store: money
and time, little else.

From the first release, colleagues run it on their own Macs with their
own meetings, months of corrections and voice profiles, and upgrade
without us there. Some of it exists nowhere else: the recorder's audio
and transcripts, the owner's corrections, the voice profiles. A loss is
now someone else's, and may not be recoverable at any price.

## 2. What protects data today (verified 2026-11-12)

- The raw store is append-only; everything derived can be rebuilt from
  it (at a model cost).
- The owner's word (corrections, judgments, decided aliases, thread
  edits, to-do marks, the forget list) lives in tables a schema change
  keeps, and is applied again to whatever is rebuilt.
- New extraction and threading versions wait for an explicit redo
  (`knowledge.updates = "explicit"`).
- `migrate-from-tomoe` saves the config and voices before overwriting
  them, and never writes Tomoe's files.
- Config edits from the app keep the previous file as `config.toml.bak`.
- **New with this change:** a database of a newer schema or format is
  refused untouched, and an older one is copied before it is upgraded
  ([upgrades.md](upgrades.md), §7).

## 3. Gaps, and what to do about each

In order of what a loss would cost the owner.

1. **Newer databases were dropped or misread; upgrades weren't copied
   first.** Before this change, a schema mismatch in either direction
   dropped every derived table, and an older build misread float16
   vectors. *Built:* refuse newer, copy before upgrading (keep three),
   `[store] backup_before_upgrade`.
2. **The recorder's folder is backed up by nothing.** Its sessions hold
   the only copy of each meeting's audio and transcript; its voice
   profiles hold months of naming. "Back up now" copies the database, raw
   and inbox, not this. *Proposal:* "Back up now" includes the recorder's
   folder (APFS clones: no extra space until files change) and the data
   folder's `profiles/` and `headshots/`.
3. **No automatic backups, no restore.** Backups exist only when someone
   clicks. *Proposal:* a daily snapshot while the app is open (keep 7),
   and one before each destructive step: `forget`, `knowledge --redo`,
   the rethread in the work queue, `vectors encode`, a voice relearn,
   `migrate-from-tomoe`. Settings → Dataset lists the backups with
   **Restore** (quit, swap, reopen). Cost: disk, about one database's size
   per snapshot (430 MB on the largest dataset) since raw and sessions
   clone; a few seconds per snapshot.
4. **Migrations aren't recorded or tested on real old databases.**
   Today's upgrades are columns added on every open and one-off flags in
   `store_meta`. *Proposal:* a migration ledger (`migrations` table:
   id, applied at, by which build), each step idempotent and in one
   transaction; a fixture database from each release, built from the
   synthetic corpus and kept in `testdata/fixtures/`, which CI opens with
   the new build and checks: schema, format, row counts of the kept
   tables, search results. *Built:* `internal/store/migrate.go`: the
   one-off changes an open used to make (the schema check, columns added
   in place, the replaced-alias fix, the claim-centroid dedup) are named
   steps recorded in `migrations` (id, applied at, build), all pending
   ones run in one transaction with `schema.sql`; a database that
   already has a step's effect records it without changing anything. A
   pending step counts as an upgrade, so the copy above is made first.
   Fixtures `v0.1.0`, `pre-format` (db0bc02) and `pre-labels` (de7b3f6)
   in `testdata/fixtures/` (`make fixtures`); `make test` upgrades each
   and checks schema, format, ledger, the copy, row counts and the top
   five results of three searches. `thread_items` in them is a few
   invented rows (the synthetic eval doesn't thread); `pre-labels`
   predates corrections, aliases, thread edits and to-do marks.
5. **Nothing checks counts after an upgrade.** Our own dataset lost
   statements once to a cascade nobody noticed for a day. *Proposal:*
   after a migration or a redo, compare statements, thread items,
   corrections and voice profiles with before; a drop beyond what the
   step expects stops, says so, and offers the backup. *Built:* the
   upgrade transaction compares items, statements, thread items and the
   kept tables before it commits; a kept table losing rows, or items or
   statements dropping (other than by a schema change), rolls it back
   and the error names the copy. `musubi knowledge --redo` and the app's
   rethread copy the kept tables beside the database first, report the
   counts before and after, and put back rows a kept table lost, then
   fail (`[store] check_counts = false`: as before). Voice profiles are
   the recorder's, not counted here.
6. **Recorder data has no format version.** Sessions and voice files are
   read as they come; the voices file's `version` is written but never
   checked. *Proposal:* the same rule as the database: a format number
   in the recorder's folder, newer refused, older copied before
   conversion. *Built* (`internal/recorder/dataformat`): a `format` file
   in the recorder's folder (1). The recorder, finding a newer one,
   records nothing and changes nothing there, and says why in its
   health lines; the app's readers (ingest, the meeting's speakers, the
   calendar files) leave such a folder alone. An older or missing one has
   `voices/` copied to `voices.bak-<time>`, then the format recorded.
   Voices files: `version` checked on load; newer is neither read nor
   written (recording goes on, nobody learnt or named by voice, a health
   line). `voices/` is also copied before the first save of a file
   converted on load, before a relearn for new rules and before
   `learn_saved`; the newest five copies kept (with `migrate-from-tomoe`'s,
   same name), none made when the last matches.
   `[recorder.meeting] voice_profile_backups = false` turns the copies
   off. Session files have no version yet; their readers ignore fields
   they don't know, so a later one needs only the format bump.
7. **Upgrades cost money without warning.** A chunker change re-extracts
   the whole history, five meetings per update; a status version bump
   rewrites every thread's status. *Proposal:* any change that triggers
   model calls on existing data waits for the owner, with its cost shown
   in the work queue, like a rethread.
8. **No release discipline yet.** *Proposal:*
   - Tags `vX.Y.Z`; release notes with a **Data changes** section: what
     is migrated, whether it is one-way, what it costs, what to back up.
   - Our dataset first: a release candidate (`vX.Y.Z-rc1`, a pre-release)
     runs on ours for a day of meetings before the release.
   - A migration ships only after it has run on a copy of our dataset,
     with counts compared.
9. **Support without data.** When something goes wrong on someone
   else's Mac we can't look at their meetings. *Proposal:* Status →
   **Copy diagnostics**: versions, schema and format, counts, recent log
   lines, health. No meeting content, names or email.

## 4. Rules for a change that touches stored data

Until the items above are built, every PR that changes what is stored
(store schema, `store_meta`, vectors, the recorder's sessions or voices,
config meaning):

- prefers additive changes: new columns or tables, old ones left alone;
- makes a one-off change to existing data a new step at the end of
  `migrations` (internal/store/migrate.go), never an edit to a recorded
  one; each release adds its fixture to `scripts/fixtures.sh`;
- bumps `formatVersion` (internal/store/upgrade.go) when an older build
  would misread the result, and `schemaVersion` only when tables must be
  rebuilt; both get a line in the release notes;
- says in its description what an existing dataset goes through on
  upgrade, whether it is one-way, and what it costs in model calls;
- is run on a copy of our dataset before merging, with statement,
  thread item and correction counts compared.

## 5. Suggested order

1 and 6, done. Then 2 and 3 together (backups that cover everything, made on
their own, with restore), 8 (release discipline, cheap and immediate),
5, 4, 7, 9.
