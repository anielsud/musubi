package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config mirrors the sections of Tomoe's config.toml that recording reads.
// Tables and keys it has no field for (Tomoe's [hotkey], [output] and
// [multilingual], gpu_enabled, the hotwords keys) are skipped on load, so
// the same file serves both programs while they run side by side.
type Config struct {
	Audio         AudioConfig         `toml:"audio"`
	Transcription TranscriptionConfig `toml:"transcription"`
	Meeting       MeetingConfig       `toml:"meeting"`

	// MusubiOfRecord is Musubi's [recorder] of_record = "musubi": the
	// Musubi recorder, not Tomoe, is the recorder of record, so its own
	// voices are authoritative and Tomoe's are no longer copied over them.
	// Only Musubi's config sets it (OverlayMusubi); false ("tomoe", the
	// default) keeps copying, as before.
	MusubiOfRecord bool `toml:"-"`

	// OwnerName is Musubi's [owner] name: the owner is always "You" (the
	// mic), so a meeting window showing their own tile never names anyone
	// else (Zoom lights the owner's tile as they speak, and a late look
	// put their name on the next remote speaker).
	OwnerName string `toml:"-"`

	// TomoeOff is Musubi's [recorder] tomoe = "off": the recorder stands
	// alone, reading neither Tomoe's config file nor its models folder
	// and copying none of its voices (LoadRecorder, ModelDirs). Only
	// Musubi's config sets it; false ("on", the default) keeps reading
	// Tomoe's, as before.
	TomoeOff bool `toml:"-"`
}

// CopiesTomoeVoices reports whether the recorder starts each recording
// from a copy of Tomoe's voices: only while Tomoe is of record and on.
func (c *Config) CopiesTomoeVoices() bool { return !c.MusubiOfRecord && !c.TomoeOff }

// AudioConfig picks the microphone.
type AudioConfig struct {
	Device string `toml:"device"` // capture device name; "default" is the system's
}

// TranscriptionConfig chooses the speech models and how they decode.
type TranscriptionConfig struct {
	// ModelPath is the folder downloaded models live in (ModelDir unless
	// moved).
	ModelPath string `toml:"model_path"`
	// DecodingMethod and MaxActivePaths go to the transducer models as
	// sherpa-onnx takes them: "greedy_search" or "modified_beam_search",
	// and the beam width the latter keeps.
	DecodingMethod string `toml:"decoding_method"`
	MaxActivePaths int    `toml:"max_active_paths"`
	// TwoPass shows English meetings' text while it is spoken, from the
	// streaming live model (if downloaded), and swaps in the final model's
	// text when each utterance ends. Off, every line appears once, decoded
	// by the final model only.
	TwoPass bool `toml:"two_pass"`
	// Model is the transcription model: "auto" (the default) uses Cohere
	// Transcribe for English, the most accurate there of the models
	// compared, and the multilingual Parakeet v3 for other languages;
	// "parakeet-v2-en" or "parakeet-v3" pick those instead, and
	// "parakeet-v3" restores the previous behavior (models.ASRModels).
	Model string `toml:"model"`
	// LiveModel is the streaming model for the live text shown while
	// people speak (two_pass). "auto" is NeMo's streaming FastConformer:
	// on two meetings it differed from the final text on 25-38% of words
	// against 41-52% for the previous LibriSpeech Zipformer, for the same
	// CPU and ~275 MB more memory. "zipformer-2023" restores the previous
	// model; "nemotron-560" is closer still (18-31%) but needs ~1.9 GB and
	// three times the CPU (models.LiveModels).
	LiveModel string `toml:"live_model"`
	// DownloadMissing: a start with no transcription model (a new
	// install) downloads the required models into the recorder's own
	// folder before loading them, showing the progress in Musubi. false:
	// the start fails and launchd retries, as before.
	DownloadMissing bool `toml:"download_missing"`
}

