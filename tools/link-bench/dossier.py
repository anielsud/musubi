#!/usr/bin/env python3
"""Writes audit dossiers for a dataset's threads: for each selected thread,
its items in time order (meeting, speaker, kind, text, whether it is still
current) with the source lines each rests on, and the relations between
them. A reviewer (a person, or a model reading the private data locally)
judges each dossier: one subject or several, missed or wrong updates,
stale current items, wrong people, sensitive items in the wrong place.

Selected: the largest threads, the longest-running, a seeded random sample,
and likely duplicates (pairs of threads alike in name and meaning).

Usage: dossier.py <dataset bench dir> <system.json> <out dir> [per group, default 5]
       [groups.json: [{"name", "codes", "why"}], extra dossiers of chosen threads]
"""
import collections, json, math, os, random, re, sys


def words(s):
    return {w for w in re.findall(r"[a-z0-9]+", s.lower()) if len(w) > 3}


def main():
    bench, sysp, out = sys.argv[1:4]
    per = int(sys.argv[4]) if len(sys.argv) > 4 else 5
    os.makedirs(out, exist_ok=True)
    st = {s["id"]: s for s in (json.loads(l) for l in open(os.path.join(bench, "statements.jsonl")))}
    sj = json.load(open(sysp))
    threads = sj["threads"]
    rels = collections.defaultdict(list)
    for r in sj.get("relations", []):
        rels[r["later"]].append(r)
        rels[r["earlier"]].append(r)
    meetings = sorted({s["meeting"] for s in st.values()})
    idx = {m: i for i, m in enumerate(meetings)}
    span = {c: len({st[i]["meeting"] for i in t["items"] if i in st}) for c, t in threads.items()}
    pick = {}
    for c in sorted(threads, key=lambda c: -len(threads[c]["items"]))[:per]:
        pick.setdefault(c, "largest")
    for c in sorted(threads, key=lambda c: -span[c])[:per]:
        pick.setdefault(c, "longest-running")
    rest = sorted(c for c in threads if c not in pick and len(threads[c]["items"]) >= 5)
    for c in random.Random(7).sample(rest, min(per, len(rest))):
        pick.setdefault(c, "random")
    # Likely duplicates: names sharing most of their words.
    names = {c: words(t["name"] + " " + t.get("scope", "")) for c, t in threads.items()}
    pairs = []
    cs = sorted(threads)
    for i, a in enumerate(cs):
        for b in cs[i + 1:]:
            u = names[a] | names[b]
            if u:
                j = len(names[a] & names[b]) / len(u)
                if j >= 0.3:
                    pairs.append((j, a, b))
    pairs.sort(reverse=True)
    dup = []
    for j, a, b in pairs:
        if len(dup) >= per:
            break
        if a not in pick and b not in pick:
            dup.append((a, b))
            pick[a] = pick[b] = "possible duplicate"
    index = []

    def write(name, codes, why):
        lines = [f"# {name}", "", f"Selected: {why}.", ""]
        for c in codes:
            t = threads[c]
            cur = set(t["current"])
            lines += [f"## Thread {c}: {t['name']}", f"Scope: {t.get('scope', '')}", f"{len(t['items'])} items over {span[c]} meetings; "
                      f"{len(cur)} current.", ""]
            for i in sorted(t["items"], key=lambda i: (idx.get(st[i]["meeting"], 0), st[i]["minute"], i)):
                s = st[i]
                flag = "CURRENT" if i in cur else "retired"
                lines.append(f"- [{i}] {s['meeting']} min {s['minute']} · {s['kind']}/{s.get('status', '')} · {', '.join(s.get('who') or []) or 'no speaker'} · {flag}")
                lines.append(f"  {s['text']}")
                for src in (s.get("lines") or [])[:3]:
                    lines.append(f"  > {src[:300]}")
                for r in rels.get(i, []):
                    if r["later"] == i:
                        lines.append(f"  → {r['rel']} [{r['earlier']}]")
            lines.append("")
        open(os.path.join(out, name + ".md"), "w").write("\n".join(lines))
        index.append({"file": name + ".md", "threads": codes, "why": why})

    for c, why in pick.items():
        if not why.startswith("possible duplicate"):
            write(f"thread-{c}", [c], why)
    for a, b in dup:
        write(f"pair-{a}-{b}", [a, b], "a possible duplicate pair (names alike): one subject or two?")
    if len(sys.argv) > 5:
        for g in json.load(open(sys.argv[5])):
            write(g["name"], [c for c in g["codes"] if c in threads], g["why"])
    json.dump(index, open(os.path.join(out, "index.json"), "w"), indent=1)
    print(f"{len(index)} dossiers in {out}")


if __name__ == "__main__":
    main()
