#!/usr/bin/env python3
"""Lean thread linking (docs/linking-benchmark.md): the threads.py design with
the cost cuts measured on the benchmark (research-log.md, 2026-10-15):

1. Compact outputs. Assign returns groups of item ids per thread code
   (not one entry per item repeating a thread name); reconcile returns only
   effects, and the thread's current items are kept in code (previous +
   new - retired), with the same relation rules as knowledge.threadRule.
2. Skip what can't change: a thread born in this meeting has nothing
   earlier to reconcile against.
3. Bounded prompts. Each assign call sees CAND candidate threads (nearest
   by embedding to the batch's items, plus recently active ones and those
   created this meeting), not every thread; a reconcile call sees at most
   CAP current items.

Usage: RUN=<name> MODEL=zai-org/GLM-5.3-Flash threads2.py <bench dir> <musubi.db> <out system.json>
(the database supplies statement vectors; ids must be the bench's)
"""
import collections, concurrent.futures as cf, json, math, os, sqlite3, struct, sys, time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "insights-eval"))
import bt  # noqa: E402

MODEL = os.environ.get("MODEL", "zai-org/GLM-5.3-Flash")
RUN = os.environ.get("RUN", "lean")
CHUNK = int(os.environ.get("CHUNK", "70"))
PAR = int(os.environ.get("PAR", "8"))
CAND = int(os.environ.get("CAND", "40"))      # candidate threads by similarity, per assign call
RECENT = int(os.environ.get("RECENT", "15"))  # plus threads active in the last two meetings
# RECENT_DAYS: "recent" is active within that many days of the meeting (the
# Go threader's rule, 21), for corpora with many meetings a day; meeting
# names must then start with their date. Unset: the last two meetings.
RECENT_DAYS = int(os.environ.get("RECENT_DAYS", "0"))
CAP = int(os.environ.get("CAP", "25"))        # current items shown per reconcile call
SKIP_NEW = os.environ.get("SKIP_NEW", "1") == "1"
# INLINE: the assign call also reports effects among the batch's own items
# (most updates are within one meeting: 64% on the real benchmark), so a
# thread born this meeting needs no reconcile call of its own.
INLINE = os.environ.get("INLINE", "1") == "1"
ASSIGN_MAX = int(os.environ.get("ASSIGN_MAX", "3000"))
REC_MAX = int(os.environ.get("REC_MAX", "1500"))
# TRACE=<file.jsonl> records every call (prompt, candidates shown, items
# placed, effects returned) for the cost measures in costs.py. A rerun with
# the same settings replays from the call cache at no cost.
TRACE = os.environ.get("TRACE")
_trace_lock = __import__("threading").Lock()


def trace(rec):
    if TRACE:
        with _trace_lock, open(TRACE, "a") as f:
            f.write(json.dumps(rec) + "\n")

ASSIGN_SYS = """You organise a leadership team's meeting notes into threads, so that later someone can see the current state of any subject.

A thread is ONE specific subject that can have a state over time: an initiative, programme, deal, account, metric, decision, problem, launch, event, hire or departure. It is never a broad theme ("AI strategy", "marketing", "growth"): if a theme holds several specific subjects, those are separate threads. Two items that share a word, a customer name, an event or a number are not in the same thread unless they are about the same subject.

You get candidate threads (codes T…) and new items. Put every new item in a group: an existing thread's code, a new thread you define (codes n1, n2, … with a short, specific name and a one-line scope), or none (meeting logistics, greetings, jokes, small talk). An item may be in two groups only when it genuinely bears on two subjects."""
if os.environ.get("SPECIFIC") == "1":
    # The lean prompt reused broad threads (65-item umbrellas): grouping
    # precision 0.86-0.87 against 0.93-0.95 for the original.
    ASSIGN_SYS += """

Use an existing thread only when the item is about that same specific subject, not merely the same theme; otherwise start a new thread. A thread that keeps absorbing items on different specifics has become a theme: split them."""

INLINE_SYS = """

Also list effects between these new items (a later item on an earlier one, same subject):
- supersedes: a later version of the same plan, figure, decision, status or position that replaces it (within one meeting only when someone corrects or changes it, not when they walk through it)
- corrects: a correction of a figure or claim
- answers: gives what an earlier question asked
- pending: commits someone to find out or provide what an earlier question asks (the question stays open)
- contradicts: both cannot be true, about the same specific thing, and neither is clearly the later word
- same: says the same thing again
Most items do none of these; list only real effects."""

