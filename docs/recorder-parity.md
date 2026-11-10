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
