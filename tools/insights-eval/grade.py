# Cheap grader: pair candidate items with reference items by cited lines,
# compare side by side, and check only the unmatched ones against the
# transcript. Who-said-it is checked in code against the reference speakers.
#   python3 grade.py TAG [DATE ...]     reads judge/TAG-DATE.cand.txt
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,os,re,sys,collections
from concurrent.futures import ThreadPoolExecutor
import bt
OUT=DATA
MODEL=os.environ.get("GRADER","vertex:gemini-3.8-flash")
BATCH=8

def load_items(path,prefix):
    out=[]
    for l in open(path):
        m=re.match(r"(\w+) \[(\w+)\] who=(.*?) lines=\[([^\]]*)\](?: quotes=.*?)? :: (.*)",l.rstrip("\n"))
        if not m: continue
        out.append({"id":m.group(1),"kind":m.group(2),"who":[w.strip() for w in m.group(3).split(",") if w.strip()],
                    "lines":[int(x) for x in m.group(4).split(",") if x.strip()],"text":m.group(5)})
    return out

def norm(s): return re.sub(r"[^a-z]","",s.lower())

SIDE_SYS="""You compare knowledge items extracted from a meeting with reference items from the same lines. For each candidate, decide whether one reference item says the same thing.
- same: the candidate states the same thing as that reference item, with the same substance (numbers, conditions, people, outcome). Wording may differ; the candidate may be a subset if nothing it says differs.
- partial: about the same thing, but the candidate drops or changes something important (a number, a condition, the outcome, who), or merges it with something else.
- none: no reference item says this.
Pick the single best reference item. Judge substance, not style."""
SIDE_SCHEMA={"type":"object","properties":{"verdicts":{"type":"array","items":{"type":"object","properties":{
  "c":{"type":"string"},"r":{"type":"string"},"degree":{"type":"string","enum":["same","partial","none"]}},"required":["c","r","degree"]}}},"required":["verdicts"]}

COVER_SYS="""For each reference knowledge item from a meeting, decide whether the extracted items listed under it, taken together, cover it.
- covered: the extracted items convey the same substance (numbers, conditions, people, outcome); wording may differ and it may be spread over several items.
- partly: they cover it but miss or change something important.
- missing: they do not convey it."""
COVER_SCHEMA={"type":"object","properties":{"verdicts":{"type":"array","items":{"type":"object","properties":{
  "r":{"type":"string"},"coverage":{"type":"string","enum":["covered","partly","missing"]}},"required":["r","coverage"]}}},"required":["verdicts"]}

CHECK_SYS="""You check knowledge items extracted from a meeting transcript against the transcript lines they cite. Each line shows the person speaking (as identified for this meeting).
For each item decide:
- correct: everything it says is supported by the lines (paraphrase is fine)
- wrong: it misstates what was said: a wrong number or date, reversed meaning, a suggestion stated as a decision or vice versa, two things merged, the wrong thing named
- invented: it adds something the lines do not say at all (a name, surname, title, company, background fact, expanded acronym)
- trivial: true, but meeting logistics or small talk nobody would want recalled
Be strict: one wrong detail makes it wrong."""
CHECK_SCHEMA={"type":"object","properties":{"verdicts":{"type":"array","items":{"type":"object","properties":{
  "c":{"type":"string"},"verdict":{"type":"string","enum":["correct","wrong","invented","trivial"]},"why":{"type":"string"}},"required":["c","verdict","why"]}}},"required":["verdicts"]}

def speakers(date):
    sp={}
    for l in open(f"{OUT}/gold/{date}.speakers.jsonl"):
        o=json.loads(l)
        if o["confidence"] in ("high","medium") and not o["speaker"].lower().startswith("unknown"):
            sp.setdefault(o["line"],set()).add(o["speaker"])
    return sp

def transcript_lines(date):
    lines={}
    for l in open(f"{OUT}/rendered/{date}.txt"):
        m=re.match(r"(\d+) \[S\d+\] (.*)",l)
        if m: lines[int(m.group(1))]=m.group(2)
    return lines

