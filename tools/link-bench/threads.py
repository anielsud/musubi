#!/usr/bin/env python3
"""Thread-level linking prototype (docs/linking-benchmark.md, "Candidates").

Meeting by meeting:
1. Assign: each new item goes to one or two specific threads (an initiative,
   deal, programme, metric, issue or event; never a broad theme), or starts
   a new one, or none (meeting logistics, small talk).
2. Reconcile: for each thread the meeting touched, one call with the
   thread's current items and the new ones returns what each new item does
   to earlier ones (supersedes, completes, answers, pending, contradicts,
   corrects, same) and the thread's current items afterwards.

Writes the benchmark's system shape. Uses tools/insights-eval/bt.py (cached,
spend-capped).

Usage: MODEL=zai-org/GLM-5.3-Flash threads.py <bench dir> <out system.json>
"""
import collections, concurrent.futures as cf, json, os, sys, time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "insights-eval"))
import bt  # noqa: E402


def chat(model, messages, schema, tag, max_tokens, empty):
    """bt.chat, with a second try at twice the room when the reply is cut
    off or malformed (streamed replies occasionally are), then empty."""
    for cap in (max_tokens, 2 * max_tokens):
        try:
            return bt.chat(model, messages, schema, tag, max_tokens=cap)
        except (RuntimeError, json.JSONDecodeError) as e:
            print(f"  {tag} at {cap} tokens: {e}", flush=True)
    return empty

MODEL = os.environ.get("MODEL", "zai-org/GLM-5.3-Flash")
RUN = os.environ.get("RUN", "threads")
CHUNK = int(os.environ.get("CHUNK", "70"))
PAR = int(os.environ.get("PAR", "8"))
# A reply is a few hundred to a few thousand tokens; a cap stops a decoding
# loop early (the local 27B looped to 16k tokens at temperature 0).
ASSIGN_MAX = int(os.environ.get("ASSIGN_MAX", "6000"))
REC_MAX = int(os.environ.get("REC_MAX", "3000"))

ASSIGN_SYS = """You organise a leadership team's meeting notes into threads, so that later someone can see the current state of any subject.

A thread is ONE specific subject that can have a state over time: an initiative, programme, deal, account, metric, decision, problem, launch, event, hire or departure. It is never a broad theme ("AI strategy", "marketing", "growth"): if a theme holds several specific subjects, those are separate threads. Two items that share a word, a customer name, an event or a number are not in the same thread unless they are about the same subject.

For every new item, give the thread(s) it belongs to: existing thread ids, or new threads you name (short, specific names; reuse the same new name for every item of the same new subject). Most items belong to exactly one thread; give two only when an item genuinely bears on two subjects. Use none for meeting logistics, greetings, jokes and small talk."""

ASSIGN_SCHEMA = {"type": "object", "properties": {
    "new_threads": {"type": "array", "items": {"type": "object", "properties": {
        "name": {"type": "string"}, "scope": {"type": "string"}}}},
    "assign": {"type": "array", "items": {"type": "object", "properties": {
        "id": {"type": "integer"}, "threads": {"type": "array", "items": {"type": "string"}}}}}}}

REC_SYS = """You keep one thread of a leadership team's notes up to date. You get the thread's items that are current so far (oldest first) and the new items from today's meeting (in the order they were said).

For each new item, say what it does to earlier items in this thread (current ones or new ones said before it today):
- supersedes: a later version of the same plan, figure, decision, status or position that replaces it (within one meeting only when someone corrects or changes it, not when they walk through it)
- corrects: same meeting, a correction of a figure or claim
- completes: says the earlier commitment or plan was done or happened
- answers: gives what an earlier question asked
- pending: commits someone to find out or provide what an earlier question asks (the question stays open)
- contradicts: both cannot be true, about the same specific thing, and neither is clearly the later word
- same: says the same thing again
Most new items do none of these: they add to the thread. Only list real effects.

Then list the thread's current items after today: what someone needs to see to know where the subject stands (latest figures and plans, open questions and commitments, rules in force). Leave out items that were superseded, corrected, completed, answered or repeated."""

REC_SCHEMA = {"type": "object", "properties": {
    "effects": {"type": "array", "items": {"type": "object", "properties": {
        "later": {"type": "integer"}, "earlier": {"type": "integer"},
        "relation": {"type": "string", "enum": ["supersedes", "corrects", "completes", "answers", "pending", "contradicts", "same"]}}}},
    "current": {"type": "array", "items": {"type": "integer"}}}}


def line(s):
    who = ", ".join(s.get("who") or []) or "?"
    st = ("/" + s["status"]) if s.get("status") else ""
    return f"[{s['id']}] ({s['meeting']} m{s['minute']}, {s['kind']}{st}, {who}) {s['text']}"


