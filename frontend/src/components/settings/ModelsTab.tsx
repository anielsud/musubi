import { useEffect, useMemo, useState } from 'react';
import { api, on } from '../../api';
import type { ModelCatalog, ModelRow, RecStatus } from '../../types';
import { Spinner } from '../common';
import { bytes, formatName, price, sourceName, sourceShort, taskName, tokens } from './shared';

type Sort = 'used' | 'name' | 'price' | 'context' | 'retires';
const sorts: [Sort, string][] = [['used', 'In use first'], ['name', 'Name'], ['price', 'Price'], ['context', 'Context'], ['retires', 'Retires soonest']];

const label = (m: ModelRow) => m.name || m.id;
const key = (m: ModelRow) => m.source + '|' + m.id;

// compare orders two rows by the chosen sort; unknown values go last.
function compare(s: Sort, a: ModelRow, b: ModelRow): number {
  const last = (x: number) => (x ? x : Infinity);
  let c = 0;
  if (s === 'used') c = (b.usedBy?.length ?? 0) - (a.usedBy?.length ?? 0);
  if (s === 'price') c = last(a.priceIn) - last(b.priceIn) || last(a.priceOut) - last(b.priceOut);
  if (s === 'context') c = (b.context || 0) - (a.context || 0);
  if (s === 'retires') c = (a.retires || '9999').localeCompare(b.retires || '9999');
  return (Number.isNaN(c) ? 0 : c) || label(a).localeCompare(label(b));
}

// useWideWindow: the full window, where the list may be a slim table.
function useWideWindow() {
  const q = '(min-width: 900px)';
  const [wide, setWide] = useState(() => window.matchMedia(q).matches);
  useEffect(() => {
    const m = window.matchMedia(q);
    const f = () => setWide(m.matches);
    m.addEventListener('change', f);
    return () => m.removeEventListener('change', f);
  }, []);
  return wide;
}

