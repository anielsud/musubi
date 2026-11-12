# Recorder screens in Musubi

Status: agreed 2026-11-11, being built.

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
   live in Settings.
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
   meeting; "Same person?" suggestions. Open reviews are counted on the
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
calendar).

### Settings

The recorder's settings move into Musubi's one config file under
`[recorder]`, edited from Settings like the rest. A key set there wins;
anything not set still comes from Tomoe's config until Tomoe is retired.

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
fix_tool, reload_config, and (2026-11-12, for Settings → Models)
models and download_model. Types and a client are in
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
  speaker. Statement counts are checked after, as for any re-run.
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