def main():
    bench, out = sys.argv[1], sys.argv[2]
    stm = [json.loads(l) for l in open(os.path.join(bench, "statements.jsonl"))]
    by = {s["id"]: s for s in stm}
    meetings = sorted({s["meeting"] for s in stm})
    threads = {}  # tid → {"name","scope","items":[],"current":[]}
    member = collections.defaultdict(list)  # statement → [tid]
    relations = []
    calls, t0 = 0, time.time()
    for m in meetings:
        items = sorted([s for s in stm if s["meeting"] == m], key=lambda s: (s["minute"], s["id"]))
        # 1. Assign, chunk by chunk, so new threads from one chunk are known to the next.
        touched = collections.OrderedDict()
        for k in range(0, len(items), CHUNK):
            part = items[k:k + CHUNK]
            reg = "\n".join(f"{tid}: {t['name']} — {t['scope']} (latest: {by[t['current'][-1]]['text'][:140] if t['current'] else '-'})"
                            for tid, t in threads.items()) or "(none yet)"
            msg = f"Existing threads:\n{reg}\n\nNew items from the {m} meeting:\n" + "\n".join(line(s) for s in part) + \
                  "\n\nAssign every new item (by id). For new threads, list each once in new_threads with a one-line scope, and use its name in assign."
            r = chat(MODEL, [{"role": "system", "content": ASSIGN_SYS}, {"role": "user", "content": msg}], ASSIGN_SCHEMA,
                     f"{RUN}-assign-{m}-{k}", ASSIGN_MAX, {})
            calls += 1
            # Outputs sometimes collapse (no or few assignments): ask again
            # for the items left out, with the threads named so far.
            for retry in range(3):
                got = {a["id"] for a in r.get("assign", [])}
                missing = [s for s in part if s["id"] not in got]
                if len(missing) <= len(part) // 20:
                    break
                known = "\n".join(f"- {n['name']}: {n.get('scope', '')}" for n in r.get("new_threads", []))
                msg2 = f"Existing threads:\n{reg}\n\nNew threads already named for this meeting:\n{known or '(none)'}\n\n" + \
                       f"Items from the {m} meeting still to assign:\n" + "\n".join(line(s) for s in missing) + \
                       "\n\nAssign every one of these items (by id). Reuse the threads above where they fit; list any further new threads in new_threads."
                r2 = chat(MODEL, [{"role": "system", "content": ASSIGN_SYS}, {"role": "user", "content": msg2}], ASSIGN_SCHEMA,
                          f"{RUN}-assign-{m}-{k}-retry{retry}", ASSIGN_MAX, {})
                calls += 1
                r = {"new_threads": r.get("new_threads", []) + r2.get("new_threads", []), "assign": r.get("assign", []) + r2.get("assign", [])}
            names = {}
            for nt in r.get("new_threads", []):
                key = nt["name"].strip().lower()
                tid = next((t for t, v in threads.items() if v["name"].strip().lower() == key), None)
                if tid is None:
                    tid = f"T{len(threads)+1}"
                    threads[tid] = {"name": nt["name"].strip(), "scope": nt.get("scope", ""), "items": [], "current": []}
                names[key] = tid
            ids = {s["id"] for s in part}
            for a in r.get("assign", []):
                if a["id"] not in ids:
                    continue
                for ref in a.get("threads", [])[:2]:
                    ref = ref.strip()
                    tid = ref if ref in threads else names.get(ref.lower()) or next(
                        (t for t, v in threads.items() if v["name"].lower() == ref.lower()), None)
                    if tid is None or ref.lower() == "none":
                        continue
                    if tid not in member[a["id"]]:
                        member[a["id"]].append(tid)
                        threads[tid]["items"].append(a["id"])
                        touched.setdefault(tid, []).append(a["id"])

        # 2. Reconcile each touched thread.
        def rec(tid):
            t = threads[tid]
            new = touched[tid]
            msg = f"Thread: {t['name']} — {t['scope']}\n\nCurrent items so far:\n" + \
                  ("\n".join(line(by[i]) for i in t["current"]) or "(none: the thread starts today)") + \
                  f"\n\nNew items from the {m} meeting:\n" + "\n".join(line(by[i]) for i in new)
            return tid, chat(MODEL, [{"role": "system", "content": REC_SYS}, {"role": "user", "content": msg}], REC_SCHEMA,
                             f"{RUN}-rec-{m}-{tid}", REC_MAX, {"effects": [], "current": []})
        with cf.ThreadPoolExecutor(PAR) as ex:
            for tid, r in ex.map(rec, list(touched)):
                calls += 1
                t = threads[tid]
                valid = set(t["current"]) | set(touched[tid])
                for e in r.get("effects", []):
                    if e["later"] in valid and e["earlier"] in valid and e["later"] != e["earlier"]:
                        relations.append({"later": e["later"], "earlier": e["earlier"], "rel": e["relation"]})
                cur = [i for i in r.get("current", []) if i in valid]
                t["current"] = cur or t["current"] + touched[tid]
        print(f"{m}: {len(items)} items, {len(threads)} threads, {len(touched)} touched, {calls} calls, {time.time()-t0:.0f}s", flush=True)
    # Shape for the scorer: claims are statements, joined where "same"; a
    # statement's topic is its first thread; "corrects" retires like supersedes.
    claim = {s["id"]: s["id"] for s in stm}
    for r in relations:
        if r["rel"] == "same":
            claim[r["later"]] = claim.get(r["earlier"], r["earlier"])
    tnum = {tid: i + 1 for i, tid in enumerate(threads)}
    topic = {s["id"]: (tnum[member[s["id"]][0]] if member[s["id"]] else 0) for s in stm}
    rels = [{**r, "rel": "supersedes" if r["rel"] == "corrects" else r["rel"]} for r in relations if r["rel"] != "same"]
    json.dump({"name": f"threads-{MODEL.split('/')[-1]}", "claim": claim, "topic": topic, "relations": rels,
               "threads": {tid: {"name": t["name"], "scope": t["scope"], "items": t["items"], "current": t["current"]} for tid, t in threads.items()},
               "calls": calls, "seconds": round(time.time() - t0)}, open(out, "w"))
    print(f"done: {len(threads)} threads, {len(rels)} relations, {calls} calls, spent so far ${bt.spent():.2f}")


if __name__ == "__main__":
    main()
