# Synthetic link benchmark: Northwind Analytics

A small, fully invented benchmark for thread linking: grouping meeting knowledge items into subjects, and tracking how later items update earlier ones. The company (Northwind Analytics, a B2B analytics software firm), its people, customers (Halvorsen Retail Group, Kestrel Foods, Marlow Health, Brightwater Logistics, Pinecrest Bank, Ostara Mobility), partners (Greyfield Consulting, Arcturus Partners, Delvan Group), vendors (Vantor AI, Cirrusbase, Codewright, Fernleaf CMS), products (Lumen, Lumen Copilot) and the Summitcon conference are all fictional, and so are all the figures. It contains no real meeting data, so it can be shared and committed.

Six weekly leadership meetings (2026-03-02 to 2026-04-06), 250 statements, 12 reference threads, 142 labelled pairs.

## Files

- `statements.jsonl`: knowledge items in the meeting extractor's shape (`id, meeting, minute, kind, status, who, text, lines`), in the order said. It includes small talk, logistics, duplicates under different kinds, long items and partial answers.
- `reference.json`: reference threads (`thread, scope, members, near_misses, states, updates, open_at_end`). `threads-S.json` is an identical copy, so the scorer picks it up.
- `pairs.jsonl`: stratified pairs (`pair, stratum, earlier, later, sim, same_meeting, system`). `sim` is 0 and `system` is "not judged": the pairs were planted, not sampled from a system.
- `labels.jsonl`: blind reference labels per pair (`relation, retires_earlier, confidence, note`). `labels-S.jsonl` is an identical copy, so the scorer picks it up.

Unthreaded statements (small talk, logistics, one-off facts) are deliberately left out of every thread.

## Threads

| Thread | Members | Updates | Near misses | Open at end |
|---|---|---|---|---|
| Project Larch restructuring | 25 | 8 | 5 | 218 |
| Gross revenue churn | 23 | 10 | 4 | 222, 223 |
| Northwind Alliance partner programme | 19 | 8 | 2 | 114 |
| Halvorsen Retail deal and reporting | 20 | 12 | 2 | - |
| Tomas Lindqvist departure | 12 | 2 | 4 | 195, 196 |
| Lumen Copilot product bet | 26 | 12 | 7 | 158, 228 |
| Mid-market pricing analysis | 10 | 4 | 2 | 206 |
| Internal tooling spend forecast | 14 | 9 | 3 | - |
| Customer onboarding time to first dashboard | 13 | 7 | 3 | 35 |
| Marketing transformation | 16 | 7 | 4 | 235 |
| Summitcon conference and lessons | 13 | 3 | 6 | - |
| Hiring freeze question | 9 | 7 | 7 | - |

## Planted patterns

Each pattern is a stratum in `pairs.jsonl`. Pairs are written as earlier→later statement ids, with the reference relation.

### lookalike (21)

Items sharing a word (pack, freeze, onboarding), a customer (Kestrel), an event/city (Summitcon, Lisbon, Rotterdam), a number (12.9%, $410k) or a first name (Tomas / Tom Feeney) but on different subjects. Reference: `unrelated`; listed as `near_misses`.

42→51 `unrelated`; 53→162 `unrelated`; 233→236 `unrelated`; 42→128 `unrelated`; 217→233 `unrelated`; 64→83 `unrelated`; 38→65 `unrelated`; 138→158 `unrelated`; 139→228 `unrelated`; 46→74 `unrelated`; 160→164 `unrelated`; 74→159 `unrelated`; 34→50 `unrelated`; 121→201 `unrelated`; 4→48 `unrelated`; 97→200 `unrelated`; 10→234 `unrelated`; 96→248 `unrelated`; 106→110 `unrelated`; 10→140 `unrelated`; 187→247 `unrelated`

### same-minute walkthrough (10)

A status and its walk-through said in the same minute. Reference: `refines`, never `supersedes`.

11→12 `refines`; 22→23 `refines`; 44→45 `refines`; 96→97 `refines`; 108→109 `refines`; 104→105 `refines`; 151→152 `refines`; 184→185 `refines`; 224→225 `refines`; 138→139 `refines`

### in-meeting correction (4)

A figure corrected within the same minute ("actually it's 12.9 not 12.6"). Reference: `supersedes` (thread update `corrects`).

63→64 `supersedes`; 71→72 `supersedes`; 120→121 `supersedes`; 150→151 `supersedes`

### later revision (20)

A figure, date, target or term revised in a later meeting. Reference: `supersedes`.

11→64 `supersedes`; 64→104 `supersedes`; 147→184 `supersedes`; 184→220 `supersedes`; 28→108 `supersedes`; 108→157 `supersedes`; 157→192 `supersedes`; 192→224 `supersedes`; 34→121 `supersedes`; 121→197 `supersedes`; 197→231 `supersedes`; 38→150 `supersedes`; 151→203 `supersedes`; 102→183 `supersedes`; 15→112 `supersedes`; 112→241 `supersedes`; 44→127 `supersedes`; 22→72 `supersedes`; 42→161 `supersedes`; 122→159 `supersedes`

