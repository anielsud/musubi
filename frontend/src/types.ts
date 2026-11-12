export type Level = 'public' | 'internal' | 'confidential' | 'restricted';

export interface Label {
  level: Level;
  categories: string[];
  reason?: string;
}

export interface Hit {
  chunkId: number;
  itemId: string;
  kind: string;
  title: string;
  occurredAt: string;
  seq: number;
  start: number;
  end: number;
  speakers: string;
  text: string;
  score: number;
  label: Label;
}

export interface Segment {
  id: string;
  speaker: string;
  text: string;
  start_time: number;
  end_time: number;
  status: string;
}

export interface Session {
  id: string;
  title: string;
  platform: string;
  created_at: string;
  // The other people on the calendar event under way, when there is one.
  invited?: string[];
}

export interface LiveStatus {
  connected: boolean;
  mode: 'idle' | 'live' | 'replay' | 'ended';
  session?: Session;
  scope: string;
  lines: number;
  // The panel shows what fits the conversation (live.focus); relevance:
  // Topic cards are picked (live.pick): the recent picks show in short.
  pick?: boolean;
  // what fits best takes the places (live.relevance).
  focus?: boolean;
  relevance?: boolean;
}

export interface Suggestion {
  query: string;
  why: string;
  kind: 'llm' | 'entity' | 'phrase' | 'typed';
  entityId?: string;
  score: number;
}

export interface Brief {
  query: string;
  why: string;
  kind: string;
  state: 'loading' | 'done' | 'empty' | 'error';
  summary: string;
  hits: Hit[] | null;
  at: string;
  spokenAgo: number;
  // The line of the conversation the lookup is about.
  quote?: string;
  // With relevance on: judged; has a place on the panel; had one; fit.
  rated?: boolean;
  open?: boolean;
  wasOpen?: boolean;
  fit?: number;
  summarizing: boolean;
  status?: { topic: number; name: string; asOf: string; now: string; changed?: string[] | null; open?: string[] | null };
}

export interface Card {
  id: string;
  name: string;
  type: string;
  items: number;
  lastSeen: string;
  neighbors: string[] | null;
  recent: string[] | null;
  mentions: number;
}

export interface TopicLine {
  claimId: number;
  kind: string;
  text: string;
  who?: string;
  said: string;
  meeting?: string;
  due?: string;
  dueAt?: string;
  status: string;
  note?: string;
  mine?: boolean;
  salience: number;
}

export interface TopicCard {
  topicId: number;
  // What raised the card: a topic, or a term (term cards span topics and
  // have no topic id).
  anchor: { topic?: number; term?: string };
  // Focus: the line that raised it, its lines closest to the conversation,
  // whether it stays open, and whether the subject is old.
  focus?: boolean;
  quote?: string;
  lines?: (TopicLine & { section: string; score: number })[] | null;
  active?: boolean;
  stale?: boolean;
  mentionedAgo?: number;
  // Pushed off the panel by newer cards, kept collapsed for the meeting.
  kept?: boolean;
  // live.pick.brief: where it stands, what to raise about it in this
  // meeting; for a kept card, its place among those most worth raising.
  headline?: string;
  raise?: string;
  // Other things worth raising, shown when the card is opened.
  raiseMore?: string[] | null;
  rank?: number;
  now?: string;
  title: string;
  terms?: string[] | null;
  last: string;
  meetings: number;
  label: { level: string; categories: string };
  collapsed: boolean;
  sections: { name: string; lines: TopicLine[] }[];
  people?: string[] | null;
  why: string;
  at: string;
  spokenAgo: number;
}

export interface EntityRef {
  id: string;
  type: string;
  name: string;
  items: number;
  lastSeen: string;
}

export interface ItemRef {
  id: string;
  kind: string;
  title: string;
  occurredAt: string;
  mentions: number;
  participant: boolean;
  label: Label;
}

export interface Graph {
  entity: EntityRef;
  items: ItemRef[] | null;
  neighbors: EntityRef[] | null;
}

export interface ItemView {
  id: string;
  kind: string;
  title: string;
  occurredAt: string;
  label: Label;
  chunks: Hit[];
}

export interface MeetingRef {
  id: string;
  ref: string;
  title: string;
  occurredAt: string;
  chunks: number;
}

export interface Stats {
  ItemsByKind: Record<string, number>;
  ItemLevels: Record<string, number>;
  ChunkLevels: Record<string, number>;
  Categories: Record<string, number>;
  Chunks: number;
  Entities: Record<string, number>;
  Mentions: number;
  Ambiguous: number;
  MeetingsLinked: number;
  Meetings: number;
}

