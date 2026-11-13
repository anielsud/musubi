# Data safety: upgrading other people's datasets

Status: **proposed** 2026-11-12. Items 1, 7 and 9 of §3 are built; the
rest is for the owner to approve, item by item.

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
   tables, search results.
5. **Nothing checks counts after an upgrade.** Our own dataset lost
   statements once to a cascade nobody noticed for a day. *Proposal:*
   after a migration or a redo, compare statements, thread items,
   corrections and voice profiles with before; a drop beyond what the
   step expects stops, says so, and offers the backup.
6. **Recorder data has no format version.** Sessions and voice files are
   read as they come; the voices file's `version` is written but never
   checked. *Proposal:* the same rule as the database: a format number
   in the recorder's folder, newer refused, older copied before
   conversion.
7. **Upgrades cost money without warning.** A chunker change re-extracts
   the whole history, five meetings per update; a status version bump
   rewrites every thread's status. *Proposal:* any change that triggers
   model calls on existing data waits for the owner, with its cost shown
   in the work queue, like a rethread. *Built:* with
   `knowledge.hold_upgrades` (default on; `updates = "explicit"`),
   passages of extracted or summarised meetings that a new chunker or
   translator would re-cut, statuses under a new status version and
   names under a new naming version or model are kept, and counted.
   The rule: a stale stamp whose version-free input is unchanged (the
   raw record or the content, for passages; the statements, for
   statuses; the text, for names) changed only by an upgrade and waits;
   a changed input runs as before. Status → Work queue → **Apply
   upgrades** shows them with an estimate and runs them under the spend
   cap; `musubi knowledge --plan` lists them and `--redo
   extract,statuses,names` applies them. `hold_upgrades = false` redoes
   them on the next run, as before.
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
   lines, health. No meeting content, names or email. *Built:* Status →
   **Copy diagnostics** and `musubi diagnostics` (internal/diagnostics):
   versions, schema and format, counts, connections and recorder
   health, which provider and model each job uses, on/off settings, and
   recent error lines. Everything but the error and health lines is
   numbers, booleans and fixed strings; those lines lose email and web
   addresses, paths, quoted and bracketed text, whatever follows a word
   like "title" or "name", every capitalised word but product terms, and
   every word of the dataset's titles and names. A test plants names,
   addresses, titles and keys in a synthetic dataset, config and log and
   checks none reach the report.

## 4. Rules for a change that touches stored data

Until the items above are built, every PR that changes what is stored
(store schema, `store_meta`, vectors, the recorder's sessions or voices,
config meaning):

- prefers additive changes: new columns or tables, old ones left alone;
- bumps `formatVersion` (internal/store/upgrade.go) when an older build
  would misread the result, and `schemaVersion` only when tables must be
  rebuilt; both get a line in the release notes;
- says in its description what an existing dataset goes through on
  upgrade, whether it is one-way, and what it costs in model calls;
- is run on a copy of our dataset before merging, with statement,
  thread item and correction counts compared.

## 5. Suggested order

1, done. Then 2 and 3 together (backups that cover everything, made on
their own, with restore), 8 (release discipline, cheap and immediate),
5, 4, 6, 7, 9.
