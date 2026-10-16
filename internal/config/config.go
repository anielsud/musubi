// Package config loads Musubi's TOML config over DefaultConfig, so a key
// missing from the file keeps its default.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	// DataDir holds the database, raw blobs and inbox.
	DataDir string `toml:"data_dir"`
	// TomoeSessions is Tomoe's session directory; empty disables the source.
	TomoeSessions string `toml:"tomoe_sessions"`

	Owner       Owner       `toml:"owner"`
	Embed       Embed       `toml:"embed"`
	LLM         LLM         `toml:"llm"`
	Live        Live        `toml:"live"`
	Sensitivity Sensitivity `toml:"sensitivity"`
	Knowledge   Knowledge   `toml:"knowledge"`
}

// Knowledge configures statement extraction.
type Knowledge struct {
	// Gate skips passages whose probability of holding knowledge (one
	// yes/no forward pass) is below this. 0 extracts every passage, the
	// previous behaviour, and keeps existing extractions valid.
	Gate float64 `toml:"gate"`
	// MinWorth leaves statements whose worth score (one yes/no pass per
	// statement: work knowledge, or small talk and garble?) is below this
	// out of claims. Scores are stored, so changing it re-links but never
	// re-extracts. 0 turns scoring off, the previous behaviour.
	MinWorth float64 `toml:"min_worth"`
	// Judge is how candidate claims are judged: "json" (the model writes
	// verdicts for up to 3 candidates at once, the earlier behaviour) or
	// "decide" (one forward pass per pair, read as probabilities, which is
	// cheap enough to consider more candidates).
	Judge string `toml:"judge"`
	// Extract is how knowledge is taken from items: "none" (statements are
	// imported, e.g. by `musubi import-items`; only embedding, linking and
	// topics run), "statements" (one call per passage, the earlier
	// behaviour) or "points" (a meeting-level
	// brief, then self-contained points per run of passages, read with the
	// neighbouring passages as context; see docs/research-log.md).
	Extract string `toml:"extract"`
	// StrictLinks stops linking from retiring live items: a same-meeting
	// "supersedes" without a spoken correction becomes "refines", one
	// owner's commitment never replaces another's, and a promise to find
	// out leaves a question open ("pending"). false: the earlier rules.
	StrictLinks bool `toml:"strict_links"`
	// ImportKeepProposed: imported proposed decisions and commitments keep
	// their kind, imported items get typed values read from their text,
	// and the import check accepts the meeting's date and known people's
	// full names. false: the earlier import.
	ImportKeepProposed bool `toml:"import_keep_proposed"`
	// Points configures extract = "points".
	Points Points `toml:"points"`
	// Grouping is how claims are grouped and related: "topics" (pairwise
	// judging, then clustering; the earlier behaviour) or "threads" (a model
	// assigns each meeting's items to specific threads, then reconciles
	// each thread it touched; docs/linking-benchmark.md). Threads read
	// meeting text, so they need a local model (Threads.Model), and they
	// replace the pairwise judge: only near-identical statements join.
	Grouping string  `toml:"grouping"`
	Threads  Threads `toml:"threads"`
}

// Threads configures grouping = "threads".
type Threads struct {
	// Model does the threading; provider "" uses [llm]. Measured with
	// Qwen3.8-27B on llama-server (provider "openai").
	Model LLM `toml:"model"`
	// Chunk is how many items one assign call reads; Parallel how many
	// threads are reconciled at once (match the server's slots).
	Chunk    int `toml:"chunk"`
	Parallel int `toml:"parallel"`
	// Candidates is how many threads, nearest by embedding, an assign call
	// chooses from (plus the most recently active); StateCap how many of a
	// thread's current statements a reconcile call sees. They keep a
	// meeting's cost flat however long the history (research-log.md,
	// 2026-10-15/16). 0 uses the defaults (40, 25).
	Candidates int `toml:"candidates"`
	StateCap   int `toml:"state_cap"`
}

// Points configures meeting-level point extraction.
type Points struct {
	// Model is the extraction model; provider "" uses [llm]. Points were
	// measured with Qwen3.8-27B on llama-server (provider "openai").
	Model LLM `toml:"model"`
	// Think lets the model reason before writing a segment's points: about
	// half the errors, at about twice the time. Run llama-server with
	// --reasoning-budget to bound it.
	Think bool `toml:"think"`
	// Segment is how many passages one call covers.
	Segment int `toml:"segment"`
	// Parallel is how many segments are extracted at once; match the
	// server's slots (llama-server -np).
	Parallel int `toml:"parallel"`
}

