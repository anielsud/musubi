# Upgrading Musubi, and moving over from Tomoe

For someone who already runs Musubi with their own data, usually with
Tomoe recording: how to upgrade without losing anything, switch to
Musubi's own recorder, bring in older Teams transcripts, rethread, and
let the voice profiles relearn. Last section: what every build does to
keep an upgrade from losing data.

Command paths below are for the app installed from the DMG. From a
checkout they are `bin/musubi` and `bin/musubi-recorder`.

```sh
musubi=/Applications/Musubi.app/Contents/MacOS/musubi
recorder="/Applications/Musubi.app/Contents/Helpers/Musubi Recorder.app/Contents/MacOS/musubi-recorder"
```

## 1. Before upgrading

- Not during a meeting.
- **Settings → Dataset → Back up now** (or `$musubi backup now`). It
  copies the database, everything else in the data folder, the
  recorder's folder (`~/.local/share/musubi/recorder`, but its speech
  models, which download again) and the config to
  `~/.local/share/musubi/backups/`. A Musubi from before §7's backups
  copies only the database, raw store, inbox and config: copy the
  recorder's folder, `profiles/` and `headshots/` by hand if you have
  them.

## 2. Install the new version

- **From the DMG:** quit Musubi, drag the new Musubi to Applications.
  See the [user guide](user-guide.md#updating) for the first open.
- **From source:** `git pull`, then `make install-gui-mac`, and
  `make recorder-app && scripts/install-recorder.sh` if you use
  Musubi's recorder.

If you installed Musubi's recorder from source before, that one keeps
running: the app installs the recorder it carries only when none is
installed. To move to the app's, while no meeting is being recorded:

```sh
launchctl bootout gui/$(id -u)/com.anielsud.musubi.recorder
rm ~/Library/LaunchAgents/com.anielsud.musubi.recorder.plist
```

then open Musubi.

## 3. The first open

The new version upgrades the database the first time it opens it, after
copying it to `backups/pre-upgrade-<old version>-<time>.db` in the data
folder (§6). The 2026-11-12 changes (claim centroids stored once, vectors
as float16) are one-way: a version from before them misreads the
upgraded database, so going back means restoring that copy.

Nothing is re-extracted or rethreaded on its own because the code
changed: a new extraction or threading version waits for you
(`knowledge.updates = "explicit"`). So, on meetings that haven't
changed, does what a new version would otherwise redo at a model's cost
on the next update (`knowledge.hold_upgrades`): passages a new chunker
would cut again (and so extract again), thread statuses under a new
status version, meeting names under a new naming version or model. New
meetings, and ones whose recording or transcript changed, are processed
as before. **Status → Work queue** lists what waits, with an estimate,
and **Apply upgrades** runs it under a spending cap. To see what is on
older versions:

```sh
$musubi knowledge --plan
$musubi knowledge --redo extract,statuses,names   # applies what waits
```

**Settings → Processing → Use recommended** sets the hosted models and
now also turns on what they are for: meetings read into threads
(`knowledge.extract = "lean"`, `knowledge.grouping = "threads"`),
meeting summaries with to-dos, and Live's briefs. On a dataset that used
other settings, the change applies to new meetings; the rest follow when
you rethread (§5).

**Defaults changed on 2026-11-12.** Settings your config doesn't set now
follow the setup Musubi is developed on: meetings read into threads
(lean extraction), summaries with to-dos, Live's briefs, voice profiles,
meeting rooms split into their people, and the recorder's speaker
tuning. The full list, old and new values, is in
[research-log.md](research-log.md) ("the defaults follow the owner's
setup"). New meetings use them at once; existing ones follow when you
rethread (§5). To keep an old behaviour, set its key in your config.

## 4. Switch to Musubi's recorder

Tomoe can keep running beside it as a fallback; Musubi takes meetings
only from the recorder of record.

1. **Settings → Recording → switch to Musubi's recorder.** From then on
   Musubi imports only what Musubi's recorder records (`[recorder]
   since`, the moment you switched). Don't switch back and forth: each
   switch to Musubi moves `since`, and recordings made before it aren't
   imported.
2. **Copy Tomoe's data in**, checking first:

   ```sh
   "$recorder" migrate-from-tomoe --dry-run
   "$recorder" migrate-from-tomoe
   ```

   It copies Tomoe's sessions (as APFS clones: no extra space), voices,
   models and recording settings into the recorder's folder, never
   changes Tomoe's, saves your config and voices before overwriting
   them, and then sets `[recorder] tomoe = "off"`. Meetings already in
   Musubi keep their ids and aren't imported twice. Safe to run again: it
   repairs what is missing.
3. **Restart the recorder after the copy, not before:** Status → Start
   the recorder, or

   ```sh
   launchctl kickstart -k gui/$(id -u)/com.anielsud.musubi.recorder
   ```

   then quit and reopen Musubi. The voice relearn below runs once, over
   the sessions in the recorder's folder at that start.

## 5. Bring in older history, and rethread

**Older Teams transcripts.** Each sync reads 30 days back (`[m365]
past_days`); the six weeks of a first sync only apply to a new
connection. For more, once:

```sh
$musubi m365 sync --days 180
$musubi update
```

Transcripts of meetings you were invited to but weren't in are passed
over unless `[m365] unattended = true`. A meeting Tomoe also recorded
becomes knowledge once (`knowledge.prefer_source`: Tomoe's copy first).

**Rethread.** Meetings older than what is already threaded wait (they
can only join the threads in time order). **Status → Work queue →
Rethread everything in time order** shows the cost and time first and
stops at a spending cap; or

```sh
$musubi knowledge --plan
$musubi knowledge --redo threads
```

Your corrections, merges and splits, decided aliases and to-do marks
are kept and applied again. Measured cost: about $0.06 per meeting hour
with Claude Haiku 5.5 (extraction and threading $0.04, statuses and
summaries $0.02; [research-log.md](research-log.md), 2026-11-11), about
4–6¢ per meeting with GLM before it ([setup.md](setup.md) §8).

## 6. Voices

**Voice profiles** (Settings → Recording) are on by default; if your config
turned them off, turn them on. The
copy brought Tomoe's profiles over. At the next recorder start the
profiles are relearnt once from every saved meeting under the current
learning rules: in the background, on this Mac, no model calls, paused
while a meeting records. Named and confirmed meetings are kept; then
work through Explore → Voices → Who's this? (the user guide, "Explore").

## 7. What every upgrade does to keep your data

Since 2026-11-12 (`internal/store/upgrade.go`):

- **A newer database is refused, untouched.** The database records its
  schema and its format (one-way changes to what is stored). A version
  that finds either newer than it knows stops with "this database was
  written by a newer Musubi" instead of dropping derived tables or
  misreading vectors. Builds from before this didn't check: don't open
  an upgraded database with one.
- **An older database is copied before it is changed:** to
  `<data folder>/backups/pre-upgrade-s<schema>-f<format>-<time>.db`, the
  newest three kept. If the copy fails (no space), the upgrade stops
  before anything changes. `[store] backup_before_upgrade = false` turns
  the copy off.
- **To go back:** quit Musubi (and stop `musubi mcp` servers), replace
  `musubi.db` in the data folder with the copy (remove `musubi.db-wal`
  and `musubi.db-shm` beside it), and install the version you came from.
- **The recorder's folder has a format too** (a `format` file in it). A
  recorder that finds a newer one records nothing and changes nothing
  there, and Status says "written by a newer Musubi"; Musubi doesn't
  read its meetings. An older folder has its `voices/` copied to
  `voices.bak-<time>` before the format is recorded, and again before
  voice profiles are relearnt or converted (the newest five kept;
  `[recorder.meeting] voice_profile_backups = false` turns the copies
  off). A voices file from a newer Musubi is left alone: meetings still
  record, but nobody is named by voice. To go back, put a
  `voices.bak-<time>` folder in place of `voices/` while the recorder is
  stopped.

Also since 2026-11-12 (`internal/backup`), backups of the whole dataset are
also made on their own, in `~/.local/share/musubi/backups/`, each with a
`manifest.json` saying what it is:

- **Daily** while the app is open (not during an update or a
  recording); the newest 7 kept.
- **Before a bulk change:** `musubi forget`, `musubi knowledge --redo`,
  `musubi vectors encode`; in the app, Catch up and Rethread (Status →
  Work queue), deleting a meeting, renaming a voice's speakers. The
  newest 5 kept. If the backup fails (no space), the change doesn't run.
- **Settings:** `[backup] daily`, `keep_daily`, `before_changes`,
  `keep_before`; false or 0 turns each off. Backups made by hand, and
  those made before a restore, are never removed.
- **To restore:** Settings → Dataset → Backups → **Restore…**, or
  `$musubi backup list` and `$musubi backup restore <name>` (refused while
  the app has the dataset open; stop `musubi mcp` servers first). The
  dataset as it is is backed up first ("before a restore"): restoring that
  undoes it. The data folder is put back; the config isn't (the backup's
  copy is in its `config/` folder). The recorder's sessions and voices are
  put back only when asked (`--recorder`), never while it records;
  otherwise meetings recorded since stay and are read in again by the
  next update. A backup made before manifests (an older "Back up now")
  is listed but restored by hand.

For developers, the rules a change to what is stored must follow are in
[data-safety.md](data-safety.md).
