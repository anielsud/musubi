import { useEffect, useMemo, useState } from 'react';
import { api, fmtDate } from '../api';
import type { ClaimView, KnowledgeCounts, Statement, TopicSummary, TopicView } from '../types';
import { Empty, LabelBadge, Spinner } from './common';

// Status presentation: what's live first, then what changed.
export const statusOrder = ['contested', 'established', 'likely', 'open', 'speculative', 'uncertain', 'answered', 'superseded', 'disproven', 'stale'];
const statusNote: Record<string, string> = {
  established: 'independently or in writing confirmed',
  likely: 'stated, not yet confirmed independently',
  contested: 'evidence on both sides',
  speculative: 'opinions or guesses so far',
  uncertain: 'weak evidence',
  open: 'an open question',
  answered: 'a question that was answered',
  superseded: 'replaced by something later',
  disproven: 'contradicted, or marked false',
  stale: 'nothing has reinforced it lately',
};
const history = new Set(['superseded', 'disproven', 'stale', 'answered']);

export function StatusPill({ status }: { status: string }) {
  return <span className={'status st-' + status} title={statusNote[status]}>{status}</span>;
}

interface NavProps {
  scope: string;
  onSource: (itemId: string, chunkId: number) => void;
}

// TopicsPane: the topic map and list, or a topic's page.
export function TopicsPane({ scope, onSource }: NavProps) {
  const [counts, setCounts] = useState<KnowledgeCounts | null>(null);
  const [topics, setTopics] = useState<TopicSummary[] | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [filter, setFilter] = useState('');
  const [err, setErr] = useState('');

  useEffect(() => {
    api.knowledgeCounts().then(setCounts).catch(() => {});
    setTopics(null);
    api.topics(scope).then((t) => setTopics(t ?? []), (e) => setErr(String(e)));
  }, [scope]);

  if (err) return <div className="error">{err}</div>;
  if (counts && counts.topics === 0) {
    return (
      <Empty>
        The knowledge layer hasn't been built yet: {counts.extracted} of {counts.extractable} sources read.
        Run <code>musubi knowledge</code> to extract statements, link them into claims and group topics.
      </Empty>
    );
  }
  if (open !== null) return <TopicPage id={open} scope={scope} onBack={() => setOpen(null)} onOpen={setOpen} onSource={onSource} />;
  if (!topics) return <Spinner />;
  const f = filter.toLowerCase();
  const shown = topics.filter((t) => !f || (t.label + ' ' + (t.keywords ?? []).join(' ')).toLowerCase().includes(f));
  const byRecent = shown.slice().sort((a, b) => (b.last > a.last ? 1 : -1));
  return (
    <div className="topics">
      <input className="filter" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={`Filter ${topics.length} topics…`} />
      <TopicMap topics={topics} highlight={f ? new Set(shown.map((t) => t.id)) : null} onOpen={setOpen} />
      <div className="section-head"><span>Most recent</span><span>{counts ? `${counts.claims} claims · ${counts.statements} statements` : ''}</span></div>
      <div className="topic-list">
        {byRecent.slice(0, 60).map((t) => (
          <button key={t.id} className="topic-row" onClick={() => setOpen(t.id)}>
            <span className={'topic-label' + (t.labelHidden ? ' hidden-label' : '')}>{t.label}</span>
            <span className="topic-meta">
              {t.statuses.contested ? <span className="status st-contested">{t.statuses.contested} contested</span> : null}
              {t.claims} claims · {fmtDate(t.last)}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

// TopicMap places topics by meaning (first two principal components):
// near each other means similar. Size is the number of claims; a contested
// topic gets an amber ring.
function TopicMap({ topics, highlight, onOpen }: { topics: TopicSummary[]; highlight: Set<number> | null; onOpen: (id: number) => void }) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 420, H = 300, pad = 22;
  const pts = useMemo(() => {
    if (!topics.length) return [];
    const xs = topics.map((t) => t.x), ys = topics.map((t) => t.y);
    const [x0, x1, y0, y1] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
    const maxC = Math.max(...topics.map((t) => t.claims));
    return topics.map((t) => ({
      t,
      x: pad + ((t.x - x0) / (x1 - x0 || 1)) * (W - 2 * pad),
      y: pad + ((t.y - y0) / (y1 - y0 || 1)) * (H - 2 * pad),
      r: 3 + 9 * Math.sqrt(t.claims / maxC),
    }));
  }, [topics]);
  const big = new Set(pts.slice().sort((a, b) => b.t.claims - a.t.claims).slice(0, 10).map((p) => p.t.id));
  return (
    <svg className="topic-map" viewBox={`0 0 ${W} ${H}`}>
      {pts.map(({ t, x, y, r }) => {
        const dim = highlight && !highlight.has(t.id);
        const live = (t.statuses.established ?? 0) + (t.statuses.likely ?? 0);
        const past = (t.statuses.superseded ?? 0) + (t.statuses.disproven ?? 0) + (t.statuses.stale ?? 0);
        const fill = past > live ? 'var(--text-faint)' : t.statuses.open ? 'var(--accent-2)' : 'var(--accent)';
        return (
          <g key={t.id} className="tnode" opacity={dim ? 0.15 : 1} onClick={() => onOpen(t.id)}
            onMouseEnter={() => setHover(t.id)} onMouseLeave={() => setHover(null)}>
            <circle cx={x} cy={y} r={r} fill={fill} fillOpacity={0.55} stroke={t.statuses.contested ? 'var(--lvl-confidential)' : 'none'} strokeWidth={2} />
            {(big.has(t.id) || hover === t.id) && !dim && (
              <text x={x} y={y - r - 4} textAnchor="middle" className={hover === t.id ? 'hot' : ''}>{short(t.label)}</text>
            )}
          </g>
        );
      })}
    </svg>
  );
}

function short(s: string): string {
  return s.length > 26 ? s.slice(0, 25) + '…' : s;
}

function TopicPage({ id, scope, onBack, onOpen, onSource }: { id: number; scope: string; onBack: () => void; onOpen: (id: number) => void } & NavProps) {
  const [t, setT] = useState<TopicView | null | undefined>(undefined);
  const [showHistory, setShowHistory] = useState(false);
  const [claimOpen, setClaimOpen] = useState<number | null>(null);
  const reload = () => api.topic(id, scope).then(setT);
  useEffect(() => {
    setT(undefined);
    reload();
  }, [id, scope]);
  if (t === undefined) return <Spinner />;
  if (t === null) return <Empty>Not visible in this scope. <button className="link" onClick={onBack}>back</button></Empty>;
  const claims = t.claimList ?? [];
  const live = claims.filter((c) => !history.has(c.belief.status));
  const past = claims.filter((c) => history.has(c.belief.status));
  return (
    <div className="topic-page">
      <button className="link" onClick={onBack}>← all topics</button>
      <h2 className={t.labelHidden ? 'hidden-label' : ''}>{t.label}</h2>
      {t.labelHidden && <div className="dim small">The topic's name was written from material not visible here; these are its keywords.</div>}
      {t.summary && <p className="topic-summary">{t.summary}</p>}
      <div className="chips">
        {statusOrder.filter((s) => t.statuses[s]).map((s) => <span key={s} className={'status st-' + s}>{t.statuses[s]} {s}</span>)}
      </div>
      {(t.people?.length ?? 0) > 0 && (
        <div className="people"><span className="dim">who: </span>{t.people!.slice(0, 8).map((p) => `${p.name} (${p.claims})`).join(' · ')}</div>
      )}

      <div className="section-head"><span>What's believed now</span></div>
      {live.length === 0 && <Empty>Nothing current; see history.</Empty>}
      {live.map((c) => <ClaimCard key={c.id} c={c} scope={scope} open={claimOpen === c.id} onToggle={() => setClaimOpen(claimOpen === c.id ? null : c.id)} onSource={onSource} onChanged={reload} />)}

      {past.length > 0 && (
        <>
          <button className="section-head link-head" onClick={() => setShowHistory(!showHistory)}>
            <span>{showHistory ? '▾' : '▸'} History: superseded, disproven, answered, stale ({past.length})</span>
          </button>
          {showHistory && past.map((c) => <ClaimCard key={c.id} c={c} scope={scope} open={claimOpen === c.id} onToggle={() => setClaimOpen(claimOpen === c.id ? null : c.id)} onSource={onSource} onChanged={reload} />)}
        </>
      )}

      {(t.related?.length ?? 0) > 0 && (
        <>
          <div className="section-head"><span>Related topics</span></div>
          {t.related!.slice(0, 10).map((r) => (
            <button key={r.id} className="topic-row" onClick={() => onOpen(r.id)}>
              <span className="topic-label">{r.label}</span>
              <span className="topic-meta">
                {r.unconnected ? <span className="status st-open" title="similar in meaning, but never discussed together">never discussed together</span> : `${r.shared} shared sources`}
              </span>
            </button>
          ))}
        </>
      )}
    </div>
  );
}

export function ClaimCard({ c, scope, open, onToggle, onSource, onChanged }: { c: ClaimView; scope: string; open: boolean; onToggle: () => void; onChanged: () => void } & Omit<NavProps, 'scope'>) {
  const sts = c.statements ?? [];
  const holders = new Set(sts.map((s) => s.holderId || s.holder)).size;
  return (
    <article className={'claim st-border-' + c.belief.status}>
      <button className="claim-head" onClick={onToggle}>
        <StatusPill status={c.belief.status} />
        <span className="claim-kind">{c.kind}</span>
        <span className="claim-text">{c.text}</span>
      </button>
      <div className="claim-meta">
        {sts.length} source{sts.length === 1 ? '' : 's'} · {holders} {holders === 1 ? 'person' : 'people'} · {fmtDate(c.belief.last)}
        {c.belief.verdict && <> · <b>you: {c.belief.verdict}</b></>}
      </div>
      {open && (
        <div className="claim-body">
          <div className="belief">
            <div className="beliefbar"><div className="sup" style={{ width: `${Math.round(c.belief.support * 100)}%` }} /></div>
            {c.belief.against > 0 && <div className="beliefbar against"><div className="sup" style={{ width: `${Math.round(c.belief.against * 100)}%` }} /></div>}
            <span className="dim small">{c.belief.why} · support {c.belief.support.toFixed(2)}{c.belief.against > 0 ? ` · against ${c.belief.against.toFixed(2)}` : ''}</span>
          </div>
          {(c.relations ?? []).map((r) => (
            <div key={r.rel + r.claimId} className="rel">
              <span className="rel-kind">{r.direction === 'out' ? r.rel : relIn(r.rel)}</span>
              <StatusPill status={r.status} /> {r.text}
            </div>
          ))}
          <div className="stmts">
            {sts.map((s) => <StatementRow key={s.id} s={s} onSource={onSource} />)}
          </div>
          {scope === 'owner' && sts.length > 0 && <Judge statementId={sts[sts.length - 1].id} verdict={c.belief.verdict} onDone={onChanged} />}
        </div>
      )}
    </article>
  );
}

function relIn(rel: string): string {
  return ({ supersedes: 'superseded by', contradicts: 'contradicted by', refines: 'refined by', answers: 'answered by' } as Record<string, string>)[rel] ?? rel;
}

function StatementRow({ s, onSource }: { s: Statement; onSource: (itemId: string, chunkId: number) => void }) {
  return (
    <button className="stmt" onClick={() => onSource(s.itemId, s.chunkId)} title="Open the source passage">
      <div className="stmt-head">
        <b>{s.holder || 'unknown'}</b>
        <span className="dim">{s.kind} · {s.certainty} certainty · {fmtDate(s.saidAt)}{s.when ? ` · about ${s.when}` : ''}</span>
        <LabelBadge label={s.label} compact />
      </div>
      <div className="stmt-text">{s.text}</div>
      <div className="stmt-src">{s.itemTitle}</div>
    </button>
  );
}

function Judge({ statementId, verdict, onDone }: { statementId: number; verdict?: string; onDone: () => void }) {
  const set = (v: string) => api.judge(statementId, v === verdict ? '' : v).then(onDone);
  return (
    <div className="judge">
      <span className="dim small">Your judgement:</span>
      {[['verified', '✓ verified'], ['disputed', '? disputed'], ['false', '✗ false']].map(([v, label]) => (
        <button key={v} className={'chip' + (verdict === v ? ' on' : '')} onClick={() => set(v)}>{label}</button>
      ))}
    </div>
  );
}

// ClaimSearch lists claims matching words (in the Search mode).
export function ClaimResults({ q, scope, onSource }: { q: string } & NavProps) {
  const [cs, setCs] = useState<ClaimView[] | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const run = () => api.searchClaims(q, scope).then((r) => setCs(r ?? []));
  useEffect(() => {
    if (q.trim()) run();
    else setCs(null);
  }, [q, scope]);
  if (!cs || cs.length === 0) return null;
  return (
    <section>
      <div className="section-head"><span>Claims</span></div>
      {cs.map((c) => <ClaimCard key={c.id} c={c} scope={scope} open={open === c.id} onToggle={() => setOpen(open === c.id ? null : c.id)} onSource={onSource} onChanged={run} />)}
    </section>
  );
}
