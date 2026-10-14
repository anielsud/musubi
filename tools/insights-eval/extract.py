# Stage 2: knowledge items from a speaker-repaired transcript.
# Brief once per meeting, then segments with neighbouring lines as context.
# Only these prompts reach the model (raw chat completions), so this tests
# the prompts themselves.
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,os,re,sys
from concurrent.futures import ThreadPoolExecutor
from parse import load,MEETINGS
from attrib import directory_text
import bt
MODEL=os.environ.get("MODEL","zai-org/GLM-5.3")
OUT=DATA
SEG=int(os.environ.get("SEG","50")); CTX=8
TAG=MODEL.split("/")[-1]+os.environ.get("SUFFIX","")

def repaired_lines(date,speakers_file):
    """Transcript lines with the repaired speaker; label shown only when unknown."""
    m=load(date)
    att={t["line"]:t for t in json.load(open(speakers_file))["turns"]}
    out=[]
    for t in m["turns"]:
        a=att.get(t["n"]); parts=(a or {}).get("parts") or []
        text=t["text"]+"".join(f" ({fl}: {ft})" for fl,ft in t["folded"])
        if len(parts)<=1:
            sp=parts[0]["speaker"] if parts else "unknown"
            who=sp if not sp.lower().startswith("unknown") else f"unknown speaker (label: {t['label']})"
            out.append(f"{t['n']} {who}: {text}")
        else:
            # split at each part's first words, if found
            segs=[];rest=text
            for i,p in enumerate(parts):
                k=rest.find(p["starts"][:30]) if p.get("starts") else -1
                segs.append((k,p["speaker"]))
            pieces=[];idx=[k for k,_ in segs]
            if all(k>=0 for k in idx) and idx==sorted(idx):
                for i,(k,sp) in enumerate(segs):
                    end=idx[i+1] if i+1<len(idx) else len(text)
                    pieces.append(f"{sp}: {text[k:end].strip()}")
                out.append(f"{t['n']} "+" | ".join(pieces))
            else:
                out.append(f"{t['n']} unknown speaker (label: {t['label']}; several voices): {text}")
    return m,out

RULES="""You extract knowledge items from part of an executive leadership meeting transcript, for a knowledge base that people and AI assistants will rely on later. Wrong, invented or misattributed items are much worse than missing ones: they will be used as the basis for decisions.

Item kinds:
- decision: something decided or agreed in the meeting
- commitment: someone will do something (owner; due date if said)
- fact: stated as true (status, events, plans), including organisation and people changes
- metric: a number with what it measures, its as-of date, and its definition or any dispute about it
- position: a stance someone took and why
- disagreement: an issue with two or more sides; name each side and who holds it
- framing: a memorable phrase, metaphor, story or way of framing an issue that someone used to persuade; quote it, say who used it and how the room responded if that shows
- rule: a lesson or rule of thumb: when X, do or don't do Y, because Z (hard rules, and lessons drawn from experience)
- outside_fact: about the outside world (competitors, partners, customers, regulation, market), with when it holds if said
- question: an open question or issue left unresolved
- sensitivity_cue: someone says something is confidential, embargoed, not to be shared, or not yet announced (until when)
- personal: a personal detail about someone (travel, family, background), attributed to them

Each item must stand on its own, as a note a colleague could read cold:
- name people, organisations, products and deals explicitly; never "it", "they", "the deal" without saying which;
- keep numbers, dates, amounts, conditions and negations exactly as said;
- fold in context and what followed: a question and its answer, a proposal and its outcome, a correction made later;
- attribute to the speaker shown on the line (they have been worked out from the transcript); when the speaker is unknown, leave "who" empty rather than guessing; a demand someone relays belongs to its original source;
- never expand acronyms, and never add titles, companies or facts that were not said; the company directory is only for recognising who is meant (e.g. a first name or a misheard name): never copy a title or surname from it into an item;
- leave out garbled passages rather than guessing what they meant; skip small talk unless it is a personal detail;
- use_when: two or three future situations in which this item should be shown to someone (e.g. "drafting an order form that includes credits", "preparing a board update on churn");
- sensitivity: restricted for layoffs or restructuring names and numbers, compensation, legal disputes, individual performance, or departures not yet announced; confidential for deal values, financials and board matters; personal for private life; otherwise normal.
- thread: a short stable name for the initiative, deal or issue the item belongs to (e.g. "Northwind renewal"), or "".

Example (an invented meeting; for format and judgement only, never copy its content):
41 Dana Reyes: Northwind renewal is stuck. They want 40,000 credits but they'll only ever use the reporting module, which doesn't consume credits.
42 Lee Chen: If we put credits they won't use on the order form, finance has to strip them out of every usage report. We learned that the hard way with Bluegill.
43 Dana Reyes: Fine, then credits stay off the form and we price the reporting module on its own.
44 Sam Owner: Agreed. And please, this is not public yet; the price change goes out on the first.
45 Lee Chen: It's like selling fuel to someone with a bicycle.
Items:
{"items":[
 {"kind":"decision","text":"Northwind's renewal will price the reporting module on its own and leave credits off the order form, because Northwind would not consume them.","who":["Dana Reyes","Sam Owner"],"lines":[41,43,44],"quotes":["credits stay off the form and we price the reporting module on its own","Agreed"],"thread":"Northwind renewal","status":"agreed","use_when":["structuring Northwind's renewal","quoting credits to a customer whose products don't consume them"],"sensitivity":"confidential"},
 {"kind":"rule","text":"Don't put credits a customer won't consume on an order form: finance then has to strip them out of every usage report (learned on the Bluegill deal).","who":["Lee Chen"],"lines":[42],"quotes":["finance has to strip them out of every usage report"],"thread":"","status":"stated","use_when":["drafting an order form that includes credits","reviewing usage reporting"],"sensitivity":"normal"},
 {"kind":"sensitivity_cue","text":"Sam Owner said the price change is not public until it goes out on the first of the month.","who":["Sam Owner"],"lines":[44],"quotes":["this is not public yet; the price change goes out on the first"],"thread":"price change","status":"stated","use_when":["talking to customers about pricing before the first"],"sensitivity":"confidential"},
 {"kind":"framing","text":"Lee Chen compared selling credits to a customer who won't use them to \\"selling fuel to someone with a bicycle\\".","who":["Lee Chen"],"lines":[45],"quotes":["selling fuel to someone with a bicycle"],"thread":"Northwind renewal","status":"stated","use_when":["explaining why unused credits don't belong on an order form"],"sensitivity":"normal"}
]}"""

