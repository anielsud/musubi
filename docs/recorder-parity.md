# Recorder parity with Tomoe

The recorder replaces Tomoe only once it gives the same results. Each check
replays a saved Tomoe session's audio through Tomoe and through the
recorder with the same config, and compares the outputs.

Baseline: Tomoe `8d0e659` (the installed build), built from an export of
that commit. Recorder: branch `recorder-offline`. Command for both:
`session replay <id> --only-current`, run in parallel at background
priority (`taskpolicy -b`) while nothing was recording.

## 2026-11-10: first replay

One 9-minute meeting from 2026-11-10, two sources (mic and system audio),
75 lines, mic voices on, single pass, Cohere Transcribe.

| | Tomoe | Recorder |
|---|---|---|
| Transcript (`current.txt`) | 75 lines | byte-identical |
| Lines with times, speakers, timings (`current.json`) | | byte-identical |
| Mic-voice fingerprints | 1016 | 1016 |
| Speaker-change signals | 24 | 24 |
| Turn decisions (joined, length cap, pause, ...) | | same counts |
| Decode time | 441 s (80% of audio length) | 440 s (80%) |

The rewritten upstream code (audio, transcription, sessions, live pipeline,
speaker tracking) gives the same output as Tomoe's on this meeting. One
meeting covers one config; two-pass live text, single-source meetings and
room audio are not yet compared.

## 2026-11-10: three more meetings

Six replays in parallel (three meetings, each through both programs).
Two-pass was turned on through a scratch copy of the config
(`XDG_CONFIG_HOME`); Tomoe's own config was only read.

| Meeting | Lines | `current.txt` | `current.json` | Notes |
|---|---|---|---|---|
| In person, 75 min, mic only (every line from the mic) | 240 | byte-identical | byte-identical | no call signals; 154 voice changes in both |
| Zoom, 34 min | 257 | byte-identical | byte-identical | 213 speaker-change signals in both |
| Teams, 7 min, two-pass | 56 | byte-identical | byte-identical | 61 first-pass drafts (`current.drafts.json`), byte-identical |

Turn decisions matched count for count in every meeting. Decode times were
within 1% of each other (the six runs shared the machine, so absolute
times are slower than live).
