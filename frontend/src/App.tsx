import { useCallback, useEffect, useState } from 'react';
import { api, on } from './api';
import type { AppStatus, DatasetView, Hit, LiveStatus } from './types';
import { LiveView } from './components/LiveView';
import { AskView } from './components/AskView';
import { PrepView } from './components/PrepView';
import { ExploreView } from './components/ExploreView';
import { StatusView } from './components/StatusView';
import { SourceDrawer, type SourceRef } from './components/SourceDrawer';
import { DiagPanel } from './components/DiagPanel';

type View = 'live' | 'ask' | 'prep' | 'explore' | 'status';

export function App() {
  const [view, setView] = useState<View>('live');
  const [status, setStatus] = useState<AppStatus | null>(null);
  const [live, setLive] = useState<LiveStatus | null>(null);
  const [source, setSource] = useState<SourceRef | null>(null);
  const [focus, setFocus] = useState<string | null>(null);
  const [onTop, setOnTop] = useState(false);
  const [diag, setDiag] = useState(false);
  const [toast, setToast] = useState('');
  const [wideWin, setWideWin] = useState(false);

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
  const [topicReq, setTopicReq] = useState<{ id: number; n: number } | null>(null);
  const openTopic = (id: number) => { setFocus(null); setTopicReq({ id, n: Date.now() }); setView('explore'); };

  return (
    <div className={'app' + (diag ? ' with-diag' : '')}>
      <div className="app-main">
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
          <button className={'icon-btn' + (wideWin ? ' on' : '')} title={wideWin ? 'Back to the side panel' : 'Landscape: most of the screen wide'}
            onClick={() => { api.setWide(!wideWin); setWideWin(!wideWin); }}>{wideWin ? '▯' : '▭'}</button>
          <button className={'icon-btn' + (onTop ? ' on' : '')} title="Keep on top"
            onClick={() => { api.setOnTop(!onTop); setOnTop(!onTop); }}>📌</button>
          <button className={'icon-btn' + (diag ? ' on' : '')} title="Diagnostics: how Musubi is processing"
            onClick={() => { api.setDiagnostics(!diag); setDiag(!diag); }}>⚙︎</button>
        </div>
      </header>

      <DatasetPicker onError={setToast} />

      <nav className="tabs" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
        {(['live', 'ask', 'prep', 'explore', 'status'] as View[]).map((v) => (
          <button key={v} className={view === v ? 'on' : ''} onClick={() => setView(v)}>
            {v === 'live' && live?.mode === 'live' && <span className="pulse" />}
            {v}
          </button>
        ))}
      </nav>

      <main>
        <div hidden={view !== 'live'}><LiveView status={live} onOpen={openHit} onEntity={openEntity} onTopic={openTopic} /></div>
        <div hidden={view !== 'ask'}><AskView onOpen={openHit} scope={scope} onTopic={openTopic} /></div>
        <div hidden={view !== 'prep'}><PrepView onOpen={openHit} scope={scope} onTopic={openTopic} /></div>
        <div hidden={view !== 'explore'}>
          <ExploreView scope={scope} focus={focus} setFocus={setFocus} onOpen={openHit} topicReq={topicReq}
            onOpenItem={(id, sc) => setSource({ itemId: id, scope: sc })} />
        </div>
        <div hidden={view !== 'status'}><StatusView status={status} /></div>
      </main>

      {source && <SourceDrawer src={source} onClose={() => setSource(null)} />}
      {toast && <div className="toast">{toast}</div>}
      </div>
      {diag && <DiagPanel />}
    </div>
  );
}

// DatasetPicker switches this window to another dataset, or opens one in a
// window of its own to compare side by side.
function DatasetPicker({ onError }: { onError: (m: string) => void }) {
  const [sets, setSets] = useState<DatasetView[]>([]);
  const [other, setOther] = useState('');
  useEffect(() => {
    const t = setTimeout(() => api.datasets().then((d) => setSets(d ?? [])).catch(() => {}), 200);
    return () => clearTimeout(t);
  }, []);
  if (sets.length === 0) return null;
  const current = sets.find((d) => d.current)?.key ?? '';
  const switchTo = (key: string) => {
    if (!key || key === current) return;
    api.switchDataset(key).then(() => window.location.reload(), (e) => onError(String(e)));
  };
  const openWindow = () => {
    const key = other || sets.find((d) => d.available && !d.current)?.key;
    if (key) api.openDatasetWindow(key).catch((e) => onError(String(e)));
  };
  return (
    <div className="datasets" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
      <select value={current} onChange={(e) => switchTo(e.target.value)} title="Dataset shown in this window">
        {!current && <option value="">default config</option>}
        {sets.map((d) => (
          <option key={d.key} value={d.key} disabled={!d.available} title={d.note}>
            {d.name}{d.available ? '' : ' (not built)'}
          </option>
        ))}
      </select>
      <select className="other" value={other} onChange={(e) => setOther(e.target.value)} title="Dataset to open in a new window">
        <option value="">compare with…</option>
        {sets.filter((d) => d.available && !d.current).map((d) => <option key={d.key} value={d.key}>{d.name}</option>)}
      </select>
      <button className="icon-btn" title="Open the chosen dataset in a new window, side by side" onClick={openWindow}>⧉</button>
    </div>
  );
}
