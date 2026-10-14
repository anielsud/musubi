# Evaluating insight extraction

How we measure extraction quality and cost, and find the cheapest model
and prompt that come close to a ceiling. Results are in
`docs/research-log.md` (2026-10-12 onwards). The tools are in
`tools/insights-eval/`. Data (transcripts, references, runs) stays in
`testdata/private/insights-eval/` and is never committed.

## 1. Reference ("gold")

- **The corpus is hand-picked:** four consecutive weekly leadership
  meetings, about 1,900 turns. It grows only deliberately.
- **Speakers.**
  - Teams labels are devices, not people, so a reference speaker is
    worked out for every turn.
  - One Opus sub-agent per meeting does this from the transcript text,
    with directory titles as context.
  - It agrees with 242 of 244 turns labelled by hand beforehand.
  - Since 2026-10-12 attribution is Tomoe's job, so extraction is
    always fed these reference speakers, which keeps attribution out of
    idea scores. Low-confidence speakers are passed as unknown, with no
    device label shown.
- **Items.**
  - The same sub-agents extract every item exhaustively: 670 across the
    four meetings, in 12 kinds (decision, commitment, fact, metric,
    position, disagreement, framing, rule, outside_fact, question,
    sensitivity_cue, personal).
  - Each item carries its lines, verbatim quotes, who, thread, use-when
    cues and sensitivity.
  - Quotes were checked word for word; samples were checked against a
    reading of the meetings.
- The reference is good but not perfect: graders occasionally overrule it
  from the transcript.

## 2. What is scored

- **Precision**, for every extracted item:
  - **correct**;
  - **wrong**: misstated, a suggestion turned into a decision, two
    things merged;
  - **invented**: says something not in the transcript, including added
    surnames and titles;
  - **misattributed**: names the wrong person;
  - **trivial**.

  The headline is **wrong + invented**: misattribution is less harmful,
  and trivial items are noise rather than poison.
- **Recall**, for every reference item: found, partly found or missed.
- **Cost:** tokens and dollars per meeting, from the APIs' usage figures
  at list price. Local runs record wall time and tokens per second.

## 3. Graders

- **Opus judge** (a sub-agent per meeting, 110–200k tokens of Claude
  usage each): the most trusted. Used for audits only.
- **Cheap grader** (`grade.py`):
  1. Pair each extracted item with reference items within ±2 cited lines
     (±6 if none).
  2. Ask in batches whether it is the same, partly the same, or none.
  3. Check everything not "same" against its cited lines ±6, with the
     reference speakers shown.
  4. Check in code who said it.
  5. A reverse pass asks whether each reference item is covered by its
     nearby items, which gives recall.
- **Calibration** on eight Opus-graded meeting-runs:

  | Grader | Item agreement with Opus | Wrong + invented vs Opus | Cost per ELT meeting |
  |---|---|---|---|
  | Gemini 3.8 Flash (Vertex) | ~88% | 1.5–3 points stricter | ~$0.19 |
  | GLM-5.3-Flash (Baseten) | ~88–90%; recall closer to Opus | ~2 points stricter | ~$0.03 |

  Both under-count misattribution: they tend to call it "wrong".
- **Self-grading is lenient.** GLM-5.3-Flash passed 16 real errors in its
  own output that Gemini caught. Rule: a model never grades its own
  output. GLM-5.3-Flash grades other models; Gemini grades GLM-5.3-Flash.
- Compare runs only with the same grader. Graders differ by a point or
  two between themselves, and single-meeting differences of 5–7 items are
  noise.

## 4. Procedure for a new model or prompt

1. Screen on one or two meetings (Sep 23, the shortest, plus Oct 7, the
   messiest) with the cheap grader.
2. Confirm on all four meetings when it is close to the best run.
3. Audit one meeting with Opus before treating a result as settled.
4. Write the result into `docs/research-log.md`: tokens, cost, the
   grader used, and what changed.

Rules of the harness:
- Only our prompts reach the model, as raw chat completions with a JSON
  schema. No agent system prompts.
- One retry on malformed JSON, the same for every model; no per-model
  debugging.
- Spend caps per provider. Paid calls are cached by request, so a re-run
  never pays twice.

## 5. Current best (2026-10-12)

GLM-5.3-Flash with the lean exhaustive v2 prompt (`extract_lean.py`,
`VARIANT=lean_ex_v2`):
- recall: 86% found / 10% partly / 4% missed (Gemini grader, 4 meetings);
- wrong + invented: about 5% (Opus audit of Sep 30, the most
  number-heavy meeting);
- cost: about 2.5 cents per ELT-length meeting, under a cent for an
  average one.

Remaining errors are mostly numeric (garbled figures read literally,
totals and cohorts conflated) and proposals stated as decisions.
