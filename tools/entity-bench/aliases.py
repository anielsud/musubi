#!/usr/bin/env python3
"""Proposes alias candidates from an instance database, for review: names
and terms that are probably one thing spelled or heard two ways (a city name
misheard, a surname misspelled). No model, nothing applied.

Evidence comes only from what any customer has: calendar attendees, the
transcript's speaker labels, who the extracted statements belong to, and
capitalised terms in the statements. Not the directory.

A pair is proposed when the spellings are close (edit distance, or the same
sound) and scored by:
- support: how often each spelling occurs (the rarer one is likelier the
  error), and whether one is an attendee's name on an invite (the likely
  right spelling);
- against: both names attending one meeting as different people, which
  means they are two people, whatever the spelling.

Usage: aliases.py <musubi.db> <out.md> [--json out.json]
"""
import collections, json, re, sqlite3, sys


def norm(s):
    return re.sub(r"[^a-z]", "", s.lower())


def lev(a, b):
    if abs(len(a) - len(b)) > 2:
        return 9
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


def sound(w):
    """A rough sound key (Soundex-like, keeps the first letter's sound)."""
    w = norm(w)
    if not w:
        return ""
    w = re.sub(r"ph", "f", w)
    w = re.sub(r"(?<=.)h", "", w)  # Dhaka → Daka, Bhinder → Binder
    codes = {**dict.fromkeys("bfpv", "1"), **dict.fromkeys("cgjkqsxz", "2"), **dict.fromkeys("dt", "3"), "l": "4",
             **dict.fromkeys("mn", "5"), "r": "6"}
    first = codes.get(w[0], w[0])
    out = [first]
    for c in w[1:]:
        d = codes.get(c, "")
        if d and d != out[-1]:
            out.append(d)
        elif not d:
            out.append("")
    return "".join(x for x in out if x)[:5]


