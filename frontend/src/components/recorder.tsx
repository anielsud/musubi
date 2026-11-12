import { useEffect, useMemo, useRef, useState } from 'react';
import { api, on, thumbURL } from '../api';
import type { LookView, RecDiag, RecorderView, RecorderWatch, RecSource, RecStatus, ToolStatus } from '../types';
import { Spinner } from './common';

// The recorder's screens on Status, Live and the thinking view: its state
// and sources, health, live labelling and the looks at the meeting window
// (docs/recorder-screens.md). The recorder is always Musubi's own; which
// recorder Musubi's meetings come from is Settings → Recording.

// useRecorder follows the recorder's state: asked once, then kept current
// by its feed.
export function useRecorder() {
  const [rec, setRec] = useState<RecorderView | null>(null);
  useEffect(() => {
    const load = () => api.recorderState().then(setRec, () => {});
    const t0 = setTimeout(load, 200);
    const t = setInterval(load, 30000);
    const set = (s: RecStatus) => setRec((r) => (r ? { ...r, running: true, status: s } : r));
    const offs = [
      on<RecStatus>('recorder:status', set),
      on<RecStatus>('recorder:audio', set),
      on<boolean>('recorder:conn', load),
      on('prefs:changed', load),
    ];
    return () => { clearTimeout(t0); clearInterval(t); offs.forEach((f) => f()); };
  }, []);
  return rec;
}

// useRecorderWatch follows the app's watchdog: while the recorder is
// down, since when, and why the last start failed.
export function useRecorderWatch() {
  const [w, setW] = useState<RecorderWatch | null>(null);
  useEffect(() => on<RecorderWatch>('recorder:watch', (v) => setW(v?.down ? v : null)), []);
  return w;
}

const srcName = (s: RecSource) => s.name || (s.kind === 'mic' ? 'the microphone' : 'the meeting audio');

