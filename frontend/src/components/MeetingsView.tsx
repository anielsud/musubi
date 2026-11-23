import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { api, fmtDate, on, speakerName } from '../api';
import type { Hit, LiveStatus, MeetingDetails, MeetingListItem, MeetingPage, MeetingToDo, PrepRequest, PrepTarget } from '../types';
import { Empty, LabelBadge, Spinner } from './common';
import { Names } from './people';
import { PrepPage } from './PrepView';
import { LookTimeline } from './recorder';
import { ActNotes, ReviewItems, VoicesError, openReviews, readOnlyWhy, useAct, useVoices } from './voices';
import type { MeetingSpeakers, SplitResult, VoiceProfile } from '../types';

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

// A row of the merged list: a past meeting with a summary (or, pending,
// with its transcript and the summary still coming), one to prepare for
// (upcoming on the calendar), or the call being recorded (now: live, or
// ended and its transcript still being processed).
interface Row { id: string; title: string; at: Date; upcoming: boolean; m?: MeetingListItem; people?: number; past?: boolean; rec?: string; now?: 'live' | 'processing' }

// Recorded is the call Live followed last, kept after it ends until its
// recording is in the list (or a while has passed: a recording too short
// to keep never arrives).
interface Recorded { id: string; title: string; at: Date; eventId?: string; ended?: Date }
const processingFor = 60 * 60 * 1000;

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

// useWide follows the landscape breakpoint (style.css, 1000px).
function useWide() {
  const q = '(min-width: 1000px)';
  const [wide, setWide] = useState(() => window.matchMedia(q).matches);
  useEffect(() => {
    const m = window.matchMedia(q);
    const f = () => setWide(m.matches);
    m.addEventListener('change', f);
    return () => m.removeEventListener('change', f);
  }, []);
  return wide;
}

type Sel = { kind: 'recap'; id: string } | { kind: 'prep'; req: PrepRequest; id?: string };

