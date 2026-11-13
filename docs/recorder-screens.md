# Recorder screens in Musubi

Status: agreed 2026-11-11; built (PRs #34–#36, merged 2026-11-12).

Step 5 of bringing Tomoe's engine into Musubi: the screens that let the
owner stop opening Tomoe's window. Steps 1–4 are in
[recorder-parity.md](recorder-parity.md) and
[recorder-provenance.md](recorder-provenance.md).

## What we know

- **Tomoe's window** has about 35 actions across 8 panels. The ones used
  after a meeting are the Voices panel's review queue ("Right" / "Not them"
  / "Someone else" / "Can't tell", "Who's this?", "Window or voice?", "Same
  person?"), voice profile edits (rename, confirm, start over, forget, move
  or discard a meeting), renaming a speaker in a transcript, export and
  delete.
- **Musubi shows Tomoe's data read only**: the live transcript (Live tab),
  a headshot and one voice clip per person (People), and meeting digests
  (Meetings). It reads `session.json` lines and speakers, `audio.m4a` and
  `looks/`, never `voices/`. It writes `calendar.json` and `title.json`
  into session folders.
- **The recorder has the storage operations but not the glue.** It carries
  Tomoe's voice store whole (rename, confirm, merge, keep apart, start over,
  forget, move, reject), `LearnSaved`, `RenameLocked`, the session store,
  export and the looks reader. Tomoe's `internal/backend` functions that
  tie these to a saved meeting (`RenameSpeaker`, `NotThem`,
  `SkipVoiceName`, `SkipVoiceDoubt`, `MoveVoiceMeeting`, the Voices view)
  were not brought over. No command or socket exposes any edit.
- **Paths.** Musubi reads sessions from config `tomoe_sessions` and the
  live feed from `[live] tomoe_socket`, both defaulting to Tomoe's folder.
  The recorder writes `~/.local/share/musubi/recorder/{sessions,live.sock}`
  and copies Tomoe's voices at the start of each recording.
- **A changed session re-runs the AI steps for that meeting.** Import
  compares the file's hash (`internal/pipeline/pipeline.go`), so any edit
  to `session.json` (a renamed speaker, say) re-extracts that meeting with
  the hosted model.

## The gap

Musubi can show a meeting but cannot correct who said what, and voice
profiles can only be fixed in Tomoe's window. Those are the two things the
owner needs before Tomoe can go.

## Proposal

### One rule: only the recorder writes its folder

The recorder holds voices in memory while it records, and a meeting can be
running at any time. So Musubi never writes the recorder's sessions or
voices itself; it asks the recorder over a second local socket,
`control.sock` (one JSON request, one JSON reply, owner-only file
permissions). If the recorder is not running, edits are unavailable and the
screen says so. The live feed socket stays as it is.

### What comes over

1. **Recording on the Status tab**: recording or idle, which meeting and
   for how long, Start and Stop (detection stays automatic; Start is for a
   meeting it missed, Stop for one it caught by mistake), the sources in
   use, and a health block (permissions, models, tools, each with its fix).
2. **Sources that look after themselves.** When a recording has a source
   with no signal (a silent mic, a meeting app producing no audio), the
   recorder listens briefly to the other sources and, if one has signal,
   Musubi suggests switching to it, one click. The chosen default sources
   live in Settings. A mic that only hears the meeting audio
   from the speakers is not offered (`Status.suggest_note` says so;
   research log, 2026-11-12).
3. **Diagnostics on the Status tab**: Tomoe's live-labelling pane (who the
   diarizer thinks is speaking, voice matches, window hints) beside
   Musubi's own pipeline traces.
4. **What Musubi saw, in its thinking view.** The meeting-window looks
   (Tomoe's hint timeline: tiles, active speaker, names read from the
   window) join the trace of how Musubi is thinking, live during a meeting
   and on the meeting page afterwards.
5. **Speakers on the meeting page** (Meetings): the meeting's review queue,
   same choices as Tomoe's Voices panel, each with its voice clip and
   video tile; rename a speaker from the transcript; delete the meeting.
6. **Voices on the person page** (People): the person's voice profile with
   its meetings; rename, confirm, start over, forget, move or discard a
   meeting (to a profile picked from a list, so two of one name are told
   apart, or to a new profile); "Same person?" and "Two profiles named X?"
   suggestions (the latter: same person merges them, different people
   keeps them apart and offers to rename one). Open reviews are counted on the
   People tab and listed there.
