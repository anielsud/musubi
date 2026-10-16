#!/usr/bin/env python3
"""Write a system's linking output from a Musubi instance database, in the
benchmark's shape (docs/linking-benchmark.md):

  {"name", "claim": {stmt: claim}, "topic": {stmt: topic},
   "relations": [{"later": stmt, "earlier": stmt, "rel": ...}]}

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
rels = []
for frm, to, rel, sid in db.execute("SELECT from_claim, to_claim, rel, statement_id FROM claim_relations"):
    sid = m(sid)
    if sid is None:
        continue
    for e in members.get(to, []):
        if e != sid:
            rels.append({"later": sid, "earlier": e, "rel": rel})
json.dump({"name": name, "claim": claim, "topic": topic, "relations": rels}, sys.stdout)
