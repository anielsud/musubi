# link-bench

Tools for the linking benchmark: how well Musubi groups extracted items
into subjects and tracks how later items update earlier ones. Method,
tiers and results: `docs/linking-benchmark.md`; findings:
`docs/research-log.md`. Real reference data stays in
`testdata/private/link-bench/`; the synthetic set is in
`testdata/synthetic/link-bench/`.

| Tool | Does |
|---|---|
| `export/` (Go) | writes `statements.jsonl` (every linked statement with the lines it cites, its claim and topic) and a stratified, shuffled `pairs.jsonl` from an instance and its `knowledge -v` log |
| `system_from_db.py` | reads an instance's groups and relations into the scorer's shape; maps ids by meeting and text when statements were re-imported |
| `score.py` | scores systems against the reference: relations on pairs (false and missed retirements, connection), thread updates, grouping (B-cubed, fragmentation, near-miss contamination), current state |
| `guard.py` | applies the thread relation rules (as `threadRule` in Go) to a system's relations |
| `threads.py` | thread prototype, original design |
| `threads2.py` | thread prototype, lean design (candidates, capped state, compact replies) with the measured variants as options |
| `apply_threads.py` | loads a thread system into a copy of an instance, in place of topics and relations, so cards and search can be scored |
| `retrieval.py` | per-subject queries: share of top passages, claims and card lines on the subject, and current items shown |

A system's shape, whatever produced it:
```
{"name": "...", "claim": {statement: claim}, "topic": {statement: topic or thread},
 "relations": [{"later": statement, "earlier": statement, "rel": "supersedes|answers|completes|pending|contradicts|refines|same"}]}
```
