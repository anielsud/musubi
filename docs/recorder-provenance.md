# Recorder provenance

`internal/recorder/` brings Tomoe's recording engine into Musubi. Tomoe is
GPLv3, a fork of an upstream project. The owner wrote most of it and
relicenses their own lines into Musubi (owner's decision, 2026-11-10).
Lines committed by Tomoe's upstream author are not copied: each is
rewritten as new code with the same behaviour, the same exported API and,
for numerical code, the same arithmetic, so replays of the same audio
match Tomoe.

Source: Tomoe commit `8d0e659` (`feature/mic-voices`, the installed build
on 2026-11-10). Authorship was read with `git blame` at that commit.

## Lifted unchanged (owner-authored only)

Files with no upstream-authored lines, copied with import paths changed:

- `audiosources`, `axtree`, `diarize`, `eval`, `livefeed`, `ocr`, `onnxrt`,
  `teamsvideo`, `toolpath`, `voices`: whole packages. `voices` has since
  changed (below, "Changed since").
- `cmd/musubi-recorder`: `replay.go`, `textdiff.go`, `pipeline.go` (edited
  only to drop the GPU, hotwords and multilingual settings) and
  `fingerprints.go` (`loadFingerprints`, from Tomoe's `cmd/tomoe/tune.go`).
  `main.go` is new.
- `audio`, `live`, `meeting`, `models`, `session`, `transcribe`,
  `videohint`: the files with no upstream lines; the rest were rewritten
  (below).
- `guestaudio`: `bridge.h`, `bridge_darwin.m`, `callback_darwin.go`,
  `tap_darwin.go`. `meetingaudio`: `meetingaudio_darwin.go` and its test.
- `recording` (the live recorder, from Tomoe's `internal/backend`, with
  `*App` now `*Recorder` and the Wails events, window and settings parts
  removed): `livefeed.go` and its test, `meetingdiarize.go`,
  `ownervoice.go`, `titles.go` (without `showCalendarTitle`) and its
  `defaultTitle` test, `videohint.go` (the recording side only: the
  watcher, `followMeetingAudio`, `watchMic`), `permissions_darwin.go`
  (the status and prompt functions). From `app.go`, the owner-authored
  `setupEngines`, `engineBundle` and `close`, `meetingEmbedder`,
  `tuningFromConfig`, `startDetector`, `leaseEnginesLocked` and
  `releaseLeaseLocked`, now in `recorder.go`, and the owner lines of
  `StartSession`, `StopSession` and `persistSession` in `session.go`.
- `Makefile` `recorder-app`: the library-bundling loop from Tomoe's
  `install-gui-mac`.
- `recording`, for the Musubi app's control socket (step 5,
  [recorder-screens.md](recorder-screens.md)), from Tomoe's
  `internal/backend`. Tomoe's window called `a.fixSignals()` (an
  upstream line) at the top of each method; those calls are not carried
  over: `Handle` repairs the signal handlers once per request instead.
  - `voices.go`, from `voices.go` (all owner lines): the Voices view,
    profile edits, `NotThem`, `SkipVoiceName`, `SkipVoiceDoubt`,
    `MoveVoiceMeeting`, `DiscardedVoiceClip`,
    `LearnVoicesFromSavedSessions` and the tile and fingerprint caches.
    Changed: the view types are the control protocol's (aliases),
    sessions are loaded by exact ID and edits refuse one still being
    saved, `cutVoiceClip` makes AAC in MP4 instead of WAV, and
    `PlayVoiceClip` and `GetLookTile` gave way to control ops.
  - `speakers.go`: `RenameSpeaker` and `renameLines`, from
    `videohint.go` (owner lines; the upstream `a.fixSignals()` is not
    carried over), sending relabelled lines to the live feed instead of
    window events.
  - `looks.go`: `toLookView`, `GetVideoHintLooks` (now `Looks`) and
    `GetLookThumb`, from `videohint.go` (owner lines, as above);
    `videohint.go` gains `onLook`'s and `startHintWatcher`'s timeline
    parts from the same file.
  - `tools.go`, `tools_darwin.go`, from `tools.go` and `tools_darwin.go`
    (all owner lines): the ffmpeg, diarization worker, model and
    permission statuses, `FixTool` and `runStreaming`. Dropped: GPU,
    Bengali, Linux tools and Tomoe's model download (models were read
    from Tomoe's folder only, so their fix was a command; the recorder's
    own download, below, is new code); progress and outcome go to
    `Status.Busy` and the health lines instead of window events.
    `permissions_darwin.go` gains the permission table,
    `openPrivacySettings` and the accessibility prompt from Tomoe's
    `permissions_darwin.go` (all owner lines).
- `control/server.go`: the private listen and stale-socket removal are
  the live feed's (`livefeed`, owner), adapted.

## Changed since

Behaviour that now differs from Tomoe at `8d0e659`, all in owner-authored
code.

