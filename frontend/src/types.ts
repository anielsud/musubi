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
}

export interface LiveStatus {
  connected: boolean;
  mode: 'idle' | 'live' | 'replay' | 'ended';
  session?: Session;
  scope: string;
  lines: number;
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
  config: Record<string, string>;
  dataset: string;
}

export interface DatasetView {
  key: string;
  name: string;
  note?: string;
  available: boolean;
  current: boolean;
}

export interface Statement {
  id: number;
  itemId: string;
  chunkId: number;
  chunkSeq: number;
  line: number;
  kind: string;
  text: string;
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

export interface CompareTopic {
  id: number;
  label: string;
  statements: number;
  cluster: number;
}

export interface CompareRow {
  a: CompareTopic;
  matches: { b: CompareTopic; coverA: number; coverB: number }[] | null;
  status: 'same' | 'split' | 'merged' | 'partial' | 'only';
  with?: string[];
}

export interface Comparison {
  mode: 'statements' | 'wording';
  rows: CompareRow[] | null;
  onlyB: CompareTopic[] | null;
  counts: Record<string, number>;
  sizes: Record<string, number>;
  shared: number;
}

export interface PrepTarget {
  itemId: string;
  title: string;
  at: string;
  upcoming: boolean;
  people: number;
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
}

export interface PrepView {
  title: string;
  people: string[] | null;
  since: string;
  met: number;
  subjects: PrepSubject[] | null;
  open: { text: string; who?: string; due?: string; thread: number; name: string; items: ThreadItem[] | null }[] | null;
  share: { thread: number; name: string; item: ThreadItem }[] | null;
}
