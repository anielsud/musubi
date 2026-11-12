// Package control is how the Musubi app asks the recorder to do things:
// start or stop a recording, switch a source, correct speakers and voices,
// delete a session. Only the recorder writes its own folder (sessions,
// voices), so a correction never races a recording in progress.
//
// Protocol v1: a Unix domain socket, control.sock in the recorder's data
// folder, mode 0600, carrying newline-delimited JSON. The client writes
// one request per line and reads one reply per line, matched by id:
//
//	{"v":1,"id":7,"op":"rename_speaker","args":{"session":"...","label":"Person 2","name":"..."}}
//	{"v":1,"id":7,"ok":true,"result":{...}}
//	{"v":1,"id":7,"ok":false,"error":"no such session"}
//
// Args and result shapes for each op are the types below. Unknown fields
// are ignored both ways, so either side can grow without breaking the
// other.
package control

import (
	"encoding/json"
	"path/filepath"
	"time"
)

const Version = 1

// SocketName is the control socket's file name in the recorder's folder.
const SocketName = "control.sock"

// SocketPath is the control socket in dataDir.
func SocketPath(dataDir string) string { return filepath.Join(dataDir, SocketName) }

// Request is one line from the client.
type Request struct {
	V    int             `json:"v"`
	ID   int64           `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Reply is one line from the recorder.
type Reply struct {
	V      int             `json:"v"`
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Operations. The comment gives args → result; "-" is none.
const (
	OpStatus       = "status"        // - → Status
	OpStart        = "start"         // StartArgs → Status
	OpStop         = "stop"          // - → Status
	OpSources      = "sources"       // - → Sources
	OpSetSource    = "set_source"    // SetSourceArgs → Status (applies to the recording in progress, if any)
	OpReloadConfig = "reload_config" // - → Status (re-read Musubi's [recorder] settings)

	OpSession = "session"        // SessionArgs → session.Session as JSON (session.json, voice_names, voice_doubts included)
	OpDelete  = "delete_session" // SessionArgs → - (refused for the recording in progress)

	OpRenameSpeaker  = "rename_speaker"   // SpeakerArgs (Name set; Session "" = the recording in progress) → -
	OpNotThem        = "not_them"         // SpeakerArgs → -
	OpSkipVoiceName  = "skip_voice_name"  // SpeakerArgs → -
	OpSkipVoiceDoubt = "skip_voice_doubt" // SpeakerArgs → -

	OpVoices             = "voices"               // - → VoicesView
	OpRenameVoice        = "rename_voice"         // VoiceArgs (Name) → -
	OpMergeVoices        = "merge_voices"         // VoiceArgs (ID = into, Other = from) → -
	OpKeepApart          = "keep_apart"           // VoiceArgs (ID, Other) → -
	OpForgetVoice        = "forget_voice"         // VoiceArgs → -
	OpStartOver          = "start_over"           // VoiceArgs → -
	OpConfirmVoice       = "confirm_voice"        // VoiceArgs → -
	OpRemoveVoiceMeeting = "remove_voice_meeting" // VoiceArgs (Session) → -
	OpMoveVoiceMeeting   = "move_voice_meeting"   // VoiceArgs (Session, Name = to) → -
	OpLearnSaved         = "learn_saved"          // - → Count
	OpVoiceClip          = "voice_clip"           // VoiceClip → Media (audio/mp4)
	OpDiscardedClip      = "discarded_clip"       // VoiceArgs (Session) → VoiceClip

	OpLooks     = "looks"      // SessionArgs → []LookView
	OpLookTile  = "look_tile"  // LookArgs → Media (image/jpeg): the speaker's tile
	OpLookThumb = "look_thumb" // LookArgs → Media (image/jpeg): the whole window

	OpTools   = "tools"    // - → []ToolStatus
	OpFixTool = "fix_tool" // ToolArgs → - (progress arrives as feed "status" messages)
)

// Status is the recorder's state, the result of most ops and the body of
// the live feed's "status" message.
type Status struct {
	Version   string       `json:"version"`
	Recording bool         `json:"recording"`
	Session   *SessionInfo `json:"session,omitempty"`
	StartedAt time.Time    `json:"started_at,omitzero"`
	Mic       Source       `json:"mic"`
	System    Source       `json:"system"`
	// Silent is a source in use that has had no signal for a while, and
	// Suggest a source the recorder heard signal on instead (nil if it
	// found none). Both nil when all is well.
	Silent  *Source `json:"silent,omitempty"`
	Suggest *Source `json:"suggest,omitempty"`
	// Health is one line per problem (a missing permission, model or
	// tool); empty when all is well. Details come from OpTools.
	Health []string `json:"health,omitempty"`
	// Busy says what long job is running (a model download, learning
	// voices), "" if none.
	Busy string `json:"busy,omitempty"`
	// Restart names the changed settings that wait for the recorder to
	// restart (its engines were built with the old ones); empty if none.
	Restart []string `json:"restart,omitempty"`
}

// SessionInfo identifies a session; the live feed's SessionInfo.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Platform  string    `json:"platform"`
	CreatedAt time.Time `json:"created_at"`
}

// Source is an audio source. Kind "mic" is an input device (ID is the
// device ID, "" the system default); kind "system" is the second source:
// "everything", or a decimal PID for one app's audio.
type Source struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Sources lists what can be chosen now.
type Sources struct {
	Mics   []Source `json:"mics"`
	System []Source `json:"system"`
}

// StartArgs starts a recording; empty fields use the configured sources.
type StartArgs struct {
	Mic      string `json:"mic,omitempty"`
	System   string `json:"system,omitempty"`
	Platform string `json:"platform,omitempty"`
}

// SetSourceArgs switches one source.
type SetSourceArgs struct {
	Kind string `json:"kind"` // "mic" or "system"
	ID   string `json:"id"`
}

type SessionArgs struct {
	Session string `json:"session"`
}

// SpeakerArgs names a speaker label in a session.
type SpeakerArgs struct {
	Session string `json:"session"`
	Label   string `json:"label"`
	Name    string `json:"name,omitempty"`
}

// VoiceArgs addresses a voice profile (Model + ID, as in VoicesView).
type VoiceArgs struct {
	Model   string `json:"model"`
	ID      string `json:"id"`
	Other   string `json:"other,omitempty"`
	Session string `json:"session,omitempty"`
	Name    string `json:"name,omitempty"`
}

type LookArgs struct {
	Session string `json:"session"`
	Look    int    `json:"look"`
}

type ToolArgs struct {
	ID string `json:"id"`
}

type Count struct {
	N int `json:"n"`
}

// Media is a small file, base64 in JSON.
type Media struct {
	Type string `json:"type"`
	Data []byte `json:"data"`
}

// The types below are Tomoe's window views (internal/backend), carried
// over with their JSON names so the screens ported from Tomoe read them
// unchanged.

type VoiceClip struct {
	Session string  `json:"session"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
}