// MeetingConfig is everything about recording a meeting: sources, line
// cutting, speakers and video hints.
//
// The speaker-clustering and video-hint-timing fields below (from
// SpeakerThreshold down) are hot-reloaded: internal/backend and
// internal/daemon both watch config.toml's mtime and re-apply these
// live via speaker.Tracker.SetTuning, so they can be retuned without a
// rebuild or even a relaunch — added after a real live-tuning session
// (see speaker.DefaultThreshold's doc comment) needed several
// rebuild+relaunch cycles just to test one constant change at a time.
type MeetingConfig struct {
	DefaultSources     string  `toml:"default_sources"`     // "mic", "monitor", "both"
	MonitorDevice      string  `toml:"monitor_device"`      // monitor source device name; "" = default monitor (Linux), "none" = mic only
	SpeakerThreshold   float64 `toml:"speaker_threshold"`   // cosine similarity threshold for a confident speaker match
	MaxSpeechDuration  float64 `toml:"max_speech_duration"` // seconds
	MinSilenceDuration float64 `toml:"min_silence_duration"`
	// MinSpeechLevelDB drops utterances quieter than this (RMS dBFS)
	// before transcription: room noise the speech detector mistakes for
	// speech, which the model fills with invented words. 0 keeps all.
	MinSpeechLevelDB float64 `toml:"min_speech_level_db"`
	// MicLevelMarginDB drops mic utterances more than this many dB below
	// the user's typical level on their own microphone (background
	// sounds, people further away). 0 turns it off.
	MicLevelMarginDB float64 `toml:"mic_level_margin_db"`
	// MicVoices tells the user's voice from others' on their own
	// microphone (someone beside them, or everyone in an in-person
	// meeting): an utterance unlike the user's saved voice is labeled as
	// another speaker. false labels everything on the mic "You", the
	// earlier behaviour.
	MicVoices bool `toml:"mic_voices"`
	// MicOtherBelow is the cosine similarity to the user's voice below
	// which a mic utterance is someone else's. On 14 meetings (2026-11-09)
	// the user's own utterances scored 0.57 or more in 95 of 100 (lowest
	// 0.41) and other people's 0.31 or less in 95 of 100 (highest 0.41).
	MicOtherBelow float64 `toml:"mic_other_below"`
	// TurnMode transcribes a speaker's turn at once instead of each
	// pause-separated utterance: lines keep growing across pauses until
	// the other side speaks, the diarizer or meeting window signals a
	// speaker change, a pause passes TurnMaxGap seconds or the line would
	// pass TurnMaxSeconds. More context per decode (13% more judged
	// errors fixed on five reference meetings) for about 0.3 points of
	// speaker accuracy; false restores one line per utterance.
	TurnMode       bool    `toml:"turn_mode"`
	TurnMaxSeconds float64 `toml:"turn_max_seconds"`
	TurnMaxGap     float64 `toml:"turn_max_gap"` // seconds
	// DecodePad is seconds of silence put before and after each line's
	// audio when it's transcribed. Without it Cohere drops a last syllable
	// that stops abruptly (a 1:1 had 7 lines ending "shouldn", "What'";
	// a replay reproduced 6, and 0.25 s fixed those 6 while staying
	// neutral on five reference meetings). 0 restores the previous
	// behavior.
	DecodePad float64 `toml:"decode_pad"`
	// TurnInterjection is the longest utterance (seconds) from one side,
	// mic or meeting audio, that doesn't end the other side's turn: a
	// "mm" or "yeah" over someone talking stays its own line instead of
	// splitting theirs in two. On two meetings it cut such splits from
	// 31 to 18, with speakers unchanged on five. 0 restores the previous
	// behavior (any utterance ends the other side's turn).
	TurnInterjection float64 `toml:"turn_interjection"`
	// SplitAtHighlight cuts meeting audio where the meeting window's
	// highlight moves to someone else, even with no pause there, so a
	// speaker who cuts in gets their own line instead of sharing one
	// with whoever was talking. Needs video hints (macOS). false restores
	// the previous behavior (lines only end at pauses).
	SplitAtHighlight bool `toml:"split_at_highlight"`
	// HighlightShortLines gives a short meeting-audio line (1.5 s or less)
	// to the person the meeting window's highlight showed for all of it,
	// rather than whoever its little audio sounds most like. false
	// restores the previous behavior.
	HighlightShortLines bool `toml:"highlight_short_lines"`
	// LiveLabelsFromHighlight labels a live line the timeline hasn't
	// reached yet with the meeting window's name before the live pass's
	// voice match, and shows that name (or "New speaker") instead of the
	// live pass's own numbering before the first recluster. The live
	// voice match split one person into 5-25 clusters a meeting. Final
	// labels are unaffected. false restores the previous live labels.
	LiveLabelsFromHighlight bool `toml:"live_labels_from_highlight"`
	// DecodeAsync (single pass) decodes finished lines on their own worker,
	// so a slow decode never stops the meeting audio being read: on an
	// overloaded machine the transcript is late, not missing (decoding
	// inline once lost 14% of a meeting's audio). false decodes inline.
	DecodeAsync bool `toml:"decode_async"`
	// DiarizeRoomSplit uses the meeting window to keep a meeting room's
	// audio (the room device's tile) apart from everyone else's, and
	// clusters it on its own, more finely (RoomSplitThreshold; two room
	// clusters merge only at RoomSplitMerge similarity), so the room's
	// people come out as separate speakers instead of one. On a labelled
	// room meeting: speakers right 94.7 -> 98.1%. Meetings without a room
	// tile are unchanged. false: one clustering for everyone, as before.
	DiarizeRoomSplit   bool    `toml:"diarize_room_split"`
	RoomSplitThreshold float64 `toml:"room_split_threshold"`
	RoomSplitMerge     float64 `toml:"room_split_merge"`
	// VoiceProfiles keeps a voice profile for each person, learnt from
	// meetings where their own tile was lit or the user named them
	// (DataDir/voices.json, fingerprints only), and names speakers the
	// meeting window can't (in a room, on the phone, with no window) by
	// who they sound like: at least VoiceProfileThreshold alike, ahead of
	// the next profile by VoiceProfileMargin. On saved meetings: 112 of
	// 119 voices named, all right; 1 of 57 voices with no profile
	// misnamed. false: no profiles kept or used, as before.
	VoiceProfiles         bool    `toml:"voice_profiles"`
	VoiceProfileThreshold float64 `toml:"voice_profile_threshold"`
	VoiceProfileMargin    float64 `toml:"voice_profile_margin"`
	// SnapReadsToInvitees corrects a name read from the meeting window on
	// a saved meeting to the spelling of the meeting's invitee it is a
	// misread of (the companion app's calendar.json in the session's
	// folder): the one invitee within an edit or two (a fifth of the
	// name's letters, at most two). A read close to no invitee, or to two,
	// is left as read; nothing is ever named outside the invitee list.
	// Applies wherever saved meetings' reads are used (rediarizing,
	// learning voice profiles, their clips); a meeting being recorded has
	// no calendar.json yet. On 25 saved meetings with a Teams transcript,
	// every one of the 27 read spellings it changed became a name in the
	// transcript, and none moved away from one. false: reads as the
	// window showed them.
	SnapReadsToInvitees bool `toml:"snap_reads_to_invitees"`
	// VoiceProfileMinMeetings: a profile names nobody until it's been
	// heard in this many meetings, unless the user confirmed it or named
	// the person themselves (1: any profile names voices, as before).
	VoiceProfileMinMeetings int `toml:"voice_profile_min_meetings"`
	// VoiceProfileMinLearn: the least fingerprints a meeting's voice needs
	// to teach a profile (voices.Params.MinLearn); fewer still names a
	// speaker. 8: learn from any voice, as before.
	VoiceProfileMinLearn int `toml:"voice_profile_min_learn"`
	// VoiceProfileRelearn: when the learning rules change
	// (voices.LearnRules), learn every saved meeting again once, in the
	// background between recordings. false: profiles keep what they were
	// built with, and only new meetings follow the new rules.
	VoiceProfileRelearn bool `toml:"voice_profile_relearn"`
	// VoiceProfileBackups copies the voices folder to voices.bak-<time>
	// (the newest five kept) before anything rewrites the profiles in
	// bulk: a file converted on load, a relearn for new rules or of every
	// saved meeting, the recorder's folder moving to a new format
	// (docs/data-safety.md, item 6). false: no copies, as before.
	VoiceProfileBackups bool `toml:"voice_profile_backups"`
	// AskUnfingerprintedSamples keeps asking "Who's this?" about samples
	// taken out of a voice ("Not them") that have no fingerprints (too
	// short for one): they can't be matched or named, only skipped.
	// false: they aren't asked about.
	AskUnfingerprintedSamples bool `toml:"ask_unfingerprinted_samples"`
	// RefineSpeakerCuts, after each meeting is saved, moves each cut
	// between back-to-back speakers to where the voice changes, from short
	// fingerprints across it (a low-priority job, about 2 minutes of one
	// core per meeting hour; diarizing during the meeting must be on). On
	// 11 changes marked by ear in a meeting with two rooms: median error
	// 0.95 -> 0.23 s; but on a second meeting, whose cuts were mostly
	// right, it made them worse (0.00 -> 0.47 s), so it's experimental.
	// false: cuts stay where the diarization put them.
	RefineSpeakerCuts bool `toml:"refine_speaker_cuts"`
	AutoSave          bool `toml:"auto_save"`   // save session on stop
	AutoDetect        bool `toml:"auto_detect"` // auto-detect meetings: an app using mic and speaker at once (PulseAudio on Linux, CoreAudio on macOS)

	// StickyGraceWindow/StickyThresholdMargin/MinAssignDuration/
	// ShortSegmentGraceWindow mirror speaker.Tuning's fields exactly
	// (seconds here instead of time.Duration, since TOML has no
	// duration type) — see speaker.Tuning's doc comment for what each
	// one does.
	StickyGraceWindow       float64 `toml:"sticky_grace_window"`
	StickyThresholdMargin   float64 `toml:"sticky_threshold_margin"`
	MinAssignDuration       float64 `toml:"min_assign_duration"`
	ShortSegmentGraceWindow float64 `toml:"short_segment_grace_window"`

	// SplitOnSpeakerChange makes the post-meeting diarization pass split a
	// transcript line wherever the speaker changes mid-line (a quick
	// "Right." from someone else), instead of giving the whole line one
	// speaker. On by default since 2026-11-12, the owner's setting; false
	// gives the whole line one speaker, as before.
	SplitOnSpeakerChange bool `toml:"split_on_speaker_change"`

	// MinSpeakerWords gives the lines of any diarized speaker who says
	// fewer words than this in the whole meeting to a neighbouring
	// speaker when diarizing during the meeting: such "speakers" are
	// diarization flickers, each shown as its own "Person N" line (see
	// session.AbsorbSmallSpeakers). 0 keeps every speaker, as before.
	MinSpeakerWords int `toml:"min_speaker_words"`
	// MinSpeakerSeconds does the same by voice, first: a diarized speaker
	// heard for less than this in the whole meeting joins the speaker
	// whose voice is most like theirs (diarize.Prepared.AbsorbSmallClusters).
	// Final labels only, when diarizing during the meeting. 0 turns it off.
	MinSpeakerSeconds float64 `toml:"min_speaker_seconds"`

	// SpeakerModel is the speaker embedding model used to tell voices
	// apart, live and after the meeting: a models.SpeakerModels ID, or
	// "auto" for the English-trained model in English meetings and the
	// base model otherwise.
	SpeakerModel string `toml:"speaker_model"`

	// DiarizeDuringMeeting runs Tomoe's own diarizer while the meeting is
	// recorded instead of sherpa-onnx's after it: labels improve as the
	// meeting goes on and the final ones are ready when it ends (see
	// docs/speaker-pipeline-design.md). On by default; false restores the
	// pass after the meeting exactly as before (sherpa-onnx in a
	// subprocess), and it falls back to that by itself if the in-process
	// diarizer can't load.
	// DiarizeStride fingerprints every nth analysis window (the CPU
	// budget) and DiarizeRecluster is the seconds of audio between
	// reclusters.
	DiarizeDuringMeeting bool    `toml:"diarize_during_meeting"`
	DiarizeStride        int     `toml:"diarize_stride"`
	DiarizeRecluster     float64 `toml:"diarize_recluster"`

	// VideoHintLearnInterval is the time between looks at the meeting
	// window while a speaker still needs naming or right after a speaker
	// change, and VideoHintCheckInterval the time between looks that only
	// confirm known names (seconds; see videohint.Watcher). Both default
	// to 1 s: on five tuned meetings, looking every 1 s named the saved
	// transcript within 0.1 points of every 0.35 s for about a third of the
	// CPU (docs/speaker-attribution-research.md). 0.35 restores the old
	// learning rate, which RecordForTuning still uses.
	// VideoHintWindow is which window video hints watch: "" finds the
	// Teams or Zoom meeting window, "none" turns them off, anything else is an
	// app's name (its largest window). An app without a rule is still
	// captured, for writing one from its saved frames.
	VideoHintWindow string `toml:"video_hint_window"`

	VideoHintLearnInterval float64 `toml:"video_hint_learn_interval"`
	VideoHintCheckInterval float64 `toml:"video_hint_check_interval"`

	// RecordForTuning records meetings at full detail for working out
	// the best settings offline (`tomoe eval --session`): video hints
	// look every tuningLookInterval (or the learning rate, if faster)
	// throughout, so sparser rates can be simulated, keep every distinct
	// full frame (about 250 MB an hour), and diarization fingerprints
	// every window. Off by default; costs more CPU and disk while on.
	RecordForTuning bool `toml:"record_for_tuning"`

	// LiveFeed serves the meeting transcript, as it is written, to other
	// programs on this computer over a Unix socket only the user can open
	// (~/.local/share/tomoe/live.sock; protocol in internal/livefeed), for
	// a local consumer that wants lines, revisions and speaker relabels
	// as they happen rather than the saved session afterwards. On by
	// default since 2026-11-12 (Musubi's Live follows it); off, nothing
	// listens or is encoded: transcription
	// is unchanged either way, since a client that falls behind is
	// disconnected rather than waited for. Applied from the settings page
	// at once; an edit to config.toml itself takes effect on the next
	// launch.
	LiveFeed bool `toml:"live_feed"`

	// SourceCheck, while a recording's mic or system audio has had no
	// signal for a while, listens briefly to the other inputs and
	// sources and, if one has signal, offers it to the Musubi app
	// (Status.Silent and Status.Suggest). false only logs the silent mic,
	// as before.
	SourceCheck bool `toml:"source_check"`
	// SourceCheckEcho, during the source check, compares what another mic
	// hears with the meeting audio being captured and does not offer a
	// mic that only hears the speakers (a webcam mic in the same room
	// would record the far side twice and not the owner). false offers
	// any mic with signal, as before.
	SourceCheckEcho bool `toml:"source_check_echo"`
}

