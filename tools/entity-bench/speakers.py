#!/usr/bin/env python3
"""Flags meetings whose speaker labels look wrong, for review: the
attribution problems the audits found (a whole 1:1 credited to one
person). No model; evidence is the invite (attendees) and the transcript
(speaker labels, line counts), which any customer has.

Flags:
- one voice: a meeting with two or more people (the owner counts: they
  are in their own recordings) where one label has nearly every passage
  (≥ 90%, with 10+ passages);
- unknown label: a label that matches no attendee's name (first, last or
  full) and isn't the owner; a misheard name, or a guest not invited;
- silent attendee: in a 1:1 (two attendees), one of them never speaks.

Usage: speakers.py <musubi.db> <owner name> <out.md> [--json out.json]
"""
import collections, json, re, sqlite3, sys


def key(n):
    n = n.strip()
    if "," in n:
        last, first = [x.strip() for x in n.split(",", 1)]
        n = f"{first} {last}"
    return " ".join(re.findall(r"[a-z]+", n.lower()))


def main():
    db = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
    owner = key(sys.argv[2])
    out_md = sys.argv[3]
    out_json = sys.argv[sys.argv.index("--json") + 1] if "--json" in sys.argv else None
    flags = []
    for iid, title, at, parts in db.execute("SELECT id, title, date(occurred_at, 'unixepoch'), participants FROM items WHERE kind = 'meeting' ORDER BY occurred_at"):
        att = [key(p.get("name") or "") for p in (json.loads(parts or "[]") or []) if p.get("name")]
        att = [a for a in att if a]
        lines = collections.Counter()
        for (sp,) in db.execute("SELECT speakers FROM chunks WHERE item_id = ?", (iid,)):
            for s in re.split(r"[,;/]", sp or ""):
                if key(s):
                    lines[key(s)] += 1
        total = sum(lines.values())
        if not total:
            continue
        def known(label):
            if label == owner or owner.split()[0] == label:
                return True
            for a in att:
                if label == a or label in a.split() or a in label:
                    return True
            return False
        f = []
        top, n = lines.most_common(1)[0]
        # The owner is in their own recordings, invited or not.
        if len(set(att) | {owner}) >= 2 and total >= 10 and n / total >= 0.9:
            f.append(f"one voice: {top} has {n} of {total} passages")
        for label, c in lines.items():
            if not known(label) and not re.match(r"(speaker|person|unknown|guest)\b", label):
                f.append(f"unknown label: {label} ({c} lines) is no attendee")
        others = [a for a in set(att) if a != owner]
        if len(set(att) | {owner}) == 2 and others:
            o = others[0]
            if not any(o == l or l in o.split() for l in lines):
                f.append(f"silent attendee: {o} never speaks")
        if f:
            flags.append({"item": iid, "title": title, "date": at, "attendees": len(set(att)), "lines": total, "flags": f})
    lines = ["# Speaker attribution: meetings to check (for review; nothing changed)", "",
             f"{len(flags)} meetings flagged.", "", "| Date | Meeting | Attendees | Lines | Flags |", "|---|---|---|---|---|"]
    for x in flags:
        lines.append(f"| {x['date']} | {x['title'][:50]} | {x['attendees']} | {x['lines']} | {'; '.join(x['flags'])} |")
    open(out_md, "w").write("\n".join(lines) + "\n")
    if out_json:
        json.dump(flags, open(out_json, "w"), indent=1)
    c = collections.Counter(fl.split(":")[0] for x in flags for fl in x["flags"])
    print(f"{len(flags)} meetings flagged: {dict(c)}")


if __name__ == "__main__":
    main()
