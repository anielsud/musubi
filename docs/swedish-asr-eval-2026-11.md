# Swedish and mixed Swedish/English transcription: model comparison

2026-11-14. Question: which transcription model handles meetings held in
Swedish, and meetings that switch between Swedish and English mid-
conversation? The recorder treats every meeting as English
(`meetingLanguages`, `internal/recorder/recording/recorder.go`), and
`auto` picks Cohere Transcribe, which has no Swedish.

## Setup

- Two recorded meetings (not in git): **A**, 24 min, almost all Swedish;
  **B**, 28 min, mostly English with stretches of Swedish.
- `musubi-recorder session replay <id> --only-current --single-pass
  --threads 8`, with `--asr-kind whisper --asr-lang auto|sv` for the Whisper
  models (`--asr-lang` added on this branch; `auto` leaves sherpa-onnx's
  Whisper language empty, so it is detected per utterance).
- Apple Silicon, 11 cores, CPU only, int8 models:
  - Parakeet TDT 0.6B v3 (sherpa-onnx release)
  - Whisper large-v3-turbo (`sherpa-onnx-whisper-turbo`, sherpa-onnx release)
  - KB-Whisper large (KBLab, Apache-2.0; third-party sherpa-onnx export
    `frictional123/sherpa-onnx-kb-whisper-int8`)
- Cohere Transcribe could not be run: the local copy is incomplete (no
  `tokens.txt`, encoder data 644 kB), so recordings have been falling back to
  Parakeet v3 (`Status.ASRModelFor`). It has no Swedish in any case.
- No reference transcripts: the numbers below are decode time and a
  stop-word count per transcript; quality was judged by reading the models'
  text side by side, 30-second windows, over the first minutes of each
  meeting.

## Numbers

Decode time as a share of the audio's length (8 threads):

| Model | A (sv) | B (en+sv) |
|---|---|---|
| Parakeet v3 | 3.4% | 4.6% |
| Whisper turbo, auto | 28.3% | 30.7% |
| KB-Whisper large, auto | 207.8% | 199.2% |
| KB-Whisper large, sv | 164.2% | 197.6% |

Share of words that are common Swedish / English function words (B is
mostly English, so a model that keeps the spoken language should stay low on
Swedish there):

| Model | A sv% | A en% | B sv% | B en% |
|---|---|---|---|---|
| Parakeet v3 | 39.9 | 1.6 | 3.6 | 26.6 |
| Whisper turbo, auto | 40.3 | 1.4 | 3.8 | 26.8 |
| KB-Whisper large (auto and sv) | 40.4 | 1.3 | 28.7 | 8.1 |

## Findings

1. **KB-Whisper translates English into Swedish.** On B it writes most
   English stretches as Swedish sentences (Swedish function words 3.6% →
   28.7%). Its `auto` and `sv` output are byte-identical on both meetings:
   the export does not detect language. It also decodes at 1.6-2x real
   time. Not usable for mixed meetings or live.
2. **Whisper turbo is the most accurate on Swedish of the usable models.**
   Product and technical terms, compound words and inflections are
   noticeably better than Parakeet's, which misspells most English terms
   inside Swedish speech. On English (B) it is on par with Parakeet, slightly
   better on a few terms.
3. **Whisper turbo invents text in other languages on short or unclear
   utterances.** On A, several short clips (greetings, fillers, crosstalk)
   came out as Icelandic, Norwegian, Vietnamese or Japanese. 18 language
   switches between consecutive lines against 8 for Parakeet. Free
   detection chooses among ~100 languages.
4. **Speed:** turbo at ~30% of real time is fine for single-pass recording
   on this machine but 8x Parakeet's CPU.

## Language guard (built, 2026-11-14)

`transcription.model = "whisper-turbo"` with `whisper_languages = ["sv",
"en"]` (the default; `[]` turns the guard off). An utterance detected as
anything else is decoded again with the language forced: Swedish for
Norwegian, Danish, Icelandic or Faroese, else the last allowed language
detected, else the first in the list. The Go binding (v1.13.8) can't change
a recognizer's language, so each forced language is a second recognizer,
loaded the first time it is needed (~1 GB for turbo).

Replayed through the app's own engine path (`NewEngineSetFromConfig`), 8
threads. Lines in a non-Latin script or Vietnamese, and lines with two or
more Norwegian/Icelandic words or æ/ø/ð/þ:

| A (sv) | non-Latin | Norwegian/Icelandic | decode time |
|---|---|---|---|
| Parakeet v3 | 0 | 0 | 3.4% |
| Whisper turbo, unguarded | 2 | 8 | 28.3% |
| guard, redo in last language | 0 | 1 | 29.1% |
| guard, Nordic → Swedish | 0 | 0 | 29.0% |

- On A, 13 of 179 lines differ from unguarded turbo. The first version redid
  Swedish-heard-as-Norwegian in the last language detected (English then)
  and made up English sentences; mapping the Nordic languages to Swedish
  turned those into the Swedish that was said.
- On B (mostly English) nothing was redone: the output is identical to
  unguarded turbo. Decode time 35.0% (30.7% unguarded; noise, as no second
  recognizer was loaded).
- Left: Whisper's usual errors on fillers and crosstalk within the allowed
  languages (a made-up short English or Swedish phrase), which the guard
  can't see.