// VideoHintsOn reports whether video hints run: on macOS (the only platform
// that captures the meeting window) unless switched off. Its text-reading
// models are only fetched then.
func (m MeetingConfig) VideoHintsOn() bool {
	return runtime.GOOS == "darwin" && m.VideoHintWindow != "none"
}

// tuningLookInterval is the look rate (seconds) RecordForTuning records
// at: replays can thin a dense recording to any sparser rate, not the
// reverse.
const tuningLookInterval = 0.35

// VideoHintTiming is VideoHintLearnInterval and VideoHintCheckInterval
// as durations, with defaults for unset values; while RecordForTuning,
// learning is at most tuningLookInterval.
func (m MeetingConfig) VideoHintTiming() (learn, check time.Duration) {
	def := DefaultConfig().Meeting
	l, c := m.VideoHintLearnInterval, m.VideoHintCheckInterval
	if l <= 0 {
		l = def.VideoHintLearnInterval
	}
	if c <= 0 {
		c = def.VideoHintCheckInterval
	}
	if m.RecordForTuning && l > tuningLookInterval {
		l = tuningLookInterval
	}
	return time.Duration(l * float64(time.Second)), time.Duration(c * float64(time.Second))
}

// DefaultConfig is what a missing file, section or key means. Load starts
// from it, so these are also the values for keys a config file predates.
func DefaultConfig() *Config {
	cfg := &Config{}
	cfg.Audio.Device = "default"
	cfg.Transcription = TranscriptionConfig{
		ModelPath:      ModelDir(),
		DecodingMethod: "greedy_search",
		MaxActivePaths: 4,
		// Off, as on main: two-pass is opt-in until it has been
		// tuned on real recordings (see `tomoe session replay`).
		TwoPass:         false,
		Model:           "auto",
		LiveModel:       "auto",
		DownloadMissing: true,
	}
	cfg.Meeting = MeetingConfig{
		DefaultSources:          "both",
		SpeakerThreshold:        0.55,
		MaxSpeechDuration:       30.0,
		MinSilenceDuration:      0.5,
		MinSpeechLevelDB:        -50,
		MicLevelMarginDB:        20,
		MicVoices:               true,
		MicOtherBelow:           0.35,
		TurnMode:                true,
		TurnMaxSeconds:          15,
		TurnMaxGap:              2,
		DecodePad:               0.25,
		TurnInterjection:        1,
		SplitAtHighlight:        true,
		HighlightShortLines:     true,
		LiveLabelsFromHighlight: true,
		DecodeAsync:             true,
		RoomSplitThreshold:      0.45,
		RoomSplitMerge:          0.7,
		VoiceProfileThreshold:   0.7,
		VoiceProfileMargin:      0.1,
		VoiceProfileMinMeetings: 2,
		VoiceProfileMinLearn:    10,
		VoiceProfileRelearn:     true,
		VoiceProfileBackups:     true,
		SnapReadsToInvitees:     true,
		AutoSave:                true,
		AutoDetect:              true,
		SpeakerModel:            "auto",
		DiarizeDuringMeeting:    true,
		DiarizeStride:           2,
		DiarizeRecluster:        10,
		MinSpeakerWords:         20,
		MinSpeakerSeconds:       2,
		SplitOnSpeakerChange:    true,
		VoiceProfiles:           true,
		DiarizeRoomSplit:        true,
		LiveFeed:                true,

		// The sticky-speaker and short-segment rules are on with the
		// owner's tuning from live meetings (2026-11-12: the defaults
		// follow the owner's config; margin and duration 0 turn them off).
		StickyGraceWindow:       1.0,
		StickyThresholdMargin:   0.15,
		MinAssignDuration:       0.7,
		ShortSegmentGraceWindow: 15.0,

		VideoHintLearnInterval: 1.0,
		VideoHintCheckInterval: 1.0,

		SourceCheck:     true,
		SourceCheckEcho: true,
	}
	return cfg
}

