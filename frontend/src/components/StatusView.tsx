import { api, fmtDate } from '../api';
import type { AppStatus } from '../types';
import { Spinner } from './common';

const levels = ['public', 'internal', 'confidential', 'restricted'];

export function StatusView({ status }: { status: AppStatus | null }) {
  if (!status) return <div className="view"><Spinner /></div>;
  const s = status.stats;
  const chunkTotal = s ? levels.reduce((n, l) => n + (s.ChunkLevels[l] ?? 0), 0) : 0;
  return (
    <div className="view status-page">
      {status.error && <div className="error">{status.error}</div>}
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

      <section className="kv">
        <div className="section-head"><span>Engine</span></div>
        <Row k="owner" v={status.owner || '—'} />
        <Row k="local models" v={status.ollamaUp ? `✓ ${status.chatModel} · ${status.embedModel}` : '✗ Ollama not reachable'} />
        <Row k="Tomoe feed" v={status.live.connected ? '✓ connected' : '✗ not connected'} />
        <Row k="meetings linked to calendar" v={s ? `${s.MeetingsLinked} of ${s.Meetings}` : '—'} />
        <Row k="names resolved" v={s ? `${s.Mentions - s.Ambiguous} of ${s.Mentions}` : '—'} />
        <Row k="last update" v={status.lastUpdate && !status.lastUpdate.startsWith('0001') ? fmtDate(status.lastUpdate) : 'not this session'} />
        {Object.entries(status.config ?? {}).map(([k, v]) => <Row key={k} k={k} v={v} />)}
      </section>

      <button className="primary wide" disabled={status.updating} onClick={() => api.update()}>
        {status.updating ? <><Spinner /> Updating…</> : 'Update now'}
      </button>
      <p className="dim small">Update reads new Tomoe recordings and anything in the inbox. Meetings are also ingested automatically when Tomoe saves them.</p>
    </div>
  );
}

function Tile({ n, label }: { n: number; label: string }) {
  return <div className="tile"><div className="tile-n">{n.toLocaleString()}</div><div className="tile-l">{label}</div></div>;
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}
