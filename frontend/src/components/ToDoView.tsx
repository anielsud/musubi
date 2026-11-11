import { useEffect, useMemo, useState } from 'react';
import { api, fmtDate } from '../api';
import type { Hit, OwnerLink, Statement, ToDo } from '../types';
import { Empty, Spelled, Spinner } from './common';
import { Names } from './people';

// ToDoView: everything someone said would happen (commitments) or, asked
// for, should happen (proposals), with what came of it: done or changed by
// something said later, raised again, gone quiet, overdue. Grouped by the
// thread each is on, or by state. The owner can say what happened (done,
// dropped), how two relate (same, replaces, a step of), or fix the reading
// (not a to-do, the due date, the text or who); each is recorded to the
// side and can be undone.
//
// It opens on what you owe: to-dos you agreed to that are still open,
// grouped by who asked. Waiting on is the same for what others owe;
// Everything is the full list. New to-dos of yours since you last looked
// can be gone through one at a time.
const sections: { key: string; title: string; note?: string; test: (t: ToDo) => boolean }[] = [
  { key: 'overdue', title: 'Overdue', test: (t) => t.state === 'open' && !!t.overdue },
  { key: 'open', title: 'Open', test: (t) => t.state === 'open' && !t.overdue },
  { key: 'proposed', title: 'Proposed', note: 'said should happen, not agreed', test: (t) => t.state === 'proposed' },
  { key: 'quiet', title: 'Gone quiet', note: 'nothing said of these since; check before chasing', test: (t) => t.state === 'quiet' },
  { key: 'closed', title: 'Done or changed', test: (t) => t.state === 'done' || t.state === 'changed' },
];
const pageSize = 50;
const closed = (t: ToDo) => t.state === 'done' || t.state === 'changed';

const linkWords: Record<string, string> = { same: 'the same as', replaces: 'replacing', part: 'a step of' };
const markWords: Record<string, string> = {
  same: 'same as', replaces: 'replaces', part: 'a step of', done: 'done', dropped: 'dropped', not: 'not a to-do', due: 'due', who: 'whose',
};

// agreed: a to-do from statements, or one a summary read as agreed (not
// only asked, suggested or turned down).
const agreed = (t: ToDo) => !t.agreed || t.agreed === 'agreed';
const openAgreed = (t: ToDo) => t.state === 'open' && agreed(t);

// What has been gone through in review: a watermark (said before it, done)
// and the ids looked at since. Kept in this browser only; without it,
// everything of the last two weeks counts as new.
const reviewKey = 'musubi.todo.review';
type Review = { upTo: string; seen: number[] };
function loadReview(): Review {
  try {
    const r = JSON.parse(localStorage.getItem(reviewKey) ?? '');
    if (r && typeof r.upTo === 'string' && Array.isArray(r.seen)) return r;
  } catch { /* none kept */ }
  return { upTo: new Date(Date.now() - 14 * 864e5).toISOString(), seen: [] };
}
function saveReview(r: Review) {
  try { localStorage.setItem(reviewKey, JSON.stringify(r)); } catch { /* not kept */ }
}

// Linking picks the to-do that the one being linked relates to.
type Linking = { from: ToDo; rel: 'same' | 'replaces' | 'part' };

