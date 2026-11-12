import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../api';
import type { AppStatus, M365View, ProviderView, QueueView } from '../types';
import { checkDoes, Spinner } from './common';
import { RecordingCard } from './recorder';

const levels = ['public', 'internal', 'confidential', 'restricted'];

export function StatusView({ status }: { status: AppStatus | null }) {
  if (!status) return <div className="view"><Spinner /></div>;
  const s = status.stats;
  const chunkTotal = s ? levels.reduce((n, l) => n + (s.ChunkLevels[l] ?? 0), 0) : 0;
  return (
    <div className="view status-page">
      {status.error && <div className="error">{status.error}</div>}
      <RecordingCard />
      <section className="tiles">
        <Tile n={s?.ItemsByKind.meeting ?? 0} label="meetings" />
        <Tile n={s?.ItemsByKind.event ?? 0} label="calendar events" />
        <Tile n={(s?.ItemsByKind.email ?? 0) + (s?.ItemsByKind.chat ?? 0)} label="mail & chat" />
        <Tile n={s?.Chunks ?? 0} label="passages" />
      </section>

      <section>
        <div className="section-head"><span>Sensitivity of passages</span></div>
        <div className="levelbar">
          {levels.map((l) => {
            const n = s?.ChunkLevels[l] ?? 0;
            return n ? <div key={l} className={'seg lvl-' + l} style={{ flex: n }} title={`${l}: ${n}`} /> : null;
          })}
        </div>
        <div className="legend">
          {levels.map((l) => (
            <span key={l}><i className={'dot lvl-' + l} />{l} {s?.ChunkLevels[l] ?? 0}{chunkTotal ? ` (${Math.round(100 * (s?.ChunkLevels[l] ?? 0) / chunkTotal)}%)` : ''}</span>
          ))}
        </div>
        {s && Object.keys(s.Categories).length > 0 && (
          <div className="dim small">categories: {Object.entries(s.Categories).map(([k, v]) => `${k} ${v}`).join(' · ')}</div>
        )}
      </section>

      <WorkQueue />

      <Connections status={status} />

      <section className="kv">
        <div className="section-head"><span>Data</span></div>
        <Row k="meetings linked to their calendar event" v={s ? `${s.MeetingsLinked.toLocaleString()} of ${s.Meetings.toLocaleString()}` : '—'} />
        <Row k="names recognised" v={s ? `${(s.Mentions - s.Ambiguous).toLocaleString()} of ${s.Mentions.toLocaleString()}` : '—'} />
        <Row k="last checked for new meetings" v={status.lastUpdate && !status.lastUpdate.startsWith('0001') ? fmtDate(status.lastUpdate) : 'not since Musubi opened'} />
      </section>

      <button className="primary wide" disabled={status.updating} onClick={() => api.update()}>
        {status.updating ? <><Spinner /> Checking for new meetings…</> : 'Check for new meetings'}
      </button>
      <p className="dim small">{checkDoes(status)}</p>

    </div>
  );
}

// Connections: is each thing Musubi depends on there right now. Setting
// them up is in Settings.
function Connections({ status }: { status: AppStatus }) {
  const [m, setM] = useState<M365View | null>(null);
  const [ps, setPs] = useState<ProviderView[] | null>(null);
  useEffect(() => {
    const load = () => { api.m365().then(setM); api.providers().then((v) => setPs(v ?? [])); };
    load();
    const offs = [on('m365:changed', load), on('gateway:changed', load), on('providers:changed', load), on('update:done', load)];
    return () => offs.forEach((f) => f());
  }, []);
  const gw = ps?.find((p) => p.kind === 'gateway');
  const others = ps?.filter((p) => p.kind === 'custom' && p.usedBy?.length) ?? [];
  const synced = m?.lastSync && !m.lastSync.startsWith('0001') ? `, synced ${fmtDate(m.lastSync)}` : '';
  return (
    <section className="kv">
      <div className="section-head"><span>Connections</span></div>
      <Row k="Microsoft 365" v={!m ? '…' : m.needsSignIn ? '✗ sign-in lapsed: Settings → Accounts' : m.connected ? (m.enabled ? `✓ connected${synced}` : '✓ connected, syncing off') : '✗ not connected'} />
      <Row k="AI Gateway" v={!gw ? '…' : gw.ready ? '✓ connected' : '✗ not connected: Settings → AI providers'} />
      {others.map((p) => <Row key={p.name} k={p.name} v={p.ready ? '✓ key in your Keychain' : '✗ no key: Settings → AI providers'} />)}
      <Row k="Ollama (this Mac)" v={status.ollamaUp ? '✓ running' : '✗ not running'} />
      <Row k="Tomoe live feed" v={status.live.connected ? '✓ connected' : '✗ not connected'} />
    </section>
  );
}

