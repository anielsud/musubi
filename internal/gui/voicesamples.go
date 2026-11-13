package gui

import (
	"fmt"
	"math"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// A voice profile's samples (the stretches of each meeting it learnt
// from), "Not them" and "Junk" on one, naming a returned one under
// "Who's this?", and splitting a speaker by voice.

// validModel: speaker model IDs are short names ("eres2net-en").
func validModel(m string) bool {
	if m == "" || len(m) > 64 {
		return false
	}
	for _, c := range m {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// ProfileSamples is every meeting's samples for a voice profile.
func (a *App) ProfileSamples(model, id string) (*control.ProfileSamplesView, error) {
	if !validModel(model) || !validProfileID(id) {
		return nil, fmt.Errorf("not a voice profile")
	}
	var v control.ProfileSamplesView
	return &v, a.recCall(control.OpProfileSamples, control.VoiceArgs{Model: model, ID: id}, &v)
}

// sampleOps are the actions on one sample of a profile's meeting.
var sampleOps = map[string]string{
	"not_them": control.OpExcludeSample, // to "Who's this?"
	"junk":     control.OpJunkSample,    // no profile learns from it
	"put_back": control.OpIncludeSample, // either undone
}

// validSampleArgs checks a sample's address before anything is asked.
func validSampleArgs(args control.SampleArgs) bool {
	ok := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x < 86400 }
	return validModel(args.Model) && validProfileID(args.ID) && validSession(args.Session) && ok(args.Start) && ok(args.End) && args.End > args.Start
}

// VoiceSample is an action on a sample of a profile's meeting: "not_them"
// (it leaves the profile and is asked about under "Who's this?"), "junk"
// (no profile learns from it) or "put_back".
func (a *App) VoiceSample(action string, args control.SampleArgs) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	op, known := sampleOps[action]
	if !known {
		return fmt.Errorf("no such sample action %q", action)
	}
	if !validSampleArgs(args) {
		return fmt.Errorf("not a voice sample")
	}
	args.Name = ""
	if err := a.recCall(op, args, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	return nil
}

// NameReturnedSample names a sample taken out with "Not them", under
// "Who's this?": its fingerprints teach the profile named name (an
// existing one of exactly that name its voice fits, else a new one).
// Labels in the meeting are unchanged, so Musubi's copy needs nothing.
func (a *App) NameReturnedSample(args control.SampleArgs, name string) (*control.NamedSample, error) {
	if err := a.requireEditable(); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || strings.ContainsAny(name, "\n\r") {
		return nil, fmt.Errorf("a one-line name is needed")
	}
	if !validSampleArgs(args) {
		return nil, fmt.Errorf("not a voice sample")
	}
	args.Name = name
	var out control.NamedSample
	if err := a.recCall(control.OpNameReturnedSample, args, &out); err != nil {
		return nil, err
	}
	a.emit("voices:changed", nil)
	return &out, nil
}

// SkipReturnedSample is "Can't tell" on a sample under "Who's this?".
func (a *App) SkipReturnedSample(args control.SampleArgs) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	if !validSampleArgs(args) {
		return fmt.Errorf("not a voice sample")
	}
	args.Name = ""
	if err := a.recCall(control.OpSkipReturnedSample, args, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	return nil
}

// SplitTarget is a profile a speaker's lines may go to, and the label
// they get ("" for the profile's name).
type SplitTarget struct {
	Model string `json:"model"`
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// SplitSpeakerByVoice gives each line of label in a saved meeting to the
// profile (of into, at least two) its voice matches best by a margin
// (docs/recorder-screens.md, "Split a speaker by voice"). Who said what
// changes, so the meeting is imported again (re-extracted) as any changed
// meeting is. preview works it out without saving.
func (a *App) SplitSpeakerByVoice(session, label string, into []SplitTarget, preview bool) (*control.SplitResult, error) {
	if err := a.requireEditable(); err != nil {
		return nil, err
	}
	if !validSession(session) {
		return nil, fmt.Errorf("not a session id")
	}
	if label == "" || len(label) > 200 || strings.ContainsAny(label, "\n\r") {
		return nil, fmt.Errorf("a speaker is needed")
	}
	if len(into) < 2 || len(into) > 8 {
		return nil, fmt.Errorf("pick two to eight voices to split it into")
	}
	args := control.SplitArgs{Session: session, Label: label, Preview: preview}
	for _, t := range into {
		if !validModel(t.Model) || !validProfileID(t.ID) || len(t.Label) > 200 || strings.ContainsAny(t.Label, "\n\r") {
			return nil, fmt.Errorf("not a voice profile")
		}
		args.Into = append(args.Into, control.SplitTarget{ID: t.ID, Label: strings.TrimSpace(t.Label)})
	}
	var out control.SplitResult
	if err := a.recCall(control.OpSplitLabelByVoice, args, &out); err != nil {
		return nil, err
	}
	if !preview && out.Batch != "" {
		a.emit("voices:changed", nil)
		a.emit("meetings:changed", "")
		a.updateSoon()
	}
	return &out, nil
}

// UndoSplit puts the lines a split moved back on their speaker; the
// meeting is imported again.
func (a *App) UndoSplit(batch string) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	if !relabelBatch.MatchString(batch) {
		return fmt.Errorf("not a split")
	}
	if err := a.recCall(control.OpUndoRelabel, control.RelabelArgs{Batch: batch}, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	a.emit("meetings:changed", "")
	a.updateSoon()
	return nil
}
