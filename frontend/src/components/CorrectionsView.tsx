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

  useEffect(() => {
    if (!wide) api.setWide(true);
  }, []);
  useEffect(() => {
    setList(null);
    setSel(null);
    api.aliases(status).then((x) => { setList(x ?? []); setSel((x ?? [])[0] ?? null); }, (e) => setErr(String(e)));
  }, [status]);
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
    api.confirmAliasIn(a.id, [...checked], all).then(() => drop(a), (e) => setErr(String(e)));
  };
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
            {a.newLinks > 0 && <span className={'status ' + (a.newLinks > 3 ? 'st-contested' : 'st-open')} title="threads where the kept form isn't mentioned yet">{a.newLinks} new links</span>}
          </div>
          <div className="dim small">{a.statements} statements in {a.items} meetings · {a.threads} threads</div>
        </article>
      ))}
    </div>
  );
  const usagePane = sel && (
    <div className="corr-uses">
      <div className="corr-uses-head">
        <h3><b>{sel.variant}</b> <span className="dim">→</span> <b>{sel.keep}</b></h3>
        {status === 'proposed' && (
          <span className="alias-actions">
            <button className="chip" disabled={checked.size === 0} onClick={() => confirmChecked(sel)}
              title="Apply the alias to the checked uses only (all checked: everywhere, new meetings too)">
              ✓ same in {checked.size === (uses ?? []).length && checked.size > 0 ? 'all' : checked.size} {checked.size === 1 ? 'use' : 'uses'}
            </button>
            <button className="chip" onClick={() => decide(sel, false)}>✗ different everywhere</button>
          </span>
        )}
      </div>
      <div className="dim small">Every statement using “{sel.variant}”, newest first. Check the uses that mean {sel.keep}; those in threads already naming {sel.keep} start checked. “New link”: that thread doesn't mention {sel.keep} yet.</div>
      {!uses && <Spinner />}
      {(uses ?? []).map((u) => (
        <div key={u.statement} className={'corr-use' + (checked.has(u.statement) ? ' on' : '')}>
          {status === 'proposed' && <input type="checkbox" checked={checked.has(u.statement)} onChange={() => toggle(u.statement)} title={`This use means ${sel.keep}`} />}
          <button className="corr-use-body" onClick={() => onSource(u.itemId, u.chunkId)} title="Open the source passage">
          <div className="corr-use-head">
            <span className="dim">{fmtDate(u.at)} · {u.itemTitle}</span>
            {u.thread && <span className="corr-thread">{u.thread}</span>}
            {u.thread && !u.hasKept && <span className="status st-open">new link</span>}
            {!u.current && u.thread && <span className="dim small">retired</span>}
          </div>
          <div className="corr-use-text"><Marked text={u.text} word={sel.variant} /></div>
          </button>
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
