package gui

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/backup"
	"github.com/anielsud/musubi/internal/config"
)

// PrefField is one setting the owner may want to change, shown in Settings
// and written to the config in place. Only settings that already exist.
type PrefField struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	Label   string `json:"label"`
	Help    string `json:"help"`
	Type    string `json:"type"` // "bool", "int" or "string"
	Unit    string `json:"unit,omitempty"`
	Value   any    `json:"value"`
}

type PrefGroup struct {
	Title  string      `json:"title"`
	Fields []PrefField `json:"fields"`
}

var prefTabs = map[string][]PrefGroup{
	"accounts": {
		{"You", []PrefField{
			{Section: "owner", Key: "name", Label: "Name", Type: "string", Help: "How you appear in transcripts; meetings are read from your side."},
			{Section: "owner", Key: "email", Label: "Email", Type: "string", Help: "Matches you in calendar invites and Teams."},
		}},
		{"Microsoft 365 sync", []PrefField{
			{Section: "m365", Key: "enabled", Label: "Sync Microsoft 365", Type: "bool", Help: "Off: nothing is read, even when connected."},
			{Section: "m365", Key: "calendar", Label: "Calendar", Type: "bool"},
			{Section: "m365", Key: "past_days", Label: "Calendar, days back", Type: "int", Unit: "days"},
			{Section: "m365", Key: "future_days", Label: "Calendar, days ahead", Type: "int", Unit: "days"},
			{Section: "m365", Key: "first_days", Label: "First sync, days back", Type: "int", Unit: "days", Help: "How much history a new connection brings in: meetings, invites and Teams transcripts."},
			{Section: "m365", Key: "transcripts", Label: "Teams meeting transcripts", Type: "bool"},
			{Section: "m365", Key: "meeting_chat", Label: "Teams meeting chats", Type: "bool"},
			{Section: "m365", Key: "people", Label: "People from the directory", Type: "bool"},
			// Mail and Teams chats ([m365] mail, chats) are left out until their
			// extraction has been tried on real messages (2026-11-11).
			{Section: "m365", Key: "tomoe_context", Label: "Give Tomoe each meeting's invite", Type: "bool", Help: "Writes the calendar event next to each Tomoe recording, for its titles and names."},
			{Section: "m365", Key: "reconnect", Label: "Ask to sign in again when it lapses", Type: "bool"},
		}},
	},
	"dataset": {
		{"Checking for new meetings", []PrefField{
			{Section: "live", Key: "update_on_save", Label: "When Tomoe saves a meeting", Type: "bool"},
			{Section: "live", Key: "update_every", Label: "Also every", Type: "int", Unit: "minutes", Help: "0: only when Tomoe saves a meeting, or by hand."},
			{Section: "knowledge", Key: "on_update", Label: "Turn new meetings into knowledge", Type: "bool", Help: "Off: they are stored and searchable, and processed from Status."},
			{Section: "knowledge", Key: "auto_max", Label: "New meetings processed each time", Type: "int", Help: "More wait for Status → Catch up. 0: all."},
		}},
		{"Live panel", []PrefField{
			{Section: "live", Key: "llm_suggestions", Label: "Suggest what to look up", Type: "bool", Help: "Off: names and key phrases only, with no model running during meetings."},
			{Section: "live", Key: "suggest_every", Label: "Suggestions at most every", Type: "int", Unit: "seconds", Help: "0: as soon as there is new speech."},
			{Section: "live", Key: "window", Label: "Suggestions read the last", Type: "int", Unit: "seconds"},
			{Section: "live", Key: "auto_brief", Label: "Looked up without a click", Type: "int", Unit: "suggestions", Help: "How many of the top suggestions are looked up automatically; the rest wait for a click."},
			{Section: "live", Key: "topic_cards", Label: "Topic cards", Type: "bool", Help: "A subject's card when the conversation mentions it."},
			{Section: "live", Key: "focus", Label: "Focus", Type: "bool", Help: "Shows what fits the last minute and a half: at most two cards open, the rest as one line."},
			{Section: "live", Key: "keep_topics", Label: "Cards kept, collapsed", Type: "int", Help: "Cards pushed off by newer ones stay for the rest of the meeting, up to this many. 0: dropped."},
			{Section: "live", Key: "tomoe_socket", Label: "Tomoe's live feed", Type: "string", Help: "Tomoe's socket; only change it if Tomoe's is elsewhere."},
		}},
		{"Ask", []PrefField{
			{Section: "ask", Key: "threads", Label: "Answer from subjects too", Type: "bool", Help: "Their status and latest statements, as well as passages. Off: passages alone."},
			{Section: "ask", Key: "small_context", Label: "Shorter answers, sooner", Type: "bool", Help: "Reads less; answers start sooner and are slightly worse."},
		}},
		{"Backups", []PrefField{
			{Section: "backup", Key: "daily", Label: "Back up every day", Type: "bool", Help: "While Musubi is open, once a day."},
			{Section: "backup", Key: "keep_daily", Label: "Daily backups kept", Type: "int", Help: "0: none made."},
			{Section: "backup", Key: "before_changes", Label: "Back up before bulk changes", Type: "bool", Help: "Before deleting a meeting, Catch up, Rethread, renaming a voice's speakers, and the command line's forget, redo and vector re-encode. A change whose backup fails doesn't run."},
			{Section: "backup", Key: "keep_before", Label: "Backups before changes kept", Type: "int", Help: "0: none made."},
		}},
		{"Meeting prep", []PrefField{
			{Section: "prep", Key: "auto_brief", Label: "Draft talking points ahead", Type: "bool", Help: "For your meetings this week, in the background; redrafted when what they rest on changes."},
			{Section: "prep", Key: "quiet_days", Label: "Gone quiet after", Type: "int", Unit: "days", Help: "Open items nothing has been said about for this long are listed as gone quiet. 0: never."},
			{Section: "prep", Key: "mark_private", Label: "Keep personal subjects to you", Type: "bool", Help: "Jobs, pay, health, legal matters and private lives: marked for you alone and listed last when others are in the meeting."},
		}},
	},
}

