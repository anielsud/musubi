# Lean variants of extract.py (which stays frozen as the baseline).
#   VARIANT=lean        lean output schema (kind, text, who, lines, status)
#   VARIANT=lean_ex     + one item per distinct point, never merge
#   VARIANT=lean_ex_in  + 100-line segments, no brief, fixed text first (cacheable)
#   VARIANT=lean_ex_v2_rich  v2 with the brief in full (every role, subject and term)
#   VARIANT=lean_ex_v2_full  v2 with the whole transcript in every call in place of
#                            the brief: shared by a meeting's calls, so cached
#   RUN_SUFFIX=...           appended to the output name, so reruns don't overwrite
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,os,sys
from concurrent.futures import ThreadPoolExecutor
from parse import MEETINGS
from attrib import directory_text
from extract import repaired_lines,BRIEF_SCHEMA
import bt
MODEL=os.environ.get("MODEL","vertex:gemini-3.8-flash")
V=os.environ.get("VARIANT","lean")
OUT=DATA
SEG={"lean_ex_in":100,"lean_ex_30":30}.get(V,50); CTX=8
NOBRIEF=V in ("lean_ex_in","lean_ex_nobrief","lean_ex_v2_full")
TAG=MODEL.split("/")[-1]+"-"+V+os.environ.get("RUN_SUFFIX","")

KINDS="""Item kinds:
- decision: something decided or agreed in the meeting
- commitment: someone will do something (owner; due date if said)
- fact: stated as true (status, events, plans), including organisation and people changes
- metric: a number with what it measures, its as-of date, and its definition or any dispute about it
- position: a stance someone took and why
- disagreement: an issue with two or more sides; name each side and who holds it
- framing: a memorable phrase, metaphor or story someone used to persuade; quote it and say who used it
- rule: a lesson or rule of thumb: when X, do or don't do Y, because Z
- outside_fact: about the outside world (competitors, partners, customers, regulation, market)
- question: an open question or issue left unresolved
- sensitivity_cue: someone says something is confidential, embargoed or not yet announced (until when)
- personal: a personal detail about someone, attributed to them"""

RULES="""You extract knowledge items from part of an executive leadership meeting transcript, for a knowledge base that people and AI assistants will rely on later. Wrong, invented or misattributed items are much worse than missing ones.

"""+KINDS+"""

Each item must stand on its own, as a note a colleague could read cold:
- name people, organisations, products and deals explicitly; never "it", "they", "the deal" without saying which;
- keep numbers, dates, amounts, conditions and negations exactly as said;
- fold in context and what followed: a question and its answer, a proposal and its outcome, a correction made later;
- attribute to the speaker shown on the line; when the speaker is unknown, leave "who" empty; a demand someone relays belongs to its original source;
- never expand acronyms, and never add titles, surnames, companies or facts that were not said; the company directory is only for recognising who is meant;
- leave out garbled passages rather than guessing; skip small talk unless it is a personal detail;
- lines: the line numbers the item rests on."""

EXHAUST="""

Be exhaustive. Make one item per distinct point: each number, each decision, each commitment, each position, each rule, each framing gets its own item. Never merge two points into one item, and never summarise several turns into one general statement. A dense 50-line stretch of a leadership meeting typically holds 12 to 20 items."""

V2_RULES="""

Status and kind discipline:
- Use decision only when the meeting clearly agreed or decided it, and commitment only when the person clearly committed ("I'll do it", "will do", an accepted ask). A suggestion, an idea, "we should", "maybe", "I'd like to" or "let's think about" is a position or a proposal with status proposed, not a decision or commitment.
- Keep who will do something exactly as said; do not move a task, a date or an occasion to someone or somewhere else.

Nothing that was not said:
- Write names exactly as they were said (a first name stays a first name); never add surnames, titles, departments, roles, channels, places or explanations.
- If a word looks misheard (a product or company name where a person seems meant, or an odd phrase), do not build an item on your guess of what it meant.
- When a line's speaker is unknown, leave who empty; never infer who said it from the content.
- lines: cite every line the item draws on, not just one."""

