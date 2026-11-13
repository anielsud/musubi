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
  `teamsvideo`, `toolpath`, `voices`: whole packages.
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
and auto-paste, Tomoe's window and tray, its settings, tools and voices
pages, session list/export/delete/re-transcribe, notifications,
multilingual mode (language detection, Bengali, the multi-engine switcher),
hotwords, GPU libraries, Linux meeting detection.