export function ToDoView({ scope, onOpen, onTopic, onMeeting }: { scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; onMeeting?: (itemId: string) => void }) {
  const [all, setAll] = useState<ToDo[] | null | undefined>(undefined);
  const [err, setErr] = useState('');
  const [mode, setMode] = useState<'owe' | 'waiting' | 'all'>('owe');
  const [review, setReview] = useState<Review>(loadReview);
  const [queue, setQueue] = useState<ToDo[] | null>(null);
  const [mine, setMine] = useState(true);
  const [proposals, setProposals] = useState(false);
  const [byThread, setByThread] = useState(true);
  const [q, setQ] = useState('');
  const [shown, setShown] = useState<Record<string, number>>({});
  const [linking, setLinking] = useState<Linking | null>(null);
  const [reload, setReload] = useState(0);
  const owner = scope === 'owner';

  useEffect(() => {
    if (reload === 0) setAll(undefined);
    setErr('');
    const t = setTimeout(() => api.toDos(proposals, scope).then((x) => setAll(x ?? []), (e) => { setErr(String(e)); setAll(null); }), reload ? 0 : 150);
    return () => clearTimeout(t);
  }, [proposals, scope, reload]);
  const changed = () => setReload((n) => n + 1);
  const act = (p: Promise<unknown>) => p.then(changed, (e) => setErr(String(e)));

  const fresh = useMemo(() => (all ?? []).filter((t) => t.mine && (t.state === 'open' || t.state === 'proposed') &&
    t.saidAt > review.upTo && !review.seen.includes(t.id)).sort((a, b) => a.saidAt.localeCompare(b.saidAt)), [all, review]);
  const endReview = (r: Review) => {
    // All gone through: the watermark moves to the newest looked at.
    const left = (all ?? []).some((t) => t.mine && (t.state === 'open' || t.state === 'proposed') && t.saidAt > r.upTo && !r.seen.includes(t.id));
    const next = left ? r : { upTo: (all ?? []).reduce((m, t) => (r.seen.includes(t.id) && t.saidAt > m ? t.saidAt : m), r.upTo), seen: [] };
    saveReview(next);
    setReview(next);
    setQueue(null);
  };
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const matches = (t: ToDo) => words.every((w) => (t.text + ' ' + (t.owners ?? []).join(' ') + ' ' + (t.requester ?? '') + ' ' + (t.itemTitle ?? '') + ' ' + (t.topicLabel ?? '')).toLowerCase().includes(w));

  const list = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    return (all ?? []).filter((t) => (!mine || t.mine) &&
      words.every((w) => (t.text + ' ' + (t.owners ?? []).join(' ') + ' ' + (t.itemTitle ?? '') + ' ' + (t.topicLabel ?? '')).toLowerCase().includes(w)));
  }, [all, mine, q]);
  // Steps sit under their parent when it is listed.
  const steps = useMemo(() => {
    const ids = new Set(list.map((t) => t.id));
    const m = new Map<number, ToDo[]>();
    for (const t of list) if (t.parent && ids.has(t.parent)) m.set(t.parent, [...(m.get(t.parent) ?? []), t]);
    return m;
  }, [list]);
  const top = list.filter((t) => t.state !== 'not' && !(t.parent && steps.get(t.parent)?.includes(t)));
  const aside = list.filter((t) => t.state === 'not');

  const row = (t: ToDo) => (
    <ToDoRow key={t.id} t={t} scope={scope} owner={owner} steps={steps.get(t.id) ?? []} linking={linking} setLinking={setLinking}
      act={act} onOpen={onOpen} onTopic={onTopic} showThread={!byThread} onMeeting={onMeeting} />
  );
  const more = (key: string, n: number, total: number) => total > n &&
    <button className="link small" onClick={() => setShown({ ...shown, [key]: n + pageSize })}>show {Math.min(pageSize, total - n)} more</button>;

  if (queue) {
    return (
      <div className="view todo">
        <Triage queue={queue} scope={scope} act={(p) => p.then(changed, (e) => setErr(String(e)))} onOpen={onOpen}
          onDone={(seen) => endReview({ ...review, seen: [...review.seen, ...seen] })} />
        {err && <div className="error small">{err}</div>}
      </div>
    );
  }

  const owe = mode === 'owe' ? (all ?? []).filter((t) => t.mine && openAgreed(t) && matches(t)) : [];
  const waiting = mode === 'waiting' ? (all ?? []).filter((t) => !t.mine && openAgreed(t) && matches(t)) : [];
  const asked = mode === 'owe' ? (all ?? []).filter((t) => t.mine && t.state === 'open' && !agreed(t)).length : 0;

  return (
    <div className="view todo">
      <div className="chips todo-modes">
        <button className={'chip' + (mode === 'owe' ? ' on' : '')} onClick={() => setMode('owe')} title="What you agreed to that is still open">You owe</button>
        <button className={'chip' + (mode === 'waiting' ? ' on' : '')} onClick={() => setMode('waiting')} title="What others agreed to that is still open">Waiting on</button>
        <button className={'chip' + (mode === 'all' ? ' on' : '')} onClick={() => setMode('all')} title="Every to-do, with what came of it">Everything</button>
        {fresh.length > 0 && owner && (
          <button className="chip todo-new" onClick={() => setQueue(fresh)} title="Go through them one at a time: keep, done, not a to-do, not yours">
            {fresh.length} new since you last looked · review
          </button>
        )}
      </div>
      {mode !== 'all' && (
        <>
          <input className="todo-q" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter: words, a name, a meeting, a thread" />
          {err && <div className="error small">{err}</div>}
          {all === undefined && <Spinner />}
          {all && (
            <ByPerson items={mode === 'owe' ? owe : waiting} row={row} key={mode}
              who={mode === 'owe' ? (t) => (t.requester && !(t.owners ?? []).some((o) => o.toLowerCase() === t.requester!.toLowerCase()) ? [t.requester] : [])
                : (t) => (t.owners ?? []).slice(0, 1)}
              head={mode === 'owe' ? (n) => <>for {n}</> : (n) => <>{n}</>}
              none={mode === 'owe' ? 'No one else named' : 'Owner unclear'}
              empty={mode === 'owe' ? 'Nothing open that you agreed to.' : 'Nothing open that others agreed to.'} />
          )}
          {mode === 'owe' && asked > 0 && (
            <div className="dim small">Not shown: {asked} you were asked to do but didn't take on. They're under Everything.</div>
          )}
        </>
      )}
      {mode === 'all' && <>
      <div className="todo-filters">
        <div className="chips">
          <button className={'chip' + (mine ? ' on' : '')} onClick={() => setMine(true)}>Mine</button>
          <button className={'chip' + (!mine ? ' on' : '')} onClick={() => setMine(false)}>Everyone's</button>
          <button className={'chip' + (proposals ? ' on' : '')} title="Also what someone said should happen, not yet agreed" onClick={() => setProposals(!proposals)}>+ proposals</button>
          <span className="todo-sep" />
          <button className={'chip' + (byThread ? ' on' : '')} onClick={() => setByThread(true)}>By thread</button>
          <button className={'chip' + (!byThread ? ' on' : '')} onClick={() => setByThread(false)}>By state</button>
        </div>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter: words, a name, a meeting, a thread" />
      </div>
      {linking && (
        <div className="todo-linking small">
          Pick the to-do that “{linking.from.text}” is {linkWords[linking.rel]}.
          <button className="link" onClick={() => setLinking(null)}>cancel</button>
        </div>
      )}
      {err && <div className="error small">{err}</div>}
      {all === undefined && <Spinner />}
      {all && list.length === 0 && <Empty>{mine ? "Nothing you said you would do. Try “Everyone's”." : 'Nothing found.'}</Empty>}
      {all && byThread && <ByThread items={top} row={row} shown={shown} more={more} onTopic={onTopic} />}
      {all && !byThread && sections.map((sec) => {
        const items = top.filter(sec.test);
        if (items.length === 0) return null;
        const n = shown[sec.key] ?? (sec.key === 'closed' || sec.key === 'quiet' ? 10 : pageSize);
        return (
          <section key={sec.key}>
            <div className="section-head"><span>{sec.title} <span className="dim">{items.length}</span></span>{sec.note && <span className="dim">{sec.note}</span>}</div>
            <ul className="todo-list">{items.slice(0, n).map(row)}</ul>
            {more(sec.key, n, items.length)}
          </section>
        );
      })}
      {all && aside.length > 0 && <SetAside items={aside} row={row} />}
      </>}
    </div>
  );
}

