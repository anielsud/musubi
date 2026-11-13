# Musubi architecture

Musubi (結び, "tying together") is a local-first personal knowledge base. It
ingests meetings (from Musubi's recorder or Tomoe, and Teams transcripts),
calendar, email, chat and CRM records, distills them into people,
accounts, topics, decisions and commitments linked to their sources, and
serves that knowledge two ways:

- **Live:** a side panel that surfaces relevant cards while a meeting is in
  progress, driven by the live transcript of the recorder of record
  (Musubi's recorder or Tomoe).
- **Explore:** a graph, timeline, topic map and trend view across time, people,
  business area, customer and topic.

**Atoms first.** Features read the knowledge layer (statements, claims,
threads and their statuses) and show passages as the evidence behind it,
not as the answer. Measured each time it was compared: claims beat passages
as lookup results (on-subject 85% against 69%, 2026-10-14); Ask from threads
beat passages 22–5 (2026-10-17); for live lookups statements were rated
useful five times as often as passages, and on replay showed 23% useful
against 15% (2026-10-29), so they are now Live's default. Where a feature
still starts from passages (the Search tab and `musubi search`, Ask's extra
passages, the retrieval eval), that is a gap to close, not the design.

Status (2026-10-21; the summary is in [overview.md](overview.md)):
- **Built since 2026-10-16:** thread statuses; Ask from threads; meeting
  prep; insert, forget and correct as local operations on the thread
  graph, with the owner's merges, splits and corrections kept to the side;
  correction rules for names and terms; the typed statement labeller;
  Live focus and relevance; the stepped replay.
- **Built:**
  - ingest (Tomoe sessions, inbox drops) and calendar linking;
  - classify / chunk / embed / resolve, with scoped search and graph;
  - the knowledge layer: statements or points, claims and belief, topics,
    and threads as the alternative grouping;
  - topic cards in the live panel;
  - the desktop app (live lane, Q&A, explorer);
  - eval harnesses for retrieval and leaks, knowledge, extraction
    ([insights-eval.md](insights-eval.md)) and linking
    ([linking-benchmark.md](linking-benchmark.md)).
- **Design only:** trends, signals, email and chat connectors (beyond inbox
  drops), and everything in [scaling.md](scaling.md) past today's
  measurements.
- **Built since 2026-10-21** (to 2026-11-12): Musubi's own meeting
  recorder (`cmd/musubi-recorder`, `internal/recorder/`) and its screens in
  the app ("Desktop app" below); Microsoft 365 through Graph
  (`internal/m365`: calendar, Teams transcripts, meeting chats, directory;
  mail and Teams chats, off by default; docs/teams-design.md); `musubi
  mcp` (docs/mcp-design.md); the store's read pool, memory-mapped reads and
  float16 vectors ("Storage").

Findings: [research-log.md](research-log.md).

## Principles

1. **Local first.** Storage and default processing run on this machine. Cloud
   providers are pluggable and opt-in per source (see Sensitivity).
2. **Raw is immutable; everything else is rebuildable.** Every input is kept
   exactly as it arrived. All derived data can be regenerated from raw, so
   better extractors and models are applied by re-running, not migrating.
3. **Every derived row knows where it came from.** Provenance makes citations,
   reprocessing, deletion ("forget this thread") and access control possible.
4. **Sensitivity is part of the data model, and every read is scoped.** There
   is no unscoped query API.
5. **Measure before changing defaults.** Replay past meetings and score what the
   panel would have shown; record findings in `docs/`.
6. **New behavior gets a config switch** that restores the previous behavior.
7. **Private and proprietary.** No GPL/AGPL/LGPL dependencies; no code copied
   from Tomoe (GPLv3), except code the owner wrote, brought into
   `internal/recorder/` and recorded in
   [recorder-provenance.md](recorder-provenance.md). Integration with Tomoe
   is otherwise through its files and its live stream only.

## Flow

```
 sources                   cold lane (batch, re-runnable)                 hot lane (live)
 ───────                   ──────────────────────────────                 ───────────────
 meetings ───────┐                                                            live feed
 calendar        │  ┌───────────┐  ┌───────────┐  ┌────────────────────┐        │
 email           ├─▶│ raw store │─▶│ normalize │─▶│ derive stages       │        ▼
 Teams chat      │  │ immutable │  │ → items   │  │ classify → chunk →  │   spotter (names,
 Salesforce      │  └───────────┘  └───────────┘  │ embed → mentions →  │   terms) → scoped
 others ─────────┘                                │ resolve → facts →   │   retrieval → cards
                                                  │ edges               │        │
                                                  └─────────┬──────────┘        ▼
                                                            ▼               side panel
                                        periodic analysis: topics, clusters,
                                        trends, signals, centrality
```

### 1. Sources and the raw store

A source connector (`internal/source/<name>`) fetches new data since its last
cursor and writes it to the raw store unchanged: content-addressed blob
(`raw/<sha256>`) plus a manifest row (source, source ref, fetched at, cursor,
content type). Connectors do no interpretation.

Planned connectors, in order:

| Connector | Access |
|---|---|
| `tomoe` | reads `*/session.json` in the sessions folder of the recorder of record: Tomoe's (`tomoe_sessions`, default) or, with `[recorder] of_record = "musubi"`, Musubi's recorder's (`[recorder] data`/sessions), then only sessions created after `[recorder] since` |
| `calendar` | ICS or Graph; gives meetings their titles and attendees |
| `mcpdrop` | an inbox directory that scheduled Claude Code jobs fill from the approved M365 / Salesforce / Zoom connectors, as JSON |
| `graph` | direct Microsoft Graph (mail, chat), once an Entra app registration is approved; until then `internal/m365` signs in as Microsoft's already-consented Graph Command Line Tools client and writes calendar events, Teams transcripts, meeting chats and (off by default) mail and Teams chats into the inbox, and looks up internal participants in the directory: docs/teams-design.md |

### 2. Items

A per-source translator turns raw records into **items**, the one shape all
later stages consume:

```
item: id, source, source_ref, raw_hash, kind (utterance|email|chat|event|record),
      occurred_at, thread, participants[], text, metadata{}, native_labels{}
```

`native_labels` keeps any sensitivity marking the source already carries
(Purview label, calendar "private" flag, Salesforce record type).

Adding a source never touches stages beyond the translator.

### 3. Derive stages

Each stage is a named, versioned processor. Every row it writes is stamped with
`(stage, stage_version, input_hash)`. A job queue in the database runs whatever
is stale; bumping a stage's version reprocesses exactly what it produced.

**Versions in the knowledge layer are explicit** (`knowledge.updates`,
default "explicit"). A version is everything that shapes a stage's
output: its prompt version, model, rules in code and settings.
- **Extraction** records per item both its version and a hash of its
  input alone (passages, labels, the meeting's known facts). A changed
  input is always re-extracted. A new version is used for new items;
  items done under an earlier one are kept and reported, and redone only
  with `musubi knowledge --redo extract` (`--newest N` for the most
  recent first).
- **Claims** (linking rules) and **threads** (`ThreadsVersion`, model,
  linking rules) work the same way: new statements are linked and new
  items threaded under the current rules; earlier claims and threads are
  kept and reported until `--redo links` or `--redo threads`. In thread
  mode a claims rebuild also rethreads, because threading's relations
  live on claims.
- **Reporting:** every run names what is on an earlier version and the
  command that updates it; `--plan` says what a run would do without
  doing it.
- **Re-extraction requeues threading:** a re-extracted item is threaded
  again, after the items already threaded. Before this, its statements
  silently left their threads.
- **Upgrades that would cost model calls wait too**
  (`knowledge.hold_upgrades`, default on; `pipeline/upgrades.go`): a
  chunk stamp, status stamp or name input that is stale while what it
  was made from is not (the item's raw record or content, recorded as
  `chunk-source`; a status hash recomputed under the version that wrote
  it; a name's text hash kept beside its input) is kept and counted, and
  the passages keep their statements, since re-cutting them would delete
  them. `--redo extract` re-cuts and re-extracts them, `--redo
  statuses`, `--redo names` the rest; the work queue shows them with an
  estimate (**Apply upgrades**).
- **Old behaviour:** `knowledge.updates = "auto"` redoes everything when
  a version changes; `hold_upgrades = false` only the upgrades above.
- **Speaker relabels** (`[recorder] relabel`; docs/recorder-screens.md,
  "Renaming a voice profile renames its speakers"): a corrected speaker
  label is written into a meeting's stored copies (item, passages,
  statements, digest, thread prose and statuses) by
  `pipeline.Runner.RelabelSpeaker` and `store.RelabelSpeaker`, in one
  transaction and with no model calls, when it changes nothing but labels
  and passage boundaries. The new chunk stamp is written and the one it
  replaces kept in the `chunk-relabelled` stage row; extraction and
  digests whose stamp (or, kept from an earlier version, whose input) was
  made from a kept chunk stamp are restamped, not run. Changed passages
  and statements are embedded again locally; the registry, links,
  classify and resolve then re-run as on any import of a new name. The
  next import of the rewritten session translates to the same
  `content_hash` and changes nothing. `speaker_relabels` keeps each
  field's before and after, for undo.

| Stage | Output |
|---|---|
| `classify` | sensitivity label for each item (see below) |
| `chunk` | retrieval passages (meeting turns grouped by topic shift; email bodies without quoted history) |
| `embed` | passage vectors (local ONNX embedding model by default) |
| `mentions` | spans naming people, organisations, products, projects, terms |
| `resolve` | mentions → canonical entities |
| `facts` | decisions, commitments (owner, due), claims; each with valid time and source spans |
| `edges` | typed, weighted, timestamped links between items, entities, facts and topics |

**Resolution is seeded from registries** (`internal/registry`): people from
the Entra directory (email is the canonical ID), customers from Salesforce
accounts, business areas from the org tree. Tomoe speaker names resolve to the
same people. Matching against known lists is far more reliable than discovering
entities from scratch; unmatched mentions become provisional entities for
review.

### 4. Periodic analysis

Reads derived data; never edits it.

- **Topics:** cluster claims (see "Knowledge layer"), label clusters, and track
  topic identity across runs so a topic keeps its ID as it drifts.
- **Trends:** weekly counts per entity and topic; burst detection; newly
  appearing entities.
- **Signals:** overdue commitments, customers going quiet or spiking, topics
  spreading across business areas.
- **Importance:** recency-weighted centrality on the graph.

**Calendar linking.** A Tomoe recording is linked to the calendar event
that contains its start (15 minutes' slack) and overlaps at least 30% of the
shorter of the two. All-day, longer-than-4-hour, free and cancelled entries
are never linked: a recording linked to a focus block would inherit the
block's tiny attendee list and look less sensitive than it is. The linked
event gives the meeting its title and attendees.

**Ambiguous names.** When a first name fits several people, the one who has
shared the most meetings with this item's participants wins, if the evidence
is clear (at least two shared meetings, twice the runner-up; the owner is left
out of the count). Otherwise the mention stays ambiguous with its candidates.

### 5. Hot lane

The live path never writes. Tomoe streams lines over an opt-in local socket (a
small Tomoe PR, off by default; Musubi's recorder serves the same feed:
tomoe-live-feed.md). A spotter matches names and terms against the
registry and entity tables, runs scoped retrieval, and ranks cards (who this is,
recent decisions, open commitments, related threads). Slow enrichment, if any,
arrives after the cards. After the meeting, the recorder's saved session
enters the cold lane like any other source.

### Desktop app (as built)

`cmd/musubi-gui` is a Wails app (`internal/gui` backend, React frontend in
`frontend/`), installed with `make install-gui-mac`.

- **Distribution** (2026-11-12): `make dmg` (and the `release` workflow)
  packs the recorder inside the app (`Contents/Helpers/Musubi
  Recorder.app`) with its LaunchAgent template. A missing LaunchAgent is
  written by the app (`[recorder] install_agent`), and the recorder
  downloads its required models when it starts without them
  (`[recorder.transcription] download_missing`). A first launch with no
  config file and no Tomoe folder writes a config with Musubi's recorder
  of record, Tomoe off and its live feed on (`internal/gui/firstrun.go`).

- **Phone** ([phone.md](phone.md), part 2; 2026-11-13): with `[phone]
  enabled` (default false; `port`, default 47615) the app runs
  `internal/phone`, an HTTPS server (Go's crypto/tls, a self-signed ECDSA
  P-256 certificate kept in `<data_dir>/phone/`, key 0600, pinned by the
  phone) that closes connections from non-private addresses at accept.
  Settings → Phone turns it on, opens a pairing window (8-digit code, 5
  minutes, single use, closed by 5 wrong codes) and lists and revokes
  devices; only each token's SHA-256 is stored (`devices.json`). Paired
  phones fetch the agenda (`phone.BuildAgenda`: calendar events in the
  owner's scope with their saved briefs and labels; a request starts the
  weekly drafting's path, `draftBriefsUntil`, for briefs missing in its
  window, one pass at a time and not during a live meeting) and upload
  recordings as ADTS chunks checked against their SHA-256. `complete`
  checks every chunk against the phone's `meta.json`, joins the whole
  ADTS frames into the recorder's inbox (`<recorder data>/inbox/<rid>.aac`)
  and asks the recorder over `control.sock` to import it (op `import`);
  chunks are deleted once the recorder reports the session saved.
  The recorder (`recording/importer.go`) runs one import at a time as
  `musubi-recorder import --here` in a child process, never while a
  meeting records (a meeting that starts stops the child; the import
  waits and starts over), and feeds `saved` so the app takes it in. The
  import makes a mic-only session (`platform: "phone"`, `event_id` when
  the phone named the event) with replay's offline pipeline and the
  current config, then the post-meeting diarization pass; the owner's
  voice tells them from others on the mic and learns, as in a recording.
  The tomoe translator carries `event_id` into `Native`, and
  `linkMeetings` links such a meeting to that event when Musubi has it
  (any event not cancelled), before matching by time.

- **Recorder** ([recorder-screens.md](recorder-screens.md)): `[recorder]`
  `of_record` is `"tomoe"` (default: meetings from `tomoe_sessions`, Live
  from `[live] tomoe_socket`, as before) or `"musubi"` (both from the
  recorder's folder, `[recorder] data`, default
  `~/.local/share/musubi/recorder`: `sessions/`, `live.sock`,
  `control.sock`). Switching to `"musubi"` in Settings sets `since`;
  ingest then takes only recorder sessions created after it, since
  earlier ones shadowed meetings Tomoe already gave. Files (audio, looks)
  of meetings imported from Tomoe before the switch are still read from
  Tomoe's folder (`Config.SessionDirs`), unless `[recorder] tomoe =
  "off"`: then only the recorder's folder, into which `musubi-recorder
  migrate-from-tomoe` copies Tomoe's sessions, voices, models and
  settings (recorder-screens.md, "Standalone"); the recorder then reads
  nothing of Tomoe's. After the switch, calendar context is written into
  the recorder's sessions only (`Config.ContextSessionsDir`). The app always follows the
  recorder's live feed for its `status`, `audio`, `diag` and `look`
  messages (re-emitted as `recorder:*` events) and asks it everything
  else over `control.sock` (`internal/recorder/control`): start, stop,
  sources, tools and fixes, voices, speaker and voice edits, looks,
  clips and tiles (served to the webview under `/recorder-media/`), and
  delete. Musubi never writes the recorder's folder. Speaker and voice
  edits and delete are offered only with `of_record = "musubi"`. A speaker
  edit on a saved meeting changes its `session.json`, so an update is
  queued and that meeting is re-extracted; delete removes the recording
  through the recorder, then forgets the item (`knowledge.ForgetItem`:
  its statements leave their threads, and it is never imported again).
  `[recorder.audio]`, `[recorder.transcription]` and `[recorder.meeting]`
  override the keys of the same tables in Tomoe's config (with Tomoe
  off, the recorder's built-in defaults) for the recorder; Settings → Recording writes them and asks the recorder to
  reload (`reload_config`).
- **Voice samples**: a voice profile keeps per meeting only the sum of
  its fingerprints and one clip, so `profile_samples` works the rest out
  again from each saved meeting (`diarization.gob`, no model run): the
  windows learning picked for the profile (`diarize.SavedLearning`, what
  `LearnSaved` teaches; each `voices.Learned` now carries its `Parts`,
  fingerprint plus time), grouped into samples where the pauses are at
  most 1.5 s. Each sample is scored against the profile's learnt voice
  and against the meeting's other samples, and each meeting's sample
  means are clustered as a meeting's fingerprints are, at the profile
  threshold, leaving out samples under `MinLearn` fingerprints (specks of
  crosstalk added up to a false second voice); a cluster counts as a
  voice only with at least `MinLearn` fingerprints and 5% of the meeting's (a person's tail of odd seconds
  stays under that: on 2026-11-12 one meeting room's profile counted 4
  voices, matching the 4 speakers the diarizer heard under its tile, and
  a single person's 10 meetings 1 each). Results are cached per profile
  and meeting in the recorder until the meeting's voice, its session or
  its fingerprints change. "Not them" on a sample (`exclude_sample`)
  takes its fingerprints out of the meeting's voice, records the span
  on it (`Heard.ExcludedSpans`) and keeps it as a returned sample in the
  voices file (`voices/returned.go`), which "Who's this?" asks about;
  learning leaves both out whenever the meeting is learnt again, also
  after the meeting's contribution was dropped. Naming a returned sample
  (`name_returned_sample`) adds its fingerprints to a profile of that
  name (`Heard.Assigned`, kept through relearns); "Can't tell"
  (`skip_returned_sample`) stops asking. "Junk" (`junk_sample`) keeps
  the span per meeting in the voices file, and no profile learns from
  it. `include_sample` puts either back. `split_label_by_voice` gives each
  line of one label in a saved session to the candidate profile its own
  fingerprints match by `Margin`, logged per line in `relabels.jsonl`
  (recorder-screens.md, "Not them and Junk on a sample; split a speaker
  by voice"). The
  Voices card (`voicesamples.tsx`) lists per meeting the samples under
  85% like the profile, the rest behind "+N more that fit well". A
  meeting learnt live, from name reads the saved meeting doesn't keep,
  can rebuild a slightly different set (shown as "N of M fingerprints").
- **Voice learning rules**: tile learning skips windows where the
  transcript names someone else; learning a whole meeting
  (`Store.LearnMeeting`) replaces its automatic contributions; the voices
  file records the `voices.LearnRules` its profiles were built under, and
  the recorder relearns every saved meeting once, in the background
  between recordings, when that is older (`recording/relearn.go`;
  recorder-screens.md, "Voice learning rules and automatic relearn").
- **Voices review index**: the recorder gathers the Voices page's review
  lists (who's this, 90 days, plus any voice taken out of a profile with
  "Not them"; named by voice and window or voice, 14 days) once in the
  background at start and keeps them in memory, reading a session again
  when it saves it or its files change on disk; the Voices op reads the
  index (`reviewindex.go`; recorder-screens.md, "Voice profile edits
  after samples"). "Not them" on a profile's meeting
  (`return_voice_meeting`) takes the meeting out of the profile and puts
  its profile-named speakers back to unnamed through the cheap relabel,
  checked first with `pipeline.RelabelRequest.Check`.
- **Models and providers** (Settings): AI providers lists the AI
  Gateway, custom providers (`[providers.*]`), Ollama and **ONNX (this
  Mac)**: the recorder's models (transcription, live text, speaker
  voices, speech detection, segmentation, text reading), run in its
  process by sherpa-onnx and ONNX Runtime; ONNX is ready when the
  recorder answers. Settings → Models lists every source's models in
  three sections (in use, recording models, everything else) with
  search, source filter and sort; the recorder's come from its `models`
  op, and a missing one is downloaded with `download_model`, into the
  recorder's own folder (`[recorder] data`/models). The recorder looks
  for models there first and then in Tomoe's folder
  (`config.ModelDirs`; not with `[recorder] tomoe = "off"`), so nothing
  is downloaded twice, and never writes Tomoe's. Settings → Processing chooses the transcription and speaker
  voice models (`[recorder.transcription] model`,
  `[recorder.meeting] speaker_model`, read when the recorder starts);
  they are no longer in Settings → Recording.
- **Live** (`internal/live`): follows the recorder of record's feed ([tomoe-live-feed.md](tomoe-live-feed.md)),
  keeps the meeting so far, and every `suggest_every` seconds of new speech
  turns the last `window` seconds into suggestions from three sources:
  entities named (unambiguous aliases only), phrases that are rare in the
  knowledge base but present in it, and the local chat model. The top
  `auto_brief` suggestions are looked up (the meeting itself excluded) and
  summarised in two cited sentences; the model answers NONE when the results
  aren't about the topic and the brief is dropped. The title comes from the
  calendar event under way. When the recorder saves the meeting, Musubi
  ingests it.
  A replay mode plays a saved meeting through the same path for testing.
  With relevance on, lookups search statements (`live.lookups`, default;
  `"passages"` restores the passage path above): every query finds its
  nearest statements (claim code index, then exact similarity, scoped),
  the relevance classifier chooses one among all of them or none (shown
  while P(none) < `live.lookup_max_none`, 0.3), and the statement is shown
  with its passage as the source; no summary is written.
- **Topic cards** (`live.topic_cards`): when a line says a topic's trigger
  term, or a lookup's nearest claims are on a topic, the panel shows that
  topic's card (see "Topic cards" below), rebuilt under the current scope
  whenever the scope changes. A replay sees only what was known when the
  replayed meeting happened: its card index is built from what was said
  before it.
- **Prep card** (`live.prep_card`, default on; false: no card, as
  before): when a meeting starts, the calendar event the title and "With"
  line come from (`store.EventMatchAt`, the same match as `EventAt`) is
  kept on the session (`eventId`), and the top of the cards shows that
  event's prep, read in the panel's scope (taken from Live, not from the
  screen, so the shared screen gets what the Prep tab shows there): the
  talking points drafted ahead (`prep_briefs`) with their sources, then
  what is open with the people in it, what the last meeting of the series
  left open, and who is new. It is compact (the first three talking
  points, or the first three open items) until "more", and × collapses it
  to one line for that meeting (remembered per viewer). Without drafted
  talking points, "Draft prep" runs the Prep tab's path (`PrepBrief`,
  same local/hosted rule); nothing is drafted unless pressed. Loading it
  runs no chat model; like the Prep tab, finding the threads the event
  is about embeds its title once. Talking points are not tracked against
  what is said.
- **Focus** (`live.focus`): each card is cut to the three lines closest
  to the last 90 s (embedding similarity), with the line of the
  conversation that raised it; lookups quote theirs too. Subjects last
  discussed over 60 days ago are marked old. Without relevance, the two
  most recently raised cards and the two newest lookups are open; the
  rest are one line each under "Earlier in this meeting". Sensitive cards
  collapse to their title only on a shared screen. The panel holds four
  cards; with `live.keep_topics` (default 0: dropped), cards pushed off
  by newer ones stay for the meeting, at most that many, most recent
  first. They aren't ranked against the conversation. In landscape,
  earlier cards show as collapsed cards (title, the line that raised it,
  age) as a menu of what else to raise; a click opens one, × collapses
  it. Raised again, a kept card goes back on the panel.
- **Relevance** (`[live.relevance]`): cards and lookups compete for four
  places by how well they fit the last 90 s, asked of a classifier (Jev,
  or the chat model's yes/no) as one question, about 100 ms a call. What
  is shown keeps its place until a newcomer fits clearly better (0.15) or
  it stops fitting (judged again every 20 s, with the picker too); nothing
  under the bar (`min_p`, 0.5) is shown.
- **Picking** (`[live.pick]`, with focus and relevance's classifier):
  lookups, trigger terms and the talk itself put topics forward instead
  of opening cards. Each lookup votes for its 3 nearest topics, a term
  said for its topic, and the last 3 minutes for their 5 nearest; every
  20 s of meeting the classifier reads those 3 minutes and chooses among
  the 12 strongest candidates (plus "none"), and the up to two above
  `min_p` (0.4) open. Topic cards replace relevance's places; lookups
  keep them. `[live.pick.brief]` (a chat model) writes each picked card
  a headline (something the last 3 minutes didn't say) and up to three
  things to raise in this meeting; when the notes planned this very
  conversation, the headline says so. The brief reads the lookups that
  put the card forward and the 3 minutes split where the card was first
  put forward (less 15 s), the earlier part marked as possibly another
  subject (`full_window` restores one undivided window). It first says
  whether the notes fit: when the conversation is about a customer,
  person or project the notes never mention, the card is taken down and
  not picked again for 10 minutes (`keep_unfit` keeps it). A picked card is its title,
  headline and best raise line, so the panel keeps the recent picks up
  (the current one marked), not only the open ones; opened, a card shows
  its other raise lines and the topic's written status, each point's
  citations opening the statements it rests on. Once more than five
  cards are pushed off, they are ranked by what is most worth raising:
  the menu shows those five, the rest a click away. Briefs are kept per scope, so one written in
  private doesn't show on a shared screen. Research log 2026-10-28:
  80–90% of cards on topic against 12%.
- **Cost during a meeting** (research log 2026-11-12): with `live.cache`
  (default on) the reads Live repeats are kept for the meeting in a
  `store.ReadCache`: topic infos, each card's evidence, thread statuses,
  entity graphs, and for statement lookups each claim's visible statements
  and each statement's vector. Each result is kept under the whole scope
  it was read in (so a toggle to the shared screen never sees what was
  read in private) and everything is dropped when `KnowledgeSignature`
  moves (any write: an import, a knowledge run, a correction). A new
  cache starts with each meeting and each Reload. With `live.skip_idle`
  (default on) a due pick, or a model suggestion run, whose input is what
  the last one read (the conversation it reads, the scope and, for a
  pick, its votes) is skipped and the cadence kept. Menu ranking already
  runs only when the cards it ranks change. While the window can't be seen
  (`live.background`, default on; the page's `visibilitychange`:
  minimized, hidden, on a Space not shown, covered; another monitor
  showing it counts as seen), Live does only what the topic cards depend
  on, as a stepped replay's `CardsOnly` does: picks and the picked cards'
  briefs, lookups that only put topics forward, and model suggestions on
  complete lines. Entity graphs, card drawing, the menu's ranking and
  lookup summaries wait, and the panel is redrawn once the window shows
  (`Live.Redraw`). On a live meeting it took the app from 3% to 1% of a
  core and Ollama from 11% to 8% (research log 2026-11-13). The diagnostics panel's
  "Record a 30 s profile" (`[diagnostics] profiling`) writes a CPU profile
  and a goroutine dump under the data folder's `profiles/`; Live's steps
  carry pprof labels (`step` = pick, suggest, fastlane, cards; queue for
  the work queue).
  Its "resources" view (`[diagnostics] resources`, default on) shows CPU
  (share of one core, from the change in each process's CPU time) and
  memory (macOS footprint and resident) for Musubi, its web view, the
  recorder, Ollama and Tomoe, with each one's child processes, over the
  last 15 minutes (`internal/procstat`, libproc on macOS; other systems
  say it isn't supported). It samples every 2 s only while the panel is
  open: the panel holds a watch it renews, and sampling stops when it
  closes or stops renewing. The recorder is the pid its control Status
  reports (`pid`), else its app path or `musubi-recorder run`; the web
  view is the WebKit web content process macOS holds responsible for the
  app (left out where that can't be asked); a process counts in one
  component only.
  The update check (`internal/updates`, `[updates]`, default on,
  release candidates included) asks GitHub's API once a day,
  anonymously, for the releases of the public releases-only repository
  (`anielsud/musubi-releases`; the source repository is private). Only
  the release workflow's DMG checks (`make CHANNEL=release`); a build
  from source is `local` and never checks or installs, so it is never
  replaced by a release behind it. The header offers a newer release.
  **Update**, only when clicked, and never while recording or while an
  update or run is going:
  1. downloads the DMG over HTTPS from that repository's own download
     URLs;
  2. checks it against the `.sha256` file the workflow publishes beside
     it;
  3. mounts it read-only and checks that it holds the same bundle
     identifier at that version and that its signature verifies. A
     certificate-signed app also requires the new version to satisfy its
     own designated requirement, meaning the same certificate (the checksum
     comes from the same place as the DMG, so the signer is the real
     tie). Releases are signed with the self-signed "Musubi Release
     Signing" certificate (workflow secrets), so macOS's privacy
     permissions, which it keys on that requirement, survive updates;
  4. copies it beside the app and swaps the two by rename, putting the
     old one back if the swap fails; the old one is kept as
     `.Musubi-previous.app` until the next launch;
  5. restarts the recorder (its agent runs the copy inside the app) and
     relaunches Musubi.

  `install = false` opens the release page instead.
- **Stepped replay** (`musubi live --replay ID --stepped`): the meeting
  line by line on its own clock, each line's work (lookups, cards,
  suggestions, summaries) done before the next, so the result doesn't
  depend on speed. `--events` writes every panel event for measuring.
- **Datasets**: the window opens `MUSUBI_CONFIG` when set, else the
  dataset last opened, else the first built one in
  `~/.config/musubi/datasets.toml` (key, name, config, note;
  `MUSUBI_DATASETS` overrides the path), else the default config. The
  window title names the dataset. The dropdown that switched datasets was
  removed on 2026-10-27, after side-by-side compare (2026-10-23): one
  dataset is in use and no experiments need switching.
- **Landscape**: the ▭ button widens the window to most of the screen
  (and back). From 1000 px wide the layout uses columns: in Explore, the
  topic views stay on the left and the list or a topic's page opens beside
  them; in Live, the conversation is on the left and cards and briefs fill
  a grid on the right.
- **Screen-safe**: a toggle switches the panel's scope to `shared-screen`.
  Detecting the owner's own screen share is not built (Tomoe can see a
  meeting app's sharing state but doesn't send it); restricted and HR cards
  are collapsed to their title meanwhile.
- **Ask** (`internal/ask`): the threads a question is about (thread
  finder, below) give their written status and latest current items;
  scoped hybrid passage search adds up to six passages (eight when no
  thread fits). The `[ask] model` (else `[llm]`) answers, leading with
  where things stand and citing items and passages; the answer names the
  subjects it drew on, which open their topic pages. `ask.threads = false`
  answers from passages alone, as before. A hosted model reads only the
  cloud scope unless `knowledge.allow_hosted`.
- **Prep** (`knowledge.Prep`, `internal/gui/prep.go`): pick an upcoming
  calendar event or "the next one" of a recent meeting (or name people).
  From its attendees: their subjects (threads where they hold items,
  weighted by recency and by meetings with them), each with its status
  and what is new since the last meeting; what is open between you (the
  statuses' open points owned by them or you); and what is worth sharing
  (current items on their subjects said since, in meetings none of them
  attended). Sharing suggestions never include an item labelled with a
  category or restricted, nor anything from a thread where 15% or more of
  the items are (the labeller misses about a quarter of a sensitive
  subject's items). Open points carry the date they were last raised;
  those with nothing said for `prep.quiet_days` (21) are listed after the
  rest as gone quiet, and a passed due date is marked overdue. Subjects
  over the same 15% are marked for the owner alone and, with more than
  one other person attending, listed last (`prep.mark_private`).
  The previous meeting of an event's series (`knowledge.LastOfSeries`: the
  latest earlier event with the same subject, "Canceled: " aside, that has
  a recording, the copy with a recap first) sets "since" and anchors the
  prep: its recap and its to-dos, with how each stands now, come before
  the subjects, and the talking points start from what it left open. On
  the 12 Nov ELT meeting this took the talking points from none of the
  last meeting's agenda to most of it, on 22,700 characters of input
  (a naive prep from the raw transcripts filled 120,000 with one meeting).
  "Draft" writes talking points with the Ask model.
  With `prep.auto_brief` (on), the app drafts them ahead for the owner's
  calendar events in the rest of the week: every 15 minutes it builds each
  event's prep, hashes it with the model's name, and drafts again only
  when the hash changed; drafts are kept in `prep_briefs` with the
  combined label of the sources they cite (confidential with none), and
  nothing runs while a meeting is live.
  `musubi prep <item> [--brief]` is the same from the command line.
- **To-do** (`knowledge.ToDos`, `store.Commitments`, or the digest's to-dos, see Meeting digests): everything someone
  said would happen (commitment statements) and, with "+ proposals", what
  someone said should happen and nobody agreed yet (positions and
  decisions still proposed). Nothing is stored for the list: what came of
  each is read from the statements threading related to it later, each
  visible in the scope on its own. One that completes (or answers) it
  marks it done; one that supersedes it, changed; one that repeats it
  folds the earlier saying into the later ("raised 3×"). An open item
  with nothing said of it for `prep.quiet_days` is gone quiet; a date in
  its text sets when it is due, and a passed one, overdue. "Mine" is the
  items whose owners (the point's people, else its holder) include
  `owner.name`. Grouped by the thread each is on (the most pressing thread
  first; a thread's name only where the scope may see it), or by state:
  overdue, open (due soonest, then latest raised), proposed, gone quiet,
  done or changed.

  The owner's word (`owner_links`, kept across rebuilds and named by
  statement as thread edits are) is read the same way and outranks the
  threader's. It is of two kinds. What happened in the world is evidence:
  "done" and "dropped" (with an optional note) are statements of the
  owner's that complete or replace the item, shown as "you, 30 Oct". How
  items relate is a link: "same as" folds the earlier into the later as a
  repeat does, "replaces" changes the other, "step of" nests it under a
  parent that counts its steps. A misreading is fixed, not answered: "not
  a to-do" sets it aside, "due" sets the date, and "correct" is the thread
  view's correction of the text or whose it is. A link or mark names
  statements the scope reads on its own, so one touching an item the
  screen can't see is left out. Each is undone from the item. The owner's
  marks are read by the To-do view only; Ask and Live don't see them yet.
- **Explore**:
  - **Topics**: the topic map in four views, picked with small buttons
    under it: Graph, Meaning, Timeline and Clusters (see "Topic map").
    Clicking a topic opens its page; a filter box and the cluster chips
    narrow what is lit. Beside them, "Recently moved" lists topics by
    their latest item, each with the first sentence of its status. A
    thread's page leads with its status ("Where it stands", with what
    changed and what is open, each cited), then its current items, with
    what was replaced folded away.
  - **Search**: passage search, entity lookup, and a radial graph of an
    entity's connections with its timeline.
- Policy (2026-10-16): the approved hosted endpoints may read any data,
  meeting text included. With a cloud chat model configured, Q&A still
  filters passages to the cloud scope and live suggestions are off. Points,
  threads and statement labels use a hosted model only with
  `knowledge.allow_hosted`. The OpenAI-compatible provider then sends the
  key named by `llm.api_key` (`keychain:<service>` or `env:<VAR>`), streams
  replies (a hung reply is cut off and retried), allows room for reasoning,
  and sends strict schemas and `llm.reasoning_effort`.

## Knowledge layer

Passages are what was said; knowledge is what it amounts to, and what the
features read (see "Atoms first" above). Each source
mixes facts, opinions, hypotheses, decisions and commitments, held by
different people, with different authority, and they change over time:
reinforced, clarified, superseded, disproven, or simply going stale.

```
passages ─▶ statements ─▶ claims ◀── relations ──▶ claims ─▶ topics
            (who said what,  (one assertion across     (clusters of claims,
             of what kind)    sources; belief          related to each other)
                              computed when read)
```

### Statements (`extract` stage)

A local model reads each passage once and lists what it asserts. Each
statement has:
- **kind**: fact, opinion, hypothesis, decision, commitment, requirement, or
  question;
- **text**: self-contained, with names made explicit;
- **holder**: who said it, resolved to a person through the speaker labels;
- **certainty** as expressed, from 0 to 1;
- **said_at**;
- **value**: the date, amount, number, percentage or duration it turns on,
  as said ("Q4", "mid November"). It is normalised in code into bounds
  (Oct–Dec; Nov 10–20), so values compare deterministically: within,
  contains, same, or differs. "Mid November" within "Q4" refines;
  "22 Sep" against "29 Sep" differs.
- **line**: the passage line it came from, so it inherits that passage's
  sensitivity and can be cited exactly.

Small talk and procedure are skipped. The stage is versioned by model and
prompt like any other, so improving the prompt means re-running it.

A reply that isn't valid JSON (e.g. cut off at the token limit) skips that
passage only. An item whose every passage fails fails the run after
extraction, so embedding, linking and threads don't run; with
`knowledge.skip_failed_items` such an item is logged, left unstamped (tried
again next run) and counted in the summary, and the run goes on. A run in
which no item extracts still fails: that is a broken model or cache, not
nothing worth extracting (research-log.md, 2026-11-05).

Checks after extraction, all cheap and stored with the statement:
- **quote and grounding**: the statement carries the words it came from;
  code checks the quote is in the cited line, its figures are in the
  source, and its negation agrees ("three years instead of two" counts as
  the contrast "three years, not two"). Ungrounded statements weigh less
  and never join a claim without the judge.
- **worth**: one yes/no forward pass per statement ("work knowledge, or
  small talk, a joke taken literally, garble?"), read as a probability
  from the model's next token rather than generated. Statements below
  `knowledge.min_worth` (0.2) stay out of claims but are kept, so a
  different threshold re-links without re-extracting. On the pilot about
  9% scored below 0.2, nearly all noise.
- **search text**: the statement plus the digits of spelled-out numbers
  ("forty five days" is found by "45").

An optional passage gate (`knowledge.gate`, off) asks the same kind of
question per passage before extraction; on meetings it skipped too little to
pay (see the research log).

### Points (`knowledge.extract = "points"`, meetings)

Statements are taken one passage at a time, so they miss what the next
passage clarifies and lean on "it" and "the agreement". Points are the
meeting-level alternative, measured on the pilot at about 23.5 of 24 key
points and 1–2% wrong (Qwen3.8-27B thinking, on llama-server). Lean
extraction (`knowledge.extract = "lean"`, `internal/knowledge/lean.go`) is
the measured hosted prompt, lean_ex_v2: a brief of the whole meeting, then
every item in 50-line parts with 8 lines of context, turned into statements
the way imported items are. Email and chat stay on statements.

What runs when: an update after new data (`on_update`) extracts and
threads only items newer than the thread frontier (the newest threaded
meeting), at most `auto_max`; older items are held (`threads.hold_older`)
until a rethread in time order, since threading appends. An item already
extracted or threaded isn't a backlog: re-extracted after its content
changed (a speaker renamed in Tomoe), it goes back into the threads in its
place. A meeting
recorded twice (Tomoe and a transcript overlapping) is knowledge once
(`prefer_source`); two copies from one source count only when they share
most of their words (a Teams transcript imported by hand and synced), never
two Tomoe recordings. A recording without an end time (imported
transcripts, Bluedot) ends at its last line for linking and duplicates
(`item.End`), and an extracted item keeps the title its passages carry,
so a later link to its calendar event doesn't re-extract it. With `knowledge.stitch` (on), a Tomoe recording whose
calendar event also has a Teams transcript (sharing at least 0.6 of its
words, `item.WordAgreement`) takes the transcript's lines for the
stretches it missed (joined late, left early, between two parts of a
split recording), marked "Teams transcript, not heard by you" in its
passages; the Teams copy is then its duplicate. The stitched copy exists
in memory only (chunking onward; stored items are untouched), and only
for a recording not yet threaded when first stitched (stamp
`stitch/<id>`), so a transcript arriving late never re-chunks a threaded
meeting. A Teams meeting's recording waits up to
`knowledge.stitch_wait_hours` (2) after the meeting for its transcript
before threading; it is extracted and summarised meanwhile, and both are
redone if the transcript stitches in (cheap, as nothing is threaded yet).
`Runner.Queue` reports the waiting work; the Status
tab shows it and starts "catch up" and "rethread" runs under a spend
cap.

1. **Known facts**, written in code: the owner and their company, and each
   invitee's organisation by email domain. An address that isn't
   first.last is shown as it is, never turned into a guessed name.
2. **Brief**, one call without thinking: purpose, who's who (only as far
   as the transcript shows), subjects, and a glossary of vague references.
3. **Segments**, `points.segment` (5) passages per call,
   `points.parallel` (4) at once, with thinking (`points.think`). Each
   sees the known facts, the brief, its passages, and one passage either
   side marked `[context]`, from which it may complete a point but not
   take one. The prompt carries rules and two invented worked examples of
   past mistakes (expanded acronyms, a client's ask stated as practice, a
   relayed demand attributed to the relayer, a garbled line turned into a
   position).
4. **Checks in code**: every name and figure in a point must appear in the
   meeting or the known facts (not the brief, which is model-written), and
   each quote must be in the line it cites (or the one before). A failing
   point is kept, marked ungrounded with the reason. Post-hoc model
   verification was tried and caught at most half the errors (research
   log, 2026-10-12), so none runs here.
5. **Merge in code**: near-identical points (word overlap ≥ 0.8) from
   neighbouring segments are kept once. A model rollup was tried and lost
   decisions.

A point is stored as a statement, so claims, belief, topics and views
work unchanged. It adds a **status** (decided, agreed, proposed, open,
answered, rejected, stated), **who** it belongs to, and **evidence** (every
line it rests on; the first anchors it). Its kind is mapped onto the
statement kinds by status: a proposed decision is an opinion, an answered
question a fact. Points skip the worth score, which drops personal
details.

**Personal details are kept**, attributed to the person they are about,
and labelled `personal` (restricted): the owner sees them, nothing else
does. A point's own label also takes the most sensitive passage it rests
on and whatever its text matches in the sensitivity rules; reads combine
it with the passage label.

The extraction model is `knowledge.points.model` (provider `openai` for
llama-server; empty uses `[llm]`) and must be local. llama-server is run
with the measured settings: `--spec-type draft-mtp -np 4 -c 98304
--cache-type-k q8_0 --cache-type-v q8_0 --reasoning-budget 3000`.

### Imported items (experiments)

`musubi import-items --item <id> --file items.json` loads knowledge items
extracted outside Musubi (another model or prompt) as an item's
statements:
- **input:** kind, status, text, who, and the meeting lines each item
  cites;
- **checks:** they go through the same checks and labelling as points.
  Names and figures must appear in the meeting; the label is the most
  sensitive passage cited; personal items are owner-only;
- **kinds:** insight kinds are kept (metric, framing, rule, disagreement,
  outside_fact, sensitivity_cue, personal), with their own belief weights
  and half-lives;
- **keeping them:** `knowledge.extract = "none"` stops Musubi's own
  extraction, so imported statements are kept and only embedding,
  linking and topics run;
- **proposed plans** (`knowledge.import_keep_proposed`): a proposed
  decision or commitment keeps its kind with status `proposed` instead of
  becoming an opinion; dates and figures are read from the text into typed
  values (`DatesIn` reads "October 1st", "two weeks after that", "end of
  the year"); and the check accepts the meeting's own date and known
  people's full names, which extractors write without them being said.

Experiment instances run beside the real one: `MUSUBI_CONFIG` points the
CLI and the app at another config, and so at another data directory.
Inbox drops gain a `meeting` record (title, start, attendees, turns with
speaker, offset and text), for transcripts from other recorders.

### Beyond points: threads, metrics, framings, rules

Where the knowledge layer is heading (threads with state, metrics with
definitions, disagreements, framings, rules, commitments, outside facts,
people) and how items surface by situation: `docs/insights-direction.md`.

### Claims (`link` stage)

A claim is one assertion across sources. Statements are linked in the order
they were made:
1. Embed the new statement and find the nearest claims by cosine similarity.
2. A near-identical match (≥ 0.97, same kind, same figures and values, and a
   grounded statement) joins without asking the model.
3. Up to six plausible matches (≥ 0.65) are judged one pair at a time: one
   forward pass gives a probability for each of same, contradicts, refines,
   supersedes, answers, unrelated; a relation needs 0.5. Code then applies
   rules the model is unreliable at: a contradiction of a question answers
   it, a "newer version" with the same date or figure confirms it (same),
   relations that make no sense for the kinds are dropped, and statements
   about different customers or projects are never related. (`knowledge.judge
   = "json"` restores the earlier judge, which wrote verdicts for three
   candidates at once.)
4. Otherwise the statement starts a new claim.

With `knowledge.strict_links` (the default), three more rules stop
linking from retiring items that are still live:
- within one meeting, "supersedes" needs a spoken correction ("actually",
  "correcting", "revised"); otherwise it is "refines" (a status and the
  walk-through of it are complementary, said in the same minute);
- one owner's commitment never supersedes or refines another owner's;
- a commitment to find out doesn't answer a question: it is "pending", and
  the question stays open.

Claims are cheap to rebuild from stored statements, so when the linking
rules, model, `min_worth` or worth scorer change, the next run clears them
and links every statement again; extraction is untouched.

Every statement belongs to exactly one claim (what it says). Relations
between claims record how it bears on others:
- contradicts;
- refines;
- supersedes: a decision changed;
- answers: resolves an open question.

A claim's centroid is the mean of its statements' vectors. It is stored
only for claims of two or more statements; a claim of one reads its
statement's vector (a NULL `claims.centroid`), written when a second
statement joins (2026-11-12: 99.4% of claims hold one statement).

### Belief (computed when read, never stored)

Belief is worked out from the visible statements of a claim and its
relations. Each statement contributes evidence

    e = w(kind) × w(certainty) × w(source) × decay(age, half-life(kind))

and evidence is combined per holder first (repetition by one person adds
little), then across holders, so independent people count. Contradicting
claims count against it.

Half-lives:

| kind | half-life |
|---|---|
| question | 14 days |
| hypothesis | 30 days |
| opinion | 60 days |
| fact | 90 days |
| requirement | 180 days |
| decision | none; it changes by being superseded |

Written sources (email, documents) weigh more than speech.

The owner's judgement overrides: verified, disputed or false. Judgements are
kept like the forget list (not derived), keyed by the statement judged, so
they survive rebuilds.

Statuses:

| status | when |
|---|---|
| established | high belief and (two or more independent holders, a written source, or verified) |
| likely | |
| contested | substantial support and substantial contradiction |
| speculative | mostly hypotheses or opinions |
| stale | evidence decayed past two half-lives |
| superseded | a newer visible claim supersedes it |
| disproven | judged false, or contradicted by stronger, newer evidence |

Because nothing decayed is stored, belief drifts with time on its own, and
changing the formula re-scores everything.

### Topics (`topics` step)

Topics are cross-linked, not nested. A commitment to one customer is linked:
- to other topics about the same customer;
- to the same kind of commitment for other customers ("parallel");
- to the topics it is discussed alongside.

Claims naming different organisations or projects are pushed apart while
clustering, so those become separate, linked topics.

1. Claims are clustered by centroid (agglomerative, average link).
2. Each cluster is labelled by the model, with a one-line summary.
3. Topics keep their ids across runs: a new cluster takes the id of the
   previous topic whose claims it mostly shares.
4. Topics relate to each other, as:
   - **same**: they share a customer or project;
   - **together**: they are discussed in the same items;
   - **parallel**: alike in meaning, about different customers or projects;
   - **similar**.

   Similar but never discussed together is shown as a possible connection
   nobody has made.
5. Each rebuild records lifecycle events: born, grew, shrank, merged, split,
   absorbed, dissolved.

### Threads (`knowledge.grouping = "threads"`)

The alternative to pairwise linking and clustering, chosen by the linking
benchmark ([linking-benchmark.md](linking-benchmark.md)): grouping
precision 0.86–0.95 against 0.73, recall 0.43–0.53 against 0.21, and 0.5–2.5%
of live items wrongly retired against 22%. Items are threaded one at a time,
oldest first, with `knowledge.threads.model` (local today: the code refuses
a hosted model, although policy now allows one; see "Desktop app"):

1. **Assign.** The item's statements, 70 at a time (`threads.chunk`), with
   a list of candidate threads: the 40 nearest by embedding
   (`threads.candidates`; a thread's centroid is the mean of its members'
   statement vectors), the 15 most recently active (within three weeks),
   and those born while threading this item, each with its latest current
   statement. The model puts each statement in one or two specific threads
   (an initiative, deal, programme, metric, issue or event; never a broad
   theme), in a new thread it names, or in none (logistics, small talk). It
   replies with groups of ids per thread code (`T12`, or `n1` for a new
   one), not one entry per item. A reply that leaves items out is asked
   again for them.
2. **Reconcile.** For every thread the item touched, one call with up to
   25 of the thread's current statements (`threads.state_cap`, latest
   first) and its new ones. The reply lists only effects: which new
   statement supersedes, corrects, completes, answers or contradicts which
   earlier one, promises to find out (pending), or repeats it (same).
   Code keeps the current list: the previous one plus the new statements,
   less those retired.
3. **Rules in code** (`threadRule`): "corrects" is stored as "supersedes";
   "completes" applies only to a commitment or decision from an earlier
   item; an in-meeting "supersedes" needs a spoken correction. Measured on
   the local 27B, these removed mostly wrong relations (it had called 84
   pairs "completes", 70 of them on views and facts).

The candidate list and the state cap keep a meeting's cost flat however
long the history: over a 20-meeting replay the prompts stayed at 7.6–8.3k
(assign) and 1.8–2.4k (reconcile) tokens. Skipping reconcile for threads
born in the meeting, or asking assign for the effects, both failed: 64% of
real updates are within one meeting.

Storage: threads are rows of `topics` (name and scope as label and
summary), so Explore and cards read them as topics. `thread_items` holds
membership (a statement can be in two threads) and which statements are
current; `thread_runs` which items are done under which rules. After
threading, `LinkThreads` (no model call; also `musubi thread-links`)
writes what threading doesn't: each thread's centroid (the mean of its
claims' centroids, which places it on the meaning map), its organisations
and projects, and relations by meaning or a shared organisation. Without
it every thread sat at one point on the map and had no related threads.
Sharing meetings isn't stored as a relation in thread mode: a recurring
meeting's threads all share it (1,523 of 1,540 pairs did on four
meetings). Relations
are claim relations. In thread mode the pairwise judge is off: only
near-identical statements join a claim, and linking takes seconds. A
thread is as sensitive as the statements it was named from. A change of
prompts (`ThreadsVersion`), model or link rules applies to items threaded
from then on; everything is rethreaded only on request (see "Derive
stages": versions are explicit).

Calls that share a prompt start carry a session (`x-session-affinity`,
hashed: per meeting for points, per item for threads), so a hosted
provider routes them to one replica and serves the shared start from its
cache; with `knowledge.warm_cache` the first segment and the first
reconcile go alone, so the others find it cached.

**t4 (`knowledge.threads.audit_fixes`, default on).** The backfill audits
(research log, 2026-10-16) found subjects fragmenting as history grew and
86% of items still current months after they were settled. The changes,
measured on the ELT year (research log, 2026-10-17):
- **Assign** prefers continuing a subject over starting one: a new figure,
  an outcome or a new angle continues it, and a person or a meeting is
  not a subject. Each candidate shows its size, dates and two latest
  items.
- **Merge:** threads born in an item are offered, with their most similar
  older threads (centroid ≥ 0.75), to one call that merges those on the
  same subject.
- **Reconcile** sees the thread's current items nearest the new ones plus
  the five latest (not just the latest 25), and related current items of
  other threads (similarity ≥ 0.80, at most 10), which it may retire. Its
  prompt says what replaces what: a later figure for the same quantity and
  period, an outcome settling a plan, months later. Added detail isn't
  replacement, and an item is never retired by an older one. "Completes"
  also takes positions and proposals.
- **Consolidate:** each touched thread with more than 8 current items has
  its list (up to 80) reviewed once. Items a later item in it replaced,
  completed or answered stop being current, with the relation recorded. A
  replacing item is required: "no longer current" without one retired
  valid positions.
- **Lapse:** open commitments and questions untouched for 60 days stop
  being current.
- **Repeats:** when reconcile calls a new item the same as an earlier one,
  the newer wording stays current (t3 kept the older one, which dropped
  the newest statement of a subject).
- Threads named in an assign reply but given no statements are removed
  (all versions).

Model settings: Qwen's non-thinking sampling (temperature 0.7, top-p 0.8,
top-k 20) and token caps of 6k (assign) and 3k (reconcile). At temperature
0 the local 27B looped to 16k tokens on long JSON replies. On the M5 Pro
four ELT meetings take 34 minutes (8 parallel slots).

### Thread statuses (`knowledge.status`)

After threading, each thread whose items changed gets a written status
(`knowledge.WriteStatus`, version `ts1`): `now` (where it stands as of its
latest item, leading with the decision, number, blocker or date), up to
four changes in the month before, what is open (owner, due date), and
who drives it, each citing the statements it rests on. It is written from
the thread's latest 60 current items and its latest 30 retired ones said
within 60 days of its newest, and rewritten only when that input changes
(`thread_status.input_hash`). A status carries the most sensitive label of
what it was written from. When some of a thread's items are hidden from a
shared screen, a second status ("open") is written from the rest, and a
scope gets the fullest one it may see. The model is
`knowledge.status.model` (else the threads model; hosted only with
`knowledge.allow_hosted`). On GLM-5.3-Flash: 275 ELT-year threads in 2
minutes for $0.18; September's 519 in 3½ minutes.

Consecutive items overlap (`knowledge.threads.overlap`, default on): an
item's consolidation runs while the next item is placed and reconciled.
Threads under review are marked; the next item waits for one before
adding to it, reconciling it or retiring anything in it, then narrows its
snapshot to what is still current (a review only retires). Lapse runs
before the review in this mode. A batch slightly older than a few
threaded items rewinds them (`knowledge.threads.rewind`) and appends.

A knowledge run is resumable from what is stored: an item not marked
threaded under the current rules but already in threads (a run stopped
part way) is taken out of them (`knowledge.Unthread`, no model) before
it is threaded again. A correction is recorded first and the graph
mended after; one whose mending didn't finish (the model unreachable) is
done at the start of the next run (`corrections.related`).

**Thread edits** (`knowledge.MergeThreads`, `SplitThread`,
`UndoThreadEdit`; the thread page in the app). The owner merges a thread
into another or splits picked statements out to a new thread. No model:
memberships move, "current" is derived again (relations are between
statements, so they survive the move), and the edited threads' statuses
are dropped and written again. Each edit is recorded in `thread_edits`,
naming its statements by item and extracted text, and done again after
every threading run wherever a rethread put them
(`knowledge.ApplyThreadEdits`, a no-op where it holds). A newer edit that
names statements on both sides of an older one supersedes it, so a merge
of what was split doesn't fight the split on every run.

`knowledge.on_update` (default on) runs the knowledge stages after the
app's Update, so new meetings are threaded and their threads' statuses
rewritten without the command line.

The thread finder (`knowledge.ThreadFinder`, cached until the knowledge
changes) scores each thread whose name the scope may see by the better of
its centroid and its three best-matching statements, plus a little for
each question word in its name, and returns up to three within 0.08 of
the best. With `ask.find_newer` (off by default) it adds one more within 0.12
when that thread's latest item is later than every thread found: a
subject's latest turn is sometimes threaded on its own (research log
2026-10-20).

### Meeting digests (`knowledge.digest`)

A meeting's summary and its to-dos, each from one whole-meeting read, apart
from statement extraction (which keeps its 50-line excerpts: handing it the
whole transcript found fewer, research log 2026-10-16; a narrow task over
the whole meeting does better, 2026-11-02). Two calls per meeting
(`knowledge.Digester`), the transcript fenced as data between markers with
the task after it:

- **A sectioned summary**: an overview and one section per subject (title,
  2 to 4 sentences, people, the line ranges it rests on, sensitive
  categories). Against a plain paragraph, graders preferred it 10 of 10; it
  covered every major subject (85% for the paragraph), about 2% errors per
  claim, mostly who-said-what swaps in a back-and-forth.
- **To-dos**: each with owner, who asked, agreed / asked / floated /
  declined, the timing words and a date worked out from the meeting's day,
  and the lines and quote it rests on. Against to-dos read from statements:
  88% good (68%), 4% duplicates (17%), owner right 96% (87%).

Stored in `meeting_digests`, `meeting_sections` and `meeting_todos`
(created by `schema.sql`, no schema-version bump, so nothing extracted is
dropped). Stamped stage `digest` (version `DigestVersion`, input hash of the
passages, labels and facts; a new version waits for `--redo digest` as
extraction does), deleted with the item.

- **Checks in code.** Replies are schema-constrained and validated: counts
  and lengths capped, line numbers kept only inside the meeting, dates only
  real dates from the meeting's day to three years on, states and
  categories from fixed lists. Names and figures are checked against the
  meeting as points are (`Meeting.Check`); a failing row is kept and shown
  as "check wording". A long meeting answered with no to-dos, or with most
  to-dos citing no line, is asked once more (the model does both sometimes)
  and the reading that cites more is kept.
- **Sensitivity.** Each row's label is worked out in code from the passages
  it cites (all the meeting's, when it cites none that exist), the text
  rules and the model's flags (personal, hr, legal, financial), which only
  ever add. Every read combines it with the item's, as statements do; the
  overview is as sensitive as the most sensitive section. 93% of flags were
  right, 7% over-flagged, none missed.
- **To-dos and statements.** A to-do is matched with the statement that
  says the same (cited lines within two of its own plus shared words, or
  many shared words; one to-do per statement). The statement carries what
  the to-do view needs: its thread and the later statements related to it,
  so done, changed and repeated are read as before, and the owner's
  marks (named by statement) still apply. The match is made again, with no
  model call, when the item is re-extracted (`digest-link` stamp). A to-do
  no statement matches stands alone with a negative id: no thread, and the
  owner can't mark it yet.
- **The To-do view** reads digest to-dos when `knowledge.digest.todos` is
  on (`DigestCommitments`): the statement's id and relations, the to-do's
  own words, owners, state and date over it. A digest date replaces the
  guess from the words (`due()`), and none means none was said; floated
  ones are proposals; a declined one is closed by the meeting.
- **Meetings tab** (`Views.MeetingPage`, `MeetingList`): the summary in
  sections, each linked to where it was said; the meeting's to-dos with
  their current state; and its topics, the threads holding at least
  `knowledge.digest.min_topic_statements` (3) of its statements, derived
  when read from the statements' memberships so they follow rethreading,
  merges and splits (a thread holding one statement was judged "not here";
  weight predicts relevance). A summary is what was said in that meeting; it
  is never shown as where a subject stands now, and Ask doesn't read it
  (untested).
- **Run**: in the normal knowledge run after labels, or alone with
  `musubi knowledge --digest-only [--plan]`; model `knowledge.digest.model`
  (else the threads model); hosted needs `knowledge.allow_hosted`.

### Topic map (`Views.Map`, computed when read)

What Explore draws, from what the scope can see, in 40–80 ms on four
meetings:
- **Links** between topics, for four reasons:
  - *shared*: statements in both (threads can share members);
  - *together*: discussed back to back (a statement and the next two in
    the same item, within five minutes, in different topics; once is
    chance, so it takes two);
  - *same*: the same organisation or project;
  - *similar*: alike in meaning, counted only above the top fifth of the
    dataset's stored pairs, because one organisation's topics all read
    alike (thread centroids sit at 0.8–0.9).

  Counts are divided by the square root of the two topics' sizes, so a
  catch-all topic doesn't pull everything to it, and each topic keeps its
  five strongest links. A topic page's related topics are these links.
- **Clusters**: communities of the link graph (Louvain, deterministic),
  named after their largest topic. Topics with no links are "not linked".
- **Activity**: statements per topic per item, for the timeline.

The views:
- **Graph**: a force layout where each cluster has a home on a ring, with
  linked clusters next to each other, so clusters read as regions. Links
  between clusters are drawn faint.
- **Meaning**: the first two principal components of the topics'
  centroids, spread half by rank so outliers don't squeeze the rest.
  Datasets without vectors (imported reference items) have none.
- **Timeline**: a row per topic, a column per meeting, grouped by cluster.
- **Clusters**: a card per cluster with its topics and the clusters it
  links to most.

Both are quadratic in topics (pairs of stored relations, all-pairs layout
in the browser); fine for hundreds, not for the 4,000+ threads of a large
corpus (scaling.md).

### Topic cards (`knowledge.CardIndex`, `Views.Card`)

What someone needs about a subject at the moment it comes up
(`docs/topic-cards.md`). Built from claims, with no model at read time
(~30 ms), so it fits the live panel's fast lane.

- **Index**, learned from the knowledge layer and rebuilt with it:
  - trigger terms: capitalised words that most of their claims' topic
    uses and other topics mostly don't. Words also written in lower case
    are ordinary words; names (directory aliases, transcript speakers,
    words seen as the one who "said" or "asked", one letter off a known
    name) are people, not terms;
  - companions: small topics raised during a topic's discussion (most of
    their statements fall between two of the topic's, within 10 minutes,
    in the same meeting), or using its terms. A companion's host is the
    topic whose card it appears on;
  - catch-alls: a topic whose discussion runs cover more than 40% of a
    meeting hosts no companions and anchors no term;
  - term cards: a term whose claims are spread over topics (an event people
    tell stories from) opens a card of the claims that say it, with at
    least 4 claims, unless the word usually ends a longer name ("New
    York");
  - lookup order: a trigger term in the text; else the query's words (2–5
    content words) found verbatim in at most 12 claims, most of them in one
    topic; else the query's embedding against claim centroids, where the
    topic of most of the nearest six wins if the nearest is ≥ 0.66. Short
    queries embed poorly, so exact words come first.
- **In thread mode** a topic is a thread: its card holds the thread's
  members (including those it shares with another thread) and shows only
  the ones the thread marks current; no companions are inferred.
- **Card**, read under a scope, as of a time: the topic's and its
  companions' visible claims, minus superseded, disproven and answered
  ones and lines about running the meeting; placed in sections (Watch,
  Now, Due, Yours, Open, Rules) and ranked by salience: kind, a figure,
  a date, recency (45-day half-life), grounding; reported views ("X
  thinks") rank below state. Commitments show when they fall due. The
  card's label is the narrowest of its lines; restricted, HR, pay,
  health, legal or personal cards are collapsed to their title.
- `musubi card <words>` prints the card the panel would show; `--at`
  replays a date; `--terms` lists what the index learned. Eval cases of
  kind `card` check lines by section, lines that must stay off, collapse,
  and that scopes which mustn't see a card get none.

### Cost and scale

Growth from today's four benchmark meetings to tens of thousands, email and
chat: what breaks first, how it is measured, and the order of work is in
[scaling.md](scaling.md). The design rules below still hold.

The final corpus is much larger than today's:
- meetings back to late last year;
- a year of email;
- six months of chat;
- SharePoint.

We optimise for two things, lookup time and storage, and recomputing must
stay cheap.

- **One expensive step, run once.** Extraction (a model writing statements)
  is the only costly stage. Everything after it is cheap and recomputes from
  statements in minutes:
  - classification rules;
  - value normalisation (in Go);
  - entity matching;
  - linking (incremental);
  - topics;
  - belief (computed at read time).

  Its prompt version changes only when unavoidable.
- **Taxonomy changes without re-extraction.** A new kind is added by
  re-classifying the affected statements (one word each, many per call), not
  by re-extracting. Statement ids, claims and judgements stay put.
  Judgements are anchored to the source line, not the wording.
- **Extraction by need.**
  - Meetings and direct email or chat are extracted as they arrive.
  - The long tail (old mail, newsletters, SharePoint) is chunked, embedded
    and searchable at once, and extracted lazily, the first time it is
    retrieved or a topic needs it.
- **Compact output.** The model writes only what Musubi can't derive:
  statement, kind, holder, certainty, and a raw value. Entities, normalised
  values and sensitivity are derived in code. The JSON schema keeps replies
  well-formed, so a smaller model can be tried.
- **Vectors were kept full** (float32) until 2026-11-12, when half precision
  (float16) became the default after measuring no change in results (a lever
  pulled: research log, 2026-11-12). Re-embedding is the second costliest
  recompute; the compact forms below derive from stored vectors without it:
  - Matryoshka truncation;
  - int8;
  - binary codes for a first-pass scan with re-ranking.

  Search stays exact over the scope-filtered rows up to a few hundred
  thousand chunks. Beyond that, a binary prefilter (96 bytes per vector,
  tens of milliseconds per million) feeds a float re-rank of the top few
  hundred. The scope filter still applies before ranking, never after.
- **Topics at scale.** Full average-link clustering is O(n²). It is fine to
  roughly 20,000 claims; past that, claims are assigned incrementally to
  their nearest topic, and re-clustered within organisation and project
  partitions. Threads avoid the problem by design: each item is placed as
  it arrives.

### Sensitivity in the knowledge layer

- A statement is visible exactly when its passage is and its own label
  allows (a point's: personal, or a more sensitive passage it rests on).
- **Statement labels with a typed classifier**
  (`knowledge.label.classifier`, off by default; `LabelVersionTyped`):
  one call per statement to a classification model (`provider.Classifier`;
  Jev) with the statement and its meeting's title, nine options (six
  sensitive, three kinds of ordinary). It labels statements no labeller
  has checked; `--redo label` relabels all. Measured on September's
  audited sample: recall 0.86 at precision 0.91, against the 27B's 0.71
  at 0.91 (research log 2026-10-20).
- **Statement labels** (`knowledge.label`, on by default; `LabelVersion`):
  a local model reads each statement once, with one forward pass over six
  choices. It labels what passage rules miss: a named person's
  performance, exit or replacement, anything about a job candidate, pay,
  health, legal matters and private life. The label is raised, never
  lowered (HR, Compensation, Health, Legal or Personal, at that category's
  level). On items the backfill audits flagged, the 27B on llama-server
  found 69% at `min_p` 0.3, and most of what it flagged beyond them was
  sensitive too; the 8B found fewer and misfired more.
- A claim is visible when any of its statements is, and it shows the text of
  a visible one.
- **Belief is computed only from visible statements**, so a confidence or
  source count can't reveal a hidden meeting.
- A topic's label records the highest level of the statements used to write
  it. In a narrower scope the label is replaced by keywords drawn from the
  visible claims.

### Evaluation

Synthetic scenarios (each with leak checks):
- a decision changed later (supersedes);
- a hypothesis later disproven;
- one person's opinion contradicted by a written fact;
- a fact confirmed both in a restricted meeting and in the open, whose
  evidence count must differ by scope;
- an open question later answered.

Case kinds:
- `statement`: kind, holder and text extracted from a passage;
- `claim`: status, relation and evidence count under a scope;
- `topic`: claims grouped together;
- `card`: the card a query opens, its lines by section, lines that must
  stay off, collapse, and no card in scopes that mustn't see it.

Grouping and linking quality (groups, relations, current state, search and
cards per subject) is measured by the linking benchmark, outside the eval
harness: [linking-benchmark.md](linking-benchmark.md).

CI replays recorded model replies and embeddings (`testdata/synthetic/llm-cache/`);
`make eval-record` re-records them locally when prompts change. Model quality
(extraction precision, linking accuracy) is measured locally on private
cases.

## Storage

One SQLite file (`~/.local/share/musubi/musubi.db`, WAL mode) holds all
derived data. The raw store is a directory of blobs beside it.

Upgrades (2026-11-12, `internal/store/upgrade.go`): the file records its
schema (`PRAGMA user_version`) and its format (`store_meta` "format": one-way
changes to what is stored, such as float16 vectors). A build refuses a
newer schema or format untouched, and copies an older file to
`backups/pre-upgrade-*.db` beside it before upgrading it (newest three
kept; `[store] backup_before_upgrade`). One-off changes to an existing
file are named steps in a ledger (`migrations`: id, when, which build;
`internal/store/migrate.go`), each run once, in order, all pending ones
in one transaction with `schema.sql`; it commits only if no kept table
lost rows and items and statements didn't drop (except a schema change,
which empties derived tables on purpose), so a refused upgrade leaves the
file as it was. `testdata/fixtures/` holds the synthetic corpus as
earlier releases left it (`make fixtures`), and a test upgrades each.
A redo or the app's rethread copies the kept tables first and puts back
rows they lost (`[store] check_counts`). [upgrades.md](upgrades.md) has
the steps for an existing dataset; [data-safety.md](data-safety.md) the
rules for a change to what is stored.

Backups (`internal/backup`, the app and `musubi backup`): a folder per
backup under `~/.local/share/musubi/backups/` with the database (VACUUM
INTO), the rest of the data folder and the recorder's folder (APFS
clones; not its models) and the config, and a `manifest.json`. Made by
hand, daily while the app is open, and before bulk deletes and rewrites
(`[backup]`). A restore backs the dataset up first, then swaps the
backup's data folder in; the app holds a shared lock on `musubi.lock`
in the data folder, so a restore from the command line refuses while
it is open.

| Need | Mechanism |
|---|---|
| Keyword search | FTS5 |
| Semantic search, topic clustering | sqlite-vec for exact cosine. Past 2,000 passages a 1-bit code index (`internal/vindex`; codes in `chunk_codes` and `claim_codes`, written with the vectors, about 96 bytes each in memory) proposes candidates, and the exact comparison runs over only those, through the same scope-filtered SQL. The candidates must hold twenty visible passages per result, otherwise the set is widened and finally scanned exactly, so a narrow scope never sees the wrong neighbours and never bypasses the filter. |
| Graph | `entities` with `entity_aliases`, linked to items by `item_entities` and `mentions` and to each other through what they share (`item_links`, `topic_entities`, `topic_relations`); traversal by joins and recursive CTEs |
| Facets across dimensions | person, customer, business area, project and topic are all entity types; a facet is a join through `edges`, with time as a filter |
| Trends | time-bucketed aggregate tables; DuckDB can attach the SQLite file for heavier analysis |

Chunks carry their item's title (for a meeting, the calendar subject). It is
searchable and is prepended when embedding, so a passage keeps the context of
the meeting it came from.

The schema has a version. Because everything except the forget list and the
audit log is rebuilt from the raw store, an incompatible schema change drops
the derived tables and the next update refills them. That includes the
extracted statements, which at full scale cost hours of model time, so new
columns on them are added in place (`addColumns`) instead of bumping the
version.

**Technology.** `mattn/go-sqlite3` (cgo, built with the `sqlite_fts5` tag)
and `sqlite-vec`, in WAL mode with foreign keys on, a 10 s busy timeout and
a single connection for writing (one writer at a time is SQLite's model; it
also keeps transactions from interleaving between goroutines). `[store]`
(`store.Options`) adds, per connection, a page cache of `cache_mb` and
memory-mapped reads up to `mmap_mb` (applied by a connect hook; sqlite-vec
is an auto extension, so every connection has it), and `read_conns`
read-only connections (`mode=ro` and `query_only`) for the read paths, so
a screen or Live doesn't wait behind a long write. Only plain reads go to
the pool (the list is in `internal/store/reads.go`); writes, reads inside
a write transaction and reads made by a writing method stay on the
writer. `KnowledgeSignature` is then read from a read-only connection of
its own (its `data_version` moves with every commit), not the writer's.
Defaults: `mmap_mb = 2048`, `cache_mb = 0` (SQLite's own; no faster
with mmap on), `read_conns = 4`; `mmap_mb = 0`, `read_conns = 1` restore
one connection with SQLite's defaults. Mapped pages count in the app's
resident memory. Measured: research log, 2026-11-12. Vectors
(nomic-embed-text: 768 dimensions) are blobs in one of two encodings, told
apart by length (`internal/store/vecenc.go`): float32 (3 KB, sqlite-vec's
format) or float16 (a tag byte and 1.5 KB, two to a page), written as
`store.vector_encoding` says (default `"f16"`; `"f32"` is the earlier
behaviour) and both always read as float32. `musubi vectors encode
f16|f32` rewrites what is stored. While a database holds only float32
passage vectors sqlite-vec compares them exactly, as before; once it holds
float16 ones (`store_meta`), the candidates are decoded and compared in
Go the same way. The 1-bit codes above stay in front past 2,000.

**Schema** (`internal/store/schema.sql`, version 4; 36 tables), by layer:

| Layer | Tables |
|---|---|
| Raw and items | `raw_manifest`, `items`, `item_links`, `item_audience`, `labels` |
| Passages | `chunks`, `chunks_fts` (FTS5, porter), `chunk_vecs`, `chunk_codes` |
| Entities | `entities`, `entity_aliases`, `item_entities`, `mentions` |
| Knowledge | `statements`, `statement_vecs`, `claims`, `claim_codes`, `statement_claims`, `claim_relations`, `statement_relations` |
| Topics and threads | `topics`, `topic_relations`, `topic_entities`, `topic_events`, `thread_items`, `thread_runs`, `thread_status` |
| Owner's decisions (kept across rebuilds) | `corrections`, `learned_aliases`, `thread_edits`, `owner_links`, `forgotten`, `audit`, `judgments` |
| Stage bookkeeping | `stage_runs`, `global_runs`, `store_meta` (one-off data upgrades done, vector encodings present), `speaker_relabels` (what a speaker relabel replaced, for undo), `migrations` (the upgrade steps applied) |

**Size, measured (the "All" dataset, 2026-10-21):** 268 MB for 165
meetings and 55 other items (9,494 passages, 20,879 statements, 690
threads), about 1.6 MB a meeting, plus 11 MB of raw blobs. Vectors are
most of it: statement vectors 86 MB, claim centroids 85 MB, passage
vectors 39 MB; statements 14 MB, passages 10 MB, items 10 MB, the
FTS index 4 MB. 99.6% of claims hold a single statement, so their
centroids repeat the statement's vector: a third of the file. Storing a
centroid only for claims of two or more, and vectors as float16, would
take it to about 120 MB, under half of today's size. Done 2026-11-12
(research log, "vector storage"): on the all-t4 copy (50,924 passages,
38,439 statements) 762.5 MB became 607.1 MB with centroids stored once
and 420.7 MB with float16 vectors.

**Scaling.** `tools/scale-bench` times the read paths on a synthetic
corpus shaped like the leadership meetings (research log, "Reads at
scale"): at 1,600 meetings (192k passages, 400k claims) vector search
takes 7–8 ms, a card lookup 12–15 ms, a topic page 0.3 s and the topic
list 4.2 s cold. At today's 1.6 MB a meeting a person's 1,000 meetings
are about 1.6 GB, well within SQLite; at company scale (tens of thousands
of meetings) the vector bytes and cold aggregate loads are what to cut
first ([scaling.md](scaling.md)), before any change of engine.

A dedicated graph database is not warranted at this scale (thousands of items
and entities). The schema is plain relational behind a `store` interface so it
can move to Postgres + pgvector (and Apache AGE if needed) without redesign.

Visualization (Wails frontend, as built): hand-written SVG for the entity
graph and the four topic views (no graph library). Trend charts are not
built.

## Sensitivity

Every item gets a label; every derived row inherits from its sources; every
read declares a scope and is filtered in SQL. This is what makes automatic uses
(drafted replies, shared-screen panels, cloud LLM calls) safe to add later.

### Label

A label has three parts:

- **Level** (ordered): `public` < `internal` < `confidential` < `restricted`.
- **Categories** (any of): `hr`, `compensation`, `legal`, `security`,
  `financial`, `deal-terms`, `customer-pii`, `health`, `strategy`, `personal`.
  A category can restrict an item regardless of level. `personal` (someone's
  life outside work) is set on knowledge points, not items; drafted replies
  never allow it.
- **Audience**: the principals who were party to the source (meeting attendees,
  email recipients, chat members). This is "need to know" taken from provenance:
  something learned in a 1:1 with one person is not automatically shareable with
  another. For a recording matched to a calendar event, the audience is the
  owner and the people heard speaking, not the invite list: a recording can
  run on after most invitees leave. Silent attendees and unidentified voices
  lose access, which only narrows reads. The invite list still sets the
  level, so a broad meeting stays visible to its attendees through
  `open_level`. `sensitivity.audience_from_speakers = false` restores the
  invite list.

**Default levels.** Only broadcasts (an audience of 15 or more, or a company
list) are `internal`. One-to-ones, working groups, anything with an external
participant, and anything with unidentified participants are `confidential`.
Group size is a poor proxy for sensitivity (an executive team meeting is a
group), so working groups default closed; `internal_min_audience` changes the
threshold.

Each label records its origin (`native`, `rule`, `model`, `user`), confidence
and reason. Precedence: `user` > `native` > `rule` > `model`. An item with no
confident label is treated as `confidential`; the default is to fail closed.

### Classification (the `classify` stage)

1. **Native labels:** Purview/MIP sensitivity labels on mail, calendar private
   flags, Salesforce object types.
2. **Rules:** audience size and makeup, external participants, subject and
   keyword patterns. Keyword rules catch formal terms well and conversation
   about people poorly; see the research log.
3. **Local model:** a classifier over item text for level and categories,
   applied where rules are silent or to raise a level, never to lower one.
4. **User overrides** from the UI, stored as labels with origin `user`.

Classification is a versioned stage like any other, so improving it means
re-running it. A hand-labelled set in `testdata/private/` measures it; recall
on `restricted` and on the categories matters more than precision.

### Propagation

- Chunks, mentions and embeddings inherit their item's label.
- A **fact** supported by several sources can be disclosed in a scope if *any*
  supporting source is visible in that scope. Each disclosure cites only the
  visible sources.
- **Entities** carry a level too: an entity's existence can itself be
  sensitive (a project codename, an acquisition target), set in the curated
  registry. An entity not in the registry or directory is visible only where
  some visible item links to it.
- **Aggregates and topics** compute per scope, or carry the highest label of
  their inputs, so counts and trend lines can't leak what the rows hide.

### Scoped reads

`store` exposes no query without a `Scope`:

```
scope: name, max_level, allowed_categories, audience[], open_level
```

`open_level` is what an audience may see from sources they weren't party to:
`internal` when every recipient is an employee, `public` as soon as anyone
outside the company is included.

| Purpose | Scope |
|---|---|
| Owner explores alone | everything, with labels shown |
| Side panel, normal | everything, with cards marked when other attendees weren't party to the source |
| Side panel, screen shared | `internal` and below, no categories; set by the screen-safe toggle |
| Drafted reply to X | items where X is in the audience, plus uncategorised `internal` (X an employee) or `public` (X external); never `personal` |
| Sending text to a cloud provider | the per-source egress policy, e.g. no email to cloud |
| The owner's assistant over MCP (`musubi mcp`) | the owner's (capped by `mcp.max_share`); nothing hidden, every result labelled with a share verdict and, for a named audience, whether it fits (docs/mcp-design.md) |

Egress to a cloud LLM is just another scope, so provider policy and
sensitivity are enforced by the same code. Scoped reads made for outbound
purposes (replies, cloud calls) are logged to an audit table.

## Providers

`provider.Embedder` and `provider.LLM` interfaces with local implementations
(ONNX Runtime embeddings; llama.cpp or Ollama for extraction) and a Claude
implementation. A stage asks for a provider by capability; the scope decides
which providers it may receive data for.

As built (2026-11-12): embeddings through Ollama (`embed.provider`;
`"hash"` for tests); chat models `"ollama"`, `"openai"` (any
OpenAI-compatible endpoint: Baseten, llama.cpp, the AI Gateway's Workers
AI shim) and `"anthropic"` (the Messages API, e.g. Claude through the AI
Gateway's `/anthropic-shim`), each with an optional `fallback`; typed
classifiers `"jev"`, `"clef"` and `"decider"` (a chat model's yes/no).
Models name a set-up endpoint with `via` (`[providers.*]`, or `"gateway"`
for `[gateway]`). The code's defaults are all local; Settings →
Processing → Use recommended writes `internal/config/recommended.toml`
(docs/setup.md). The recorder's speech models run in its own process on
ONNX Runtime through sherpa-onnx ("Desktop app").

Hosted calls stream. A reply streaming slower than `llm.stall_rate` (20
tokens a second) 30 seconds in (`llm.stall_after`) is sent again and the
first reply used: most slow calls are stalled streams, not long replies
(research log 2026-10-20).

## Deletion

Because every derived row points at its raw source, forgetting a source (one
email thread, one meeting, a whole connector) deletes the raw blob and cascades
through items, chunks, vectors, facts and edges, then marks affected aggregates
stale.

## Evaluation

- **Replay:** play a past meeting through Live with the stepped replay,
  rebuild what the panel showed at moments of it, and have blind graders
  judge each item against the conversation before it (research log,
  2026-10-21).
- **Classification:** precision and recall per level and category on a
  labelled set.
- **Resolution:** mention-to-entity accuracy on a labelled set.

Findings go in `docs/` research notes, dated, with the numbers.

## Packages

```
cmd/musubi/            CLI: update, search, ask, graph, show, stats, forget, knowledge,
                       claims, topics, topic, card, judge, gate, import-items, live, eval, version,
                       m365, people, mcp, vectors
cmd/musubi-gui/        Wails app (frontend embedded from frontend/dist)
cmd/musubi-recorder/   the meeting recorder: run, session, replay, shadow, migrate-from-tomoe, import
internal/
  config/              TOML config over defaults
  source/              connectors: tomoe/ (either recorder's sessions), bluedot/, mcpdrop/ (inbox drops)
  m365/                Microsoft Graph sign-in and sync: calendar, Teams transcripts and chats,
                       mail, directory (writes inbox drops)
  recorder/            the meeting recorder (recording, transcription, voices, live feed,
                       control socket); provenance in docs/recorder-provenance.md
  mcpserver/           `musubi mcp` tools
  raw/                 immutable blob store and manifest
  item/                item shape and per-source translators
  pipeline/            stage runner, version stamps, the knowledge run (extract, link,
                       topics or threads)
  stages/              classify, chunk, embed, resolve (mentions included)
  sensitivity/         labels, rules, scopes
  registry/            people and organisations for resolution
  store/               SQLite schema, scoped reads, FTS, vectors, graph, knowledge,
                       cards and threads tables
  knowledge/           statements, points, imports, values, linking, belief, topics,
                       threads, card index and cards
  engine/              opens the store and providers; search; card lookup
  ask/                 Q&A over scoped retrieval
  live/                live feed (recorder of record), replay, suggestions, briefs, topic cards
  provider/            embedders and chat models (Ollama, OpenAI-compatible, Anthropic), decider,
                       typed classifiers (Jev, Clef), fallbacks,
                       recorded-reply cache
  eval/                case harness (search, leak, classify, resolve, graph,
                       knowledge, card)
  gui/                 Wails backend
  phone/               phone access server: TLS, pairing, devices, agenda, chunk uploads
frontend/              React + TypeScript
tools/insights-eval/   extraction benchmark scripts (hosted and local models)
tools/link-bench/      linking benchmark: export, scorer, retrieval, thread prototypes
testdata/synthetic/    invented corpora and cases (committed)
testdata/private/      real data and references, gitignored
```

## First milestone

Ingest the Tomoe sessions and calendar; run `classify`, `chunk`, `embed`,
`mentions` and `resolve`; answer `musubi search` and `musubi graph <entity>`
from the CLI with scopes enforced. That validates the schema and the
sensitivity model before any UI exists.
