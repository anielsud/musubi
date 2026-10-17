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

import numpy as np

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
# Fixes from the backfill audits (research log 2026-10-16), off by default:
# FIX_REC=1  reconcile sees the thread's current items most relevant to the
#            new ones (by meaning, plus the latest few) instead of only the
#            latest CAP; its prompt says what replaces what; an item is never
#            retired by one from an earlier meeting; "completes" also takes
#            positions and proposals.
# CROSS=1    reconcile also sees closely related current items of other
#            threads, and may retire them (updates often land in a sibling).
FIX_REC = os.environ.get("FIX_REC") == "1"
CROSS = os.environ.get("CROSS") == "1"
CROSS_SIM = float(os.environ.get("CROSS_SIM", "0.80"))
CROSS_MAX = int(os.environ.get("CROSS_MAX", "10"))
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

# ASSIGN_V2 (backfill audits): subjects fragmented by the angle of each
# sentence and merged through people and meetings; this wording replaces
# SPECIFIC's, and candidates show their size, dates and two latest items.
ASSIGN_V2 = os.environ.get("ASSIGN_V2") == "1"
if ASSIGN_V2:
    ASSIGN_SYS = ASSIGN_SYS.split("\n\nUse an existing thread only when")[0] + """

Prefer an existing thread when the item continues its subject: a new figure, an outcome, a follow-up, a decision, or a different angle on the same initiative, deal, programme or event, even when the wording differs. Start a new thread only for a subject none of the candidates covers.
A person is not a subject: put items about someone's role, hire, departure or performance in the thread about that event (one thread per hire, departure or case), not in a thread about the person.
A meeting is not a subject either: an interview, a 1:1 or a review covers several subjects; put each item in the thread of its own subject."""
# MERGE=1: after each meeting's assignment, threads born in it are compared
# with the most similar older threads (one call per meeting) and merged
# into them when they are the same subject.
MERGE = os.environ.get("MERGE") == "1"
MERGE_SIM = float(os.environ.get("MERGE_SIM", "0.75"))
# LAPSE_DAYS=N: open commitments and questions not touched for N days stop
# being current (dated plans otherwise stay current for ever).
LAPSE_DAYS = int(os.environ.get("LAPSE_DAYS", "0"))
LAPSE_KINDS = ("commitment", "question")

# CONSOLIDATE=1: after reconcile, each touched thread with more than
# CONSOLIDATE_MIN current items has its whole current list reviewed once:
# which items are no longer the current word on the subject (the audits
# found 86% of items current: reconcile only judges what new items do).
CONSOLIDATE = os.environ.get("CONSOLIDATE") == "1"
CONSOLIDATE_MIN = int(os.environ.get("CONSOLIDATE_MIN", "8"))
CONSOLIDATE_MAX = int(os.environ.get("CONSOLIDATE_MAX", "80"))
# CONS_PAST=0: consolidation may only retire an item that a later item in
# the list replaces ("past" without a replacement retired positions,
# framings and standing facts: R4, research log 2026-10-17); lapse covers
# dated commitments and questions.
CONS_PAST = os.environ.get("CONS_PAST", "1") == "1"
# CONS_RULE=1: consolidation's replacements pass the same rules as
# reconcile's (rule()); R5 retired live items through in-meeting
# walk-throughs taken for replacement.
CONS_RULE = os.environ.get("CONS_RULE") == "1"
SAME_LATER = os.environ.get("SAME_LATER") == "1"
CONS_SYS = """You keep one thread of a leadership team's notes up to date, so that someone asking about the subject sees only where things stand now. You get the thread's current items, oldest first, each with its meeting date, and today's date.

List the items that are no longer the current word on this subject:
- superseded: a later item in the list gives a newer version of the same figure, target, date, plan, decision, status or position
- completed: a later item says the commitment or plan was done, or the event happened
- answered: a later item answers the question
- past: the item was about something now over (an event that has happened, a deadline long past) and a later item shows things moved on
For superseded, completed and answered, give the later item that does it.
Keep everything still true or still open: rules, standing facts, decisions still in force, open questions nobody answered, commitments not yet due. When unsure, keep the item."""
CONS_SCHEMA = {"type": "object", "properties": {"outdated": {"type": "array", "items": {"type": "object", "properties": {
    "id": {"type": "integer"}, "by": {"type": "integer"},
    "why": {"type": "string", "enum": ["superseded", "completed", "answered", "past"]}}}}}}

