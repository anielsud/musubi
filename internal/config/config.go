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
	// BluedotDir is a folder of Bluedot meeting transcripts (markdown);
	// empty disables the source. BluedotSince ("2006-01-02") skips older
	// files. A transcript already loaded through the inbox (the same file,
	// as source "teams") isn't read again.
	BluedotDir   string `toml:"bluedot_dir"`
	BluedotSince string `toml:"bluedot_since"`

	Owner       Owner       `toml:"owner"`
	Embed       Embed       `toml:"embed"`
	LLM         LLM         `toml:"llm"`
	Live        Live        `toml:"live"`
	Sensitivity Sensitivity `toml:"sensitivity"`
	Knowledge   Knowledge   `toml:"knowledge"`
	Ask         Ask         `toml:"ask"`
	Prep        Prep        `toml:"prep"`
	M365        M365        `toml:"m365"`
	MCP         MCP         `toml:"mcp"`
	Gateway     Gateway     `toml:"gateway"`
	Recorder    Recorder    `toml:"recorder"`
	Store       Store       `toml:"store"`
	Backup      Backup      `toml:"backup"`
	Diagnostics Diagnostics `toml:"diagnostics"`
	Updates     Updates     `toml:"updates"`
	// Providers are hosted endpoints set up once and named, for models to
	// use with `via` (Settings → AI providers).
	Providers map[string]Provider `toml:"providers"`
}

// Store is how the database keeps what it derives.
// M365 reads the owner's Microsoft 365 data through Microsoft Graph
// (internal/m365) once the owner has connected from Settings or
// `musubi m365 connect`. Until then nothing changes.
type M365 struct {
	// Enabled false never syncs, even when connected: the earlier behaviour.
	Enabled bool `toml:"enabled"`
	// Calendar writes the owner's events as inbox drops, so recordings get
	// their meeting's title and invitees and prep sees what's coming.
	Calendar bool `toml:"calendar"`
	// PastDays and FutureDays bound the calendar window each sync reads.
	PastDays   int `toml:"past_days"`
	FutureDays int `toml:"future_days"`
	// FirstDays is how far back the first sync reads (no calendar drops
	// yet), so a new owner starts with their recent meetings and their
	// Teams transcripts. 0: the first sync reads PastDays like the rest.
	FirstDays int `toml:"first_days"`
	// Reconnect opens the sign-in page again when a sync finds the sign-in
	// no longer works (not while Tomoe is recording). false: the app only
	// says so.
	Reconnect bool `toml:"reconnect"`
	// Transcripts writes the Teams transcripts of the owner's past meetings
	// (calendar window) as inbox meeting drops: the second copy of a meeting
	// Tomoe recorded, the only copy of one it missed. Needs Calendar.
	Transcripts bool `toml:"transcripts"`
	// TeamsNames names a recorded meeting's unnamed speakers ("Person 3")
	// from its Teams transcript, when the transcript's text lines up with
	// the recording and one Teams name clearly covers that speaker's speech
	// (session.AlignTeams). Names from the meeting window, voice profiles
	// or the owner are never replaced. false: Teams transcripts only fill
	// what the recording missed, as before.
	TeamsNames bool `toml:"teams_names"`
	// Unattended also keeps transcripts of meetings the owner was invited to
	// but didn't join (no Tomoe recording, not among the speakers), including
	// ones the owner was invited to only through a distribution list.
	Unattended bool `toml:"unattended"`
	// MeetingChat writes what was written in a meeting's chat during it.
	MeetingChat bool `toml:"meeting_chat"`
	// People looks up internal participants in the directory (job title,
	// department, office, manager): names and name variants for resolving
	// mentions, and profiles for the person views.
	People bool `toml:"people"`
	// Mail and Chats read the owner's email and Teams chats (not meeting
	// chats) as inbox drops, from MailDays / ChatDays back on the first sync
	// and incrementally after. Off by default: turning them on is the
	// owner's decision (volume, and extraction of written messages).
	// MailFocusedOnly leaves out what Outlook files as "Other".
	Mail            bool `toml:"mail"`
	MailDays        int  `toml:"mail_days"`
	MailFocusedOnly bool `toml:"mail_focused_only"`
	Chats           bool `toml:"chats"`
	ChatDays        int  `toml:"chat_days"`
	// TomoeContext writes calendar.json next to each Tomoe recording linked
	// to a calendar event (title, times, invitees), for Tomoe to show the
	// meeting's name and to know who could be speaking.
	TomoeContext bool `toml:"tomoe_context"`
}