// LLM is the chat model for answers, live query suggestions and briefs.
type LLM struct {
	// Provider is "ollama", "openai" (an OpenAI-compatible server such as
	// llama-server; URL without /v1) or "none" (Q&A then shows passages
	// only, and live suggestions come from names and key phrases alone).
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	URL      string `toml:"url"`
	// StartOllama lets the app start `ollama serve` if it isn't running.
	StartOllama bool `toml:"start_ollama"`
}

// Live configures the side panel during a meeting.
type Live struct {
	// TomoeSocket is Tomoe's live feed (Tomoe's [meeting] live_feed = true).
	TomoeSocket string `toml:"tomoe_socket"`
	// FastLane looks up names and distinctive phrases as soon as a line
	// (or a live draft) contains them, without waiting for the model.
	// false, with suggest_every = 20, restores the first behaviour: one
	// cycle every 20 s doing everything.
	FastLane bool `toml:"fast_lane"`
	// TopicCards shows a topic's card (docs/topic-cards.md) when a line says
	// one of its trigger terms, or a lookup's nearest claims are on it.
	// false: no topic cards, the earlier panel.
	TopicCards bool `toml:"topic_cards"`
	// PhraseLookups looks up distinctive phrases from single lines on
	// their own. Off: on real meetings they were mostly fragments ("moves
	// the bar"); the model's queries cover topics better.
	PhraseLookups bool `toml:"phrase_lookups"`
	// SuggestEvery is the minimum gap, in seconds, between the model's query
	// suggestions; 0 runs them back to back while new speech arrives.
	SuggestEvery int `toml:"suggest_every"`
	// Window is how many seconds of recent conversation suggestions read.
	Window int `toml:"window"`
	// AutoBrief is how many top suggestions are looked up automatically.
	AutoBrief int `toml:"auto_brief"`
	// LLMSuggestions turns the chat model's query suggestions on; off leaves
	// names and key phrases only, with no model load during meetings.
	LLMSuggestions bool `toml:"llm_suggestions"`
	// UpdateOnSave ingests a meeting as soon as Tomoe saves it.
	UpdateOnSave bool `toml:"update_on_save"`
}

type Sensitivity struct {
	// InternalMinAudience is the audience size from which an all-internal
	// meeting, mail or chat is labelled internal instead of confidential.
	// The default (15) means only broadcasts are internal; 3 restores the
	// first behaviour, where any internal group of three was internal.
	InternalMinAudience int `toml:"internal_min_audience"`
	// AudienceFromSpeakers limits a recording linked to a calendar event to
	// the owner and the people heard speaking; false restores the earlier
	// behaviour, where every invitee was in the audience.
	AudienceFromSpeakers bool `toml:"audience_from_speakers"`
}

// Owner identifies whose knowledge base this is. "You" in a Tomoe
// transcript resolves to this person, and they are in every item's audience.
type Owner struct {
	Email string `toml:"email"`
	Name  string `toml:"name"`
	// InternalDomains are email domains treated as inside the company.
	InternalDomains []string `toml:"internal_domains"`
}

type Embed struct {
	// Provider is "ollama", "hash" (deterministic, for tests) or "none".
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	URL      string `toml:"url"`
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:       filepath.Join(home, ".local", "share", "musubi"),
		TomoeSessions: filepath.Join(home, ".local", "share", "tomoe", "sessions"),
		Embed:         Embed{Provider: "ollama", Model: "nomic-embed-text", URL: "http://127.0.0.1:11434"},
		Sensitivity:   Sensitivity{InternalMinAudience: 15, AudienceFromSpeakers: true},
		Knowledge:     Knowledge{Judge: "decide", MinWorth: 0.2, Extract: "statements", StrictLinks: true, ImportKeepProposed: true, Grouping: "topics", Threads: Threads{Chunk: 70, Parallel: 4}, Points: Points{Think: true, Segment: 5, Parallel: 4}},
		LLM:           LLM{Provider: "ollama", Model: "qwen3:8b", URL: "http://127.0.0.1:11434", StartOllama: true},
		Live: Live{
			TomoeSocket: filepath.Join(home, ".local", "share", "tomoe", "live.sock"),
			FastLane:    true, TopicCards: true, SuggestEvery: 0, Window: 90, AutoBrief: 2, LLMSuggestions: true, UpdateOnSave: true,
		},
	}
}

// Path is the default config location.
func Path() string {
	// MUSUBI_CONFIG points the CLI and the app at another instance (e.g. an
	// experiment corpus) without touching the real one.
	if p := os.Getenv("MUSUBI_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "musubi", "config.toml")
}

// Load reads path over the defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

func (c Config) DBPath() string   { return filepath.Join(c.DataDir, "musubi.db") }
func (c Config) RawDir() string   { return filepath.Join(c.DataDir, "raw") }
func (c Config) InboxDir() string { return filepath.Join(c.DataDir, "inbox") }
func (c Config) Registry() string { return filepath.Join(c.DataDir, "registry.toml") }