ASSIGN_SCHEMA_INLINE = None  # set below

ASSIGN_SCHEMA = {"type": "object", "properties": {
    "new_threads": {"type": "array", "items": {"type": "object", "properties": {
        "code": {"type": "string"}, "name": {"type": "string"}, "scope": {"type": "string"}}}},
    "groups": {"type": "array", "items": {"type": "object", "properties": {
        "thread": {"type": "string"}, "ids": {"type": "array", "items": {"type": "integer"}}}}}}}

ASSIGN_SCHEMA_INLINE = {"type": "object", "properties": {**ASSIGN_SCHEMA["properties"],
    "effects": {"type": "array", "items": {"type": "object", "properties": {
        "later": {"type": "integer"}, "earlier": {"type": "integer"},
        "relation": {"type": "string", "enum": ["supersedes", "corrects", "answers", "pending", "contradicts", "same"]}}}}}}

REC_SYS = """You keep one thread of a leadership team's notes up to date. You get the thread's current items (oldest first) and the new items from today's meeting (in the order they were said).

List only what a new item does to an earlier item in this thread (a current one, or a new one said before it today):
- supersedes: a later version of the same plan, figure, decision, status or position that replaces it (within one meeting only when someone corrects or changes it, not when they walk through it)
- corrects: same meeting, a correction of a figure or claim
- completes: says an earlier commitment or plan was done or happened
- answers: gives what an earlier question asked
- pending: commits someone to find out or provide what an earlier question asks (the question stays open)
- contradicts: both cannot be true, about the same specific thing, and neither is clearly the later word
- same: says the same thing again
Most new items do none of these: they add to the thread. Only list real effects; an empty list is common."""

REC_SCHEMA = {"type": "object", "properties": {
    "effects": {"type": "array", "items": {"type": "object", "properties": {
        "later": {"type": "integer"}, "earlier": {"type": "integer"},
        "relation": {"type": "string", "enum": ["supersedes", "corrects", "completes", "answers", "pending", "contradicts", "same"]}}}}}}

CORRECTION = ["correct", "actually", "revis", "updated", "update:", "instead", "changed", "no longer",
              "moved from", "moved to", "now expected", "now sitting", "not anymore", "rather than"]
RETIRE = {"supersedes", "completes", "answers"}


def rule(rel, earlier, later):
    """knowledge.threadRule: what the kinds and timing allow; None drops it."""
    same = earlier["meeting"] == later["meeting"]
    if rel == "corrects":
        return "supersedes"
    if rel == "completes" and (same or earlier["kind"] not in ("commitment", "decision", "opinion")):
        return None
    if rel == "supersedes" and same and not any(w in later["text"].lower() for w in CORRECTION):
        return None
    return rel


def line(s):
    who = ", ".join(s.get("who") or []) or "?"
    st = ("/" + s["status"]) if s.get("status") else ""
    return f"[{s['id']}] ({s['meeting']} m{s['minute']}, {s['kind']}{st}, {who}) {s['text']}"


def cos(a, b):
    d = sum(x * y for x, y in zip(a, b))
    return d / (math.sqrt(sum(x * x for x in a)) * math.sqrt(sum(y * y for y in b)) or 1)


