import { useEffect, useMemo, useState } from 'react';
import { on } from '../api';
import { LookTimeline } from './recorder';

/* eslint-disable @typescript-eslint/no-explicit-any */
export interface Trace {
  seq: number;
  cycle: number;
  at: string;
  clock: number;
  kind: string;
  title: string;
  ms?: number;
  detail?: any;
}

const kinds = ['feed', 'line', 'cycle', 'entities', 'llm', 'phrases', 'rank', 'search', 'summary', 'brief', 'error'];
const filters: Record<string, string[]> = {
  all: kinds,
  model: ['llm', 'summary'],
  search: ['rank', 'search', 'brief'],
  input: ['feed', 'line', 'cycle', 'entities', 'phrases'],
  // window: what the recorder saw of the meeting window (its looks), not
  // Musubi's own steps.
  window: [],
};

export function mmss(s: number): string {
  if (!isFinite(s) || s < 0) return '–';
  const m = Math.floor(s / 60);
  return `${m}:${String(Math.floor(s % 60)).padStart(2, '0')}`;
}

// DiagPanel shows how the live panel is processing the conversation, step
// by step, grouped by suggestion cycle, newest first; and, under
// "window", what the recorder saw of the meeting window. Embedded, it
// sits in a page (Status) rather than beside the app.
export function DiagPanel({ embedded }: { embedded?: boolean } = {}) {
  const [traces, setTraces] = useState<Trace[]>([]);
  const [filter, setFilter] = useState('all');
  const [open, setOpen] = useState<Record<number, boolean>>({});

  useEffect(() => {
    window.go?.gui?.App?.Traces().then((t: Trace[] | null) => setTraces(t ?? []));
    const offs = [
      on<Trace>('diag:trace', (t) => setTraces((prev) => [...prev, t].slice(-800))),
      on<null>('diag:reset', () => setTraces([])),
    ];
    return () => offs.forEach((f) => f());
  }, []);

  const stats = useMemo(() => summarize(traces), [traces]);
  const groups = useMemo(() => {
    const shown = traces.filter((t) => filters[filter].includes(t.kind));
    const out: { key: string; cycle: number; items: Trace[] }[] = [];
    for (const t of shown.slice().reverse()) {
      const last = out[out.length - 1];
      if (t.cycle > 0 && last && last.cycle === t.cycle) last.items.push(t);
      else out.push({ key: `${t.cycle}-${t.seq}`, cycle: t.cycle, items: [t] });
    }
    return out;
  }, [traces, filter]);

  return (
    <aside className={'diag' + (embedded ? ' diag-embedded' : '')}>
      <header className="diag-head">
        <div className="diag-title">How Musubi is thinking</div>
        <div className="diag-filters">
          {Object.keys(filters).map((f) => (
            <button key={f} className={filter === f ? 'on' : ''} onClick={() => setFilter(f)}>{f}</button>
          ))}
        </div>
      </header>

      {filter === 'window' ? <div className="diag-list"><LookTimeline /></div> : <>
      <section className="diag-stats">
        <Stat label="model runs" v={String(stats.cycles)} />
        <Stat label="lines in" v={`${stats.finals} final · ${stats.drafts} draft`} />
        <Stat label="final text lag" v={stats.finalLag !== null ? `${stats.finalLag.toFixed(1)}s` : '–'} hint="speech end → final text arrives" />
        <Stat label="model: suggest" v={stats.llmMs !== null ? `${(stats.llmMs / 1000).toFixed(1)}s` : '–'} />
        <Stat label="model: summary" v={stats.sumMs !== null ? `${(stats.sumMs / 1000).toFixed(1)}s` : '–'} />
        <Stat label="briefs" v={`${stats.shown} summarised · ${stats.dropped} dropped · ${stats.empty} empty`} />
        <Stat label="said → passages, new topics" v={stats.fresh !== null ? `${stats.fresh.toFixed(1)}s median (${stats.freshN})` : '–'} hint="from the latest mention to passages on screen, for lookups within 10 s of the mention (meeting time)" />
        <Stat label="follow-up angles" v={String(stats.followups)} hint="lookups about something mentioned more than 10 s earlier: the model returning to a thread" />
      </section>

      <div className="diag-list">
        {groups.length === 0 && <div className="empty">Start a replay or a meeting; each step will show up here.</div>}
        {groups.map((g) =>
          g.cycle > 0 ? (
            <div key={g.key} className="diag-cycle">
              <button className="diag-cycle-head" onClick={() => setOpen((o) => ({ ...o, [g.cycle]: !o[g.cycle] }))}>
                <span className="clk">{mmss(g.items[g.items.length - 1].clock)}</span>
                <b>{g.items.some((t) => t.kind === 'line') ? 'Line' : 'Model run'} #{g.cycle}</b>
                <span className="dim">{cycleSummary(g.items)}</span>
                <span className="chev">{open[g.cycle] === false ? '▸' : '▾'}</span>
              </button>
              {open[g.cycle] !== false && g.items.slice().reverse().map((t) => <Step key={t.seq} t={t} />)}
            </div>
          ) : (
            g.items.map((t) => <Step key={t.seq} t={t} />)
          ),
        )}
      </div>
      </>}
    </aside>
  );
}

