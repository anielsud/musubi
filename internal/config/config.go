// Package config loads Musubi's TOML config over DefaultConfig, so a key
// missing from the file keeps its default.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"time"
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
	Ask         Ask         `toml:"ask"`
	Prep        Prep        `toml:"prep"`
}

// Prep configures meeting preparation.
type Prep struct {
	// QuietDays: an open item with nothing said of it for this many days
	// is shown as gone quiet, after the live ones. 0 shows every open item
	// as open, the earlier behaviour.
	QuietDays int `toml:"quiet_days"`
	// MarkPrivate marks subjects about people's jobs, pay, health, legal
	// matters or private lives as for the owner alone, and lists them last
	// in a meeting with more than one other person. false: unmarked, in
	// score order, the earlier behaviour.
	MarkPrivate bool `toml:"mark_private"`
}

// QuietAfter is QuietDays as a duration.
func (p Prep) QuietAfter() time.Duration { return time.Duration(p.QuietDays) * 24 * time.Hour }

// Ask configures answering questions.
type Ask struct {
	// Model answers; provider "" uses [llm]. A hosted model reads only
	// what the cloud scope allows, unless knowledge.allow_hosted.
	Model LLM `toml:"model"`
	// Fallback answers when Model fails (unreachable, refused, rate limited
	// past its waits): a local model behind a hosted one. Provider "": no
	// fallback. Talking points (prep) use the same pair.
	Fallback LLM `toml:"fallback"`
	// Threads answers from the threads a question is about (their status
	// and current items) as well as from passages. false answers from
	// passages alone, the earlier behaviour.
	Threads bool `toml:"threads"`
	// SmallContext reads fewer items, passages and citations: answers start
	// sooner, slightly worse on the question set (off by default).
	SmallContext bool `toml:"small_context"`
	// FindNewer adds one later-active thread just past the margin (a
	// subject whose latest turn was threaded apart). Off by default: it
	// finds more of the reference's sources (263 of 383 against 249) but
	// answers didn't improve, since the model leans on the threads it is
	// given first (research log 2026-10-20).
	FindNewer bool `toml:"find_newer"`
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
	// WarmCache sends a meeting's first point segment, and an item's first
	// reconcile, before the rest: their prompts start alike, and a server
	// that caches prompt prefixes (hosted APIs; llama-server per slot)
	// reuses the start only once one call has finished with it. The
	// replies are the same; it costs one call's wait per meeting
	// (research-log.md, 2026-10-16). false sends all at once.
	WarmCache bool `toml:"warm_cache"`
	// Updates says what happens when a stage's version changes (its prompt,
	// model, rules or settings): "explicit" keeps what was done under the
	// earlier version, uses the new one for new items, and reports what is
	// older until `musubi knowledge --redo <stage>` asks for it; "auto"
	// (the earlier behaviour) redoes it all at once: every item re-extracted,
	// claims rebuilt, every item rethreaded in order. A changed input (a
	// meeting's passages or labels) is re-extracted either way.
	Updates string `toml:"updates"`
	// Label checks each statement for sensitive content about people (a
	// person's performance or exit, a candidate, pay, health, legal
	// matters, private life) with a local model, and raises its label so
	// scopes hide it where they should (knowledge.StatementSensitivity).
	Label Label `toml:"label"`
	// ProposeAliases refreshes the alias candidates after the knowledge
	// stages (knowledge.ProposeAliases): proposals only, for the owner to
	// confirm in Explore → Corrections. false proposes none.
	ProposeAliases bool `toml:"propose_aliases"`
	// OnUpdate runs the knowledge stages after the app's Update (new items
	// are extracted, linked, threaded and their threads' statuses
	// rewritten). false leaves them to `musubi knowledge`, as before.
	OnUpdate bool `toml:"on_update"`
	// Status writes each thread's status (where it stands, what changed,
	// what is open) after threading, for the topic page, Ask and meeting
	// prep (knowledge.WriteStatus).
	Status Status `toml:"status"`
	// AllowHosted lets points, threads and labels use a hosted model (the
	// owner's policy since 2026-10-16 allows the approved endpoints to read
	// meeting text). false keeps them local.
	AllowHosted bool `toml:"allow_hosted"`
}

// Status configures thread statuses.
type Status struct {
	// Enabled: false writes none (the topic page then shows the thread's
	// scope line, as before).
	Enabled bool `toml:"enabled"`
	// Model writes them; provider "" uses the threads model. It reads
	// meeting text, so it must be local unless knowledge.allow_hosted.
	Model LLM `toml:"model"`
	// Parallel is how many statuses are written at once.
	Parallel int `toml:"parallel"`
}

