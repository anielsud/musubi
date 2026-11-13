import { useCallback, useEffect, useState } from 'react';
import { api, on } from './api';
import type { AppStatus, Hit, LiveStatus } from './types';
import { LiveView } from './components/LiveView';
import { ToDoView } from './components/ToDoView';
import { MeetingsView } from './components/MeetingsView';
import { ExploreView } from './components/ExploreView';
import { StatusView } from './components/StatusView';
import { SettingsView } from './components/SettingsView';
import { PeopleProvider } from './components/people';
import { NavContext } from './components/nav';
import { SourceDrawer, type SourceRef } from './components/SourceDrawer';
import { DiagPanel } from './components/DiagPanel';
import { RecordButton } from './components/recorder';
import { SelectionMenu, type Picked } from './components/SelectionMenu';

type View = 'live' | 'meetings' | 'to-do' | 'explore' | 'status' | 'settings';

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
  const [askUpdate, setAskUpdate] = useState(false);
  const [canInstall, setCanInstall] = useState(false);
  const [updStep, setUpdStep] = useState('');

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
      on<string>('selfupdate:step', (m) => setUpdStep(m + '…')),
      on<string>('selfupdate:failed', (m) => { setUpdStep(''); setToast('Update failed: ' + m); }),
    ];
    return () => { clearTimeout(t0); clearInterval(t); offs.forEach((f) => f()); };
  }, [refresh]);

  useEffect(() => {
    if (askUpdate) api.canInstallUpdate().then(setCanInstall, () => setCanInstall(false));
  }, [askUpdate]);

  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(''), 4000);
    return () => clearTimeout(t);
  }, [toast]);

  const sharing = live?.scope === 'shared-screen';
  const scope = sharing ? 'shared-screen' : 'owner';
  const openHit = (h: Hit, sc: string) => setSource({ itemId: h.itemId, chunkId: h.chunkId, scope: sc });
  const openEntity = (id: string) => { setFocus(id); setView('explore'); };
  const openPerson = (id: string) => { setSource(null); openEntity(id); };
  const [topicReq, setTopicReq] = useState<{ id: number; n: number } | null>(null);
  const [meetingReq, setMeetingReq] = useState<{ id: string; n: number } | null>(null);
  const openMeeting = (id: string) => { setMeetingReq({ id, n: Date.now() }); setView('meetings'); };
  const openTopic = (id: number) => { setFocus(null); setTopicReq({ id, n: Date.now() }); setView('explore'); };
  // From a text selection: a question to ask, or a word to correct.
  const [askReq, setAskReq] = useState<{ text: string; n: number } | null>(null);
  const [correctReq, setCorrectReq] = useState<{ variant: string; n: number } | null>(null);
  const askAbout = (p: Picked) => {
    const from = p.title ? ` (from ${p.title}${p.at ? ', ' + new Date(p.at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) : ''})` : '';
    setAskReq({ text: `What do we know about “${p.text}”${from}?`, n: Date.now() });
    setView('live');
  };
  const correct = (p: Picked) => { setFocus(null); setCorrectReq({ variant: p.text, n: Date.now() }); setView('explore'); };

  return (
    <div className={'app' + (diag ? ' with-diag' : '')}>
      <div className="app-main">
      <header className="titlebar" style={{ ['--wails-draggable' as string]: 'drag' }}>
        <div className="brand"><span className="mark">結</span> Musubi</div>
        <div className="dots">
          {status?.liveFrom === 'Musubi recorder'
            ? <span className={'dot ' + (live?.connected ? 'ok' : 'off')} title={live?.connected ? 'Live feed connected (Musubi recorder)' : 'Live feed not connected (Musubi recorder)'}>L</span>
            : <span className={'dot ' + (live?.connected ? 'ok' : 'off')} title={live?.connected ? 'Tomoe feed connected' : 'Tomoe feed not connected'}>T</span>}
          <span className={'dot ' + (status?.ollamaUp ? 'ok' : 'off')} title={status?.ollamaUp ? 'Local models ready' : 'Ollama not reachable'}>M</span>
        </div>
        <div className="title-actions" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
          {status?.newVersion && (
            <span className="update-wrap">
              <button className="toggle on" title={`You have ${status.version}; ${status.newVersion.version} is out.`}
                disabled={updStep !== ''} onClick={() => setAskUpdate(!askUpdate)}>
                {updStep || `↑ ${status.newVersion.version}`}
              </button>
              {askUpdate && (
                <div className="update-pop">
                  <p>Musubi {status.newVersion.version} is out{status.newVersion.prerelease ? ' (a release candidate)' : ''}; you have {status.version}.</p>
                  {canInstall
                    ? <p className="dim small">Update downloads it, checks it, replaces this copy and restarts Musubi. Not while a meeting is being recorded.</p>
                    : <p className="dim small">This copy can&apos;t update itself: download it from the release page.</p>}
                  <div className="row-buttons">
                    {canInstall && <button onClick={() => { setAskUpdate(false); api.installUpdate().then(() => setUpdStep('Starting…'), (e) => setToast(String(e))); }}>Update</button>}
                    <button onClick={() => { setAskUpdate(false); api.openNewVersion().catch((e) => setToast(String(e))); }}>Release page</button>
                    <button onClick={() => setAskUpdate(false)}>Not now</button>
                  </div>
                </div>
              )}
            </span>
          )}
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

      <nav className="tabs" style={{ ['--wails-draggable' as string]: 'no-drag' }}>
        {(['live', 'meetings', 'to-do', 'explore', 'status', 'settings'] as View[]).map((v) => (
          <button key={v} className={view === v ? 'on' : ''} onClick={() => setView(v)}>
            {v === 'live' && live?.mode === 'live' && <span className="pulse" />}
            {v}
          </button>
        ))}
        <span className="tabs-end"><RecordButton /></span>
      </nav>

      <NavContext.Provider value={{ openMeeting: (id) => { setSource(null); openMeeting(id); }, openPerson, openTranscript: (id, at) => setSource({ itemId: id, scope, at }), scope }}>
      <PeopleProvider scope={scope} onOpen={openPerson}>
      <main>
        <div hidden={view !== 'live'}><LiveView status={live} onOpen={openHit} onEntity={openEntity} onTopic={openTopic} scope={scope} prefill={askReq} /></div>
        <div hidden={view !== 'to-do'}><ToDoView onOpen={openHit} scope={scope} onTopic={openTopic} onMeeting={openMeeting} /></div>
        <div hidden={view !== 'meetings'}><MeetingsView onOpen={openHit} scope={scope} onTopic={openTopic} req={meetingReq} active={view === 'meetings'} /></div>
        <div hidden={view !== 'explore'}>
          <ExploreView scope={scope} focus={focus} setFocus={setFocus} onOpen={openHit} topicReq={topicReq} correctReq={correctReq}
            onOpenItem={(id, sc) => setSource({ itemId: id, scope: sc })} />
        </div>
        <div hidden={view !== 'status'}><StatusView status={status} active={view === 'status'} /></div>
        <div hidden={view !== 'settings'}><SettingsView status={status} /></div>
      </main>

      {source && <SourceDrawer src={source} onClose={() => setSource(null)} />}
      {toast && <div className="toast">{toast}</div>}
      <SelectionMenu onAsk={askAbout} onCorrect={correct} onSource={(itemId, chunkId) => setSource({ itemId, chunkId, scope })} />
      </PeopleProvider>
      </NavContext.Provider>
      </div>
      {diag && <DiagPanel />}
    </div>
  );
}
