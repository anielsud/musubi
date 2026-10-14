# Parse Teams/Bluedot markdown transcripts into turns, with short speaker
# aliases and filler turns folded into the previous turn.
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import re,json,sys,os
SRC=os.path.expanduser(CORPUS["source_dir"])
MEETINGS=CORPUS["meetings"]
FILL=set("yeah yes yep yup ok okay good great right sure thanks thank you bye cheers hi hello hey mm mhm uhhuh um uh cool perfect awesome exactly totally absolutely correct fine wow oh ah huh very so".split())
def is_filler(t):
    w=re.findall(r"[a-z']+",t.lower())
    return bool(w) and len(w)<=4 and all(x in FILL for x in w)
def load(date):
    raw=open(os.path.join(SRC,CORPUS["file_pattern"].format(date=date))).read()
    head,tr=raw.split("## Transcript",1)
    att=[]
    m=re.search(r"\*\*Attendees:\*\* (.*)",head)
    if m:
        for a in m.group(1).split("),"):
            nm=a.split("(")[0].strip()
            if nm and not nm.lower().startswith("room") and "calendar" not in nm.lower(): att.append(nm)
    turns=[]
    for ts,label,text in re.findall(r"\*\*\[([\d:]+)\] ([^*]+):\*\* (.*)",tr):
        text=text.strip()
        if turns and is_filler(text):
            turns[-1]["folded"].append((label,text)); continue
        turns.append({"ts":ts,"label":label,"text":text,"folded":[]})
    labels=[]
    for t in turns:
        for l in [t["label"]]+[f[0] for f in t["folded"]]:
            if l not in labels: labels.append(l)
    alias={l:f"S{i+1}" for i,l in enumerate(labels)}
    for i,t in enumerate(turns): t["n"]=i+1
    return {"date":date,"attendees":att,"labels":labels,"alias":alias,"turns":turns,"merged":False}
def label_note(l):
    if l.startswith("Room - "): return "a meeting-room device: anyone in that room, often several people"
    if l.startswith("Speaker: "): return "an unidentified voice"
    return "a named participant's device or account"
def render(m,a=1,b=None):
    out=[]
    for t in m["turns"]:
        if t["n"]<a or (b and t["n"]>b): continue
        f="".join(f" ({m['alias'][fl]}: {ft})" for fl,ft in t["folded"])
        out.append(f"{t['n']} [{m['alias'][t['label']]}] {t['text']}{f}")
    return "\n".join(out)
def legend(m):
    return "\n".join(f"{m['alias'][l]} = label \"{l}\" ({label_note(l)})" for l in m["labels"])
if __name__=="__main__":
    for d in MEETINGS:
        m=load(d); r=render(m)
        raw=open(os.path.join(SRC,CORPUS["file_pattern"].format(date=d))).read().split("## Transcript",1)[1]
        print(d,"turns",len(m["turns"]),"labels",len(m["labels"]),"chars",len(r),"vs raw",len(raw),f"({100*len(r)/len(raw):.0f}%)","attendees",len(m["attendees"]))
    m=load("2026-09-30"); print(legend(m)); print(render(m,195,205))
