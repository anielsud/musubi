#!/usr/bin/env python3
"""Scores a dataset's threads (a threads2.py system file) on what the Opus
audits found (research log 2026-10-16, "Backfill"), so a change to
threading can be measured without auditing again:

- missed updates fixed: earlier items an audited later item should have
  retired, now no longer current;
- stale fixed: items the audit called out of date, now no longer current;
- wrong retirements fixed: items the audit said were wrongly retired, now
  current again (a guard: retiring everything would "fix" the first two);
- the share of items still current;
- and, for the benchmark meetings inside the dataset, the reference scores
  (carryover.py, guard.py, score.py): grouping, update recall, live items
  wrongly retired.

Statement ids are the dataset's, which rethreading doesn't change.

Usage: memory_eval.py <dataset dir> <system.json> [label]
"""
import glob, json, os, re, subprocess, sys, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))
BENCH = os.path.join(ROOT, "testdata/private/link-bench")


def audit_sets(ds):
    missed, stale, wrong = set(), set(), set()
    for f in glob.glob(os.path.join(ds, "audit", "findings-batch*.json")):
        for d in json.load(open(f))["dossiers"]:
            for t in d["threads"]:
                for m in t.get("missed_updates") or []:
                    if isinstance(m, dict) and isinstance(m.get("earlier"), int):
                        missed.add(m["earlier"])
                stale |= {x for x in t.get("stale_current") or [] if isinstance(x, int)}
                wrong |= {x for x in t.get("wrong_retirements") or [] if isinstance(x, int)}
    return missed, stale - wrong, wrong


def main():
    ds, sysp = os.path.abspath(sys.argv[1]), sys.argv[2]
    label = sys.argv[3] if len(sys.argv) > 3 else os.path.basename(sysp)
    sj = json.load(open(sysp))
    member, current = set(), set()
    for t in sj["threads"].values():
        member |= set(t["items"])
        current |= set(t["current"])
    missed, stale, wrong = audit_sets(ds)
    pct = lambda a, b: f"{100 * a / b:.0f}%" if b else "-"
    fm = sum(1 for i in missed if i in member and i not in current)
    fs = sum(1 for i in stale if i in member and i not in current)
    fw = sum(1 for i in wrong if i in current)
    row = {"label": label, "threads": len(sj["threads"]), "current": pct(len(current), len(member)),
           "missed fixed": f"{fm}/{len(missed)}", "stale fixed": f"{fs}/{len(stale)}", "wrong fixed": f"{fw}/{len(wrong)}"}
    # Benchmark carry-over.
    with tempfile.TemporaryDirectory() as tmp:
        c, g = os.path.join(tmp, "c.json"), os.path.join(tmp, "g.json")
        r = subprocess.run([sys.executable, os.path.join(HERE, "carryover.py"), BENCH, os.path.join(ds, "bench"), sysp, c], capture_output=True, text=True)
        if r.returncode == 0:
            open(g, "w").write(subprocess.run([sys.executable, os.path.join(HERE, "guard.py"), BENCH, c], capture_output=True, text=True).stdout)
            out = subprocess.run([sys.executable, os.path.join(HERE, "score.py"), BENCH, g], capture_output=True, text=True).stdout
            m = re.search(r"false retire.*?([\d.]+)%", out)
            row["false retire"] = m.group(1) + "%" if m else "-"
            m = re.search(r"that retire, retired:\s+([\d.]+)%", out)
            row["updates retired"] = m.group(1) + "%" if m else "-"
            m = re.search(r"precision ([\d.]+), recall ([\d.]+)", out)
            row["grouping P/R"] = f"{m.group(1)}/{m.group(2)}" if m else "-"
    print(json.dumps(row))


if __name__ == "__main__":
    main()