// ModelsTab lists every model the dataset can use, from every source:
// the AI Gateway, other providers, Ollama and the recorder's ONNX models.
export function ModelsTab() {
  const [cat, setCat] = useState<ModelCatalog | null>(null);
  const [q, setQ] = useState('');
  const [src, setSrc] = useState('all');
  const [sort, setSort] = useState<Sort>('used');
  const [open, setOpen] = useState<string | null>(null);
  const [showRest, setShowRest] = useState(false);
  const [progress, setProgress] = useState<Record<string, string>>({});
  const [recBusy, setRecBusy] = useState('');
  const [recNotes, setRecNotes] = useState<string[]>([]);
  const [name, setName] = useState('');
  const [err, setErr] = useState('');
  const wide = useWideWindow();
  const load = () => api.models().then(setCat, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    let busy = '';
    const offs = [
      on('models:changed', load), on('providers:changed', load), on('gateway:changed', load),
      on<{ name: string; status: string; pct: number }>('models:pull', (p) => setProgress((o) => ({ ...o, [p.name]: p.pct ? `${p.status} ${Math.round(p.pct)}%` : p.status }))),
      // The recorder downloads its models; its status says how far, and
      // a download ending (busy cleared) means the list has changed.
      on<RecStatus>('recorder:status', (st) => {
        const now = st.busy?.startsWith('Downloading') ? st.busy : '';
        if (busy && !now) load();
        busy = now;
        setRecBusy(now);
        setRecNotes((st.health ?? []).filter((h) => h.startsWith('Downloading') || h.startsWith('Restart the recorder to start using')));
      }),
    ];
    return () => offs.forEach((f) => f());
  }, []);
  const all = cat?.rows ?? [];
  const sources = useMemo(() => {
    const order = (s: string) => (s === 'gateway' ? 0 : s === 'ollama' ? 2 : s === 'onnx' ? 3 : 1);
    const seen = [...new Set([...all.map((m) => m.source), ...Object.keys(cat?.errors ?? {})])];
    return seen.sort((a, b) => order(a) - order(b) || a.localeCompare(b));
  }, [cat]);
  const rows = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    return all
      .filter((m) => src === 'all' || m.source === src)
      .filter((m) => words.every((w) => [m.id, m.name, sourceName(m.source), sourceShort(m.source), ...(m.formats ?? []).map(formatName), m.kind, taskName(m.task), ...(m.usedBy ?? [])].join(' ').toLowerCase().includes(w)))
      .sort((a, b) => compare(sort, a, b));
  }, [cat, q, src, sort]);
  if (!cat) return err ? <div className="error">{err}</div> : <Spinner />;
  const pulling = new Set(cat.pulls ?? []);
  const pull = (n: string) => { setErr(''); api.pullModel(n).then(load, (e) => setErr(String(e))); };
  const remove = (n: string) => {
    if (confirm(`Remove ${n} from this Mac? It can be downloaded again.`)) api.removeModel(n).then(load, (e) => setErr(String(e)));
  };
  // The recorder marks itself busy before the call returns; its state says
  // so even if its feed isn't followed. A model already there just reloads.
  const fetchRec = (id: string) => {
    setErr('');
    api.downloadRecorderModel(id).then(() => api.recorderState().then((v) => {
      const busy = v.status?.busy ?? '';
      setRecBusy(busy.startsWith('Downloading') ? busy : '');
      load();
    }), (e) => setErr(String(e)));
  };
  const actions = { pulling, progress, recBusy, pull, remove, fetchRec };

  const inUse = rows.filter((m) => m.usedBy?.length);
  const rec = rows.filter((m) => m.source === 'onnx' && !m.usedBy?.length);
  const rest = rows.filter((m) => m.source !== 'onnx' && !m.usedBy?.length);
  const filtering = q.trim() !== '' || src !== 'all';
  const list = (rs: ModelRow[]) => (wide
    ? <ModelTable rows={rs} open={open} setOpen={setOpen} actions={actions} />
    : <div className="mlist">{rs.map((m) => <ModelLine key={key(m)} m={m} open={open === key(m)} onToggle={() => setOpen(open === key(m) ? null : key(m))} actions={actions} />)}</div>);
  return (
    <section className="kv models-tab">
      <div className="section-head"><span>Models</span><span className="dim small">{rows.length} of {all.length}</span></div>
      <input className="search" placeholder="Search: a name, a source, what it's used for" value={q} onChange={(e) => setQ(e.target.value)} />
      <div className="model-filters">
        <div className="seg-buttons">
          {['all', ...sources].map((s) => (
            <button key={s} className={src === s ? 'on' : ''} onClick={() => setSrc(s)} title={s === 'all' ? 'Every source' : sourceName(s)}>{s === 'all' ? 'All' : sourceShort(s)}</button>
          ))}
        </div>
        <select className="sort" value={sort} onChange={(e) => setSort(e.target.value as Sort)} title="Sort">
          {sorts.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </select>
      </div>
      {Object.entries(cat.errors ?? {}).filter(([s]) => src === 'all' || s === src).map(([s, e]) => <div key={s} className="warn small">{sourceName(s)}: {e}</div>)}
      {recBusy && <div className="dim small"><Spinner /> {recBusy}</div>}
      {recNotes.map((n) => <div key={n} className={n.startsWith('Restart') ? 'dim small' : 'warn small'}>{n}</div>)}

      {inUse.length > 0 && <><div className="mhead">In use</div>{list(inUse)}</>}
      {rec.length > 0 && <><div className="mhead">Recording (this Mac)</div>{list(rec)}</>}
      {rest.length > 0 && (
        <>
          <button className={'mhead mhead-toggle' + (showRest || filtering ? ' open' : '')} onClick={() => setShowRest((v) => !v)} aria-expanded={showRest || filtering}>
            Everything else ({rest.length})
          </button>
          {(showRest || filtering) && list(rest)}
        </>
      )}
      {rows.length === 0 && <div className="dim small">No model matches.</div>}

      <div className="row-buttons">
        <input placeholder="Download a model to this Mac, e.g. qwen3:14b" value={name} onChange={(e) => setName(e.target.value)} />
        <button disabled={!name.trim()} onClick={() => { pull(name.trim()); setName(''); }}>Download</button>
        <button onClick={() => window.runtime?.BrowserOpenURL('https://ollama.com/library')}>Browse Ollama&apos;s library</button>
      </div>
      {Object.entries(progress).filter(([n]) => !all.some((m) => m.id === n && m.size)).map(([n, p]) => <div key={n} className="dim small">{n}: {p}</div>)}
      {err && <div className="error small">{err}</div>}
      <p className="dim small">Prices are per million tokens, in / out. Thinking lists the levels a model takes; &quot;?&quot; where its provider doesn&apos;t say. ONNX models run in the recorder; it downloads them into its own folder.</p>
    </section>
  );
}