// MeetingsView: past meetings and the ones coming up, by day in the order
// they happen, from a week back to a week ahead (earlier ones a click or a
// calendar day away), with a line at now, which the list opens centred on. A past meeting opens its recap; an upcoming one its prep.
// One box filters the list, or names people to prep for. Wide, the
// calendar and list are on the left and the box and the open meeting on
// the right (the next meeting open to start); narrow, a meeting opens in
// place of the list.
export function MeetingsView({ scope, onOpen, onTopic, req, active, live, onLive }: {
  scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; req: { id: string; n: number } | null;
  // active: the tab is showing. The app mounts every tab hidden, and a
  // hidden list can't be scrolled, so centring on now waits for it.
  active: boolean;
  // live: Live's state, for the call being recorded (null: not listed,
  // knowledge.digest.list_pending off); onLive opens Live.
  live: LiveStatus | null; onLive: () => void;
}) {
  const [list, setList] = useState<MeetingListItem[] | null | undefined>(undefined);
  const [targets, setTargets] = useState<PrepTarget[]>([]);
  const [sel, setSel] = useState<Sel | null>(null);
  const [err, setErr] = useState('');
  const [q, setQ] = useState('');
  const [month, setMonth] = useState(() => { const d = new Date(); return new Date(d.getFullYear(), d.getMonth(), 1); });
  const [day, setDay] = useState<string | null>(null);
  // from: the earliest day the list shows; a week back to start.
  const [from, setFrom] = useState(() => { const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - 7); return d; });
  const [pending, setPending] = useState<string | null>(null);
  const listEl = useRef<HTMLUListElement>(null);
  const wide = useWide();
  // The call Live is following (not a replay), and once it ends, until
  // its recording shows in the list.
  const [recorded, setRecorded] = useState<Recorded | null>(null);
  useEffect(() => {
    const se = live?.session;
    if (live?.mode === 'live' && se) {
      setRecorded((r) => (r?.id === se.id && !r.ended && r.title === se.title ? r : { id: se.id, title: se.title, at: new Date(se.created_at), eventId: se.eventId }));
    } else {
      setRecorded((r) => (r && !r.ended ? { ...r, ended: new Date() } : r));
    }
  }, [live?.mode, live?.session?.id, live?.session?.title]);

  // Loaded when the tab shows, after each update (a saved recording shows
  // as soon as it is imported, not at the next 5-minute reload) and every
  // 5 minutes while it shows: meetings move from coming up to happened.
  useEffect(() => {
    if (!active) return;
    setErr('');
    const load = () => {
      api.meetingList(scope).then((x) => setList(x ?? []), (e) => { setErr(String(e)); setList(null); });
      api.prepTargets().then((x) => setTargets(x ?? []), () => setTargets([]));
    };
    const t = setTimeout(load, 150);
    const every = setInterval(load, 5 * 60 * 1000);
    const offs = [
      on<string>('meetings:changed', (id) => { setSel((s) => (s?.kind === 'recap' && s.id === id ? null : s)); load(); }),
      on('update:done', load),
      // A summary (or a name) was just written.
      on('meeting:ready', load),
    ];
    return () => { clearTimeout(t); clearInterval(every); offs.forEach((f) => f()); };
  }, [scope, active]);
  useEffect(() => { if (req) setSel({ kind: 'recap', id: req.id }); }, [req]);

  // The call being recorded, until its recording is in the list.
  const recNow = useMemo(() => {
    if (!recorded) return null;
    if ((list ?? []).some((m) => m.itemId === 'tomoe:' + recorded.id)) return null;
    if (recorded.ended && Date.now() - recorded.ended.getTime() > processingFor) return null;
    return { ...recorded, now: (recorded.ended ? 'processing' : 'live') as Row['now'] };
  }, [recorded, list]);
  const rows = useMemo(() => {
    // The call's calendar event stands for it: marked, not listed twice.
    const forCall = (id: string) => !!recNow?.eventId && recNow.eventId === id;
    const up: Row[] = targets.filter((t) => t.upcoming).map((t) => ({ id: t.itemId, title: t.title, at: new Date(t.at), upcoming: !forCall(t.itemId), people: t.people, now: forCall(t.itemId) ? recNow!.now : undefined }));
    const past: Row[] = (list ?? []).map((m) => ({ id: m.itemId, title: m.title || m.itemId, at: new Date(m.start), upcoming: false, m }));
    // A calendar event that has happened stands until its recording is
    // in the list; then the recording's row takes its place.
    const recapped = new Set((list ?? []).map((m) => m.itemId));
    const happened: Row[] = targets.filter((t) => t.past && t.upcoming !== true && !(t.recordingId && recapped.has(t.recordingId)))
      .map((t) => ({ id: t.itemId, title: t.title, at: new Date(t.at), upcoming: false, past: true, rec: t.recordingId, people: t.people, now: forCall(t.itemId) ? recNow!.now : undefined }));
    const call: Row[] = recNow && !(recNow.eventId && targets.some((t) => t.itemId === recNow.eventId))
      ? [{ id: 'live:' + recNow.id, title: recNow.title || 'Call', at: recNow.at, upcoming: false, now: recNow.now }] : [];
    return [...up, ...past, ...happened, ...call].sort((x, y) => x.at.getTime() - y.at.getTime());
  }, [list, targets, recNow]);
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  // A filter searches every meeting; otherwise the list keeps to its days.
  const ahead = useMemo(() => { const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() + 8); return d; }, []);
  const shown = rows.filter((r) => words.length ? words.every((w) => (r.title + ' ' + (r.m?.overview ?? '')).toLowerCase().includes(w)) : r.at >= from && r.at < ahead);
  const earlier = words.length === 0 && rows.some((r) => r.at < from);
  const group = (rs: Row[]) => {
    const g = new Map<string, Row[]>();
    for (const r of rs) {
      const k = dayKey(r.at);
      g.set(k, [...(g.get(k) ?? []), r]);
    }
    // Within a day, in the order they happen.
    for (const x of g.values()) x.sort((a, b) => a.at.getTime() - b.at.getTime());
    return g;
  };
  const byDay = useMemo(() => group(shown), [shown]);
  const allByDay = useMemo(() => group(rows), [rows]);

  // jump moves the list to a day (the nearest with meetings), taking in
  // earlier days first when it's before the list's first; today centres
  // the line at now.
  const jump = (k: string) => {
    setDay(k);
    const d = new Date(k + 'T00:00');
    if (d < from) setFrom(d);
    setPending(k);
  };
  useEffect(() => {
    if (!pending) return;
    const smooth = placed.current ? 'smooth' : 'auto';
    if (pending === dayKey(new Date())) {
      listEl.current?.querySelector('.now-line')?.scrollIntoView({ block: 'center', behavior: smooth });
    } else {
      const keys = [...byDay.keys()];
      const near = keys.length ? keys.reduce((best, x) => (Math.abs(Date.parse(x) - Date.parse(pending)) < Math.abs(Date.parse(best) - Date.parse(pending)) ? x : best)) : '';
      if (near) listEl.current?.querySelector(`[data-day="${near}"]`)?.scrollIntoView({ block: 'start', behavior: smooth });
    }
    setPending(null);
    placed.current = true;
  }, [pending, byDay]);
  // Opened, the list is centred on now. Wide, the next meeting (else the
  // latest) is open to start.
  const placed = useRef(false);
  useEffect(() => {
    if (placed.current || !active || rows.length === 0 || (!wide && sel)) return;
    setPending(dayKey(new Date()));
  }, [rows, wide, sel, active]);
  useEffect(() => {
    if (!wide || sel || rows.length === 0) return;
    const now = Date.now();
    const next = rows.find((r) => r.upcoming && r.at.getTime() >= now - 3600e3) ?? [...rows].reverse().find((r) => !!r.m);
    if (next) setSel(next.upcoming ? { kind: 'prep', req: { template: next.id }, id: next.id } : { kind: 'recap', id: next.id });
  }, [rows, wide, sel]);

  // A meeting open for prep that has since happened turns to its recap.
  useEffect(() => {
    if (sel?.kind !== 'prep' || !sel.id) return;
    const t = targets.find((x) => x.itemId === sel.id);
    if (t?.past) setSel(t.recordingId ? { kind: 'recap', id: t.recordingId } : null);
  }, [targets, sel]);

  // Coming up: its prep. Happened: its recap (the recording's), never prep.
  const open = (r: Row) => {
    if (r.now) onLive();
    else if (r.upcoming) setSel({ kind: 'prep', req: { template: r.id }, id: r.id });
    else if (r.m) setSel({ kind: 'recap', id: r.id });
    else if (r.rec) setSel({ kind: 'recap', id: r.rec });
  };
  const back = wide ? undefined : () => setSel(null);
  const people = q.split(',').map((x) => x.trim()).filter(Boolean);
  const prepPeople = () => { if (people.length) setSel({ kind: 'prep', req: { people } }); };
  const selId = sel?.kind === 'recap' ? sel.id : sel?.id;
  const isSel = (r: Row) => r.id === selId || (!!r.rec && r.rec === selId);

  const detail = sel && (sel.kind === 'prep'
    ? <PrepPage key={JSON.stringify(sel.req)} req={sel.req} scope={scope} onBack={back} onOpen={onOpen} onTopic={onTopic} />
    : <MeetingPageView id={sel.id} scope={scope} onBack={back} onOpen={onOpen} onTopic={onTopic} onPrep={() => setSel({ kind: 'prep', req: { template: sel.id } })}
      pending={(list ?? []).find((m) => m.itemId === sel.id && m.pending)} />);

  const box = (
    <form className="meetings-box" onSubmit={(e) => { e.preventDefault(); prepPeople(); }}>
      <input className="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter meetings, or name people to prep for" />
      {people.length > 0 && <button type="submit" className="link small" title="What to bring to a meeting with these people (names, comma-separated)">prep for {people.join(', ')} →</button>}
    </form>
  );

  const rowView = (r: Row) => (
    <li key={r.id}>
      <button className={'meeting-row' + (r.upcoming ? ' upcoming' : '') + (r.past && !r.now ? ' happened' : '') + (r.now ? ' now-' + r.now : '') + (wide && isSel(r) ? ' on' : '')} onClick={() => open(r)}
        disabled={r.past && !r.rec && !r.now}>
        <div className="meeting-head"><span className="mtg-title">{r.now === 'live' && <span className="live-dot" />}{r.title}</span><span className="dim small">{timeWords(r.at)}</span></div>
        {r.now ? (
          <div className="dim small">{r.now === 'live' ? 'recording now · open Live' : 'call ended · processing the transcript…'}</div>
        ) : r.m?.pending ? (
          <div className="dim small">{r.m.lines ? 'transcript ready · summary being written…' : 'processing the transcript…'}</div>
        ) : r.m ? (
          <>
            <div className="meeting-overview small">{r.m.overview}</div>
            <div className="dim small">{r.m.sections} subjects · {r.m.todos} to-dos</div>
          </>
        ) : r.past ? (
          <div className="dim small">{r.rec ? 'happened · summary coming' : 'happened · not recorded'}</div>
        ) : <div className="dim small">coming up · {r.people} {r.people === 1 ? 'other' : 'others'} · prep</div>}
      </button>
    </li>
  );

  const picker = (
    <>
      <MonthGrid month={month} setMonth={setMonth} byDay={allByDay} day={day} onDay={(k) => jump(k)} />
      <div className="mt-list">
      {err && <div className="error small">{err}</div>}
      {list === undefined && <div className="empty"><Spinner /></div>}
      {list && rows.length === 0 && <Empty>No meetings yet. Summaries are written when knowledge.digest is on; upcoming meetings come from the calendar.</Empty>}
      {rows.length > 0 && shown.length === 0 && <div className="dim small">No meeting matches “{q}”.</div>}
      <ul className="meeting-list" ref={listEl}>
        {earlier && <li><button className="link small" onClick={() => setFrom(new Date(from.getTime() - 7 * 864e5))}>earlier meetings</button></li>}
        {withNow(byDay, rowView)}
      </ul>
      </div>
    </>
  );

  if (!wide) {
    if (detail) return detail;
    return <div className="view meetings">{box}{picker}</div>;
  }
  return (
    <div className="view meetings wide">
      <div className="mt-left">{picker}</div>
      <div className="mt-right">
        {box}
        {detail ?? <Empty>Pick a meeting: a past one shows its recap, an upcoming one its prep.</Empty>}
      </div>
    </div>
  );
}