// ByPerson groups to-dos by a person each names (who asked, for what you
// owe; whose it is, for what you wait on): the group with something
// overdue first, then the biggest. Rows come sorted (overdue, then due
// soonest, then latest raised).
function ByPerson({ items, row, who, head, none, empty }: {
  items: ToDo[]; row: (t: ToDo) => JSX.Element; who: (t: ToDo) => string[];
  head: (name: JSX.Element) => JSX.Element; none: string; empty: string;
}) {
  const [n, setN] = useState(30);
  const groups = useMemo(() => {
    const m = new Map<string, { name: string; ts: ToDo[] }>();
    for (const t of items) {
      const name = who(t)[0] ?? '';
      const k = name.toLowerCase();
      if (!m.has(k)) m.set(k, { name, ts: [] });
      m.get(k)!.ts.push(t);
    }
    const score = (g: { name: string; ts: ToDo[] }) => (g.name ? 0 : 1e6) - (g.ts.some((t) => t.overdue) ? 1e5 : 0) - g.ts.length;
    return [...m.values()].sort((a, b) => score(a) - score(b));
  }, [items, who]);
  if (items.length === 0) return <Empty>{empty}</Empty>;
  return (
    <>
      {groups.slice(0, n).map((g) => (
        <section key={g.name || '-'} className="todo-group">
          <div className="todo-group-head">
            {g.name ? head(<Names names={[g.name]} />) : <span>{none}</span>}
            <span className="dim small"> {g.ts.length}{g.ts.some((t) => t.overdue) ? `, ${g.ts.filter((t) => t.overdue).length} overdue` : ''}</span>
          </div>
          <ul className="todo-list">{g.ts.map(row)}</ul>
        </section>
      ))}
      {groups.length > n && <button className="link small" onClick={() => setN(n + 30)}>{groups.length - n} more people</button>}
    </>
  );
}

