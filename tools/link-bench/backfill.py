#!/usr/bin/env python3
"""Builds an experiment dataset from extracted items, the way Best GLM was
built (linking-benchmark.md, "Datasets"), step by step; a step already done
is skipped, so a run can be resumed.

1. import    each item's extracted items (musubi import-items); benchmark
             meetings (drops named by date) take the existing benchmark
             extraction, so the benchmark's reference still applies to them
2. knowledge embed statements and link claims (musubi knowledge
             --no-threads: no local threading)
3. export    the statements for the thread prototype (link-bench/export)
4. threads   lean threads on the hosted model (threads2.py, Best GLM's
             settings)
5. apply     load the threads into the dataset (apply_threads.py)
6. links     thread centroids and relations (musubi thread-links)

Usage: backfill.py <dataset dir> [--benchmark-items DIR] [--titled]
       [--recent-days N] [--run NAME]
The dataset dir holds config.toml, data/ (ingested with musubi update) and
items/<drop ref>.json. MUSUBI names the musubi binary.
"""
import argparse, json, os, sqlite3, subprocess, sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))


def step(ds, name, fn):
    mark = os.path.join(ds, f".done-{name}")
    if os.path.exists(mark):
        print(f"== {name}: done earlier", flush=True)
        return
    print(f"=== step {name}", flush=True)
    fn()
    open(mark, "w").close()


def run(cmd, env=None):
    print("  $", " ".join(cmd), flush=True)
    r = subprocess.run(cmd, env={**os.environ, **(env or {})}, cwd=ROOT)
    if r.returncode != 0:
        sys.exit(f"{cmd[0]} failed ({r.returncode})")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("ds")
    ap.add_argument("--benchmark-items", default=os.path.join(ROOT, "testdata/private/insights-eval/insights"))
    ap.add_argument("--titled", action="store_true")
    ap.add_argument("--recent-days", type=int, default=0)
    ap.add_argument("--run", default="")
    a = ap.parse_args()
    ds = os.path.abspath(a.ds)
    cfg = os.path.join(ds, "config.toml")
    db = os.path.join(ds, "data", "musubi.db")
    musubi = os.environ.get("MUSUBI", "musubi")
    runname = a.run or os.path.basename(ds)

    def imports():
        items = sqlite3.connect(f"file:{db}?mode=ro", uri=True).execute("SELECT id, ref FROM items WHERE kind = 'meeting' ORDER BY occurred_at").fetchall()
        missing = []
        for iid, ref in items:
            f = os.path.join(ds, "items", ref + ".json")
            if not os.path.exists(f):
                f = os.path.join(a.benchmark_items, f"items-GLM-5.3-Flash-lean_ex_v2-{ref}.json")
            if not os.path.exists(f):
                missing.append(ref)
                continue
            run([musubi, "--config", cfg, "import-items", "--item", iid, "--file", f, "--tag", "glmf-v2"])
        if missing:
            sys.exit(f"no extracted items for {len(missing)} meetings, e.g. {missing[:3]}")

    step(ds, "import", imports)
    step(ds, "knowledge", lambda: run([musubi, "--config", cfg, "knowledge", "--no-threads"]))
    bench = os.path.join(ds, "bench")
    step(ds, "export", lambda: run(["go", "run", "-tags", "sqlite_fts5", "./tools/link-bench/export", "-db", db, "-out", bench]
                                   + (["-titled"] if a.titled else [])))
    system = os.path.join(ds, "system-threads.json")
    env = {"RUN": runname, "SPECIFIC": "1", "SKIP_NEW": "0", "INLINE": "0", "ASSIGN_EFFORT": "low", "ASSIGN_MAX": "8000", "REC_MAX": "8000",
           "TRACE": os.path.join(ds, "trace.jsonl"), "MODEL": os.environ.get("MODEL", "zai-org/GLM-5.3-Flash")}
    if a.recent_days:
        env["RECENT_DAYS"] = str(a.recent_days)
    step(ds, "threads", lambda: run([sys.executable, "-u", os.path.join(HERE, "threads2.py"), bench, db, system], env))
    step(ds, "apply", lambda: run([sys.executable, os.path.join(HERE, "apply_threads.py"), system, db]))
    step(ds, "links", lambda: run([musubi, "--config", cfg, "thread-links"]))
    print("dataset ready:", ds)


if __name__ == "__main__":
    main()
