// Bridge to the Go backend (internal/gui). Wails injects window.go and
// window.runtime at load.
import type { AppStatus, EntityRef, Graph, Hit, ItemView, LiveStatus, MeetingRef } from './types';

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
  setOnTop: (on: boolean): Promise<void> => app().SetOnTop(on),
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