### promise to find out (7)

A question followed by a promise to find out. Reference: `pending`; the question stays open.

8→9 `pending`; 16→18 `pending`; 31→33 `pending`; 74→77 `pending`; 79→80 `pending`; 164→165 `pending`; 79→211 `pending`

### real answer later (11)

The real answer, often weeks later (some same-meeting). Reference: `answers`.

8→59 `answers`; 16→145 `answers`; 31→81 `answers`; 74→178 `answers`; 79→227 `answers`; 141→142 `answers`; 100→102 `answers`; 156→189 `answers`; 17→19 `answers`; 75→76 `answers`; 58→237 `answers`

### nearby answer to a different question (4)

An answer said next to a question but answering a different one. Reference: `same_subject`, not `answers`.

16→19 `same_subject`; 74→76 `same_subject`; 31→32 `same_subject`; 100→101 `same_subject`

### different owner commitment (7)

One owner's commitment followed by a different owner's on the same subject. Reference: `same_subject`, not `supersedes`.

39→40 `same_subject`; 35→36 `same_subject`; 7→61 `same_subject`; 42→85 `same_subject`; 80→228 `same_subject`; 195→196 `same_subject`; 13→186 `same_subject`

### done said of opinion or fact (5)

"Done" or "happened" said about an opinion or a fact, where nothing was owed. Reference: `same_subject`/`refines`, not `completes`.

4→51 `same_subject`; 14→106 `refines`; 30→111 `same_subject`; 47→167 `same_subject`; 3→176 `same_subject`

### commitment completed later (18)

A commitment actually delivered in a later meeting. Reference: `completes`.

7→96 `completes`; 9→59 `completes`; 39→150 `completes`; 124→203 `completes`; 40→204 `completes`; 42→233 `completes`; 85→160 `completes`; 61→216 `completes`; 26→245 `completes`; 36→198 `completes`; 68→105 `completes`; 180→243 `completes`; 13→148 `completes`; 80→227 `completes`; 77→178 `completes`; 33→81 `completes`; 18→145 `completes`; 154→203 `completes`

### same point two kinds (5)

The same point extracted twice under different kinds in one meeting. Reference: `same`.

6→7 `same`; 27→28 `same`; 178→179 `same`; 187→188 `same`; 220→221 `same`

### raised in passing (10)

A subject raised in passing during another subject's discussion. It belongs to its own thread (`refines`/`same_subject` with it) and is `unrelated` to the discussion it was said in. Tests grouping.

15→56 `same_subject`; 53→56 `unrelated`; 74→103 `refines`; 99→103 `unrelated`; 34→73 `same_subject`; 72→73 `unrelated`; 108→149 `refines`; 148→149 `unrelated`; 96→144 `refines`; 142→144 `unrelated`

### broad AI theme (6)

Items about AI in different threads (Summitcon lessons, Copilot, AI coding seats, AI social copy, Copilot in a deal). Reference: `unrelated`; there is no catch-all AI thread.

27→53 `unrelated`; 38→57 `unrelated`; 108→126 `unrelated`; 81→152 `unrelated`; 55→126 `unrelated`; 23→32 `unrelated`

### sensitivity cue (5)

Confidentiality cues tied to Project Larch that do not name it. Reference: `same_subject` with Larch items; members of the Larch thread. A cue about a different subject (the departure) is a lookalike.

6→10 `same_subject`; 60→62 `same_subject`; 96→98 `same_subject`; 99→182 `same_subject`; 217→219 `same_subject`

### contradicts (5)

Disagreements on figures or treatment. Reference: `contradicts`.

24→25 `contradicts`; 118→119 `contradicts`; 15→20 `contradicts`; 151→153 `contradicts`; 44→87 `contradicts`

### partial answer (4)

Partial answers and partial completions. Reference: `pending` where the question or commitment stays open.

114→115 `pending`; 195→238 `pending`; 66→67 `answers`; 61→181 `pending`

Other planted structure in the reference threads:

- The Larch thread includes a legal deadline (works council notice by April 10) that came up while Larch was being discussed, from the question (8) through the answer (59) to the filing (216).
- Brand packs for event social graphics belong to the marketing transformation thread. Summitcon (the CEO's conference and the lessons she told from it) is a separate thread, although both mention events.
- The churn figure (11, 63/64, 104, 147, 184, 220) goes 12.4 → 12.6/12.9 (corrected in the meeting) → 13.1 → 13.1 → 12.8 → 12.7.
- The hiring freeze question is asked (74), promised twice (77, 165), partly addressed during the Larch discussion (103), and answered three weeks later (178).

## Usage

Use the same scorer as the real benchmark:

```
python3 tools/link-bench/score.py testdata/synthetic/link-bench <system.json>
```

The scorer reads `statements.jsonl`, `pairs.jsonl`, `labels-*.jsonl` and `threads-*.json`. A system file carries `claim`, `topic` and `relations` for the statement ids. Per-stratum agreement shows which pattern a linker fails on. Thread grouping is scored with B-cubed over the reference members, together with near-miss contamination. Retirement is checked against the last state's `current` list.