interface Actions {
  pulling: Set<string>;
  progress: Record<string, string>;
  recBusy: string;
  pull: (n: string) => void;
  remove: (n: string) => void;
  fetchRec: (id: string) => void;
}

const thinking = (m: ModelRow) => (m.efforts?.length ? 'thinking ' + m.efforts.join(' · ') + (m.note?.includes('unverified') ? ' ?' : '') : '');

// detail is a row's second line: price, context and thinking, or for a
// model on this Mac what it does and its size.
function detail(m: ModelRow): string {
  if (m.source === 'onnx') return [taskName(m.task), m.missing ? '' : bytes(m.size)].filter(Boolean).join(' · ');
  if (m.source === 'ollama') return [m.size ? bytes(m.size) : 'not downloaded', 'free'].join(' · ');
  const p = m.kind === 'decision'
    ? (m.priceIn ? `${price(m.priceIn)} / M` : '')
    : m.priceIn || m.priceOut ? `${price(m.priceIn) || '?'} / ${price(m.priceOut) || '?'}` : '';
  return [p || 'price not known', m.context ? `${tokens(m.context)} context` : '', m.kind === 'decision' ? 'decision' : thinking(m)].filter(Boolean).join(' · ');
}

// Badge: the source chip, then a warning if any.
function Badges({ m }: { m: ModelRow }) {
  return (
    <>
      <span className={`src-chip src-${m.source === 'gateway' || m.source === 'ollama' || m.source === 'onnx' ? m.source : 'custom'}`}>{sourceShort(m.source)}</span>
      {m.source === 'onnx' && (m.missing ? <span className="warn-badge small">missing</span> : <span className="ok-text small">✓</span>)}
      {m.retires && <span className="warn-badge small">retires {m.retires}</span>}
      {m.note === 'not available now' && <span className="warn-badge small">not available now</span>}
    </>
  );
}

function ModelLine({ m, open, onToggle, actions }: { m: ModelRow; open: boolean; onToggle: () => void; actions: Actions }) {
  return (
    <div className={`mrow${open ? ' open' : ''}${m.usedBy?.length ? ' in-use' : ''}`}>
      <div className="mrow-top">
        <button className="mrow-main" onClick={onToggle} aria-expanded={open}>
          <span className="mrow-1"><span className="mname">{label(m)}</span><Badges m={m} /></span>
          <span className="mrow-2 dim small">{detail(m)}</span>
        </button>
        {!open && <QuickAction m={m} actions={actions} />}
      </div>
      {open && <Details m={m} actions={actions} />}
    </div>
  );
}

// QuickAction: Download on a missing recorder model's line, without
// opening it.
function QuickAction({ m, actions }: { m: ModelRow; actions: Actions }) {
  if (m.source !== 'onnx' || !m.missing) return null;
  if (actions.recBusy) return null;
  return <button className="small quick" onClick={(e) => { e.stopPropagation(); actions.fetchRec(m.id); }}>Download</button>;
}

