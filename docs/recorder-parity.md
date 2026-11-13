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

## Live shadow

`musubi-recorder run` records the same meetings as Tomoe, live, into its
own folder (`~/.local/share/musubi/recorder`, or `--data`): sessions, a
copy of Tomoe's voices taken at the start of each recording, the live
feed's socket and the log. It reads Tomoe's config and models and never
writes Tomoe's folder; it refuses to start with `--data` pointing there.
(Since 2026-11-12 it can also stand alone, reading nothing of Tomoe's:
`[recorder] tomoe = "off"`, recorder-screens.md, "Standalone".)

Install (after `make dev-cert-mac` in Tomoe, once, for the signing
identity), not while a meeting is being recorded:

    make recorder-app
    scripts/install-recorder.sh

The LaunchAgent `com.anielsud.musubi.recorder` starts it at login and
after a crash; `launchctl bootout gui/$(id -u)/com.anielsud.musubi.recorder`
stops it, saving a recording in progress. Log:
`~/.local/share/musubi/recorder/logs/recorder.log`.

Compare with Tomoe (read-only on both folders; print to the terminal,
don't commit the output):

    musubi-recorder shadow compare --since 2026-11-12

Per meeting: start and stop difference, durations, line counts, word
error rate of the recorder's text against Tomoe's, the share of aligned
words with the same speaker, and recordings only one side has.

## 2026-11-12: first live shadow meeting

A 19-minute Teams call (two speakers), recorded live by both, build
3f1f677 beside Tomoe 8d0e659. `shadow compare --since 2026-11-12`:

| | |
|---|---|
| start / stop | recorder +0.6 s / +0.2 s |
| duration | 1134 s / 1135 s (recorder / Tomoe) |
| lines | 164 / 162 |
| WER against Tomoe | 3.6% |
| same speaker | 100.0% of 2862 aligned words |

Offline replays are byte-identical because both read the same file; live,
each captures its own audio, starting 0.6 s apart, so chunk boundaries and
some words differ. Speakers agreed everywhere. The recorder detected the
call on its own and logged a 20 s mic silence (the owner muted). Load
during the call: about 30% of one core, 2.5 GB resident.
