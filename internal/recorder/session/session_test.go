package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The exact bytes matter: Tomoe and Musubi read each other's session.json,
// so keys, their order and value formats are pinned here.
func TestSessionJSONIsTomoeFormat(t *testing.T) {
	start := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	sess := &Session{
		ID:        "abc",
		Title:     "Weekly sync",
		Platform:  "Zoom",
		CreatedAt: start,
		EndedAt:   start.Add(90 * time.Second),
		Duration:  90.5,
		Sources:   []string{"mic", "monitor"},
		Segments: []Segment{{
			ID: "s1", Speaker: "Person 1 (Kit)", Text: "Hi there", StartTime: 1.25, EndTime: 2,
			Source: "monitor", Status: "pending", Decision: "sticky",
			Words:       []Word{{Text: "Hi", Start: 1.25, End: 1.5}},
			LiveSpeaker: "Person 2", ShownSpeaker: "Person 2",
			VoiceMatch: &VoiceMatch{Profile: "p1", Similarity: 0.5},
		}},
		AudioPath:   "/data/abc/audio.m4a",
		VoiceNames:  []VoiceName{{Label: "Person 1", Name: "Kit", Profile: "p1", Model: "m", Similarity: 0.75}},
		VoiceDoubts: []VoiceDoubt{{Label: "Person 3", Window: "Lee", Name: "Kit", Profile: "p1", Model: "m", Similarity: 0.25, Skipped: true}},
		TitleSet:    true,
	}
	got, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"abc","title":"Weekly sync","platform":"Zoom","created_at":"2026-04-02T10:00:00Z",` +
		`"ended_at":"2026-04-02T10:01:30Z","duration":90.5,"sources":["mic","monitor"],` +
		`"segments":[{"id":"s1","speaker":"Person 1 (Kit)","text":"Hi there","start_time":1.25,"end_time":2,` +
		`"source":"monitor","status":"pending","decision":"sticky","words":[{"t":"Hi","s":1.25,"e":1.5}],` +
		`"live_speaker":"Person 2","shown_speaker":"Person 2","voice_match":{"profile":"p1","similarity":0.5}}],` +
		`"audio_path":"/data/abc/audio.m4a",` +
		`"voice_names":[{"label":"Person 1","name":"Kit","profile":"p1","model":"m","similarity":0.75}],` +
		`"voice_doubts":[{"label":"Person 3","window":"Lee","name":"Kit","profile":"p1","model":"m","similarity":0.25,"skipped":true}],` +
		`"title_set":true}`
	if string(got) != want {
		t.Errorf("JSON\n got %s\nwant %s", got, want)
	}

	var back Session
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, sess) {
		t.Errorf("round trip changed the session:\n got %+v\nwant %+v", back, *sess)
	}
}

// Optional fields stay out of the file when unset; the always-present ones
// (and a zero ended_at, which omitempty can't drop from a struct) stay in.
func TestSessionJSONMinimal(t *testing.T) {
	got, err := json.Marshal(&Session{ID: "x", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"x","title":"","created_at":"2026-01-01T00:00:00Z","ended_at":"0001-01-01T00:00:00Z",` +
		`"duration":0,"sources":null,"segments":null}`
	if string(got) != want {
		t.Errorf("JSON\n got %s\nwant %s", got, want)
	}

	seg, _ := json.Marshal(Segment{ID: "a"})
	if s := string(seg); s != `{"id":"a","speaker":"","text":"","start_time":0,"end_time":0,"source":""}` {
		t.Errorf("segment JSON = %s", s)
	}
}

// Files from Tomoe, including ones older than most fields, and ones from
// its multilingual mode, load as they were written.
func TestSessionReadsTomoeFiles(t *testing.T) {
	old := `{"id":"o","title":"T","created_at":"2026-03-13T14:00:00Z","duration":300,` +
		`"language":"bn","segments":[{"id":"1","speaker":"You","text":"hi","start_time":0,"end_time":1,"source":"mic","language":"bn"}]}`
	var s Session
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatal(err)
	}
	if s.ID != "o" || s.Duration != 300 || s.Platform != "" || s.Language != "bn" ||
		len(s.Segments) != 1 || s.Segments[0].Language != "bn" || s.Segments[0].Status != "" {
		t.Errorf("loaded %+v", s)
	}
	again, _ := json.Marshal(&s)
	if !strings.Contains(string(again), `"language":"bn"`) {
		t.Errorf("language dropped on save: %s", again)
	}
}

func TestLiveLabel(t *testing.T) {
	if l := (Segment{Speaker: "Person 2"}).LiveLabel(); l != "Person 2" {
		t.Errorf("no live speaker: %q", l)
	}
	if l := (Segment{Speaker: "Person 2", LiveSpeaker: "Person 1 (Kit)"}).LiveLabel(); l != "Person 1 (Kit)" {
		t.Errorf("relabeled: %q", l)
	}
}

func ids(segs []Segment) string {
	var out []string
	for _, s := range segs {
		out = append(out, s.ID)
	}
	return strings.Join(out, " ")
}

func TestUpsertSegmentSettlesForward(t *testing.T) {
	var s Session
	for _, u := range []Segment{
		{ID: "x", Text: "th", Status: "live"},
		{ID: "x", Text: "the", Status: "live"}, // same rank replaces
		{ID: "x", Text: "the end", Status: "pending"},
		{ID: "x", Text: "the e", Status: "live"}, // stale partial: ignored
		{ID: "x", Text: "The end."},
		{ID: "x", Text: "the end", Status: "pending"}, // stale: ignored
	} {
		s.UpsertSegment(u)
	}
	if len(s.Segments) != 1 || s.Segments[0].Text != "The end." || s.Segments[0].Status != "" {
		t.Errorf("segments = %+v", s.Segments)
	}
}

func TestUpsertSegmentKeepsFirstShownSpeaker(t *testing.T) {
	var s Session
	s.UpsertSegment(Segment{ID: "x", Speaker: "Person 1", ShownSpeaker: "Person 1", Status: "live"})
	s.UpsertSegment(Segment{ID: "x", Speaker: "Person 2"})
	if got := s.Segments[0]; got.Speaker != "Person 2" || got.ShownSpeaker != "Person 1" {
		t.Errorf("segment = %+v", got)
	}
	s.UpsertSegment(Segment{ID: "x", Speaker: "Person 3", ShownSpeaker: "Person 3"})
	if got := s.Segments[0].ShownSpeaker; got != "Person 3" {
		t.Errorf("explicit shown speaker = %q", got)
	}
}

func TestUpsertSegmentFinalBeforeFirstVersion(t *testing.T) {
	var s Session
	s.UpsertSegment(Segment{ID: "x", Text: "Done.", StartTime: 2})
	s.UpsertSegment(Segment{ID: "x", Text: "do", StartTime: 2, Status: "live"})
	if len(s.Segments) != 1 || s.Segments[0].Text != "Done." {
		t.Errorf("segments = %+v", s.Segments)
	}
}

func TestUpsertSegmentOrdersByStart(t *testing.T) {
	var s Session
	for _, u := range []Segment{
		{ID: "b", StartTime: 4},
		{ID: "d", StartTime: 9},
		{ID: "a", StartTime: 0}, // before everything
		{ID: "c", StartTime: 4}, // tie: after the one already there
		{ID: "e", StartTime: 9.5},
	} {
		s.UpsertSegment(u)
	}
	if got := ids(s.Segments); got != "a b c d e" {
		t.Errorf("order = %q", got)
	}
}
