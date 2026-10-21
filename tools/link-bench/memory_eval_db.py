#!/usr/bin/env python3
"""Scores an instance database's threads on the Opus audit findings of
other datasets (memory_eval.py's measures), matching statements by meeting
day and text: a combined dataset (the ELT year plus September) is scored
on both audits without auditing again.

- missed updates fixed: earlier items an audited later item should have
  retired, now not current;
- stale fixed: items the audit called out of date, now not current;
- wrong retirements fixed: items the audit said were wrongly retired, now
  current (the guard against retiring everything);
- unplaced: statements in no thread;
- and, with --derive, how far "current" derived from the stored relations
  (and the 60-day lapse) agrees with the stored flags
  (docs/incremental-design.md).

Usage: memory_eval_db.py <musubi.db> <dataset dir>... [--derive]
Each dataset dir has audit/findings-batch*.json and data/musubi.db (the
database the audit's ids refer to).
"""
import collections, glob, json, os, sqlite3, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from memory_eval import audit_sets  # noqa: E402


def key_rows(db):
    q = "SELECT st.id, date(st.said_at, 'unixepoch'), st.text FROM statements st"
    return {(d, " ".join(t.lower().split())): i for i, d, t in db.execute(q)}


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    derive = "--derive" in sys.argv
    target = sqlite3.connect(f"file:{args[0]}?mode=ro", uri=True)
    tkey = key_rows(target)
    member, current = set(), set()
    for sid, cur in target.execute("SELECT statement_id, current FROM thread_items"):
        member.add(sid)
        if cur:
            current.add(sid)
    total = target.execute("SELECT count(*) FROM statements").fetchone()[0]
    pct = lambda a, b: f"{100 * a / b:.0f}%" if b else "-"
    row = {"db": args[0], "statements": total, "unplaced": pct(total - len(member), total), "current": pct(len(current), len(member))}
    for ds in args[1:]:
        src = sqlite3.connect(f"file:{os.path.join(ds, 'data', 'musubi.db')}?mode=ro", uri=True)
        skey = {i: k for k, i in key_rows(src).items()}
        to = lambda i: tkey.get(skey.get(i))
        missed, stale, wrong = audit_sets(ds)
        m = [to(i) for i in missed if to(i)]
        s = [to(i) for i in stale if to(i)]
        w = [to(i) for i in wrong if to(i)]
        name = os.path.basename(os.path.normpath(ds))
        row[f"{name} missed fixed"] = f"{sum(1 for i in m if i in member and i not in current)}/{len(m)}"
        row[f"{name} stale fixed"] = f"{sum(1 for i in s if i in member and i not in current)}/{len(s)}"
        row[f"{name} wrong fixed"] = f"{sum(1 for i in w if i in current)}/{len(w)}"
    if derive:
        said = dict(target.execute("SELECT id, said_at FROM statements"))
        kind = dict(target.execute("SELECT id, kind FROM statements"))
        claim_st = collections.defaultdict(list)
        for s, c in target.execute("SELECT statement_id, claim_id FROM statement_claims"):
            claim_st[c].append(s)
        retiring = collections.defaultdict(list)
        for tc, sid in target.execute("SELECT to_claim, statement_id FROM claim_relations WHERE rel IN ('supersedes','completes','answers','repeats')"):
            for t in claim_st[tc]:
                if t != sid:
                    retiring[t].append(sid)
        if target.execute("SELECT 1 FROM sqlite_master WHERE name = 'statement_relations'").fetchone():
            for later, earlier in target.execute("SELECT later, earlier FROM statement_relations WHERE rel IN ('supersedes','completes','answers','repeats')"):
                retiring[earlier].append(later)
        threads = collections.defaultdict(list)
        for tid, sid, cur in target.execute("SELECT topic_id, statement_id, current FROM thread_items"):
            threads[tid].append((sid, cur))
        conf = collections.Counter()
        for its in threads.values():
            last = max(said[s] for s, _ in its)
            for s, cur in its:
                later = any(said.get(b, 0) >= said[s] for b in retiring.get(s, []))
                lapsed = kind[s] in ("commitment", "question") and last - said[s] > 60 * 86400
                conf[(bool(cur), not later and not lapsed)] += 1
        n = sum(conf.values())
        row["derived agrees"] = pct(conf[(True, True)] + conf[(False, False)], n)
        row["stored retired, unexplained"] = conf[(False, True)]
        row["stored current, derived retired"] = conf[(True, False)]
    print(json.dumps(row, indent=1))


if __name__ == "__main__":
    main()