def embed_all(bench, stm):
    """Statement vectors for a bench without a database (the synthetic set):
    nomic-embed-text through Ollama, as Musubi embeds statements, cached in
    the bench directory."""
    import urllib.request
    path = os.path.join(bench, "vectors.json")
    cache = json.load(open(path)) if os.path.exists(path) else {}
    todo = [s for s in stm if str(s["id"]) not in cache]
    for k in range(0, len(todo), 64):
        part = todo[k:k + 64]
        body = json.dumps({"model": "nomic-embed-text", "input": ["search_document: " + s["text"] for s in part]}).encode()
        r = json.load(urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:11434/api/embed", body, {"Content-Type": "application/json"})))
        for s, v in zip(part, r["embeddings"]):
            cache[str(s["id"])] = v
    if todo:
        json.dump(cache, open(path, "w"))
    out = {}
    for k, v in cache.items():
        n = math.sqrt(sum(x * x for x in v)) or 1
        out[int(k)] = [x / n for x in v]
    return out


def vec_of(vec, i):
    # The long-history replay (link-bench-long) offsets ids by 100000 per cycle.
    return vec.get(i) or vec.get(i % 100000)


def day_of(meeting):
    import datetime
    return datetime.date.fromisoformat(meeting[:10])


def main():
    bench, dbp, out = sys.argv[1], sys.argv[2], sys.argv[3]
    stm = [json.loads(l) for l in open(os.path.join(bench, "statements.jsonl"))]
    by = {s["id"]: s for s in stm}
    vec = {}
    if dbp != "-":
        db = sqlite3.connect(f"file:{dbp}?immutable=1", uri=True)
        for sid, b in db.execute("SELECT statement_id, vec FROM statement_vecs WHERE model = 'nomic-embed-text'"):
            v = struct.unpack(f"{len(b)//4}f", b)
            n = math.sqrt(sum(x * x for x in v)) or 1
            vec[sid] = [x / n for x in v]
    else:
        vec = embed_all(bench, stm)
    meetings = sorted({s["meeting"] for s in stm})
    threads = {}  # code → {"name","scope","items","current","sum","last"}
    member = collections.defaultdict(list)
    relations = []
    calls = collections.Counter()
    spend0, t0 = bt.spent(), time.time()

    def centroid(t):
        n = math.sqrt(sum(x * x for x in t["sum"])) or 1
        return [x / n for x in t["sum"]]

    for mi, m in enumerate(meetings):
        items = sorted([s for s in stm if s["meeting"] == m], key=lambda s: (s["minute"], s["id"]))
        touched = collections.OrderedDict()
        born = set()
        inline_effects = []
        rec_effort = os.environ.get("EFFORT")
        if os.environ.get("ASSIGN_EFFORT"):
            os.environ["EFFORT"] = os.environ["ASSIGN_EFFORT"]
        for k in range(0, len(items), CHUNK):
            part = items[k:k + CHUNK]
            # Candidate threads: nearest by embedding to any item in the
            # batch, recently active, and born this meeting.
            cents = {c: centroid(t) for c, t in threads.items() if t["sum"]}
            score = {c: max((cos(vec_of(vec, s["id"]), v) for s in part if vec_of(vec, s["id"])), default=0) for c, v in cents.items()}
            cand = sorted(score, key=lambda c: -score[c])[:CAND]
            if RECENT_DAYS:
                today = day_of(m)
                recent = sorted((c for c, t in threads.items() if (today - day_of(meetings[t["last"]])).days <= RECENT_DAYS),
                                key=lambda c: -threads[c]["last"])[:RECENT]
            else:
                recent = sorted((c for c, t in threads.items() if t["last"] >= mi - 2), key=lambda c: -threads[c]["last"])[:RECENT]
            show = list(dict.fromkeys(cand + recent + sorted(born)))
            reg = "\n".join(f"{c}: {threads[c]['name']} — {threads[c]['scope']} (latest: {by[threads[c]['current'][-1]]['text'][:140] if threads[c]['current'] else '-'})"
                            for c in show) or "(none yet)"
            msg = f"Candidate threads:\n{reg}\n\nNew items from the {m} meeting:\n" + "\n".join(line(s) for s in part) + \
                  "\n\nPut every new item in a group (by id)."
            r = {}
            for cap in (ASSIGN_MAX, 2 * ASSIGN_MAX):
                try:
                    r = bt.chat(MODEL, [{"role": "system", "content": ASSIGN_SYS + (INLINE_SYS if INLINE else "")}, {"role": "user", "content": msg}],
                                ASSIGN_SCHEMA_INLINE if INLINE else ASSIGN_SCHEMA, f"{RUN}-assign-{m}-{k}", max_tokens=cap,
                                session=f"{RUN}-{os.environ.get('CACHE_SALT', '')}-assign")
                    break
                except (RuntimeError, json.JSONDecodeError) as e:
                    print(f"  assign {m}/{k} at {cap} tokens: {e}", flush=True)
            calls["assign"] += 1
            trace({"step": "assign", "meeting": m, "k": k, "tag": f"{RUN}-assign-{m}-{k}", "system": ASSIGN_SYS + (INLINE_SYS if INLINE else ""),
                   "user": msg, "shown": show, "items": [s["id"] for s in part]})
            for retry in range(3):
                got = {i for g in r.get("groups", []) for i in g.get("ids", [])}
                missing = [s for s in part if s["id"] not in got]
                if len(missing) <= len(part) // 20:
                    break
                named = "\n".join(f"{n['code']}: {n['name']} — {n.get('scope', '')}" for n in r.get("new_threads", []))
                msg2 = f"Candidate threads:\n{reg}\n{named}\n\nItems from the {m} meeting still to place:\n" + \
                       "\n".join(line(s) for s in missing) + "\n\nPut every one of these items in a group (by id); new threads continue the n… numbering."
                try:
                    r2 = bt.chat(MODEL, [{"role": "system", "content": ASSIGN_SYS}, {"role": "user", "content": msg2}], ASSIGN_SCHEMA,
                                 f"{RUN}-assign-{m}-{k}-retry{retry}", max_tokens=2 * ASSIGN_MAX,
                                 session=f"{RUN}-{os.environ.get('CACHE_SALT', '')}-assign")
                except (RuntimeError, json.JSONDecodeError) as e:
                    print(f"  assign retry {m}/{k}: {e}", flush=True)
                    r2 = {}
                calls["assign retry"] += 1
                trace({"step": "assign-retry", "meeting": m, "k": k, "tag": f"{RUN}-assign-{m}-{k}-retry{retry}", "system": ASSIGN_SYS,
                       "user": msg2, "items": [s["id"] for s in missing]})
                r = {"new_threads": r.get("new_threads", []) + r2.get("new_threads", []), "groups": r.get("groups", []) + r2.get("groups", [])}
            local = {}
            for nt in r.get("new_threads", []):
                key = nt["name"].strip().lower()
                code = next((c for c, t in threads.items() if t["name"].strip().lower() == key), None)
                if code is None:
                    code = f"T{len(threads)+1}"
                    threads[code] = {"name": nt["name"].strip(), "scope": nt.get("scope", ""), "items": [], "current": [], "sum": None, "last": mi}
                    born.add(code)
                local[nt.get("code", "").strip()] = code
            ids = {s["id"] for s in part}
            for g in r.get("groups", []):
                ref = g.get("thread", "").strip()
                code = local.get(ref) or (ref if ref in threads else None)
                if code is None:
                    continue
                for i in g.get("ids", []):
                    if i not in ids or code in member[i] or len(member[i]) >= 2:
                        continue
                    member[i].append(code)
                    t = threads[code]
                    t["items"].append(i)
                    t["last"] = mi
                    v = vec_of(vec, i)
                    if v:
                        t["sum"] = list(v) if t["sum"] is None else [x + y for x, y in zip(t["sum"], v)]
                    touched.setdefault(code, []).append(i)
            trace({"step": "placed", "meeting": m, "k": k, "member": {i: member[i] for i in ids}})

        def rec(code):
            t = threads[code]
            shown = t["current"][-CAP:]
            msg = f"Thread: {t['name']} — {t['scope']}\n\nCurrent items:\n" + "\n".join(line(by[i]) for i in shown) + \
                  f"\n\nNew items from the {m} meeting:\n" + "\n".join(line(by[i]) for i in touched[code])
            # Reasoning counts against the cap; a reply cut off there is
            # asked again with twice the room, then given up (no effects).
            for cap in (REC_MAX, 2 * REC_MAX):
                try:
                    out = bt.chat(MODEL, [{"role": "system", "content": REC_SYS}, {"role": "user", "content": msg}], REC_SCHEMA,
                                  f"{RUN}-rec-{m}-{code}", max_tokens=cap, session=f"{RUN}-{os.environ.get('CACHE_SALT', '')}-rec-{m}")
                    trace({"step": "rec", "meeting": m, "code": code, "tag": f"{RUN}-rec-{m}-{code}", "system": REC_SYS, "user": msg,
                           "current": len(shown), "new": len(touched[code]), "effects": len(out.get("effects", []))})
                    return code, out
                except (RuntimeError, json.JSONDecodeError) as e:
                    print(f"  reconcile {code} at {cap} tokens: {e}", flush=True)
            trace({"step": "rec", "meeting": m, "code": code, "tag": f"{RUN}-rec-{m}-{code}", "system": REC_SYS, "user": msg,
                   "current": len(shown), "new": len(touched[code]), "effects": 0, "failed": True})
            return code, {"effects": []}
            if INLINE:
                inpart = {s["id"] for s in part}
                for e in r.get("effects", []):
                    a, b = e.get("later"), e.get("earlier")
                    if a in inpart and b in inpart and a != b and set(member[a]) & set(member[b]):
                        inline_effects.append(e)

        if rec_effort is None:
            os.environ.pop("EFFORT", None)
        else:
            os.environ["EFFORT"] = rec_effort

        def needs(c):
            # A thread born today has nothing earlier to reconcile, unless an
            # open question was raised and more followed (a promise to find
            # out, or the answer, in the same meeting).
            if threads[c]["current"] or not SKIP_NEW:
                return True
            if INLINE:
                return False  # its in-meeting effects came with the assign reply
            new = touched[c]
            qs = [k for k, i in enumerate(new) if by[i]["kind"] == "question" and by[i].get("status", "open") in ("", "open")]
            return bool(qs) and qs[0] < len(new) - 1
        todo = [c for c in touched if needs(c)]
        calls["reconcile skipped"] += len(touched) - len(todo)
        results = {}
        # One reconcile alone first, so the rest find the shared instructions
        # in the provider's cache. WARM=0 sends all at once, as before.
        first = todo[:1] if os.environ.get("WARM", "1") == "1" else []
        for code, r in map(rec, first):
            calls["reconcile"] += 1
            results[code] = r
        with cf.ThreadPoolExecutor(PAR) as ex:
            for code, r in ex.map(rec, todo[len(first):]):
                calls["reconcile"] += 1
                results[code] = r
        kept, retired = collections.Counter(), collections.Counter()
        for code in touched:
            t = threads[code]
            new = touched[code]
            cur = t["current"] + new
            fresh = set(new)
            valid = set(t["current"][-CAP:]) | fresh
            mine = [e for e in inline_effects if e["later"] in fresh and e["earlier"] in fresh]
            seen_e = set()
            for e in results.get(code, {}).get("effects", []) + mine:
                key = (e.get("later"), e.get("earlier"))
                if key in seen_e:
                    continue
                seen_e.add(key)
                a, b = e.get("later"), e.get("earlier")
                if a not in fresh or b not in valid or a == b:
                    continue
                rel = e.get("relation")
                if rel == "same":
                    cur = [i for i in cur if i != a]
                    continue
                rel = rule(rel, by[b], by[a])
                if rel is None:
                    continue
                relations.append({"later": a, "earlier": b, "rel": rel})
                kept[code] += 1
                if rel in RETIRE:
                    cur = [i for i in cur if i != b]
                    retired[code] += 1
            t["current"] = cur
            trace({"step": "kept", "meeting": m, "code": code, "kept": kept.get(code, 0), "retired": retired.get(code, 0)})
        print(f"{m}: {len(items)} items, {len(threads)} threads, {len(touched)} touched, {len(todo)} reconciled, "
              f"{dict(calls)}, {time.time()-t0:.0f}s, ${bt.spent()-spend0:.3f}", flush=True)
    claim = {s["id"]: s["id"] for s in stm}
    tnum = {c: i + 1 for i, c in enumerate(threads)}
    topic = {s["id"]: (tnum[member[s["id"]][0]] if member[s["id"]] else 0) for s in stm}
    json.dump({"name": f"{RUN}-{MODEL.split('/')[-1]}", "claim": claim, "topic": topic, "relations": relations,
               "threads": {c: {"name": t["name"], "scope": t["scope"], "items": t["items"], "current": t["current"]} for c, t in threads.items()},
               "calls": dict(calls), "seconds": round(time.time() - t0), "usd": round(bt.spent() - spend0, 4)}, open(out, "w"))
    print(f"done: {len(threads)} threads, {len(relations)} relations, {dict(calls)}, {time.time()-t0:.0f}s, ${bt.spent()-spend0:.3f}")


if __name__ == "__main__":
    main()