// Preferences are a Settings tab's settings ("accounts" or "dataset"),
// with their current values.
func (a *App) Preferences(tab string) ([]PrefGroup, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("no dataset open")
	}
	var out []PrefGroup
	for _, g := range prefTabs[tab] {
		ng := PrefGroup{Title: g.Title}
		for _, f := range g.Fields {
			f.Value = a.eng.Cfg.Value(f.Section, f.Key)
			ng.Fields = append(ng.Fields, f)
		}
		out = append(out, ng)
	}
	return out, nil
}

// SetPreference writes one of the Preferences settings and reopens the
// dataset so it takes effect.
func (a *App) SetPreference(section, key string, value any) error {
	var field *PrefField
	for _, gs := range prefTabs {
		for _, g := range gs {
			for i := range g.Fields {
				if g.Fields[i].Section == section && g.Fields[i].Key == key {
					field = &g.Fields[i]
				}
			}
		}
	}
	if field == nil {
		return fmt.Errorf("%s.%s isn't a setting here", section, key)
	}
	var v any
	switch field.Type {
	case "bool":
		b, ok := value.(bool)
		if !ok {
			return fmt.Errorf("%s: on or off", field.Label)
		}
		v = b
	case "int":
		f, ok := value.(float64) // JSON numbers
		if !ok || f < 0 || f != float64(int(f)) {
			return fmt.Errorf("%s: a whole number, 0 or more", field.Label)
		}
		v = int(f)
	default:
		s, ok := value.(string)
		if !ok || strings.ContainsAny(s, "\n\r") {
			return fmt.Errorf("%s: one line of text", field.Label)
		}
		v = strings.TrimSpace(s)
	}
	if err := config.EditFile(a.cfgPath, []config.Edit{{Section: section, Key: key, Value: v}}); err != nil {
		return err
	}
	return a.reopen("prefs:changed")
}

// StorageView is the dataset on disk.
type StorageView struct {
	Dataset    string     `json:"dataset"`
	Config     string     `json:"config"`
	Data       string     `json:"data"`
	Parts      []DiskPart `json:"parts"`
	Total      int64      `json:"total"`
	Backups    string     `json:"backups"`
	LastBackup string     `json:"lastBackup"` // the newest backup's folder, or ""
}

type DiskPart struct {
	Label string `json:"label"`
	Bytes int64  `json:"bytes"`
}

// Storage measures the data folder: the database, the raw store (what was
// read, kept as it came), the inbox, and the rest.
func (a *App) Storage() (StorageView, error) {
	if a.eng == nil {
		return StorageView{}, fmt.Errorf("no dataset open")
	}
	data := a.eng.Cfg.DataDir
	v := StorageView{Dataset: datasetName(a.cfgPath), Config: a.cfgPath, Data: data, Backups: backupsDir()}
	sizes := map[string]int64{}
	filepath.WalkDir(data, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && filepath.Clean(p) == filepath.Clean(v.Backups) {
			return fs.SkipDir // clones: their sizes would count the dataset again
		}
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(data, p)
		part := "other"
		switch {
		case strings.HasPrefix(filepath.Base(rel), "musubi.db"):
			part = "database"
		case strings.HasPrefix(rel, "raw"+string(filepath.Separator)):
			part = "raw store (what was read, as it came)"
		case strings.HasPrefix(rel, "inbox"+string(filepath.Separator)):
			part = "inbox"
		}
		sizes[part] += info.Size()
		v.Total += info.Size()
		return nil
	})
	for _, l := range []string{"database", "raw store (what was read, as it came)", "inbox", "other"} {
		if sizes[l] > 0 {
			v.Parts = append(v.Parts, DiskPart{l, sizes[l]})
		}
	}
	if all, err := backup.List(v.Backups, v.Dataset); err == nil && len(all) > 0 {
		v.LastBackup = all[0].Name
	}
	return v, nil
}

func backupsDir() string { return backup.DefaultRoot() }

// Backup backs the whole dataset up to the backups folder
// (internal/backup): the database (a consistent copy while in use), the
// rest of the data folder and the recorder's folder (cloned: on APFS they
// take no space until either copy changes), and the config. It returns the
// backup's folder.
func (a *App) Backup() (string, error) {
	if a.eng == nil {
		return "", fmt.Errorf("no dataset open")
	}
	b, err := backup.Make(backupsDir(), a.backupSource(), backup.Manual, "backed up by hand", time.Now())
	if err != nil {
		return "", err
	}
	a.emit("storage:changed", nil)
	return b.Dir, nil
}

// RevealPath shows the dataset's config, data folder or a backup in the
// Finder; nothing else.
func (a *App) RevealPath(p string) error {
	p = filepath.Clean(p)
	ok := a.eng != nil && (p == a.cfgPath || p == filepath.Clean(a.eng.Cfg.DataDir) || p == backupsDir() ||
		filepath.Dir(p) == backupsDir())
	if !ok {
		return fmt.Errorf("not one of the dataset's folders")
	}
	if _, err := os.Stat(p); err != nil {
		return err
	}
	return exec.Command("open", "-R", p).Run()
}