// withNow renders the days and their meetings with a line at now: before
// the first meeting still to come (today's heading shows even with none
// left, so the line has a place).
function withNow(byDay: Map<string, Row[]>, rowView: (r: Row) => JSX.Element) {
  const now = Date.now();
  const today = dayKey(new Date());
  const days = [...byDay.entries()];
  if (!byDay.has(today) && days.length) {
    const i = days.findIndex(([k]) => k > today);
    days.splice(i < 0 ? days.length : i, 0, [today, []]);
  }
  let drawn = false;
  const line = () => {
    drawn = true;
    return <li key="now" className="now-line"><span>now · {timeWords(new Date())}</span></li>;
  };
  return days.map(([k, rs]) => (
    <Fragment key={k}>
      <li className={'day-head' + (k === today ? ' today' : '')} data-day={k}>{dayWords(rs[0]?.at ?? new Date())}</li>
      {rs.map((r) => (!drawn && r.at.getTime() > now ? [line(), rowView(r)] : rowView(r)))}
      {k === today && !drawn && line()}
    </Fragment>
  ));
}

// MonthGrid is the month calendar the list is browsed by, Monday first:
// each day shows how many meetings it has (past and upcoming); a click
// moves the list to that day.
function MonthGrid({ month, setMonth, byDay, day, onDay }: {
  month: Date; setMonth: (d: Date) => void; byDay: Map<string, Row[]>; day: string | null; onDay: (k: string) => void;
}) {
  const first = new Date(month.getFullYear(), month.getMonth(), 1);
  const lead = (first.getDay() + 6) % 7;
  const days = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate();
  const cells: (Date | null)[] = [...Array(lead).fill(null), ...Array.from({ length: days }, (_, i) => new Date(month.getFullYear(), month.getMonth(), i + 1))];
  const today = dayKey(new Date());
  const step = (n: number) => setMonth(new Date(month.getFullYear(), month.getMonth() + n, 1));
  const toToday = () => { const d = new Date(); setMonth(new Date(d.getFullYear(), d.getMonth(), 1)); onDay(today); };
  return (
    <div className="month">
      <div className="month-head">
        <button className="link" onClick={() => step(-1)} title="Previous month">‹</button>
        <span>{month.toLocaleDateString(undefined, { month: 'long', year: 'numeric' })}</span>
        <span>
          <button className="link small" onClick={toToday}>today</button>
          <button className="link" onClick={() => step(1)} title="Next month">›</button>
        </span>
      </div>
      <div className="month-grid">
        {['M', 'T', 'W', 'T', 'F', 'S', 'S'].map((d, i) => <div key={i} className="dow dim small">{d}</div>)}
        {cells.map((d, i) => {
          if (!d) return <div key={i} />;
          const k = dayKey(d);
          const rs = byDay.get(k) ?? [];
          const up = rs.some((r) => r.upcoming);
          return (
            <button key={i} className={'mday' + (k === today ? ' today' : '') + (k === day ? ' on' : '') + (rs.length ? ' has' : '')} onClick={() => onDay(k)}
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

function MeetingPageView({ id, scope, onBack, onOpen, onTopic, onPrep, pending }: {
  id: string; scope: string; onBack?: () => void; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; onPrep: () => void;
  // pending: the list's row for a recording whose summary isn't written
  // yet; its page offers the transcript meanwhile.
  pending?: MeetingListItem;
}) {
  const [page, setPage] = useState<MeetingPage | null | undefined>(undefined);
  const [err, setErr] = useState('');
  useEffect(() => {
    setPage(undefined);
    setErr('');
    const load = () => api.meetingPage(id, scope).then((p) => setPage(p ?? null), (e) => { setErr(String(e)); setPage(null); });
    load();
    // The summary shows as soon as it is written.
    return on<string>('meeting:ready', (x) => { if (x === id) load(); });
  }, [id, scope]);
  const src = (chunkId: number) => onOpen({ itemId: id, chunkId } as unknown as Hit, scope);
  const todos = page?.todos ?? [];
  return (
    <div className="view meeting-page">
      <div className="page-nav">
        {onBack ? <button className="link" onClick={onBack}>← all meetings</button> : <span />}
        <button className="link" onClick={onPrep} title="What to bring to the next one with these people">prep for the next one →</button>
      </div>
      {err && <div className="error small">{err}</div>}
      {page === undefined && <div className="empty"><Spinner /></div>}
      {page === null && !err && pending && (
        <>
          <h2 className="meeting-h">{pending.title || pending.itemId} <span className="dim small">{fmtDate(pending.start)}</span></h2>
          {(pending.lines ?? 0) > 0 && <MeetingExport itemId={id} scope={scope} />}
          <Empty>{(pending.lines ?? 0) > 0 ? 'The summary is being written; it shows here as soon as it is ready. The transcript is ready now.' : 'The transcript is being processed.'}</Empty>
        </>
      )}
      {page === null && !err && !pending && <Empty>No summary for this meeting yet (it's written once the meeting has been processed), or none that is fine to show here.</Empty>}
      {page && (
        <>
          <h2 className="meeting-h">{page.title || page.itemId} <span className="dim small">{fmtDate(page.start)}</span></h2>
          <MeetingExport itemId={page.itemId} page={page} scope={scope} />
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
      <PhoneDetails itemId={id} scope={scope} />
      {scope === 'owner' && <MeetingRecording itemId={id} />}
    </div>
  );
}

// PhoneDetails is what the owner added to a phone recording: notes and
// people under the meeting's label, photos (owner-only) in owner scope.
function PhoneDetails({ itemId, scope }: { itemId: string; scope: string }) {
  const [d, setD] = useState<MeetingDetails | null>(null);
  const [open, setOpen] = useState<{ name: string; url: string } | null>(null);
  const [err, setErr] = useState('');
  useEffect(() => {
    setD(null); setOpen(null); setErr('');
    api.meetingDetails(itemId, scope).then((x) => setD(x ?? null), (e) => setErr(String(e)));
  }, [itemId, scope]);
  if (err) return <div className="error small">{err}</div>;
  if (!d) return null;
  const show = (name: string) => api.meetingPhoto(itemId, name, scope, false).then((url) => setOpen({ name, url }), (e) => setErr(String(e)));
  return (
    <div className="phone-details">
      <div className="section-head"><span>From your phone</span><span className="dim small"><LabelBadge label={d.label} compact /> share: {d.share}</span></div>
      {d.people.length > 0 && <div className="small"><b>People:</b> {d.people.map((p) => p.name).join(', ')}</div>}
      {d.notes && <div className="phone-notes small">{d.notes}</div>}
      {d.photos.length > 0 && (
        <>
          <div className="dim small">Photos ({d.photos.length}) · owner-only: kept on this Mac, never sent to a model</div>
          <div className="phone-photos">
            {d.photos.map((p) => <PhoneThumb key={p.name} itemId={itemId} scope={scope} name={p.name} onOpen={() => show(p.name)} />)}
          </div>
        </>
      )}
      {(d.photosHidden ?? 0) > 0 && <div className="dim small">{d.photosHidden} photos, shown in your own scope only.</div>}
      {open && (
        <div className="phone-photo-open">
          <img src={open.url} alt={open.name} />
          <button className="link small" onClick={() => setOpen(null)}>close</button>
        </div>
      )}
    </div>
  );
}

function PhoneThumb({ itemId, scope, name, onOpen }: { itemId: string; scope: string; name: string; onOpen: () => void }) {
  const [url, setUrl] = useState('');
  useEffect(() => {
    let live = true;
    api.meetingPhoto(itemId, name, scope, true).then((u) => { if (live) setUrl(u); }, () => {});
    return () => { live = false; };
  }, [itemId, name, scope]);
  return <button className="phone-thumb" onClick={onOpen} title={name}>{url ? <img src={url} alt={name} /> : <Spinner />}</button>;
}

// notesMarkdown is the meeting page as Markdown: what the page shows in
// its scope (the speakers only where the page lists them, owner scope),
// nothing more.
function notesMarkdown(page: MeetingPage, speakers: string[]): string {
  const one = (x: string) => x.replace(/\s+/g, ' ').trim();
  const at = new Date(page.start);
  const out: string[] = [`# ${one(page.title || page.itemId)}`];
  if (!isNaN(at.getTime()) && at.getFullYear() >= 2000) {
    out.push(at.toLocaleString(undefined, { weekday: 'short', year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }));
  }
  out.push('');
  if (speakers.length) out.push(`**Speakers:** ${speakers.join(', ')}`);
  if ((page.topics?.length ?? 0) > 0) out.push(`**Threads:** ${page.topics!.map((t) => one(t.label)).join(', ')}`);
  if (speakers.length || (page.topics?.length ?? 0) > 0) out.push('');
  if (page.overview) out.push(one(page.overview), '');
  const sections = page.sections ?? [];
  if (sections.length) {
    out.push('## Summary', '');
    for (const sec of sections) {
      out.push(`### ${one(sec.subject)}`, one(sec.text));
      if ((sec.people?.length ?? 0) > 0) out.push(`_${sec.people!.join(', ')}_`);
      out.push('');
    }
  }
  const todos = page.todos ?? [];
  out.push(`## To-dos (${todos.length})`, '');
  if (todos.length === 0) out.push('None listed.');
  for (const t of todos) {
    const bits = [(t.owners?.length ?? 0) > 0 ? t.owners!.join(', ') : 'owner unclear'];
    if (t.due) bits.push(`due ${t.due}`); else if (t.dueText) bits.push(one(t.dueText));
    bits.push(stateWords[t.state]);
    if (agreedWords[t.agreed] && t.state !== 'declined') bits.push(agreedWords[t.agreed]);
    out.push(`- [${t.state === 'done' ? 'x' : ' '}] ${one(t.text)} (${bits.join(' · ')})`);
  }
  return out.join('\n').trimEnd() + '\n';
}

// MeetingExport copies the page's notes and saves its transcript, each
// under the page's scope; with no page (summary not written yet), only
// the transcript.
function MeetingExport({ itemId, page, scope }: { itemId: string; page?: MeetingPage; scope: string }) {
  const [note, setNote] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!note) return;
    const t = setTimeout(() => setNote(''), 2500);
    return () => clearTimeout(t);
  }, [note]);
  const copy = async () => {
    if (!page) return;
    setErr(''); setNote('');
    try {
      // The Speakers section is on the page in owner scope only.
      const sp = scope === 'owner' ? await api.meetingSpeakers(page.itemId).catch(() => null) : null;
      await api.copyText(notesMarkdown(page, (sp?.speakers ?? []).map((x) => speakerName(x.label))));
      setNote('Copied');
    } catch (e) { setErr(String(e)); }
  };
  const save = () => {
    setErr(''); setNote(''); setBusy(true);
    api.saveTranscript(itemId, scope).then((path) => { setBusy(false); if (path) setNote('Saved'); }, (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <div className="meeting-export">
      {page && <button className="link small" onClick={copy} title={scope === 'owner' ? 'Copy this page as Markdown' : 'Copy this page as Markdown: what the shared screen shows'}>Copy notes</button>}
      <button className="link small" onClick={save} disabled={busy} title={scope === 'owner' ? 'Save the transcript as a text file' : 'Save the transcript as a text file: the passages fine for a shared screen'}>Download transcript</button>
      {note && <span className="ok-text small">{note}</span>}
      {err && <span className="error small">{err}</span>}
    </div>
  );
}

// MeetingRecording is the recording behind a meeting: its speakers, with
// the voice review for this meeting and renames; what the recorder saw of
// the meeting window; and delete. Owner scope only: names come straight
// from the recording. Edits need Musubi's recorder of record (and its
// recording of this meeting).
function MeetingRecording({ itemId }: { itemId: string }) {
  const [sp, setSp] = useState<MeetingSpeakers | null | undefined>(undefined);
  const { view, err } = useVoices();
  const a = useAct();
  const [renaming, setRenaming] = useState<string | null>(null);
  const [splitting, setSplitting] = useState<string | null>(null);
  const [split, setSplit] = useState<SplitResult | null>(null);
  const [looks, setLooks] = useState(false);
  const [deleting, setDeleting] = useState(false);
  useEffect(() => {
    const load = () => api.meetingSpeakers(itemId).then(setSp, () => setSp(null));
    load();
    return on('voices:changed', load);
  }, [itemId]);
  if (!sp) return null;
  const editable = sp.editable;
  const reviews = openReviews(view, sp.session);
  const rename = (label: string, name: string) => {
    setRenaming(null);
    a.act('rename/' + label, () => api.recorderSpeaker('rename_speaker', sp.session, label, name), `Named ${speakerName(label)} ${name}.`);
  };
  return (
    <>
      <div className="section-head"><span>Speakers ({sp.speakers.length})</span>{reviews > 0 && <span className="badge">{reviews} to check</span>}</div>
      {!editable && <div className="dim small">{sp.why || readOnlyWhy}</div>}
      <VoicesError err={err} />
      {view && <ReviewItems view={view} session={sp.session} editable={editable} a={a} showTitle={false} />}
      {sp.speakers.length > 0 && <div className="review-head">Everyone heard</div>}
      <ul className="speaker-list">
        {sp.speakers.map((s) => (
          <li key={s.label}>
            {renaming === s.label ? <SpeakerNameForm start={speakerName(s.label).startsWith('Person ') ? '' : speakerName(s.label)} onName={(n) => rename(s.label, n)} onCancel={() => setRenaming(null)} /> : (
              <>
                <span>{speakerName(s.label)}</span>
                <span className="dim small"> · {s.lines} {s.lines === 1 ? 'line' : 'lines'}, {Math.round(s.seconds / 60) || '<1'} min</span>
                {editable && s.label !== 'You' && <button className="link" disabled={a.busy !== null} title="Give this speaker another name in this meeting; their voice learns from it" onClick={() => setRenaming(s.label)}>rename</button>}
                {editable && s.label !== 'You' && (view?.profiles ?? []).length >= 2 && <button className="link" disabled={a.busy !== null} title="Two people under one name? Give each line to the voice it sounds like" onClick={() => { setSplit(null); setSplitting(s.label); }}>split by voice…</button>}
              </>
            )}
            {splitting === s.label && <SplitForm session={sp.session} label={s.label} profiles={view?.profiles ?? []} a={a}
              onDone={(r) => { setSplitting(null); setSplit(r); }} onCancel={() => setSplitting(null)} />}
          </li>
        ))}
      </ul>
      {split?.batch && (
        <div className="confirm-row">
          <span className="small">Split by voice: {(split.counts ?? []).filter((c) => c.lines > 0).map((c) => `${c.lines} to ${c.label}`).join(', ')}{split.undecided > 0 && `, ${split.undecided} left as they were`}.</span>
          <button disabled={a.busy !== null} title="Put those lines back on the speaker they had; Musubi reads the meeting again" onClick={() => { const b = split.batch!; setSplit(null); a.act('split', () => api.undoSplit(b), 'Put the lines back.'); }}>Undo</button>
          <button title="Keep the split" onClick={() => setSplit(null)}>OK</button>
        </div>
      )}
      <ActNotes a={a} />
      <div className="section-head"><span>What Musubi saw</span><button className="link" onClick={() => setLooks(!looks)}>{looks ? 'hide' : 'the meeting window, look by look'}</button></div>
      {looks && <LookTimeline session={sp.session} />}
      {editable && (deleting ? (
        <div className="confirm-row">
          <span className="small">Delete this meeting? The recording and its audio are deleted, and what Musubi learnt from it leaves its subjects. This can&apos;t be undone.</span>
          <button className="primary danger" disabled={a.busy !== null} onClick={() => { setDeleting(false); a.act('delete', () => api.deleteMeeting(itemId)); }}>Delete</button>
          <button onClick={() => setDeleting(false)}>Keep</button>
        </div>
      ) : <button className="link danger-link" onClick={() => setDeleting(true)}>Delete meeting…</button>)}
    </>
  );
}

// SplitForm picks the voices a speaker's lines may belong to (two or
// more), shows how the recorder would share them out, and splits on
// confirm. Profiles heard in this meeting, or named like the speaker,
// come first.
function SplitForm({ session, label, profiles, a, onDone, onCancel }: {
  session: string; label: string; profiles: VoiceProfile[]; a: ReturnType<typeof useAct>; onDone: (r: SplitResult) => void; onCancel: () => void;
}) {
  const [picked, setPicked] = useState<string[]>([]);
  const [q, setQ] = useState('');
  const [pre, setPre] = useState<SplitResult | null>(null);
  const [err, setErr] = useState('');
  const fold = (x: string) => x.trim().toLowerCase();
  const near = (p: VoiceProfile) => (p.heard ?? []).some((h) => h.session === session) || fold(p.name) === fold(speakerName(label));
  const key = (p: VoiceProfile) => p.model + '/' + p.id;
  const words = fold(q).split(/\s+/).filter(Boolean);
  const list = profiles
    .filter((p) => picked.includes(key(p)) || near(p) || (words.length > 0 && words.every((w) => fold(p.name).includes(w))))
    .sort((x, y) => Number(near(y)) - Number(near(x)) || x.name.localeCompare(y.name))
    .slice(0, 12);
  const into = () => picked.map((k) => profiles.find((p) => key(p) === k)).filter((p): p is VoiceProfile => !!p).map((p) => ({ model: p.model, id: p.id }));
  const toggle = (k: string) => { setPre(null); setPicked(picked.includes(k) ? picked.filter((x) => x !== k) : [...picked, k]); };
  const preview = () => { setErr(''); api.splitSpeakerByVoice(session, label, into(), true).then(setPre, (e) => setErr(String(e))); };
  const go = () => a.act('split', async () => { const r = await api.splitSpeakerByVoice(session, label, into(), false); onDone(r); return r; },
    (r) => (r.batch ? '' : 'No line changed speaker.'));
  const shares = pre ? (pre.counts ?? []).map((c) => `${c.lines} ${c.lines === 1 ? 'line' : 'lines'} to ${c.label}`).join(', ') : '';
  return (
    <div className="split-form">
      <div className="dim small">Which voices are under {speakerName(label)}? Pick two or more.</div>
      <input className="search" placeholder="Find a voice by name" value={q} onChange={(e) => setQ(e.target.value)} />
      <div className="split-picks">
        {list.map((p) => (
          <label key={key(p)} className="small"><input type="checkbox" checked={picked.includes(key(p))} onChange={() => toggle(key(p))} /> {p.name} <span className="dim">({p.meetings} {p.meetings === 1 ? 'meeting' : 'meetings'})</span></label>
        ))}
        {list.length === 0 && <span className="dim small">No voice heard here: find one by name.</span>}
      </div>
      {pre ? (
        <div className="confirm-row">
          <span className="small">Split {speakerName(label)}: {shares}{pre.undecided > 0 && `; ${pre.undecided} left as they are (no voice to go by, or too close to call)`}. Who said what changes, so Musubi reads this meeting again (re-extracts it) at the next update.</span>
          <button className="primary" disabled={a.busy !== null} title="Give each line to the voice it sounds like; lines too close to call keep the speaker. You can undo it." onClick={go}>Split</button>
          <button title="Change nothing" onClick={onCancel}>Cancel</button>
        </div>
      ) : (
        <div className="voice-actions">
          <button className="primary" disabled={picked.length < 2} title="See how the lines would be shared out; nothing changes yet" onClick={preview}>Split by voice…</button>
          <button title="Change nothing" onClick={onCancel}>Cancel</button>
        </div>
      )}
      {err && <div className="error small">{err}</div>}
    </div>
  );
}

function SpeakerNameForm({ start, onName, onCancel }: { start: string; onName: (n: string) => void; onCancel: () => void }) {
  const [n, setN] = useState(start);
  return (
    <form className="voice-name" onSubmit={(e) => { e.preventDefault(); if (n.trim()) onName(n.trim()); }}>
      <input value={n} autoFocus placeholder="Who is it?" onChange={(e) => setN(e.target.value)} />
      <button className="primary" type="submit" disabled={!n.trim()}>Save</button>
      <button type="button" onClick={onCancel}>Cancel</button>
    </form>
  );
}