7. **Live**: Musubi follows the recorder's socket; rename a speaker during
   the meeting.
8. **Settings, Recording section**: which recorder is of record, default
   sources, the meeting window to watch, mic voices, and the rest of
   Tomoe's settings that still apply.

The recorder stays its own process, started at login by launchd and
always on. A menu-bar item showing its state may come later.

### What stays behind

Hotkey, dictation and other languages (dropped in step 2, and with them
retranscribing in another language), export (Musubi's own views and MCP
replace it; can be added later), title editing (titles come from the
calendar). (2026-11-12: a meeting's transcript can be saved as text from
its page, in the scope shown.)

### Settings

The recorder's settings move into Musubi's one config file under
`[recorder]`, edited from Settings like the rest. A key set there wins;
anything not set still comes from Tomoe's config until Tomoe is retired
(`[recorder] tomoe = "off"`, below).

### Shadow first, then the switch

Musubi gets a `[recorder]` setting `of_record` (`tomoe` or `musubi`,
default `tomoe`, so nothing changes until the owner switches) and the
recorder's sessions folder and sockets. Recording, sources, health and
diagnostics always show the Musubi recorder; meetings, Live and voices
follow whichever recorder is of record. On switching, Musubi imports only
recorder sessions that start after the switch, so shadow recordings of
meetings Tomoe already gave it are not imported twice.

While Tomoe is of record the recorder still copies Tomoe's voices at each
recording, which would overwrite voice edits made in Musubi; that is why
voice and speaker edits are offered only once the Musubi recorder is of
record.

### Standalone (2026-11-12)

After the switch Musubi still read Tomoe in four places: the recorder's
unset settings (Tomoe's `config.toml`), its models (Tomoe's folder as a
fallback), the app's meeting files for meetings imported from Tomoe
(headshots, voice clips, replay audio from `tomoe_sessions`), and the
calendar context it wrote into Tomoe's sessions. `[recorder] tomoe`
(`"on"`, the default, or `"off"`) ends that.

- **Migration**: `musubi-recorder migrate-from-tomoe` copies Tomoe's data
  in, then sets `tomoe = "off"` in Musubi's config. It never writes
  Tomoe's folder or config and never overwrites a recorder session, so
  it can be run again to repair. Sessions: every Tomoe session folder
  created before `--until` (default `[recorder] since`; later ones Musubi's
  recorder recorded itself) that the recorder lacks, into
  `<data>/sessions/<same uuid>` as APFS clones (`cp -c`, else a plain
  copy) through `<data>/migrate-tmp`, with the copy's `audio_path`
  pointed at its own `audio.m4a` (Tomoe writes an absolute path into its
  folder); the copy's `session.json` must parse before it is renamed into
  place. Voices: Tomoe's `*.json` over the recorder's, except where the
  recorder's file is newer (kept, and said), with what is replaced kept in
  `<data>/voices.bak-<date>` (the newest five such copies kept, shared
  with the copies below). Models: each of Tomoe's not already in
  `<data>/models`, cloned. Settings: every key Tomoe's config sets in
  `[audio]`, `[transcription]` and `[meeting]` that the recorder reads and
  `[recorder.*]` doesn't set, written with the app's comment-keeping
  config editor, `model_path` set to `<data>/models`; the file is backed up
  first as `<name>.bak-<date>-standalone`. `tomoe = "off"` is written only
  if every step succeeded. `--session <uuid>` copies one session whatever
  its date and does nothing else; `--dry-run` changes nothing. It refuses a
  config still of record `"tomoe"`. Meetings already imported stay as
  they are (`tomoe:<uuid>`); the copies are older than `since`, so ingest
  does not take them again.
- **Recorder with `tomoe = "off"`**: never reads Tomoe's `config.toml`
  (built-in defaults with Musubi's `[recorder.*]` over them, models in
  `<data>/models`), never searches Tomoe's models folder
  (`config.ModelDirs`), never copies Tomoe's voices, and does not watch
  Tomoe's config file. A change of the key through `reload_config` asks
  for a restart. `shadow compare` still reads Tomoe's sessions, read-only:
  it is a diagnostic. The offline `session replay` and `textdiff`
  commands still default to Tomoe's folder and config, as before.