// xdgDir is $env when it's set, else the home directory joined with
// fallback (the XDG base-directory convention). $HOME stands in for a
// home directory the OS can't report.
func xdgDir(env string, fallback ...string) string {
	if dir := os.Getenv(env); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(append([]string{home}, fallback...)...)
}

// Path is Tomoe's config file, $XDG_CONFIG_HOME/tomoe/config.toml
// (~/.config by default): the recorder shares it while it shadows Tomoe.
func Path() string {
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "tomoe", "config.toml")
}

// TomoeDataDir is Tomoe's data folder, $XDG_DATA_HOME/tomoe
// (~/.local/share by default), holding models, sessions and voices.
func TomoeDataDir() string {
	return filepath.Join(xdgDir("XDG_DATA_HOME", ".local", "share"), "tomoe")
}

var (
	dataDirMu sync.RWMutex
	dataDir   string // "" until SetDataDir: Tomoe's folder
	tomoeOff  bool   // SetTomoeOff
)

// SetTomoeOff records the process's [recorder] tomoe = "off", for model
// lookups made without a Config at hand (ModelDirs(nil)).
func SetTomoeOff(off bool) {
	dataDirMu.Lock()
	defer dataDirMu.Unlock()
	tomoeOff = off
}

// SetDataDir moves what the recorder writes (sessions, voices, the live
// feed's socket, hint analysis) to dir, so a recorder running beside
// Tomoe never touches Tomoe's files. Models are looked for in dir/models
// first and then where Tomoe keeps them (ModelDirs). "" goes back to Tomoe's folder, which offline commands
// such as session replay read.
func SetDataDir(dir string) {
	dataDirMu.Lock()
	defer dataDirMu.Unlock()
	dataDir = dir
}

