import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { api, fmtDate } from '../api';
import type { Hit, MeetingListItem, MeetingPage, MeetingToDo, PrepRequest, PrepTarget } from '../types';
import { Empty, Spinner } from './common';
import { Names } from './people';
import { PrepPage } from './PrepView';

// A meeting's page shows the summary in sections (each linked to where
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

// A row of the merged list: a past meeting with a summary, or one to
// prepare for (upcoming on the calendar).
interface Row { id: string; title: string; at: Date; upcoming: boolean; m?: MeetingListItem; people?: number }

const dayKey = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
const dayWords = (d: Date) => {
  const today = dayKey(new Date());
  const t = new Date(); t.setDate(t.getDate() + 1);
  const y = new Date(); y.setDate(y.getDate() - 1);
  const k = dayKey(d);
  const name = k === today ? 'Today' : k === dayKey(t) ? 'Tomorrow' : k === dayKey(y) ? 'Yesterday' : '';
  const date = d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: d.getFullYear() === new Date().getFullYear() ? undefined : 'numeric' });
  return name ? `${name} · ${date}` : date;
};
const timeWords = (d: Date) => d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });

// MeetingsView: past meetings and the ones coming up, in one list by day
// (newest first, the next two weeks above today) or on a month calendar.
// A past meeting opens its summary; an upcoming one opens its prep.
export function MeetingsView({ scope, onOpen, onTopic, req }: {
  scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; req: { id: string; n: number } | null;
}) {
  const [list, setList] = useState<MeetingListItem[] | null | undefined>(undefined);
  const [targets, setTargets] = useState<PrepTarget[]>([]);
  const [sel, setSel] = useState<string | null>(null);
  const [prep, setPrep] = useState<PrepRequest | null>(null);
  const [err, setErr] = useState('');
  const [q, setQ] = useState('');
  const [names, setNames] = useState('');
  const [mode, setMode] = useState<'list' | 'calendar'>('list');
  const [month, setMonth] = useState(() => { const d = new Date(); return new Date(d.getFullYear(), d.getMonth(), 1); });
  const [day, setDay] = useState<string | null>(null);
  const listEl = useRef<HTMLUListElement>(null);

  useEffect(() => {
    setErr('');
    const t = setTimeout(() => {
      api.meetingList(scope).then((x) => setList(x ?? []), (e) => { setErr(String(e)); setList(null); });
      api.prepTargets().then((x) => setTargets(x ?? []), () => setTargets([]));
    }, 150);
    return () => clearTimeout(t);
  }, [scope]);
  useEffect(() => { if (req) { setPrep(null); setSel(req.id); } }, [req]);

  const rows = useMemo(() => {
    const up: Row[] = targets.filter((t) => t.upcoming).map((t) => ({ id: t.itemId, title: t.title, at: new Date(t.at), upcoming: true, people: t.people }));
    const past: Row[] = (list ?? []).map((m) => ({ id: m.itemId, title: m.title || m.itemId, at: new Date(m.start), upcoming: false, m }));
    return [...up, ...past].sort((x, y) => y.at.getTime() - x.at.getTime());
  }, [list, targets]);
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const shown = rows.filter((r) => words.every((w) => (r.title + ' ' + (r.m?.overview ?? '')).toLowerCase().includes(w)));
  const byDay = useMemo(() => {
    const g = new Map<string, Row[]>();
    for (const r of shown) {
      const k = dayKey(r.at);
      g.set(k, [...(g.get(k) ?? []), r]);
    }
    // Within a day, in the order they happen.
    for (const rs of g.values()) rs.sort((x, y) => x.at.getTime() - y.at.getTime());
    return g;
  }, [shown]);

  // jump scrolls the list to a day, or the nearest day with meetings.
  const jump = (k: string) => {
    const keys = [...byDay.keys()];
    if (!keys.length) return;
    const near = keys.reduce((best, x) => (Math.abs(Date.parse(x) - Date.parse(k)) < Math.abs(Date.parse(best) - Date.parse(k)) ? x : best));
    listEl.current?.querySelector(`[data-day="${near}"]`)?.scrollIntoView({ block: 'start', behavior: 'smooth' });
  };
  // Opened, the list starts at today: above are the coming two weeks.
  const placed = useRef(false);
  useEffect(() => {
    if (placed.current || mode !== 'list' || sel || prep || byDay.size === 0) return;
    placed.current = true;
    setTimeout(() => {
      const keys = [...byDay.keys()];
      const k = keys.find((x) => x <= dayKey(new Date()));
      if (k) listEl.current?.querySelector(`[data-day="${k}"]`)?.scrollIntoView({ block: 'start' });
    }, 0);
  }, [byDay, mode, sel, prep]);
  const open = (r: Row) => (r.upcoming ? setPrep({ template: r.id }) : setSel(r.id));
  const back = () => { setSel(null); setPrep(null); };

  if (prep) return <PrepPage req={prep} scope={scope} onBack={back} onOpen={onOpen} onTopic={onTopic} />;
  if (sel) return <MeetingPageView id={sel} scope={scope} onBack={back} onOpen={onOpen} onTopic={onTopic} onPrep={() => setPrep({ template: sel })} />;

  const rowView = (r: Row) => (
    <li key={r.id}>
      <button className={'meeting-row' + (r.upcoming ? ' upcoming' : '')} onClick={() => open(r)}>
        <div className="meeting-head"><span className="mtg-title">{r.title}</span><span className="dim small">{timeWords(r.at)}</span></div>
        {r.m ? (
          <>
            <div className="meeting-overview small">{r.m.overview}</div>
            <div className="dim small">{r.m.sections} subjects · {r.m.todos} to-dos</div>
          </>
        ) : <div className="dim small">coming up · {r.people} {r.people === 1 ? 'other' : 'others'} · prep</div>}
      </button>
    </li>
  );

  return (
    <div className="view meetings">
      <div className="todo-filters meetings-bar">
        <input className="search" placeholder="Filter meetings" value={q} onChange={(e) => setQ(e.target.value)} />
        <div className="seg">
          <button className={mode === 'list' ? 'on' : ''} onClick={() => setMode('list')}>List</button>
          <button className={mode === 'calendar' ? 'on' : ''} onClick={() => setMode('calendar')}>Calendar</button>
        </div>
        {mode === 'list' && (
          <>
            <input type="date" title="Jump to a day" onChange={(e) => e.target.value && jump(e.target.value)} />
            <button className="link small" onClick={() => jump(dayKey(new Date()))}>today</button>
          </>
        )}
      </div>
      <form className="prep-people" onSubmit={(e) => { e.preventDefault(); const ps = names.split(',').map((x) => x.trim()).filter(Boolean); if (ps.length) setPrep({ people: ps }); }}>
        <input value={names} onChange={(e) => setNames(e.target.value)} placeholder="Prep for people: name, name" />
      </form>
      {err && <div className="error small">{err}</div>}
      {list === undefined && <div className="empty"><Spinner /></div>}
      {list && rows.length === 0 && <Empty>No meetings yet. Summaries are written when knowledge.digest is on; upcoming meetings come from the calendar.</Empty>}

      {mode === 'calendar' && (
        <MonthGrid month={month} setMonth={setMonth} byDay={byDay} day={day} setDay={setDay} />
      )}
      {mode === 'calendar' && day && (
        <ul className="meeting-list">
          <li className="day-head">{dayWords(new Date(day + 'T12:00'))}</li>
          {(byDay.get(day) ?? []).map(rowView)}
          {!byDay.get(day) && <li className="dim small">No meetings that day.</li>}
        </ul>
      )}

      {mode === 'list' && (
        <ul className="meeting-list" ref={listEl}>
          {[...byDay.entries()].map(([k, rs]) => (
            <Fragment key={k}>
              <li className={'day-head' + (k === dayKey(new Date()) ? ' today' : '')} data-day={k}>{dayWords(rs[0].at)}</li>
              {rs.map(rowView)}
            </Fragment>
          ))}
        </ul>
      )}
    </div>
  );
}

