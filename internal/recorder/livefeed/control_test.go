package livefeed

import (
	"testing"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// The Musubi app's messages: a status in the snapshot after the
// session's lines, then status, audio, diag and look as published, each
// with its control type's fields.
func TestControlMessages(t *testing.T) {
	info := SessionInfo{ID: "s1", Title: "Weekly sync"}
	s := listen(t, Options{Snapshot: func(attach Attach) {
		attach(&info, []session.Segment{{ID: "seg-1", Text: "hello there"}}, StatusMessage(control.Status{Recording: true}))
	}})
	r := dial(t, s)
	for _, want := range []string{"hello", "session/started", "segment:seg-1:hello there"} {
		if got := describe(r.next()); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	}
	if m := r.next(); m["type"] != control.FeedStatus || m["status"].(map[string]any)["recording"] != true {
		t.Fatalf("snapshot status = %v", m)
	}
	waitClients(t, s, 1)

	s.Status(control.Status{Busy: "learning voices"})
	s.Audio(control.Status{Silent: &control.Source{Kind: "mic", Name: "Built-in"}})
	s.Diag("s1", func() control.Diag { return control.Diag{Kind: "label", Line: "seg-2", Label: "Person 1"} })
	s.Look("s1", func() control.LookView { return control.LookView{ID: 4, Name: "Ana", Usable: true} })

	if m := r.next(); m["type"] != "status" || m["status"].(map[string]any)["busy"] != "learning voices" {
		t.Errorf("status = %v", m)
	}
	if m := r.next(); m["type"] != "audio" || m["status"].(map[string]any)["silent"].(map[string]any)["name"] != "Built-in" {
		t.Errorf("audio = %v", m)
	}
	if m := r.next(); m["type"] != "diag" || m["session_id"] != "s1" || m["diag"].(map[string]any)["line"] != "seg-2" {
		t.Errorf("diag = %v", m)
	}
	if m := r.next(); m["type"] != "look" || m["session_id"] != "s1" || m["look"].(map[string]any)["name"] != "Ana" {
		t.Errorf("look = %v", m)
	}
}

// Nothing is built for a message with no client to read it.
func TestControlMessagesNotBuiltWithoutClients(t *testing.T) {
	s := listen(t, Options{})
	s.Diag("s1", func() control.Diag { t.Error("diag built with no client"); return control.Diag{} })
	s.Look("s1", func() control.LookView { t.Error("look built with no client"); return control.LookView{} })
	var nilServer *Server
	nilServer.Status(control.Status{})
	nilServer.Diag("s1", func() control.Diag { return control.Diag{} })
}
