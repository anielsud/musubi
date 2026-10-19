import { Fragment, useEffect, useState } from 'react';
import { api, fmtDate } from '../api';
import type { AliasUsage, AliasView } from '../types';
import { Empty, Spinner } from './common';
import { useWide } from './TopicViews';

// CorrectionsView: aliases the corpus proposes (a misheard or misspelled
// name or term), each with what it would touch. Confirming applies it when
// reading (Ask, Prep, Live); nothing is rewritten, and it can be undone.
// It opens in landscape: the list on the left, and beside it every place
// the selected alias is used, so the decision rests on the data itself.
export function CorrectionsView({ onSource }: { onSource: (itemId: string, chunkId: number) => void }) {
  const wide = useWide();
  const [status, setStatus] = useState('proposed');
  const [list, setList] = useState<AliasView[] | null>(null);
  const [sel, setSel] = useState<AliasView | null>(null);
  const [uses, setUses] = useState<AliasUsage[] | null>(null);
  // The uses the alias is right for: those in threads that already name
  // the kept form start checked.
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [err, setErr] = useState('');
  // The right form: the kept spelling, or one the owner types when
  // neither is right.
  const [keep, setKeep] = useState('');
  const [newWrong, setNewWrong] = useState('');
  const [newRight, setNewRight] = useState('');

  useEffect(() => {
    if (!wide) api.setWide(true);
  }, []);
  const load = (select?: number) => {
    setList(null);
    api.aliases(status).then((x) => {
      setList(x ?? []);
      setSel((x ?? []).find((a) => a.id === select) ?? (x ?? [])[0] ?? null);
    }, (e) => setErr(String(e)));
  };
  useEffect(() => { setSel(null); load(); }, [status]);
  useEffect(() => { setKeep(sel?.keep ?? ''); }, [sel]);
  const addNew = () => {
    setErr('');
    api.addAlias(newWrong, newRight).then((id) => {
      setNewWrong('');
      setNewRight('');
      if (status !== 'proposed') setStatus('proposed');
      load(id);
    }, (e) => setErr(String(e)));
  };
  useEffect(() => {
    setUses(null);
    if (sel) api.aliasUsages(sel.id).then((u) => {
      setUses(u ?? []);
      setChecked(new Set((u ?? []).filter((x) => x.hasKept).map((x) => x.statement)));
    }, (e) => setErr(String(e)));
  }, [sel]);

  const drop = (a: AliasView) =>
    setList((l) => {
      const rest = (l ?? []).filter((x) => x.id !== a.id);
      const i = (l ?? []).findIndex((x) => x.id === a.id);
      setSel(rest[Math.min(i, rest.length - 1)] ?? null);
      return rest;
    });
  const confirmChecked = (a: AliasView) => {
    const all = (uses ?? []).length > 0 && (uses ?? []).every((u) => checked.has(u.statement));
    api.confirmAliasAs(a.id, keep, [...checked], (uses ?? []).map((u) => u.statement), all).then(() => drop(a), (e) => setErr(String(e)));
  };
  const allChecked = (uses ?? []).length > 0 && (uses ?? []).every((u) => checked.has(u.statement));
  const setAll = (on: boolean) => setChecked(on ? new Set((uses ?? []).map((u) => u.statement)) : new Set());
  const toggle = (id: number) => setChecked((c) => { const n = new Set(c); if (n.has(id)) n.delete(id); else n.add(id); return n; });
  const decide = (a: AliasView, confirm: boolean) =>
    api.decideAlias(a.id, confirm).then(() => {
      setList((l) => {
        const rest = (l ?? []).filter((x) => x.id !== a.id);
        const i = (l ?? []).findIndex((x) => x.id === a.id);
        setSel(rest[Math.min(i, rest.length - 1)] ?? null);
        return rest;
      });
    }, (e) => setErr(String(e)));

  const listPane = (
    <div className="corr-list">
      {(list ?? []).map((a) => (
        <article key={a.id} className={'alias' + (a.newLinks > 3 ? ' risky' : '') + (sel?.id === a.id ? ' sel' : '')} onClick={() => setSel(a)}>
          <div className="alias-head">
            <b>{a.variant}</b> <span className="dim">→</span> <b>{a.keep}</b>
            {a.newUses ? <span className="status st-likely" title="confirmed for some uses; used since in places not yet reviewed">{a.newUses} new uses</span>
              : a.newLinks > 0 && <span className={'status ' + (a.newLinks > 3 ? 'st-contested' : 'st-open')} title="threads where the kept form isn't mentioned yet">{a.newLinks} new links</span>}
          </div>
          <div className="dim small">{a.statements} statements in {a.items} meetings · {a.threads} threads</div>
        </article>
      ))}
    </div>
  );
  const usagePane = sel && (
    <div className="corr-uses">
      <div className="corr-uses-head">
        <h3><b>{sel.variant}</b> <span className="dim">→</span>{' '}
          {status === 'proposed'
            ? <input className="keep-input" value={keep} onChange={(e) => setKeep(e.target.value)} title="The right form: edit it when neither spelling is right" />
            : <b>{sel.keep}</b>}
        </h3>
        {status === 'proposed' && (
          <span className="alias-actions">
            <button className="chip" disabled={checked.size === 0} onClick={() => confirmChecked(sel)}
              title={`Read the checked uses as ${keep || sel.keep} (all checked: everywhere, new meetings too)`}>
              ✓ same in {checked.size === (uses ?? []).length && checked.size > 0 ? 'all' : checked.size} {checked.size === 1 ? 'use' : 'uses'}
            </button>
            {!sel.newUses && <button className="chip" onClick={() => decide(sel, false)}>✗ different everywhere</button>}
          </span>
        )}
      </div>
      {status === 'proposed' && (uses ?? []).length > 0 && (
        <div className="corr-select">
          <button className="link" onClick={() => setAll(!allChecked)}>{allChecked ? 'select none' : 'select all'}</button>
          <span className="dim small">{checked.size} of {(uses ?? []).length} checked{sel.newUses ? ' · new uses since your last review' : ''}</span>
        </div>
      )}
      <div className="dim small">Every statement using “{sel.variant}”, newest first. Check the uses that mean {sel.keep}; those in threads already naming {sel.keep} start checked. “New link”: that thread doesn't mention {sel.keep} yet.</div>
      {!uses && <Spinner />}
      {(uses ?? []).map((u) => (
        <div key={u.statement} className={'corr-use' + (checked.has(u.statement) ? ' on' : '') + (status === 'proposed' ? ' pickable' : '')}
          onClick={() => status === 'proposed' && toggle(u.statement)} title={status === 'proposed' ? `Click: this use means ${sel.keep}` : undefined}>
          {status === 'proposed' && <input type="checkbox" checked={checked.has(u.statement)} readOnly />}
          <div className="corr-use-body">
          <div className="corr-use-head">
            <span className="dim">{fmtDate(u.at)} · {u.itemTitle}</span>
            {u.thread && <span className="corr-thread">{u.thread}</span>}
            {u.thread && !u.hasKept && <span className="status st-open">new link</span>}
            {!u.current && u.thread && <span className="dim small">retired</span>}
          </div>
          <div className="corr-use-text"><Marked text={u.text} word={sel.variant} /></div>
          </div>
          <button className="link corr-src" onClick={(e) => { e.stopPropagation(); onSource(u.itemId, u.chunkId); }} title="Open the source passage">source</button>
        </div>
      ))}
    </div>
  );

  return (
    <div className="corrections">
      <div className="tview-switch">
        {['proposed', 'confirmed', 'rejected'].map((s) => (
          <button key={s} className={status === s ? 'on' : ''} onClick={() => setStatus(s)}>{s}</button>
        ))}
        <span className="tview-count">Names and terms written or heard two ways. Confirming applies when reading; nothing is rewritten.</span>
      </div>
      <form className="corr-new" onSubmit={(e) => { e.preventDefault(); addNew(); }}>
        <span className="dim small">New correction:</span>
        <input value={newWrong} onChange={(e) => setNewWrong(e.target.value)} placeholder="written as" />
        <span className="dim">→</span>
        <input value={newRight} onChange={(e) => setNewRight(e.target.value)} placeholder="should be" />
        <button className="chip" type="submit" disabled={!newWrong.trim() || !newRight.trim()}>find uses</button>
      </form>
      {err && <div className="error small">{err}</div>}
      {!list && <Spinner />}
      {list && list.length === 0 && <Empty>{status === 'proposed' ? 'No proposals yet: they are refreshed after each update.' : 'None yet.'}</Empty>}
      {list && list.length > 0 && <div className="corr-split">{listPane}{usagePane}</div>}
    </div>
  );
}

// Marked highlights a word in text, whole words only.
function Marked({ text, word }: { text: string; word: string }) {
  const re = new RegExp(`\\b(${word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})\\b`, 'gi');
  const parts = text.split(re);
  return <>{parts.map((p, i) => (i % 2 === 1 ? <mark key={i}>{p}</mark> : <Fragment key={i}>{p}</Fragment>))}</>;
}
