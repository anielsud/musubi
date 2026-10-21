#!/usr/bin/env python3
"""Write a system's linking output from a Musubi instance database, in the
benchmark's shape (docs/linking-benchmark.md):

  {"name", "claim": {stmt: claim}, "topic": {stmt: topic},
   "relations": [{"later": stmt, "earlier": stmt, "rel": ...}],
   "threads": {code: {"name", "scope", "items", "current"}}}

Usage: system_from_db.py <musubi.db> <name> [bench statements.jsonl] > system.json

With the bench's statements, ids are mapped by meeting date and text, so a
database whose statements were re-imported (new ids) scores against the
same reference.
"""
import datetime, json, sqlite3, sys

db = sqlite3.connect(f"file:{sys.argv[1]}?immutable=1", uri=True)
name = sys.argv[2]
remap = None
if len(sys.argv) > 3:
    ref = {}
    for l in open(sys.argv[3]):
        s = json.loads(l)
        ref[(s["meeting"], s["text"])] = s["id"]
    remap = {}
    for sid, text, occ in db.execute("SELECT st.id, st.text, i.occurred_at FROM statements st JOIN items i ON i.id = st.item_id"):
        day = datetime.datetime.fromtimestamp(occ).strftime("%Y-%m-%d")
        if (day, text) in ref:
            remap[sid] = ref[(day, text)]
m = (lambda x: remap.get(x)) if remap is not None else (lambda x: x)
claim, topic, members = {}, {}, {}
for sid, cid, tid in db.execute("""SELECT sc.statement_id, sc.claim_id, coalesce(c.topic_id, 0)
                                   FROM statement_claims sc JOIN claims c ON c.id = sc.claim_id"""):
    sid = m(sid)
    if sid is None:
        continue
    claim[sid], topic[sid] = cid, tid
    members.setdefault(cid, []).append(sid)
rels, seen = [], set()
def add(later, earlier, rel):
    if later is None or earlier is None or later == earlier or (later, earlier, rel) in seen:
        return
    seen.add((later, earlier, rel))
    rels.append({"later": later, "earlier": earlier, "rel": rel})
for frm, to, rel, sid in db.execute("SELECT from_claim, to_claim, rel, statement_id FROM claim_relations"):
    for e in members.get(to, []):
        add(m(sid), e, rel)
# Threading also records relations between statements (two statements of
# one claim have no claim relation); older databases lack the table.
if db.execute("SELECT 1 FROM sqlite_master WHERE name = 'statement_relations'").fetchone():
    for later, earlier, rel in db.execute("SELECT later, earlier, rel FROM statement_relations"):
        add(m(later), m(earlier), rel)
# Threads with their current items, for memory_eval.py.
threads = {}
for tid, label, summary, sid, cur in db.execute("""SELECT t.id, t.label, t.summary, ti.statement_id, ti.current
                                                   FROM thread_items ti JOIN topics t ON t.id = ti.topic_id"""):
    sid = m(sid)
    if sid is None:
        continue
    t = threads.setdefault(f"T{tid}", {"name": label, "scope": summary, "items": [], "current": []})
    t["items"].append(sid)
    if cur:
        t["current"].append(sid)
json.dump({"name": name, "claim": claim, "topic": topic, "relations": rels, "threads": threads}, sys.stdout)