def grade(tag,date):
    ref=load_items(f"{OUT}/judge/{tag}-{date}.ref.txt","R")
    cand=load_items(f"{OUT}/judge/{tag}-{date}.cand.txt","C")
    sp=speakers(date); tl=transcript_lines(date)
    rby={r["id"]:r for r in ref}
    def near(a,b,k): return any(abs(x-y)<=k for x in a for y in b)
    # 1. side by side, batched
    jobs=[]
    for c in cand:
        nb=[r for r in ref if near(c["lines"],r["lines"],2)] or [r for r in ref if near(c["lines"],r["lines"],6)]
        jobs.append((c,nb[:6]))
    def side(batch):
        txt=[]
        for c,nb in batch:
            txt.append(f"CANDIDATE {c['id']} [{c['kind']}] who={', '.join(c['who']) or '-'}: {c['text']}")
            for r in nb: txt.append(f"   reference {r['id']} [{r['kind']}] who={', '.join(r['who']) or '-'}: {r['text']}")
            if not nb: txt.append("   (no reference items on these lines)")
        return bt.chat(MODEL,[{"role":"system","content":SIDE_SYS},{"role":"user","content":"\n".join(txt)+"\n\nGive one verdict per candidate; r is the best reference id, or \"\" for none."}],SIDE_SCHEMA,f"grade-side-{tag}-{date}",4000)["verdicts"]
    batches=[jobs[i:i+BATCH] for i in range(0,len(jobs),BATCH)]
    with ThreadPoolExecutor(int(os.environ.get('GPAR','16'))) as ex: sv=[v for b in ex.map(side,batches) for v in b]
    side_v={v["c"]:v for v in sv}
    # 2. transcript check for everything not 'same'
    left=[c for c in cand if side_v.get(c["id"],{}).get("degree")!="same"]
    def ctx(c):
        ls=sorted({n for x in c["lines"] for n in range(x-6,x+7) if n in tl})
        return "\n".join(f"{n} {'/'.join(sorted(sp.get(n,{'unknown'})))}: {tl[n]}" for n in ls)
    def check(batch):
        txt="\n\n".join(f"ITEM {c['id']} [{c['kind']}] cites lines {c['lines']}: {c['text']}\nLines:\n{ctx(c)}" for c in batch)
        return bt.chat(MODEL,[{"role":"system","content":CHECK_SYS},{"role":"user","content":txt+"\n\nOne verdict per item."}],CHECK_SCHEMA,f"grade-check-{tag}-{date}",4000)["verdicts"]
    lb=[left[i:i+4] for i in range(0,len(left),4)]
    with ThreadPoolExecutor(int(os.environ.get('GPAR','16'))) as ex: cv={v["c"]:v for b in ex.map(check,lb) for v in b}
    # 3. combine; who checked in code
    res={}
    for c in cand:
        s=side_v.get(c["id"],{}); v="correct" if s.get("degree")=="same" else cv.get(c["id"],{}).get("verdict","correct")
        if v=="correct" and c["who"]:
            heard=set().union(*[sp.get(n,set()) for x in c["lines"] for n in (x-1,x,x+1)]) if c["lines"] else set()
            if heard and not any(norm(w)[:6] in {norm(h)[:6] for h in heard} for w in c["who"]): v="misattributed"
        res[c["id"]]=v
    # 4. recall: is each reference item covered by its nearby candidates?
    rjobs=[]
    for r in ref:
        nb=[c for c in cand if near(c["lines"],r["lines"],2)] or [c for c in cand if near(c["lines"],r["lines"],6)]
        rjobs.append((r,nb[:8]))
    def cover(batch):
        txt=[]
        for r,nb in batch:
            txt.append(f"REFERENCE {r['id']} [{r['kind']}] who={', '.join(r['who']) or '-'}: {r['text']}")
            for c in nb: txt.append(f"   extracted {c['id']}: {c['text']}")
            if not nb: txt.append("   (nothing extracted near these lines)")
        return bt.chat(MODEL,[{"role":"system","content":COVER_SYS},{"role":"user","content":"\n".join(txt)+"\n\nOne verdict per reference item."}],COVER_SCHEMA,f"grade-cover-{tag}-{date}",4000)["verdicts"]
    rb=[rjobs[i:i+BATCH] for i in range(0,len(rjobs),BATCH)]
    with ThreadPoolExecutor(int(os.environ.get('GPAR','16'))) as ex: rv0={v["r"]:v["coverage"] for b in ex.map(cover,rb) for v in b}
    rv={r["id"]:{"covered":"found","partly":"partial"}.get(rv0.get(r["id"]),"missed") for r in ref}
    suffix="" if MODEL=="vertex:gemini-3.8-flash" else "."+MODEL.split("/")[-1]
    json.dump({"cand":res,"ref":rv,"side":side_v,"check":cv},open(f"{OUT}/judge/{tag}-{date}.cheap{suffix}.json","w"),indent=1)
    return res,rv

if __name__=="__main__":
    tag=sys.argv[1]; dates=sys.argv[2:] or ["2026-09-16","2026-09-23","2026-09-30","2026-10-07"]
    C=collections.Counter(); R=collections.Counter()
    for d in dates:
        res,rv=grade(tag,d); C.update(res.values()); R.update(rv.values())
        print(d,dict(collections.Counter(res.values())),dict(collections.Counter(rv.values())),flush=True)
    print("TOTAL cand",dict(C),"ref",dict(R),"vertex spent $%.3f"%bt.vspent())
