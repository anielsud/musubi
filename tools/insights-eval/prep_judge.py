# Write judge inputs: reference items (R-ids) and candidate items (C-ids) per meeting.
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,sys,os
tag=sys.argv[1]
src=sys.argv[2] if len(sys.argv)>2 else tag   # item-file tag, if different
for d in ["2026-09-16","2026-09-23","2026-09-30","2026-10-07"]:
    ref=[json.loads(l) for l in open(f"gold/{d}.items.jsonl")]
    if os.path.exists(f"judge/{tag}-{d}.cand.txt"):
        print(d,"judge input exists; not overwriting"); continue
    if not os.path.exists(f"items-{src}-{d}.json"): continue
    cand=json.load(open(f"items-{src}-{d}.json"))["items"]
    with open(f"judge/{tag}-{d}.ref.txt","w") as f:
        for i,r in enumerate(ref,1):
            f.write(f"R{i} [{r['kind']}] who={', '.join(r['who'])} lines={r['lines']} :: {r['text']}\n")
    with open(f"judge/{tag}-{d}.cand.txt","w") as f:
        for i,c in enumerate(cand,1):
            f.write(f"C{i} [{c['kind']}] who={', '.join(c['who'])} lines={c['lines']} quotes={c['quotes'][:2]} :: {c['text']}\n")
    print(d,"ref",len(ref),"cand",len(cand))
