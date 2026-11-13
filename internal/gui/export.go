package gui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/anielsud/musubi/internal/store"
)

// maxExistIDs bounds one ItemsExist call: a voice card asks about the
// meetings it lists, a few dozen at most.
const maxExistIDs = 500

// ItemsExist answers which of ids are items the scope may see, so a
// recording's meeting is offered as a link only when Musubi has it.
func (a *App) ItemsExist(ids []string, scope string) ([]string, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	if len(ids) > maxExistIDs {
		return nil, fmt.Errorf("too many ids (%d, at most %d)", len(ids), maxExistIDs)
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var want []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return []string{}, nil
	}
	found, err := a.eng.Store.ItemTitles(sc, want)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, id := range want {
		if _, ok := found[id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// SaveTranscript saves a meeting's transcript as text where the owner
// picks in a Save dialog, with what the scope may see of it (the source
// view's passages). It returns the path, or "" when the dialog was
// cancelled.
func (a *App) SaveTranscript(itemID, scope string) (string, error) {
	t, err := a.transcript(itemID, scope)
	if err != nil {
		return "", err
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Save transcript",
		DefaultFilename: transcriptFileName(t.Title, t.OccurredAt),
		Filters:         []wruntime.FileFilter{{DisplayName: "Text (*.txt)", Pattern: "*.txt"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	// The transcript is meeting text: readable by the owner only.
	if err := os.WriteFile(path, []byte(transcriptText(t)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) transcript(itemID, scope string) (*store.Transcript, error) {
	if a.eng == nil {
		return nil, a.notReady()
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, err
	}
	t, err := a.eng.Store.Transcript(sc, itemID)
	if err != nil {
		return nil, err
	}
	if t == nil || len(t.Lines) == 0 {
		return nil, fmt.Errorf("no transcript that is fine to show here")
	}
	return t, nil
}

var tomoeLabel = regexp.MustCompile(`^(?:Person|Speaker) \d+ \((.+)\)$`)

// transcriptText is the transcript as text: a header line with the title
// and when, then "[mm:ss] Speaker: text" per line, minutes counting on
// past the hour. A line taken from another copy of the meeting is marked
// as the source view marks it.
func transcriptText(t *store.Transcript) string {
	var b strings.Builder
	title := oneLine(t.Title)
	if title == "" {
		title = "Meeting"
	}
	b.WriteString(title)
	if !t.OccurredAt.IsZero() && t.OccurredAt.Year() >= 2000 {
		b.WriteString(" · " + t.OccurredAt.Format("Mon 2 Jan 2006, 15:04"))
	}
	b.WriteString("\n\n")
	for _, s := range t.Lines {
		text := oneLine(s.Text)
		if text == "" {
			continue
		}
		sec := int(max(s.Start, 0))
		fmt.Fprintf(&b, "[%02d:%02d] ", sec/60, sec%60)
		if who := oneLine(s.Speaker); who != "" {
			if m := tomoeLabel.FindStringSubmatch(who); m != nil {
				who = m[1]
			}
			b.WriteString(who + ": ")
		}
		if s.From != "" {
			b.WriteString("[" + oneLine(s.From) + " transcript, not heard by you] ")
		}
		b.WriteString(text + "\n")
	}
	return b.String()
}

// oneLine folds whitespace, newlines included, so each line of the file
// is one line of the meeting.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// transcriptFileName is a default file name from the title and date:
// letters, digits and a few safe marks, everything else a space, so no
// path separator, dot run or control character gets through.
func transcriptFileName(title string, at time.Time) string {
	var b strings.Builder
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_()&,'", r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	name := oneLine(b.String())
	if r := []rune(name); len(r) > 80 {
		name = strings.TrimSpace(string(r[:80]))
	}
	name = strings.Trim(name, "-_")
	if name == "" {
		name = "Meeting"
	}
	if !at.IsZero() && at.Year() >= 2000 {
		name += " " + at.Format("2006-01-02")
	}
	return name + ".txt"
}
