#!/usr/bin/env python3
"""Apply the thread relation rules (as knowledge.threadRule in Go) to a
system's relations, to measure them before and after.

Usage: guard.py <bench dir> <system.json> > guarded.json
"""
import json, os, sys

CORRECTION = ["correct", "actually", "revis", "updated", "update:", "instead", "changed", "no longer",
              "moved from", "moved to", "now expected", "now sitting", "not anymore", "rather than"]
S = {json.loads(l)["id"]: json.loads(l) for l in open(os.path.join(sys.argv[1], "statements.jsonl"))}
sysj = json.load(open(sys.argv[2]))


def keep(r):
    a, b = S.get(r["later"]), S.get(r["earlier"])
    if not a or not b:
        return True
    rel, same = r["rel"], a["meeting"] == b["meeting"]
    if rel == "completes":
        return b["kind"] in ("commitment", "decision", "opinion") and not same
    # Not "answers only a question": questions are often extracted as
    # positions or facts, and half the answers that rule dropped were real.
    if rel == "supersedes" and same:
        return any(w in a["text"].lower() for w in CORRECTION)
    return True


sysj["relations"] = [r for r in sysj["relations"] if keep(r)]
sysj["name"] += "+rules"
json.dump(sysj, sys.stdout)