type TileRef struct {
	Session string `json:"session"`
	Look    int    `json:"look"`
}

type VoiceMeetingView struct {
	Session      string    `json:"session"`
	Title        string    `json:"title"`
	At           time.Time `json:"at"`
	Source       string    `json:"source"`
	Fingerprints int       `json:"fingerprints"`
	Agreement    float64   `json:"agreement"`
	Clip         VoiceClip `json:"clip"`
	Tiles        []TileRef `json:"tiles"`
}

type VoiceProfileView struct {
	Model        string             `json:"model"`
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Meetings     int                `json:"meetings"`
	Fingerprints int                `json:"fingerprints"`
	FromTiles    int                `json:"fromTiles"`
	FromNamed    int                `json:"fromNamed"`
	LastHeard    time.Time          `json:"lastHeard"`
	Clip         VoiceClip          `json:"clip"`
	Heard        []VoiceMeetingView `json:"heard"`
	Rejected     int                `json:"rejected"`
	StartedOver  bool               `json:"startedOver"`
	TakenOut     []VoiceMeetingView `json:"takenOut"`
	Face         *TileRef           `json:"face,omitempty"`
	InUse        bool               `json:"inUse"`
	Confirmed    bool               `json:"confirmed"`
}

type NamedByVoiceView struct {
	Session    string    `json:"session"`
	Title      string    `json:"title"`
	At         time.Time `json:"at"`
	Label      string    `json:"label"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	Profile    string    `json:"profile"`
	Similarity float64   `json:"similarity"`
	Seconds    float64   `json:"seconds"`
	Clip       VoiceClip `json:"clip"`
	Window     string    `json:"window,omitempty"`
}

type VoiceDoubtView struct {
	Session    string    `json:"session"`
	Title      string    `json:"title"`
	At         time.Time `json:"at"`
	Label      string    `json:"label"`
	Window     string    `json:"window"`
	Name       string    `json:"name"`
	Similarity float64   `json:"similarity"`
	Seconds    float64   `json:"seconds"`
	Clip       VoiceClip `json:"clip"`
	NoProfile  bool      `json:"noProfile"`
}

type VoicePairView struct {
	Model      string  `json:"model"`
	A          string  `json:"a"`
	B          string  `json:"b"`
	Similarity float64 `json:"similarity"`
}

type UnknownVoice struct {
	Session    string    `json:"session"`
	Title      string    `json:"title"`
	At         time.Time `json:"at"`
	Label      string    `json:"label"`
	Seconds    float64   `json:"seconds"`
	Clip       VoiceClip `json:"clip"`
	SoundsLike string    `json:"soundsLike,omitempty"`
	Similarity float64   `json:"similarity,omitempty"`
}

type VoicesView struct {
	Enabled      bool               `json:"enabled"`
	Profiles     []VoiceProfileView `json:"profiles"`
	Pairs        []VoicePairView    `json:"pairs"`
	Unknown      []UnknownVoice     `json:"unknown"`
	NamedByVoice []NamedByVoiceView `json:"namedByVoice"`
	Doubts       []VoiceDoubtView   `json:"doubts"`
}

// LookView is one meeting-window look (Tomoe's hint timeline entry),
// Tomoe's backend LookView. Ring details stay raw: they are shown, not
// interpreted, by Musubi.
type LookView struct {
	ID          int               `json:"id"`
	Time        string            `json:"time"`
	SessionTime float64           `json:"sessionTime"`
	Stage       string            `json:"stage"`
	Detail      string            `json:"detail"`
	Window      string            `json:"window,omitempty"`
	Name        string            `json:"name,omitempty"`
	FromCache   bool              `json:"fromCache,omitempty"`
	Usable      bool              `json:"usable"`
	Ring        json.RawMessage   `json:"ring,omitempty"`
	Rings       []json.RawMessage `json:"rings,omitempty"`
	Candidates  []string          `json:"candidates,omitempty"`
	Width       int               `json:"width,omitempty"`
	Height      int               `json:"height,omitempty"`
	Thumb       string            `json:"thumb,omitempty"`   // data URI, on live looks
	ThumbOf     int               `json:"thumbOf,omitempty"` // look whose thumbnail this one shares
}

// ToolStatus is one dependency, model or permission and its fix.
type ToolStatus struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Group     string   `json:"group"`
	OK        bool     `json:"ok"`
	Required  bool     `json:"required"`
	NeededFor string   `json:"neededFor"`
	Detail    string   `json:"detail"`
	Fix       *ToolFix `json:"fix,omitempty"`
}

type ToolFix struct {
	Kind    string `json:"kind"` // "action" (OpFixTool runs it) or "command" (to copy into a terminal)
	Label   string `json:"label"`
	Command string `json:"command,omitempty"`
}

// Live feed messages added for the Musubi app, beside hello, session and
// segment (livefeed). Readers that do not know a type skip it.
const (
	FeedStatus = "status" // {"v":1,"type":"status","status":Status}
	FeedAudio  = "audio"  // {"v":1,"type":"audio","status":Status}: Silent / Suggest changed
	FeedDiag   = "diag"   // {"v":1,"type":"diag","session_id":"...","diag":Diag}
	FeedLook   = "look"   // {"v":1,"type":"look","session_id":"...","look":LookView}
)

// Diag is one live-labelling event, Tomoe's diagnostics pane entry: what
// the diarizer decided for a line and why.
type Diag struct {
	At     time.Time         `json:"at"`
	Kind   string            `json:"kind"` // e.g. "label", "match", "hint", "rename"
	Line   string            `json:"line,omitempty"`
	Label  string            `json:"label,omitempty"`
	Detail string            `json:"detail"`
	Fields map[string]string `json:"fields,omitempty"`
}
