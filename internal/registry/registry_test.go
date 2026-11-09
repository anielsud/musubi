package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/m365"
)

func writeDir(t *testing.T, cfg config.Config, fetched time.Time) {
	t.Helper()
	d := m365.Directory{People: map[string]*m365.Person{
		"rvale@corp.example": {Email: "rvale@corp.example", Name: "Vale, Robin", GivenName: "Robin", Surname: "Vale", Title: "VP Engineering", FetchedAt: fetched},
		"gone@corp.example":  {Email: "gone@corp.example", Missing: true, FetchedAt: fetched},
	}}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(cfg.DirectoryPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The directory's display name beats the name a participant carries, its
// "Given Surname" becomes an alias, and an address that isn't first.last
// still counts as a person when the directory knows it. A refresh that
// changes only lookup times leaves the hash alone.
func TestDirectoryPeople(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Owner = config.Owner{Email: "owner@corp.example", Name: "Owner Name", InternalDomains: []string{"corp.example"}}
	items := []item.Item{{ID: "x", Kind: item.KindEvent, Participants: []item.Participant{{Email: "rvale@corp.example", Name: "R Vale"}, {Email: "gone@corp.example", Name: "Gone"}}}}
	writeDir(t, cfg, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	r, err := Build(cfg, filepath.Join(cfg.DataDir, "registry.toml"), items)
	if err != nil {
		t.Fatal(err)
	}
	var robin *string
	for _, e := range r.Entities {
		if e.ID == "person:rvale@corp.example" {
			n := e.Name
			robin = &n
			if e.Type != "person" || e.Origin != "m365" {
				t.Errorf("entity %+v", e)
			}
		}
	}
	if robin == nil || *robin != "Vale, Robin" {
		t.Fatalf("name %v", robin)
	}
	found := false
	for _, a := range r.Aliases {
		if a.EntityID == "person:rvale@corp.example" && a.Alias == "robin vale" {
			found = true
		}
	}
	if !found {
		t.Error("no Given Surname alias")
	}
	writeDir(t, cfg, time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC))
	r2, _ := Build(cfg, filepath.Join(cfg.DataDir, "registry.toml"), items)
	if r2.Hash != r.Hash {
		t.Error("hash changed on a refresh that changed nothing")
	}
}