ITEM_SCHEMA={"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{
 "kind":{"type":"string","enum":["decision","commitment","fact","metric","position","disagreement","framing","rule","outside_fact","question","sensitivity_cue","personal"]},
 "text":{"type":"string"},"who":{"type":"array","items":{"type":"string"}},
 "lines":{"type":"array","items":{"type":"integer"}},"quotes":{"type":"array","items":{"type":"string"}},
 "thread":{"type":"string"},"status":{"type":"string","enum":["decided","agreed","proposed","open","answered","rejected","stated","disputed"]},
 "use_when":{"type":"array","items":{"type":"string"}},"sensitivity":{"type":"string","enum":["normal","confidential","restricted","personal"]}}}}}}
BRIEF_SCHEMA={"type":"object","properties":{"purpose":{"type":"string"},
 "participants":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"role":{"type":"string"}}}},
 "subjects":{"type":"array","items":{"type":"string"}},
 "glossary":{"type":"array","items":{"type":"object","properties":{"term":{"type":"string"},"means":{"type":"string"}}}}}}

def run(date):
    m,lines=repaired_lines(date,f"{OUT}/attrib-{os.environ.get('SPEAKERS','GLM-5.3-dir')}-{date}.json")
    head=f"Meeting: {CORPUS['description']}, {date}.\nCompany directory (only for recognising who is meant):\n{directory_text()}\n"
    whole="\n".join(lines)
    br=bt.chat(MODEL,[{"role":"system","content":"You read a whole meeting transcript and write a brief that helps someone interpret any excerpt of it."},
        {"role":"user","content":head+"\nTranscript:\n"+whole+"\n\nWrite: the meeting's purpose; each participant's role in this meeting as far as the transcript shows it; the subjects discussed, in order; and a glossary resolving vague references, acronyms, product names and misheard names (\"the deal\", a product name misheard as a common word, etc.) to what they mean. Say \"unclear\" rather than guess."}],
        BRIEF_SCHEMA,f"brief-{date}",max_tokens=8000)
    btext=("Purpose: "+br["purpose"]+"\nParticipants: "+"; ".join(f"{p['name']} ({p['role'][:80]})" for p in br["participants"])+
           "\nTerms: "+"; ".join(f"{g['term']} = {g['means'][:120]}" for g in br["glossary"][:25]))
    n=len(lines)
    def seg(a):
        b=min(a+SEG-1,n)
        ex=[]
        for i in range(max(1,a-CTX),min(n,b+CTX)+1):
            ex.append(("" if a<=i<=b else "[context] ")+lines[i-1])
        u=(head+"\nBrief of the whole meeting:\n"+btext+
           f"\n\nTranscript excerpt. Lines marked [context] come just before or after the part you cover: use them to understand and complete items, but only extract items whose substance is in the unmarked lines {a}-{b}.\n\n"+"\n".join(ex)+
           "\n\nList every item in the unmarked lines. Evidence: the line numbers and short exact quotes each item rests on.")
        try: return bt.chat(MODEL,[{"role":"system","content":RULES},{"role":"user","content":u}],ITEM_SCHEMA,f"items-{date}-{a}",max_tokens=16000)["items"]
        except Exception as e: print(date,a,"failed",e,flush=True); return []
    with ThreadPoolExecutor(int(os.environ.get("PAR","16"))) as ex: res=list(ex.map(seg,range(1,n+1,SEG)))
    items=[it for r in res for it in r]
    json.dump({"date":date,"model":MODEL,"brief":br,"items":items},open(f"{OUT}/items-{TAG}-{date}.json","w"),indent=1)
    print(date,"items",len(items),"spent $%.2f"%bt.spent(),flush=True)
if __name__=="__main__":
    with ThreadPoolExecutor(4) as ex: list(ex.map(run,sys.argv[1:] or MEETINGS))
