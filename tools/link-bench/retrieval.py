#!/usr/bin/env python3
"""Score search and cards per reference thread (docs/linking-benchmark.md):
for each query a person would type or say about a thread, the share of the
top passages, claims and card lines that belong to the thread, and how many
of the thread's current items (after the last meeting) the card shows.

Usage: retrieval.py <bench dir> <queries.json> <musubi binary> <config.toml> <musubi.db>

queries.json: {"<substring of a thread name>": ["query", ...], ...}
Ids in the database must be the bench's ids (score the instance the bench
was exported from).
"""
import glob, json, os, re, sqlite3, subprocess, sys

bench, qfile, binary, cfg, dbp = sys.argv[1:6]
threads = []
for f in sorted(glob.glob(os.path.join(bench, "threads-*.json"))):
    threads += json.load(open(f))["threads"]
queries = json.load(open(qfile))
db = sqlite3.connect(f"file:{dbp}?immutable=1", uri=True)
chunk_key = {cid: f"{item}#{seq}" for cid, item, seq in db.execute("SELECT id, item_id, seq FROM chunks")}
cited = {}  # statement → passages it cites
for sid, ev in db.execute("SELECT id, evidence FROM statements"):
    cited[sid] = {chunk_key.get(e["chunkId"]) for e in json.loads(ev or "[]")}
claim_members = {}
for sid, cid in db.execute("SELECT statement_id, claim_id FROM statement_claims"):
    claim_members.setdefault(cid, []).append(sid)


def run(*args):
    return subprocess.run([binary, "--config", cfg, *args], capture_output=True, text=True).stdout


tot = {"passages": [0, 0], "claims": [0, 0], "card": [0, 0], "current": [0, 0]}
for key, qs in queries.items():
    t = next((t for t in threads if key.lower() in t["thread"].lower()), None)
    if t is None:
        print(f"no thread matching {key!r}")
        continue
    members = set(t.get("members", []))
    passages = set().union(*(cited.get(m, set()) for m in members)) if members else set()
    last = [s for s in t.get("states", []) if s.get("meeting") == "2026-10-07"] or t.get("states", [])[-1:]
    current = set(i for s in last for i in s.get("current", []))
    print(f"## {t['thread']} ({len(members)} members, {len(passages)} passages, {len(current)} current)")
    for q in qs:
        hits = re.findall(r"\s(\S+#\d+)\s*$", run("search", "-k", "5", q), re.M)
        hp = sum(h in passages for h in hits)
        cl = [int(x) for x in re.findall(r"^#(\d+) \[", run("claims", "-k", "5", q), re.M)]
        cp = sum(any(s in members for s in claim_members.get(c, [])) for c in cl)
        card = run("card", q)
        lines = [int(x) for x in re.findall(r"claim (\d+) ·", card)]
        on = [s for c in lines for s in claim_members.get(c, [])]
        kp = sum(any(s in members for s in claim_members.get(c, [])) for c in lines)
        kc = len(current & set(on))
        title = card.splitlines()[0][:60] if card.strip() else "(no card)"
        print(f"  {q!r:38s} passages {hp}/{len(hits)}  claims {cp}/{len(cl)}  card {kp}/{len(lines)} lines, {kc}/{len(current)} current  [{title}]")
        for k, (a, b) in (("passages", (hp, len(hits))), ("claims", (cp, len(cl))), ("card", (kp, len(lines))), ("current", (kc, len(current)))):
            tot[k][0] += a
            tot[k][1] += b
print("\nTOTAL " + "  ".join(f"{k} {a}/{b} = {a/b:.0%}" if b else f"{k} n/a" for k, (a, b) in tot.items()))
