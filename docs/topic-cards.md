# Topic cards: from extracted knowledge to the live panel

A card is what someone needs about a subject at the moment it comes up:
where it stands, what is due, what is theirs, what is open, the rules that
apply, what must not be repeated, and who to ask. This document records why
cards exist, how they are built, how they are measured, and what is left.
The design summary is in architecture.md ("Topic cards"); numbers are in
research-log.md (2026-10-14 to 2026-10-16). Examples here are invented;
the real worked example and its gold card are in `testdata/private/`.

## Why (2026-10-14)

A worked example (a confidential programme, discussed in a dedicated slot
in one leadership meeting and updated in the next, then raised in a
different meeting) showed that extraction already captured enough for a
useful card: state, dates, open questions, rules, owners. Four things stood
between that and the panel:

1. The live panel never read the knowledge layer: it searched passages and
   summarised them.
2. Programme names were not triggers: the fast lane spotted people and
   organisations only.
3. Claims were ordered by belief (corroboration), so a programme lead's
   dated milestones ranked as "speculative" and a passing compliment first.
4. Linking hid live items: wrong `supersedes` and `answers` relations
   retired commitments and closed open questions.

Passage search also missed paraphrases that claim search found, so cards
are built from claims, not passages.

## How a card is built

Built when read, from claims under the reader's scope, with no model call
(about 30 ms), so it fits the live panel's fast lane. Nothing is stored
per card; the card index is rebuilt when the knowledge layer changes.

**Finding the subject** (`knowledge.CardIndex.Find`), in order:
1. A trigger term in the text: capitalised words learned from the corpus,
   used by most claims of one topic and few others. Ordinary words (also
   written in lower case) and names (directory aliases, transcript
   speakers, words seen as the one who "said" or "asked", near or short
   forms of known names) are excluded.
2. The query's own words (2–5 content words) found verbatim in at most 12
   claims, most of them in one topic. Short queries embed poorly; this
   fixed lookups such as a two-word workstream name opening another
   thread's card.
3. The query's embedding against claim centroids: the topic of most of
   the nearest six, if the nearest is at least 0.66 similar.

**What it holds** (an `Anchor`):
- **A topic**, with its companions: small topics raised during its
  discussion (most of their statements between two of the topic's, within
  10 minutes, in the same meeting) or using its terms. A catch-all (a topic
  covering more than 40% of a meeting) hosts nothing.
- **A term**, when the term's claims are spread over topics (an event
  people tell stories from): the claims that say it, if there are at least
  4 and the word doesn't usually end a longer name.
- **A thread**, in thread mode: the thread's members, including those it
  shares with another thread, showing only the ones the thread marks
  current. No companions are inferred.

**What it shows:**

| Section | From | Rule |
|---|---|---|
| Header | topic or thread label, last discussed, meetings | as-of date always visible |
| Watch ("don't repeat") | sensitivity cues | first, in red |
| Now | metrics, facts, decisions, outside facts | not superseded, answered, done or disproven |
| Due | commitments; proposed decisions with dates | upcoming soonest first, then undated, then past due |
| Yours | the owner's claims | always shown if present |
| Open | questions still open | a promise to find out shows as pending |
| Rules | rules and requirements | |
| People | holders and named owners | most lines first, up to 4 |

- **Ranking:** salience, not belief: kind, then a figure, a date, recency
  (45-day half-life) and grounding; reported views ("X thinks…") rank
  below state; lines about running the meeting are dropped. A line that a
  picked line refines gives way to it, and a Now line that repeats a rule
  or cue gives way to it. Sections show 2–4 lines.
- **Dates:** commitments show when they fall due relative to now ("due
  tomorrow", "was due 1 Oct"), from dates read out of the text, including
  relative ones ("two weeks after that").
- **Time:** a card shows only what was said by its "now"; a replay of a
  past meeting sees only what was known then.
- **Sensitivity:** a card is as sensitive as its narrowest line.
  Restricted cards, and cards touching HR, pay, health, legal matters or
  private life, are collapsed to their title until opened. Scopes that
  mustn't see a card (a shared screen, a cloud model, a reply to someone
  who wasn't there) get none.

**In the panel** (`live.topic_cards`): a line that says a trigger term
opens the card; a lookup whose nearest claims are on a topic opens it too.
The panel keeps up to four cards, newest first, and rebuilds them when the
scope changes.

**Tools:** `musubi card <words>` prints the card the panel would show;
`--at <date>` replays a date; `--terms` lists what the index learned.

## Measured

- **Card cases** (`eval`, kind `card`; private, on the experiment
  instance): 8–9 of 10 pass with no leaks, depending on the grouping. They
  cover the worked example's gold card, three paraphrases, a replay date,
  three scopes that must get nothing, and two lookalikes.
- **Per-subject queries** (linking benchmark, 36 queries over 15
  subjects): share of card lines on the queried subject. Pairwise linking
  and topics: 75%. Threads: 78–83%; lean threads with the phrase lookup:
  84%.
- Cards show about 15% of a subject's current items in every setup: the
  section limits, not grouping, limit that.

## Not built yet

- A model-based classification stage for conversational HR content (the
  rules miss reduction programmes described without HR vocabulary).
- A restricted project's name marking its mentions elsewhere as sensitive.
- Detecting the owner's own screen share (the toggle sets it today).
- Resolving guests who speak but weren't invited, so People links and the
  audience include them.
- More of a subject's current state on the card without crowding it.
