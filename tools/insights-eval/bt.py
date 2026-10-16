# Baseten Model APIs client with spend tracking and a hard cap.
import os as _os, json as _json
# Data (transcripts, references, runs) lives outside git: INSIGHTS_DATA,
# default testdata/private/insights-eval/insights in the repo.
DATA=_os.environ.get("INSIGHTS_DATA",_os.path.join(_os.path.dirname(_os.path.abspath(__file__)),"..","..","testdata","private","insights-eval","insights"))
CORPUS=_json.load(open(_os.path.join(DATA,"corpus.json")))

import json,time,subprocess,urllib.request,urllib.error,os,threading
PRICES={"openai/gpt-oss-120b":(0.1,0.5,0.01),"vertex:gemini-3.8-flash":(0.75,3.75,0.075),"zai-org/GLM-5.3":(1.4,4.4,0.14),"zai-org/GLM-5.3-Flash":(0.15,0.5,0.03),"deepseek-ai/DeepSeek-V4.1-Flash":(0.3,1.2,0.007),"moonshotai/Kimi-K3":(3,15,0.3)}
LEDGER=os.path.join(DATA,"spend.jsonl")
CAP=float(os.environ.get("SPEND_CAP","8"))
_key=None;_lock=threading.Lock()
def spent():
    try: return sum(json.loads(l)["usd"] for l in open(LEDGER))
    except FileNotFoundError: return 0.0
def closed(sc):
    if isinstance(sc,dict):
        sc={k:closed(v) for k,v in sc.items()}
        if sc.get("type")=="object":
            sc["additionalProperties"]=False; sc["required"]=list(sc.get("properties",{}).keys())
    elif isinstance(sc,list): sc=[closed(x) for x in sc]
    return sc
CACHE=os.path.join(DATA,"cache")
def chat(model,messages,schema,tag,max_tokens=32000):
    import hashlib
    os.makedirs(CACHE,exist_ok=True)
    # CACHE_SALT reruns the same prompts afresh (to measure run-to-run variance).
    k=hashlib.sha256(json.dumps([model,messages,schema,os.environ.get("TEMP","0.7"),os.environ.get("EFFORT","")]+([os.environ["CACHE_SALT"]] if os.environ.get("CACHE_SALT") else [])).encode()).hexdigest()[:20]
    cp=os.path.join(CACHE,k+".json")
    if os.path.exists(cp): return json.load(open(cp))
    try:
        out=_chat(model,messages,schema,tag,max_tokens)
    except json.JSONDecodeError:
        # one retry on malformed JSON, the same for every model
        out=_chat(model,messages[:-1]+[{**messages[-1],"content":messages[-1]["content"]+"\n\n(Reply with valid JSON only.)"}],schema,tag,max_tokens)
    json.dump(out,open(cp,"w"))
    return out
