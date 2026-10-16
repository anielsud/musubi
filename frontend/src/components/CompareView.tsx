import { useEffect, useState } from 'react';
import { api } from '../api';
import type { CompareRow, CompareTopic, Comparison, DatasetView } from '../types';
import { Empty, Spinner } from './common';
import { clusterColor } from './TopicViews';

const statusInfo: Record<string, [string, string]> = {
  same: ['same', 'One topic on each side, mostly the same material'],
  split: ['split', 'Spread over several topics on the other side'],
  merged: ['merged', 'The other side holds it together with other topics of this side'],
  partial: ['partial', 'A weaker match'],
  only: ['only here', 'Nothing like it on the other side'],
};
const statusOrder = ['split', 'merged', 'partial', 'only', 'same'];

// CompareView lines this window's topics up with another dataset's: which
// subjects match, which one side splits or merges, and what only one side
// has.
export function CompareView({ scope }: { scope: string }) {
  const [sets, setSets] = useState<DatasetView[]>([]);
  const [other, setOther] = useState('');
  const [reverse, setReverse] = useState(false);
  const [cmp, setCmp] = useState<Comparison | null | undefined>(undefined);
  const [only, setOnly] = useState<string | null>(null);
  const [err, setErr] = useState('');

  useEffect(() => {
    api.datasets().then((d) => {
      const ds = d ?? [];
      setSets(ds);
      const first = ds.find((x) => x.available && !x.current);
      if (first) setOther((o) => o || first.key);
    }).catch((e) => setErr(String(e)));
  }, []);
  useEffect(() => {
    if (!other) return;
    setCmp(undefined);
    setErr('');
    api.compareWith(other, scope, reverse).then(setCmp, (e) => { setErr(String(e)); setCmp(null); });
  }, [other, scope, reverse]);

  const here = sets.find((d) => d.current)?.name ?? 'This dataset';
  const there = sets.find((d) => d.key === other)?.name ?? 'other';
  const [a, b] = reverse ? [there, here] : [here, there];
  const rows = (cmp?.rows ?? []).filter((r) => !only || r.status === only)
    .sort((x, y) => statusOrder.indexOf(x.status) - statusOrder.indexOf(y.status) || y.a.statements - x.a.statements);

  return (
    <div className="compare">
      <div className="cmp-pick">
        <select value={other} onChange={(e) => setOther(e.target.value)}>
          {sets.filter((d) => !d.current).map((d) => <option key={d.key} value={d.key} disabled={!d.available}>{d.name}{d.available ? '' : ' (not built)'}</option>)}
        </select>
        <button className="small-btn" title="Swap which side is lined up against which" onClick={() => setReverse(!reverse)}>⇄ swap</button>
        <button className="small-btn" title="Open the other dataset in its own window" disabled={!other} onClick={() => api.openDatasetWindow(other).catch((e) => setErr(String(e)))}>⧉ open</button>
      </div>
      <div className="cmp-sides"><span className="cmp-a">{a}</span><span className="dim">→</span><span className="cmp-b">{b}</span></div>
      {err && <div className="error small">{err}</div>}
      {cmp === undefined && other && <Spinner />}
      {cmp && (
        <>
          <div className="dim small">
            {cmp.mode === 'statements'
              ? `Matched by shared statements: ${Math.round(cmp.shared * 100)}% of the left side's statements are also on the right. Percentages are the share of the left topic found in the right one, then the share of the right one that came from the left.`
              : 'Matched by wording: the two sides were extracted separately, so no statements are shared. Percentages are how alike the topics read.'}
            {' '}{cmp.sizes.a} topics on the left, {cmp.sizes.b} on the right.
          </div>
          <div className="cmp-filter">
            <button className={only === null ? 'on' : ''} onClick={() => setOnly(null)}>all {(cmp.rows ?? []).length}</button>
            {statusOrder.filter((s) => cmp.counts[s]).map((s) => (
              <button key={s} className={(only === s ? 'on ' : '') + 'cs-' + s} title={statusInfo[s][1]} onClick={() => setOnly(only === s ? null : s)}>
                {statusInfo[s][0]} {cmp.counts[s]}
              </button>
            ))}
          </div>
          <div className="cmp-rows">
            {rows.map((r) => <Row key={r.a.id} r={r} mode={cmp.mode} />)}
            {rows.length === 0 && <Empty>None.</Empty>}
          </div>
          {(cmp.onlyB?.length ?? 0) > 0 && (only === null || only === 'only') && (
            <>
              <div className="section-head"><span>Only on the right ({cmp.onlyB!.length})</span></div>
              <div className="chips">{cmp.onlyB!.map((t) => <span key={t.id} className="chip"><Dot t={t} />{t.label}</span>)}</div>
            </>
          )}
        </>
      )}
      {!other && sets.length > 0 && <Empty>No other dataset is built yet.</Empty>}
    </div>
  );
}

function Dot({ t }: { t: CompareTopic }) {
  return <span className="cl-dot" style={{ background: clusterColor(t.cluster) }} />;
}

function Row({ r, mode }: { r: CompareRow; mode: string }) {
  const pct = (x: number) => Math.round(x * 100) + '%';
  return (
    <div className="cmp-row">
      <div className="cmp-left"><Dot t={r.a} />{r.a.label}<span className="dim"> {r.a.statements}</span></div>
      <div className="cmp-status"><span className={'cmp-pill cs-' + r.status} title={statusInfo[r.status][1]}>{statusInfo[r.status][0]}</span></div>
      <div className="cmp-right">
        {(r.matches ?? []).map((m) => (
          <div key={m.b.id} className="cmp-match">
            <span className="cmp-bar"><span style={{ width: pct(m.coverA) }} /></span>
            <Dot t={m.b} />{m.b.label}
            <span className="dim"> {mode === 'statements' ? `${pct(m.coverA)} · ${pct(m.coverB)}` : pct(m.coverA)}</span>
          </div>
        ))}
        {r.with && r.with.length > 0 && <div className="dim small">together with {r.with.slice(0, 4).join(' · ')}{r.with.length > 4 ? ` and ${r.with.length - 4} more` : ''}</div>}
      </div>
    </div>
  );
}