// MonthGrid is a month calendar, Monday first: each day shows how many
// meetings it has (past and upcoming); a click shows them below.
function MonthGrid({ month, setMonth, byDay, day, setDay }: {
  month: Date; setMonth: (d: Date) => void; byDay: Map<string, Row[]>; day: string | null; setDay: (k: string) => void;
}) {
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  const lead = (first.getDay() + 6) % 7;
  const days = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate();
  const cells: (Date | null)[] = [...Array(lead).fill(null), ...Array.from({ length: days }, (_, i) => new Date(month.getFullYear(), month.getMonth(), i + 1))];
  const today = dayKey(new Date());
  const step = (n: number) => setMonth(new Date(month.getFullYear(), month.getMonth() + n, 1));
  return (
    <div className="month">
      <div className="month-head">
        <button className="link" onClick={() => step(-1)}>‹</button>
        <span>{month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}</span>
        <button className="link" onClick={() => step(1)}>›</button>
      </div>
      <div className="month-grid">
        {['M', 'T', 'W', 'T', 'F', 'S', 'S'].map((d, i) => <div key={i} className="dow dim small">{d}</div>)}
        {cells.map((d, i) => {
          if (!d) return <div key={i} />;
          const k = dayKey(d);
          const rs = byDay.get(k) ?? [];
          const up = rs.some((r) => r.upcoming);
          return (
            <button key={i} className={'mday' + (k === today ? ' today' : '') + (k === day ? ' on' : '') + (rs.length ? ' has' : '')} onClick={() => setDay(k)}
              title={rs.map((r) => r.title).join('\n')}>
              <span>{d.getDate()}</span>
              {rs.length > 0 && <span className={'mcount' + (up ? ' up' : '')}>{rs.length}</span>}
            </button>
          );
        })}
      </div>
    </div>
  );
}

function MeetingPageView({ id, scope, onBack, onOpen, onTopic, onPrep }: {
  id: string; scope: string; onBack: () => void; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; onPrep: () => void;
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
      <div className="page-nav">
        <button className="link" onClick={onBack}>← all meetings</button>
        <button className="link" onClick={onPrep} title="What to bring to the next one with these people">prep for the next one →</button>
      </div>
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
