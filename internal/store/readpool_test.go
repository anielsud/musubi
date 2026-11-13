package store

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// TestMain runs the package's tests twice: as Open has always opened the
// database (one connection), and again with a read pool, a page cache and
// memory-mapped reads, so every read routed to the pool (reads.go) is
// checked against what the writer just wrote.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		openOptions = Options{CacheMB: 8, MmapMB: 64, ReadConns: 3}
		code = m.Run()
	}
	os.Exit(code)
}

// TestReadPool: the pool's connections read what the writer committed at
// once, never write, and the knowledge signature still moves with every
// write of this process (it is read from a connection of its own).
func TestReadPool(t *testing.T) {
	s, err := OpenWith(filepath.Join(t.TempDir(), "t.db"), Options{CacheMB: 4, MmapMB: 16, ReadConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.rdb == s.db || s.sdb == nil {
		t.Fatal("no read pool")
	}
	sig0, err := s.KnowledgeSignature()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if _, err := s.UpsertItem(item.Item{ID: "m:1", Source: "t", Ref: "m:1", Kind: item.KindMeeting, Title: "Weekly sync", OccurredAt: at, Segments: []item.Segment{{Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	items, err := s.AllItems() // routed to the pool
	if err != nil || len(items) != 1 {
		t.Fatalf("pool read after a write: %d items, %v", len(items), err)
	}
	sig1, _ := s.KnowledgeSignature()
	if sig1 == sig0 {
		t.Error("a write didn't move the signature")
	}
	if sig2, _ := s.KnowledgeSignature(); sig2 != sig1 {
		t.Errorf("no write, but the signature moved: %s, %s", sig1, sig2)
	}
	if _, err := s.rdb.Exec(`DELETE FROM items`); err == nil {
		t.Error("a pool connection wrote")
	}
	// sqlite-vec is loaded into the pool's connections too.
	a, _ := sqlite_vec.SerializeFloat32([]float32{1, 0})
	b, _ := sqlite_vec.SerializeFloat32([]float32{0, 1})
	var d float64
	if err := s.rdb.QueryRow(`SELECT vec_distance_cosine(?, ?)`, a, b).Scan(&d); err != nil || math.Abs(d-1) > 1e-6 {
		t.Fatalf("sqlite-vec on a pool connection: %v, %v", d, err)
	}
	var mm, cache int64
	if err := s.rdb.QueryRow(`PRAGMA mmap_size`).Scan(&mm); err != nil || mm != 16<<20 {
		t.Errorf("mmap_size on a pool connection: %d, %v", mm, err)
	}
	if err := s.db.QueryRow(`PRAGMA cache_size`).Scan(&cache); err != nil || cache != -4*1024 {
		t.Errorf("cache_size on the writer: %d, %v", cache, err)
	}
}

// TestReadsDontWaitForTheWriter: with the writer's connection held (as a
// long write holds it), Live's reads and the signature their caches check
// still answer. Runs in the read-pool pass of TestMain.
func TestReadsDontWaitForTheWriter(t *testing.T) {
	if openOptions.ReadConns <= 1 {
		t.Skip("read pool pass only")
	}
	s := nearFixture(t)
	defer s.Close()
	sc := sensitivity.Owner()
	q := []float32{1, 0, 0}
	// Loaded once per change of the database, after a backfill that
	// writes: not a read that waits each time.
	if _, err := s.ClaimIndex(); err != nil {
		t.Fatal(err)
	}
	hs, err := s.StatementsNear(sc, q, "m", 10, nil)
	if err != nil || len(hs) == 0 {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	reads := []struct {
		name string
		f    func() error
	}{
		{"KnowledgeSignature", func() error { _, err := s.KnowledgeSignature(); return err }},
		{"ThreadStatuses", func() error { _, err := s.ThreadStatuses(sc); return err }},
		{"TopicInfos", func() error { _, err := s.TopicInfos(); return err }},
		{"StatementsNear", func() error { _, err := s.StatementsNear(sc, q, "m", 10, nil); return err }},
		{"WithPassages", func() error { return s.WithPassages(sc, hs) }},
		{"Evidence", func() error { _, err := s.Evidence(sc, []int64{hs[0].ClaimID}); return err }},
		{"EntityGraph", func() error { _, err := s.EntityGraph(sc, "person:nobody", nil, 4); return err }},
		{"AllItems", func() error { _, err := s.AllItems(); return err }},
		{"WorkStamp", func() error { _, err := s.WorkStamp(); return err }},
	}
	for _, r := range reads {
		done := make(chan error, 1)
		go func() { done <- r.f() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", r.name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s waited for the writer's connection", r.name)
		}
	}
}
