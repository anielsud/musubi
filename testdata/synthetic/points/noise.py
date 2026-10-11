# Make a clean synthetic transcript look like real speech recognition output:
# fragments carried across line (and speaker) boundaries, false starts,
# repeats, fillers, and padding chatter. Seeded, so the set is reproducible.
import json,random,re,sys
FILL=["um","uh","like","you know","I mean","so","yeah","right"]
CHAT=["Yeah.","Mm-hmm.","Right, right.","Can you see my screen?","Sorry, go ahead.","Yeah, yeah.","Okay.","Uh, hold on, someone's at the door.","No, go ahead.","Sorry, you cut out for a sec.","Mm.","Totally."]
def disfluent(text,r):
    w=text.split()
    out=[]
    for i,x in enumerate(w):
        if r.random()<0.07 and len(w)>4: out.append(r.choice(FILL)+",")
        if r.random()<0.06: out.append(x)            # repeat
        if r.random()<0.04 and i+2<len(w): out+= [x, w[i+1]]  # false start: "it's the it's the"
        out.append(x)
    return " ".join(out)
def corrupt(lines,seed=7,carry=0.30,chat=0.35,mislabel=0.05):
    r=random.Random(seed); out=[]
    speakers=sorted({l.split(": ",1)[0] for l in lines})
    pending=""
    for l in lines:
        sp,t=l.split(": ",1)
        t=disfluent(t,r)
        if pending:   # a fragment of the previous speaker's line lands at the start of this one
            t=pending+" "+t; pending=""
        w=t.split()
        if len(w)>8 and r.random()<carry:
            k=r.randint(len(w)-4,len(w)-1)
            t," ".join(w[:k]); pending=" ".join(w[k:]); t=" ".join(w[:k])
        if r.random()<mislabel and len(speakers)>1:
            sp=r.choice([s for s in speakers if s!=sp])
        out.append(f"{sp}: {t}")
        if r.random()<chat:
            out.append(f"{r.choice(speakers)}: {r.choice(CHAT)}")
    if pending: out[-1]+=" "+pending
    return out
if __name__=="__main__":
    src,dst=sys.argv[1],sys.argv[2]
    d=json.load(open(src)); d["clean_lines"]=d["lines"]; d["lines"]=corrupt(d["lines"]); d["id"]=d["id"]
    json.dump(d,open(dst,"w"),indent=1,ensure_ascii=False)
    print(dst,len(d["clean_lines"]),"->",len(d["lines"]))
