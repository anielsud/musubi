package store

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"

	"github.com/anielsud/musubi/internal/item"
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
