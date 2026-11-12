# Recorder screens in Musubi

Status: design, for approval. Nothing here is built.

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

1. **Recording status** (Status tab): recording or idle, which meeting and
   for how long, mic silent, and a health line (permissions, models, last
   crash). A "Stop recording" button for a meeting detected by mistake.
   Detection stays automatic; no start button.
2. **Speakers on the meeting page** (Meetings): the meeting's review queue,
   same choices as Tomoe's Voices panel, each with its voice clip and
   video tile; and rename a speaker from the transcript.
3. **Voices on the person page** (People): the person's voice profile with
   its meetings; rename, confirm, start over, forget, move or discard a
   meeting; "Same person?" suggestions. A count of open reviews on the
   People tab leads to them.
4. **Live**: Musubi follows the recorder's socket; rename a speaker during
   the meeting.
5. **Delete a meeting**: removes the recording and its knowledge in Musubi
   (statements, links). Asks first.

### What stays behind

Hotkey, dictation and other languages (dropped in step 2, and with them
retranscribing in another language), manual start, audio source picker (read from config), the hint timeline and
diagnostics panes (debug tools), model download screen (folded into the
health line), export (Musubi's own views and MCP replace it; can be added
later), title editing (titles come from the calendar).

### Settings

The recorder's settings move into Musubi's one config file under
`[recorder]`, edited from Settings like the rest. Until the switch the
recorder keeps reading Tomoe's config.

### Shadow first, then the switch

Built against the recorder's own sessions; Musubi gets
`recorder_sessions` and `[live] recorder_socket` keys, and a Settings
choice of which recorder is "of record" (Tomoe until step 6). While Tomoe
is of record the recorder still copies Tomoe's voices at each recording,
which overwrites voice edits made in Musubi: fine for trying the screens,
and the reason edits only become real at the switch, when the copy stops.

### Order of work

1. **Recorder control socket** and the meeting glue (rename speaker, not
   them, skip, doubts, voice edits, move or discard a meeting, stop,
   delete, status). Tomoe's backend functions are lifted where the owner
   wrote them and rewritten where upstream did, per the provenance rules.
   Tested against copies of real sessions.
2. **Paths, Status and Live**: the config keys, recording status, Live on
   the recorder's socket with rename.
3. **Meeting page speakers.**
4. **Voices in People.**
5. **Delete.**

One PR each, stacked on #25.

## Trade-offs and open questions

- **Re-extraction cost.** A renamed speaker re-extracts that meeting
  (one meeting's hosted calls). Alternative: a relabel-only path that
  updates speaker names on existing statements without re-extracting:
  cheaper, but statements extracted under the wrong speaker keep wording
  that may assume them. Proposal: re-extract (correctness first), check
  statement counts after, as for any re-run.
- **Control socket vs Musubi writing files.** The socket adds a small
  protocol to maintain; writing files directly would race the recorder
  during a meeting and could lose a voice edit or a recording. Socket
  proposed.
- **Edits unavailable when the recorder is down.** Acceptable: it is
  started by launchd and restarts after a crash.
- **Delete reaches into Musubi's knowledge.** Unknown: whether a deleted
  meeting's statements should vanish from threads or stay marked as from a
  removed source. Proposed: remove them, like `forget` does for other
  sources; to confirm before step 5.
