import { useCallback, useEffect, useState } from 'react';
import { api, on } from './api';
import type { AppStatus, Hit, LiveStatus } from './types';
import { LiveView } from './components/LiveView';
import { AskView } from './components/AskView';
import { ExploreView } from './components/ExploreView';
import { StatusView } from './components/StatusView';
import { SourceDrawer, type SourceRef } from './components/SourceDrawer';

type View = 'live' | 'ask' | 'explore' | 'status';

export function App() {
  const [view, setView] = useState<View>('live');
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [live, setLive] = useState<LiveStatus | null>(null);
  const [source, setSource] = useState<SourceRef | null>(null);
  const [focus, setFocus] = useState<string | null>(null);
  const [onTop, setOnTop] = useState(false);
  const [toast, setToast] = useState('');

  const refresh = useCallback(() => {
    api.status().then((s) => {
      setStatus(s);
      setLive(s.live);
    }).catch(() => {});
  }, []);

  useEffect(() => {
    // Wails injects the bridge just after load.
    const t0 = setTimeout(refresh, 150);
    const t = setInterval(refresh, 30000);
    const offs = [
      on<AppStatus>('app:status', (s) => { setStatus(s); setLive(s.live); }),
      on<LiveStatus>('live:status', (s) => {
        setLive(s);
        if (s.mode === 'live') setView((v) => (v === 'status' ? 'live' : v));
      }),
      on<string>('update:done', (m) => { setToast('Updated: ' + m); refresh(); }),
    ];
    return () => { clearTimeout(t0); clearInterval(t); offs.forEach((f) => f()); };
  }, [refresh]);

  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(''), 4000);
    return () => clearTimeout(t);
  }, [toast]);

  const sharing = live?.scope === 'shared-screen';
  const scope = sharing ? 'shared-screen' : 'owner';
  const openHit = (h: Hit, sc: string) => setSource({ itemId: h.itemId, chunkId: h.chunkId, scope: sc });
  const openEntity = (id: string) => { setFocus(id); setView('explore'); };

  return (
    <div className="app">
      <header className="titlebar" style={{ ['--wails-draggable' as string]: 'drag' }}>
        <div className="brand"><span className="mark">結</span> Musubi</div>
        <div className="dots">
          <span className={'dot ' + (live?.connected ? 'ok' : 'off')} title={live?.connected ? 'Tomoe feed connected' : 'Tomoe feed not connected'}>T</span>
          <span className={'dot ' + (status?.ollamaUp ? 'ok' : 'off')} title={status?.ollamaUp ? 'Local models ready' : 'Ollama not reachable'}>M</span>
        </div>
        <div className="title-actions" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
          <button className={'toggle' + (sharing ? ' on' : '')} title="Screen-safe: show only what's fine on a shared screen"
            onClick={() => api.setSharing(!sharing).then(refresh)}>
            {sharing ? '◑ screen-safe' : '◐ private'}
          </button>
          <button className={'icon-btn' + (onTop ? ' on' : '')} title="Keep on top"
            onClick={() => { api.setOnTop(!onTop); setOnTop(!onTop); }}>📌</button>
        </div>
      </header>

      <nav className="tabs" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
        {(['live', 'ask', 'explore', 'status'] as View[]).map((v) => (
          <button key={v} className={view === v ? 'on' : ''} onClick={() => setView(v)}>
            {v === 'live' && live?.mode === 'live' && <span className="pulse" />}
            {v}
          </button>
        ))}
      </nav>

      <main>
        <div hidden={view !== 'live'}><LiveView status={live} onOpen={openHit} onEntity={openEntity} /></div>
        <div hidden={view !== 'ask'}><AskView onOpen={openHit} scope={scope} /></div>
        <div hidden={view !== 'explore'}>
          <ExploreView scope={scope} focus={focus} setFocus={setFocus} onOpen={openHit}
            onOpenItem={(id, sc) => setSource({ itemId: id, scope: sc })} />
        </div>
        <div hidden={view !== 'status'}><StatusView status={status} /></div>
      </main>

      {source && <SourceDrawer src={source} onClose={() => setSource(null)} />}
      {toast && <div className="toast">{toast}</div>}
    </div>
  );
}