export interface AppStatus {
  error?: string;
  owner: string;
  ollamaUp: boolean;
  chatModel: string;
  embedModel: string;
  updating: boolean;
  lastUpdate: string;
  stats?: Stats;
  live: LiveStatus;
  dataset: string;
  m365: boolean;
  onUpdate: boolean;
  autoMax: number;
  updateEvery: number;
}

export interface Statement {
  id: number;
  itemId: string;
  chunkId: number;
  chunkSeq: number;
  line: number;
  kind: string;
  text: string;
  // The text as written, when a spelling rule changed it for reading.
  original?: string;
  holder: string;
  holderId?: string;
  certainty: number;
  saidAt: string;
  value: { type: string; raw: string; lo: number; hi: number; normalized: boolean };
  quote: string;
  grounded: boolean;
  issues?: string;
  itemKind?: string;
  itemTitle?: string;
  label: Label;
  claimId?: number;
}

export interface Belief {
  score: number;
  support: number;
  against: number;
  status: string;
  holders: number;
  written: boolean;
  last: string;
  verdict?: string;
  why: string;
}

export interface RelView {
  rel: string;
  direction: 'in' | 'out';
  claimId: number;
  text: string;
  status: string;
}

export interface ClaimView {
  id: number;
  kind: string;
  text: string;
  holder: string;
  topicId: number;
  first: string;
  belief: Belief;
  statements: Statement[] | null;
  relations: RelView[] | null;
}

export interface TopicSummary {
  id: number;
  label: string;
  summary: string;
  keywords: string[] | null;
  labelHidden: boolean;
  claims: number;
  statuses: Record<string, number>;
  last: string;
  now?: string;
  x: number;
  y: number;
}

export interface TopicView extends TopicSummary {
  claimList: ClaimView[] | null;
  people: { name: string; id?: string; claims: number }[] | null;
  entities: { id: string; name: string; type: string; claims: number }[] | null;
  related: { id: number; label: string; sim: number; shared: number; kind: string; via?: string[]; unconnected: boolean }[] | null;
  events: { at: string; topicId: number; event: string; other?: number; detail: string }[] | null;
  thread?: ThreadDetail;
}

export interface StatusPoint {
  text: string;
  ids: number[] | null;
  date?: string;
  who?: string;
  due?: string;
}

export interface ThreadStatus {
  now: string;
  now_ids: number[] | null;
  changed: StatusPoint[] | null;
  open: StatusPoint[] | null;
  people: string[] | null;
}

// A to-do: something someone said would or should happen, and what came
// of it (knowledge.ToDo).
export interface ToDo extends Statement {
  status?: string;
  who?: string[] | null;
  owners: string[] | null;
  mine?: boolean;
  state: 'open' | 'quiet' | 'proposed' | 'done' | 'changed' | 'not';
  due?: string;
  dueAt?: string;
  overdue?: boolean;
  topic?: number;
  raised: number;
  lastRaised: string;
  // owner: the owner said it (a mark), not a statement from a source.
  outcome?: Statement & { rel: string; owner?: boolean };
  repeats?: Statement[] | null;
  topicLabel?: string;
  parent?: number;
  steps?: number;
  stepsDone?: number;
  marks?: OwnerLink[] | null;
  // From a meeting summary (knowledge.digest): who asked for it, and the
  // summary to-do's id. A negative id means no statement says the same, so
  // it has no thread and can't be marked yet.
  requester?: string;
  digest?: number;
  // The summary's reading: agreed, asked, floated, declined (summary to-dos).
  agreed?: 'agreed' | 'asked' | 'floated' | 'declined';
}

// OwnerLink is the owner's word on a to-do: a link (same, replaces, part)
// from statement to other, or a mark on statement (done, dropped, not, due).
export interface OwnerLink {
  id: number;
  statement: { item: string; text: string };
  rel: 'same' | 'replaces' | 'part' | 'done' | 'dropped' | 'not' | 'due' | 'who';
  other: { item: string; text: string };
  note?: string;
  at: string;
}

export interface ThreadItem extends Statement {
  status?: string;
  who?: string[] | null;
  topic: number;
  current: boolean;
}

export interface ThreadDetail {
  status?: ThreadStatus;
  asOf: string;
  partial: boolean;
  current: ThreadItem[] | null;
  retired: ThreadItem[] | null;
}

export interface ThreadRef {
  id: number;
  name: string;
  asOf: string;
  now?: string;
  status?: ThreadStatus;
}

export interface KnowledgeCounts {
  statements: number;
  claims: number;
  topics: number;
  relations: number;
  extracted: number;
  extractable: number;
}

