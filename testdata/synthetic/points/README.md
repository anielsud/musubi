# Hard meetings for extraction screening

Invented meetings (no real people, companies or data) that reproduce the
failure modes seen on real transcripts. Each file holds the transcript,
the known facts given to the extractor, the key points a good extraction
covers, and traps: regular expressions that match known kinds of wrong
point.

The traps cover:
- inverted negations ("we don't feel like ... is giving away the farm");
- a lawyer's hedged "I'd say no ... but yes, that permits it";
- an unrelated deal mentioned next to a signing remark;
- a client's demand vs current practice vs what was agreed;
- garbled numbers;
- acronyms that tempt expansion;
- retained vs eliminated staff;
- personal asides that should be kept.

`noise.py` makes the clean script look like speech recognition output
(fragments carried across lines and speakers, false starts, fillers,
chatter). It is seeded, so `python3 noise.py <in> <out>` is reproducible.
Score on the noisy versions; the clean ones are too easy.

Safe to send to hosted model endpoints: nothing here is real.