// SourceBanner says which source has gone quiet, in words that say what
// it is (your microphone, or the meeting's audio), and suggests one the
// recorder heard sound on instead: one click.
export function SourceBanner({ rec }: { rec: RecorderView | null }) {
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const st = rec?.status;
  if (!st?.recording || !st.silent) return null;
  const sil = st.silent, sug = st.suggest;
  const mic = sil.kind === 'mic';
  const swap = () => {
    if (!sug) return;
    setBusy(true); setErr('');
    api.recorderSetSource(sug.kind, sug.id).then(() => setBusy(false), (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <div className="source-banner">
      <span>
        {mic
          ? <>Your microphone (<b>{srcName(sil)}</b>) has picked up nothing for 20 seconds: muted, or the wrong microphone?</>
          : <>No meeting audio from <b>{srcName(sil)}</b> for a minute.</>}
      </span>
      {sug
        ? <><span>{mic ? <>The <b>{srcName(sug)}</b> microphone hears sound.</> : <><b>{srcName(sug)}</b> is playing sound.</>}</span>
            <button className="primary" disabled={busy} onClick={swap}>{busy ? <Spinner /> : `Use ${srcName(sug)}`}</button></>
        : <span className="dim">{st.suggest_note ? st.suggest_note + '.' : mic ? 'No other microphone hears anything either.' : 'Nothing else is playing either.'}</span>}
      {err && <div className="error small">{err}</div>}
    </div>
  );
}

function elapsed(from: string | undefined, now: number) {
  const t = from ? new Date(from).getTime() : NaN;
  if (!isFinite(t) || t < 946684800000) return '';
  const s = Math.max(0, Math.floor((now - t) / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h ? `${h}:${String(m).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${m}:${String(s % 60).padStart(2, '0')}`;
}

// RecordButton sits at the right of the top tabs: Record for a meeting
// the recorder didn't notice, and while recording, the time and Stop.
export function RecordButton() {
  const rec = useRecorder();
  const watch = useRecorderWatch();
  const [now, setNow] = useState(Date.now());
  const [busy, setBusy] = useState(false);
  const recording = !!rec?.status?.recording;
  useEffect(() => {
    if (!recording) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [recording]);
  const run = (f: () => Promise<unknown>) => {
    setBusy(true);
    f().then(() => setBusy(false), (e) => { setBusy(false); alert(String(e)); });
  };
  if (!rec?.running || !rec.status) {
    // Loud on purpose: while it's down, meetings aren't recorded.
    const start = () => run(api.restartRecorder);
    return (
      <button className="rec-btn off" disabled={busy} title={`The recorder isn't running, so meetings aren't being recorded. Musubi tries to start it by itself; click to try now.${watch?.problem ? ' Last try: ' + watch.problem : ''}`} onClick={start}>
        {busy ? <Spinner /> : '⚠'} Recorder off
      </button>
    );
  }
  const st = rec.status;
  if (!recording) {
    return <button className="rec-btn" disabled={busy} title="Start recording: for a meeting it didn't notice" onClick={() => run(api.recorderStart)}>{busy ? <Spinner /> : '●'} Record</button>;
  }
  const stop = () => { if (confirm('Stop recording this meeting? What is recorded so far is saved.')) run(api.recorderStop); };
  return (
    <button className="rec-btn on" disabled={busy} title={`Recording ${st.session?.title || 'this meeting'}: click to stop`} onClick={stop}>
      {busy ? <Spinner /> : '■'} {elapsed(st.started_at || st.session?.created_at, now) || 'Stop'}
    </button>
  );
}

// RecordingCard is the Status tab's recorder: recording or idle, which
// meeting and for how long, the sources, and its health with each fix.
// Start and Stop are the record button by the tabs.
export function RecordingCard() {
  const rec = useRecorder();
  const watch = useRecorderWatch();
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  if (!rec) return null;
  const st = rec.status;
  const from = rec.ofRecord === 'musubi' ? 'this recorder' : 'Tomoe';
  return (
    <section className="kv recording">
      <div className="section-head"><span>Recording</span>{st?.recording && <span className="rec-dot">● recording</span>}</div>
      {rec.error && <div className="error small">{rec.error}</div>}
      {!rec.running ? (
        <>
          <div className="error small">The recorder isn&apos;t running: meetings aren&apos;t being recorded.</div>
          <p className="dim small">
            It starts at login and launchd restarts it whenever it stops. While Musubi is open it also checks every 10 seconds and starts the
            recorder itself after 30 seconds down{watch?.tried ? `; last tried ${new Date(watch.tried).toLocaleTimeString()}` : ''}.
            {watch?.problem && <> Last problem: {watch.problem}</>}
          </p>
          <div className="row-buttons"><button className="primary" onClick={() => api.restartRecorder().catch(() => {})}>Start the recorder</button></div>
        </>
      ) : st && (
        <>
          <div className="rec-state">
            {st.recording
              ? <><b>{st.session?.title || 'Untitled meeting'}</b> <span className="dim">{elapsed(st.started_at || st.session?.created_at, now)}</span></>
              : <span className="dim">Idle: meetings are noticed and recorded by themselves.</span>}
          </div>
          <SourceBanner rec={rec} />
          <Row k="microphone" v={st.mic?.name || '—'} />
          <Row k="meeting audio" v={st.system?.name || '—'} />
          {st.busy && <div className="dim small"><Spinner /> {st.busy}</div>}
          <Health health={st.health ?? []} />
        </>
      )}
      <p className="dim small">Musubi&apos;s meetings and live panel come from {from} (Settings → Recording).</p>
    </section>
  );
}

// Health lists what the recorder is missing, each with its fix: an action
// it runs, or a command to copy into a terminal.
function Health({ health }: { health: string[] }) {
  const [tools, setTools] = useState<ToolStatus[] | null>(null);
  const [running, setRunning] = useState<Record<string, boolean>>({});
  const [copied, setCopied] = useState('');
  const [err, setErr] = useState('');
  const load = () => api.recorderTools().then((t) => setTools(t ?? []), (e) => setErr(String(e)));
  useEffect(() => { load(); }, [health.join('|')]);
  const missing = (tools ?? []).filter((t) => !t.ok);
  const fix = (t: ToolStatus) => {
    setRunning((r) => ({ ...r, [t.id]: true })); setErr('');
    api.recorderFixTool(t.id).then(() => { setRunning((r) => ({ ...r, [t.id]: false })); load(); }, (e) => { setRunning((r) => ({ ...r, [t.id]: false })); setErr(String(e)); });
  };
  const copy = (c: string) => api.copyText(c).then(() => { setCopied(c); setTimeout(() => setCopied(''), 2000); }, (e) => setErr(String(e)));
  return (
    <div className="rec-health">
      {health.length === 0 && missing.length === 0 && tools && <div className="ok-text small">Everything the recorder uses is in place.</div>}
      {health.map((h) => <div key={h} className="warn small">{h}</div>)}
      {missing.map((t) => (
        <div key={t.id} className="tool-line">
          <div className="small"><b>{t.name}</b>{t.required && <span className="badge"> needed</span>} <span className="dim">{t.neededFor}</span></div>
          {t.detail && <div className="dim small">{t.detail}</div>}
          {t.fix?.kind === 'action' && <button disabled={running[t.id]} onClick={() => fix(t)}>{running[t.id] ? <><Spinner /> working…</> : t.fix.label}</button>}
          {t.fix?.kind === 'command' && t.fix.command && (
            <div className="tool-cmd"><code>{t.fix.command}</code><button onClick={() => copy(t.fix!.command!)}>{copied === t.fix.command ? 'Copied' : t.fix.label || 'Copy'}</button></div>
          )}
        </div>
      ))}
      {err && <div className="error small">{err}</div>}
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}

const clockOf = (iso: string) => {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
};

// LabellingPane is the recorder's live labelling: who it thinks is
// speaking on each line, voice matches and window hints, as they happen.
export function LabellingPane() {
  const [ds, setDs] = useState<RecDiag[]>([]);
  useEffect(() => {
    api.recorderDiags().then((d) => setDs(d ?? []), () => {});
    return on<RecDiag>('recorder:diag', (d) => setDs((p) => [...p, d].slice(-300)));
  }, []);
  return (
    <div className="labelling">
      <div className="labelling-list">
        {ds.length === 0 && <div className="empty">While a meeting is recorded, each line&apos;s speaker and why show up here.</div>}
        {ds.slice().reverse().map((d, i) => (
          <div key={ds.length - i} className="labelling-row">
            <span className="clk">{clockOf(d.at)}</span>
            <span className={'kind kind-' + d.kind}>{d.kind}</span>
            {d.label && <b>{d.label}</b>}
            <span>{d.detail}</span>
            {d.line && <div className="dim small">“{d.line}”</div>}
          </div>
        ))}
      </div>
    </div>
  );
}

// The meeting window's looks (the recorder's hint timeline): the frame,
// the tile lit for the speaker, and the name read or why none was.
const stageWords: Record<string, string> = {
  window_not_found: 'no window', capture_failed: 'capture failed', frame_captured: 'captured', not_a_call: 'not a call',
  blank_capture: 'window black', ui_frozen: 'window not repainting', one_on_one: '1:1 call', no_rule: 'no rule',
  ring_matched: 'tile lit', no_ring_match: 'no tile lit', ambiguous_ring: 'several tiles lit', speaker_view: 'speaker view',
  no_label_region: 'no name area', ocr_hit: 'name', ocr_miss: 'no name read',
};
const failed = new Set(['ambiguous_ring', 'no_ring_match', 'ocr_miss', 'blank_capture', 'ui_frozen']);
const lookClass = (l: LookView) => (l.usable ? 'look-ok' : failed.has(l.stage) ? 'look-fail' : 'look-idle');
const mmss = (s: number) => { const x = Math.max(0, Math.floor(s)); return `${Math.floor(x / 60)}:${String(x % 60).padStart(2, '0')}`; };

// LookTimeline shows a meeting's looks: live (no session: the recording
// in progress, from the feed) or a saved meeting's (asked of the
// recorder). Changes only by default.
export function LookTimeline({ session }: { session?: string }) {
  const [looks, setLooks] = useState<LookView[]>([]);
  const [sid, setSid] = useState(session ?? '');
  const sidRef = useRef(session ?? '');
  const [all, setAll] = useState(false);
  const [err, setErr] = useState('');
  useEffect(() => {
    setLooks([]); setErr('');
    if (session) {
      setSid(session);
      api.recorderLooks(session).then((l) => setLooks(l ?? []), (e) => setErr(String(e)));
      return;
    }
    api.recorderLiveLooks().then((v) => { sidRef.current = v.session; setSid(v.session); setLooks(v.looks ?? []); }, () => {});
    return on<{ session: string; look: LookView }>('recorder:look', ({ session: s, look }) => {
      // A new recording starts its own timeline.
      if (sidRef.current !== s) {
        sidRef.current = s;
        setSid(s);
        setLooks([look]);
      } else setLooks((p) => [...p, look].slice(-300));
    });
  }, [session]);
  const sum = useMemo(() => ({
    names: looks.filter((l) => l.usable).length,
    fails: looks.filter((l) => failed.has(l.stage)).length,
  }), [looks]);
  const rows = (all ? looks : looks.filter((l) => !l.thumbOf)).slice(-150).reverse();
  return (
    <div className="looks">
      <div className="looks-head small">
        <span>{looks.length} looks · <span className="look-ok">{sum.names} with a name</span> · <span className="look-fail">{sum.fails} missed</span></span>
        <label className="dim"><input type="checkbox" checked={!all} onChange={(e) => setAll(!e.target.checked)} /> changes only</label>
      </div>
      {err && <div className="dim small">{/not running/.test(err) ? 'The recorder is not running, so the meeting window looks can’t be shown.' : err}</div>}
      {!err && rows.length === 0 && <div className="empty">{session ? 'No looks at the meeting window were kept for this meeting.' : 'Looks at the meeting window show up here while a meeting is recorded.'}</div>}
      {rows.map((l) => (
        <div key={l.id} className={'look-row ' + lookClass(l)}>
          <Thumb look={l} session={sid} />
          <div className="look-info">
            <div><span className="clk">{mmss(l.sessionTime)}</span> <span className={'look-stage ' + lookClass(l)}>{stageWords[l.stage] ?? l.stage}</span>
              {l.name && <b> {l.name}{l.fromCache ? ' (known tile)' : ''}</b>}
              {l.candidates && <span> {l.candidates.map((c) => c || '?').join(' / ')}</span>}
            </div>
            <div className="dim small">{l.detail}</div>
          </div>
        </div>
      ))}
    </div>
  );
}

function Thumb({ look, session }: { look: LookView; session: string }) {
  const [broken, setBroken] = useState(false);
  if (!look.width || !look.height) return <div className="look-thumb empty">{stageWords[look.stage] ?? look.stage}</div>;
  const src = look.thumb || (session ? thumbURL(session, look.thumbOf || look.id) : '');
  const rings = look.ring ? [look.ring] : look.rings ?? [];
  return (
    <div className="look-thumb">
      {src && !broken && <img src={src} alt="" onError={() => setBroken(true)} />}
      <svg viewBox={`0 0 ${look.width} ${look.height}`} preserveAspectRatio="none">
        {rings.map((r, i) => <rect key={i} x={r.X} y={r.Y} width={r.Width} height={r.Height} className={rings.length > 1 ? 'ring-many' : 'ring'} />)}
      </svg>
    </div>
  );
}