EXAMPLE="""

Example (an invented meeting; for format and judgement only, never copy its content):
41 Dana Reyes: Northwind renewal is stuck. They want 40,000 credits but they'll only ever use the reporting module, which doesn't consume credits.
42 Lee Chen: If we put credits they won't use on the order form, finance has to strip them out of every usage report. We learned that the hard way with Bluegill.
43 Dana Reyes: Fine, then credits stay off the form and we price the reporting module on its own.
44 Sam Owner: Agreed. And please, this is not public yet; the price change goes out on the first.
45 Lee Chen: It's like selling fuel to someone with a bicycle.
Items:
{"items":[
 {"kind":"fact","text":"Northwind's renewal is stuck: Northwind wants 40,000 credits but will only use the reporting module, which doesn't consume credits.","who":["Dana Reyes"],"lines":[41],"status":"stated"},
 {"kind":"decision","text":"Northwind's renewal will leave credits off the order form and price the reporting module on its own.","who":["Dana Reyes","Sam Owner"],"lines":[43,44],"status":"agreed"},
 {"kind":"rule","text":"Don't put credits a customer won't consume on an order form: finance then has to strip them out of every usage report (learned on the Bluegill deal).","who":["Lee Chen"],"lines":[42],"status":"stated"},
 {"kind":"sensitivity_cue","text":"The price change is not public until it goes out on the first of the month.","who":["Sam Owner"],"lines":[44],"status":"stated"},
 {"kind":"framing","text":"Lee Chen compared selling credits to a customer who won't use them to \\"selling fuel to someone with a bicycle\\".","who":["Lee Chen"],"lines":[45],"status":"stated"}
]}"""

SCHEMA={"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{
 "kind":{"type":"string","enum":["decision","commitment","fact","metric","position","disagreement","framing","rule","outside_fact","question","sensitivity_cue","personal"]},
 "text":{"type":"string"},"who":{"type":"array","items":{"type":"string"}},
 "lines":{"type":"array","items":{"type":"integer"}},
 "status":{"type":"string","enum":["decided","agreed","proposed","open","answered","rejected","stated","disputed"]}}}}}}

def run(date):
    m,lines=repaired_lines(date,f"{OUT}/attrib-reference-{date}.json")
    if "_v2" in V:
        import re as _re
        lines=[_re.sub(r"unknown speaker \(label: [^)]*?(; several voices)?\)","unknown speaker",l) for l in lines]
    run_lines(date,date,lines,CORPUS["description"],f"{OUT}/items-{TAG}-{date}.json")

