# Score speaker repair against the reference speakers (gold/*.speakers.jsonl),
# counting only reference lines with high/medium confidence and a named speaker.
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,sys,re,collections
from parse import load
def norm(s): return re.sub(r"[^a-z]","",s.lower())
def same(a,b):
    a,b=norm(a),norm(b); return a==b or (len(a)>4 and len(b)>4 and (a.startswith(b[:6]) or b.startswith(a[:6])))
for path in sys.argv[1:]:
    d=json.load(open(path)); date=d["date"]; m=load(date)
    ref={}
    for l in open(f"gold/{date}.speakers.jsonl"):
        o=json.loads(l); ref.setdefault(o["line"],[]).append(o)
    by={t["line"]:t for t in d["turns"]}
    c=collections.Counter(); base=collections.Counter(); wrong=collections.Counter()
    for n,rs in ref.items():
        if len(rs)!=1: c["multi-speaker lines skipped"]+=1; continue
        r=rs[0]
        if r["speaker"].lower().startswith("unknown") or r["confidence"]=="low": continue
        lab=m["turns"][n-1]["label"]
        base["right" if same(lab,r["speaker"]) else "wrong"]+=1
        t=by.get(n); sp=(t["parts"][0]["speaker"] if t and t["parts"] else "missing")
        if sp=="missing": c["missing"]+=1
        elif sp.lower().startswith("unknown"): c["unknown"]+=1
        elif same(sp,r["speaker"]): c["right"]+=1
        else: c["wrong"]+=1; wrong[(lab,r["speaker"],sp)]+=1
    tot=c["right"]+c["wrong"]+c["unknown"]+c["missing"]
    print(f"{date}: scored {tot} | model right {c['right']} ({100*c['right']/tot:.0f}%) wrong {c['wrong']} unknown {c['unknown']} missing {c['missing']} | raw labels right {base['right']} ({100*base['right']/tot:.0f}%)")
    for (lab,ref_sp,sp),k in wrong.most_common(6): print(f"    {k:3d}x label '{lab}' -> said {sp}, reference {ref_sp}")
