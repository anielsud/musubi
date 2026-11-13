#!/bin/sh
# Rebuilds testdata/fixtures/*.db: the synthetic corpus as each listed
# build left its database, for store's upgrade test (fixtures_test.go),
# and <name>.search: that build's top five passages for a few queries.
# Each build is checked out in a temporary git worktree, built, and run on
# its own copy of testdata/synthetic with the hash embedder and the
# recorded model replies (no model is called; a missing reply fails the
# run). Then a few rows the owner would have made (forget, judgments,
# corrections, aliases, thread edits, to-do marks) are added to the kept
# tables that build had, and the database is vacuumed into the fixture.
#
# Usage: scripts/fixtures.sh            (all fixtures)
#        scripts/fixtures.sh v0.1.0     (one)
#
# Add a line to FIXTURES for each release; never rebuild an existing
# fixture from a different commit: it stands for what that build wrote.
set -eu

FIXTURES="
v0.1.0:v0.1.0
pre-format:db0bc02
pre-labels:de7b3f6
"

repo=$(cd "$(dirname "$0")/.." && pwd)
out="$repo/testdata/fixtures"
mkdir -p "$out"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/musubi-fixtures.XXXXXX")
# Go's caches stay where they are; HOME is replaced for the runs only, so
# no build touches the real config or data folder.
GOCACHE=$(go env GOCACHE)
GOMODCACHE=$(go env GOMODCACHE)
export GOCACHE GOMODCACHE
cleanup() {
	for w in "$tmp"/src-*; do
		[ -d "$w" ] && git -C "$repo" worktree remove --force "$w"
	done
	rm -rf "$tmp"
}
trap cleanup EXIT

for f in $FIXTURES; do
	name=${f%%:*}
	rev=${f#*:}
	if [ $# -gt 0 ] && [ "$1" != "$name" ]; then
		continue
	fi
	src="$tmp/src-$name"
	data="$tmp/data-$name"
	git -C "$repo" worktree add --detach "$src" "$rev" >/dev/null
	(cd "$src" && go build -tags sqlite_fts5 -o "$tmp/musubi-$name" ./cmd/musubi)
	mkdir -p "$tmp/home-$name"
	# The run's score doesn't matter here, only the database it leaves.
	HOME="$tmp/home-$name" "$tmp/musubi-$name" --config "$src/testdata/synthetic/config.toml" \
		eval --corpus "$src/testdata/synthetic" --embed hash --knowledge --keep "$data" >"$tmp/$name.log" 2>&1 || true
	if ! grep -q '^knowledge:' "$tmp/$name.log"; then
		cat "$tmp/$name.log" >&2
		echo "fixtures: $name ($rev) did not build its knowledge" >&2
		exit 1
	fi
	db="$data/musubi.db"
	has() { [ "$(sqlite3 "$db" "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '$1'")" = 1 ]; }
	# Invented rows in the shape each build writes them; they refer to the
	# corpus's own items and statements.
	sqlite3 "$db" "INSERT INTO forgotten(source, ref, at) VALUES ('mail', 'synthetic-forgotten-1', 1760000000);
		INSERT INTO audit(at, scope, op, detail) VALUES (1760000000, 'owner', 'forget', 'mail/synthetic-forgotten-1');
		INSERT INTO judgments(item_id, text, verdict, note, at) SELECT item_id, text, 'verified', 'fixture', 1760000001 FROM statements ORDER BY id LIMIT 2;"
	if has corrections; then
		sqlite3 "$db" "INSERT INTO corrections(item_id, statement, extracted, field, old, new, by, at)
			SELECT item_id, id, text, 'text', text, text || ' (corrected)', 'owner', 1760000002 FROM statements ORDER BY id DESC LIMIT 1;"
	fi
	if has learned_aliases; then
		sqlite3 "$db" "INSERT OR IGNORE INTO learned_aliases(keep, variant, kind, status, by, at) VALUES
			('Northwind', 'Northwnd', 'spelling', 'confirmed', 'owner', 1760000003),
			('Pike', 'Pyke', 'spelling', 'rejected', 'owner', 1760000004);"
	fi
	if has thread_edits; then
		cols=$(sqlite3 "$db" "SELECT group_concat(name) FROM pragma_table_info('thread_edits') WHERE \"notnull\" = 1 AND dflt_value IS NULL AND pk = 0")
		vals=$(sqlite3 "$db" "SELECT group_concat(CASE WHEN type = 'INTEGER' THEN '1760000005' ELSE '''[]''' END) FROM pragma_table_info('thread_edits') WHERE \"notnull\" = 1 AND dflt_value IS NULL AND pk = 0")
		sqlite3 "$db" "INSERT INTO thread_edits($cols) VALUES ($vals); UPDATE thread_edits SET kind = 'merge', name = 'Fixture thread', old_name = 'Fixture other';"
	fi
	if has owner_links; then
		sqlite3 "$db" "INSERT INTO owner_links(item_id, text, rel, by, at) SELECT item_id, text, 'done', 'owner', 1760000006 FROM statements ORDER BY id LIMIT 1;"
	fi
	# The synthetic eval groups into topics without threading, so
	# thread_items would be empty: put a few statements in the first topic.
	sqlite3 "$db" "INSERT OR IGNORE INTO thread_items(topic_id, statement_id) SELECT (SELECT min(id) FROM topics), id FROM statements ORDER BY id LIMIT 5;"
	# The top results this build gives, for the test to compare with.
	printf 'data_dir = "%s"\n' "$data" >"$tmp/search-$name.toml"
	cat "$src/testdata/synthetic/config.toml" >>"$tmp/search-$name.toml"
	: >"$out/$name.search"
	for q in "why did search latency regress" "pricing decision for the renewal" "who owns the migration"; do
		hits=$(HOME="$tmp/home-$name" "$tmp/musubi-$name" --config "$tmp/search-$name.toml" search --embed hash -k 5 "$q" |
			sed -n 's/^ *[0-9]*\. .*  \([^ ]*#[0-9]*\)$/\1/p' | paste -sd ' ' -)
		printf '%s\t%s\n' "$q" "$hits" >>"$out/$name.search"
	done
	rm -f "$out/$name.db"
	sqlite3 "$db" "VACUUM INTO '$out/$name.db'"
	echo "$name ($rev): $(du -h "$out/$name.db" | cut -f1) $(sqlite3 "$out/$name.db" "SELECT 'items ' || (SELECT count(*) FROM items) || ', statements ' || (SELECT count(*) FROM statements)")"
done
