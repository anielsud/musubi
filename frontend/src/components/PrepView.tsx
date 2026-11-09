import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../api';
import type { Hit, PrepRequest, PrepView as Prep, ThreadItem, ThreadRef } from '../types';
import { Empty, HitRow, Spelled, Spinner } from './common';
import { Names } from './people';
import { Answer } from './AskView';
import { Cited, Refs, citedIn } from './Knowledge';

// PrepPage: before a meeting, what to bring: the subjects these people are
// part of and where each stands, what moved since you last met, what is
// open between you, and what was said elsewhere they likely haven't heard.
// Opened from the Meetings tab, for an upcoming meeting, the next one of a
// past meeting, or named people.
export function PrepPage({ req, scope, onBack, onOpen, onTopic }: {
  req: PrepRequest; scope: string; onBack?: () => void; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void;
}) {
  const [view, setView] = useState<Prep | null | undefined>(null);
  const [err, setErr] = useState('');
  const [brief, setBrief] = useState<{ id: string; text: string; sources: Hit[]; threads: ThreadRef[]; done: boolean; error?: string; madeAt?: string } | null>(null);
  // Talking points drafted ahead (prep.auto_brief) show until redrafted
  // here, and are replaced when a newer draft lands.
  const ev = req.template;
  const loadSaved = () => {
    if (!ev) return;
    api.savedPrepBrief(ev, scope).then((b) => {
      if (b) setBrief((cur) => (cur && !cur.madeAt ? cur : { id: 'saved', text: b.text, sources: b.sources ?? [], threads: b.threads ?? [], done: true, madeAt: b.madeAt }));
    }, () => {});
  };
  useEffect(() => on<{ id: string }>('prep:brief', ({ id }) => { if (id === ev) loadSaved(); }), [ev, scope]);
  const src = (itemId: string, chunkId: number) => onOpen({ itemId, chunkId } as unknown as Hit, scope);

  useEffect(() => {
    const offs = [
      on<{ id: string; text: string }>('prep:token', ({ id, text }) => setBrief((b) => (b && b.id === id ? { ...b, text: b.text + text } : b))),
      on<{ id: string; text?: string; sources?: Hit[]; threads?: ThreadRef[]; error?: string; stopped?: boolean }>('prep:done', (r) =>
        setBrief((b) => (b && b.id === r.id ? { ...b, done: true, text: r.text || b.text, sources: r.sources ?? [], threads: r.threads ?? [], error: r.error ?? (r.stopped ? 'stopped' : undefined) } : b)),
      ),
    ];
    return () => offs.forEach((f) => f());
  }, []);

  useEffect(() => {
    setView(undefined);
    setBrief(null);
    setErr('');
    api.prep(req, scope).then(setView, (e) => { setErr(String(e)); setView(null); });
    loadSaved();
  }, [req, scope]);

  const draft = () => {
    const id = String(Date.now());
    setBrief({ id, text: '', sources: [], threads: [], done: false });
    api.prepBrief(id, req, scope).catch((e) => setBrief({ id, text: '', sources: [], threads: [], done: true, error: String(e) }));
  };

  return (
    <div className="view prep">
      {onBack && <button className="link back" onClick={onBack}>← Meetings</button>}
      {err && <div className="error small">{err}</div>}
      {view === undefined && <Spinner />}
      {view && (
        <>
          <div className="prep-head">
            <h2>{view.title || 'Meeting'}</h2>
            <div className="dim small">with {(view.people?.length ?? 0) > 0 ? <Names names={view.people!} /> : 'nobody found'} · since {fmtDate(view.since)} · {view.met} meetings with them in the record</div>
          </div>

          <section className="thread-status">
            <div className="section-head">
              <span>Talking points</span>
              {!brief || brief.done ? <button className="link" onClick={draft}>{brief ? 'redraft' : 'draft'}</button> : <span><Spinner /> <button className="link" onClick={() => api.stopAsk()}>stop</button></span>}
            </div>
            {!brief && <div className="dim small">A model drafts what to raise, from everything below.</div>}
            {brief?.madeAt && <div className="dim small">Drafted {fmtDate(brief.madeAt)}; drafted again as new information comes in.</div>}
            {brief?.text && <Answer text={brief.text} hits={brief.sources} onCite={(h) => onOpen(h, scope)} />}
            {brief?.error && <div className="error small">{brief.error}</div>}
          </section>

          {(view.open?.length ?? 0) > 0 && (
            <>
              <div className="section-head"><span>Open between you</span></div>
              <ul className="status-list prep-open">
                {view.open!.map((o, i) => {
                  const byId = new Map((o.items ?? []).map((it) => [it.id, it] as [number, ThreadItem]));
                  const firstQuiet = o.quiet && !view.open![i - 1]?.quiet;
                  return (
                    <li key={i} className={o.quiet ? 'prep-quiet' : undefined}>
                      {firstQuiet && <div className="status-head">Gone quiet: nothing said of these since; check before raising</div>}
                      <Cited text={o.text} byId={byId} onSource={src} />
                      {o.overdue && <span className="badge prep-overdue">overdue</span>}
                      {o.private && <span className="scope-pill">for you</span>}
                      <span className="dim"> — {[o.who, o.due && 'due ' + o.due, o.raised && 'raised ' + fmtDate(o.raised)].filter(Boolean).join(' · ')} · </span>
                      <button className="link" onClick={() => onTopic(o.thread)}>{o.name}</button>
                      <Refs ids={(o.items ?? []).map((x) => x.id).filter((id) => !citedIn(o.text).has(id))} byId={byId} onSource={src} />
                    </li>
                  );
                })}
              </ul>
            </>
          )}

          <div className="section-head"><span>Their subjects</span></div>
          {(view.subjects ?? []).length === 0 && <Empty>No subjects with them yet.</Empty>}
          {(view.subjects ?? []).map((s) => {
            const byId = new Map((s.items ?? []).map((it) => [it.id, it] as [number, ThreadItem]));
            return (
              <article key={s.id} className="prep-subject">
                <button className="topic-label link" onClick={() => onTopic(s.id)}>{s.name}</button>
                {s.private && <span className="scope-pill" title="About people's jobs, pay, health, legal matters or private lives: for you, not the room">for you, not the room</span>}
                <div className="dim small">{s.why} · as of {fmtDate(s.asOf)}</div>
                {s.status && <p className="status-now"><Cited text={s.status.now} byId={byId} onSource={src} /></p>}
                {(s.newSince?.length ?? 0) > 0 && (
                  <>
                    <div className="status-head">New since you last met</div>
                    <ul className="status-list">
                      {s.newSince!.map((it) => (
                        <li key={it.id} data-statement={it.id} data-item={it.itemId} data-chunk={it.chunkId} data-title={it.itemTitle} data-at={it.saidAt}><span className="dim">{fmtDate(it.saidAt)} · </span><Spelled s={it} /> <Refs ids={[it.id]} byId={byId} onSource={src} /></li>
                      ))}
                    </ul>
                  </>
                )}
              </article>
            );
          })}

          {(view.share?.length ?? 0) > 0 && (
            <>
              <div className="section-head"><span>Worth sharing</span><span className="dim">said where none of them were</span></div>
              {view.share!.map((sh) => (
                <HitRow key={sh.item.id} hit={{ chunkId: sh.item.chunkId, itemId: sh.item.itemId, kind: sh.item.itemKind ?? 'meeting', title: `${sh.name} — from ${sh.item.itemTitle}`, occurredAt: sh.item.saidAt, seq: sh.item.chunkSeq, start: 0, end: 0, speakers: sh.item.holder, text: sh.item.text, score: 0, label: sh.item.label }} onOpen={(h) => onOpen(h, scope)} />
              ))}
              <div className="dim small">Never suggested: anything about people's jobs, pay, health, legal matters or private lives, or subjects often about them.</div>
            </>
          )}
        </>
      )}
    </div>
  );
}
