// Bridge to the Go backend (internal/gui). Wails injects window.go and
// window.runtime at load.
import type { AliasUsage, AliasView, AppStatus, ClaimView, QueueView, EntityRef, Graph, Hit, ItemView, KnowledgeCounts, LiveStatus, MeetingRef, PrepRequest, PrepTarget, PrepView, StatementEdit, ThreadEditView, TopicMapData, TopicSummary, TopicView } from './types';

/* eslint-disable @typescript-eslint/no-explicit-any */
declare global {
  interface Window {
    go: any;
    runtime: any;
  }
}

const app = () => window.go?.gui?.App;

export const api = {
  status: (): Promise<AppStatus> => app().Status(),
  update: (): Promise<void> => app().Update(),
  queue: (): Promise<QueueView> => app().Queue(),
  runKnowledge: (kind: 'catchup' | 'rethread'): Promise<void> => app().RunKnowledge(kind),
  search: (q: string, scope: string, k = 12): Promise<Hit[] | null> => app().Search(q, scope, k),
  ask: (id: string, q: string, scope: string): Promise<void> => app().Ask(id, q, scope),
  stopAsk: (): Promise<void> => app().StopAsk(),
  entities: (q: string, scope: string): Promise<EntityRef[] | null> => app().Entities(q, scope),
  graph: (id: string, scope: string): Promise<Graph | null> => app().Graph(id, scope),
  item: (id: string, scope: string): Promise<ItemView | null> => app().Item(id, scope),
  liveStatus: (): Promise<LiveStatus> => app().LiveStatus(),
  setSharing: (on: boolean): Promise<void> => app().SetSharing(on),
  lookUp: (q: string, why: string, kind: string): Promise<void> => app().LookUp(q, why, kind),
  meetings: (): Promise<MeetingRef[] | null> => app().Meetings(),
  replay: (ref: string, speed: number): Promise<void> => app().Replay(ref, speed),
  stopReplay: (): Promise<void> => app().StopReplay(),
  replayWithAudio: (ref: string): Promise<void> => app().ReplayWithAudio(ref),
  replayClock: (t: number): Promise<void> => app().ReplayClock(t),
  hasAudio: (ref: string): Promise<boolean> => app().HasAudio(ref),
  setOnTop: (on: boolean): Promise<void> => app().SetOnTop(on),
  setDiagnostics: (on: boolean): Promise<void> => app().SetDiagnostics(on),
  knowledgeCounts: (): Promise<KnowledgeCounts> => app().KnowledgeCounts(),
  topics: (scope: string): Promise<TopicSummary[] | null> => app().Topics(scope),
  topic: (id: number, scope: string): Promise<TopicView | null> => app().Topic(id, scope),
  claim: (id: number, scope: string): Promise<ClaimView | null> => app().Claim(id, scope),
  searchClaims: (q: string, scope: string): Promise<ClaimView[] | null> => app().SearchClaims(q, scope),
  judge: (statementId: number, verdict: string, note = ''): Promise<void> => app().Judge(statementId, verdict, note),
  topicMap: (scope: string): Promise<TopicMapData> => app().TopicMap(scope),
  setWide: (on: boolean): Promise<void> => app().SetWide(on),
  aliases: (status: string, limit = 100): Promise<AliasView[] | null> => app().Aliases(status, limit),
  aliasUsages: (id: number, limit = 200): Promise<AliasUsage[] | null> => app().AliasUsages(id, limit),
  confirmAliasIn: (id: number, statements: number[], reviewed: number[], all: boolean): Promise<void> => app().ConfirmAliasIn(id, statements, reviewed, all),
  addAlias: (variant: string, keep: string): Promise<number> => app().AddAlias(variant, keep),
  confirmAliasAs: (id: number, keep: string, statements: number[], reviewed: number[], all: boolean): Promise<void> => app().ConfirmAliasAs(id, keep, statements, reviewed, all),
  decideAlias: (id: number, confirm: boolean): Promise<void> => app().DecideAlias(id, confirm),
  setAliasRule: (id: number, mode: string, keep: string, statements: number[], reviewed: number[]): Promise<number> => app().SetAliasRule(id, mode, keep, statements, reviewed),
  statementEdits: (): Promise<StatementEdit[] | null> => app().StatementEdits(),
  undoStatementEdit: (id: number): Promise<void> => app().UndoStatementEdit(id),
  correctStatement: (id: number, text: string, who: string): Promise<void> => app().CorrectStatement(id, text, who),
  mergeThreads: (into: number, from: number): Promise<number> => app().MergeThreads(into, from),
  splitThread: (from: number, ids: number[], name: string): Promise<number> => app().SplitThread(from, ids, name),
  undoThreadEdit: (id: number): Promise<void> => app().UndoThreadEdit(id),
  threadEdits: (): Promise<ThreadEditView[] | null> => app().ThreadEdits(),
  prepTargets: (): Promise<PrepTarget[] | null> => app().PrepTargets(),
  prep: (req: PrepRequest, scope: string): Promise<PrepView> => app().Prep(req, scope),
  prepBrief: (id: string, req: PrepRequest, scope: string): Promise<void> => app().PrepBrief(id, req, scope),
};

export function on<T>(event: string, fn: (data: T) => void): () => void {
  if (!window.runtime) return () => {};
  return window.runtime.EventsOn(event, fn);
}

export function fmtDate(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime()) || d.getFullYear() < 2000) return '';
  const now = new Date();
  // Calendar days, not 24-hour periods: last night is "yesterday".
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((day(now) - day(d)) / 86400000);
  if (days === 0) return 'today ' + d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  if (days === 1) return 'yesterday';
  if (days < 7) return d.toLocaleDateString([], { weekday: 'short' }) + ' ' + d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

export function speakerName(s: string): string {
  const m = s.match(/^Person \d+ \((.+)\)$/);
  return m ? m[1] : s;
}

// The scope a source item is opened under follows the view it came from.
export const kindIcon: Record<string, string> = { meeting: '◐', email: '✉', chat: '◌', event: '▢' };
