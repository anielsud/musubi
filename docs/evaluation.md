# Evaluation

How Musubi's quality is measured, and how to add to it. Findings from runs
go in [research-log.md](research-log.md).

## What is measured

| Kind | Question | Metric | Gate |
|---|---|---|---|
| `leak` | Does a narrow scope hide what it must, and still show what it may? | forbidden hits; invariant violations | **zero, always** |
| `search` | Does the right passage come back? | hit@k (default 5), MRR; per mode: hybrid, keyword, vector | tracked |
| `classify` | Is a passage labelled at the right level and categories? | exact match; **under-labelled** count | synthetic: exact |
| `resolve` | Does a mention resolve to the right person/org, or stay ambiguous? | exact match | synthetic: exact |
| `graph` | Are an entity's neighbours right, and nothing hidden shown? | expected/forbidden neighbours and items; visibility | synthetic: exact |
| `statement`, `claim`, `topic` | Is knowledge extracted, linked and grouped as expected? | per case | synthetic: tracked (two known failures, KC01 and KC04) |
| `card` | Does the query open the right card, with the right lines, collapsed when it should be, and nothing in scopes that mustn't see it? | lines found by section; lines that must stay off; recall | private: tracked; leaks: zero |

Two benchmarks live outside this harness because they need hosted or
large local models and a reference built by stronger models:
extraction ([insights-eval.md](insights-eval.md)) and grouping and linking
([linking-benchmark.md](linking-benchmark.md)).

Under-labelling (got a lower level than expected, or missing a category) is
reported separately from over-labelling because it is the error that leaks.

## Three layers of leak checking

1. **Property test** (`internal/store/visibility_test.go`). Builds every
   combination of item level, item category, chunk escalation and audience
   (144 items), and checks the SQL scope filter against `sensitivity.Visible`
   under ordinary and unusual scopes. This is the only layer that exercises
   scopes the product doesn't use yet, which is where an SQL mistake hides.
2. **Invariant re-check** (`internal/eval`). Every result the harness sees,
   under the case's scope and under every `probe_scopes` entry, is re-checked
   against labels read directly from the database with `sensitivity.Visible`.
   A disagreement is a violation even if no case anticipated it.
3. **Cases.** `forbid` targets name what must not come back; `expect` targets
   check that open material still comes through, so a filter that hides
   everything fails too.

Mutation check (2026-10-09): each of these filter bugs was introduced on
purpose and caught.

| Mutation | Property test | Synthetic eval |
|---|---|---|
| audience check removed | caught | caught (1008 violations) |
| audience "all members" weakened to "any member" | caught (46) | not caught |
| category filter removed | caught (195) | not caught |
| chunk escalation ignored | caught (53) | not caught |
| entity level check removed | — | caught (G03) |

The synthetic eval misses three because no current scope separates them (every
categorised chunk is also confidential or above); the property test covers
them. Re-run this check after changing `visSQL` or `Visible`.

## Synthetic corpus (committed)

`testdata/synthetic/` is a fictional company, Northwind Labs; every name is
invented. `gen.py` writes it; `cases.toml` holds the cases. Scenarios:

| Scenario | Where | Why it matters |
|---|---|---|
| Decision changed later | T1 then T7 (batching fix date) | the newer meeting must surface |
| Comp talk inside a 1:1 | T2 | chunk escalation; the rest of the 1:1 stays confidential |
| Same fact public later | T6 all-hands | a fact may be disclosed through its open source only |
| Customer call with deal terms | T3, m2 | the customer may see what was said to them, not internal pricing mail |
| Unidentified participants, no calendar entry | T4 | fail closed |
| All-day block over an ad-hoc call | e9, T4 | must not link; the call would inherit a tiny internal audience |
| Private calendar entry, acquisition | e4, T5 | native label; strategy category; restricted codename and target |
| Internal broadcast | m3, T6 | open to employees, not to customers |
| Small internal working group | T1, T7 | not open to colleagues who weren't there |
| HR mail about a third person | m1 | never in a reply to that person |
| Security disclosure in a group chat | c2 | security category |
| Two people with one first name | c1 ("Sam") | must stay ambiguous without evidence |
| Lower-case live-style text | T8 | org names resolve; "will" is not Will Harper |
| Name only known from the calendar | e5, T4 ("Nina") | first-name resolution to a non-participant |

`make eval` runs it with the deterministic hash embedder; `make test` runs it
as a Go test that fails on any leak or any classify/resolve/graph miss. With
`--embed ollama:<model>` it compares real embedders.

## Private set (never committed)

`testdata/private/cases.toml` runs over the real database. It is gitignored,
like everything under `testdata/private/`. Workflow:

1. `musubi update` with the embedders to compare, e.g.
   `--embed ollama:nomic-embed-text,ollama:qwen3-embedding:0.6b`.
2. `musubi eval --cases testdata/private/cases.toml --embed <same> --json testdata/private/eval-<date>.json`
3. After a change, add `--baseline` with the earlier JSON to see deltas.

Writing cases: use `musubi search` and `musubi show <item>` to find passages;
item ids may be prefixes (`tomoe:7a94e35f`). Write retrieval queries in two
groups: wording close to the transcript, and paraphrases with little word
overlap (the latter is what vectors are for). Write leak cases for every
scope the product will use, with an `expect` alongside each `forbid` where
something should still come through. Classification cases record the owner's
judgement; drafts by Claude must be reviewed.

## Adding a scenario

1. Add the content to `testdata/synthetic/gen.py` (invented names only) and
   run it.
2. Add cases to `testdata/synthetic/cases.toml`.
3. `make test eval`. A new scenario should fail before the change that
   handles it, and pass after.