# SPLIT=1: after each meeting, a touched thread holding at least SPLIT_MIN
# items, and half again more than when it was last checked, is divided into
# its specific subjects (one call): year-long catch-all threads were the
# main grouping problem left by t4 (research log 2026-10-17).
SPLIT = os.environ.get("SPLIT") == "1"
SPLIT_MIN = int(os.environ.get("SPLIT_MIN", "60"))
SPLIT_SHOW = int(os.environ.get("SPLIT_SHOW", "160"))
SPLIT_SYS = """You keep a leadership team's notes organised in threads, one per specific subject (an initiative, programme, deal, account, metric, decision, problem, launch, event, hire or departure). This thread has grown, and may now hold several specific subjects, or a broad theme. You get its items, oldest first.

If it holds one specific subject, return no groups. Otherwise keep the subject most of it is about as the thread itself (its name and scope, possibly reworded), and list each other specific subject it holds as a group: a short, specific name, a one-line scope, and the ids of its items. Leave in the thread itself items that belong to no specific subject. Do not split one subject by angle (figures, decisions, follow-ups on the same programme stay together)."""
SPLIT_SCHEMA = {"type": "object", "properties": {
    "main": {"type": "object", "properties": {"name": {"type": "string"}, "scope": {"type": "string"}}},
    "groups": {"type": "array", "items": {"type": "object", "properties": {
        "name": {"type": "string"}, "scope": {"type": "string"}, "ids": {"type": "array", "items": {"type": "integer"}}}}}}}

MERGE_SYS = """You keep a leadership team's notes organised in threads, one per specific subject. Some threads were just started in today's meeting; each is shown with older threads that look similar. Say which new threads are the same specific subject as an older one (the same initiative, deal, programme, metric, issue, event or hire), so they can be merged. Different subjects that share a word, a person, a customer or a meeting are not the same. When unsure, do not merge."""
MERGE_SCHEMA = {"type": "object", "properties": {"merges": {"type": "array", "items": {"type": "object", "properties": {
    "new": {"type": "string"}, "into": {"type": "string"}}}}}}

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