// Triage goes through new to-dos one at a time: keep, done, not a to-do,
// or not yours (and whose, if known). Keys: k, d, n, m.
function Triage({ queue, scope, act, onOpen, onDone }: {
  queue: ToDo[]; scope: string; act: (p: Promise<unknown>) => void; onOpen: (h: Hit, scope: string) => void; onDone: (seen: number[]) => void;
}) {
  const [i, setI] = useState(0);
  const [seen, setSeen] = useState<number[]>([]);
  const [whose, setWhose] = useState<string | null>(null);
  const t = queue[i];
  const next = () => {
    const s = [...seen, t.id];
    setSeen(s);
    setWhose(null);
    if (i + 1 >= queue.length) onDone(s);
    else setI(i + 1);
  };
  const mark = (rel: string, note = '') => { act(api.linkToDo(t.id, rel, 0, note)); next(); };
  useEffect(() => {
    const f = (e: KeyboardEvent) => {
      if (whose !== null || e.metaKey || e.ctrlKey || (e.target as HTMLElement).tagName === 'INPUT') return;
      if (e.key === 'k') next();
      else if (e.key === 'd') mark('done');
      else if (e.key === 'n') mark('not');
      else if (e.key === 'm') setWhose('');
    };
    window.addEventListener('keydown', f);
    return () => window.removeEventListener('keydown', f);
  });
  if (!t) return null;
  return (
    <section className="triage">
      <div className="section-head">
        <span>New since you last looked · {i + 1} of {queue.length}</span>
        <button className="link" onClick={() => onDone(seen)}>finish later</button>
      </div>
      <article className="triage-card">
        <div className="triage-text"><Spelled s={t} /></div>
        <div className="dim small">
          {(t.owners?.length ?? 0) > 0 ? <Names names={t.owners!} /> : 'owner unclear'}
          {t.requester && <> · asked by <Names names={[t.requester]} /></>}
          {t.due && <span className={'badge' + (t.overdue ? ' prep-overdue' : ' todo-due')}>{t.due}</span>}
          {t.state === 'proposed' && <> · suggested, not agreed</>}
          {t.agreed === 'asked' && <> · asked, not accepted</>}
          {' · '}<button className="link" onClick={() => onOpen({ itemId: t.itemId, chunkId: t.chunkId } as unknown as Hit, scope)}>{fmtDate(t.saidAt)}, {t.itemTitle || 'source'}</button>
        </div>
        {t.quote && <div className="card-quote">“{t.quote}”</div>}
      </article>
      {whose === null ? (
        <div className="triage-actions">
          <button className="chip" onClick={next}><b>k</b>eep</button>
          <button className="chip" onClick={() => mark('done')}><b>d</b>one</button>
          <button className="chip" onClick={() => mark('not')}><b>n</b>ot a to-do</button>
          <button className="chip" onClick={() => setWhose('')}>not <b>m</b>ine</button>
        </div>
      ) : (
        <NoteForm label="save" placeholder="whose is it? names, comma-separated (blank: not sure)" onSave={(n) => mark('who', n)} onCancel={() => setWhose(null)} />
      )}
      <div className="dim small">Your answers are recorded on each to-do and can be undone there.</div>
    </section>
  );
}

