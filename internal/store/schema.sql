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
	speakers TEXT NOT NULL,
	text     TEXT NOT NULL,
	UNIQUE (item_id, seq)
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	text, speakers, content='chunks', content_rowid='id', tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
	INSERT INTO chunks_fts(rowid, text, speakers) VALUES (new.id, new.text, new.speakers);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, text, speakers) VALUES ('delete', old.id, old.text, old.speakers);
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