REC_FIX = """

What replaces what:
- A later figure for the same quantity and period (a forecast, a target, a count, a date) supersedes the earlier one, even months later.
- An outcome (done, launched, decided, signed, lost, cancelled, hired, left) completes or supersedes the plans, proposals, commitments and open questions it settles, even months later.
- Adding detail or context to an earlier item does not supersede it; a disagreement is "contradicts", not "supersedes".
- Only a newer item can replace an older one; never mark the newest statement of something as replaced by an older one.
Items under "Related items in other threads" may be affected too, by the same rules."""

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
    kinds = ("commitment", "decision", "opinion") + (("position",) if FIX_REC else ())
    if rel == "completes" and (same or (earlier["kind"] not in kinds and not (FIX_REC and earlier.get("status") == "proposed"))):
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
    midx = {mm: k for k, mm in enumerate(meetings)}
    seq = [0]  # thread codes: T1, T2, … (merged threads leave gaps)

    def tcent(code):
        ids = [i for i in threads[code]["items"] if i in vrow]
        if not ids:
            return None
        c = V[[vrow[i] for i in ids]].mean(axis=0)
        return c / (np.linalg.norm(c) or 1)

    def split_threads(m, mi, touched):
        """SPLIT: divide grown threads into their specific subjects."""
        todo = [c for c in touched if c in threads and len(threads[c]["items"]) >= SPLIT_MIN
                and len(threads[c]["items"]) >= 1.5 * threads[c].get("checked", 0)]
        def one(code):
            t = threads[code]
            items = t["items"]
            if len(items) > SPLIT_SHOW:  # the latest, and an even sample of the rest
                step = (len(items) - SPLIT_SHOW // 2) / (SPLIT_SHOW // 2)
                items = [items[int(k * step)] for k in range(SPLIT_SHOW // 2)] + items[-SPLIT_SHOW // 2:]
            msg = f"Thread: {t['name']} — {t['scope']}\n\nItems (oldest first):\n" + "\n".join(line(by[i]) for i in items) + \
                  "\n\nDoes this thread hold several specific subjects? Group them."
            try:
                return code, items, bt.chat(MODEL, [{"role": "system", "content": SPLIT_SYS}, {"role": "user", "content": msg}], SPLIT_SCHEMA,
                                            f"{RUN}-split-{m}-{code}", max_tokens=REC_MAX, session=f"{RUN}-{os.environ.get('CACHE_SALT', '')}-split-{m}")
            except (RuntimeError, json.JSONDecodeError) as e:
                print(f"  split {code}: {e}", flush=True)
                return code, items, {}
        with cf.ThreadPoolExecutor(PAR) as ex:
            results = list(ex.map(one, todo))
        for code, shown_items, r in results:
            calls["split check"] += 1
            t = threads[code]
            t["checked"] = len(t["items"])
            if r.get("main", {}).get("name"):
                t["name"], t["scope"] = r["main"]["name"].strip(), r["main"].get("scope", t["scope"])
            allowed = set(shown_items)
            taken = set()
            for g in r.get("groups", []):
                ids = [i for i in g.get("ids", []) if i in allowed and i not in taken]
                if len(ids) < 3 or not g.get("name", "").strip():
                    continue  # a group of one or two items isn't a subject of its own
                seq[0] += 1
                nc = f"T{seq[0]}"
                threads[nc] = {"name": g["name"].strip(), "scope": g.get("scope", ""), "items": ids,
                               "current": [i for i in t["current"] if i in set(ids)], "sum": None, "last": mi, "first": mi, "checked": len(ids)}
                for i in ids:
                    member[i] = [nc if c == code else c for c in member[i]]
                taken |= set(ids)
                calls["split off"] += 1
                trace({"step": "split", "meeting": m, "from": code, "into": nc, "items": len(ids)})
            if taken:
                t["items"] = [i for i in t["items"] if i not in taken]
                t["current"] = [i for i in t["current"] if i not in taken]

    def merge_new_threads(m, mi, born, touched):
        """MERGE: ask, in one call, which threads born in this meeting are
        an older thread's subject, and fold them in."""
        old = [c for c in threads if c not in born]
        if not old:
            return
        oc = {c: tcent(c) for c in old}
        oc = {c: v for c, v in oc.items() if v is not None}
        if not oc:
            return
        codes = list(oc)
        M = np.array([oc[c] for c in codes])
        pairs = []
        for n in sorted(born):
            v = tcent(n)
            if v is None:
                continue
            sims = M @ v
            near = [codes[k] for k in np.argsort(-sims)[:2] if sims[k] >= MERGE_SIM]
            if near:
                pairs.append((n, near))
        if not pairs:
            return
        def show(c, k):
            t = threads[c]
            return f"{c}: {t['name']} — {t['scope']}\n" + "\n".join(f"   - {by[i]['text'][:160]}" for i in t["items"][-k:])
        msg = "\n\n".join("New thread " + show(n, 5) + "\nSimilar older threads:\n" + "\n".join(show(o, 3) for o in near) for n, near in pairs) + \
              "\n\nList the new threads that are the same subject as one of their older threads."
        try:
            r = bt.chat(MODEL, [{"role": "system", "content": MERGE_SYS}, {"role": "user", "content": msg}], MERGE_SCHEMA,
                        f"{RUN}-merge-{m}", max_tokens=REC_MAX, session=f"{RUN}-{os.environ.get('CACHE_SALT', '')}-merge")
        except (RuntimeError, json.JSONDecodeError) as e:
            print(f"  merge {m}: {e}", flush=True)
            return
        calls["merge"] += 1
        allowed = {n: set(near) for n, near in pairs}
        for mg in r.get("merges", []):
            n, o = mg.get("new", "").strip(), mg.get("into", "").strip()
            if n not in allowed or o not in allowed[n] or n not in threads or o not in threads:
                continue
            src, dst = threads.pop(n), threads[o]
            for i in src["items"]:
                if i not in dst["items"]:
                    dst["items"].append(i)
                member[i] = [o if c == n else c for c in member[i]]
                member[i] = list(dict.fromkeys(member[i]))
            dst["last"] = mi
            moved = touched.pop(n, [])
            touched.setdefault(o, [])
            for i in moved:
                if i not in touched[o]:
                    touched[o].append(i)
            born.discard(n)
            calls["merged"] += 1
            trace({"step": "merge", "meeting": m, "new": n, "into": o})
    vrow = {sid: k for k, sid in enumerate(vec)}
    V = np.array([vec[sid] for sid in vec], dtype=np.float32) if vec else np.zeros((0, 1), dtype=np.float32)
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
            if ASSIGN_V2:
                def desc(c):
                    t = threads[c]
                    span = f"{len(t['items'])} items, {meetings[t['first']][:10]} to {meetings[t['last']][:10]}"
                    lat = " | ".join(by[i]["text"][:110] for i in t["current"][-2:]) or "-"
                    return f"{c}: {t['name']} — {t['scope']} ({span}; latest: {lat})"
                reg = "\n".join(desc(c) for c in show) or "(none yet)"
            else:
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
                    seq[0] += 1
                    code = f"T{seq[0]}"
                    threads[code] = {"name": nt["name"].strip(), "scope": nt.get("scope", ""), "items": [], "current": [], "sum": None, "last": mi, "first": mi}
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

        if MERGE and born:
            merge_new_threads(m, mi, born, touched)

        def shown_for(code):
            """The thread's current items reconcile sees: the latest CAP, or
            (FIX_REC) those nearest the new items plus the latest few."""
            cur = threads[code]["current"]
            if not FIX_REC or len(cur) <= CAP:
                return cur[-CAP:]
            new = [i for i in touched[code] if i in vrow]
            recent = cur[-5:]
            rest = [i for i in cur if i not in recent and i in vrow]
            if not new or not rest:
                return cur[-CAP:]
            sims = (V[[vrow[i] for i in rest]] @ V[[vrow[i] for i in new]].T).max(axis=1)
            top = [rest[k] for k in np.argsort(-sims)[:CAP - len(recent)]]
            keep = set(top) | set(recent)
            return [i for i in cur if i in keep]

        def cross_for(code):
            """(CROSS) Current items of other threads nearest the new items."""
            if not CROSS:
                return []
            new = [i for i in touched[code] if i in vrow]
            pool = [(i, c) for c, t2 in threads.items() if c != code for i in t2["current"] if i in vrow and i not in touched[code]]
            if not new or not pool:
                return []
            sims = (V[[vrow[i] for i, _ in pool]] @ V[[vrow[i] for i in new]].T).max(axis=1)
            out, seen = [], set()
            for k in np.argsort(-sims):
                if sims[k] < CROSS_SIM or len(out) >= CROSS_MAX:
                    break
                i, c = pool[k]
                if i not in seen:
                    seen.add(i)
                    out.append((i, c))
            return out

        def rec(code):
            t = threads[code]
            shown = shown_for(code)
            cross = cross_for(code)
            msg = f"Thread: {t['name']} — {t['scope']}\n\nCurrent items:\n" + "\n".join(line(by[i]) for i in shown) + \
                  (("\n\nRelated items in other threads:\n" + "\n".join(f"{line(by[i])} [thread: {threads[c]['name']}]" for i, c in cross)) if cross else "") + \
                  f"\n\nNew items from the {m} meeting:\n" + "\n".join(line(by[i]) for i in touched[code])
            system = REC_SYS + (REC_FIX if FIX_REC else "")
            shown_by[code], cross_by[code] = shown, cross
            # Reasoning counts against the cap; a reply cut off there is
            # asked again with twice the room, then given up (no effects).
            for cap in (REC_MAX, 2 * REC_MAX):
                try:
                    out = bt.chat(MODEL, [{"role": "system", "content": system}, {"role": "user", "content": msg}], REC_SCHEMA,
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
        shown_by, cross_by = {}, {}
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
        retired_elsewhere = set()
        for code in touched:
            t = threads[code]
            new = touched[code]
            cur = t["current"] + new
            fresh = set(new)
            others = {i for i, _ in cross_by.get(code, [])}
            valid = set(shown_by.get(code, t["current"][-CAP:])) | fresh | others
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
                    # SAME_LATER: keep the newer wording (it may add detail,
                    # and it dates the subject); dropping it was most of
                    # t4's audited wrong retirements (research log 2026-10-17).
                    gone = b if SAME_LATER else a
                    cur = [i for i in cur if i != gone]
                    continue
                rel = rule(rel, by[b], by[a])
                if rel is None:
                    continue
                if FIX_REC and midx[by[b]["meeting"]] > midx[by[a]["meeting"]]:
                    continue  # never retired by an older item
                relations.append({"later": a, "earlier": b, "rel": rel})
                kept[code] += 1
                if rel in RETIRE:
                    cur = [i for i in cur if i != b]
                    retired[code] += 1
                    if b in others:
                        retired_elsewhere.add(b)
            t["current"] = cur
            trace({"step": "kept", "meeting": m, "code": code, "kept": kept.get(code, 0), "retired": retired.get(code, 0)})
        if CONSOLIDATE:
            def cons(code):
                t = threads[code]
                cur = t["current"][-CONSOLIDATE_MAX:]
                msg = f"Thread: {t['name']} — {t['scope']}\nToday: {m[:10]}\n\nCurrent items (oldest first):\n" + "\n".join(line(by[i]) for i in cur) + \
                      "\n\nList the items that are no longer the current word."
                try:
                    return code, cur, bt.chat(MODEL, [{"role": "system", "content": CONS_SYS}, {"role": "user", "content": msg}], CONS_SCHEMA,
                                              f"{RUN}-cons-{m}-{code}", max_tokens=REC_MAX, session=f"{RUN}-{os.environ.get('CACHE_SALT', '')}-cons-{m}")
                except (RuntimeError, json.JSONDecodeError) as e:
                    print(f"  consolidate {code}: {e}", flush=True)
                    return code, cur, {"outdated": []}
            todo_c = [c for c in touched if c in threads and len(threads[c]["current"]) > CONSOLIDATE_MIN]
            with cf.ThreadPoolExecutor(PAR) as ex:
                for code, cur, r in ex.map(cons, todo_c):
                    calls["consolidate"] += 1
                    shownset = set(cur)
                    gone = set()
                    for o in r.get("outdated", []):
                        i, b = o.get("id"), o.get("by")
                        if i not in shownset:
                            continue
                        if b in shownset and b != i:
                            if midx[by[b]["meeting"]] < midx[by[i]["meeting"]] or (by[b]["meeting"] == by[i]["meeting"] and b < i):
                                continue  # only a later item replaces an earlier one
                            rel = {"superseded": "supersedes", "completed": "completes", "answered": "answers"}.get(o.get("why"))
                            if rel and CONS_RULE:
                                # The same rules as reconcile: no in-meeting
                                # walk-through "supersedes", completes only plans.
                                rel = rule(rel, by[i], by[b])
                                if rel not in RETIRE:
                                    continue
                            if rel:
                                relations.append({"later": b, "earlier": i, "rel": rel})
                        elif o.get("why") != "past" or not CONS_PAST:
                            continue  # a replacement needs the item that replaces it
                        gone.add(i)
                    threads[code]["current"] = [i for i in threads[code]["current"] if i not in gone]
                    calls["consolidated"] += len(gone)
                    trace({"step": "consolidate", "meeting": m, "code": code, "shown": len(cur), "outdated": len(gone)})
        if SPLIT:
            split_threads(m, mi, touched)
        if LAPSE_DAYS:
            today = day_of(m)
            for t in threads.values():
                keep = [i for i in t["current"] if not (by[i]["kind"] in LAPSE_KINDS and (today - day_of(by[i]["meeting"])).days > LAPSE_DAYS)]
                calls["lapsed"] += len(t["current"]) - len(keep)
                t["current"] = keep
        if retired_elsewhere:
            for t in threads.values():
                t["current"] = [i for i in t["current"] if i not in retired_elsewhere]
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
