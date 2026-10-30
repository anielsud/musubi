import { useEffect, useMemo, useState } from 'react';
import { api, fmtDate } from '../api';
import type { Hit, OwnerLink, Statement, ToDo } from '../types';
import { Empty, Spelled, Spinner } from './common';

// ToDoView: everything someone said would happen (commitments) or, asked
// for, should happen (proposals), with what came of it: done or changed by
// something said later, raised again, gone quiet, overdue. Grouped by the
// thread each is on, or by state. The owner can say what happened (done,
// dropped), how two relate (same, replaces, a step of), or fix the reading
// (not a to-do, the due date, the text or who); each is recorded to the
// side and can be undone.
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
  same: 'same as', replaces: 'replaces', part: 'a step of', done: 'done', dropped: 'dropped', not: 'not a to-do', due: 'due',
};

// Linking picks the to-do that the one being linked relates to.
type Linking = { from: ToDo; rel: 'same' | 'replaces' | 'part' };

export function ToDoView({ scope, onOpen, onTopic }: { scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void }) {
  const [all, setAll] = useState<ToDo[] | null | undefined>(undefined);
  const [err, setErr] = useState('');
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
      act={act} onOpen={onOpen} onTopic={onTopic} showThread={!byThread} />
  );
  const more = (key: string, n: number, total: number) => total > n &&
    <button className="link small" onClick={() => setShown({ ...shown, [key]: n + pageSize })}>show {Math.min(pageSize, total - n)} more</button>;

  return (
    <div className="view todo">
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
    </div>
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

function ToDoRow({ t, scope, owner, steps, linking, setLinking, act, onOpen, onTopic, showThread }: {
  t: ToDo; scope: string; owner: boolean; steps: ToDo[]; linking: Linking | null; setLinking: (l: Linking | null) => void;
  act: (p: Promise<unknown>) => void; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void; showThread: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [edit, setEdit] = useState<'' | 'done' | 'dropped' | 'due' | 'correct'>('');
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
        {(t.owners ?? []).join(', ')}
        {t.due && <span className={'badge' + (t.overdue ? ' prep-overdue' : ' todo-due')}>{t.due}</span>}
        {' · '}<button className="link" onClick={() => src(t)}>{fmtDate(t.saidAt)}, {t.itemTitle || 'source'}</button>
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
              <button className="chip" onClick={() => setEdit('correct')} title="Correct the text or who said it">correct</button>
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
          {steps.map((s) => <ToDoRow key={s.id} t={s} scope={scope} owner={owner} steps={[]} linking={linking} setLinking={setLinking} act={act} onOpen={onOpen} onTopic={onTopic} showThread={false} />)}
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
        <>{markWords[m.rel]}{m.note ? ': ' + m.note : m.rel === 'due' ? ': no date' : ''}</>}
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