def _chat(model,messages,schema,tag,max_tokens):
    global _key
    if model.startswith("local:"):
        return _local(model,messages,schema,tag,max_tokens)
    if model.startswith("vertex:"):
        return _vertex(model,messages,schema,tag,max_tokens)
    if spent()>=CAP: raise RuntimeError(f"spend cap ${CAP} reached")
    if _key is None:
        _key=subprocess.run(["security","find-generic-password","-s","baseten-api-key","-w"],capture_output=True,text=True).stdout.strip()
    body={**({"reasoning_effort":os.environ["EFFORT"]} if os.environ.get("EFFORT") else {}),"model":model,"temperature":float(os.environ.get("TEMP","0.7")),"top_p":0.95,"max_tokens":max_tokens,"messages":messages,
          "response_format":{"type":"json_schema","json_schema":{"name":"out","schema":closed(schema),"strict":True}}}
    # Streamed: a long, non-streamed generation sends nothing until it is
    # done, and such requests hung for 5-30 minutes (2026-10-15) while the
    # same request streamed in seconds. BT_TIMEOUT bounds the wait for each
    # chunk, not the whole reply.
    body["stream"]=True
    body["stream_options"]={"include_usage":True}
    t=time.time()
    for attempt in range(6):
        try:
            resp=urllib.request.urlopen(urllib.request.Request("https://inference.baseten.co/v1/chat/completions",json.dumps(body).encode(),
                {"Content-Type":"application/json","Authorization":"Bearer "+_key}),timeout=float(os.environ.get("BT_TIMEOUT","120")))
            parts,finish,usage=[],None,{}
            t1=time.time()
            for raw in resp:
                # A reply still streaming after BT_WALL seconds is a reasoning
                # loop at a crawl (seen: 4k reasoning tokens at ~2/s); a fresh
                # sample usually doesn't loop.
                if time.time()-t1>float(os.environ.get("BT_WALL","180")):
                    resp.close(); raise TimeoutError("reply still streaming after BT_WALL")
                raw=raw.strip()
                if not raw.startswith(b"data:"): continue
                data=raw[5:].strip()
                if data==b"[DONE]": break
                ev=json.loads(data)
                if ev.get("usage"): usage=ev["usage"]
                for ch in ev.get("choices") or []:
                    d=ch.get("delta") or {}
                    if d.get("content"): parts.append(d["content"])
                    if ch.get("finish_reason"): finish=ch["finish_reason"]
            r={"usage":usage,"choices":[{"message":{"content":"".join(parts)},"finish_reason":finish}]}
            break
        except urllib.error.HTTPError as e:
            if e.code in (429,500,502,503) and attempt<5: time.sleep(4*2**attempt); continue
            raise
        except (urllib.error.URLError, ConnectionError, TimeoutError) as e:
            # dropped or stalled connections: retry like a 5xx
            if attempt<5: time.sleep(4*2**attempt); continue
            raise
    u=r.get("usage") or {}
    cached=((u.get("prompt_tokens_details") or {}).get("cached_tokens") or 0)
    pi,po,pc=PRICES[model]
    usd=((u.get("prompt_tokens",0)-cached)*pi+cached*pc+u.get("completion_tokens",0)*po)/1e6
    with _lock:
        open(LEDGER,"a").write(json.dumps({"tag":tag,"model":model,"in":u.get("prompt_tokens",0),"cached":cached,"out":u.get("completion_tokens",0),"reasoning":((u.get("completion_tokens_details") or {}).get("reasoning_tokens") or 0),"usd":round(usd,4),"s":round(time.time()-t,1),"finish":r["choices"][0].get("finish_reason")})+"\n")
    msg=r["choices"][0]["message"]
    c=msg.get("content")
    if not c: raise RuntimeError(f"no content (finish={r['choices'][0].get('finish_reason')}, out={u.get('completion_tokens')})")
    c=c[c.find("{"):c.rfind("}")+1]
    return json.loads(c)

LOCAL=os.environ.get("LOCAL_URL","http://127.0.0.1:8081")
def _local(model,messages,schema,tag,max_tokens):
    """llama-server (OpenAI-compatible): same prompts and schema, no cost."""
    think=os.environ.get("THINK")=="1"
    samp={"temperature":0.6,"top_p":0.95,"top_k":20} if think else {"temperature":0}
    if os.environ.get("LOCAL_SAMPLE")=="1" and not think:
        # Qwen's settings for non-thinking mode; greedy decoding loops on long JSON.
        samp={"temperature":0.7,"top_p":0.8,"top_k":20}
    body={"model":"local",**samp,"max_tokens":max_tokens,"messages":messages,
          "response_format":{"type":"json_schema","json_schema":{"name":"out","schema":schema}},
          "chat_template_kwargs":{"enable_thinking":think}}
    t=time.time()
    r=json.load(urllib.request.urlopen(urllib.request.Request(LOCAL+"/v1/chat/completions",json.dumps(body).encode(),{"Content-Type":"application/json"}),timeout=3600))
    u=r.get("usage") or {}; tm=r.get("timings") or {}
    with _lock:
        open(LEDGER.replace("spend","local"),"a").write(json.dumps({"tag":tag,"model":model,"in":u.get("prompt_tokens",0),"out":u.get("completion_tokens",0),"s":round(time.time()-t,1),"pp_tps":round(tm.get("prompt_per_second",0),1),"tg_tps":round(tm.get("predicted_per_second",0),1),"finish":r["choices"][0].get("finish_reason")})+"\n")
    if r["choices"][0].get("finish_reason")=="length":
        raise json.JSONDecodeError("reply hit max_tokens","",0)
    c=r["choices"][0]["message"]["content"]; c=c[c.find("{"):c.rfind("}")+1]
    return json.loads(c)