def main():
    db = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
    out_md = sys.argv[2]
    out_json = sys.argv[sys.argv.index("--json") + 1] if "--json" in sys.argv else None
    count = collections.Counter()      # surface → occurrences
    where = collections.defaultdict(set)  # surface → items
    kind = collections.defaultdict(set)   # surface → attendee / speaker / holder / term
    attendees = collections.defaultdict(set)  # item → attendee names (normalised full names)
    tokens_in = collections.defaultdict(set)  # item → attendee name parts
    full_of = collections.defaultdict(set)    # name part → attendee full names it is part of
    for iid, parts in db.execute("SELECT id, participants FROM items"):
        for p in json.loads(parts or "[]") or []:
            n = (p.get("name") or "").strip()
            if "," in n:
                last, first = [x.strip() for x in n.split(",", 1)]
                n = f"{first} {last}"
            if not n:
                continue
            for w in [n] + n.split():
                count[w] += 1
                where[w].add(iid)
                kind[w].add("attendee" if w == n else "attendee-part")
            attendees[iid].add(norm(n))
            toks = [norm(x) for x in n.split() if norm(x)]
            for t in toks:
                full_of[t].add(" ".join(toks))
                tokens_in[iid].add(t)
    for iid, sp in db.execute("SELECT item_id, speakers FROM chunks WHERE speakers != ''"):
        for n in re.split(r"[,;/]", sp):
            n = n.strip()
            if n and not re.match(r"(?i)speaker|person|unknown", n):
                for w in [n] + n.split():
                    count[w] += 1
                    where[w].add(iid)
                    kind[w].add("speaker")
    lower = collections.Counter()  # word → times written in lower case
    ctx = collections.defaultdict(list)  # surface → statement ids using it
    for sid, iid, who, holder, text in db.execute("SELECT id, item_id, who, holder, text FROM statements"):
        for w in re.findall(r"\b[a-z]{4,}\b", text):
            lower[w] += 1
        for w in set(re.findall(r"\b[A-Za-z]{4,}\b", text)):
            ctx[w].append(sid)
        for n in ((json.loads(who) if who else []) or []) + ([holder] if holder else []):
            for w in [n] + n.split():
                count[w] += 1
                where[w].add(iid)
                kind[w].add("holder")
        for w in re.findall(r"\b[A-Z][A-Za-z]{3,}\b|\b[A-Z]{4,}\b", text):
            count[w] += 1
            where[w].add(iid)
            kind[w].add("term")
    # Single words only, 4+ letters, seen twice or more, and not ordinary
    # words (written in lower case at least a fifth of the time).
    def ordinary(w):
        lw = w.lower()
        return lower[lw] * 5 >= lower[lw] + count[w]
    words = [w for w in count if " " not in w and len(norm(w)) >= 4 and count[w] >= 2 and not ordinary(w)]
    # Context: the mean meaning of the statements using a spelling.
    import struct, math
    vec = {}
    for sid, b in db.execute("SELECT statement_id, vec FROM statement_vecs"):
        v = struct.unpack(f"{len(b) // 4}f", b)
        n = math.sqrt(sum(x * x for x in v)) or 1
        vec[sid] = [x / n for x in v]
    def centre(w):
        vs = [vec[i] for i in ctx.get(w, [])[:200] if i in vec]
        if not vs:
            return None
        c = [sum(x) / len(vs) for x in zip(*vs)]
        n = math.sqrt(sum(x * x for x in c)) or 1
        return [x / n for x in c]
    centres = {}
    def ctxsim(a, b):
        for w in (a, b):
            if w not in centres:
                centres[w] = centre(w)
        ca, cb = centres[a], centres[b]
        if ca is None or cb is None:
            return None
        return sum(x * y for x, y in zip(ca, cb))
    by_sound = collections.defaultdict(list)
    for w in words:
        by_sound[sound(w)].append(w)
    seen, pairs = set(), []
    for group in by_sound.values():
        for i, a in enumerate(group):
            for b in group[i + 1:]:
                na, nb = norm(a), norm(b)
                if na == nb or (na, nb) in seen:
                    continue
                d = lev(na, nb)
                if d > 2 or (d == 2 and min(len(na), len(nb)) < 6):
                    continue
                seen.add((na, nb))
                # Two people: both attended one meeting under these names.
                together = sum(1 for it in where[a] & where[b] if na in tokens_in[it] and nb in tokens_in[it])
                # Each spelling is part of a different person's full name on
                # invites (Christian X and Kristian Y): two people.
                fa, fb = full_of.get(na, set()), full_of.get(nb, set())
                distinct = bool(fa) and bool(fb) and not (fa & fb) and not any(
                    x.split()[-1] == y.split()[-1] for x in fa for y in fb if len(x.split()) > 1 and len(y.split()) > 1)
                common, rare = (a, b) if count[a] >= count[b] else (b, a)
                cs = ctxsim(a, b)
                score = (count[rare] ** 0.5) * (1.5 if "attendee" in kind[common] or "attendee-part" in kind[common] else 1) / (1 + d)
                # Used in different contexts: two different things.
                if cs is not None:
                    score *= max(0.0, (cs - 0.6) / 0.3)
                if together or distinct:
                    score = 0
                pairs.append({"keep": common, "variant": rare, "distance": d, "keep_count": count[common], "variant_count": count[rare],
                              "keep_seen_as": sorted(kind[common]), "variant_seen_as": sorted(kind[rare]),
                              "meetings_both": len(where[a] & where[b]), "attend_together": together,
                              "two_people": distinct, "context": None if cs is None else round(cs, 2), "score": round(score, 2)})
    pairs.sort(key=lambda p: -p["score"])
    lines = ["# Alias candidates (for review; nothing applied)", "",
             "Each row: probably the same thing spelled or heard two ways. Keep = the likelier right form (commoner, or on an invite).",
             "Blocked = two people: both names attended one meeting, or each is part of a different full name on invites.", "",
             "Context = how alike the statements using each spelling are (0.9+: same contexts).", "",
             "| Score | Keep | Variant | Seen (keep / variant) | Context | Meetings with both | Blocked |", "|---|---|---|---|---|---|---|"]
    for p in pairs[:200]:
        lines.append(f"| {p['score']} | {p['keep']} | {p['variant']} | {p['keep_count']} / {p['variant_count']} | "
                     f"{p['context']} | {p['meetings_both']} | {'together' if p['attend_together'] else ('two people on invites' if p['two_people'] else '')} |")
    open(out_md, "w").write("\n".join(lines) + "\n")
    if out_json:
        json.dump(pairs, open(out_json, "w"), indent=1)
    print(f"{len(pairs)} candidate pairs ({sum(1 for p in pairs if p['attend_together'] or p['two_people'])} blocked as two people); top 200 in {out_md}")


if __name__ == "__main__":
    main()