// DataDir is the folder sessions, voices and the live socket live in:
// the one SetDataDir chose, else Tomoe's (TomoeDataDir).
func DataDir() string {
	dataDirMu.RLock()
	dir := dataDir
	dataDirMu.RUnlock()
	if dir != "" {
		return dir
	}
	return TomoeDataDir()
}

// ModelDir is where Tomoe keeps downloaded models: read-only to the
// recorder, whatever SetDataDir says.
func ModelDir() string {
	return filepath.Join(TomoeDataDir(), "models")
}

// OwnModelDir is the recorder's own model folder, DataDir/models, where
// it downloads models; "" while DataDir is Tomoe's folder (offline
// commands), which has only Tomoe's.
func OwnModelDir() string {
	dataDirMu.RLock()
	dir := dataDir
	dataDirMu.RUnlock()
	if dir == "" || filepath.Clean(dir) == filepath.Clean(TomoeDataDir()) {
		return ""
	}
	return filepath.Join(dir, "models")
}

// ModelDirs are the folders models are looked for in, in order: the
// recorder's own (OwnModelDir), then cfg's model_path (Tomoe's folder by
// default; ModelDir when cfg is nil), so a model Tomoe downloaded is used
// where it is. Downloads go to the first. With Tomoe off (cfg.TomoeOff or
// SetTomoeOff) Tomoe's folder is never among them, whatever model_path
// says.
func ModelDirs(cfg *Config) []string {
	dataDirMu.RLock()
	off := tomoeOff
	dataDirMu.RUnlock()
	if cfg != nil && cfg.TomoeOff {
		off = true
	}
	tomoe := ModelDir()
	if cfg != nil && cfg.Transcription.ModelPath != "" {
		tomoe = cfg.Transcription.ModelPath
	}
	if off {
		var out []string
		for _, d := range []string{OwnModelDir(), tomoe} {
			if d != "" && !inside(d, ModelDir()) && (len(out) == 0 || filepath.Clean(out[0]) != filepath.Clean(d)) {
				out = append(out, d)
			}
		}
		return out
	}
	if own := OwnModelDir(); own != "" && filepath.Clean(own) != filepath.Clean(tomoe) {
		return []string{own, tomoe}
	}
	return []string{tomoe}
}