def run_lines(key,date,lines,description,outpath):
    """Extracts one meeting's items from its numbered lines ("N speaker: text")."""
    system=RULES+(EXHAUST if V!="lean" else "")+(V2_RULES if "_v2" in V else "")+EXAMPLE
    head=f"Meeting: {description}, {date}.\nCompany directory (only for recognising who is meant):\n{directory_text()}\n"
    btext=""
    if not NOBRIEF:
        br=bt.chat(MODEL,[{"role":"system","content":"You read a whole meeting transcript and write a brief that helps someone interpret any excerpt of it."},
            {"role":"user","content":head+"\nTranscript:\n"+"\n".join(lines)+"\n\nWrite: the meeting's purpose; each participant's role in this meeting as far as the transcript shows it; the subjects discussed, in order; and a glossary resolving vague references, acronyms, product names and misheard names to what they mean. Say \"unclear\" rather than guess."}],
            BRIEF_SCHEMA,f"brief-{key}",max_tokens=8000,session=f"extract-{TAG}-{key}")
        btext=("\nBrief of the whole meeting:\nPurpose: "+br["purpose"]+"\nParticipants: "+"; ".join(f"{p['name']} ({p['role'][:80]})" for p in br["participants"])+
               "\nTerms: "+"; ".join(f"{g['term']} = {g['means'][:120]}" for g in br["glossary"][:25]))
        if V=="lean_ex_v2_rich":
            # Nothing cut: every participant's role, the subjects in order,
            # every glossary entry.
            btext=("\nBrief of the whole meeting:\nPurpose: "+br["purpose"]+"\nParticipants:\n"+"\n".join(f"- {p['name']}: {p['role']}" for p in br["participants"])+
                   ("\nSubjects, in order:\n"+"\n".join(f"- {x}" for x in br.get("subjects",[])) if br.get("subjects") else "")+
                   "\nTerms:\n"+"\n".join(f"- {g['term']} = {g['means']}" for g in br["glossary"]))
    if V=="lean_ex_in": system=system+"\n\n"+head   # fixed text first: identical across calls, so it can be cached
    n=len(lines)
    # segment boundaries: fixed line counts, or (lean_ex_tok) about 3k tokens of text each
    if V=="lean_ex_tok":
        bounds=[];a=1;size=0
        for i in range(1,n+1):
            size+=len(lines[i-1])
            if size>=12000 or i==n: bounds.append((a,i)); a=i+1; size=0
    else:
        bounds=[(a,min(a+SEG-1,n)) for a in range(1,n+1,SEG)]
    ends={a:b for a,b in bounds}
    def ctx_range(a,b):
        if V!="lean_ex_tok": return max(1,a-CTX),min(n,b+CTX)
        lo=a;used=0
        while lo>1 and used<2400: lo-=1; used+=len(lines[lo-1])
        hi=b;used=0
        while hi<n and used<2400: hi+=1; used+=len(lines[hi-1])
        return lo,hi
    def seg(a):
        b=ends[a]
        if V=="lean_ex_v2_full":
            # The whole meeting first (the same for every call of the
            # meeting, so the provider caches it), the part to cover last.
            u=(head+"\nThe whole meeting transcript, for understanding (numbered lines):\n\n"+"\n".join(lines)+
               f"\n\nYour part is lines {a}-{b}. Only extract items whose substance is in lines {a}-{b}; use the rest of the meeting to understand and complete them (what was asked, answered, corrected or decided before or after).\n\nList every item in lines {a}-{b}.")
            try: return bt.chat(MODEL,[{"role":"system","content":system},{"role":"user","content":u}],SCHEMA,f"lean-{V}{os.environ.get('RUN_SUFFIX','')}-{key}-{a}",max_tokens=16000,session=f"extract-{TAG}-{key}")["items"]
            except Exception as e: print(key,a,"failed",e,flush=True); return []
        lo,hi=ctx_range(a,b)
        ex=[("" if a<=i<=b else "[context] ")+lines[i-1] for i in range(lo,hi+1)]
        u=((head if V!="lean_ex_in" else "")+btext+
           f"\n\nTranscript excerpt. Lines marked [context] come just before or after the part you cover: use them to understand and complete items, but only extract items whose substance is in the unmarked lines {a}-{b}.\n\n"+"\n".join(ex)+
           "\n\nList every item in the unmarked lines.")
        try: return bt.chat(MODEL,[{"role":"system","content":system},{"role":"user","content":u}],SCHEMA,f"lean-{V}{os.environ.get('RUN_SUFFIX','')}-{key}-{a}",max_tokens=16000,session=f"extract-{TAG}-{key}")["items"]
        except Exception as e: print(key,a,"failed",e,flush=True); return []
    # The first segment alone, then the rest: every segment's prompt starts
    # with the same instructions, directory and brief, which the provider
    # serves from its cache (at about a fifth of the price) only once one
    # call has finished with it. WARM=0 sends all at once, as before.
    starts=[a for a,_ in bounds]
    first=[seg(starts[0])] if os.environ.get("WARM","1")=="1" else []
    with ThreadPoolExecutor(16) as ex: res=first+list(ex.map(seg,starts[len(first):]))
    items=[dict(it,quotes=[]) for r in res for it in r]
    json.dump({"date":date,"key":key,"model":MODEL,"variant":V,"items":items},open(outpath,"w"),indent=1)
    print(key,V,"items",len(items),flush=True)
if __name__=="__main__":
    with ThreadPoolExecutor(4) as ex: list(ex.map(run,sys.argv[1:] or MEETINGS))