function ModelTable({ rows, open, setOpen, actions }: { rows: ModelRow[]; open: string | null; setOpen: (k: string | null) => void; actions: Actions }) {
  return (
    <table className="models-table">
      <colgroup><col className="c-model" /><col className="c-num" /><col className="c-num" /><col className="c-num" /><col className="c-what" /><col className="c-used" /></colgroup>
      <thead><tr><th>model</th><th>$ / M in</th><th>$ / M out</th><th>context</th><th>thinking · task</th><th>used by</th></tr></thead>
      <tbody>
        {rows.flatMap((m) => {
          const k = key(m);
          const local = m.source === 'onnx' || m.source === 'ollama';
          const out = [
            <tr key={k} className={`${m.usedBy?.length ? 'in-use' : ''} clickable`} onClick={() => setOpen(open === k ? null : k)}>
              <td><span className="mrow-1"><span className="mname">{label(m)}</span><Badges m={m} />{open !== k && <QuickAction m={m} actions={actions} />}</span></td>
              <td>{local ? 'free' : price(m.priceIn)}</td>
              <td>{local ? '' : price(m.priceOut)}</td>
              <td>{tokens(m.context)}</td>
              <td>{m.source === 'onnx' ? [taskName(m.task), m.missing ? '' : bytes(m.size)].filter(Boolean).join(' · ') : m.source === 'ollama' ? bytes(m.size) : m.kind === 'decision' ? 'decision' : (m.efforts ?? []).join(' · ')}</td>
              <td>{(m.usedBy ?? []).join(', ')}</td>
            </tr>,
          ];
          if (open === k) out.push(<tr key={k + '-more'} className="more"><td colSpan={6}><Details m={m} actions={actions} /></td></tr>);
          return out;
        })}
      </tbody>
    </table>
  );
}

function Details({ m, actions }: { m: ModelRow; actions: Actions }) {
  const { pulling, progress, recBusy, pull, remove, fetchRec } = actions;
  const kv = (k: string, v: React.ReactNode) => (v ? <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div> : null);
  return (
    <div className="mrow-more">
      {kv('id', m.name && m.name !== m.id ? m.id : '')}
      {kv('source', sourceName(m.source))}
      {kv('format', (m.formats ?? []).map(formatName).join(', '))}
      {m.source === 'onnx' && kv('does', taskName(m.task))}
      {kv('price', m.source === 'onnx' || m.source === 'ollama' ? 'free (this Mac)' : m.priceIn ? `${price(m.priceIn)} in${m.priceOut ? ` · ${price(m.priceOut)} out` : ''} per million tokens` : '')}
      {kv('context', m.context ? `${tokens(m.context)} tokens` : '')}
      {kv('thinking', m.efforts?.length ? m.efforts.join(' · ') + (m.defaultEffort ? ` (default ${m.defaultEffort})` : '') : '')}
      {kv('size', m.size ? bytes(m.size) : m.missing || (m.source === 'ollama' && !m.size) ? 'not downloaded' : '')}
      {kv('retires', m.retires)}
      {kv('used by', (m.usedBy ?? []).join(', ') || <span className="dim">nothing</span>)}
      {kv('note', m.note)}
      {m.source === 'onnx' && m.restart && <div className="dim small">The recorder loads it when it starts: a download or a new choice applies after a recorder restart.</div>}
      <div className="row-buttons">
        {m.source === 'ollama' && !m.size && (pulling.has(m.id) || progress[m.id]
          ? <span className="dim small">{progress[m.id] || 'starting…'}</span>
          : <button className="small" onClick={() => pull(m.id)}>Download</button>)}
        {m.source === 'ollama' && m.size > 0 && !m.usedBy?.length && <button className="small" onClick={() => remove(m.id)}>Remove</button>}
        {m.source === 'onnx' && m.missing && (recBusy
          ? <span className="dim small">{recBusy}</span>
          : <button className="small" onClick={() => fetchRec(m.id)}>Download</button>)}
      </div>
    </div>
  );
}
