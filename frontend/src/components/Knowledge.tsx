import { useEffect, useState } from 'react';
import { api, fmtDate } from '../api';
import type { ClaimView, KnowledgeCounts, Statement, StatusPoint, ThreadDetail, ThreadItem, TopicMapData, TopicSummary, TopicView } from '../types';
import { Empty, LabelBadge, Spinner } from './common';
import { TopicViews, clusterColor, useWide } from './TopicViews';

// Status presentation: what's live first, then what changed.
export const statusOrder = ['contested', 'established', 'likely', 'open', 'speculative', 'uncertain', 'answered', 'done', 'superseded', 'disproven', 'stale'];
const statusNote: Record<string, string> = {
  established: 'independently or in writing confirmed',
  likely: 'stated, not yet confirmed independently',
  contested: 'evidence on both sides',
  speculative: 'opinions or guesses so far',
  uncertain: 'weak evidence',
  open: 'an open question',
  answered: 'a question that was answered',
  done: 'a commitment or plan that was carried out',
  superseded: 'replaced by something later',
  disproven: 'contradicted, or marked false',
  stale: 'nothing has reinforced it lately',
};
const history = new Set(['superseded', 'disproven', 'stale', 'answered', 'done']);
const relKinds: [string, string][] = [
  ['shared', 'Shares statements'],
  ['same', 'Same customer or project'],
  ['together', 'Discussed together'],
  ['parallel', 'The same kind of thing, elsewhere'],
  ['similar', 'Similar'],
];

export function StatusPill({ status }: { status: string }) {
  return <span className={'status st-' + status} title={statusNote[status]}>{status}</span>;
}

interface NavProps {
  scope: string;
  onSource: (itemId: string, chunkId: number) => void;
}

