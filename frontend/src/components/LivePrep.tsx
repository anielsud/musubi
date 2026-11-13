import { useEffect, useRef, useState } from 'react';
import { api, fmtDate, on } from '../api';
import type { Hit, LivePrep, PrepView, ThreadItem } from '../types';
import { HitRow, Spinner } from './common';
import { Names } from './people';
import { Answer } from './AskView';
import { Cited } from './Knowledge';

// The meetings whose prep card the owner collapsed, newest last, so it
// stays collapsed for that meeting (per viewer; storage may be missing).
const collapsedKey = 'musubi.livePrep.collapsed';
const keepCollapsed = 20;
function readCollapsed(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(collapsedKey) ?? '[]');
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : [];
  } catch {
    return [];
  }
}
function writeCollapsed(session: string, collapsed: boolean) {
  try {
    const rest = readCollapsed().filter((s) => s !== session);
    localStorage.setItem(collapsedKey, JSON.stringify((collapsed ? [...rest, session] : rest).slice(-keepCollapsed)));
  } catch {
    // Without storage it is only forgotten when the app restarts.
  }
}

// How much of the talking points shows before "more".
const compactPoints = 3;

// firstPoints is the brief cut to its first few bullets (or lines).
function firstPoints(text: string, n: number): { text: string; cut: boolean } {
  const lines = text.split('\n').filter((l) => l.trim());
  const bullets = lines.filter((l) => /^\s*[*-]\s+/.test(l) && !/^\s{2,}/.test(l));
  const src = bullets.length > 0 ? bullets : lines;
  return { text: src.slice(0, n).join('\n'), cut: src.length > n || (bullets.length > 0 && lines.length > bullets.length) };
}

interface Draft { id: string; text: string; sources: Hit[]; done: boolean; error?: string }