- **App**: with `of_record = "musubi"`, calendar context
  (`calendar.json`, `title.json`, `teams.json`) is written only into the
  recorder's sessions, never Tomoe's, whatever `tomoe` says. These are
  files the recorder only reads, so this is the one exception to "only
  the recorder writes its folder". With `tomoe = "off"`, headshots, voice
  clips and replay audio come only from the recorder's folder
  (`Config.SessionDirs`). Status's live feed row and the header dot name
  the feed of record ("Live feed · Musubi recorder", "L") once Musubi's
  recorder is of record. Settings → Recording and Processing say that
  unset values are the recorder's defaults ("· default") with Tomoe off.
- `tomoe_sessions` stays in the config, for the migration's repairs and
  for going back (`tomoe = "on"`).

### How Musubi talks to the recorder

A second socket beside the live feed, `control.sock` in the recorder's
folder (owner-only permissions): one JSON request per line,
`{"v":1,"id":7,"op":"rename_speaker","args":{...}}`, one reply per line,
`{"v":1,"id":7,"ok":true,"result":{...}}` or `{"ok":false,"error":"..."}`.
Operations: status, start, stop, sources, set_source, session,
rename_speaker, not_them, skip_voice_name, skip_voice_doubt, voices,
rename_voice, merge_voices, keep_apart, forget_voice, start_over,
confirm_voice, remove_voice_meeting, move_voice_meeting, learn_saved,
voice_clip, discarded_clip, looks, look_tile, delete_session, tools,
fix_tool, reload_config, (2026-11-12, for Settings → Models)
models and download_model, (2026-11-12) relabel_profile_meetings and
undo_relabel, and (2026-11-12) return_voice_meeting and
skip_returned_voice. Types and a client are in
`internal/recorder/control`.

The live feed gains message types old readers ignore: `status`
(recording state and sources, on every change), `audio` (a silent source
and, if found, a source with signal), `diag` (live labelling) and `look`
(a meeting-window look).

### Order of work

1. **Recorder**: the control socket, the new feed messages, Tomoe's
   backend glue for each operation (lifted where the owner wrote it,
   rewritten where upstream did, per the provenance rules), the source
   check, and `[recorder]` settings from Musubi's config.
