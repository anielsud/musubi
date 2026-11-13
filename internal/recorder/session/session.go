package session

import (
	"slices"
	"time"
)

// Session is one recorded meeting and its transcript, as saved in
// session.json. Field order and JSON keys are the on-disk format Tomoe
// reads and writes too, so neither may change.
type Session struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Platform string `json:"platform,omitempty"` // meeting app, e.g. "Teams", "Zoom"; PlatformPhone for a phone's recording
	// Language is an ISO 639-1 code. Only Tomoe's multilingual mode set
	// it; kept so its sessions round-trip unchanged.
	Language  string    `json:"language,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Duration  float64   `json:"duration"` // in seconds
	Sources   []string  `json:"sources"`  // "mic", "monitor" or both
	Segments  []Segment `json:"segments"`
	AudioPath string    `json:"audio_path,omitempty"`
	// VoiceNames are the speakers voice profiles named in this meeting
	// (MeetingConfig.VoiceProfiles), for reviewing them.
	VoiceNames []VoiceName `json:"voice_names,omitempty"`
	// VoiceDoubts are speakers the meeting window named whose voice
	// sounds a little like someone else, to check.
	VoiceDoubts []VoiceDoubt `json:"voice_doubts,omitempty"`
	// TitleSet means the user named the session: its title is kept over
	// any name Tomoe finds for it (the meeting window's, a calendar
	// event's).
	TitleSet bool `json:"title_set,omitempty"`
	// Returned are speakers the user took out of a voice profile ("Not
	// them" on one of its meetings): their labels went back to unnamed,
	// and the Voices page asks who they are whatever their age or length.
	// Musubi's recorder only; Tomoe ignores the key.
	Returned []ReturnedVoice `json:"returned_voices,omitempty"`
	// EventID is the calendar event (Musubi's item id) an imported
	// recording was of, when the phone that made it named one: Musubi
	// links the session to that event rather than by time. Musubi's
	// recorder only.
	EventID string `json:"event_id,omitempty"`
}

// ReturnedVoice is a speaker a voice profile named until the user said
// it wasn't them.
type ReturnedVoice struct {
	Label   string    `json:"label"` // the speaker's label now ("Person 3")
	Name    string    `json:"name"`  // the profile's name when it was taken out
	Profile string    `json:"profile"`
	Model   string    `json:"model"`
	At      time.Time `json:"at"`
	// Skipped: the user couldn't tell who it is; not asked again.
	Skipped bool `json:"skipped,omitempty"`
}

// VoiceName is a speaker a voice profile named.
type VoiceName struct {
	Label      string  `json:"label"` // the speaker's label on its lines
	Name       string  `json:"name"`
	Profile    string  `json:"profile"` // profile ID
	Model      string  `json:"model"`   // speaker model the profile is for
	Similarity float64 `json:"similarity"`
	// Window is the name the meeting window gave the speaker, when the
	// voice profile disagreed and was trusted over it.
	Window string `json:"window,omitempty"`
	// Skipped: the user couldn't tell from its clip, so it isn't asked
	// about again. It teaches the profile nothing either way.
	Skipped bool `json:"skipped,omitempty"`
}

// VoiceDoubt is a speaker the meeting window named whose voice is
// somewhat like another person's profile: not enough to overrule the
// window, enough to ask.
type VoiceDoubt struct {
	Label      string  `json:"label"`  // the speaker's label on its lines
	Window     string  `json:"window"` // the name the window gave
	Name       string  `json:"name"`   // whom the voice sounds like
	Profile    string  `json:"profile"`
	Model      string  `json:"model"`
	Similarity float64 `json:"similarity"`
	Skipped    bool    `json:"skipped,omitempty"`
}

// VoiceMatch says a line's speaker was named by a voice profile, not by
// the meeting window or the user.
type VoiceMatch struct {
	Profile    string  `json:"profile"`
	Similarity float64 `json:"similarity"`
}

// Segment is one transcript line. Times are seconds since the session
// started.
type Segment struct {
	ID        string  `json:"id"`
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Source    string  `json:"source"` // "mic" or "monitor"
	// Language: see Session.Language.
	Language string `json:"language,omitempty"`
	// Status is "" (default, meaning final -- also what every segment
	// from before this field existed implicitly means), "live" (the
	// person is still talking; Text is pass 1's partial hypothesis and
	// will keep growing under the same ID), or "pending" (the utterance
	// is done, Text is pass 1's finished-but-unrefined text, and a
	// higher-quality re-decode is in flight). A later update carrying
	// the same ID and Status "" supersedes either. See internal/live's
	// two-pass pipeline. StatusRemoved withdraws a segment already shown
	// (its audio turned out to be noise); it's never stored.
	Status string `json:"status,omitempty"`
	// Decision is which rule inside speaker.Tracker.Assign produced
	// Speaker for this segment ("confident", "sticky", "short-segment",
	// "new-speaker" — see speaker.AssignDecision), or "" for the mic
	// source (always "You", never audio-clustered) or when no
	// clustering ran at all. Plain string rather than importing
	// speaker.AssignDecision here, since internal/session has no other
	// reason to depend on internal/speaker. Diagnostic only, purely
	// informational for a diagnostics view (see
	// docs/macos-video-hints.md) -- never read back to change
	// behavior, and safe for older sessions on disk to simply lack it.
	Decision string `json:"decision,omitempty"`
	// Words are the text's words with their timings (session seconds),
	// from the recognizer's token timestamps. Used to split a line where
	// diarization says the speaker changed (see SplitByDiarization).
	// Absent for pass-1 text and for sessions recorded before it existed.
	Words []Word `json:"words,omitempty"`
	// LiveSpeaker is the label the live pass gave the segment when a
	// later pass (diarizing during the meeting) has since relabeled
	// Speaker: kept because it can carry a video-hint name that the
	// relabeling votes on. "" means Speaker is still the live label.
	LiveSpeaker string `json:"live_speaker,omitempty"`
	// ShownSpeaker is the label the line first showed during the meeting,
	// for measuring how often live labels change before the final ones.
	ShownSpeaker string `json:"shown_speaker,omitempty"`
	// VoiceMatch is set when Speaker's name came from a voice profile.
	VoiceMatch *VoiceMatch `json:"voice_match,omitempty"`
}

// LiveLabel is the label the live pass gave seg (see LiveSpeaker).
func (seg Segment) LiveLabel() string {
	if seg.LiveSpeaker != "" {
		return seg.LiveSpeaker
	}
	return seg.Speaker
}

// Word is one transcribed word and when it was said. Short JSON keys, since
// a long meeting has thousands.
type Word struct {
	Text  string  `json:"t"`
	Start float64 `json:"s"`
	End   float64 `json:"e"`
}

// StatusRemoved is the Segment.Status that withdraws a segment.
const StatusRemoved = "removed"

// settledness ranks a Status so an update can be checked against what is
// already held; higher is further along:
// "live" < "pending" < "" (final) < removed.
func settledness(status string) int {
	switch status {
	case "live":
		return 0
	case "pending":
		return 1
	case StatusRemoved:
		return 3
	}
	return 2 // final
}

// UpsertSegment folds a new line, or a newer version of one already held
// (matched by ID), into the session.
//
// The live pipeline sends first versions and revisions on different
// channels, so their order isn't guaranteed: a revision may come before
// the line itself, or the line's first version may never come. Whatever
// arrives first is inserted, keeping Segments sorted by StartTime. After
// that an update only replaces a line if it is at least as settled, so a
// late partial can't overwrite the final text.
func (s *Session) UpsertSegment(seg Segment) {
	if i := slices.IndexFunc(s.Segments, func(have Segment) bool { return have.ID == seg.ID }); i >= 0 {
		if seg.Status == StatusRemoved {
			s.Segments = append(s.Segments[:i], s.Segments[i+1:]...)
			return
		}
		if settledness(seg.Status) < settledness(s.Segments[i].Status) {
			return
		}
		if seg.ShownSpeaker == "" {
			seg.ShownSpeaker = s.Segments[i].ShownSpeaker
		}
		s.Segments[i] = seg
		return
	}
	if seg.Status == StatusRemoved {
		return
	}
	// Lines nearly always arrive in order, so look for the slot from the
	// end. Equal start times go after the lines already there.
	pos := len(s.Segments)
	for pos > 0 && seg.StartTime < s.Segments[pos-1].StartTime {
		pos--
	}
	s.Segments = slices.Insert(s.Segments, pos, seg)
}

// PlatformPhone marks a session imported from a phone (docs/phone.md).
const PlatformPhone = "phone"
