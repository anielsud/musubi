import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../api';
import type { Hit, PrepRequest, PrepTarget, PrepView as Prep, ThreadItem, ThreadRef } from '../types';
import { Empty, HitRow, Spinner } from './common';
import { Answer } from './AskView';
import { Cited, Refs } from './Knowledge';

// PrepView: before a meeting, what to bring: the subjects these people are
// part of and where each stands, what moved since you last met, what is
// open between you, and what was said elsewhere they likely haven't heard.
export function PrepView({ scope, onOpen, onTopic }: { scope: string; onOpen: (h: Hit, scope: string) => void; onTopic: (id: number) => void }) {
  const [targets, setTargets] = useState<PrepTarget[] | null>(null);
  const [req, setReq] = useState<PrepRequest | null>(null);
  const [names, setNames] = useState('');
  const [view, setView] = useState<Prep | null | undefined>(null);
  const [err, setErr] = useState('');
  const [brief, setBrief] = useState<{ id: string; text: string; sources: Hit[]; threads: ThreadRef[]; done: boolean; error?: string } | null>(null);
  const src = (itemId: string, chunkId: number) => onOpen({ itemId, chunkId } as unknown as Hit, scope);

  useEffect(() => {
    const t = setTimeout(() => api.prepTargets().then((x) => setTargets(x ?? []), (e) => setErr(String(e))), 200);
    return () => clearTimeout(t);
  }, []);

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
    if (!req) return;
    setView(undefined);
    setBrief(null);
    setErr('');
    api.prep(req, scope).then(setView, (e) => { setErr(String(e)); setView(null); });
  }, [req, scope]);

  const draft = () => {
    if (!req) return;
    const id = String(Date.now());
    setBrief({ id, text: '', sources: [], threads: [], done: false });
    api.prepBrief(id, req, scope).catch((e) => setBrief({ id, text: '', sources: [], threads: [], done: true, error: String(e) }));
  };

  return (
    <div className="view prep">
      <div className="prep-pick">
        <select value={req?.template ?? ''} onChange={(e) => e.target.value && setReq({ template: e.target.value })}>
          <option value="">Prepare for…</option>
          {(targets ?? []).some((t) => t.upcoming) && <optgroup label="Upcoming">
            {targets!.filter((t) => t.upcoming).map((t) => <option key={t.itemId} value={t.itemId}>{fmtDate(t.at)} · {t.title}</option>)}
          </optgroup>}
          <optgroup label="The next one of a recent meeting">
            {(targets ?? []).filter((t) => !t.upcoming).map((t) => <option key={t.itemId} value={t.itemId}>{t.title} · last {fmtDate(t.at)}</option>)}
          </optgroup>
        </select>
        <form onSubmit={(e) => { e.preventDefault(); const ps = names.split(',').map((x) => x.trim()).filter(Boolean); if (ps.length) setReq({ people: ps }); }}>
          <input value={names} onChange={(e) => setNames(e.target.value)} placeholder="…or people: name, name" />
        </form>
      </div>
      {err && <div className="error small">{err}</div>}
      {!req && <Empty>Pick a meeting to prepare for, or name the people you're meeting. Musubi gathers their subjects and where each stands, what moved since you last met, what's open between you, and what was said elsewhere they likely haven't heard.</Empty>}
      {view === undefined && <Spinner />}
      {view && (
        <>
          <div className="prep-head">
            <h2>{view.title || 'Meeting'}</h2>
            <div className="dim small">with {(view.people ?? []).join(', ') || 'nobody found'} · since {fmtDate(view.since)} · {view.met} meetings with them in the record</div>
          </div>

          <section className="thread-status">
            <div className="section-head">
              <span>Talking points</span>
              {!brief || brief.done ? <button className="link" onClick={draft}>{brief ? 'redraft' : 'draft'}</button> : <span><Spinner /> <button className="link" onClick={() => api.stopAsk()}>stop</button></span>}
            </div>
            {!brief && <div className="dim small">A model drafts what to raise, from everything below.</div>}
            {brief?.text && <Answer text={brief.text} hits={brief.sources} onCite={(h) => onOpen(h, scope)} />}
            {brief?.error && <div className="error small">{brief.error}</div>}
          </section>

          {(view.open?.length ?? 0) > 0 && (
            <>
              <div className="section-head"><span>Open between you</span></div>
              <ul className="status-list prep-open">
                {view.open!.map((o, i) => {
                  const byId = new Map((o.items ?? []).map((it) => [it.id, it] as [number, ThreadItem]));
                  return (
                    <li key={i}>
                      <Cited text={o.text} byId={byId} onSource={src} />
                      <span className="dim"> — {[o.who, o.due && 'due ' + o.due].filter(Boolean).join(' · ')} · </span>
                      <button className="link" onClick={() => onTopic(o.thread)}>{o.name}</button>
                      <Refs ids={(o.items ?? []).map((x) => x.id)} byId={byId} onSource={src} />
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
                <div className="dim small">{s.why} · as of {fmtDate(s.asOf)}</div>
                {s.status && <p className="status-now"><Cited text={s.status.now} byId={byId} onSource={src} /></p>}
                {(s.newSince?.length ?? 0) > 0 && (
                  <>
                    <div className="status-head">New since you last met</div>
                    <ul className="status-list">
                      {s.newSince!.map((it) => (
                        <li key={it.id}><span className="dim">{fmtDate(it.saidAt)} · </span>{it.text} <Refs ids={[it.id]} byId={byId} onSource={src} /></li>
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
