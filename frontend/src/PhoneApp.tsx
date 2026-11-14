import { useState } from 'react';
import type { Hit } from './types';
import { ExploreView } from './components/ExploreView';
import { AskCard, askExamples, useAsk } from './components/AskView';
import { PeopleProvider } from './components/people';
import { NavContext } from './components/nav';
import { SourceDrawer, type SourceRef } from './components/SourceDrawer';
import { Spinner } from './components/common';

// PhoneApp is phone mode (?phone=1, phone.ts): Explore and Ask, read-only,
// in the phone's WebView. The screen-safe switch is the phone's own: it
// only picks the scope these screens read with.
export function PhoneApp() {
  const [view, setView] = useState<'explore' | 'ask'>('explore');
  const [sharing, setSharing] = useState(false);
  const [source, setSource] = useState<SourceRef | null>(null);
  const [focus, setFocus] = useState<string | null>(null);
  const [topicReq, setTopicReq] = useState<{ id: number; n: number } | null>(null);
  const scope = sharing ? 'shared-screen' : 'owner';
  const openHit = (h: Hit, sc: string) => setSource({ itemId: h.itemId, chunkId: h.chunkId, scope: sc });
  const openPerson = (id: string) => { setSource(null); setFocus(id); setView('explore'); };
  const openTopic = (id: number) => { setSource(null); setFocus(null); setTopicReq({ id, n: Date.now() }); setView('explore'); };
  // No Meetings tab here: a meeting opens as its transcript.
  const openItem = (id: string, sc: string, at?: number) => setSource({ itemId: id, scope: sc, at });

  return (
    <div className="app phone">
      <div className="app-main">
        <header className="titlebar">
          <div className="brand"><span className="mark">結</span> Musubi</div>
          <div className="title-actions">
            <button className={'toggle' + (sharing ? ' on' : '')} title="Screen-safe: show only what's fine on a shared screen"
              onClick={() => setSharing(!sharing)}>
              {sharing ? '◑ screen-safe' : '◐ private'}
            </button>
          </div>
        </header>
        <nav className="tabs">
          {(['explore', 'ask'] as const).map((v) => (
            <button key={v} className={view === v ? 'on' : ''} onClick={() => setView(v)}>{v}</button>
          ))}
        </nav>
        <NavContext.Provider value={{ openMeeting: (id) => openItem(id, scope), openPerson, openTranscript: (id, at) => openItem(id, scope, at), scope }}>
          <PeopleProvider scope={scope} onOpen={openPerson}>
            <main>
              <div hidden={view !== 'explore'}>
                <ExploreView scope={scope} focus={focus} setFocus={setFocus} onOpen={openHit} topicReq={topicReq}
                  onOpenItem={(id, sc) => openItem(id, sc)} />
              </div>
              <div hidden={view !== 'ask'}><PhoneAsk scope={scope} onOpen={openHit} onTopic={openTopic} /></div>
            </main>
            {source && <SourceDrawer src={source} onClose={() => setSource(null)} />}
          </PeopleProvider>
        </NavContext.Provider>
      </div>
    </div>
  );
}

// PhoneAsk is Ask on its own: the desktop's lives in the Live panel.
function PhoneAsk({ scope, onOpen, onTopic }: { scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void }) {
  const ask = useAsk(scope);
  const [q, setQ] = useState('');
  return (
    <div className="view phone-ask">
      <form className="lookup" onSubmit={(e) => { e.preventDefault(); if (ask.send(q)) setQ(''); }}>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ask about your meetings…" enterKeyHint="send" />
        {ask.busy && <Spinner />}
      </form>
      {ask.turns.length === 0 && (
        <div className="examples">
          {askExamples.map((e) => <button key={e} className="chip" onClick={() => ask.send(e)}>{e}</button>)}
        </div>
      )}
      {ask.turns.map((t) => <AskCard key={t.id} t={t} onOpen={onOpen} onTopic={onTopic} onClose={() => ask.dismiss(t.id)} />)}
    </div>
  );
}