// MCP configures `musubi mcp`, the local server that gives the owner's
// assistant (Claude Code) Musubi's knowledge. It reads in the owner's scope
// and labels every result rather than hiding it (docs/mcp-design.md).
type MCP struct {
	// Enabled false makes `musubi mcp` refuse to start.
	Enabled bool `toml:"enabled"`
	// Tools, when set, is an allow-list of tool names; empty offers all.
	Tools []string `toml:"tools"`
	// MaxShare caps what is returned at a share verdict (public, internal,
	// party-only); "owner-only", the default, returns everything.
	MaxShare string `toml:"max_share"`
	// Write offers the tools that record the owner's word (verdicts,
	// corrections, to-do marks); false leaves the server read-only.
	Write bool `toml:"write"`
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
	// AutoBrief drafts talking points for the owner's meetings in the rest
	// of the week, in the background, and drafts them again when what they
	// rest on changes (checked every 15 minutes, not during a live meeting).
	// false: talking points only when asked, the earlier behaviour.
	AutoBrief bool `toml:"auto_brief"`
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
	// NewerFirst reads that thread first, marked as the subject's latest
	// turn, rather than after the others. Off: on the 15 questions it
	// changed, fewer wrong statements (3 → 1) but fewer facts (34 → 31 of
	// 58), wins 4 to 5 (research log 2026-10-20).
	NewerFirst bool `toml:"newer_first"`
}

