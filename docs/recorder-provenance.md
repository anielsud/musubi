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

The config package has no `Save`: while Tomoe is the recorder of record,
nothing in the recorder may write Tomoe's config file.

## Not brought over

Hotkey, dictation (`live/dictation.go`, `Coordinator.Activity`), clipboard
and auto-paste, Tomoe's window and tray,
multilingual mode (language detection, Bengali, the multi-engine switcher),
hotwords, GPU libraries, Linux meeting detection.
