-- Every derived row points back to its item (and so to its raw record),
-- which is what makes citations, reprocessing, scoping and forgetting work.

CREATE TABLE IF NOT EXISTS raw_manifest (
	source       TEXT NOT NULL,
	ref          TEXT NOT NULL,
	hash         TEXT NOT NULL,
	content_type TEXT NOT NULL,
	fetched_at   INTEGER NOT NULL,
	PRIMARY KEY (source, ref)
);

CREATE TABLE IF NOT EXISTS items (
	id           TEXT PRIMARY KEY,
	source       TEXT NOT NULL,
	ref          TEXT NOT NULL,
	raw_hash     TEXT NOT NULL,
	kind         TEXT NOT NULL,
	title        TEXT NOT NULL,
	occurred_at  INTEGER NOT NULL,
	ended_at     INTEGER NOT NULL,
	thread       TEXT NOT NULL,
	segments     TEXT NOT NULL,
	participants TEXT NOT NULL,
	native       TEXT NOT NULL,
	content_hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS items_time ON items(occurred_at);

-- A Tomoe recording linked to the calendar event it was ("occurred_as").
CREATE TABLE IF NOT EXISTS item_links (
	item_id  TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	rel      TEXT NOT NULL,
	other_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	score    REAL NOT NULL,
	PRIMARY KEY (item_id, rel, other_id)
);

-- Principals party to an item (lower-cased email or "name:<name>").
CREATE TABLE IF NOT EXISTS item_audience (
	item_id   TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	principal TEXT NOT NULL,
	PRIMARY KEY (item_id, principal)
);

-- chunk_seq -1 is the item's own label; other rows escalate one chunk.
-- A chunk with no item label is invisible to every scope (fail closed).
CREATE TABLE IF NOT EXISTS labels (
	item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	chunk_seq  INTEGER NOT NULL,
	level      INTEGER NOT NULL,
	categories INTEGER NOT NULL,
	origin     TEXT NOT NULL,
	confidence REAL NOT NULL,
	reason     TEXT NOT NULL,
	PRIMARY KEY (item_id, chunk_seq)
);

CREATE TABLE IF NOT EXISTS chunks (
	id       INTEGER PRIMARY KEY,
	item_id  TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	seq      INTEGER NOT NULL,
	seg_from INTEGER NOT NULL,
	seg_to   INTEGER NOT NULL,
	start_s  REAL NOT NULL,
	end_s    REAL NOT NULL,
	speakers  TEXT NOT NULL,
	title     TEXT NOT NULL,
	text      TEXT NOT NULL,
	text_hash TEXT NOT NULL,  -- anchors judgements across re-extraction
	UNIQUE (item_id, seq)
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	text, speakers, title, content='chunks', content_rowid='id', tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
	INSERT INTO chunks_fts(rowid, text, speakers, title) VALUES (new.id, new.text, new.speakers, new.title);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, text, speakers, title) VALUES ('delete', old.id, old.text, old.speakers, old.title);
END;

-- Vectors for several models can coexist, so models can be compared on the
-- same data. Search is exact (brute force) over the scope-filtered rows: at
-- tens of thousands of chunks that is milliseconds, and it means the scope
-- filter can never be bypassed by an approximate index.
CREATE TABLE IF NOT EXISTS chunk_vecs (
	chunk_id INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
	model    TEXT NOT NULL,
	vec      BLOB NOT NULL,
	PRIMARY KEY (chunk_id, model)
);

-- 1-bit codes of the vectors (internal/vindex): a nearest-neighbour
-- prefilter held in memory, re-ranked against the vectors. Written with
-- them; vectors stored before codes existed get theirs when first needed.
CREATE TABLE IF NOT EXISTS chunk_codes (
	chunk_id INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
	model    TEXT NOT NULL,
	code     BLOB NOT NULL,
	PRIMARY KEY (chunk_id, model)
);

-- origin: registry (curated), directory (from participants with email),
-- speaker (a name only Tomoe knows), domain (an external organisation).
-- level lets a curated entity's very existence be sensitive (a codename).
CREATE TABLE IF NOT EXISTS entities (
	id     TEXT PRIMARY KEY,
	type   TEXT NOT NULL,
	name   TEXT NOT NULL,
	origin TEXT NOT NULL,
	level  INTEGER NOT NULL DEFAULT 0
);

-- alias is normalised (lower case, punctuation folded to spaces).
-- kind: full, first, last, domain, term.
CREATE TABLE IF NOT EXISTS entity_aliases (
	alias     TEXT NOT NULL,
	entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	kind      TEXT NOT NULL,
	PRIMARY KEY (alias, entity_id)
);

-- People and organisations party to an item (attendee, speaker, from, ...).
CREATE TABLE IF NOT EXISTS item_entities (
	item_id   TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	role      TEXT NOT NULL,
	PRIMARY KEY (item_id, entity_id, role)
);
CREATE INDEX IF NOT EXISTS item_entities_entity ON item_entities(entity_id);

-- Mentions in text. entity_id is NULL when ambiguous; candidates lists the
-- possibilities.
CREATE TABLE IF NOT EXISTS mentions (
	id         INTEGER PRIMARY KEY,
	chunk_id   INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
	item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	entity_id  TEXT REFERENCES entities(id) ON DELETE CASCADE,
	surface    TEXT NOT NULL,
	seg        INTEGER NOT NULL,
	start      INTEGER NOT NULL,
	end        INTEGER NOT NULL,
	confidence REAL NOT NULL,
	candidates TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS mentions_entity ON mentions(entity_id);
CREATE INDEX IF NOT EXISTS mentions_item ON mentions(item_id);

CREATE TABLE IF NOT EXISTS stage_runs (
	stage      TEXT NOT NULL,
	item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	version    TEXT NOT NULL,
	input_hash TEXT NOT NULL,
	ran_at     INTEGER NOT NULL,
	PRIMARY KEY (stage, item_id)
);

CREATE TABLE IF NOT EXISTS global_runs (
	step       TEXT PRIMARY KEY,
	input_hash TEXT NOT NULL,
	ran_at     INTEGER NOT NULL
);

-- Scoped reads made for outbound purposes (replies, cloud calls).
CREATE TABLE IF NOT EXISTS audit (
	at     INTEGER NOT NULL,
	scope  TEXT NOT NULL,
	op     TEXT NOT NULL,
	detail TEXT NOT NULL
);

-- Records the owner asked to forget. Ingest skips them even though the
-- source (Tomoe, the mailbox) still has them.
CREATE TABLE IF NOT EXISTS forgotten (
	source TEXT NOT NULL,
	ref    TEXT NOT NULL,
	at     INTEGER NOT NULL,
	PRIMARY KEY (source, ref)
);

-- Knowledge layer (docs/architecture.md). Statements are derived from
-- chunks and go with them; claims, relations and topics are rebuilt from
-- statements.

CREATE TABLE IF NOT EXISTS statements (
	id         INTEGER PRIMARY KEY,
	item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	chunk_id   INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
	line       INTEGER NOT NULL,
	kind       TEXT NOT NULL,
	text       TEXT NOT NULL,
	holder     TEXT NOT NULL,          -- as written in the passage
	holder_id  TEXT,                   -- resolved entity, if any
	certainty  REAL NOT NULL,          -- 0..1, as the speaker expressed it
	said_at    INTEGER NOT NULL,       -- unix seconds
	value_type TEXT NOT NULL,          -- date, number, money, percent, duration, none
	value_raw  TEXT NOT NULL,          -- the words, as said
	value_lo   REAL NOT NULL,          -- normalised bounds (dates: unix seconds)
	value_hi   REAL NOT NULL,
	value_ok   INTEGER NOT NULL,       -- 1 if the words could be normalised
	quote      TEXT NOT NULL,          -- the words it was taken from, verbatim
	grounded   INTEGER NOT NULL,       -- 1 if the quote, figures and negation check out against the source
	issues     TEXT NOT NULL,          -- why not, if not
	worth      REAL,                   -- probability it is knowledge worth keeping (NULL: not scored)
	worth_version TEXT NOT NULL DEFAULT '',
	search_text TEXT NOT NULL DEFAULT '', -- text plus digits of spelled-out numbers
	-- Points (knowledge.extract = "points"); empty or 0 for statements.
	status     TEXT NOT NULL DEFAULT '',     -- decided, agreed, proposed, open, answered, rejected, stated
	who        TEXT NOT NULL DEFAULT '',     -- JSON array of the people it belongs to
	evidence   TEXT NOT NULL DEFAULT '',     -- JSON array of {line, quote, chunk_id}
	level      INTEGER NOT NULL DEFAULT 0,   -- the statement's own label, combined with its passage's on every read
	categories INTEGER NOT NULL DEFAULT 0,
	label_version TEXT NOT NULL DEFAULT ''  -- the sensitivity labeller that checked it
);
CREATE INDEX IF NOT EXISTS statements_item ON statements(item_id);
CREATE INDEX IF NOT EXISTS statements_chunk ON statements(chunk_id);

CREATE TABLE IF NOT EXISTS statement_vecs (
	statement_id INTEGER PRIMARY KEY REFERENCES statements(id) ON DELETE CASCADE,
	model        TEXT NOT NULL,
	vec          BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS claims (
	id         INTEGER PRIMARY KEY,
	kind       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	centroid   BLOB,
	topic_id   INTEGER
);

CREATE TABLE IF NOT EXISTS claim_codes (
	claim_id INTEGER PRIMARY KEY REFERENCES claims(id) ON DELETE CASCADE,
	code     BLOB NOT NULL
);

-- Every statement belongs to one claim.
CREATE TABLE IF NOT EXISTS statement_claims (
	statement_id INTEGER PRIMARY KEY REFERENCES statements(id) ON DELETE CASCADE,
	claim_id     INTEGER NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
	sim          REAL NOT NULL,
	how          TEXT NOT NULL  -- new, near-identical, judged
);
CREATE INDEX IF NOT EXISTS statement_claims_claim ON statement_claims(claim_id);

-- How a claim bears on another (from → to), established by a statement.
CREATE TABLE IF NOT EXISTS claim_relations (
	from_claim   INTEGER NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
	to_claim     INTEGER NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
	rel          TEXT NOT NULL,  -- contradicts, refines, supersedes, answers, completes, pending, repeats
	statement_id INTEGER NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	version      TEXT NOT NULL DEFAULT '',  -- threads: the rules version, the item being threaded, processing time
	made_in      TEXT NOT NULL DEFAULT '',
	made_at      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (from_claim, to_claim, rel)
);

CREATE TABLE IF NOT EXISTS topics (
	id          INTEGER PRIMARY KEY,
	label       TEXT NOT NULL,
	summary     TEXT NOT NULL,
	label_level INTEGER NOT NULL,  -- highest level of the statements the label was written from
	centroid    BLOB,
	size        INTEGER NOT NULL
);
-- Topics cross-link rather than nest: by shared customers, projects and
-- people ("same customer"), by meaning across different ones ("parallel":
-- a commit to one customer and a commit to another), and by being
-- discussed in the same items.
CREATE TABLE IF NOT EXISTS topic_relations (
	a        INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	b        INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	sim      REAL NOT NULL,
	shared   INTEGER NOT NULL,  -- items discussing both
	entities TEXT NOT NULL,     -- shared organisation/project entity ids, comma-separated
	PRIMARY KEY (a, b)
);
CREATE TABLE IF NOT EXISTS topic_entities (
	topic_id  INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	entity_id TEXT NOT NULL,
	claims    INTEGER NOT NULL,
	PRIMARY KEY (topic_id, entity_id)
);
-- How topics changed between rebuilds: born, grew, shrank, merged, split,
-- dissolved.
CREATE TABLE IF NOT EXISTS topic_events (
	at       INTEGER NOT NULL,
	topic_id INTEGER NOT NULL,
	event    TEXT NOT NULL,
	other    INTEGER NOT NULL,  -- the other topic in a merge or split, else 0
	detail   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS topic_events_topic ON topic_events(topic_id);

-- The owner's verdicts. Not derived: kept across rebuilds, keyed by the
-- statement judged (item and text), and re-attached to its claim.
-- Anchored to the source (chunk text hash and line) with the text as a
-- fallback, so a verdict survives re-extraction rewording the statement.
CREATE TABLE IF NOT EXISTS judgments (
	item_id    TEXT NOT NULL,
	text       TEXT NOT NULL,
	verdict    TEXT NOT NULL,  -- verified, disputed, false
	note       TEXT NOT NULL,
	at         INTEGER NOT NULL,
	chunk_hash TEXT NOT NULL DEFAULT '',
	line       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (item_id, text)
);

-- Threads (knowledge.grouping = "threads"; docs/linking-benchmark.md). A
-- thread is a topics row; thread_items says which statements it holds (a
-- statement can be in two) and which state its current position.
-- thread_runs records which items have been threaded, under which rules.
CREATE TABLE IF NOT EXISTS thread_items (
	topic_id     INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	statement_id INTEGER NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	current      INTEGER NOT NULL DEFAULT 1,
	-- Provenance: the threading version, the item being threaded and the
	-- processing time when added; when, during what and why it stopped
	-- being current (reconcile, consolidate, cross, lapse).
	version      TEXT NOT NULL DEFAULT '',
	added_in     TEXT NOT NULL DEFAULT '',
	added_at     INTEGER NOT NULL DEFAULT 0,
	retired_in   TEXT NOT NULL DEFAULT '',
	retired_at   INTEGER NOT NULL DEFAULT 0,
	retired_why  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (topic_id, statement_id)
);
CREATE INDEX IF NOT EXISTS thread_items_statement ON thread_items(statement_id);
CREATE TABLE IF NOT EXISTS thread_runs (
	item_id TEXT PRIMARY KEY,
	stamp   TEXT NOT NULL
);

-- Relations between statements, as threading judged them (later
-- replaces, completes, answers, repeats … earlier), with provenance.
-- claim_relations holds the same between claims for the views; two
-- statements of one claim have no claim relation, so "current" is derived
-- from these (docs/incremental-design.md).
CREATE TABLE IF NOT EXISTS statement_relations (
	later    INTEGER NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	earlier  INTEGER NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	rel      TEXT NOT NULL,
	version  TEXT NOT NULL DEFAULT '',
	made_in  TEXT NOT NULL DEFAULT '',
	made_at  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (later, earlier, rel)
);
CREATE INDEX IF NOT EXISTS statement_relations_earlier ON statement_relations(earlier);

-- Corrections to statements (the owner's, or an applied alias): what
-- changed, from what, when, and by whom. The statement holds the
-- corrected value; this keeps the history and the provenance.
CREATE TABLE IF NOT EXISTS statement_corrections (
	id           INTEGER PRIMARY KEY,
	statement_id INTEGER NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	field        TEXT NOT NULL,  -- text, who
	old          TEXT NOT NULL,
	new          TEXT NOT NULL,
	by           TEXT NOT NULL,  -- owner, alias:<id>
	at           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS statement_corrections_statement ON statement_corrections(statement_id);

-- Learned aliases (docs/incremental-design.md, "Entities and aliases"): another
-- way a thing is written or heard. kind: spelling (one thing, everywhere),
-- short (a short form, resolved only in scope), identity (two names, one
-- person: needs evidence and the owner). Proposed by the corpus, decided
-- by the owner; nothing rewrites the source.
CREATE TABLE IF NOT EXISTS learned_aliases (
	id        INTEGER PRIMARY KEY,
	keep      TEXT NOT NULL,   -- the right form
	variant   TEXT NOT NULL,   -- the other form
	kind      TEXT NOT NULL,   -- spelling, short, identity
	scope     TEXT NOT NULL DEFAULT '',  -- JSON: items, attendees, period ('' = everywhere)
	status    TEXT NOT NULL,   -- proposed, confirmed, rejected
	evidence  TEXT NOT NULL DEFAULT '',  -- JSON, as proposed
	score     REAL NOT NULL DEFAULT 0,
	by        TEXT NOT NULL DEFAULT '',
	at        INTEGER NOT NULL,
	UNIQUE (keep, variant, kind)
);

-- A thread's written status (knowledge.status): where it stands, what
-- changed, what is open. Variant "full" is written from every item;
-- "open" only from items a shared screen may show, and is written only
-- when the two differ. level and categories are the most sensitive of the
-- items it was written from (a read checks them like any label).
CREATE TABLE IF NOT EXISTS thread_status (
	topic_id   INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
	variant    TEXT NOT NULL,
	version    TEXT NOT NULL,
	input_hash TEXT NOT NULL,
	body       TEXT NOT NULL,  -- JSON (knowledge.Status)
	level      INTEGER NOT NULL,
	categories INTEGER NOT NULL,
	as_of      INTEGER NOT NULL,  -- the latest item it was written from
	PRIMARY KEY (topic_id, variant)
);