function Stat({ label, v, hint }: { label: string; v: string; hint?: string }) {
  return <div className="stat" title={hint}><div className="stat-v">{v}</div><div className="stat-l">{label}</div></div>;
}

function Step({ t }: { t: Trace }) {
  const [expanded, setExpanded] = useState(t.kind === 'rank' || t.kind === 'brief');
  const hasDetail = t.detail !== undefined && t.detail !== null;
  return (
    <div className={'step k-' + t.kind}>
      <button className="step-head" onClick={() => hasDetail && setExpanded(!expanded)}>
        <span className="clk">{mmss(t.clock)}</span>
        <span className={'kind kind-' + t.kind}>{t.kind}</span>
        <span className="step-title">{t.title}</span>
        {t.ms ? <span className="ms">{t.ms < 1000 ? `${t.ms}ms` : `${(t.ms / 1000).toFixed(1)}s`}</span> : null}
      </button>
      {expanded && hasDetail && <div className="step-body"><Detail t={t} /></div>}
    </div>
  );
}

function Detail({ t }: { t: Trace }) {
  const d = t.detail;
  switch (t.kind) {
    case 'line':
      return (
        <>
          <div className="kvline">{d.status} line from <b>{d.speaker}</b></div>
          <Pre label="Line" text={d.text} />
        </>
      );
    case 'cycle':
      return (
        <>
          {d.feed && (
            <div className="kvline">
              since last cycle: {d.feed.finals} final, {d.feed.drafts} draft, {d.feed.pending} pending lines
              {d.feed.finals > 0 && <> · final text {d.feed.finalLagAvg.toFixed(1)}s after speech (max {d.feed.finalLagMax.toFixed(1)}s)</>}
            </div>
          )}
          <div className="kvline">scope <b>{d.scope}</b> · excluding {(d.exclude ?? []).join(', ')}</div>
          <Pre label="What it read" text={d.window} />
        </>
      );
    case 'entities':
      return (
        <table className="dt">
          <thead><tr><th>name</th><th>type</th><th>said</th></tr></thead>
          <tbody>{(d.spotted ?? []).map((e: any) => <tr key={e.ID}><td>{e.Name}</td><td>{e.Type}</td><td>{e.Count}×</td></tr>)}</tbody>
        </table>
      );
    case 'llm':
      return (
        <>
          {d.error && <div className="error small">{d.error}</div>}
          {d.waitedForModelMs > 50 && <div className="kvline">waited {(d.waitedForModelMs / 1000).toFixed(1)}s for the model to be free</div>}
          {d.droppedAboutOwner?.length > 0 && <div className="kvline">dropped as about you: {d.droppedAboutOwner.join(', ')}</div>}
          {d.prompt && <Pre label={`Sent to ${d.model}`} text={d.prompt} collapsed />}
          {d.raw !== undefined && <Pre label="Model replied" text={pretty(d.raw)} />}
        </>
      );
    case 'phrases':
      return (
        <table className="dt">
          <thead><tr><th>phrase</th><th>rarity score</th><th>said</th></tr></thead>
          <tbody>{(d ?? []).map((p: any) => <tr key={p.phrase}><td>{p.phrase}</td><td>{p.score}</td><td>{p.said}×</td></tr>)}</tbody>
        </table>
      );
    case 'rank':
      return (
        <table className="dt">
          <thead><tr><th>query</th><th>from</th><th>score</th><th>said at</th><th>decision</th></tr></thead>
          <tbody>
            {(d ?? []).map((r: any) => (
              <tr key={r.query} className={r.decision === 'looking up' ? 'hl' : ''}>
                <td>{r.query}</td><td>{r.kind}</td><td>{r.score.toFixed(1)}</td><td>{r.saidAt >= 0 ? mmss(r.saidAt) : 'inferred'}</td><td>{r.decision}</td>
              </tr>
            ))}
          </tbody>
        </table>
      );
    case 'search':
      return (
        <>
          <div className="kvline">{d.mode} search · scope <b>{d.scope}</b> · excluding {(d.exclude ?? []).join(', ')}</div>
          <table className="dt hits">
            <thead><tr><th>#</th><th>from</th><th>score</th><th>label</th><th>passage</th></tr></thead>
            <tbody>
              {(d.hits ?? []).map((h: any) => (
                <tr key={h.rank}>
                  <td>{h.rank}</td>
                  <td><div>{h.title}</div><div className="dim">{h.date} · #{h.seq}</div></td>
                  <td>{h.score.toFixed(4)}</td>
                  <td><span className={'lvl-' + h.level}>{h.level}</span>{h.cats !== '-' && <div className="dim">{h.cats}</div>}</td>
                  <td className="ex">{h.excerpt}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      );
    case 'summary':
      return (
        <>
          {d.waitedForModelMs > 50 && <div className="kvline">waited {(d.waitedForModelMs / 1000).toFixed(1)}s for the model to be free</div>}
          {d.error && <div className="error small">{d.error}</div>}
          <Pre label="Model replied" text={d.raw} />
        </>
      );
    case 'brief':
      return (
        <div className="kvline">
          {d.stage === 'summary' ? 'summary' : 'passages'}: {d.outcome} · ready at {mmss(d.readyAt)}
          {d.saidAt !== undefined && <> · last said at {mmss(d.saidAt)} → <b>{d.spokenAgo.toFixed(1)}s later</b></>}
          {' '}· {(d.totalMs / 1000).toFixed(1)}s to look up and summarise
        </div>
      );
    default:
      return <Pre label="" text={JSON.stringify(d, null, 2)} />;
  }
}

function Pre({ label, text, collapsed }: { label: string; text: string; collapsed?: boolean }) {
  const [open, setOpen] = useState(!collapsed);
  return (
    <div className="pre">
      {label && <button className="pre-label" onClick={() => setOpen(!open)}>{open ? '▾' : '▸'} {label}</button>}
      {open && <pre>{text}</pre>}
    </div>
  );
}

function pretty(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

function cycleSummary(items: Trace[]): string {
  const rank = items.find((t) => t.kind === 'rank');
  const briefs = items.filter((t) => t.kind === 'brief');
  const parts: string[] = [];
  if (rank) parts.push(rank.title);
  if (briefs.length) parts.push(briefs.map((b) => b.detail?.outcome).join(', '));
  return parts.join(' · ');
}

function median(xs: number[]): number | null {
  if (!xs.length) return null;
  const s = xs.slice().sort((a, b) => a - b);
  return s[Math.floor(s.length / 2)];
}

function summarize(ts: Trace[]) {
  let drafts = 0, finals = 0, shown = 0, dropped = 0, empty = 0, followups = 0;
  const lags: number[] = [], llm: number[] = [], sums: number[] = [], fresh: number[] = [];
  let cycles = 0;
  for (const t of ts) {
    if (t.kind === 'cycle' && t.cycle > 0) {
      cycles++;
      const f = t.detail?.feed;
      if (f) {
        drafts += f.drafts;
        finals += f.finals;
        if (f.finals > 0) lags.push(f.finalLagAvg);
      }
    }
    if (t.kind === 'llm' && t.ms) llm.push(t.ms);
    if (t.kind === 'summary' && t.ms) sums.push(t.ms);
    if (t.kind === 'brief') {
      const o = t.detail?.outcome;
      if (o === 'summary shown') shown++;
      else if (o === 'dropped') dropped++;
      else if (o === 'nothing found') empty++;
      if (t.detail?.stage === 'passages' && typeof t.detail?.spokenAgo === 'number') {
        if (t.detail.spokenAgo <= 10) fresh.push(t.detail.spokenAgo);
        else followups++;
      }
    }
  }
  const avg = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : null);
  return { cycles, drafts, finals, finalLag: avg(lags), llmMs: avg(llm), sumMs: avg(sums), shown, dropped, empty, followups, fresh: median(fresh), freshN: fresh.length };
}

