import { useEffect, useState } from 'react';
import { api } from '../api';
import type { AliasView } from '../types';
import { Empty, Spinner } from './common';

// CorrectionsView: aliases the corpus proposes (a misheard or misspelled
// name or term), each with what it would touch. Confirming applies it when
// reading (Ask, Prep, Live); nothing is rewritten, and it can be undone.
// An alias that would link many threads for the first time is the one to
// look at twice: it may be two different people or things.
export function CorrectionsView() {
  const [status, setStatus] = useState('proposed');
  const [list, setList] = useState<AliasView[] | null>(null);
  const [err, setErr] = useState('');
  const load = () => {
    setList(null);
    api.aliases(status).then((x) => setList(x ?? []), (e) => setErr(String(e)));
  };
  useEffect(load, [status]);
  const decide = (a: AliasView, confirm: boolean) =>
    api.decideAlias(a.id, confirm).then(() => setList((l) => (l ?? []).filter((x) => x.id !== a.id)), (e) => setErr(String(e)));
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
      {(list ?? []).map((a) => (
        <article key={a.id} className={'alias' + (a.newLinks > 3 ? ' risky' : '')}>
          <div className="alias-head">
            <b>{a.variant}</b> <span className="dim">→</span> <b>{a.keep}</b>
            <span className="dim small"> · {a.statements} statements in {a.items} meetings · {a.threads} threads</span>
            {a.newLinks > 0 && <span className={'status ' + (a.newLinks > 3 ? 'st-contested' : 'st-open')} title="threads where the kept form isn't mentioned yet">{a.newLinks} new links</span>}
            {status === 'proposed' && (
              <span className="alias-actions">
                <button className="chip" onClick={() => decide(a, true)}>✓ same</button>
                <button className="chip" onClick={() => decide(a, false)}>✗ different</button>
              </span>
            )}
          </div>
          {(a.sample ?? []).slice(0, 2).map((s, i) => <div key={i} className="alias-sample">{s}</div>)}
        </article>
      ))}
    </div>
  );
}