// Label configures statement sensitivity labelling.
type Label struct {
	// Enabled: false restores the earlier behaviour (only passage rules
	// and the extractor's own personal flag label statements).
	Enabled bool `toml:"enabled"`
	// Model is the local labeller; provider "" uses [llm]. Measured on
	// audited backfill items (research log, 2026-10-16): Qwen3.8-27B on
	// llama-server found more and misfired less than the 8B.
	Model LLM `toml:"model"`
	// MinP is the probability of "sensitive" at which a statement is
	// labelled.
	MinP float64 `toml:"min_p"`
	// Classifier, when set, labels with a typed classification model
	// instead of Model's one-token decision: provider "jev" (TypeSafe).
	// On September's audited sample (research log 2026-10-20) it found 86%
	// of sensitive statements against the 27B's 71%, at the same
	// precision, in seconds. It labels statements no labeller has checked;
	// those labelled earlier keep their labels unless relabelled with
	// musubi knowledge --redo label. Empty: Model, the earlier behaviour.
	Classifier Classifier `toml:"classifier"`
}

// Classifier configures a typed classification model.
type Classifier struct {
	Provider string  `toml:"provider"` // "jev"
	URL      string  `toml:"url"`
	Model    string  `toml:"model"`
	APIKey   string  `toml:"api_key"` // keychain:<service> or env:<VARIABLE>
	PriceIn  float64 `toml:"price_in"`
	// MinP for this classifier (0: 0.5, measured).
	MinP float64 `toml:"min_p"`
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
	// AuditFixes is ThreadsVersion t4 (knowledge.Threader.AuditFixes); false
	// keeps t3.
	AuditFixes bool `toml:"audit_fixes"`
	// ConsolidateWindow bounds consolidation to what a meeting could have
	// made outdated: a touched thread's new items and the current items
	// most like them, at most this many in all. 0 reviews up to 80 of its
	// current items each time, as before (the slowest calls of a run).
	ConsolidateWindow int `toml:"consolidate_window"`
	// Merged reconciles and consolidates a touched thread in one call
	// (experiment; threads version t4m). false: two calls.
	Merged bool `toml:"merged"`
	// ConsolidateEvery reviews a thread's current list every this many
	// times it is touched (and when its current set grows by half); 0 or
	// 1: every time.
	ConsolidateEvery int `toml:"consolidate_every"`
	// Overlap runs an item's consolidation while the next item is
	// threaded, waiting only where both touch a thread: on four ELT
	// meetings 8m40s against 11m28s serial, quality unchanged (research
	// log 2026-10-20). false: one item after another, as before.
	Overlap bool `toml:"overlap"`
	// InsertOlder threads new items older than ones already threaded by
	// inserting them (knowledge.Threader.Insert: judged against their
	// neighbours earlier and later, current derived), not after them in
	// time order. For backfills and late sources. false threads them
	// after, as before, and says --redo threads would put them in order.
	InsertOlder bool `toml:"insert_older"`
	// Rewind: when new items are older than at most this many items
	// already threaded, those few are taken out of their threads (no
	// model) and threaded again after them, so the batch is threaded in
	// time order instead of inserted. A batch that arrived a day late
	// costs a meeting or two again, not an insert per item: on the October
	// backfill one meeting threaded early sent 30 through insertion, whose
	// calls reason 2.7 times as long (research log 2026-10-20). 0: off.
	Rewind int `toml:"rewind"`
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
	// APIKey says where a hosted endpoint's key is kept: "keychain:<service>"
	// (macOS keychain) or "env:<VARIABLE>". Empty for local servers.
	APIKey string `toml:"api_key"`
	// ReasoningEffort is sent to hosted reasoning models ("low", "medium",
	// "high"); empty leaves the model's default. Calls may ask for their own
	// (threads ask for low on assign, as measured).
	ReasoningEffort string `toml:"reasoning_effort"`
	// PriceIn and PriceOut (dollars per million tokens) price hosted calls
	// for the spend ledger (MUSUBI_SPEND_LEDGER); 0 records tokens only.
	PriceIn  float64 `toml:"price_in"`
	PriceOut float64 `toml:"price_out"`
	// HedgeAfter (seconds, hosted only): a call not finished by then is
	// sent again and the first reply used, cutting the slow tail for
	// about a tenth more spend. 0 never does.
	HedgeAfter float64 `toml:"hedge_after"`
	// StallAfter (seconds, hosted only): a reply streaming slower than
	// StallRate tokens a second this long after it was sent is sent again,
	// the first reply used. Most slow calls are stalled streams, not long
	// ones: in the All rethread, calls over two minutes had a median of
	// 632 tokens at 4 a second, against 127 normally (research log
	// 2026-10-20). 0: 30 s; negative: off, the earlier behaviour.
	StallAfter float64 `toml:"stall_after"`
	// StallRate (tokens a second): 0 is 20.
	StallRate float64 `toml:"stall_rate"`
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
		Ask:           Ask{Threads: true},
		Prep:          Prep{QuietDays: 21, MarkPrivate: true},
		Knowledge:     Knowledge{Judge: "decide", MinWorth: 0.2, Extract: "statements", StrictLinks: true, ImportKeepProposed: true, Grouping: "topics", Threads: Threads{Chunk: 70, Parallel: 4, AuditFixes: true, Rewind: 3, Overlap: true}, Points: Points{Think: true, Segment: 5, Parallel: 4}, WarmCache: true, Updates: "explicit", Label: Label{Enabled: true, MinP: 0.3}, Status: Status{Enabled: true, Parallel: 4}, OnUpdate: true, ProposeAliases: true},
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
