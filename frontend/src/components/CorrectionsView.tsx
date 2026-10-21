import { Fragment, useEffect, useState } from 'react';
import { api, fmtDate } from '../api';
import type { AliasUsage, AliasView, StatementEdit } from '../types';
import { Empty, Spinner } from './common';
import { useWide } from './TopicViews';

// CorrectionsView: names and terms written or heard two ways. To review
// lists what the corpus proposes (and rules with uses nobody has looked
// at); Rules lists every decision, each of which can be opened and decided
// again, and the owner's edits to statements. A rule changes how text is
// read (Ask, Prep, Live, statuses); nothing stored is rewritten.
// It opens in landscape: the list on the left, and beside it every place
// the selected alias is used, so the decision rests on the data itself.
export function CorrectionsView({ onSource, start }: {
  onSource: (itemId: string, chunkId: number) => void;
  // start opens a new correction for a word (from a text selection).
  start?: { variant: string; n: number } | null;
}) {
  const wide = useWide();
  const [tab, setTab] = useState<'review' | 'rules'>('review');
  const [list, setList] = useState<AliasView[] | null>(null);
  const [edits, setEdits] = useState<StatementEdit[] | null>(null);
  const [sel, setSel] = useState<AliasView | null>(null);
  const [uses, setUses] = useState<AliasUsage[] | null>(null);
  const [checked, setChecked] = useState<Set<number>>(new Set());
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  // The right form: the kept spelling, or one the owner types when
  // neither is right.
  const [keep, setKeep] = useState('');
  const [newWrong, setNewWrong] = useState('');
  const [newRight, setNewRight] = useState('');

  useEffect(() => {
    if (!wide) api.setWide(true);
  }, []);
  useEffect(() => {
    if (start?.variant) {
      setTab('review');
      setNewWrong(start.variant);
      setNewRight('');
    }
  }, [start?.n]);
  const load = (select?: number) => {
    setList(null);
    api.aliases(tab === 'review' ? 'proposed' : 'rules').then((x) => {
      setList(x ?? []);
      setSel((x ?? []).find((a) => a.id === select) ?? (x ?? [])[0] ?? null);
    }, (e) => setErr(String(e)));
    if (tab === 'rules') api.statementEdits().then((e) => setEdits(e ?? []), (e) => setErr(String(e)));
  };
  useEffect(() => { setSel(null); load(); }, [tab]);
  useEffect(() => { setKeep(sel?.keep ?? ''); }, [sel]);
  const addNew = () => {
    setErr('');
    api.addAlias(newWrong, newRight).then((id) => {
      setNewWrong('');
      setNewRight('');
      if (tab !== 'review') setTab('review');
      else load(id);
    }, (e) => setErr(String(e)));
  };
  // A rule's uses start checked as it stands: a rule for some uses, those;
  // always, all; both right, none. A proposal starts with the uses in
  // threads that already name the kept form.
  useEffect(() => {
    setUses(null);
    if (sel) api.aliasUsages(sel.id).then((u) => {
      const us = u ?? [];
      setUses(us);
      const only = new Set(sel.only ?? []);
      setChecked(new Set(us.filter((x) =>
        sel.mode === 'always' ? true : sel.mode === 'these' ? only.has(x.statement) : sel.mode === 'apart' ? false : x.hasKept,
      ).map((x) => x.statement)));
    }, (e) => setErr(String(e)));
  }, [sel]);

  const apply = (a: AliasView, mode: 'always' | 'these' | 'apart' | 'review') => {
    setErr('');
    setBusy(true);
    const all = (uses ?? []).map((u) => u.statement);
    api.setAliasRule(a.id, mode, mode === 'review' ? '' : keep, mode === 'these' ? [...checked] : [], all).then(() => {
      setBusy(false);
      load(tab === 'rules' && mode !== 'review' ? a.id : undefined);
    }, (e) => { setBusy(false); setErr(String(e)); });
  };
  const undoEdit = (e: StatementEdit) => {
    setErr('');
    api.undoStatementEdit(e.id).then(() => load(sel?.id), (x) => setErr(String(x)));
  };
  const allChecked = (uses ?? []).length > 0 && (uses ?? []).every((u) => checked.has(u.statement));
  const setAll = (on: boolean) => setChecked(on ? new Set((uses ?? []).map((u) => u.statement)) : new Set());
  const toggle = (id: number) => setChecked((c) => { const n = new Set(c); if (n.has(id)) n.delete(id); else n.add(id); return n; });

  const listPane = (
    <div className="corr-list">
      {(list ?? []).map((a) => (
        <article key={a.id} className={'alias' + (a.mode === 'review' && a.newLinks > 3 ? ' risky' : '') + (sel?.id === a.id ? ' sel' : '')} onClick={() => setSel(a)}>
          <div className="alias-head">
            <b>{a.variant}</b> <span className="dim">{a.mode === 'apart' ? '≠' : '→'}</span> <b>{a.keep}</b>
            <ModeChip a={a} />
          </div>
          <div className="dim small">{a.statements} statements in {a.items} meetings · {a.threads} threads</div>
          {a.replaced?.length ? <div className="dim small">instead of {a.replaced.map((r) => `${a.variant} → ${r}`).join(', ')}</div> : null}
        </article>
      ))}
      {tab === 'rules' && (
        <>
          <h4 className="corr-section">Statement edits</h4>
          {!edits && <Spinner />}
          {edits && edits.length === 0 && <div className="dim small">None: edit a statement from its topic page, or select its text.</div>}
          {(edits ?? []).map((e) => (
            <article key={e.id} className="alias edit">
              <div className="dim small">{fmtDate(e.at)} · {e.itemTitle}{e.field === 'who' ? ' · who said it' : ''}{e.gone ? ' · no longer extracted' : ''}</div>
              <div className="corr-edit"><del>{e.field === 'who' ? whoList(e.old) : e.old}</del></div>
              <div className="corr-edit"><ins>{e.field === 'who' ? whoList(e.new) : e.new}</ins></div>
              <div className="corr-edit-actions">
                <button className="link" onClick={() => undoEdit(e)} title="Put the statement back as it was">undo</button>
                <button className="link" onClick={() => onSource(e.itemId, e.chunkId)}>source</button>
              </div>
            </article>
          ))}
        </>
      )}
    </div>
  );

  const usagePane = sel && (
    <div className="corr-uses">
      <div className="corr-uses-head">
        <h3><b>{sel.variant}</b> <span className="dim">→</span>{' '}
          <input className="keep-input" value={keep} onChange={(e) => setKeep(e.target.value)} title="The right form: edit it when neither spelling is right" />
        </h3>
      </div>
      <Choices a={sel} keep={keep || sel.keep} n={checked.size} total={(uses ?? []).length} busy={busy} onApply={(m) => apply(sel, m)} />
      {(uses ?? []).length > 0 && (
        <div className="corr-select">
          <button className="link" onClick={() => setAll(!allChecked)}>{allChecked ? 'select none' : 'select all'}</button>
          <span className="dim small">{checked.size} of {(uses ?? []).length} checked{sel.newUses ? ' · new uses are listed first, unchecked' : ''} · the checks count only for “these uses only”</span>
        </div>
      )}
      <div className="dim small">Every statement using “{sel.variant}”, newest first. “New link”: that thread doesn't mention {keep || sel.keep} yet.</div>
      {!uses && <Spinner />}
      {(uses ?? []).map((u) => (
        <div key={u.statement} data-statement={u.statement} data-item={u.itemId} data-chunk={u.chunkId} data-title={u.itemTitle} data-at={u.at}
          className={'corr-use pickable' + (checked.has(u.statement) ? ' on' : '')}
          onClick={() => toggle(u.statement)} title={`Click: this use means ${keep || sel.keep}`}>
          <input type="checkbox" checked={checked.has(u.statement)} readOnly />
          <div className="corr-use-body">
            <div className="corr-use-head">
              <span className="dim">{fmtDate(u.at)} · {u.itemTitle}</span>
              {u.thread && <span className="corr-thread">{u.thread}</span>}
              {u.thread && !u.hasKept && <span className="status st-open">new link</span>}
              {!u.current && u.thread && <span className="dim small">retired</span>}
              {u.new && <span className="status st-likely">new use</span>}
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
        <button className={tab === 'review' ? 'on' : ''} onClick={() => setTab('review')}>To review</button>
        <button className={tab === 'rules' ? 'on' : ''} onClick={() => setTab('rules')}>Rules</button>
        <span className="tview-count">Names and terms written or heard two ways. A rule changes how text is read (Ask, Prep, Live, statuses); nothing is rewritten, and every rule can be changed.</span>
      </div>
      <form className="corr-new" onSubmit={(e) => { e.preventDefault(); addNew(); }}>
        <span className="dim small">New correction:</span>
        <input value={newWrong} onChange={(e) => setNewWrong(e.target.value)} placeholder="written as" />
        <span className="dim">→</span>
        <input value={newRight} onChange={(e) => setNewRight(e.target.value)} placeholder="should be" autoFocus={!!start?.variant} />
        <button className="chip" type="submit" disabled={!newWrong.trim() || !newRight.trim()}>find uses</button>
      </form>
      {err && <div className="error small">{err}</div>}
      {!list && <Spinner />}
      {list && list.length === 0 && tab === 'review' && <Empty>Nothing to review: proposals are refreshed after each update.</Empty>}
      {list && (list.length > 0 || tab === 'rules') && <div className="corr-split">{listPane}{usagePane ?? <div />}</div>}
    </div>
  );
}

function ModeChip({ a }: { a: AliasView }) {
  if (a.newUses) return <span className="status st-likely" title="a rule for some uses; used since in places not yet reviewed">{a.newUses} new uses</span>;
  switch (a.mode) {
    case 'always': return <span className="status st-established" title="every use, new meetings too">always</span>;
    case 'these': return <span className="status st-likely" title="only the uses checked when it was decided">{a.only?.length ?? 0} uses</span>;
    case 'apart': return <span className="status st-answered" title="both forms are right; not proposed again">both right</span>;
  }
  return a.newLinks > 0 ? <span className={'status ' + (a.newLinks > 3 ? 'st-contested' : 'st-open')} title="threads where the kept form isn't mentioned yet">{a.newLinks} new links</span> : null;
}

// Choices are the three decisions, each saying what it will do; the one in
// force is marked, and a decided rule can go back to review.
function Choices({ a, keep, n, total, busy, onApply }: {
  a: AliasView; keep: string; n: number; total: number; busy: boolean;
  onApply: (mode: 'always' | 'these' | 'apart' | 'review') => void;
}) {
  const v = `“${a.variant}”`, k = `“${keep}”`;
  const rest = total - n;
  const opts: { mode: 'always' | 'these' | 'apart'; label: string; says: string; off?: boolean }[] = [
    { mode: 'always', label: 'Same thing, always', says: `Read ${v} as ${k} everywhere, new meetings included.` },
    { mode: 'these', label: `Same thing, ${n === 1 ? 'this use' : `these ${n} uses`} only`, off: n === 0,
      says: n === 0 ? 'Check the uses that mean ' + k + ' first.'
        : `Read ${v} as ${k} in the checked ${n === 1 ? 'use' : 'uses'}${rest > 0 ? `; the other ${rest} stay as written` : ''}. New uses come back here for review.` },
    { mode: 'apart', label: 'Both are right', says: `${v} and ${k} are different: nothing changes, and this pair isn't proposed again.` },
  ];
  const current = a.newUses ? '' : a.mode;
  return (
    <div className="corr-choices">
      {opts.map((o) => (
        <button key={o.mode} className={'corr-choice' + (current === o.mode && keep === a.keep ? ' on' : '')} disabled={busy || o.off} onClick={() => onApply(o.mode)}>
          <b>{o.label}</b>
          <span className="dim small">{o.says}</span>
        </button>
      ))}
      {a.mode !== 'review' && (
        <button className="link corr-undo" disabled={busy} onClick={() => onApply('review')} title="Undo the decision: the text reads as written, and the pair is back in To review">
          back to review
        </button>
      )}
    </div>
  );
}

function whoList(s: string): string {
  try {
    const v = JSON.parse(s);
    return Array.isArray(v) ? v.join(', ') : s;
  } catch {
    return s;
  }
}

// Marked highlights a word in text, whole words only.
function Marked({ text, word }: { text: string; word: string }) {
  const re = new RegExp(`\\b(${word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})\\b`, 'gi');
  const parts = text.split(re);
  return <>{parts.map((p, i) => (i % 2 === 1 ? <mark key={i}>{p}</mark> : <Fragment key={i}>{p}</Fragment>))}</>;
}
