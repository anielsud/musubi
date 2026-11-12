import { createContext, useContext, useEffect, useState } from 'react';
import { api } from '../api';

// Navigation from deep in a view (a voice card, a review row) without
// passing handlers through every component: the app provides it.
type Nav = { openMeeting: (itemId: string) => void; openPerson: (entityId: string) => void; scope: string };

export const NavContext = createContext<Nav>({ openMeeting: () => {}, openPerson: () => {}, scope: 'owner' });

export function useNav() { return useContext(NavContext); }

// A recording's item in Musubi.
export const recordingItem = (session: string) => 'tomoe:' + session;

// useItemsExist asks once (per change of the list) which of ids Musubi
// has under the current scope; undefined until it knows.
export function useItemsExist(ids: string[]): Set<string> | undefined {
  const { scope } = useNav();
  const key = [...new Set(ids)].sort().join('\n');
  const [found, setFound] = useState<Set<string> | undefined>(undefined);
  useEffect(() => {
    let live = true;
    const want = key ? key.split('\n') : [];
    if (want.length === 0) { setFound(new Set()); return; }
    api.itemsExist(want, scope).then((x) => { if (live) setFound(new Set(x ?? [])); }, () => { if (live) setFound(new Set()); });
    return () => { live = false; };
  }, [key, scope]);
  return found;
}

// MeetingLink is a recording's meeting title: a link to the meeting on
// the Meetings tab when Musubi has it, else plain text that says why.
export function MeetingLink({ session, title, found }: { session: string; title: string; found: Set<string> | undefined }) {
  const { openMeeting } = useNav();
  const id = recordingItem(session);
  if (!found?.has(id)) {
    return <span title={found ? 'Not in Musubi: this recording was never imported (or isn’t shown in this scope)' : undefined}>{title}</span>;
  }
  return <span className="person-link" role="link" tabIndex={0} title="Open this meeting"
    onClick={(e) => { e.stopPropagation(); openMeeting(id); }}
    onKeyDown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); openMeeting(id); } }}>{title}</span>;
}
