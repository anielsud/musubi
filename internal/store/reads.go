package store

// The read pool (Options.ReadConns, [store] read_conns): which methods read
// through s.rdb instead of the writer's s.db. With read_conns ≤ 1, s.rdb is
// s.db and nothing changes.
//
// A method goes to the pool only if it is a plain read: no write in it, no
// transaction, no temporary table, no connection state (total_changes,
// last_insert_rowid, PRAGMAs that differ per connection). Such a read is
// safe on another connection because, in WAL mode, a read that starts
// after a commit sees it; and none of them can depend on uncommitted
// writes, since the writer has one connection and a transaction holds it
// (a read of s.db from inside one would have waited forever). Pool
// connections are opened read-only, so a write routed here by mistake
// fails.
//
// Routed (2026-11-12):
//   - Live and the topic pages: EntityGraph (with entityVisible's
//     entityItems), FindEntities, TopicInfos, Topics, TopicEntities,
//     TopicEvents, Evidence's visibleStatements, visibleRelations,
//     claimKinds and judgmentsFor, statementVecsOf (StatementsNear),
//     ThreadStatuses, ThreadStatements, ThreadIndex, ThreadMembership,
//     VisibleThreadItems, Threads, the card index's CardRows,
//     ClaimCentroids, PersonNameParts and ClaimRelations.
//   - Search and sources: ids, hit, Item, ItemTitles, ChunkLabel,
//     ItemLabel, audience, Chunks, ChunkTitle, ItemLabels.
//   - The work queue: AllItems, StageStamp, GlobalStamp, ThreadedItems,
//     ThreadedSince, LatestThreaded, LinksFrom, WorkStamp.
//   - Person pages and meetings: EntityNames, AllAliases, EntityTopicRefs,
//     loadCommitments (to-dos), OwnerLinks, ThreadLabels, MeetingDigest,
//     DigestedItems, MeetingTopics, scanDigestTodos, Meetings, EventAt,
//     GivenTitles, PrepBriefFor.
//   - Status: Stats, KnowledgeCounts, AuditCounts.
//   - The owner's spelling rules, read with statements and statuses:
//     ConfirmedSpellings, ScopedSpellings.
//
// Kept on the writer: everything that writes or reads inside a write
// (UpsertItem, Judge, AddAlias, ConfirmAliasIn, TouchThread,
// DeleteEmptyThreads, backfillCodes, ...), migrations, BackupTo, the code
// indexes' loading (once per change, after their backfill), the pipeline's
// bulk reads (statements, Claims, StatementVecs, ...), the alias review
// screen's reads and thread edits. KnowledgeSignature reads its own
// connection (knowledge.go). TestReadsDontWaitForTheWriter holds the
// writer and checks Live's reads still answer.