// ByThread groups the to-dos by the thread each is on, the most pressing
// thread first (the list comes sorted: overdue, open, proposed, quiet,
// closed); a thread with one to-do is a plain row. Threads with nothing
// left open follow, folded.
function ByThread({ items, row, shown, more, onTopic }: {
  items: ToDo[]; row: (t: ToDo) => JSX.Element; shown: Record<string, number>;
  more: (key: string, n: number, total: number) => JSX.Element | false; onTopic: (id: number) => void;
}) {
  const groups = useMemo(() => {
    const m = new Map<string, ToDo[]>();
    for (const t of items) {
      const k = t.topic ? 't' + t.topic : 's' + t.id;
      m.set(k, [...(m.get(k) ?? []), t]);
    }
    return [...m.entries()];
  }, [items]);
  const live = groups.filter(([, ts]) => ts.some((t) => !closed(t)));
  const done = groups.filter(([, ts]) => ts.every(closed));
  const n = shown.threads ?? 30;
  const [openDone, setOpenDone] = useState(false);
  const group = ([k, ts]: [string, ToDo[]]) => ts.length === 1 ? <ul key={k} className="todo-list">{row(ts[0])}</ul> :
    <ThreadGroup key={k} ts={ts} row={row} onTopic={onTopic} />;
  return (
    <>
      {live.slice(0, n).map(group)}
      {more('threads', n, live.length)}
      {done.length > 0 && (
        <section>
          <button className="section-head link" onClick={() => setOpenDone(!openDone)}>
            <span>{openDone ? '▾' : '▸'} Done or changed <span className="dim">{done.reduce((a, [, ts]) => a + ts.length, 0)}</span></span>
          </button>
          {openDone && done.map(group)}
        </section>
      )}
    </>
  );
}

function ThreadGroup({ ts, row, onTopic }: { ts: ToDo[]; row: (t: ToDo) => JSX.Element; onTopic: (id: number) => void }) {
  const [showClosed, setShowClosed] = useState(false);
  const active = ts.filter((t) => !closed(t));
  const shut = ts.filter(closed);
  const overdue = active.filter((t) => t.state === 'open' && t.overdue).length;
  const t0 = ts[0];
  return (
    <section className="todo-group">
      <div className="todo-group-head">
        <button className="link" onClick={() => onTopic(t0.topic!)} title="Open the thread">{t0.topicLabel || 'A thread'}</button>
        <span className="dim small">
          {' '}{active.length} open{overdue ? `, ${overdue} overdue` : ''}{shut.length ? `, ${shut.length} done or changed` : ''}
        </span>
      </div>
      <ul className="todo-list">
        {(active.length ? active : shut).map(row)}
        {active.length > 0 && shut.length > 0 && (showClosed ? shut.map(row) :
          <li><button className="link small" onClick={() => setShowClosed(true)}>+ {shut.length} done or changed</button></li>)}
      </ul>
    </section>
  );
}

function SetAside({ items, row }: { items: ToDo[]; row: (t: ToDo) => JSX.Element }) {
  const [open, setOpen] = useState(false);
  return (
    <section>
      <button className="section-head link" onClick={() => setOpen(!open)}>
        <span>{open ? '▾' : '▸'} Set aside <span className="dim">{items.length}</span></span><span className="dim">you said these aren't to-dos</span>
      </button>
      {open && <ul className="todo-list">{items.map(row)}</ul>}
    </section>
  );
}