VERTEX_PROJECT=os.environ.get("VERTEX_PROJECT",CORPUS.get("vertex_project",""))
VCAP=float(os.environ.get("VERTEX_CAP","5"))
_vtok=[None,0.0]
def _vtoken():
    if _vtok[0] is None or time.time()-_vtok[1]>1800:
        _vtok[0]=subprocess.run(["gcloud","auth","application-default","print-access-token"],capture_output=True,text=True).stdout.strip(); _vtok[1]=time.time()
    return _vtok[0]
def vspent():
    try: return sum(json.loads(l)["usd"] for l in open(LEDGER) if json.loads(l)["model"].startswith("vertex:"))
    except FileNotFoundError: return 0.0
def _vertex(model,messages,schema,tag,max_tokens):
    """Vertex AI (global, pay-as-you-go) via its OpenAI-compatible endpoint."""
    if vspent()>=VCAP: raise RuntimeError(f"vertex spend cap ${VCAP} reached")
    body={"model":"google/"+model.split(":",1)[1],"messages":messages,"max_tokens":max_tokens,"temperature":float(os.environ.get("TEMP","0")),
          "response_format":{"type":"json_schema","json_schema":{"name":"out","schema":closed(schema)}}}
    if os.environ.get("EFFORT"): body["reasoning_effort"]=os.environ["EFFORT"]
    url=f"https://aiplatform.googleapis.com/v1/projects/{VERTEX_PROJECT}/locations/global/endpoints/openapi/chat/completions"
    t=time.time()
    for attempt in range(3):
        try:
            r=json.load(urllib.request.urlopen(urllib.request.Request(url,json.dumps(body).encode(),{"Content-Type":"application/json","Authorization":"Bearer "+_vtoken(),"x-goog-user-project":VERTEX_PROJECT}),timeout=900)); break
        except urllib.error.HTTPError as e:
            if e.code in (429,500,503) and attempt<2: time.sleep(5*(attempt+1)); continue
            raise RuntimeError(f"vertex {e.code}: {e.read()[:300]}")
    u=r.get("usage") or {}
    reason=(u.get("completion_tokens_details") or {}).get("reasoning_tokens") or 0
    cached=(u.get("prompt_tokens_details") or {}).get("cached_tokens") or 0
    pi,po,pc=PRICES[model]
    usd=((u.get("prompt_tokens",0)-cached)*pi+cached*pc+(u.get("completion_tokens",0)+reason)*po)/1e6
    with _lock:
        open(LEDGER,"a").write(json.dumps({"tag":tag,"model":model,"in":u.get("prompt_tokens",0),"cached":cached,"out":u.get("completion_tokens",0),"reasoning":reason,"usd":round(usd,5),"s":round(time.time()-t,1),"finish":r["choices"][0].get("finish_reason")})+"\n")
    c=r["choices"][0]["message"].get("content") or ""
    if not c: raise RuntimeError(f"no content (finish={r['choices'][0].get('finish_reason')})")
    c=c[c.find("{"):c.rfind("}")+1]
    return json.loads(c)
