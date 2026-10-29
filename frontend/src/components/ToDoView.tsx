import { useEffect, useMemo, useState } from 'react';
import { api, fmtDate } from '../api';
import type { Hit, Statement, ToDo } from '../types';
import { Empty, Spelled, Spinner } from './common';

// ToDoView: everything someone said would happen (commitments) or, asked
// for, should happen (proposals), with what came of it: done or changed by
// something said later, raised again, gone quiet, overdue.
const sections: { key: string; title: string; note?: string; test: (t: ToDo) => boolean }[] = [
  { key: 'overdue', title: 'Overdue', test: (t) => t.state === 'open' && !!t.overdue },
  { key: 'open', title: 'Open', test: (t) => t.state === 'open' && !t.overdue },
  { key: 'proposed', title: 'Proposed', note: 'said should happen, not agreed', test: (t) => t.state === 'proposed' },
  { key: 'quiet', title: 'Gone quiet', note: 'nothing said of these since; check before chasing', test: (t) => t.state === 'quiet' },
  { key: 'closed', title: 'Done or changed', test: (t) => t.state === 'done' || t.state === 'changed' },
];
const pageSize = 50;

export function ToDoView({ scope, onOpen, onTopic }: { scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void }) {
  const [all, setAll] = useState<ToDo[] | null | undefined>(undefined);
  const [err, setErr] = useState('');
  const [mine, setMine] = useState(true);
  const [proposals, setProposals] = useState(false);
  const [q, setQ] = useState('');
  const [shown, setShown] = useState<Record<string, number>>({});
  const [open, setOpen] = useState<number | null>(null);
  const src = (s: Statement) => onOpen({ itemId: s.itemId, chunkId: s.chunkId } as unknown as Hit, scope);

  useEffect(() => {
    setAll(undefined);
    setErr('');
    const t = setTimeout(() => api.toDos(proposals, scope).then((x) => setAll(x ?? []), (e) => { setErr(String(e)); setAll(null); }), 150);
    return () => clearTimeout(t);
  }, [proposals, scope]);

  const list = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    return (all ?? []).filter((t) => (!mine || t.mine) &&
      words.every((w) => (t.text + ' ' + (t.owners ?? []).join(' ') + ' ' + (t.itemTitle ?? '')).toLowerCase().includes(w)));
  }, [all, mine, q]);

  return (
    <div className="view todo">
      <div className="todo-filters">
        <div className="chips">
          <button className={'chip' + (mine ? ' on' : '')} onClick={() => setMine(true)}>Mine</button>
          <button className={'chip' + (!mine ? ' on' : '')} onClick={() => setMine(false)}>Everyone's</button>
          <button className={'chip' + (proposals ? ' on' : '')} title="Also what someone said should happen, not yet agreed" onClick={() => setProposals(!proposals)}>+ proposals</button>
        </div>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter: words, a name, a meeting" />
      </div>
      {err && <div className="error small">{err}</div>}
      {all === undefined && <Spinner />}
      {all && list.length === 0 && <Empty>{mine ? "Nothing you said you would do. Try “Everyone's”." : 'Nothing found.'}</Empty>}
      {all && sections.map((sec) => {
        const items = list.filter(sec.test);
        if (items.length === 0) return null;
        const n = shown[sec.key] ?? (sec.key === 'closed' || sec.key === 'quiet' ? 10 : pageSize);
        return (
          <section key={sec.key}>
            <div className="section-head"><span>{sec.title} <span className="dim">{items.length}</span></span>{sec.note && <span className="dim">{sec.note}</span>}</div>
            <ul className="todo-list">
              {items.slice(0, n).map((t) => (
                <li key={t.id} className={'todo-' + t.state}>
                  <div className="todo-text" onClick={() => setOpen(open === t.id ? null : t.id)}>
                    {(t.state === 'done' || t.state === 'changed') && <span className="todo-mark">{t.state === 'done' ? '✓' : '↻'}</span>}
                    <Spelled s={t} />
                  </div>
                  <div className="dim small todo-meta">
                    {(t.owners ?? []).join(', ')}
                    {t.due && <span className={'badge' + (t.overdue ? ' prep-overdue' : ' todo-due')}>{t.due}</span>}
                    {' · '}<button className="link" onClick={() => src(t)}>{fmtDate(t.saidAt)}, {t.itemTitle || 'source'}</button>
                    {t.raised > 1 && <span> · raised {t.raised}×, last {fmtDate(t.lastRaised)}</span>}
                    {!!t.topic && <> · <button className="link" onClick={() => onTopic(t.topic!)}>thread</button></>}
                  </div>
                  {t.outcome && (
                    <div className="small todo-outcome">
                      <span className="dim">{t.state === 'done' ? 'Done: ' : 'Changed: '}</span>
                      <Spelled s={t.outcome} />
                      <span className="dim"> — </span><button className="link" onClick={() => src(t.outcome!)}>{fmtDate(t.outcome.saidAt)}, {t.outcome.itemTitle || 'source'}</button>
                    </div>
                  )}
                  {open === t.id && (t.repeats?.length ?? 0) > 0 && (
                    <ul className="status-list">
                      {t.repeats!.map((r) => (
                        <li key={r.id}><button className="link" onClick={() => src(r)}>{fmtDate(r.saidAt)}, {r.itemTitle || 'source'}</button><span className="dim">: </span><Spelled s={r} /></li>
                      ))}
                    </ul>
                  )}
                </li>
              ))}
            </ul>
            {items.length > n && <button className="link small" onClick={() => setShown({ ...shown, [sec.key]: n + pageSize })}>show {Math.min(pageSize, items.length - n)} more</button>}
          </section>
        );
      })}
    </div>
  );
}
