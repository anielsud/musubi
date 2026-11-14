package diarize

import (
	"testing"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// A phone's recording never teaches the voice profiles unless
// phone_teaches_voices is on: LearnSaved returns before reading anything.
func TestLearnSavedSkipsPhoneRecordings(t *testing.T) {
	dir := t.TempDir() // no fingerprints: reading them fails
	phone := &session.Session{ID: "11111111-1111-4111-8111-111111111111", Platform: session.PlatformPhone}
	var m config.MeetingConfig
	names, err := LearnSaved(m, dir, phone, nil)
	if err != nil || names != nil {
		t.Fatalf("phone recording, default config: got %v, %v; want nothing learnt, no error", names, err)
	}
	m.PhoneTeachesVoices = true
	if _, err := LearnSaved(m, dir, phone, nil); err == nil {
		t.Fatal("phone_teaches_voices on: LearnSaved should try to learn (and fail on an empty folder)")
	}
	teams := &session.Session{ID: phone.ID, Platform: "Teams"}
	if _, err := LearnSaved(config.MeetingConfig{}, dir, teams, nil); err == nil {
		t.Fatal("a Mac meeting: LearnSaved should try to learn as before")
	}
}
