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
}