export interface MapTopic extends TopicSummary {
  cluster: number; // -1: linked to nothing
  statements: number;
  activity: { item: string; n: number }[] | null;
}

export interface TopicEdge {
  a: number;
  b: number;
  w: number;
  kind: 'shared' | 'together' | 'same' | 'similar';
  shared: number;
  together: number;
  sim: number;
  via?: string[];
}

export interface TopicCluster {
  id: number;
  label: string;
  topics: number[];
  statements: number;
  last: string;
}

export interface TopicMapData {
  topics: MapTopic[] | null;
  edges: TopicEdge[] | null;
  clusters: TopicCluster[] | null;
  items: ItemRef[] | null;
  meaning: boolean;
}

export interface PrepTarget {
  itemId: string;
  title: string;
  at: string;
  upcoming: boolean;
  people: number;
  // past: a calendar event of the last week that has happened, with the
  // recording matched to it, if any.
  past?: boolean;
  recordingId?: string;
}

// SavedBrief is talking points drafted ahead of a meeting (prep.auto_brief).
export interface SavedBrief {
  itemId: string;
  model: string;
  text: string;
  sources: Hit[] | null;
  threads: ThreadRef[] | null;
  madeAt: string;
}

export interface PrepRequest {
  template?: string;
  people?: string[];
}

export interface PrepSubject {
  id: number;
  name: string;
  why: string;
  status?: ThreadStatus;
  asOf: string;
  newSince: ThreadItem[] | null;
  items: ThreadItem[] | null;
  private?: boolean;
}

export interface PrepView {
  title: string;
  people: string[] | null;
  since: string;
  met: number;
  subjects: PrepSubject[] | null;
  open: { text: string; who?: string; due?: string; thread: number; name: string; items: ThreadItem[] | null; raised: string; quiet?: boolean; overdue?: boolean; private?: boolean }[] | null;
  share: { thread: number; name: string; item: ThreadItem }[] | null;
  // last: the recap of this series' previous meeting, with its to-dos.
  last?: MeetingPage | null;
}

export interface AliasView {
  id: number;
  keep: string;
  variant: string;
  kind: string;
  status: string;
  score: number;
  statements: number;
  items: number;
  threads: number;
  newLinks: number;
  sample: string[] | null;
  newUses?: number;
  // The rule: always, these (only the statements in only), apart (both
  // forms right), review (undecided).
  mode: 'always' | 'these' | 'apart' | 'review';
  only?: number[] | null;
  // Proposals answered with this alias's kept form instead.
  replaced?: string[] | null;
}

export interface StatementEdit {
  id: number;
  statement: number;
  itemId: string;
  itemTitle: string;
  chunkId: number;
  field: 'text' | 'who';
  old: string;
  new: string;
  at: string;
  gone: boolean;
}

export interface AliasUsage {
  statement: number;
  itemId: string;
  chunkId: number;
  itemTitle: string;
  at: string;
  text: string;
  thread: string;
  threadId: number;
  current: boolean;
  hasKept: boolean;
  // Not reviewed yet: used since a rule for some uses was decided.
  new?: boolean;
}

export interface ThreadEditView {
  id: number;
  kind: 'merge' | 'split';
  name: string;
  oldName: string;
  moved: number;
  at: string;
  undone: boolean;
}

// The knowledge layer's waiting work (internal/gui/queue.go).
export interface QueueView {
  extract: string;
  frontier: string;
  threaded: number;
  newExtract: number;
  newThread: number;
  autoMax: number;
  olderExtract: number;
  olderThread: number;
  duplicates: number;
  catchUpUsd: number;
  rethreadUsd: number;
  rethreadHours: number;
  running: string;
  spent: number;
  ledger: string;
}

// A meeting summary (knowledge.digest): the meeting in sections, its to-dos
// and how they stand, and the threads its statements are in.
export interface MeetingSpan { fromChunk: number; fromLine: number; toChunk: number; toLine: number }

export interface MeetingSection {
  n: number;
  subject: string;
  text: string;
  people?: string[] | null;
  spans?: MeetingSpan[] | null;
  // grounded is false when a name or figure isn't in the meeting; issues says which.
  grounded: boolean;
  issues?: string;
}

export interface MeetingToDo {
  id: number;
  text: string;
  owners?: string[] | null;
  requester?: string;
  agreed: 'agreed' | 'asked' | 'floated' | 'declined';
  dueText?: string;
  dueDate?: string;
  evidence?: { chunkId: number; line: number; quote: string }[] | null;
  saidAt: string;
  grounded: boolean;
  issues?: string;
  // State as the to-do view reads it; "repeated" when a later meeting said
  // it again, "declined" when this meeting turned it down.
  state: 'open' | 'quiet' | 'proposed' | 'done' | 'changed' | 'not' | 'repeated' | 'declined';
  due?: string;
  topic?: number;
  topicLabel?: string;
}

