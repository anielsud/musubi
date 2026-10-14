# insights-eval

Tools for measuring insight extraction against a reference. The method
and current results are in `docs/insights-eval.md` and
`docs/research-log.md`.

Data lives outside git, in `$INSIGHTS_DATA` (default
`testdata/private/insights-eval/insights`), which holds:
- `corpus.json`: where the transcripts are, the meeting dates, the file
  pattern, a one-line description, and the Vertex project;
- `directory.json`: names, titles and offices from the company directory;
- `gold/`: the reference (`DATE.speakers.jsonl`, `DATE.items.jsonl`);
- `rendered/`: numbered transcripts as the graders see them;
- `attrib-reference-DATE.json`: reference speakers in the format the
  extractors read;
- run outputs (`items-*.json`), judge inputs and verdicts (`judge/`),
  the spend ledger (`spend.jsonl`, `local.jsonl`) and the call cache
  (`cache/`).

| Script | Does |
|---|---|
| `parse.py` | Teams markdown into turns; filler folded into the previous turn; short speaker aliases |
| `bt.py` | one client for Baseten, Vertex (`vertex:` models) and llama-server (`local:` models): JSON schema, retry, spend caps, call cache |
| `attrib.py` | speaker repair from text (now Tomoe's job; kept for reference) |
| `extract.py` | the first extraction prompt (brief + 50-line segments, full schema) |
| `extract_lean.py` | lean variants; `VARIANT=lean_ex_v2` is the current best |
| `prep_judge.py` | writes judge inputs; never overwrites existing ones |
| `grade.py` | the cheap grader (`GRADER=` model; default Gemini Flash) |
| `score_attrib2.py` | scores speaker repair against the reference speakers |

Examples:
```bash
# extract with GLM-5.3-Flash, prompt v2, all meetings
EFFORT=low TEMP=0 MODEL=zai-org/GLM-5.3-Flash VARIANT=lean_ex_v2 python3 extract_lean.py
python3 prep_judge.py glmf-v2 GLM-5.3-Flash-lean_ex_v2
# grade with Gemini (a model never grades itself)
EFFORT=low GRADER=vertex:gemini-3.8-flash python3 grade.py glmf-v2
```

Keys:
- Baseten: macOS keychain service `baseten-api-key`.
- Vertex: application-default credentials
  (`gcloud auth application-default login`).
