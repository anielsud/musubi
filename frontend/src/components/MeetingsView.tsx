import { useEffect, useState } from 'react';
import { api, fmtDate } from '../api';
import type { Hit, MeetingListItem, MeetingPage, MeetingToDo } from '../types';
import { Empty, Spinner } from './common';
import { Names } from './people';

// MeetingsView: each meeting with a summary (knowledge.digest), newest
// first; opening one shows the summary in sections (each linked to where
// in the meeting it was said), the to-dos said in it and how they stand
// now, and the threads its statements are in. A summary is what was said
// in that meeting, on that day: where a subject stands now is on its thread.
const stateWords: Record<MeetingToDo['state'], string> = {
  open: 'open', quiet: 'gone quiet', proposed: 'proposed', done: 'done', changed: 'changed', not: 'not a to-do',
  repeated: 'said again later', declined: 'turned down',
};
const agreedWords: Record<MeetingToDo['agreed'], string> = {
  agreed: '', asked: 'asked, not accepted', floated: 'suggested, nobody took it on', declined: 'turned down',
};

export function MeetingsView({ scope, onOpen, onTopic, req }: {
  scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; req: { id: string; n: number } | null;
}) {
  const [list, setList] = useState<MeetingListItem[] | null | undefined>(undefined);
  const [sel, setSel] = useState<string | null>(null);
  const [err, setErr] = useState('');
  const [q, setQ] = useState('');

  useEffect(() => {
    setErr('');
    const t = setTimeout(() => api.meetingList(scope).then((x) => setList(x ?? []), (e) => { setErr(String(e)); setList(null); }), 150);
    return () => clearTimeout(t);
  }, [scope]);
  useEffect(() => { if (req) setSel(req.id); }, [req]);

  if (sel) return <MeetingPageView id={sel} scope={scope} onBack={() => setSel(null)} onOpen={onOpen} onTopic={onTopic} />;
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const shown = (list ?? []).filter((m) => words.every((w) => (m.title + ' ' + m.overview).toLowerCase().includes(w)));
  return (
    <div className="view meetings">
      <div className="todo-filters">
        <input className="search" placeholder="Filter meetings" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      {err && <div className="error small">{err}</div>}
      {list === undefined && <div className="empty"><Spinner /></div>}
      {list && list.length === 0 && <Empty>No meeting summaries yet. They are written when knowledge.digest is on.</Empty>}
      <ul className="meeting-list">
        {shown.map((m) => (
          <li key={m.itemId}>
            <button className="meeting-row" onClick={() => setSel(m.itemId)}>
              <div className="meeting-head"><span className="mtg-title">{m.title || m.itemId}</span><span className="dim small">{fmtDate(m.start)}</span></div>
              <div className="meeting-overview small">{m.overview}</div>
              <div className="dim small">{m.sections} subjects · {m.todos} to-dos</div>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function MeetingPageView({ id, scope, onBack, onOpen, onTopic }: {
  id: string; scope: string; onBack: () => void; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void;
}) {
  const [page, setPage] = useState<MeetingPage | null | undefined>(undefined);
  const [err, setErr] = useState('');
  useEffect(() => {
    setPage(undefined);
    setErr('');
    api.meetingPage(id, scope).then((p) => setPage(p ?? null), (e) => { setErr(String(e)); setPage(null); });
  }, [id, scope]);
  const src = (chunkId: number) => onOpen({ itemId: id, chunkId } as unknown as Hit, scope);
  const todos = page?.todos ?? [];
  return (
    <div className="view meeting-page">
      <button className="link" onClick={onBack}>← all meetings</button>
      {err && <div className="error small">{err}</div>}
      {page === undefined && <div className="empty"><Spinner /></div>}
      {page === null && !err && <Empty>No summary for this meeting, or none that is fine to show here.</Empty>}
      {page && (
        <>
          <h2 className="meeting-h">{page.title || page.itemId} <span className="dim small">{fmtDate(page.start)}</span></h2>
          <p className="meeting-overview">{page.overview}</p>
          {(page.topics?.length ?? 0) > 0 && (
            <div className="chips" title="Threads this meeting's statements are in">
              {page.topics!.map((t) => (
                <button key={t.id} className="chip" onClick={() => onTopic(t.id)} title={`${t.statements} statements from this meeting, ${t.size} in all`}>{t.label}</button>
              ))}
            </div>
          )}
          <div className="section-head"><span>Summary</span><span className="dim small">as said in this meeting</span></div>
          {(page.sections ?? []).map((s) => (
            <div key={s.n} className="meeting-sec">
              <div className="meeting-sec-head">
                <b>{s.subject}</b>
                {!s.grounded && <span className="badge" title={s.issues}>check wording</span>}
              </div>
              <div className="small">{s.text}</div>
              <div className="dim small">
                <Names names={s.people ?? []} />
                {(s.spans?.length ?? 0) > 0 && <>{(s.people?.length ?? 0) > 0 ? ' · ' : ''}<button className="link" onClick={() => src(s.spans![0].fromChunk)}>where it was said</button></>}
              </div>
            </div>
          ))}
          <div className="section-head"><span>To-dos ({todos.length})</span><span className="dim small">state as the To-do tab reads it</span></div>
          {todos.length === 0 && <Empty>None listed.</Empty>}
          <ul className="meeting-todos">
            {todos.map((t) => (
              <li key={t.id} className={'todo-' + (t.state === 'repeated' ? 'open' : t.state)}>
                <div>{t.state === 'done' && <span className="todo-mark">✓ </span>}{t.text}</div>
                <div className="dim small">
                  {(t.owners?.length ?? 0) > 0 ? <Names names={t.owners!} /> : 'owner unclear'}
                  {t.due ? <span className="badge todo-due">{t.due}</span> : t.dueText ? <span className="dim"> · {t.dueText}</span> : null}
                  {' · '}{stateWords[t.state]}
                  {agreedWords[t.agreed] && t.state !== 'declined' && <> · {agreedWords[t.agreed]}</>}
                  {!t.grounded && <span className="badge" title={t.issues}>check wording</span>}
                  {t.evidence && t.evidence.length > 0 && <> · <button className="link" onClick={() => src(t.evidence![0].chunkId)}>source</button></>}
                  {!!t.topic && <> · <button className="link" onClick={() => onTopic(t.topic!)}>{t.topicLabel || 'thread'}</button></>}
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
