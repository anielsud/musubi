#!/usr/bin/env python3
"""Scores a larger dataset's threads on the benchmark meetings inside it:
the benchmark's statements are found in the dataset by meeting date and
text (the dataset imported the same extraction), the system's threads,
current lists and relations are mapped onto the benchmark's ids, and the
result is written as a system file for guard.py and score.py.

Items from other meetings are left out of the mapped threads; relations by
which they retire a benchmark item are counted separately (the reference
can't judge them).

Usage: carryover.py <benchmark dir> <dataset bench dir> <system.json> <out.json>
"""
import collections, json, os, sys


def norm(t):
    return " ".join(t.lower().split())


def main():
    bench, dsb, sysp, out = sys.argv[1:5]
    ref = [json.loads(l) for l in open(os.path.join(bench, "statements.jsonl"))]
    mine = [json.loads(l) for l in open(os.path.join(dsb, "statements.jsonl"))]
    key = collections.defaultdict(list)
    for s in ref:
        key[(s["meeting"][:10], norm(s["text"]))].append(s["id"])
    to_ref = {}
    used = set()
    for s in mine:
        for rid in key.get((s["meeting"][:10], norm(s["text"])), []):
            if rid not in used:
                used.add(rid)
                to_ref[s["id"]] = rid
                break
    print(f"mapped {len(to_ref)} of {len(ref)} benchmark statements ({len(mine)} in the dataset)", file=sys.stderr)
    sj = json.load(open(sysp))
    m = lambda i: to_ref.get(int(i))
    threads = {}
    for code, t in sj.get("threads", {}).items():
        items = [m(i) for i in t["items"] if m(i)]
        if items:
            threads[code] = {"name": t["name"], "scope": t.get("scope", ""), "items": items, "current": [m(i) for i in t["current"] if m(i)],
                             "outside": len(t["items"]) - len(items)}
    rels, outside = [], collections.Counter()
    for r in sj.get("relations", []):
        a, b = m(r["later"]), m(r["earlier"])
        if a and b:
            rels.append({"later": a, "earlier": b, "rel": r["rel"]})
        elif b and not a:
            outside[r["rel"]] += 1
    tnum = {c: i + 1 for i, c in enumerate(threads)}
    topic = {s["id"]: 0 for s in ref}
    for c, t in threads.items():
        for i in t["items"]:
            if topic[i] == 0:
                topic[i] = tnum[c]
    json.dump({"name": sj.get("name", "dataset") + "-carryover", "claim": {s["id"]: s["id"] for s in ref}, "topic": topic,
               "relations": rels, "threads": threads}, open(out, "w"))
    big = sorted(threads.values(), key=lambda t: -t["outside"])[:5]
    print(f"benchmark items retired by other meetings: {dict(outside)}", file=sys.stderr)
    print("threads holding benchmark items with the most items from other meetings: " +
          "; ".join(f"{t['name'][:40]} ({len(t['items'])} + {t['outside']})" for t in big), file=sys.stderr)


if __name__ == "__main__":
    main()