// TopicsPane: the topic views and the list, or a topic's page. Wide, the
// views stay on the left and the list or page opens beside them.
export function TopicsPane({ scope, onSource, want }: NavProps & { want?: { id: number; n: number } | null }) {
  const wide = useWide();
  const [counts, setCounts] = useState<KnowledgeCounts | null>(null);
  const [map, setMap] = useState<TopicMapData | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [filter, setFilter] = useState('');
  const [err, setErr] = useState('');

  useEffect(() => {
    if (want) setOpen(want.id);
  }, [want]);

  useEffect(() => {
    api.knowledgeCounts().then(setCounts).catch(() => {});
    setMap(null);
    api.topicMap(scope).then(setMap, (e) => setErr(String(e)));
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
  const page = open !== null && <TopicPage id={open} scope={scope} onBack={() => setOpen(null)} onOpen={setOpen} onSource={onSource} />;
  if (!wide && page) return page;
  if (!map) return <Spinner />;
  const topics = map.topics ?? [];
  const f = filter.toLowerCase();
  const shown = topics.filter((t) => !f || (t.label + ' ' + (t.keywords ?? []).join(' ')).toLowerCase().includes(f));
  const byRecent = shown.slice().sort((a, b) => (b.last > a.last ? 1 : -1));
  const views = (
    <>
      <input className="filter" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={`Filter ${topics.length} topics…`} />
      <TopicViews map={map} highlight={f ? new Set(shown.map((t) => t.id)) : null} selected={open} onOpen={setOpen} wide={wide} />
    </>
  );
  const list = (
    <>
      <div className="section-head"><span>Recently moved</span><span>{counts ? `${counts.claims} claims · ${counts.statements} statements` : ''}</span></div>
      <div className="topic-list">
        {byRecent.slice(0, wide ? 200 : 60).map((t) => (
          <button key={t.id} className={'topic-row' + (t.now ? ' with-now' : '')} onClick={() => setOpen(t.id)}>
            <span className="trow-line">
              <span className="cl-dot" style={{ background: clusterColor(t.cluster) }} />
              <span className={'topic-label' + (t.labelHidden ? ' hidden-label' : '')}>{t.label}</span>
              <span className="topic-meta">
                {t.statuses.contested ? <span className="status st-contested">{t.statuses.contested} contested</span> : null}
                {t.claims} claims · {fmtDate(t.last)}
              </span>
            </span>
            {t.now && <span className="topic-now">{t.now}</span>}
          </button>
        ))}
      </div>
    </>
  );
  if (!wide) return <div className="topics">{views}{list}</div>;
  return (
    <div className="topics split">
      <div className="split-main">{views}</div>
      <div className="split-side">{page || list}</div>
    </div>
  );
}

function TopicPage({ id, scope, onBack, onOpen, onSource }: { id: number; scope: string; onBack: () => void; onOpen: (id: number) => void } & NavProps) {
  const [t, setT] = useState<TopicView | null | undefined>(undefined);
  const [showHistory, setShowHistory] = useState(false);
  const [claimOpen, setClaimOpen] = useState<number | null>(null);
  const [picked, setPicked] = useState<Set<number> | null>(null);
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
      {(t.entities?.length ?? 0) > 0 && (
        <div className="chips">{t.entities!.slice(0, 8).map((e) => <span key={e.id} className={'chip t-' + e.type}>{e.name}</span>)}</div>
      )}
      {(t.people?.length ?? 0) > 0 && (
        <div className="people"><span className="dim">who: </span>{t.people!.slice(0, 8).map((p) => `${p.name} (${p.claims})`).join(' · ')}</div>
      )}

      {t.thread && scope === 'owner' && <ThreadEdit id={t.id} scope={scope} picked={picked} setPicked={setPicked} onDone={reload} />}
      {t.thread ? (
        <ThreadSection d={t.thread} onSource={onSource} owner={scope === 'owner'} onChanged={reload} picked={picked} onPick={(x) => {
          const n = new Set(picked ?? []);
          if (n.has(x)) n.delete(x); else n.add(x);
          setPicked(n);
        }} />
      ) : (
        <>
          <div className="section-head"><span>What's believed now</span></div>
          {live.length === 0 && <Empty>Nothing current; see history.</Empty>}
          {live.map((c) => <ClaimCard key={c.id} c={c} scope={scope} open={claimOpen === c.id} onToggle={() => setClaimOpen(claimOpen === c.id ? null : c.id)} onSource={onSource} onChanged={reload} />)}
        </>
      )}

      {!t.thread && past.length > 0 && (
        <>
          <button className="section-head link-head" onClick={() => setShowHistory(!showHistory)}>
            <span>{showHistory ? '▾' : '▸'} History: superseded, disproven, answered, done, stale ({past.length})</span>
          </button>
          {showHistory && past.map((c) => <ClaimCard key={c.id} c={c} scope={scope} open={claimOpen === c.id} onToggle={() => setClaimOpen(claimOpen === c.id ? null : c.id)} onSource={onSource} onChanged={reload} />)}
        </>
      )}

      {relKinds.map(([kind, title]) => {
        const rs = (t.related ?? []).filter((r) => r.kind === kind).slice(0, 8);
        if (!rs.length) return null;
        return (
          <div key={kind}>
            <div className="section-head"><span>{title}</span></div>
            {rs.map((r) => (
              <button key={r.id} className="topic-row" onClick={() => onOpen(r.id)}>
                <span className="topic-label">{r.label}</span>
                <span className="topic-meta">
                  {kind === 'same' ? (r.via ?? []).join(', ') : r.unconnected ? <span className="status st-open" title="similar in meaning, but never discussed together">never discussed together</span> : kind === 'shared' ? `${r.shared} statements` : `${r.shared} times back to back`}
                </span>
              </button>
            ))}
          </div>
        );
      })}

      {(t.events?.length ?? 0) > 0 && (
        <>
          <div className="section-head"><span>How this topic changed</span></div>
          <div className="events">
            {t.events!.slice(0, 12).map((e, i) => (
              <div key={i} className="event"><span className="dim">{fmtDate(e.at)}</span> {e.event}{e.other ? ` #${e.other}` : ''} <span className="dim">{e.detail}</span></div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// ThreadSection: a thread's written status, then its current items, with
// what was replaced folded away.
function ThreadSection({ d, onSource, owner, onChanged, picked, onPick }: { d: ThreadDetail; onSource: (itemId: string, chunkId: number) => void; owner: boolean; onChanged: () => void; picked?: Set<number> | null; onPick?: (id: number) => void }) {
  const [showRetired, setShowRetired] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const all = [...(d.current ?? []), ...(d.retired ?? [])];
  const byId = new Map(all.map((it) => [it.id, it]));
  const cur = d.current ?? [];
  const retired = d.retired ?? [];
  const st = d.status;
  return (
    <>
      {st ? (
        <section className="thread-status">
          <div className="section-head">
            <span>Where it stands</span>
            <span className="dim">as of {fmtDate(d.asOf)}{d.partial ? ' · from what this view may show' : ''}</span>
          </div>
          <p className="status-now">
            <Cited text={st.now} byId={byId} onSource={onSource} />
            <Refs ids={st.now_ids} byId={byId} onSource={onSource} />
          </p>
          {(st.changed?.length ?? 0) > 0 && (
            <>
              <div className="status-head">Changed recently</div>
              <ul className="status-list">{st.changed!.map((p, i) => <PointRow key={i} p={p} byId={byId} onSource={onSource} lead={p.date} />)}</ul>
            </>
          )}
          {(st.open?.length ?? 0) > 0 && (
            <>
              <div className="status-head">Open</div>
              <ul className="status-list">{st.open!.map((p, i) => <PointRow key={i} p={p} byId={byId} onSource={onSource} tail={[p.who, p.due && 'due ' + p.due].filter(Boolean).join(' · ')} />)}</ul>
            </>
          )}
        </section>
      ) : (
        <div className="dim small">No written status yet: run <code>musubi knowledge</code> to write one.</div>
      )}
      <div className="section-head"><span>Current ({cur.length})</span></div>
      {cur.length === 0 && <Empty>Nothing current; see what was replaced.</Empty>}
      {(showAll || picked ? cur : cur.slice(0, 12)).map((it) => <ItemRow key={it.id} it={it} onSource={onSource} owner={owner} onChanged={onChanged} picked={picked} onPick={onPick} />)}
      {cur.length > 12 && !showAll && <button className="link" onClick={() => setShowAll(true)}>show all {cur.length}</button>}
      {retired.length > 0 && (
        <>
          <button className="section-head link-head" onClick={() => setShowRetired(!showRetired)}>
            <span>{showRetired ? '▾' : '▸'} Replaced, done or answered ({retired.length})</span>
          </button>
          {(showRetired || picked) && retired.map((it) => <ItemRow key={it.id} it={it} onSource={onSource} owner={owner} onChanged={onChanged} picked={picked} onPick={onPick} />)}
        </>
      )}
    </>
  );
}

// Cited turns the status writer's inline ids, "(5435)" or "[12, 14]", into
// chips that open the source.
export function Cited({ text, byId, onSource }: { text: string; byId: Map<number, ThreadItem>; onSource: (itemId: string, chunkId: number) => void }) {
  const parts: React.ReactNode[] = [];
  const re = /\s*[(\[]\s*(\d+(?:\s*,\s*\d+)*)\s*[)\]]/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    const ids = m[1].split(',').map((x) => parseInt(x.trim(), 10));
    if (!ids.some((id) => byId.has(id))) continue;
    if (m.index > last) parts.push(text.slice(last, m.index));
    parts.push(<Refs key={m.index} ids={ids} byId={byId} onSource={onSource} />);
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

export function Refs({ ids, byId, onSource }: { ids: number[] | null | undefined; byId: Map<number, ThreadItem>; onSource: (itemId: string, chunkId: number) => void }) {
  const its = (ids ?? []).map((id) => byId.get(id)).filter((x): x is ThreadItem => !!x);
  if (!its.length) return null;
  return (
    <>
      {' '}
      {its.slice(0, 4).map((it) => (
        <button key={it.id} className="cite" onClick={() => onSource(it.itemId, it.chunkId)} title={`${it.itemTitle} · ${fmtDate(it.saidAt)}\n${it.text}`}>
          {fmtDate(it.saidAt)}
        </button>
      ))}
    </>
  );
}

function PointRow({ p, byId, onSource, lead, tail }: { p: StatusPoint; byId: Map<number, ThreadItem>; onSource: (itemId: string, chunkId: number) => void; lead?: string; tail?: string }) {
  return (
    <li>
      {lead && <span className="dim">{lead} · </span>}
      <Cited text={p.text} byId={byId} onSource={onSource} />
      {tail && <span className="dim"> — {tail}</span>}
      <Refs ids={p.ids} byId={byId} onSource={onSource} />
    </li>
  );
}

// ItemRow is a thread's item; the owner can correct its text or who said
// it (the graph is mended around it: Threader.Correct).
function ItemRow({ it, onSource, owner, onChanged, picked, onPick }: { it: ThreadItem; onSource: (itemId: string, chunkId: number) => void; owner?: boolean; onChanged?: () => void; picked?: Set<number> | null; onPick?: (id: number) => void }) {
  const who = (it.who?.length ? it.who.join(', ') : it.holder) || '';
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(it.text);
  const [names, setNames] = useState(who);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  if (editing) {
    const save = () => {
      setBusy(true);
      setErr('');
      api.correctStatement(it.id, text === it.text ? '' : text, names === who ? '' : names).then(
        () => { setEditing(false); setBusy(false); onChanged?.(); },
        (e) => { setErr(String(e)); setBusy(false); },
      );
    };
    return (
      <div className="stmt editing">
        <input value={names} onChange={(e) => setNames(e.target.value)} placeholder="who said it (names, comma-separated)" />
        <textarea value={text} rows={3} onChange={(e) => setText(e.target.value)} />
        <div className="edit-actions">
          {busy ? <Spinner /> : <button className="chip" onClick={save} disabled={text === it.text && names === who}>save</button>}
          <button className="link" onClick={() => setEditing(false)}>cancel</button>
          <span className="dim small">The original is kept; the change is recorded.</span>
        </div>
        {err && <div className="error small">{err}</div>}
      </div>
    );
  }
  return (
    <div className={'stmt-wrap' + (picked ? ' picking' : '')}>
    {picked && <input type="checkbox" className="stmt-pick" checked={picked.has(it.id)} onChange={() => onPick?.(it.id)} title="Split this out" />}
    {owner && !picked && <button className="link stmt-edit" onClick={() => setEditing(true)} title="Correct the text or who said it">correct</button>}
    <button className={'stmt' + (it.current ? '' : ' retired')} onClick={() => onSource(it.itemId, it.chunkId)} title="Open the source passage">
      <div className="stmt-head">
        <b>{who || 'unknown'}</b>
        <span className="dim">{it.kind}{it.status ? '/' + it.status : ''} · {fmtDate(it.saidAt)}</span>
        <LabelBadge label={it.label} compact />
      </div>
      <div className="stmt-text">{it.text}</div>
      <div className="stmt-src">{it.itemTitle}</div>
    </button>
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
        <span className="dim">{s.kind} · {Math.round(s.certainty * 100)}% sure · {fmtDate(s.saidAt)}</span>
        {s.value?.type && s.value.type !== 'none' && <span className="value-chip" title={s.value.normalized ? 'read as a ' + s.value.type : 'not understood as a ' + s.value.type}>{s.value.raw}</span>}
        <LabelBadge label={s.label} compact />
      </div>
      <div className="stmt-text">{!s.grounded && <span className="ungrounded" title={s.issues}>⚠ unchecked</span>}{s.text}</div>
      {s.quote && <div className="stmt-quote">“{s.quote}”</div>}
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

// ThreadEdit: the owner merges another thread into this one, or splits
// picked statements out to a thread of their own. Both are recorded to the
// side, applied again after a rethread, and can be undone; the threads'
// statuses are written again in the background.
function ThreadEdit({ id, scope, picked, setPicked, onDone }: { id: number; scope: string; picked: Set<number> | null; setPicked: (s: Set<number> | null) => void; onDone: () => void }) {
  const [threads, setThreads] = useState<TopicSummary[]>([]);
  const [mode, setMode] = useState<'' | 'merge'>('');
  const [other, setOther] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [last, setLast] = useState<{ id: number; what: string } | null>(null);
  useEffect(() => {
    if (mode === 'merge' && threads.length === 0) api.topics(scope).then((ts) => setThreads((ts ?? []).filter((x) => x.id !== id)));
  }, [mode]);
  const run = (p: Promise<number>, what: string) => {
    setBusy(true);
    setErr('');
    p.then(
      (eid) => { setBusy(false); setLast({ id: eid, what }); setMode(''); setPicked(null); setOther(''); setName(''); onDone(); },
      (e) => { setBusy(false); setErr(String(e)); },
    );
  };
  const target = threads.find((x) => x.label === other);
  return (
    <div className="thread-edit">
      {!mode && !picked && (
        <>
          <button className="link" onClick={() => setMode('merge')}>merge another thread into this</button>
          <span className="dim"> · </span>
          <button className="link" onClick={() => setPicked(new Set())}>split statements out…</button>
        </>
      )}
      {mode === 'merge' && (
        <div className="edit-actions">
          <input list="merge-threads" value={other} onChange={(e) => setOther(e.target.value)} placeholder="the thread to merge in" />
          <datalist id="merge-threads">{threads.map((x) => <option key={x.id} value={x.label} />)}</datalist>
          {busy ? <Spinner /> : <button className="chip" disabled={!target} onClick={() => target && run(api.mergeThreads(id, target.id), `merged “${target.label}” in`)}>merge</button>}
          <button className="link" onClick={() => setMode('')}>cancel</button>
        </div>
      )}
      {picked && (
        <div className="edit-actions">
          <span className="dim">{picked.size} picked</span>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="name of the new thread" />
          {busy ? <Spinner /> : <button className="chip" disabled={picked.size === 0 || !name.trim()} onClick={() => run(api.splitThread(id, [...picked], name.trim()), `split ${picked.size} out to “${name.trim()}”`)}>split</button>}
          <button className="link" onClick={() => setPicked(null)}>cancel</button>
        </div>
      )}
      {last && (
        <div className="dim small">
          {last.what}; kept if the threads are rebuilt. <button className="link" onClick={() => api.undoThreadEdit(last.id).then(() => { setLast(null); onDone(); }, (e) => setErr(String(e)))}>undo</button>
        </div>
      )}
      {err && <div className="error small">{err}</div>}
    </div>
  );
}
