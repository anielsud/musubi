# Stage 1: work out who actually spoke each turn. A cast pass first (who is
# who, under which labels), then windows of turns against the whole meeting.
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,os,sys
from concurrent.futures import ThreadPoolExecutor
from parse import load,render,legend,MEETINGS
import bt
MODEL=os.environ.get("MODEL","zai-org/GLM-5.3")
OUT=DATA
WIN=int(os.environ.get("WIN","60"))
SYSTEM="""You work out who actually spoke each turn of a meeting transcript produced by Microsoft Teams.

The speaker labels are unreliable. They come from devices and accounts, not from people:
- a meeting-room label is anyone in that room, often several people in a row;
- a named label is usually that person, but a device can carry someone else's voice (someone using another person's laptop or microphone, or the transcription service matching the wrong voice);
- "Speaker: X" labels are unidentified voices;
- in some transcripts one turn contains several people's words run together.

Decide each turn from evidence in the transcript, in this order of strength:
1. being addressed or answered by name ("Thanks, Dana", "Priya, you're across that", "Lee, great deck");
2. what the speaker says about themselves: their role, team, org, region, what they own or did ("my team", "I spoke to the CSMs", "I'm in Austin");
3. continuity: the same voice continuing a presentation or an argument across turns;
4. what others say about the person elsewhere in the meeting.
Names are often misheard by speech recognition (e.g. a first name rendered as a similar-sounding word); match them to the roster when it is clear.

Rules:
- The speaker must be someone on the roster or named in the meeting. Never invent a person.
- If the evidence is weak, answer "unknown". A wrong name is far worse than unknown.
- Evidence quotes must be copied exactly from the transcript, with their line number.
- Short reactions ("Yeah", "Right") may be "unknown" unless continuity makes them clear.
- Give evidence (at most two short quotes) only when the speaker is not the person the label names, or the label is a room or unidentified voice; otherwise leave evidence empty.
- Keep each quote under 15 words."""
CAST_SCHEMA={"type":"object","properties":{"cast":{"type":"array","items":{"type":"object","properties":{
 "person":{"type":"string"},"role":{"type":"string"},"labels":{"type":"array","items":{"type":"string"}},
 "evidence":{"type":"array","items":{"type":"object","properties":{"line":{"type":"integer"},"quote":{"type":"string"}}}}}}},
 "label_notes":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"note":{"type":"string"}}}}}}
TURN_SCHEMA={"type":"object","properties":{"turns":{"type":"array","items":{"type":"object","properties":{
 "line":{"type":"integer"},
 "parts":{"type":"array","items":{"type":"object","properties":{
   "starts":{"type":"string"},"speaker":{"type":"string"},"confidence":{"type":"string","enum":["high","medium","low"]},
   "evidence":{"type":"array","items":{"type":"object","properties":{"line":{"type":"integer"},"quote":{"type":"string"}}}}}}}}}}}}
DIRECTORY=json.load(open(os.path.join(OUT,"directory.json")))["people"]
def directory_text():
    return "\n".join(f"- {n}: {t}" + (f" ({o})" if o else "") for n,t,o in DIRECTORY)
def prefix(m):
    return (f"Meeting: {CORPUS['description']}, {m['date']}. Guests may join for single topics.\n"
            f"Company directory (people who may be present, with title and office; others named in the meeting may also speak):\n{directory_text()}\n"
            f"Invited per the calendar: {', '.join(m['attendees']) or '(not available)'}\n\n"
            f"Speaker labels:\n{legend(m)}\n\nTranscript (line number, [label], text; '(Sx: yeah)' marks a short reaction by label Sx folded into the turn):\n{render(m)}")
def run(date):
    m=load(date); p=prefix(m)
    base=[{"role":"system","content":SYSTEM},{"role":"user","content":p}]
    if os.environ.get("CAST_FROM"):
        cast=json.load(open(os.environ["CAST_FROM"].replace("DATE",date)))["cast"]
    else:
      cast=bt.chat(MODEL,base+[{"role":"user","content":"First, the cast: for each person who speaks in this meeting, their role as far as the transcript shows it, which labels they speak under, and the evidence. Then a note on each label: who it carries."}],CAST_SCHEMA,f"cast-{date}",max_tokens=30000)
    ctext="Cast worked out so far (use it, but correct it where a turn's evidence says otherwise):\n"+"\n".join(f"- {c['person']}: {c['role']}; speaks under {', '.join(c['labels'])}" for c in cast["cast"])+"\n"+"\n".join(f"- label {n['label']}: {n['note']}" for n in cast["label_notes"])
    n=len(m["turns"])
    def win(a):
        b=min(a+WIN-1,n)
        ask=(f"{ctext}\n\nNow attribute every turn from line {a} to line {b}, one entry per line. "
             "Give one part per speaker in the turn (usually one; several only when the turn clearly runs different people's words together, each part starting with its exact first words).")
        return bt.chat(MODEL,base+[{"role":"user","content":ask}],TURN_SCHEMA,f"turns-{date}-{a}",max_tokens=int(os.environ.get("MAXTOK","30000")))
    def safe(a):
        for attempt in range(2):
            try: return win(a)
            except Exception as e: print(date,a,"failed:",e,flush=True)
        return {"turns":[]}
    with ThreadPoolExecutor(4) as ex: res=list(ex.map(safe,range(1,n+1,WIN)))
    turns=[t for r in res for t in r["turns"]]
    json.dump({"date":date,"model":MODEL,"cast":cast,"turns":turns},open(f"{OUT}/attrib-{MODEL.split('/')[-1]}{os.environ.get('SUFFIX','')}-{date}.json","w"),indent=1)
    print(date,"turns",n,"attributed",len(turns),"spent so far $%.2f"%bt.spent(),flush=True)
if __name__=="__main__":
    for d in sys.argv[1:] or MEETINGS: run(d)
