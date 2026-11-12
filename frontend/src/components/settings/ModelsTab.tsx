import { useEffect, useMemo, useState } from 'react';
import { api, on } from '../../api';
import type { ModelCatalog, ModelRow } from '../../types';
import { Spinner } from '../common';
import { bytes, formatName, price, sourceName, tokens } from './shared';

type Col = { key: string; label: string; value: (m: ModelRow) => string | number; show: (m: ModelRow) => React.ReactNode };

const cols: Col[] = [
  { key: 'id', label: 'model', value: (m) => m.id, show: (m) => <span title={m.note}>{m.id}</span> },
  { key: 'source', label: 'source', value: (m) => sourceName(m.source), show: (m) => sourceName(m.source) },
  { key: 'format', label: 'format', value: (m) => (m.formats ?? []).map(formatName).join(', '), show: (m) => (m.formats ?? []).map(formatName).join(', ') },
  { key: 'in', label: '$ / M in', value: (m) => m.priceIn, show: (m) => price(m.priceIn) },
  { key: 'out', label: '$ / M out', value: (m) => m.priceOut, show: (m) => price(m.priceOut) },
  { key: 'context', label: 'context', value: (m) => m.context, show: (m) => tokens(m.context) },
  { key: 'thinking', label: 'thinking', value: (m) => (m.efforts ?? []).join(' '), show: (m) => (m.efforts?.length ? m.efforts.join(' · ') + (m.note?.includes('unverified') ? ' ?' : '') : '') },
  { key: 'retires', label: 'retires', value: (m) => m.retires || '9999', show: (m) => m.retires },
  { key: 'used', label: 'used by', value: (m) => (m.usedBy ?? []).length, show: (m) => (m.usedBy ?? []).join(', ') },
  { key: 'size', label: 'on this Mac', value: (m) => m.size, show: (m) => bytes(m.size) },
];

// ModelsTab lists every model the dataset can use, from every source.
export function ModelsTab() {
  const [cat, setCat] = useState<ModelCatalog | null>(null);
  const [q, setQ] = useState('');
  const [sort, setSort] = useState<{ key: string; desc: boolean }>({ key: 'used', desc: true });
  const [progress, setProgress] = useState<Record<string, string>>({});
  const [name, setName] = useState('');
  const [err, setErr] = useState('');
  const load = () => api.models().then(setCat, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    const offs = [
      on('models:changed', load), on('providers:changed', load), on('gateway:changed', load),
      on<{ name: string; status: string; pct: number }>('models:pull', (p) => setProgress((o) => ({ ...o, [p.name]: p.pct ? `${p.status} ${Math.round(p.pct)}%` : p.status }))),
    ];
    return () => offs.forEach((f) => f());
  }, []);
  const rows = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    const col = cols.find((c) => c.key === sort.key) ?? cols[0];
    return (cat?.rows ?? [])
      .filter((m) => words.every((w) => [m.id, sourceName(m.source), ...(m.formats ?? []).map(formatName), m.kind, ...(m.usedBy ?? [])].join(' ').toLowerCase().includes(w)))
      .sort((a, b) => {
        const x = col.value(a), y = col.value(b);
        const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y));
        return (sort.desc ? -c : c) || a.id.localeCompare(b.id);
      });
  }, [cat, q, sort]);
  if (!cat) return err ? <div className="error">{err}</div> : <Spinner />;
  const pulling = new Set(cat.pulls ?? []);
  const pull = (n: string) => { setErr(''); api.pullModel(n).then(load, (e) => setErr(String(e))); };
  const remove = (n: string) => {
    if (confirm(`Remove ${n} from this Mac? It can be downloaded again.`)) api.removeModel(n).then(load, (e) => setErr(String(e)));
  };
  const head = (c: Col) => (
    <th key={c.key} className="sortable" onClick={() => setSort((s) => ({ key: c.key, desc: s.key === c.key ? !s.desc : c.key === 'used' }))}>
      {c.label}{sort.key === c.key ? (sort.desc ? ' ▾' : ' ▴') : ''}
    </th>
  );
  return (
    <section className="kv">
      <div className="section-head"><span>Models</span><span className="dim small">{rows.length} of {cat.rows?.length ?? 0}</span></div>
      <input className="search" placeholder="Search: a name, a source, a format, what it's used for" value={q} onChange={(e) => setQ(e.target.value)} />
      {Object.entries(cat.errors ?? {}).map(([src, e]) => <div key={src} className="warn small">{sourceName(src)}: {e}</div>)}
      <div className="table-scroll">
        <table className="models-table">
          <thead><tr>{cols.map(head)}<th /></tr></thead>
          <tbody>
            {rows.map((m) => (
              <tr key={m.source + m.id} className={m.usedBy?.length ? 'in-use' : ''}>
                {cols.map((c) => <td key={c.key} className={c.key === 'retires' && m.retires ? 'warn' : ''}>{c.show(m)}</td>)}
                <td>
                  {m.source === 'ollama' && !m.size && (pulling.has(m.id) || progress[m.id]
                    ? <span className="dim small">{progress[m.id] || 'starting…'}</span>
                    : <button className="small" onClick={() => pull(m.id)}>Download</button>)}
                  {m.source === 'ollama' && m.size > 0 && !m.usedBy?.length && <button className="small" onClick={() => remove(m.id)}>Remove</button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="row-buttons">
        <input placeholder="Download a model to this Mac, e.g. qwen3:14b" value={name} onChange={(e) => setName(e.target.value)} />
        <button disabled={!name.trim()} onClick={() => { pull(name.trim()); setName(''); }}>Download</button>
        <button onClick={() => window.runtime?.BrowserOpenURL('https://ollama.com/library')}>Browse Ollama&apos;s library</button>
      </div>
      {Object.entries(progress).filter(([n]) => !(cat.rows ?? []).some((m) => m.id === n && m.size)).map(([n, p]) => <div key={n} className="dim small">{n}: {p}</div>)}
      {err && <div className="error small">{err}</div>}
      <p className="dim small">Prices are per million tokens. Thinking lists the levels a model takes; &quot;?&quot; where its provider doesn&apos;t say.</p>
    </section>
  );
}
