#!/bin/sh
# Runs a build of musubi on a copy of a dataset and compares what it holds
# before and after: the check docs/data-safety.md §4 asks of every change
# to what is stored, before it merges or is released.
#
# The copy is made with APFS clones (no extra space until files change),
# Microsoft 365 is switched off in the copy's config, and HOME points at a
# scratch folder, so nothing real is read from the network or written
# outside the copy. The real dataset is only read. No model is called:
# `update` ingests and processes locally, and `knowledge --plan` only
# plans.
#
# Usage: scripts/check-on-copy.sh <dataset config.toml> [musubi binary]
#        (default binary: bin/musubi, built with make build)
set -eu

cfg=${1:?usage: scripts/check-on-copy.sh <dataset config.toml> [musubi binary]}
bin=${2:-bin/musubi}
[ -x "$bin" ] || { echo "no $bin: run make build" >&2; exit 1; }
data=$(sed -n 's/^data_dir *= *"\(.*\)".*/\1/p' "$cfg" | head -1)
[ -n "$data" ] && [ -f "$data/musubi.db" ] || { echo "no data_dir with musubi.db in $cfg" >&2; exit 1; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/musubi-check.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/home"
cp -c -R "$data" "$tmp/data"
rm -rf "$tmp/data/backups" "$tmp/data/musubi.db-wal" "$tmp/data/musubi.db-shm"
# A consistent copy of the database even while the app writes it.
sqlite3 "file:$data/musubi.db?mode=ro" ".backup '$tmp/data/musubi.db'"
sed "s#^data_dir *=.*#data_dir = \"$tmp/data\"#" "$cfg" > "$tmp/config.toml"
printf '\n[m365]\nenabled = false\n' >> "$tmp/config.toml"

counts() {
	for t in items statements thread_items claims topics forgotten judgments corrections learned_aliases thread_edits owner_links; do
		n=$(sqlite3 "$tmp/data/musubi.db" "SELECT count(*) FROM $t" 2>/dev/null || echo -)
		printf '%s %s\n' "$t" "$n"
	done
}
plan() { HOME="$tmp/home" "$bin" --config "$tmp/config.toml" knowledge --plan 2>&1 | grep -v '^upgraded\|^20[0-9][0-9]/' || true; }

counts > "$tmp/before.txt"
echo "== plan before (this build, first open: upgrades the copy)"
plan | tee "$tmp/plan-before.txt"
echo "== update"
HOME="$tmp/home" "$bin" --config "$tmp/config.toml" update 2>&1 | tail -4
echo "== plan after"
plan | tee "$tmp/plan-after.txt"
counts > "$tmp/after.txt"

echo "== counts (before → after)"
paste "$tmp/before.txt" "$tmp/after.txt" | awk '{ mark = ($2 == $4) ? "" : "   <<< changed"; printf "%-16s %8s → %8s%s\n", $1, $2, $4, mark }'
status=0
if ! diff -q "$tmp/before.txt" "$tmp/after.txt" >/dev/null; then
	echo "counts changed: check that the change expects it" >&2
	status=1
fi
if ! diff -q "$tmp/plan-before.txt" "$tmp/plan-after.txt" >/dev/null; then
	echo "the plan changed after the update: something now waits for model calls" >&2
	status=1
fi
ls "$tmp/data/backups" 2>/dev/null | sed 's/^/pre-upgrade copy: /'
exit $status
