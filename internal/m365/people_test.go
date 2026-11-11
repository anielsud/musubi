package m365

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDirectory struct {
	mu   sync.Mutex
	hits map[string]int
}

func (f *fakeDirectory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	f.mu.Lock()
	if f.hits == nil {
		f.hits = map[string]int{}
	}
	f.hits[p]++
	f.mu.Unlock()
	users := map[string]map[string]any{
		"kai.moreno@corp.example":  {"displayName": "Kai Moreno", "givenName": "Kai", "surname": "Moreno", "jobTitle": "Engineer", "department": "Platform", "officeLocation": "Remote", "accountEnabled": true},
		"robin.vale@corp.example":  {"displayName": "Vale, Robin", "givenName": "Robin", "surname": "Vale", "jobTitle": "VP Engineering", "accountEnabled": true},
		"gone.person@corp.example": {"displayName": "Gone Person", "accountEnabled": false},
	}
	managers := map[string]string{"kai.moreno@corp.example": "robin.vale@corp.example"}
	switch {
	case strings.HasSuffix(p, "/manager"):
		email := strings.TrimSuffix(strings.TrimPrefix(p, "/v1.0/users/"), "/manager")
		m, ok := managers[email]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"Request_ResourceNotFound","message":"no manager"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"displayName": users[m]["displayName"], "mail": strings.ToUpper(m[:1]) + m[1:]})
	default:
		u, ok := users[strings.TrimPrefix(p, "/v1.0/users/")]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"Request_ResourceNotFound","message":"not found"}}`)
			return
		}
		json.NewEncoder(w).Encode(u)
	}
}

func TestSyncPeople(t *testing.T) {
	fd := &fakeDirectory{}
	c := teamsClient(t, &fakeMS{})
	c.HTTP = &http.Client{Transport: routed{fd}}
	path := filepath.Join(t.TempDir(), "directory.json")
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	emails := []string{"Kai.Moreno@corp.example", "robin.vale@corp.example", "gone.person@corp.example", "nobody.here@corp.example"}
	rep, err := SyncPeople(context.Background(), c, path, emails, 14*24*time.Hour, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Looked != 3 || rep.Left != 1 {
		t.Fatalf("capped run %+v", rep)
	}
	rep, _ = SyncPeople(context.Background(), c, path, emails, 14*24*time.Hour, 3, now)
	if rep.Looked != 1 || rep.Left != 0 {
		t.Errorf("second run takes the rest: %+v", rep)
	}
	d, _ := LoadDirectory(path)
	kai := d.People["kai.moreno@corp.example"]
	if kai == nil || kai.Title != "Engineer" || kai.ManagerEmail != "robin.vale@corp.example" || kai.ManagerName != "Vale, Robin" {
		t.Errorf("kai %+v", kai)
	}
	if p := d.People["gone.person@corp.example"]; p == nil || !p.Missing {
		t.Errorf("disabled account %+v", p)
	}
	if p := d.People["nobody.here@corp.example"]; p == nil || !p.Missing {
		t.Errorf("unknown address %+v", p)
	}
	if ch := d.Chain("kai.moreno@corp.example"); len(ch) != 1 || ch[0].Name != "Vale, Robin" {
		t.Errorf("chain %+v", ch)
	}
	if rs := d.Reports("robin.vale@corp.example"); len(rs) != 1 || rs[0].Email != "kai.moreno@corp.example" {
		t.Errorf("reports %+v", rs)
	}
	// A top executive listed as their own manager isn't their own report.
	d.People["robin.vale@corp.example"].ManagerEmail = "robin.vale@corp.example"
	if rs := d.Reports("robin.vale@corp.example"); len(rs) != 1 {
		t.Errorf("self-managed reports %+v", rs)
	}
	// Within the refresh window: nothing looked up again.
	before := fd.hits["/v1.0/users/kai.moreno@corp.example"]
	SyncPeople(context.Background(), c, path, emails, 14*24*time.Hour, 3, now.Add(24*time.Hour))
	if fd.hits["/v1.0/users/kai.moreno@corp.example"] != before {
		t.Error("looked up again inside the refresh window")
	}
	// After it: looked up again.
	SyncPeople(context.Background(), c, path, emails, 14*24*time.Hour, 10, now.Add(15*24*time.Hour))
	if fd.hits["/v1.0/users/kai.moreno@corp.example"] != before+1 {
		t.Error("not refreshed after the window")
	}
}