export interface MeetingTopic { id: number; label: string; statements: number; size: number }

export interface MeetingPage {
  itemId: string;
  title: string;
  start: string;
  overview: string;
  sections: MeetingSection[] | null;
  todos: MeetingToDo[] | null;
  topics: MeetingTopic[] | null;
}

export interface MeetingListItem { itemId: string; title: string; start: string; overview: string; sections: number; todos: number }

export interface M365View {
  enabled: boolean;
  connected: boolean;
  account?: { username: string; name: string; tenantId: string; connectedAt: string };
  needsSignIn: boolean;
  lastSync: string;
  lastError?: string;
  lastReport?: string;
  connecting: boolean;
}

export interface GatewayView {
  url: string;
  account: string;
  tokenId: string;
  connected: boolean;
  configured: boolean;
}

export interface GatewayModel {
  id: string;
  kind: 'chat' | 'decision';
  provider: 'anthropic' | 'openai' | 'jev' | 'clef';
  priceIn: number;
  priceOut: number;
  context: number;
  retires: string;
  note?: string;
  efforts: string[] | null;
  defaultEffort: string;
}

export interface ProviderView {
  name: string;
  kind: 'gateway' | 'custom' | 'ollama' | 'onnx';
  title: string;
  url: string;
  ready: boolean;
  keyRef?: string;
  hasKey: boolean;
  formats: string[];
  headers?: Record<string, string> | null;
  usedBy: string[] | null;
  configured: boolean;
  tokenId?: string;
  startOllama: boolean;
  pullMissing: boolean;
  models?: string[] | null;
  error?: string;
}

export interface TestResult {
  ok: boolean;
  ms: number;
  models: number;
  error?: string;
}

export interface SlotView {
  kind: 'chat' | 'decision' | 'local';
  sections: string[];
  current: string;
  customText?: string;
  effort: string;
  fallback: string;
  fallbackText?: string;
  fallbackEffort: string;
  hasFallback: boolean;
}

export interface Rate {
  in: number;
  out: number;
  per: string;
}

export interface GroupView {
  key: string;
  title: string;
  does: string;
  slots: SlotView[];
  rate?: Rate;
}

export interface ModelRow {
  source: string;
  id: string;
  formats: string[] | null;
  kind: 'chat' | 'decision' | 'recording';
  priceIn: number;
  priceOut: number;
  context: number;
  efforts: string[] | null;
  defaultEffort: string;
  retires: string;
  note?: string;
  usedBy: string[] | null;
  size: number;
  // The recorder's ONNX models (source "onnx").
  name?: string;
  task?: string;
  missing?: boolean;
  restart?: boolean;
}

export interface ModelCatalog {
  rows: ModelRow[] | null;
  errors: Record<string, string> | null;
  pulls: string[] | null;
}

export interface PrefField {
  section: string;
  key: string;
  label: string;
  help: string;
  type: 'bool' | 'int' | 'string';
  unit?: string;
  value: boolean | number | string;
}

export interface PrefGroup {
  title: string;
  fields: PrefField[];
}

export interface StorageView {
  dataset: string;
  config: string;
  data: string;
  parts: { label: string; bytes: number }[] | null;
  total: number;
  backups: string;
  lastBackup: string;
}

export interface DirectoryPerson {
  email: string;
  name: string;
  givenName?: string;
  surname?: string;
  phones?: string[];
  mobile?: string;
  city?: string;
  country?: string;
  hireDate?: string;
  title?: string;
  department?: string;
  office?: string;
  managerEmail?: string;
  managerName?: string;
}

export interface PersonProfile extends DirectoryPerson {
  chain: DirectoryPerson[] | null;
  reports: DirectoryPerson[] | null;
  together: number;
  lastTogether: string;
}

export interface PersonRow {
  id: string;
  name: string;
  email?: string;
  title?: string;
  department?: string;
  together: number;
  lastTogether: string;
}

export interface MeetingRefP { itemId: string; title: string; at: string }

