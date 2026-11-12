package gui

import (
	"fmt"
	"math"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// A voice profile's samples (the stretches of each meeting it learnt
// from), and "Not them" on one.

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

// VoiceSample takes a sample out of a profile's meeting (exclude: not
// them) or puts it back.
func (a *App) VoiceSample(exclude bool, args control.SampleArgs) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	ok := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x < 86400 }
	if !validModel(args.Model) || !validProfileID(args.ID) || !validSession(args.Session) || !ok(args.Start) || !ok(args.End) || args.End <= args.Start {
		return fmt.Errorf("not a voice sample")
	}
	op := control.OpIncludeSample
	if exclude {
		op = control.OpExcludeSample
	}
	if err := a.recCall(op, args, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	return nil
}