// Knowledge configures statement extraction.
type Knowledge struct {
	// NameMeetings names meetings that have no calendar event and only a
	// generic title ("Teams Meeting 2026-11-06 10:02") from their digest,
	// with the digest model; the name is shown wherever titles are. false:
	// such meetings keep their generic titles, as before.
	NameMeetings bool `toml:"name_meetings"`
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
	// neighbouring passages as context; see docs/research-log.md) or "lean"
	// (the measured meeting prompt, lean_ex_v2, on a hosted model: a brief,
	// then every item in 50-line parts; other items use "statements").
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
	// Lean configures extract = "lean".
	Lean Lean `toml:"lean"`
	// PreferSource decides which recording of a meeting recorded twice
	// (a Tomoe session and the meeting app's transcript, overlapping) becomes
	// knowledge, unless one is extracted already: the first source listed.
	PreferSource []string `toml:"prefer_source"`
	// Stitch fills the stretches a Tomoe recording missed (joined late,
	// left early, a recording split in two) from the meeting's Teams
	// transcript, before the meeting is first threaded; the Teams copy is
	// then a duplicate of the recording. StitchWaitHours holds a Teams
	// meeting's recording back from threading that long after it ends
	// while its transcript hasn't arrived; it is extracted and summarised
	// meanwhile, and again if stitched. Stitch false: the earlier
	// behaviour (each copy on its own, one kept when they start together).
	Stitch          bool    `toml:"stitch"`
	StitchWaitHours float64 `toml:"stitch_wait_hours"`
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
	// HoldUpgrades (with updates = "explicit") also keeps what an upgrade
	// would redo at a model's cost when nothing but the code or a model
	// changed: passages of extracted or summarised meetings re-cut by a new
	// chunker or translator (which would re-extract them), thread statuses
	// under a new status version, and meeting names under a new naming
	// version or model. They wait for Status → Work queue → Apply upgrades,
	// or `musubi knowledge --redo extract,statuses,names`. Content that
	// really changed is processed as before. false redoes them on the next
	// run, as before.
	HoldUpgrades bool `toml:"hold_upgrades"`
	// Label checks each statement for sensitive content about people (a
	// person's performance or exit, a candidate, pay, health, legal
	// matters, private life) with a local model, and raises its label so
	// scopes hide it where they should (knowledge.StatementSensitivity).
	Label Label `toml:"label"`
	// ProposeAliases refreshes the alias candidates after the knowledge
	// stages (knowledge.ProposeAliases): proposals only, for the owner to
	// confirm in Explore → Corrections. false proposes none.
	ProposeAliases bool `toml:"propose_aliases"`
	// ApplySpellings reads text with the owner's confirmed spelling rules
	// wherever it is shown or written from (thread items, Ask's context,
	// statuses), and refreshes vectors, search and statuses when a rule
	// changes. false: rules are used for matching only (lines, lookups,
	// questions, prep), as before.
	ApplySpellings bool `toml:"apply_spellings"`
	// OnUpdate runs the knowledge stages after the app's Update (new items
	// are extracted, linked, threaded and their threads' statuses
	// rewritten). false leaves them to `musubi knowledge`, as before.
	OnUpdate bool `toml:"on_update"`
	// AutoMax is the most new items an update after new data
	// (on_update) extracts and threads; a bigger batch waits for a run
	// someone starts, which shows its cost first. 0: no limit, as before.
	AutoMax int `toml:"auto_max"`
	// Status writes each thread's status (where it stands, what changed,
	// what is open) after threading, for the topic page, Ask and meeting
	// prep (knowledge.WriteStatus).
	Status Status `toml:"status"`
	// SkipFailedItems: an item whose every passage failed extraction (with
	// "statements") is logged, left unstamped so the next run tries it again,
	// and the run goes on to embedding, linking and threads for the rest.
	// The run still fails when no item in it extracted, since that is a
	// broken model or cache rather than a few bad replies. false: one such
	// item fails the run after extraction, as before, so nothing is linked
	// or threaded until it extracts (research-log.md, 2026-11-05).
	SkipFailedItems bool `toml:"skip_failed_items"`
	// Digest reads each meeting whole into a sectioned summary and its
	// to-dos (knowledge.Digester).
	Digest Digest `toml:"digest"`
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

// Digest configures meeting digests: a sectioned summary of each meeting
// and the to-dos said in it, from one whole-meeting read (research log,
// 2026-11-02).
type Digest struct {
	// Enabled: false makes none, and the to-do view reads commitment
	// statements, as before.
	Enabled bool `toml:"enabled"`
	// Model reads the meeting; provider "" uses the threads model. It reads
	// meeting text, so it must be local unless knowledge.allow_hosted. A
	// long meeting needs a long context (about 30k tokens for two hours).
	Model LLM `toml:"model"`
	// Parallel is how many meetings are read at once.
	Parallel int `toml:"parallel"`
	// ToDos makes the to-do view read the digest's to-dos (with their
	// owners, agreed-or-floated state and dates) instead of commitment
	// statements. Off when Enabled is.
	ToDos bool `toml:"todos"`
	// ToDosFallback lists the commitment statements of items with no
	// digest yet (not summarised, summary failed, chats) beside the
	// digests' to-dos; false lists the digests' to-dos only, as before.
	ToDosFallback bool `toml:"todos_fallback"`
	// MinTopicStatements is how many of a meeting's statements a thread
	// must hold to be listed as one of its topics (a thread holding fewer
	// is mostly not about the meeting: 1 statement, "not here" in the
	// measure; 3 to 5 split the rest).
	MinTopicStatements int `toml:"min_topic_statements"`
}

// ToDoSource is the to-do view's source (knowledge.ToDoRequest.Source):
// "" for commitment statements, "digest", or "digest-only".
func (d Digest) ToDoSource() string {
	switch {
	case !d.Enabled || !d.ToDos:
		return ""
	case d.ToDosFallback:
		return "digest"
	}
	return "digest-only"
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
	// Provider is "jev" (TypeSafe's API) or "clef" (Cloudflare's Clef, the
	// same questions and answers, on Workers AI: URL is the model's /ai/run
	// address, the key a Cloudflare or AI Gateway token).
	Provider string  `toml:"provider"`
	URL      string  `toml:"url"`
	Model    string  `toml:"model"`
	APIKey   string  `toml:"api_key"` // keychain:<service> or env:<VARIABLE>
	PriceIn  float64 `toml:"price_in"`
	// Headers are sent with every request (as for a hosted LLM).
	Headers map[string]string `toml:"headers"`
	// MinP for this classifier (0: 0.5, measured).
	MinP float64 `toml:"min_p"`
}

// Threads configures grouping = "threads".
type Threads struct {
	// Model does the threading; provider "" uses [llm]. Measured with
	// Qwen3.8-27B on llama-server (provider "openai").
	Model LLM `toml:"model"`
	// HoldOlder leaves an item older than more threaded items than Rewind
	// redoes (a backlog) unthreaded until everything is rethreaded in time
	// order (--redo threads), as threading is only right in time order.
	// false threads it now, out of order, as before (counted "backfilled").
	HoldOlder bool `toml:"hold_older"`
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

// Lean configures lean extraction (knowledge.extract = "lean").
type Lean struct {
	// Model is the extraction model; provider "" uses [llm]. Measured on
	// GLM-5.3-Flash on Baseten (provider "openai").
	Model LLM `toml:"model"`
	// Segment is how many lines one call covers, Context how many lines
	// either side it reads as context, Parallel how many calls at once.
	Segment  int `toml:"segment"`
	Context  int `toml:"context"`
	Parallel int `toml:"parallel"`
	// Description says what the meetings are ("the weekly executive
	// leadership meeting"); "" uses each meeting's title.
	Description string `toml:"description"`
	// Directory, if set, is a JSON file {"people": [[name, title, office]]}
	// shown to the model only for recognising who is meant; else the people
	// Musubi knows (calendars, earlier meetings) by name.
	Directory string `toml:"directory"`
}

// LLM is the chat model for answers, live query suggestions and briefs.
type LLM struct {
	// Provider is "ollama", "openai" (an OpenAI-compatible server such as
	// llama-server; URL without /v1), "anthropic" (an Anthropic Messages
	// endpoint such as the AI Gateway's /anthropic-shim; URL without /v1)
	// or "none" (Q&A then shows passages only, and live suggestions come
	// from names and key phrases alone).
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	URL      string `toml:"url"`
	// Via names a [providers.<name>] to take the provider (its format),
	// url, key and headers from, for whatever this section doesn't set.
	// Empty: set here, the earlier way.
	Via string `toml:"via"`
	// StartOllama lets the app start `ollama serve` if it isn't running.
	StartOllama bool `toml:"start_ollama"`
	// PullMissing ([llm] only) has the app download the local models the
	// dataset needs (this one and [embed]'s) when Ollama doesn't have
	// them. false: they are pulled by hand, as before.
	PullMissing bool `toml:"pull_missing"`
	// APIKey says where a hosted endpoint's key is kept: "keychain:<service>"
	// (macOS keychain) or "env:<VARIABLE>". Empty for local servers.
	APIKey string `toml:"api_key"`
	// Headers are sent with every request to a hosted endpoint, for one
	// that wants more than the key: the Optimizely AI Gateway charges a call
	// to its token's owner from cf-aig-metadata ({"token_id": ...}).
	Headers map[string]string `toml:"headers"`
	// ReasoningEffort is sent to hosted reasoning models ("low", "medium",
	// "high"); empty leaves the model's default. Calls may ask for their own
	// (threads ask for low on assign, as measured); for "anthropic" the
	// configured level wins, and empty is medium.
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
	// Fallback answers when this model fails (unreachable, refused, out of
	// retries): [<section>.fallback], set from Settings → Models. None:
	// the call fails, as before. Ask keeps its own [ask.fallback].
	Fallback *LLM `toml:"fallback"`
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
	// OwnerMode is how the model's queries are kept from naming the user.
	// "" or "drop": the prompt says who the user is and queries naming them
	// are dropped. "scrub": the user's name is rewritten as "You" in the
	// conversation, the sentence is left out, and the name is stripped from
	// any query that still has it.
	OwnerMode string `toml:"owner_mode"`
	// Lookups is what the model's queries search. "" or "statements":
	// every query finds its nearest statements and the relevance
	// classifier chooses one among all of them, or none; no summary. On
	// replays of two meetings it showed 23% useful against 15%, and 5.4
	// useful items per 10 minutes against 0.8 on one (research log
	// 2026-10-29). It needs live.relevance; without it, passages.
	// "passages": the first auto_brief queries each search passages, then
	// summary and gate, as before.
	Lookups string `toml:"lookups"`
	// LookupMaxNone: with lookups = "statements", the choice is shown only
	// while the classifier's probability of "none" is below this (0: 0.3).
	LookupMaxNone float64 `toml:"lookup_max_none"`
	// LLMSuggestions turns the chat model's query suggestions on; off leaves
	// names and key phrases only, with no model load during meetings.
	LLMSuggestions bool `toml:"llm_suggestions"`
	// UpdateOnSave ingests a meeting as soon as Tomoe saves it.
	UpdateOnSave bool `toml:"update_on_save"`
	// UpdateEvery (minutes) also updates on a timer, for sources that
	// change on their own schedule (a transcript folder). 0: only on save
	// and by hand, as before.
	UpdateEvery int `toml:"update_every"`
	// Focus shows what fits the conversation: each topic card's few lines
	// closest to the last 90 s, the line that raised it, at most two cards
	// open (the most recently mentioned, within three minutes) and the
	// rest as one line, old subjects marked. false: every card in full, as
	// before.
	Focus bool `toml:"focus"`
	// KeepTopics, with focus: topic cards pushed off the panel's few by
	// newer ones are kept for the rest of the meeting, at most this many,
	// collapsed ("what else should I raise"); raised again, they come back.
	// 0: dropped, as before.
	KeepTopics int `toml:"keep_topics"`
	// Relevance, with focus, chooses what takes the panel's places by how
	// well it fits the last 90 s, asked of a classifier (provider "jev",
	// or "decider": the [llm] model's yes/no) instead of by recency; MinP
	// is the bar (0: 0.5). On a graded meeting (research log 2026-10-21)
	// it showed 2.45 helpful items a moment against 1.0. Empty provider:
	// the newest, as before.
	Relevance Classifier `toml:"relevance"`
	// Pick, with focus and relevance's classifier, chooses the topic cards
	// from candidates: what the lookups, trigger terms and the talk itself
	// put forward over the last 3 minutes. The classifier picks at most two
	// that the conversation is about, or none, instead of each lookup
	// opening its nearest topic (research log 2026-10-28: 80–90% of cards
	// on topic against 12%).
	Pick Pick `toml:"pick"`
	// Cache keeps, for the meeting, the reads Live repeats (topic infos,
	// cards' evidence, thread statuses, entity graphs, statement vectors),
	// each under the scope it was read in, until the database changes.
	// false: read every time, as before.
	Cache bool `toml:"cache"`
	// SkipIdle skips a pick, or a model suggestion run, whose input (the
	// conversation it reads and, for a pick, its candidates) hasn't changed
	// since the last; the cadence stays. false: run every time, as before.
	SkipIdle bool `toml:"skip_idle"`
	// PrepCard shows the meeting's prep at the top of the panel's cards
	// while a meeting matched to a calendar event is under way: its
	// drafted talking points and what is open with the people in it, read
	// in the panel's scope; nothing is drafted unless asked. false: no
	// prep card, as before.
	PrepCard bool `toml:"prep_card"`
}

// Store tunes the database's connections (store.Options). Measured on
// the all-t4 dataset (766 MB), docs/research-log.md, 2026-11-12.
type Store struct {
	// CacheMB is each connection's page cache, in MiB. 0 (default):
	// SQLite's own (2 MB), as before; with mmap_mb on, 64 measured no
	// faster.
	CacheMB int `toml:"cache_mb"`
	// MmapMB maps up to this much of the database file for reads (default
	// 2048; the all-t4 file is 766 MB). It took the most mentioned
	// entity's graph from 42.7 s to 14.0 s. 0: off, as before.
	MmapMB int `toml:"mmap_mb"`
	// ReadConns is how many read-only connections serve the screens' and
	// Live's reads while the one writer writes (default 4: a read during
	// a 2.4 s backup took 78 ms instead of 2.3 s). 1: everything shares
	// the single connection, as before.
	ReadConns int `toml:"read_conns"`
	// VectorEncoding is how embedding vectors are written: "f16" (half
	// precision, half the space; measured 2026-11-12, docs/research-log.md)
	// or "f32" (full precision, the earlier behaviour). Both always read;
	// `musubi vectors encode` rewrites what is already stored.
	VectorEncoding string `toml:"vector_encoding"`
	// BackupBeforeUpgrade copies the database to backups/ in its data
	// folder before a newer build changes it (keeping the newest three;
	// docs/upgrades.md). false: upgrade in place without a copy, as before.
	BackupBeforeUpgrade bool `toml:"backup_before_upgrade"`
	// CheckCounts guards a redo (`musubi knowledge --redo`) and the app's
	// rethread: the kept tables (the owner's corrections, judgments,
	// aliases, thread edits, to-do marks, forget list) are copied first,
	// counts are reported before and after, and rows a kept table lost
	// are put back and the run fails. false: no copy or check, as before.
	CheckCounts bool `toml:"check_counts"`
}

// Backup is when snapshots of the whole dataset (internal/backup) are
// made on their own; "Back up now" works either way. false or 0 turns
// each off, as before they existed.
type Backup struct {
	// Daily: one snapshot a day while the app is open, the newest
	// KeepDaily kept.
	Daily     bool `toml:"daily"`
	KeepDaily int  `toml:"keep_daily"`
	// BeforeChanges: one snapshot before each step that deletes or
	// rewrites stored data in bulk (forget, a redo, a vector re-encode,
	// the work queue's runs, deleting a meeting, a speaker relabel), the
	// newest KeepBefore kept. A step whose snapshot fails doesn't run.
	BeforeChanges bool `toml:"before_changes"`
	KeepBefore    int  `toml:"keep_before"`
}

// Diagnostics: tools in the diagnostics panel.
// Updates: the app asks GitHub once a day whether a newer Musubi has been
// released and says so in its header. The request is anonymous and
// carries nothing about the user or their data. Only the DMG from the
// release workflow checks; a build from source never does, so it is never
// replaced by a release behind it.
type Updates struct {
	// Check: false never contacts GitHub (the behaviour before).
	Check bool `toml:"check"`
	// Install: the header's button downloads the new version, checks it
	// and replaces the app, then restarts it; only when asked, never by
	// itself. false: the button opens the release page instead.
	Install bool `toml:"install"`
	// Prereleases also offers release candidates (vX.Y.Z-rcN). On by
	// default while Musubi is pre-1.0: everyone on it runs candidates.
	Prereleases bool `toml:"prereleases"`
	// Repo is the public releases-only repository, owner/name.
	Repo string `toml:"repo"`
}

type Diagnostics struct {
	// Profiling offers "Record a 30 s profile" (a CPU profile and a
	// goroutine dump under the data folder's profiles/). It only runs when
	// pressed. false: no button.
	Profiling bool `toml:"profiling"`
	// Resources offers the "resources" view: CPU and memory of Musubi,
	// its web view, the recorder, Ollama and Tomoe, sampled every 2 s only
	// while the diagnostics panel is open. false: no view, never sampled.
	Resources bool `toml:"resources"`
}

type Pick struct {
	Enabled bool `toml:"enabled"`
	// MinP is the bar for a pick (0: 0.4).
	MinP float64 `toml:"min_p"`
	// Brief writes each picked card a headline (where it stands) and what
	// to raise about it in this meeting, and ranks the cards kept from
	// earlier by what is most worth raising. Provider "": the card's lines.
	// Hosted, it reads the meeting: needs knowledge.allow_hosted.
	Brief LLM `toml:"brief"`
	// KeepUnfit keeps a picked card the brief model finds the notes don't
	// cover (the conversation is about a customer, person or project they
	// never mention). Off, the card is taken down: a card for another
	// account had its brief written with this one's facts (research log
	// 2026-11-05).
	KeepUnfit bool `toml:"keep_unfit"`
	// FullWindow gives the brief the last 3 minutes as one. Off, they are
	// split where the card's subject was first put forward (less 15 s),
	// the earlier part marked as possibly another subject, so what was
	// said about the previous account isn't written into this card.
	FullWindow bool `toml:"full_window"`
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
		Prep:          Prep{QuietDays: 21, MarkPrivate: true, AutoBrief: true},
		MCP:           MCP{Enabled: true, MaxShare: "owner-only", Write: true},
		M365:          M365{Enabled: true, Calendar: true, PastDays: 30, FutureDays: 14, FirstDays: 42, Reconnect: true, TomoeContext: true, Transcripts: true, TeamsNames: true, MeetingChat: true, People: true, MailDays: 14, MailFocusedOnly: true, ChatDays: 14},
		Knowledge:     Knowledge{NameMeetings: true, Judge: "decide", MinWorth: 0, Extract: "lean", StrictLinks: true, ImportKeepProposed: true, Grouping: "threads", Threads: Threads{Chunk: 70, Parallel: 8, AuditFixes: true, Rewind: 3, Overlap: true, HoldOlder: true, ConsolidateWindow: 24, InsertOlder: true}, Points: Points{Think: true, Segment: 5, Parallel: 4}, Lean: Lean{Segment: 50, Context: 8, Parallel: 16}, PreferSource: []string{"tomoe", "bluedot", "teams"}, Stitch: true, StitchWaitHours: 2, WarmCache: false, Updates: "explicit", Label: Label{Enabled: true, MinP: 0.3}, Status: Status{Enabled: true, Parallel: 16}, Digest: Digest{Enabled: true, ToDos: true, ToDosFallback: true, Parallel: 4, MinTopicStatements: 3}, AllowHosted: true, OnUpdate: true, AutoMax: 5, ProposeAliases: true, ApplySpellings: true, HoldUpgrades: true},
		LLM:           LLM{Provider: "ollama", Model: "qwen3:8b", URL: "http://127.0.0.1:11434", StartOllama: true, PullMissing: true},
		Live: Live{
			TomoeSocket: filepath.Join(home, ".local", "share", "tomoe", "live.sock"),
			FastLane:    true, TopicCards: true, SuggestEvery: 0, Window: 90, AutoBrief: 2, LLMSuggestions: true, UpdateOnSave: true, Focus: true,
			Cache: true, SkipIdle: true, PrepCard: true, UpdateEvery: 30, KeepTopics: 24, Pick: Pick{Enabled: true, MinP: 0.4},
		},
		Store:       Store{MmapMB: 2048, ReadConns: 4, VectorEncoding: "f16", BackupBeforeUpgrade: true, CheckCounts: true},
		Backup:      Backup{Daily: true, KeepDaily: 7, BeforeChanges: true, KeepBefore: 5},
		Diagnostics: Diagnostics{Profiling: true, Resources: true},
		Updates:     Updates{Check: true, Install: true, Prereleases: true, Repo: "anielsud/musubi-releases"},
		Recorder:    Recorder{OfRecord: "tomoe", Watchdog: true, InstallAgent: true, Relabel: true, Data: filepath.Join(home, ".local", "share", "musubi", "recorder")},
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
	if err := c.applyProviders(); err != nil {
		return c, err
	}
	c.applyGateway()
	return c, nil
}

func (c Config) DBPath() string   { return filepath.Join(c.DataDir, "musubi.db") }
func (c Config) RawDir() string   { return filepath.Join(c.DataDir, "raw") }
func (c Config) InboxDir() string { return filepath.Join(c.DataDir, "inbox") }
func (c Config) Registry() string { return filepath.Join(c.DataDir, "registry.toml") }

// DirectoryPath is where the Microsoft 365 directory lookups are kept.
func (c Config) DirectoryPath() string { return filepath.Join(c.DataDir, "directory.json") }