// PrepCard is the meeting's prep at the top of Live's cards
// (live.prep_card): the talking points drafted ahead for its calendar
// event, with their sources, then what is open with the people in it. It
// is compact (the first few points) until "more", collapses to one line
// on ×, and drafts talking points only when asked. Everything is read in
// the panel's scope, which the backend takes from Live itself.
export function PrepCard({ sessionId, scope, onOpen, onTopic }: {
  sessionId: string; scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void;
}) {
  const [prep, setPrep] = useState<LivePrep | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [more, setMore] = useState(false);
  const [showSources, setShowSources] = useState(false);
  const [collapsed, setCollapsed] = useState(() => readCollapsed().includes(sessionId));
  const [err, setErr] = useState('');
  const want = useRef({ sessionId, scope });
  want.current = { sessionId, scope };

  const load = () => {
    api.livePrep().then((p) => {
      // A reply for another meeting or scope (the toggle moved meanwhile)
      // is never shown.
      const w = want.current;
      if (!p) setPrep(null);
      else if (p.sessionId === w.sessionId && p.scope === w.scope) setPrep(p);
    }, (e) => setErr(String(e)));
  };

  useEffect(() => {
    // Drop what was shown at once: nothing read in private stays up while
    // the shared screen's prep loads.
    setPrep(null);
    setDraft(null);
    setErr('');
    setCollapsed(readCollapsed().includes(sessionId));
    load();
  }, [sessionId, scope]);

  useEffect(() => {
    const offs = [
      on<{ id: string }>('prep:brief', ({ id }) => { if (id === prep?.eventId) load(); }),
      on<{ id: string; text: string }>('prep:token', ({ id, text }) => setDraft((d) => (d && d.id === id ? { ...d, text: d.text + text } : d))),
      on<{ id: string; text?: string; sources?: Hit[]; error?: string; stopped?: boolean }>('prep:done', (r) =>
        setDraft((d) => (d && d.id === r.id ? { ...d, done: true, text: r.text || d.text, sources: r.sources ?? [], error: r.error ?? (r.stopped ? 'stopped' : undefined) } : d)),
      ),
    ];
    return () => offs.forEach((f) => f());
  }, [prep?.eventId]);

  if (!prep?.view) return err ? <div className="error small">{err}</div> : null;
  const view: PrepView = prep.view;

  const startDraft = () => {
    const id = 'live-' + Date.now();
    setDraft({ id, text: '', sources: [], done: false });
    setMore(true);
    api.livePrepBrief(id).catch((e) => setDraft({ id, text: '', sources: [], done: true, error: String(e) }));
  };
  const toggleCollapsed = () => {
    writeCollapsed(sessionId, !collapsed);
    setCollapsed(!collapsed);
  };

  // A draft made here stands for the saved one once it has text.
  const brief = draft && (draft.text || !draft.done) ? { text: draft.text, sources: draft.sources, madeAt: '' } : prep.brief ? { text: prep.brief.text, sources: prep.brief.sources ?? [], madeAt: prep.brief.madeAt } : null;
  const drafting = !!draft && !draft.done;
  const open = (view.open ?? []).filter((o) => !o.quiet);
  const quiet = (view.open ?? []).filter((o) => o.quiet);
  const lastOpen = (view.last?.todos ?? []).filter((t) => t.state === 'open' || t.state === 'quiet' || t.state === 'proposed');
  const points = brief ? firstPoints(brief.text, compactPoints) : null;
  const hasMore = !!points?.cut || open.length > (brief ? 0 : compactPoints) || quiet.length > 0 || lastOpen.length > 0 || (view.new?.length ?? 0) > 0 || (brief?.sources.length ?? 0) > 0;
  const src = (itemId: string, chunkId: number) => onOpen({ itemId, chunkId } as unknown as Hit, prep.scope);
  const summary = [brief && !drafting ? `${firstPoints(brief.text, 99).text.split('\n').length} talking points` : '', open.length ? `${open.length} open with them` : '', lastOpen.length ? `${lastOpen.length} still open from last time` : ''].filter(Boolean).join(' · ');

  const openList = (items: typeof open) => (
    <ul className="status-list prep-open">
      {items.map((o, i) => {
        const byId = new Map((o.items ?? []).map((it) => [it.id, it] as [number, ThreadItem]));
        return (
          <li key={i}>
            <Cited text={o.text} byId={byId} onSource={src} />
            {o.overdue && <span className="badge prep-overdue">overdue</span>}
            {o.private && <span className="scope-pill">for you</span>}
            <span className="dim"> — {[o.who, o.due && 'due ' + o.due].filter(Boolean).join(' · ')}{o.who || o.due ? ' · ' : ''}</span>
            <button className="link" onClick={() => onTopic(o.thread)}>{o.name}</button>
          </li>
        );
      })}
    </ul>
  );

  return (
    <article className={'topic prep-card' + (collapsed ? ' collapsed' : '')}>
      <header>
        <span className="topic-title" onClick={collapsed ? toggleCollapsed : undefined}>Prep{collapsed && summary ? <span className="dim prep-summary"> · {summary}</span> : null}</span>
        {drafting && <><Spinner /><button className="link" onClick={() => api.stopAsk()}>stop</button></>}
        <button className="link" onClick={toggleCollapsed} title={collapsed ? 'Show the prep' : 'Collapse it for this meeting'}>{collapsed ? 'show' : '×'}</button>
      </header>
      {!collapsed && (
        <>
          {brief ? (
            <>
              {brief.madeAt && <div className="brief-why">Talking points drafted {fmtDate(brief.madeAt)}</div>}
              {brief.text ? <Answer text={more ? brief.text : points!.text} hits={brief.sources} onCite={(h) => onOpen(h, prep.scope)} /> : <div className="brief-why">drafting…</div>}
              {more && brief.sources.length > 0 && (
                <>
                  <button className="link small" onClick={() => setShowSources(!showSources)}>{showSources ? 'hide sources' : `${brief.sources.length} sources`}</button>
                  {showSources && brief.sources.map((h, i) => <HitRow key={h.chunkId + ':' + i} hit={h} n={i + 1} onOpen={(x) => onOpen(x, prep.scope)} />)}
                </>
              )}
            </>
          ) : (
            <div className="prep-draft">
              <span className="brief-why">No talking points drafted for this meeting.</span>
              {!drafting && <button className="chip" onClick={startDraft}>Draft prep</button>}
            </div>
          )}
          {draft?.error && <div className="error small">{draft.error}</div>}

          {!brief && open.length > 0 && !more && openList(open.slice(0, compactPoints))}
          {more && (
            <>
              {(view.new?.length ?? 0) > 0 && <div className="brief-why">First meeting with <Names names={view.new!} /></div>}
              {open.length > 0 && (
                <div className="topic-sec">
                  <div className="topic-sec-name">Open between you</div>
                  {openList(open)}
                </div>
              )}
              {lastOpen.length > 0 && (
                <div className="topic-sec">
                  <div className="topic-sec-name">Still open from last time{view.last ? ' · ' + fmtDate(view.last.start) : ''}</div>
                  <ul className="status-list">
                    {lastOpen.map((t) => (
                      <li key={t.id}>{t.text}<span className="dim"> — {(t.owners?.length ?? 0) > 0 ? <Names names={t.owners!} /> : 'owner unclear'}{t.due ? ' · ' + t.due : ''}{t.state === 'quiet' ? ' · gone quiet' : ''}</span></li>
                    ))}
                  </ul>
                </div>
              )}
              {quiet.length > 0 && (
                <div className="topic-sec">
                  <div className="topic-sec-name">Gone quiet: check before raising</div>
                  {openList(quiet)}
                </div>
              )}
            </>
          )}
          {!brief && open.length === 0 && lastOpen.length === 0 && <div className="brief-why">Nothing open with them in the record.</div>}
          {(hasMore || (more && brief && !drafting)) && (
            <div className="prep-actions">
              {more && brief && !drafting && <button className="link small" onClick={startDraft}>redraft</button>}
              {hasMore && <button className="link small" onClick={() => setMore(!more)}>{more ? 'less' : 'more'}</button>}
            </div>
          )}
        </>
      )}
    </article>
  );
}
