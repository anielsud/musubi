#!/usr/bin/env python3
"""Runs an Ask question set against a dataset and saves the answers, for
grading against the set's references (grade.md).

Questions are private (testdata/private/ask-eval/questions.json): a list of
{"id", "type", "question", "reference", "must", "must_not", "sources"}.

Usage: run.py <config.toml> <questions.json> <out.json> [--passages-only] [--par N]
MUSUBI names the musubi binary.
"""
import argparse, concurrent.futures as cf, json, os, subprocess, sys, time


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("config")
    ap.add_argument("questions")
    ap.add_argument("out")
    ap.add_argument("--passages-only", action="store_true")
    ap.add_argument("--par", type=int, default=4)
    a = ap.parse_args()
    musubi = os.environ.get("MUSUBI", "musubi")
    qs = json.load(open(a.questions))
    done = {}
    if os.path.exists(a.out):
        done = {r["id"]: r for r in json.load(open(a.out)) if r.get("text")}

    def one(q):
        if q["id"] in done:
            return done[q["id"]]
        cmd = [musubi, "--config", a.config, "ask", "--json", q["question"]] + (["--passages-only"] if a.passages_only else [])
        t0 = time.time()
        r = subprocess.run(cmd, capture_output=True, text=True)
        try:
            res = json.loads(r.stdout.strip().splitlines()[-1])
        except (ValueError, IndexError):
            res = {"text": "", "error": r.stderr[-2000:]}
        res.update(id=q["id"], type=q["type"], question=q["question"], wall=round(time.time() - t0, 1))
        print(f"{q['id']}: {res.get('wall')}s, {len(res.get('sources') or [])} sources", file=sys.stderr, flush=True)
        return res

    with cf.ThreadPoolExecutor(a.par) as ex:
        out = list(ex.map(one, qs))
    json.dump(out, open(a.out, "w"), indent=1)


if __name__ == "__main__":
    main()
