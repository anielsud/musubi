package recording

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// Live labelling for the Musubi app's diagnostics: Tomoe's diagnostics
// pane showed, for every line and every revision of one, who it was
// given to and why (the clustering decision, a name read from the
// meeting window, a voice match) and how final it was. The feed's
// "diag" messages carry the same, one per line change.

// Diag kinds.
const (
	diagLabel   = "label"   // a new line or a revision of one
	diagMatch   = "match"   // ... whose name came from a voice profile
	diagRelabel = "relabel" // the diarizer moved lines to another speaker
	diagRename  = "rename"  // the user named a speaker
)

// feedDiagsLocked sends a diag of kind for each of segs, as sess holds
// them now. Caller holds a.mu.
func (a *Recorder) feedDiagsLocked(sess *session.Session, kind string, segs ...session.Segment) {
	if a.liveFeed == nil {
		return
	}
	now := time.Now()
	for _, seg := range segs {
		cur := currentVersion(sess, seg)
		a.liveFeed.Diag(sess.ID, func() control.Diag { return segmentDiag(now, kind, cur) })
	}
}

// labelHint splits a live label "Person 1 (Ana)" into its speaker and the
// name the meeting window gave it.
var labelHint = regexp.MustCompile(`^(.*?)(?:\s\(([^)]+)\))?$`)

// segmentDiag describes how seg got its speaker, as Tomoe's pane did:
// "Ana [Person 1, OCR]" when a window name is attached ("OCR+Centroid
// match" when this line's voice also matched confidently), else the
// speaker alone.
func segmentDiag(at time.Time, kind string, seg session.Segment) control.Diag {
	if kind == diagLabel && seg.VoiceMatch != nil {
		kind = diagMatch
	}
	detail := seg.Speaker
	if m := labelHint.FindStringSubmatch(seg.Speaker); m != nil && m[2] != "" {
		tag := "OCR"
		if seg.Decision == "confident" {
			tag = "OCR+Centroid match"
		}
		detail = fmt.Sprintf("%s [%s, %s]", m[2], m[1], tag)
	}
	fields := map[string]string{
		"status": lineStatus(seg.Status),
		"source": seg.Source,
		"start":  strconv.FormatFloat(seg.StartTime, 'f', 2, 64),
		"end":    strconv.FormatFloat(seg.EndTime, 'f', 2, 64),
		"text":   seg.Text,
	}
	if seg.Decision != "" {
		fields["decision"] = seg.Decision
	}
	if seg.LiveSpeaker != "" {
		fields["live_label"] = seg.LiveSpeaker
	}
	if vm := seg.VoiceMatch; vm != nil {
		fields["voice_profile"] = vm.Profile
		fields["voice_similarity"] = strconv.FormatFloat(vm.Similarity, 'f', 3, 64)
	}
	return control.Diag{At: at, Kind: kind, Line: seg.ID, Label: seg.Speaker, Detail: detail, Fields: fields}
}

// lineStatus names a line's Status as the pane did.
func lineStatus(status string) string {
	switch status {
	case "live":
		return "listening"
	case "pending":
		return "refining"
	case session.StatusRemoved:
		return "removed"
	}
	return "final"
}
