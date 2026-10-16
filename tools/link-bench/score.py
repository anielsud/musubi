#!/usr/bin/env python3
"""Score a system's linking output against the benchmark reference
(docs/linking-benchmark.md).

Usage: score.py <bench dir> <system.json> [more system.json ...]
"""
import collections, glob, json, os, sys

RETIRE = {"supersedes", "answers", "completes"}  # the system stops showing the earlier item


def load(bench):
    stm = {}
    for l in open(os.path.join(bench, "statements.jsonl")):
        s = json.loads(l)
        stm[s["id"]] = s
    pairs = {json.loads(l)["pair"]: json.loads(l) for l in open(os.path.join(bench, "pairs.jsonl"))}
    gold = {}
    for f in glob.glob(os.path.join(bench, "labels-*.jsonl")):
        for l in open(f):
            if l.strip():
                g = json.loads(l)
                gold[g["pair"]] = g
    threads = []
    for f in sorted(glob.glob(os.path.join(bench, "threads-*.json"))):
        threads += json.load(open(f))["threads"]
    return stm, pairs, gold, threads


def sys_index(sysj):
    claim = {int(k): v for k, v in sysj["claim"].items()}
    topic = {int(k): v for k, v in sysj["topic"].items()}
    rel = {}
    for r in sysj["relations"]:
        rel[(r["later"], r["earlier"])] = r["rel"]
    return claim, topic, rel


def sys_relation(a, b, claim, rel):
    """How the system relates later b to earlier a."""
    if claim.get(a) is not None and claim.get(a) == claim.get(b):
        return "same"
    return rel.get((b, a)) or rel.get((a, b)) or "unrelated"


def pct(n, d):
    return f"{100*n/d:5.1f}% ({n}/{d})" if d else "   n/a"


def score(bench, sysj, out):
    stm, pairs, gold, threads = load(bench)
    claim, topic, rel = sys_index(sysj)
    w = out.append
    w(f"## {sysj['name']}")
    # Relations on pairs.
    conf = collections.Counter()
    false_retire = missed_retire = retire_gold = keep_gold = 0
    connected = connected_gold = 0
    by_stratum = collections.defaultdict(lambda: [0, 0])
    examples = collections.defaultdict(list)
    for pid, g in gold.items():
        p = pairs[pid]
        a, b = p["earlier"], p["later"]
        s = sys_relation(a, b, claim, rel)
        conf[(g["relation"], s)] += 1
        gr, sr = g.get("retires_earlier", False), s in RETIRE
        if gr:
            retire_gold += 1
            missed_retire += not sr
        else:
            keep_gold += 1
            if sr:
                false_retire += 1
                examples["false retire"].append(pid)
        if g["relation"] != "unrelated":
            connected_gold += 1
            ok = s != "unrelated" or (topic.get(a) and topic.get(a) == topic.get(b))
            connected += bool(ok)
            if not ok:
                examples["not connected"].append(pid)
        agree = (g["relation"] == s) or (g["relation"] == "same_subject" and s in ("refines",)) \
            or (g["relation"] == "unrelated" and s == "unrelated")
        by_stratum[p["stratum"]][0] += agree
        by_stratum[p["stratum"]][1] += 1
    n = len(gold)
    w(f"pairs labelled: {n}")
    w(f"- false retire (hides a live item): {pct(false_retire, keep_gold)}")
    w(f"- missed retire (stale item shown):  {pct(missed_retire, retire_gold)}")
    w(f"- related pairs connected at all:   {pct(connected, connected_gold)}")
    w("- exact agreement by stratum:")
    for k in sorted(by_stratum):
        a, t = by_stratum[k]
        w(f"    {k:34s} {pct(a, t)}")
    rels = sorted({g for g, _ in conf} | {s for _, s in conf})
    w("- confusion (rows reference, columns system):")
    w("    " + " " * 13 + "".join(f"{r[:8]:>9s}" for r in rels))
    for g in rels:
        if any(conf[(g, s)] for s in rels):
            w(f"    {g[:12]:13s}" + "".join(f"{conf[(g, s)] or '':>9}" for s in rels))
    # Updates in threads.
    upd = upd_found = upd_retire_ok = upd_retire_n = 0
    for t in threads:
        for u in t.get("updates", []):
            a, b = u["earlier"], u["later"]
            if a not in stm or b not in stm:
                continue
            upd += 1
            s = sys_relation(a, b, claim, rel)
            if s != "unrelated":
                upd_found += 1
            if u["relation"] in ("supersedes", "completes", "answers", "corrects"):
                upd_retire_n += 1
                upd_retire_ok += s in RETIRE
    w(f"thread updates found (any relation): {pct(upd_found, upd)}")
    w(f"thread updates that retire, retired: {pct(upd_retire_ok, upd_retire_n)}")
    # Grouping: B-cubed over reference thread members, topics as clusters.
    member = {}
    for i, t in enumerate(threads):
        for m in t.get("members", []):
            member.setdefault(m, set()).add(i)
    items = [m for m in member if m in topic]
    by_topic = collections.defaultdict(list)
    for m in items:
        by_topic[topic[m]].append(m)
    by_thread = collections.defaultdict(list)
    for m in items:
        for i in member[m]:
            by_thread[i].append(m)
    prec = rec = 0.0
    for m in items:
        same_topic = by_topic[topic[m]]
        prec += sum(1 for x in same_topic if member[x] & member[m]) / len(same_topic)
        same_thread = set().union(*(by_thread[i] for i in member[m]))
        rec += sum(1 for x in same_thread if topic[x] == topic[m]) / len(same_thread)
    if items:
        w(f"grouping (B-cubed over {len(items)} thread members): precision {prec/len(items):.2f}, recall {rec/len(items):.2f}")
    w("- per thread: members · topics holding 80% · largest topic share · near misses sharing a member's topic")
    for t in threads:
        ms = [m for m in t.get("members", []) if m in topic]
        if not ms:
            continue
        c = collections.Counter(topic[m] for m in ms)
        need, acc = 0, 0
        for _, k in c.most_common():
            need += 1
            acc += k
            if acc >= 0.8 * len(ms):
                break
        mt = set(c)
        nm = [x["id"] if isinstance(x, dict) else x for x in t.get("near_misses", [])]
        contam = sum(1 for x in nm if topic.get(x) in mt)
        w(f"    {t['thread'][:44]:44s} {len(ms):4d} · {need:3d} · {c.most_common(1)[0][1]/len(ms):4.0%} · {contam}/{len(nm)}")
    # State after the last meeting.
    retired = set()
    for (later, earlier), r in rel.items():
        if r in RETIRE:
            retired.add(earlier)
    cur_n = cur_bad = 0
    for t in threads:
        st = [s for s in t.get("states", []) if s.get("meeting") == "2026-10-07"] or t.get("states", [])[-1:]
        for s in st:
            for i in s.get("current", []):
                if i in stm:
                    cur_n += 1
                    cur_bad += i in retired
    w(f"reference-current items the system retired: {pct(cur_bad, cur_n)}")
    for k, v in examples.items():
        w(f"examples, {k}: {', '.join(v[:12])}")
    w("")


if __name__ == "__main__":
    out = []
    for p in sys.argv[2:]:
        score(sys.argv[1], json.load(open(p)), out)
    print("\n".join(out))