export interface PersonPage {
  id: string;
  name: string;
  email?: string;
  directory?: DirectoryPerson;
  chain: DirectoryPerson[] | null;
  reports: DirectoryPerson[] | null;
  peers: DirectoryPerson[] | null;
  seenWith: { id: string; name: string }[] | null;
  together: number;
  first: string;
  recent: MeetingRefP[] | null;
  upcoming: MeetingRefP[] | null;
  topics: { id: number; label: string; claims: number }[] | null;
  // Threads linked to theirs that they aren't in, with why (the link to
  // From, the closest of theirs).
  nearby: { id: number; label: string; kind: string; via?: string[]; from: string; links: number }[] | null;
  todos: ToDo[] | null;
  names: string[];
  headshot?: string;
  voice?: { session: string; start: number; end: number; title: string; at: string; text: string };
}

// The recorder (internal/recorder/control): its state, sources, tools,
// live labelling, window looks and voices, as the control socket sends
// them.
export interface RecSource { kind: string; id: string; name: string }
export interface RecSessionInfo { id: string; title: string; platform: string; created_at: string }
export interface RecStatus {
  version: string;
  recording: boolean;
  session?: RecSessionInfo;
  started_at?: string;
  mic: RecSource;
  system: RecSource;
  silent?: RecSource;
  suggest?: RecSource;
  health?: string[];
  busy?: string;
  restart?: string[];
  suggest_note?: string;
}
export interface RecorderView { running: boolean; status?: RecStatus; ofRecord: string; editable: boolean; error?: string }
export interface RecSources { mics: RecSource[] | null; system: RecSource[] | null }
export interface ToolFix { kind: 'action' | 'command'; label: string; command?: string }
export interface ToolStatus { id: string; name: string; group: string; ok: boolean; required: boolean; neededFor: string; detail: string; fix?: ToolFix }
export interface RecDiag { at: string; kind: string; line?: string; label?: string; detail: string; fields?: Record<string, string> }
export interface Ring { X: number; Y: number; Width: number; Height: number }
export interface LookView {
  id: number; time: string; sessionTime: number; stage: string; detail: string; window?: string; name?: string;
  fromCache?: boolean; usable: boolean; ring?: Ring; rings?: Ring[]; candidates?: string[]; width?: number; height?: number;
  thumb?: string; thumbOf?: number;
}
export interface VoiceClip { session: string; start: number; end: number }
export interface TileRef { session: string; look: number }
export interface VoiceMeeting { session: string; title: string; at: string; source: string; fingerprints: number; agreement: number; clip: VoiceClip; tiles: TileRef[] | null }
export interface VoiceProfile {
  model: string; id: string; name: string; meetings: number; fingerprints: number; fromTiles: number; fromNamed: number;
  lastHeard: string; clip: VoiceClip; heard: VoiceMeeting[] | null; rejected: number; startedOver: boolean;
  takenOut: VoiceMeeting[] | null; face?: TileRef; inUse: boolean; confirmed: boolean;
}
export interface NamedByVoice { session: string; title: string; at: string; label: string; name: string; model: string; profile: string; similarity: number; seconds: number; clip: VoiceClip; window?: string }
export interface VoiceDoubt { session: string; title: string; at: string; label: string; window: string; name: string; similarity: number; seconds: number; clip: VoiceClip; noProfile: boolean }
export interface VoicePair { model: string; a: string; b: string; similarity: number }
export interface SameNamePair { model: string; a: string; b: string; name: string; similarity: number; meetingsA: number; meetingsB: number; clipA: VoiceClip; clipB: VoiceClip }
export interface UnknownVoice { session: string; title: string; at: string; label: string; seconds: number; clip: VoiceClip; soundsLike?: string; similarity?: number }
export interface VoicesView {
  enabled: boolean; profiles: VoiceProfile[] | null; pairs: VoicePair[] | null; unknown: UnknownVoice[] | null;
  namedByVoice: NamedByVoice[] | null; doubts: VoiceDoubt[] | null; sameName?: SameNamePair[] | null;
}
export interface VoiceArgs { model: string; id: string; other?: string; session?: string; name?: string; to?: string }
export interface SpeakerCount { label: string; lines: number; seconds: number }
export interface MeetingSpeakers { session: string; speakers: SpeakerCount[]; editable: boolean; why?: string }
export interface RecOption { value: string; label: string }
export interface RecField { table: string; key: string; label: string; help?: string; type: 'bool' | 'int' | 'float' | 'string' | 'select'; step?: number; min: number; options?: RecOption[]; value: unknown; set: boolean; restart?: boolean }
export interface RecGroup { title: string; fields: RecField[] }
export interface RecModelField extends RecField { missing?: string[] | null; inUse?: string }
export interface RecordingModels { fields: RecModelField[]; error?: string }
export interface RecordingSettings { ofRecord: string; since: string; groups: RecGroup[]; tomoeConfig: string }
