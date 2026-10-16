#!/usr/bin/env python3
"""Load a thread-level system (threads.py output) into a copy of an instance
database in place of its topics and claim relations, so cards and search can
be scored end to end on it. Claims are kept; a claim's topic is the thread
of its first statement.

Usage: apply_threads.py <system.json> <copy of musubi.db>
"""
import json, sqlite3, sys

sysj = json.load(open(sys.argv[1]))
db = sqlite3.connect(sys.argv[2])
claim_of = dict(db.execute("SELECT statement_id, claim_id FROM statement_claims"))
level = dict(db.execute("SELECT id, level FROM statements"))
db.execute("DELETE FROM topic_relations")
db.execute("DELETE FROM topic_entities")
db.execute("DELETE FROM topic_events")
db.execute("UPDATE claims SET topic_id = NULL")
db.execute("DELETE FROM topics")
first = {}
for tnum, (tid, t) in enumerate(sysj["threads"].items(), 1):
    items = t["items"]
    db.execute("INSERT INTO topics(id, label, summary, label_level, centroid, size) VALUES (?,?,?,?,NULL,?)",
               (tnum, t["name"], t["scope"], max([2] + [level.get(i, 0) for i in items]), len(items)))
for sid, tnum in sysj["topic"].items():
    sid = int(sid)
    if tnum and sid in claim_of and claim_of[sid] not in first:
        first[claim_of[sid]] = tnum
for cid, tnum in first.items():
    db.execute("UPDATE claims SET topic_id = ? WHERE id = ?", (tnum, cid))
db.execute("DELETE FROM claim_relations")
n = 0
for r in sysj["relations"]:
    a, b = claim_of.get(r["later"]), claim_of.get(r["earlier"])
    if a and b and a != b:
        db.execute("INSERT OR IGNORE INTO claim_relations(from_claim, to_claim, rel, statement_id) VALUES (?,?,?,?)", (a, b, r["rel"], r["later"]))
        n += 1
db.commit()
print(f"{len(sysj['threads'])} threads as topics, {len(first)} claims placed, {n} relations")
