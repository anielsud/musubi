package store

import (
	"bufio"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Databases as earlier builds left them (testdata/fixtures, made by
// scripts/fixtures.sh from the synthetic corpus) open with this build
// without losing anything: the upgrade a colleague's dataset goes through.
func TestFixturesUpgrade(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "fixtures")
	fixtures, _ := filepath.Glob(filepath.Join(dir, "*.db"))
	if len(fixtures) == 0 {
		t.Fatal("no fixtures in testdata/fixtures")
	}
	for _, fx := range fixtures {
		name := strings.TrimSuffix(filepath.Base(fx), ".db")
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "musubi.db")
			copyFile(t, fx, path)
			before := rawCounts(t, path)
			s, err := OpenWith(path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()

			var v int
			s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
			if f, err := storedFormat(s.db); v != schemaVersion || f != formatVersion || err != nil {
				t.Errorf("schema %d, format %d (%v); want %d, %d", v, f, err, schemaVersion, formatVersion)
			}
			if done, _ := appliedMigrations(s.db); len(done) != len(migrations) {
				t.Errorf("ledger %v", done)
			}
			// Every fixture predates the ledger, so every one is copied.
			if s.UpgradeBackup == "" {
				t.Error("no copy made before the upgrade")
			} else if _, err := os.Stat(s.UpgradeBackup); err != nil {
				t.Error(err)
			}
			after := rawCounts(t, path)
			for _, tb := range countedTables {
				if before[tb] != after[tb] {
					t.Errorf("%s: %d rows before, %d after", tb, before[tb], after[tb])
				}
			}
			for _, tb := range []string{"items", "statements", "thread_items", "forgotten", "audit", "judgments"} {
				if before[tb] == 0 {
					t.Errorf("%s is empty in the fixture: the test checks nothing there", tb)
				}
			}

			emb := provider.Hash{Dim: 256}
			for q, want := range searches(t, filepath.Join(dir, name+".search")) {
				vec, _ := emb.Embed(context.Background(), []string{q}, true)
				hits, err := s.Search(sensitivity.Owner(), SearchQuery{Text: q, Vec: vec[0], Model: emb.Model(), K: 5, Mode: "hybrid"})
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, h := range hits {
					got = append(got, h.ItemID+"#"+strconv.Itoa(h.Seq))
				}
				if strings.Join(got, " ") != want {
					t.Errorf("%q:\n got  %s\n want %s", q, strings.Join(got, " "), want)
				}
			}
		})
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// rawCounts counts every row (proposed aliases too) of the counted tables.
func rawCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]int{}
	for _, tb := range countedTables {
		var n int
		if db.QueryRow(`SELECT count(*) FROM "`+tb+`"`).Scan(&n) == nil {
			out[tb] = n
		}
	}
	return out
}

// searches reads "query<TAB>item#seq item#seq ..." lines.
func searches(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if q, hits, ok := strings.Cut(sc.Text(), "\t"); ok {
			out[q] = hits
		}
	}
	if len(out) == 0 {
		t.Fatalf("no searches in %s", path)
	}
	return out
}
