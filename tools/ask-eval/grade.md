# Grading Ask answers

The question set and answers are private (`testdata/private/ask-eval/`).
A grader (a person, or a model reading the files locally) scores each
answer against the question's reference, blind to which system wrote it:
answers to one question are shown as A and B in a random order.

Per answer:

- **must**: how many of the question's `must` facts the answer states
  (paraphrase counts; a wrong figure doesn't).
- **wrong**: statements that contradict the reference, including a
  `must_not` (an outdated position given as current, a confusion with a
  similar subject). Each one counts.
- **invented**: facts in neither the reference nor plausibly the record.
- **current**: 1 if it gives the latest position (and, where things
  changed, says what changed), 0 if it leads with or rests on an older one.
- **abstain** (questions of type `absent`): 1 if it says plainly that the
  record doesn't answer, without inventing an answer.
- **useful**: 0–3, how well it serves a busy executive asking this: 3 =
  leads with the answer, specific, dated, brief; 0 = misses the point.

Then, per question, which answer is better (A, B or tie) and why in one
line.

Summaries: must coverage (sum of must / sum of must possible), wrong and
invented per answer, current rate, abstain rate, mean useful, and wins.
