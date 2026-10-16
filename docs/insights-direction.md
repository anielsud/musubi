# Insights: what to learn, and how to surface it

Direction agreed 2026-10-12, after reading four consecutive weekly
leadership meetings in full. The real examples are in
`testdata/private/elt-insights-analysis.md`. Examples here are invented.

Facts and statements are not enough. What people and agents want at a
given moment is:
- the state of things over time;
- what's disputed;
- what the organisation has learned;
- who owes what;
- how ideas are landing.

They want it at the moment it matters, not when they search.

## 0. Attribution belongs upstream (decided 2026-10-12)

Who said what is the source's job: Tomoe's speaker identification, or the
transcript provider's labels. Musubi takes speakers as given and never
infers them. When the source marks a speaker unknown or uncertain, the
item's "who" stays empty. Musubi may flag contradicting evidence back to
Tomoe (for example a line addressed by name to someone other than the
labelled speaker), but it does not rewrite attribution.

For experiments on Teams transcripts, whose labels are poor, extraction
is fed reference speakers. That keeps attribution out of the idea scores.

The notes below on why labels fail remain useful input for Tomoe:

Transcripts from meeting platforms label speakers by device or room, not
by person:
- a room label covers everyone in that room;
- a named label can carry another person's voice;
- vendor summaries repeat these errors as fact.

Misattribution is the most damaging kind of wrong knowledge, because
positions and commitments end up with the wrong person.

Before extraction:
- infer the speaker from address ("thanks, Lee"), ownership ("my team"),
  role and topic consistency, and the attendee list;
- leave a point unattributed when this fails, never guess;
- use a vendor summary only as a second opinion: a disagreement flags an
  error.

Names are misheard and renamed over time, so aliases carry dates.

## 1. Kinds of knowledge

| Kind | Holds | Example (invented) |
|---|---|---|
| **Thread** | an initiative, deal or issue: its state over time, owners, next dates, what blocks it | "Pilot programme: 3 customers (wk 1) → workshop planned (wk 2) → scope cut, no build capacity (wk 3)" |
| **Metric** | value, as-of date, definition, source, disputes | "ARR 15M as of Sep 30, of which 0.8M disputed as mis-categorised" |
| **Disagreement** | the issue, each side, who holds it, how it evolved, whether resolved | "Experiments vs commitments: A and B say test, C says obligation" |
| **Framing** | a phrase or idea someone is advancing: tellers, tellings, who adopted it, reception | "'Navy SEALs' for a specialist team, CEO, wk 3" |
| **Rule / lesson** | trigger → guidance, why, scope, source, number of independent sources, status | "Partner funding is never a discount: a discount is permanent, funding is one-off" |
| **Commitment** | who, what, by when, and what happened later | "Targets next week → sent with small numbers" |
| **Outside fact** | competitor, partner, regulation, with a validity window | "Partner programme terms changed on Oct 1 (cap, concurrency limit)" |
| **Person** | role, ownership, expertise, positions, commitments, how they argue | "Wants modelled targets and clean definitions" |
| **Sensitivity cue** | confidentiality said aloud, with an end date | "Not known to the team until tomorrow's announcement" |

Approaches such as "limitless landing pages" are threads plus rules,
assembled from meetings rather than a catalogue:
- the problem it solves;
- who it fits;
- what must be in place first;
- what blocks it;
- evidence so far;
- who drives it.

## 2. Retrieval by situation

- **Use-when cues.** At extraction, each item gets the future situations
  or questions it will matter for ("useful when a deal includes credits
  for a product that doesn't consume them"). Search matches the situation
  to the cue, not the original wording.
- **Situation signals:**
  - who is present → their open threads, positions, and commitments owed
    in both directions;
  - accounts and entities mentioned → dossiers;
  - the kind of work (deal desk, board prep, planning) → applicable rules;
  - framings in use → their history and reception.
- **Currency.** Every item has a validity window and a "superseded by"
  link. Answers lead with the current state and say what changed and
  when. Stale rules about the outside world are poison.
- **Moments to surface:**
  - before a meeting: a brief per attendee and per agenda item;
  - during it: the live side panel;
  - after it: what changed since last time, and which commitments came due.

## 3. Trust

- Every item rests on extracted points with quotes. An item built on a
  point flagged as unsupported doesn't count as evidence.
- Collective wisdom is weighted by independent sources (people,
  accounts, meetings), not by repetition.
- Hints are precedents with citations, never verdicts.
- Sensitivity follows the most sensitive source; spoken embargoes are
  honoured until they end.

## 4. Order of work

1. (Moved to Tomoe) speaker attribution.
2. Extract the kinds above, linked across weeks, with use-when cues.
   Status (2026-10-16): extraction of all kinds measured and chosen
   (insights-eval.md); linking across weeks is now threads
   (linking-benchmark.md); use-when cues are not extracted yet (the lean
   prompt dropped them to save tokens; tags from clustering are the
   alternative).
3. Review by hand against the reference analysis before any backfill.
4. Grow without losing coherence or economy: scaling.md.
