package gui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// placeholderLabel is a speaker the recorder couldn't name.
var placeholderLabel = regexp.MustCompile(`^Person \d+$`)

// teamsRename is one speaker the Teams transcript names.
type teamsRename struct{ Label, Name string }

// teamsRenames are the unnamed speakers of sess that its Teams transcript
// names (session.AlignTeams, with its default, strict rules: the text must
// line up with the recording, and one name must clearly cover the
// speaker's speech). A name already on another speaker, or the owner's,
// is left alone: the owner is "You", and a name in use means the
// recording knows that person already.
func teamsRenames(sess *session.Session, tt *session.TeamsTranscript, owner string) []teamsRename {
	if sess == nil || tt == nil {
		return nil
	}
	al := session.AlignTeams(sess.Segments, tt.Spans(sess.CreatedAt), session.DefaultTeamsAlignParams())
	used := map[string]bool{strings.ToLower(strings.TrimSpace(owner)): true}
	for _, s := range sess.Segments {
		if !placeholderLabel.MatchString(s.Speaker) {
			used[strings.ToLower(labelPerson(s.Speaker))] = true
		}
	}
	var out []teamsRename
	for _, l := range al.Labels {
		key := strings.ToLower(l.Proposed)
		if l.Proposed == "" || !placeholderLabel.MatchString(l.Label) || used[key] {
			continue
		}
		used[key] = true
		out = append(out, teamsRename{l.Label, l.Proposed})
	}
	return out
}

// labelPerson is the person a label names: "Ana" of "Person 3 (Ana)".
func labelPerson(label string) string {
	if i := strings.LastIndex(label, " ("); i > 0 && strings.HasSuffix(label, ")") {
		return label[i+2 : len(label)-1]
	}
	return strings.TrimSpace(label)
}

// nameFromTeams names recorded meetings' unnamed speakers from their
// Teams transcripts ([m365] teams_names), through the recorder's rename,
// so the voice profiles learn them too, and takes each rename into
// Musubi's copy without re-extracting the meeting ([recorder] relabel).
// A meeting is looked at once per transcript (teams-names.json in the
// data folder records which).
func (a *App) nameFromTeams() {
	if a.eng == nil || !a.eng.Cfg.M365.TeamsNames || !a.eng.Cfg.MusubiRecords() || !a.recorderUp() {
		return
	}
	dir := a.eng.Cfg.RecorderSessions()
	donePath := filepath.Join(a.eng.Cfg.DataDir, "teams-names.json")
	done := map[string]string{}
	if b, err := os.ReadFile(donePath); err == nil {
		_ = json.Unmarshal(b, &done)
	}
	var st control.Status
	_ = a.recCall(control.OpStatus, nil, &st)
	recording := ""
	if st.Recording && st.Session != nil {
		recording = st.Session.ID
	}
	entries, _ := os.ReadDir(dir)
	store := session.NewStore(dir)
	changed := false
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !validSession(id) || id == recording {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, id, "teams.json"))
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
		if done[id] == key {
			continue
		}
		sess, err := store.Load(id)
		if err != nil {
			continue
		}
		renamed := false
		for _, r := range teamsRenames(sess, store.TeamsTranscript(id), a.eng.Cfg.Owner.Name) {
			if err := a.recCall(control.OpRenameSpeaker, control.SpeakerArgs{Session: id, Label: r.Label, Name: r.Name}, nil); err != nil {
				fmt.Printf("teams names %s: %v\n", id, err)
				continue
			}
			renamed = true
		}
		if renamed && !a.relabelRenamedSpeaker(id) {
			a.updateSoon("a meeting was edited")
		}
		done[id], changed = key, true
	}
	if changed {
		if b, err := json.Marshal(done); err == nil {
			_ = os.WriteFile(donePath, b, 0o600)
		}
		a.emit("voices:changed", nil)
	}
}