function ToDoRow({ t, scope, owner, steps, linking, setLinking, act, onOpen, onTopic, onMeeting, showThread }: {
  t: ToDo; scope: string; owner: boolean; steps: ToDo[]; linking: Linking | null; setLinking: (l: Linking | null) => void;
  act: (p: Promise<unknown>) => void; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; onMeeting?: (itemId: string) => void; showThread: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [edit, setEdit] = useState<'' | 'done' | 'dropped' | 'due' | 'correct' | 'who'>('');
  const src = (s: Statement) => onOpen({ itemId: s.itemId, chunkId: s.chunkId } as unknown as Hit, scope);
  const picking = linking && linking.from.id !== t.id;
  const pick = () => { act(api.linkToDo(linking!.from.id, linking!.rel, t.id)); setLinking(null); };
  const mark = (rel: string, note = '') => { act(api.linkToDo(t.id, rel, 0, note)); setEdit(''); };
  return (
    <li className={'todo-' + t.state + (linking?.from.id === t.id ? ' todo-from' : '')}>
      <div className="todo-text" onClick={() => setOpen(!open)}>
        {picking && <button className="chip todo-pick" onClick={(e) => { e.stopPropagation(); pick(); }}>pick</button>}
        {closed(t) && <span className="todo-mark">{t.state === 'done' ? '✓' : '↻'}</span>}
        <Spelled s={t} />
      </div>
      <div className="dim small todo-meta">
        <Names names={t.owners ?? []} />
        {t.due && <span className={'badge' + (t.overdue ? ' prep-overdue' : ' todo-due')}>{t.due}</span>}
        {' · '}<button className="link" onClick={() => src(t)}>{fmtDate(t.saidAt)}, {t.itemTitle || 'source'}</button>
        {!!t.digest && onMeeting && <> · <button className="link" onClick={() => onMeeting(t.itemId)} title="The meeting's summary and all its to-dos">meeting</button></>}
        {t.raised > 1 && <span> · raised {t.raised}×, last {fmtDate(t.lastRaised)}</span>}
        {!!t.steps && <span> · {t.stepsDone ?? 0} of {t.steps} steps done</span>}
        {!!t.topic && showThread && <> · <button className="link" onClick={() => onTopic(t.topic!)}>{t.topicLabel || 'thread'}</button></>}
      </div>
      {t.outcome && (
        <div className="small todo-outcome">
          <span className="dim">{t.state === 'done' ? 'Done: ' : 'Changed: '}</span>
          {t.outcome.owner ? (
            <><span>{t.outcome.text || (t.state === 'done' ? 'you marked it done' : 'you dropped it')}</span><span className="dim"> — you, {fmtDate(t.outcome.saidAt)}</span></>
          ) : (
            <><Spelled s={t.outcome} /><span className="dim"> — </span><button className="link" onClick={() => src(t.outcome!)}>{fmtDate(t.outcome.saidAt)}, {t.outcome.itemTitle || 'source'}</button></>
          )}
        </div>
      )}
      {open && (
        <div className="todo-open small">
          {owner && edit === '' && (
            <div className="todo-actions">
              {!closed(t) && <button className="chip" onClick={() => setEdit('done')} title="Say it happened">done</button>}
              {!closed(t) && <button className="chip" onClick={() => setEdit('dropped')} title="Say it won't happen">dropped</button>}
              {(['same', 'replaces', 'part'] as const).map((rel) => (
                <button key={rel} className="chip" onClick={() => setLinking({ from: t, rel })}
                  title={'Then pick the to-do it is ' + linkWords[rel]}>{({ same: 'same as…', replaces: 'replaces…', part: 'step of…' })[rel]}</button>
              ))}
              <button className="chip" onClick={() => setEdit('due')}>due…</button>
              {t.state !== 'not' && <button className="chip" onClick={() => mark('not')} title="Musubi misread this: set it aside">not a to-do</button>}
              {t.id > 0 ? <button className="chip" onClick={() => setEdit('correct')} title="Correct the text or who said it">correct</button>
                : <button className="chip" onClick={() => setEdit('who')} title="Say whose it is">whose…</button>}
            </div>
          )}
          {(edit === 'done' || edit === 'dropped') && (
            <NoteForm placeholder={edit === 'done' ? 'what happened, where (optional)' : "why it won't happen (optional)"}
              label={edit} onSave={(n) => mark(edit, n)} onCancel={() => setEdit('')} />
          )}
          {edit === 'due' && (
            <NoteForm type="date" initial={t.dueAt && !t.dueAt.startsWith('0001') ? t.dueAt.slice(0, 10) : ''} label="set date"
              placeholder="" onSave={(n) => mark('due', n)} onCancel={() => setEdit('')} hint="Empty: no date." />
          )}
          {edit === 'correct' && <CorrectForm t={t} act={act} onDone={() => setEdit('')} />}
          {edit === 'who' && <NoteForm label="save" initial={(t.owners ?? []).join(', ')} placeholder="names, comma-separated (blank: not yours, owner unclear)"
            onSave={(n) => mark('who', n)} onCancel={() => setEdit('')} />}
          {(t.marks ?? []).length > 0 && (
            <ul className="todo-marks">
              {t.marks!.map((m) => <MarkRow key={m.id} m={m} owner={owner} act={act} />)}
            </ul>
          )}
          {(t.repeats?.length ?? 0) > 0 && (
            <ul className="status-list">
              {t.repeats!.map((r) => (
                <li key={r.id}><button className="link" onClick={() => src(r)}>{fmtDate(r.saidAt)}, {r.itemTitle || 'source'}</button><span className="dim">: </span><Spelled s={r} /></li>
              ))}
            </ul>
          )}
        </div>
      )}
      {steps.length > 0 && (
        <ul className="todo-list todo-steps">
          {steps.map((s) => <ToDoRow key={s.id} t={s} scope={scope} owner={owner} steps={[]} linking={linking} setLinking={setLinking} act={act} onOpen={onOpen} onTopic={onTopic} onMeeting={onMeeting} showThread={false} />)}
        </ul>
      )}
    </li>
  );
}

function MarkRow({ m, owner, act }: { m: OwnerLink; owner: boolean; act: (p: Promise<unknown>) => void }) {
  const short = (s: string) => (s.length > 60 ? s.slice(0, 57) + '…' : s);
  return (
    <li className="dim">
      You, {fmtDate(m.at)}: {m.other.item ? <>“{short(m.statement.text)}” is {markWords[m.rel]} “{short(m.other.text)}”</> :
        <>{m.rel === 'who' && !m.note ? 'not yours' : markWords[m.rel]}{m.note ? ': ' + m.note : m.rel === 'due' ? ': no date' : ''}</>}
      {owner && <> · <button className="link" onClick={() => act(api.undoToDoLink(m.id))}>undo</button></>}
    </li>
  );
}

function NoteForm({ label, placeholder, initial = '', type = 'text', hint, onSave, onCancel }: {
  label: string; placeholder: string; initial?: string; type?: string; hint?: string; onSave: (note: string) => void; onCancel: () => void;
}) {
  const [v, setV] = useState(initial);
  return (
    <div className="todo-form">
      <input type={type} value={v} placeholder={placeholder} autoFocus onChange={(e) => setV(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && onSave(v)} />
      <button className="chip" onClick={() => onSave(v)}>{label}</button>
      <button className="link" onClick={onCancel}>cancel</button>
      {hint && <span className="dim">{hint}</span>}
    </div>
  );
}

// CorrectForm fixes the reading of what was said (as the thread view's
// correct does): the original is kept and the graph mended around it.
function CorrectForm({ t, act, onDone }: { t: ToDo; act: (p: Promise<unknown>) => void; onDone: () => void }) {
  const written = t.original || t.text;
  const who = (t.who ?? []).join(', ') || t.holder || '';
  const [text, setText] = useState(written);
  const [names, setNames] = useState(who);
  const [busy, setBusy] = useState(false);
  const save = () => {
    setBusy(true);
    act(api.correctStatement(t.id, text === written ? '' : text, names === who ? '' : names).finally(onDone));
  };
  return (
    <div className="stmt editing">
      <input value={names} onChange={(e) => setNames(e.target.value)} placeholder="whose it is (names, comma-separated)" />
      <textarea value={text} rows={2} onChange={(e) => setText(e.target.value)} />
      <div className="edit-actions">
        {busy ? <Spinner /> : <button className="chip" onClick={save} disabled={text === written && names === who}>save</button>}
        <button className="link" onClick={onDone}>cancel</button>
        <span className="dim small">The original is kept; the change is recorded.</span>
      </div>
    </div>
  );
}
