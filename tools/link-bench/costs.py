#!/usr/bin/env python3
"""Cost measures for lean threading (docs/scaling.md §8, A1), from a
threads2.py trace (TRACE=...), the spend ledger and the reference threads:

1. Cost per step, split into reasoning and answer, and what share of input
   the provider already served from its cache.
2. Reconcile calls that changed nothing (no effects, or none kept by the
   rules), and what they cost.
3. Candidate recall: for an item whose reference subject already had items
   in some thread, was such a thread among the candidates its assign call
   was shown? And did the model then join it?
4. Prompt caching potential: the share of each call's prompt that repeats
   the start of an earlier call of the same step, as laid out today and
   with the candidate list in a stable order.

Usage: costs.py <bench dir> <trace.jsonl> <ledger run tag>
The tag is the RUN the calls were first made under (the ledger has no rows
for a replay from cache).
"""
import collections, json, os, re, sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "insights-eval"))
import bt  # noqa: E402


def pct(a, b):
    return f"{100 * a / b:.0f}%" if b else "-"


def lcp(a, b):
    n = min(len(a), len(b))
    i = 0
    while i < n and a[i] == b[i]:
        i += 1
    return i


def main():
    bench, tracep, run = sys.argv[1], sys.argv[2], sys.argv[3]
    tr = [json.loads(l) for l in open(tracep)]
    ledger = [json.loads(l) for l in open(bt.LEDGER)]
    rows = [r for r in ledger if r["tag"].startswith(run + "-")]
    # The trace was made under another RUN name; tags match after the prefix.
    rename = lambda tag: run + tag[tag.index("-"):]

    # 1. Cost per step.
    print("## Cost per step (ledger)\n")
    print("| Step | Calls | Input | of it cached | Output | of it reasoning | Cut off at the cap | $ | Share |")
    print("|---|---|---|---|---|---|---|---|---|")
    total = sum(r["usd"] for r in rows)
    for step in ("assign", "rec"):
        rs = [r for r in rows if r["tag"].startswith(f"{run}-{step}")]
        i, c, o, rz = (sum(r[k] for r in rs) for k in ("in", "cached", "out", "reasoning"))
        cut = [r for r in rs if r.get("finish") == "length"]
        u = sum(r["usd"] for r in rs)
        print(f"| {step} | {len(rs)} | {i/1e3:.0f}k | {pct(c, i)} | {o/1e3:.0f}k | {pct(rz, o)} | {len(cut)} (${sum(r['usd'] for r in cut):.3f}) | ${u:.3f} | {pct(u, total)} |")
    print(f"\nTotal ${total:.3f}.")
    # Prices, fitted on calls the provider served without its cache.
    pl = [r for r in rows if not r["cached"]]
    sxx = sum(r["in"] ** 2 for r in pl); syy = sum(r["out"] ** 2 for r in pl); sxy = sum(r["in"] * r["out"] for r in pl)
    sxu = sum(r["in"] * r["usd"] for r in pl); syu = sum(r["out"] * r["usd"] for r in pl)
    det = sxx * syy - sxy ** 2
    pin, pout = (sxu * syy - syu * sxy) / det, (syu * sxx - sxu * sxy) / det
    print(f"Fitted prices: ${pin*1e6:.3f} per million input tokens, ${pout*1e6:.3f} per million output tokens.\n")
    usd_by_tag = collections.defaultdict(float)
    for r in rows:
        usd_by_tag[r["tag"]] += r["usd"]
    in_by_tag = collections.defaultdict(int)
    for r in rows:
        in_by_tag[r["tag"]] = max(in_by_tag[r["tag"]], r["in"])

    # 2. Reconcile calls that changed nothing.
    recs = [t for t in tr if t["step"] == "rec"]
    kept = {(t["meeting"], t["code"]): t for t in tr if t["step"] == "kept"}
    print("## Reconcile calls that changed nothing\n")
    groups = collections.defaultdict(list)
    for t in recs:
        k = kept.get((t["meeting"], t["code"]), {})
        t["kept"], t["retired"] = k.get("kept", 0), k.get("retired", 0)
        t["usd"] = usd_by_tag.get(rename(t["tag"]), 0)
        size = "1-2 new items" if t["new"] <= 2 else "3-9 new items" if t["new"] < 10 else "10+ new items"
        hist = "thread born this meeting" if t["current"] == 0 else "thread with history"
        groups["all"].append(t); groups[size].append(t); groups[hist].append(t)
    print("| Calls | n | No effects returned | None kept by the rules | Nothing retired | $ of no-kept calls |")
    print("|---|---|---|---|---|---|")
    for g in ("all", "thread born this meeting", "thread with history", "1-2 new items", "3-9 new items", "10+ new items"):
        ts = groups[g]
        if not ts:
            continue
        none = [t for t in ts if t["effects"] == 0]
        nok = [t for t in ts if t["kept"] == 0]
        nor = [t for t in ts if t["retired"] == 0]
        u = sum(t["usd"] for t in ts)
        print(f"| {g} | {len(ts)} | {len(none)} ({pct(len(none), len(ts))}) | {len(nok)} ({pct(len(nok), len(ts))}) | {len(nor)} ({pct(len(nor), len(ts))}) | "
              f"${sum(t['usd'] for t in nok):.3f} of ${u:.3f} |")
    print()

    # 3. Candidate recall against the reference subjects.
    subj = collections.defaultdict(set)
    for f in ("threads-T1.json", "threads-T2.json", "threads-T3.json"):
        p = os.path.join(bench, f)
        if os.path.exists(p):
            for th in json.load(open(p))["threads"]:
                for i in th["members"]:
                    subj[i].add(th["thread"])
    members = collections.defaultdict(set)  # thread code → items placed so far
    placed_by = {}
    for t in tr:
        if t["step"] == "placed":
            placed_by[(t["meeting"], t["k"])] = {int(i): set(c) for i, c in t["member"].items()}
    n = offered = top = joined_off = joined_any = 0
    miss = []
    instead = collections.Counter()
    for t in tr:
        if t["step"] != "assign":
            continue
        shown = set(t["shown"])
        after = placed_by.get((t["meeting"], t["k"]), {})
        known = set(members)
        born_now = {c for cs in after.values() for c in cs} - known
        for i in t["items"]:
            if not subj.get(i):
                continue
            have = collections.Counter()
            for code, ms in members.items():
                k = sum(1 for x in ms if subj.get(x, set()) & subj[i])
                if k:
                    have[code] = k
            if not have:
                continue
            n += 1
            off = set(have) & shown
            best = have.most_common(1)[0][0]
            offered += bool(off)
            top += best in shown
            got = after.get(i, set())
            joined_any += bool(got & set(have))
            joined_off += bool(off and got & off)
            if off and not got & off:
                if not got:
                    instead["no thread"] += 1
                elif got & born_now:
                    instead["a thread new in this call"] += 1
                else:
                    instead["another existing thread"] += 1
            if not off:
                miss.append((t["meeting"], i, best, have[best]))
        for i, cs in after.items():
            for c in cs:
                members[c].add(i)
    print("## Candidate recall (reference subjects)\n")
    print(f"Items whose subject was already in a thread: {n}")
    print(f"- a thread holding the subject was offered: {offered} ({pct(offered, n)})")
    print(f"- the thread holding most of it was offered: {top} ({pct(top, n)})")
    print(f"- joined such a thread, when one was offered: {joined_off} of {offered} ({pct(joined_off, offered)})")
    print(f"- when offered but not joined, the item went to: {dict(instead)}")
    print(f"- not offered: {len(miss)}; by meeting {dict(collections.Counter(m for m, *_ in miss))}\n")

    # 4. Prompt caching potential.
    print("## Prompt caching potential (share of prompt characters repeating an earlier call's start)\n")
    reg_re = re.compile(r"^Candidate threads:\n(.*?)\n\nNew items", re.S)

    def stable(user):
        m = reg_re.match(user)
        if not m:
            return user
        lines = m.group(1).split("\n")
        key = lambda l: int(l[1:l.index(":")]) if l.startswith("T") and ":" in l else 10**9
        return "Candidate threads:\n" + "\n".join(sorted(lines, key=key)) + user[m.end(1):]

    print("| Step | Calls | Prompt chars | Repeats today | Candidate list in stable order |")
    print("|---|---|---|---|---|")
    for step in ("assign", "rec"):
        calls = [t for t in tr if t["step"] == step]
        tot = sum(len(t["system"]) + len(t["user"]) for t in calls)
        for layout in ("today", "stable"):
            seen, rep = [], 0
            for t in calls:
                text = t["system"] + "\n" + (stable(t["user"]) if layout == "stable" else t["user"])
                rep += max((lcp(text, s) for s in seen), default=0)
                seen.append(text)
            if layout == "today":
                today = rep
            else:
                print(f"| {step} | {len(calls)} | {tot/1e3:.0f}k | {pct(today, tot)} | {pct(rep, tot)} |")
    sysshare = {s: pct(sum(len(t["system"]) for t in tr if t["step"] == s), sum(len(t["system"]) + len(t["user"]) for t in tr if t["step"] == s)) for s in ("assign", "rec")}
    print(f"\nSystem prompt share of input: {sysshare}.")


if __name__ == "__main__":
    main()