- **Voice profiles learn by voice, not by name** (2026-11-12, owner's
  decision). `voices.Store.Learn` used to put a named voice into the first
  profile with that name, alias or `same` name, and drop it silently when
  the profile had 30 or more fingerprints and the voice was less than
  `LearnFloor` (0.5) like it. Now the voice joins the likest of all the
  profiles with the name that it is at least `LearnFloor` like, at any
  profile size (`LearnFloorAfter` is no longer used: a young profile is
  held to the same bar, as a voice that doesn't fit is now kept in its own
  profile rather than lost, and mixing it in would spoil a profile that
  is just starting). A voice like none of them starts a new profile with
  the same name; profiles are never merged automatically. A profile with
  nothing learnt takes a voice none of the others fit, and a meeting the
  user took out of a profile with the name still isn't learnt (no new
  profile is made for it). A name given twice in one meeting keeps the
  last voice (the user's naming, after the tiles). `Knows` is true when
  any profile in use has the name; forgetting stays by name. `Move`
  targets a profile by ID, or a new profile with a name, instead of the
  first profile with a typed name. `Merge` no longer adds the kept
  profile's own name as an alias, and carries the merged profile's
  "different people" marks over. New: `SameNamed` (pairs of profiles
  sharing a name the user hasn't kept apart), shown in the Voices view as
  "Two profiles named X?". Existing profiles keep their IDs, aliases and
  merges; there is no migration.

## Rewritten

Files that held upstream-authored lines. The owner's lines in them are kept;
the upstream ones are new code.

| Package | Files |
|---|---|
| sigfix | fix.go |
| speaker | cluster.go, embedder.go, math.go and their tests |
| audio | capture.go, capture_malgo.go, dsp.go, monitor.go, stream.go, ticker.go and tests |
| meeting | detect.go and test; new: platform_type.go (the Platform type), detect_other.go (non-macOS stub) |
| videohint | ring.go and tests |
| config | config.go and test |
| models | manager.go, urls.go and test |
| transcribe | convert.go, engine.go, engine_set.go, factory.go, parakeet.go, streaming.go |
| session | audio.go, diarize_runner.go, export.go, reidentify.go, session.go, store.go and tests |
| live | coordinator.go, pipeline.go and tests |
| guestaudio | capturer_darwin.go; new: doc.go |
| meetingaudio | meetingaudio.go; new: meetingaudio_other.go (non-macOS stub); Tomoe's Linux files are not brought over |
| recording | session.go (`StartSession`, `StopSession`, the save worker, `persistSession`), recorder.go (`buildEngines`, the `Recorder` type, shutdown), events.go (`emitSessionSegments`, `applySegment`), detect.go (from Tomoe's `handleDetectEvent`) |
| cmd/musubi-recorder | diarize.go (from Tomoe's `cmd/tomoe/diarize.go`) |

New for the control socket, with no Tomoe lines: `control/server.go`
(apart from the part above) and `control/client.go`, `recording/control.go`
(the request handler, status, start and stop, sources, set_source,
reload_config, session and delete), `recording/sourcecheck.go`,
`recording/diag.go`, `recording/tools_other.go`, `meetingaudio`
`switch_darwin.go` and `switch_other.go`, `audio/signal.go`,
`config/musubi.go`, the live feed's status, audio, diag and look
messages, and `live.Coordinator.MonitorQuietFor`. Where Tomoe had
upstream-authored code for the same job it was not copied:
`listSources` (Tomoe's `ListAudioSources`, whose owner-written entries
it follows, and `ListAudioDevices`), `sessionForControl` (`LoadSession`)
and `deleteSession` (`DeleteSession`) are new code with the same
behaviour. `diag.go` writes in Go what Tomoe's diagnostics pane
(`frontend/src/components/DiagnosticsPane.tsx`, all owner lines) showed
for each line.

New for the Models screen (2026-11-12), with no Tomoe lines:
`models/catalog.go` (the catalog, `Installed`, `DownloadModel` and its
staged archive unpacking), `recording/models.go` (the `models` and
`download_model` ops and the Tools entries' Download fix), the fallback
folders and injectable `Fetcher` in `models/manager.go` (`dirFor`,
`WithFallback`, `NewManagerIn`, `own`) and `config.OwnModelDir` /
`ModelDirs`, and their tests. Tomoe's own model download (its `tools.go`
and `cmd/tomoe model download`) was not copied; the existing rewritten
download functions in `models` are reused.

New, with no Tomoe source: `recording/voicecopy.go`,
`recording/permissions_other.go`, the `recording` tests other than the
two lifted ones, `cmd/musubi-recorder` `run.go`, `datadir.go`,
`shadow.go` and their tests, `packaging/macos/recorder/`,
`scripts/install-recorder.sh`.

The config package has no `Save`: while Tomoe is the recorder of record,
nothing in the recorder may write Tomoe's config file. `SetDataDir` moves
everything the recorder writes out of Tomoe's data folder; `run` and
`diarize` refuse Tomoe's folder.

## Not brought over

Hotkey, dictation (`live/dictation.go`, `Coordinator.Activity`), clipboard
and auto-paste, Tomoe's window and tray (the Musubi app shows the tools
and voices pages' data through the control socket), its settings page,
session list/export/re-transcribe, title editing, notifications,
multilingual mode (language detection, Bengali, the multi-engine switcher),
hotwords, GPU libraries, Linux meeting detection.

## App screens (frontend)

The recorder screens in the Musubi app (`frontend/src/components/voices.tsx`,
`recorder.tsx`, `settings/RecordingTab.tsx`) port Tomoe's `VoicesPanel`,
`DiagnosticsPane`, `HintTimeline` and `ToolsPanel`, owner-authored
throughout at `8d0e659`, restyled for Musubi. From `SettingsPanel` only the
owner-authored rows' labels and hints were taken (into
`internal/gui/recorder_settings.go`); its upstream-authored lines (the
panel's shell and loading) were not used.
