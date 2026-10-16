# Teams markdown transcripts → Musubi inbox drops and numbered extraction
# lines, from one parse, so a drop's turns and the extractor's line numbers
# agree (an imported item cites lines counted through the item's passages,
# one line per turn).
#
#   python3 transcripts.py drops <inbox dir> <transcript.md ...>
#   python3 transcripts.py extract <items dir> <description or "title"> <transcript.md ...>
#
# The transcript's own summary and action items (above "## Transcript")
# are never read. Speakers: a name label is the speaker; a meeting-room
# device, "Speaker N" or an unknown label is an unknown speaker.
import json, os, re, sys
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from parse import is_filler  # noqa: E402


def person(label):
    l = label.lower()
    return not (label.startswith("Room - ") or label.startswith("Speaker") or "unknown" in l)


def parse_file(path):
    raw = open(path).read()
    head, tr = raw.split("## Transcript", 1)
    title = os.path.basename(path)[13:-3] if re.match(r"\d{4}-\d{2}-\d{2} - ", os.path.basename(path)) else os.path.basename(path)[:-3]
    m = re.search(r"\*\*Start:\*\* (\S+)", head)
    start = m.group(1) if m else os.path.basename(path)[:10] + "T00:00:00Z"
    start = re.sub(r"(\.\d+)?Z?$", "", start)
    start_dt = datetime.fromisoformat(start).replace(tzinfo=timezone.utc)
    turns = []
    for ts, label, text in re.findall(r"\*\*\[([\d:]+)\] ([^*]+):\*\* (.*)", tr):
        text = " ".join(text.split())
        if turns and is_filler(text):
            turns[-1]["folded"].append((label, text))
            continue
        h = [int(x) for x in ts.split(":")]
        secs = h[0] * 3600 + h[1] * 60 + h[2] if len(h) == 3 else h[0] * 60 + h[1]
        turns.append({"ts": secs, "label": label.strip(), "text": text, "folded": []})
    for i, t in enumerate(turns):
        t["n"] = i + 1
        t["full"] = t["text"] + "".join(f" ({fl}: {ft})" for fl, ft in t["folded"])
    key = os.path.basename(path)[:-3]
    return {"key": key, "title": title, "date": start_dt.strftime("%Y-%m-%d"), "start": start_dt, "turns": turns}


def lines(m):
    return [f"{t['n']} {t['label'] if person(t['label']) else 'unknown speaker'}: {t['full']}" for t in m["turns"]]


def directory_emails():
    """Company addresses for people in the directory, by the First.Last
    pattern the calendar uses. Test datasets only: outsiders get none, so a
    meeting with them classifies as confidential, as it should."""
    path = os.path.join(os.environ.get("INSIGHTS_DATA", os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "testdata", "private", "insights-eval", "insights")), "directory.json")
    try:
        people = json.load(open(path))["people"]
    except (OSError, KeyError):
        return {}
    return {p[0]: ".".join(p[0].split()) + "@optimizely.com" for p in people if len(p[0].split()) >= 2}


def drop(m, emails=None):
    emails = emails or {}
    names = []
    for t in m["turns"]:
        if person(t["label"]) and t["label"] not in names:
            names.append(t["label"])
    return {"kind": "meeting", "source": "teams", "ref": m["key"], "record": {
        "title": m["title"], "start": m["start"].strftime("%Y-%m-%dT%H:%M:%SZ"),
        "attendees": [{"name": n, **({"email": emails[n]} if n in emails else {})} for n in names],
        "turns": [{"speaker": t["label"] if person(t["label"]) else "", "email": emails.get(t["label"], "") if person(t["label"]) else "",
                   "offset": t["ts"], "text": t["full"]} for t in m["turns"]]}}


def main():
    cmd = sys.argv[1]
    if cmd == "drops":
        out = sys.argv[2]
        os.makedirs(out, exist_ok=True)
        emails = directory_emails()
        for p in sys.argv[3:]:
            m = parse_file(p)
            json.dump(drop(m, emails), open(os.path.join(out, m["key"] + ".json"), "w"))
        print(f"{len(sys.argv) - 3} drops written to {out}")
    elif cmd == "extract":
        out, desc = sys.argv[2], sys.argv[3]
        os.makedirs(out, exist_ok=True)
        import extract_lean
        assert "_v2" in extract_lean.V, "set VARIANT=lean_ex_v2"

        def one(p):
            m = parse_file(p)
            dest = os.path.join(out, m["key"] + ".json")
            if os.path.exists(dest):
                return
            extract_lean.run_lines(m["key"], m["date"], lines(m), m["title"] if desc == "title" else desc, dest)

        with ThreadPoolExecutor(int(os.environ.get("MEETINGS_PAR", "6"))) as ex:
            list(ex.map(one, sys.argv[4:]))
    else:
        sys.exit("usage: transcripts.py drops|extract ...")


if __name__ == "__main__":
    main()