2. **Musubi**: the settings keys and recorder client, then the Status,
   Settings, Live, meeting page, People and thinking-view changes, and
   delete (the recording through the recorder, the meeting's knowledge
   through Musubi's forget).

## Trade-offs and decisions

- **Re-extraction cost.** A renamed speaker changes the session file, so
  that meeting is re-extracted (one meeting's hosted calls), rather than
  only relabelling existing statements whose wording may assume the wrong
  speaker. Statement counts are checked after, as for any re-run. A pure
  label change is relabelled instead, with no model calls (`[recorder]
  relabel`; "Renaming a voice profile renames its speakers", below).
- **Control socket vs Musubi writing files.** The socket is a small
  protocol to maintain; writing files directly would race the recorder
  during a meeting and could lose a voice edit or a recording.
- **Edits unavailable when the recorder is down.** Acceptable: launchd
  starts it and restarts it after a crash. The screens say when it is not
  running.
- **Delete reaches into Musubi's knowledge.** Deleting a meeting removes
  its statements from threads, as `forget` does for other sources.
- **Decided 2026-11-11** (owner): manual start and the source picker stay;
  diagnostics move to Status; the hint timeline joins the thinking view;
  build all of it and refine from there.

## Renaming a voice profile renames its speakers (2026-11-12)

**Known.** Renaming a profile (`rename_voice`) changes only the profile;
its old name is kept in `Profile.Aliases`. The meetings it identified
keep the old name in their speaker labels ("Person 3 (Corner Room 2)", a
meeting room's tile, after the profile became "Ana Example"), and so do
Musubi's copies: the item's lines, its passages ("Corner Room 2: ..."),
statement holders and the
prose written from them, digest people and to-do owners. Re-extracting
those meetings would fix it at hosted cost, for a change that is only a
label.

**Proposal** (agreed with the owner; `[recorder] relabel`, default true;
false keeps today's behaviour, profile rename only):

1. **Recorder: `relabel_profile_meetings`** (model, profile id). In every
   saved session the profile identified (its heard meetings, and sessions
   whose `voice_names` or lines' `voice_match` name it by id), the labels
   that carry one of its old names become the new name: "Person 3 (Corner
   Room 2)" → "Person 3 (Ana Example)", a plain "ana@vendor.example" →
   the new name.
   Labels only; line text is never touched. A label the profile named
   (voice name or voice match) qualifies by itself. In a session the
   profile only heard, one label carrying the old name is it; when
   several do (a room tile covers everyone in the room), only those whose
   voice in the session's fingerprints is as like the profile as naming
   requires (`Threshold`) qualify, or, with no fingerprints, the one
   speaking during the profile's clip; the others are listed as left
   alone, as are the others once one speaker there has the profile's name.
   Only the recorder's own sessions are written, never the one
   recording or being saved. Each relabel (batch, session, old label, new
   label) is appended to `relabels.jsonl` in the recorder's folder;
   `undo_relabel` (batch) reverses a batch. The reply lists what changed
   and what was left. It works for a rename made earlier, too.
2. **Musubi: a relabel of its stored copies with no model calls**
   (`pipeline.Runner.RelabelSpeaker`, applied by
   `store.RelabelSpeaker` in one transaction). The item is taken as ingest
   will translate the rewritten `session.json` (or, without the file, by
   name); it must differ from the stored one in speakers only and give the
   same passage boundaries, else nothing is changed and the meeting is
   left to a normal update. Rewritten: the item's lines, participants and
   `content_hash`; passages' speaker prefixes, speaker lists and text
   hashes (judgements anchored to them follow); statement holders (by the
   speaker of the line each cites; a name gone from the meeting
   everywhere), `holder_id`, `who`; digest section people, to-do owners
   and requester. Prose written from the label (statement text, section
   and to-do text, the overview, and a thread's name, summary and status
   when no statement of the thread still has the old name) changes only
   for an old name that is gone from the meeting and never spoken in it,
   as a whole word. Stamps: the chunk stamp is rewritten, and the stamp it
   replaces is kept (`chunk-relabelled`) so the knowledge stages see that
   their stored output came from the same passages before the relabel and
   restamp instead of extracting or digesting again (or, for output kept
   from an earlier version, restamp its input, so it stays kept rather
   than counting as changed input); thread statuses are
   restamped in the transaction. Changed passages and statements are
   re-embedded with the local embedder afterwards (left to the next update
   if it is down). `raw_manifest` is not touched: the next import of the
   rewritten file translates to the same `content_hash`, so ingest sees no
   change. A before-and-after record of every field (`speaker_relabels`)
   makes it undoable. Then the deterministic local steps run as on any
   import that brings a new name (registry, links, classify, resolve):
   entities follow the speakers, so the old name's speaker-only entity
   goes when no meeting has that speaker any more, and the new name
   joins a directory person when it matches exactly one.
   The CLI does the same: `musubi relabel --profile MODEL/ID`, `--item ID
   --session FILE` or `--from X --to Y`, and `--undo BATCH`.
3. **App.** After a profile rename: "Also rename them in the N meetings
   where their voice was recognised" (checked). A profile whose aliases
   hold an old name gets "Rename in meetings". Refused while an update
   runs; shows "Renamed in N meetings" with Undo (the recorder's batch,
   then Musubi's records). Renaming a speaker on a saved meeting uses the
   same relabel when it is a pure label change.

**Trade-offs.** A label is the evidence a statement's holder came from,
so relabelling statements keeps them true to the transcript; prose that
names the old label is rewritten only where the old name cannot mean
anything else (gone from the meeting, never spoken). Where it is still
in use (another speaker kept it) prose is left and counted. Passages
whose boundaries would move need a re-extraction, so the relabel refuses
them: two speakers merged, or one split, as when a room's tile named two
voices alike and only one is the profile's (Musubi's copy had them as one
speaker; the dry run below). The recorder has then already relabelled its
file: Undo puts it back, or a later import re-extracts that meeting.

**Dry run (2026-11-12),** on a copy of the all-t4 backup and of the one
affected session, for a profile renamed from a room tile: the recorder
relabelled 84 lines of one diarized speaker and left the other speaker
with the tile's name, whose voice was 0.39 like the profile (the
transcript suggests that one is the person the new name belongs to, so the
profile may be someone else's voice: the owner checks its clip first).
Musubi refused that relabel (the passages would break at the new speaker
change) and wrote nothing. Relabelling every line with the tile's name
instead (the session file rewritten that way, or by name): 115 lines, 37
passages, 54 holders, 49 statement texts, 12 digest sections, 1 to-do, 1
thread name and summary and its status, 37 passages and 49 statements
embedded again locally; statements 38,696 and thread items 36,512 before
and after; the import of the rewritten file then fetched 1, changed 0
and ran no stage; the knowledge plan was the same before and after (820
items waiting, from the dataset's held backlog); no hosted call (the
spend ledger stayed empty). The registry, links, classify and resolve
re-ran for all 1,593 items (local, part of the 27 s run). The old name's
speaker-only entity went; the new name joined one directory person. The
room's calendar resource, which has the tile's name, was left as it is.

## Voice profile edits after samples (2026-11-12)

**Known.** With samples on the profile card (a stretch that isn't the
person is taken out one by one), "Start over" no longer earns its button;
"Forget" is only needed when someone asks not to have their voice kept; a
meeting row's "Discard" threw the meeting's voice away, so a speaker the
profile wrongly named kept the profile's name in that meeting and was
never asked about again. "Who's this?" covered 14 days. The Voices op read
every session file twice per call (the titles of every meeting, then each
recent one), 0.68 s warm and 3.2–3.4 s on the first call after start,
on a copy of the recorder's 207 sessions (91 MB of `session.json`).

**Agreed with the owner, built:**

1. **Start over** leaves the profile card (`start_over` stays in the
   protocol for compatibility).
2. **Forget** becomes **Delete voice print**, behind a "⋯" menu on the
   profile, confirmed with: "Delete <name>'s voice print and never learn
   the name '<name>' again — for someone who asked not to have their voice
   kept. Recordings and transcripts are unchanged." (`forget_voice`, as
   before).
3. **Not them sends the voice back.** A meeting row's "Discard" becomes
   "Not them" (`return_voice_meeting`): the meeting is taken out of the
   profile as before (`Store.ForgetMeeting`: never learnt from again),
   and the speakers the profile gave a name there go back to unnamed
   ("Person 3 (Ana)" → "Person 3"; a fresh "Person N" when another speaker
   has that label or the label has no number, so two speakers never become
   one). Which speakers: those the profile named by voice (`voice_names`,
   lines' `voice_match`); in a meeting it learnt from the person's tile,
   the label carrying its name, or of several (a room's tile) those whose
   voice is the profile's, as a relabel picks them. Their voice names,
   voice matches and doubts are dropped, and the session records them in
   `returned_voices` (label, the profile's name, when), so "Who's this?"
   shows them, with their clip and "You said this isn't <name>", whatever
   their age or length. Naming one there renames the speaker (and, as any
   rename, teaches the profiles: an existing one or a new one); "Can't
   tell" (`skip_returned_voice`) stops asking and leaves it unnamed. The
   labels change only through the cheap relabel: the app first asks the
   recorder for the rewritten session without saving it (`preview`), asks
   Musubi whether its copy can take it as a pure relabel
   (`pipeline.RelabelRequest.Check`, which writes nothing), then has the
   recorder save it (logged in `relabels.jsonl` under a batch) and
   relabels Musubi's copy (`RelabelMeeting`), with no model calls. When
   Musubi would refuse (passage boundaries would move) the recorder only
   takes the meeting out of the profile, the labels stay, and the result
   says so. A meeting Musubi hasn't imported yet is relabelled in the
   recorder and imported as it is. With `[recorder] relabel = false` "Not
   them" takes the meeting out and leaves the labels (Discard's
   behaviour). "Someone else…" stays as the direct shortcut (move the
   meeting to a profile or a new one).
4. **"Who's this?" covers 90 days** (`unknownDays`); "Named by voice" and
   "Window or voice?" stay at 14 (`reviewDays`): at 90 days the copy below
   would list 26 voice-named speakers instead of none.
5. **A review index in the recorder** (`reviewindex.go`). The review
   lists are gathered once, in the background, when the recorder starts,
   and kept in memory per session (title, date, the items, and each
   nameless voice's fingerprint sum from `diarization.gob`, read uncached).
   A session is read again when the recorder saves it (every save goes
   through `saveSession`: answers, renames, relabels, a new recording, the
   cut refinement), after the diarization worker rewrites it, and dropped
   when deleted. Each Voices op stats the session folders and reads again
   only a session whose `session.json` (or, with nameless voices, its
   `diarization.gob`) changed on disk, which also catches writers outside
   the recorder. What depends on the profiles ("sounds like", whether a
   window's name has a profile) is worked out when the lists are read, so
   a profile edit needs no update of the index. Until the first build is
   done the Voices op returns `building: true` with empty lists, and the
   app shows "building…" and asks again every 2 s. The index isn't
   persisted.

**Measured (2026-11-12)** on an APFS clone of the recorder's sessions and
voices (207 sessions from 2026-09 to 2026-11, so 90 days is all of them;
185 with fingerprints), warm disk cache, a test recorder pointed at the
copy: the index builds in 0.73–0.78 s (10 nameless voices). The Voices op
takes 14–15 ms after the build (stats and the profile view), against
0.68 s before at 14 days and 0.81 s with the old code at 90 days. The
first call after start still takes 2.5 s: it reads every meeting's
`looks.jsonl` for the profiles' tiles (2.4 s for 411 profile meetings),
cached after; before, that came on top of the session reads (3.2–3.8 s).

## Voice learning rules and automatic relearn (2026-11-12)

**Known.** A tile stays lit a moment after its person stops, so tile
learning took in the next speaker even where the transcript names them;
specks of crosstalk (one- and two-fingerprint samples) added up to a
"second voice" on the profile card; relearning a meeting kept whatever
it had taught a profile before, even when it no longer does. Explore →
Voices had a "Learn from saved meetings" button for after such fixes.

**Agreed with the owner, built** (research-log.md, "voice learning rules
2"):

1. No tile learning while the transcript line names someone else (a name
   the owner gave, or the window's in "Person N (Name)"): at save, in
   `LearnSaved` and in the samples view's rebuild (`SavedLearning`).
2. A sample counts towards a meeting's voices only with at least
   `MinLearn` (10) fingerprints.
3. Learning a whole meeting replaces its automatic contributions
   (`voices.Store.LearnMeeting`): a profile it taught from a tile, not
   confirmed, not named or moved there by the owner, and no longer
   teaches, loses that meeting. Named, confirmed and moved stay.
4. `voices.LearnRules` (now 2) is stored in the voices file (`rules`;
   absent: 1). When the recorder starts, after its models load, a store
   under older rules has every saved meeting learnt again in the
   background (`learn_saved`'s loop), meeting by meeting, waiting while a
   meeting records or saves, on a low-priority thread, with Status.Busy
   "Updating voice profiles for new rules (n of N)". When done it stores
   the version and a summary (`relearned`: when, meetings, profiles whose
   learnt voice changed, profiles added); a run cut short starts again at
   the next start. Only when the recorder's own voices are of record (with
   Tomoe's copied over them it would run at every start) and voice
   profiles are on; `[meeting] voice_profile_relearn = false` turns it off.
   Before it starts (and before `learn_saved`, or the first save of a
   voices file converted on load) the voices folder is copied to
   `<data>/voices.bak-<time>`, unless the last copy already matches
   (`[recorder.meeting] voice_profile_backups = false`: no copy;
   docs/data-safety.md, item 6). A voices file of a newer `version` is
   neither read nor written: meetings record, nobody is learnt or named
   by voice, and a health line says why.
5. The button is gone (the app's `LearnSaved` binding with it;
   `learn_saved` stays in the protocol for tools). For 3 days after a
   relearn Explore → Voices shows "Voice profiles updated for new rules
   on <date>: N changed, M new." (`VoicesView.relearned`).

## Not them and Junk on a sample; split a speaker by voice (2026-11-12)

**Known.** "Not them" on a sample took it out of the profile for good
but asked nobody who it was; a sample of noise or crosstalk could only
be taken out the same way. A meeting row's "Someone else…" (move the
meeting to another profile) duplicated "Not them" followed by naming
under "Who's this?". The owner's review of the rules-2 relearn found
that a tile's voice replaced a meeting the owner had named, that a
meeting's Confirmed mark was lost when the meeting was learnt again, and
that samples taken out came back when a dropped meeting was learnt
again. And one owner-named label can cover two people (the diarizer
merged them), which only a split of its lines can fix.

**Agreed with the owner, built:**

1. **Two actions on a sample.** "Not them" (`exclude_sample`): the
   sample leaves the profile as before and is kept as a returned sample
   in the voices file (`returned`: session, span, the profile it came
   from, the transcript's label over it, its fingerprints). "Who's
   this?" shows each pending one, whatever its age, with its clip, the
   meeting and a Transcript link at the clip. Naming it
   (`name_returned_sample`) adds its fingerprints to a profile of exactly
   that name (case and spacing aside, not the one it came from) whose
   voice it is at least `LearnFloor` like, else to a new profile with the
   name; they are kept on that profile's meeting (`Heard.Assigned`) as
   the owner's naming. "Can't tell" (`skip_returned_sample`) stops
   asking; the sample stays out of the profile it came from. "Junk"
   (`junk_sample`): the span is kept per meeting (`junk`), and no profile
   learns from it again, in every learning path (save, `LearnSaved`, the
   relearn). "Put back" (`include_sample`) undoes either; it is refused
   for a sample already named as someone else. Junk is taken out of the
   profile it was found in at once; another profile that learnt
   fingerprints in the same span loses them the next time the meeting is
   learnt. Labels in the meeting are unchanged, so Musubi's copy needs
   nothing.
2. **Migration.** Every sample taken out before (each
   `Heard.ExcludedSpans` entry) becomes a pending returned sample when
   the voices file is read; the recorder fills in its label and
   fingerprints from the saved meeting once, after the review index is
   built. On a copy of the recorder's voices file: 7 samples in 3
   meetings.
3. **Meeting rows** keep "Right, it's them" and "Not them"; "Someone
   else…" and its form left the app (on taken-out meetings too);
   `move_voice_meeting` stays in the protocol.
4. **Tooltips** on every voice button say what happens and what is kept.
5. **Learning rules 3** (`voices.LearnRules`, so stores under older rules
   are learnt again once): a tile's voice never replaces a meeting the
   owner named or moved to the profile; a meeting's Confirmed mark, its
   samples named as the profile and junk are kept when it is learnt
   again; samples taken out stay out even after the meeting's
   contribution was dropped (learning reads the returned samples, not
   only the meeting's spans). Dropping meetings the saved recording no
   longer gives a profile stays as it is.
6. **Split a speaker by voice** (`split_label_by_voice`; meeting page →
   Speakers → "split by voice…", two or more profiles, a preview, then
   "Split"). Each final line of the label in the saved session gets the
   fingerprints most of whose speech falls in it
   (`diarize.LineVoices`); the line goes to the candidate whose voice
   they are likest, when it beats the next by `Margin` (0.1); lines
   without fingerprints or too close to call keep the label. A
   candidate's voice is what its other meetings taught (its whole learnt
   voice when those hold too little), so a profile taught by the very
   label being split doesn't vote for itself. Owner-named lines are
   split too. Each moved line's start is logged in `relabels.jsonl` under
   a batch; `undo_relabel` puts back exactly those lines ("Undo" in the
   app). The profiles then learn the meeting again. Who said what
   changes, so the app imports the meeting again as any changed meeting
   (re-extraction); the confirm text says so.

**Dry run (2026-11-12),** on copies of one session and of the voices
file (the originals only read): a 163-line label the owner had named,
which the transcript shows covers two people, split between that
person's profile and a profile learnt from the second person's tile in
the same meeting: 66 lines to the first, 35 to the second (all between
34 and 54 minutes, where the second person asks and answers questions),
62 left as they were (59 with no fingerprints: short interjections such
as "Exactly.", 3 within the margin). Lines with 3 or more
fingerprints scored about 0.8-0.96 against the profile they went to and
0.3-0.5 against the other; some one- and two-fingerprint lines were
decided on lower scores (0.40-0.67 against 0.09-0.30). In 0.1 s. Undo restored the file line for line (0 lines differing).

## Keeping the recorder running (2026-11-12)

A recorder that isn't running loses meetings silently; an install left it
stopped twice on 2026-11-12. Three guards:

- launchd restarts it whenever it exits (`KeepAlive` true), not only after
  a crash; only `launchctl bootout` stops it.
- `scripts/install-recorder.sh` refuses while a meeting is being recorded
  (it asks `musubi-recorder status`; `MUSUBI_RECORDER_FORCE_INSTALL=1`
  installs anyway), waits for the old recorder to finish saving and leave,
  retries loading the new one, and exits non-zero unless it is running at
  the end.
- While Musubi is open (`[recorder] watchdog`, default true) it checks the
  recorder every 10 s; after 30 s down it loads or starts the LaunchAgent
  (at most every 2 minutes), shows a red "Recorder off" button by the tabs
  and the reason on Status, and posts one macOS notification per outage
  after 90 s.
