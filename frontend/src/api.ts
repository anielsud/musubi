// Bridge to the Go backend (internal/gui). Wails injects window.go and
// window.runtime at load.
import type { GatewayModel, GatewayView, GroupView, ModelCatalog, PrefGroup, ProviderView, StorageView, TestResult, M365View, PersonProfile, PersonRow, PersonPage, AliasUsage, AliasView, AppStatus, ClaimView, MeetingListItem, MeetingPage, QueueView, EntityRef, Graph, Hit, ItemView, KnowledgeCounts, LiveStatus, MeetingRef, PrepRequest, PrepTarget, PrepView, SavedBrief, StatementEdit, ThreadEditView, ToDo, TopicMapData, TopicSummary, TopicView } from './types';
import type { LookView, MeetingSpeakers, RecDiag, RecordingModels, RecordingSettings, RecorderView, RecSources, RecStatus, ToolStatus, VoiceArgs, VoiceClip, VoicesView } from './types';

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
  toDos: (proposals: boolean, scope: string): Promise<ToDo[] | null> => app().ToDos(proposals, scope),
  meetingList: (scope: string, limit = 200): Promise<MeetingListItem[] | null> => app().MeetingList(scope, limit),
  meetingPage: (id: string, scope: string): Promise<MeetingPage | null> => app().MeetingPage(id, scope),
  linkToDo: (id: number, rel: string, other: number, note = ''): Promise<number> => app().LinkToDo(id, rel, other, note),
  undoToDoLink: (id: number): Promise<void> => app().UndoToDoLink(id),
  savedPrepBrief: (itemId: string, scope: string): Promise<SavedBrief | null> => app().SavedPrepBrief(itemId, scope),
  prep: (req: PrepRequest, scope: string): Promise<PrepView> => app().Prep(req, scope),
  prepBrief: (id: string, req: PrepRequest, scope: string): Promise<void> => app().PrepBrief(id, req, scope),
  m365: (): Promise<M365View> => app().M365(),
  m365Connect: (): Promise<void> => app().M365Connect(),
  m365SignOut: (): Promise<void> => app().M365SignOut(),
  gateway: (): Promise<GatewayView> => app().Gateway(),
  gatewayConnect: (url: string, account: string, token: string): Promise<void> => app().GatewayConnect(url, account, token),
  gatewayDisconnect: (): Promise<void> => app().GatewayDisconnect(),
  gatewayModels: (): Promise<GatewayModel[] | null> => app().GatewayModels(),
  groups: (): Promise<GroupView[] | null> => app().Groups(),
  models: (): Promise<ModelCatalog> => app().Models(),
  setModels: (group: string, choices: string[], fallbacks: string[], efforts: string[], fallbackEfforts: string[]): Promise<void> => app().SetModels(group, choices, fallbacks, efforts, fallbackEfforts),
  pullModel: (name: string): Promise<void> => app().PullModel(name),
  removeModel: (name: string): Promise<void> => app().RemoveModel(name),
  applyRecommended: (): Promise<void> => app().ApplyRecommended(),
  providers: (): Promise<ProviderView[] | null> => app().Providers(),
  saveProvider: (name: string, url: string, headers: Record<string, string>, key: string): Promise<void> => app().SaveProvider(name, url, headers, key),
  deleteProvider: (name: string): Promise<void> => app().DeleteProvider(name),
  setOllama: (url: string, start: boolean, pull: boolean): Promise<void> => app().SetOllama(url, start, pull),
  testProvider: (name: string): Promise<TestResult> => app().TestProvider(name),
  preferences: (tab: string): Promise<PrefGroup[] | null> => app().Preferences(tab),
  setPreference: (section: string, key: string, value: boolean | number | string): Promise<void> => app().SetPreference(section, key, value),
  storage: (): Promise<StorageView> => app().Storage(),
  backup: (): Promise<string> => app().Backup(),
  revealPath: (p: string): Promise<void> => app().RevealPath(p),
  personProfile: (id: string, scope: string): Promise<PersonProfile | null> => app().PersonProfile(id, scope),
  people: (scope: string): Promise<PersonRow[] | null> => app().People(scope),
  headshot: (id: string, scope: string): Promise<string> => app().Headshot(id, scope),
  personPage: (id: string, scope: string): Promise<PersonPage | null> => app().PersonPage(id, scope),
  peopleNames: (scope: string): Promise<Record<string, string> | null> => app().PeopleNames(scope),
  recorderState: (): Promise<RecorderView> => app().RecorderState(),
  recorderStart: (): Promise<RecStatus> => app().RecorderStart(),
  recorderStop: (): Promise<RecStatus> => app().RecorderStop(),
  recorderSources: (): Promise<RecSources> => app().RecorderSources(),
  recorderSetSource: (kind: string, id: string): Promise<RecStatus> => app().RecorderSetSource(kind, id),
  recorderTools: (): Promise<ToolStatus[] | null> => app().RecorderTools(),
  recorderFixTool: (id: string): Promise<void> => app().RecorderFixTool(id),
  copyText: (s: string): Promise<void> => app().CopyText(s),
  recorderDiags: (): Promise<RecDiag[] | null> => app().RecorderDiags(),
  recorderLiveLooks: (): Promise<{ session: string; looks: LookView[] | null }> => app().RecorderLiveLooks(),
  recorderLooks: (session: string): Promise<LookView[] | null> => app().RecorderLooks(session),
  voices: (): Promise<VoicesView> => app().Voices(),
  recorderSpeaker: (op: string, session: string, label: string, name = ''): Promise<void> => app().RecorderSpeaker(op, session, label, name),
  recorderVoice: (op: string, args: VoiceArgs): Promise<void> => app().RecorderVoice(op, args),
  learnSaved: (): Promise<number> => app().LearnSaved(),
  discardedClip: (model: string, id: string, session: string): Promise<VoiceClip> => app().DiscardedClip(model, id, session),
  meetingSpeakers: (itemId: string): Promise<MeetingSpeakers | null> => app().MeetingSpeakers(itemId),
  deleteMeeting: (itemId: string): Promise<void> => app().DeleteMeeting(itemId),
  recordingSettings: (): Promise<RecordingSettings> => app().RecordingSettings(),
  setRecordingSetting: (table: string, key: string, value: boolean | number | string): Promise<void> => app().SetRecordingSetting(table, key, value),
  setOfRecord: (v: string): Promise<void> => app().SetOfRecord(v),
  recordingModels: (): Promise<RecordingModels> => app().RecordingModels(),
  downloadRecorderModel: (id: string): Promise<void> => app().DownloadRecorderModel(id),
};

// Recorder media, served by the app (internal/gui recorderMedia).
export const clipURL = (c: VoiceClip) => `/recorder-media/clip?session=${encodeURIComponent(c.session)}&start=${c.start}&end=${c.end}`;
export const tileURL = (session: string, look: number) => `/recorder-media/tile/${encodeURIComponent(session)}/${look}`;
export const thumbURL = (session: string, look: number) => `/recorder-media/thumb/${encodeURIComponent(session)}/${look}`;

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