// inside reports whether path is dir or within it, by name.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SessionDir is where recorded sessions are saved (DataDir/sessions).
func SessionDir() string {
	return filepath.Join(DataDir(), "sessions")
}

// HintAnalysisDir is where the hint timeline's "Save for analysis" puts
// looks (~/.local/share/tomoe/hint-analysis/): frames kept for working out
// why the ring or the name wasn't found. Local only.
func HintAnalysisDir() string {
	return filepath.Join(DataDir(), "hint-analysis")
}

// Exists reports whether there is a file at Path.
func Exists() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// Load reads the config file at path over DefaultConfig, so anything the
// file leaves out keeps its default. Keys this Config has no field for are
// ignored rather than rejected.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	defer func() { _ = f.Close() }()

	cfg := DefaultConfig()
	if err := toml.NewDecoder(f).Decode(cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return cfg, nil
}

// Watch polls path's mtime every interval and calls onChange with a
// freshly reloaded Config whenever it changes -- the mechanism behind
// retuning live speaker-clustering/video-hint-timing behavior without
// a rebuild or even a relaunch (see MeetingConfig's doc comment).
// onChange is never called concurrently with itself, and never for
// the file's state as of when Watch was called (only for a real
// change made afterward). A reload that fails to parse is logged and
// skipped, leaving whatever's currently applied in place rather than
// falling back to defaults. Returns a stop func; safe to call more
// than once.
func Watch(path string, interval time.Duration, onChange func(*Config)) (stop func()) {
	var lastMod time.Time
	if info, err := os.Stat(path); err == nil {
		lastMod = info.ModTime()
	}

	done := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				info, err := os.Stat(path)
				if err != nil || !info.ModTime().After(lastMod) {
					continue
				}
				lastMod = info.ModTime()
				cfg, err := Load(path)
				if err != nil {
					fmt.Printf("config: reload of %s failed, keeping previous values: %v\n", path, err)
					continue
				}
				onChange(cfg)
			}
		}
	}()

	return func() { stopOnce.Do(func() { close(done) }) }
}