function Tile({ n, label }: { n: number; label: string }) {
  return <div className="tile"><div className="tile-n">{n.toLocaleString()}</div><div className="tile-l">{label}</div></div>;
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}

const usd = (n: number) => (n < 1 ? `${Math.max(1, Math.round(n * 100))}¢` : `$${n.toFixed(n < 10 ? 2 : 0)}`);

// WorkQueue is the knowledge layer's waiting work: what updates take on
// their own (new meetings, a few at a time), and what needs a run started
// here (a big batch, or a backlog that only joins by rethreading in time
// order).
function WorkQueue() {
  const [q, setQ] = useState<QueueView | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const load = () => api.queue().then((v) => { setQ(v); setErr(null); }, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    // Other processes (the CLI, a run elsewhere) change the queue too.
    const t = setInterval(load, 30000);
    const offs = [on('queue:changed', load), on('update:done', load)];
    return () => { clearInterval(t); offs.forEach((f) => f()); };
  }, []);
  if (err) return <section><div className="section-head"><span>Work queue</span></div><div className="error">{err}</div></section>;
  if (!q) return null;
  const newN = q.newExtract + q.newThread;
  const older = q.olderExtract + q.olderThread;
  const run = (kind: 'catchup' | 'rethread') => { setConfirm(false); api.runKnowledge(kind).then(load, (e) => setErr(String(e))); };
  const frontier = q.frontier && !q.frontier.startsWith('0001') ? fmtDate(q.frontier) : null;
  return (
    <section className="kv">
      <div className="section-head"><span>Work queue <button className="icon-btn small" title="Refresh" onClick={load}>↻</button></span>{q.running && <span className="dim small"><Spinner /> {q.running === 'rethread' ? 'rethreading in time order…' : 'catching up…'}</span>}</div>
      {q.extract === 'none' && (
        <div className="warn small">Extraction is off for this dataset (knowledge.extract = "none"): new meetings are stored and searchable but don't become knowledge.</div>
      )}
      <Row k="threaded" v={`${q.threaded.toLocaleString()} meetings${frontier ? `, through ${frontier}` : ''}`} />
      <Row k="new, to extract" v={String(q.newExtract)} />
      <Row k="new, to thread" v={String(q.newThread)} />
      <Row k="backlog (older than threaded)" v={older ? `${older.toLocaleString()} (${q.olderExtract} to extract, ${q.olderThread} extracted)` : 'none'} />
      {q.duplicates > 0 && <Row k="recorded twice" v={`${q.duplicates} (one of each is used)`} />}
      <Row k="spent (ledger)" v={usd(q.spent)} />
      <p className="dim small">
        After each new meeting, an update takes {q.autoMax > 0 ? `up to ${q.autoMax}` : 'every'} new {q.autoMax === 1 ? 'meeting' : 'meetings'} on its own.
        Older meetings can only join the threads in time order, so they wait for a rethread.
      </p>
      {newN > 0 && (
        <button className="wide" disabled={!!q.running} onClick={() => run('catchup')}>
          Catch up: {newN} new {newN === 1 ? 'meeting' : 'meetings'}, about {usd(q.catchUpUsd)}
        </button>
      )}
      {older > 0 && !confirm && (
        <button className="wide" disabled={!!q.running} onClick={() => setConfirm(true)}>
          Rethread everything in time order: about {usd(q.rethreadUsd)}, {q.rethreadHours.toFixed(q.rethreadHours < 10 ? 1 : 0)} h
        </button>
      )}
      {confirm && (
        <div className="confirm-row">
          <span className="small">Rethreading rebuilds every thread from the oldest meeting on, taking in the backlog. Hosted calls stop at about {usd(q.rethreadUsd * 1.5)}.</span>
          <button className="primary" onClick={() => run('rethread')}>Rethread</button>
          <button onClick={() => setConfirm(false)}>Cancel</button>
        </div>
      )}
    </section>
  );
}
