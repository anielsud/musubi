import { useEffect, useState } from 'react';
import { api, on } from '../api';
import type { GatewayModel, GroupView, LocalModel, ModelsView, SlotView } from '../types';
import { Spinner } from './common';

// ModelsCard chooses which model does each kind of work: a preferred one,
// a fallback for when it fails, and how hard hosted models think.
export function ModelsCard() {
  const [v, setV] = useState<ModelsView | null>(null);
  const [gw, setGw] = useState<GatewayModel[]>([]);
  const [gwErr, setGwErr] = useState('');
  const [err, setErr] = useState('');
  const load = () => {
    api.models().then(setV, (e) => setErr(String(e)));
    api.gatewayModels().then((m) => { setGw(m ?? []); setGwErr(''); }, (e) => { setGw([]); setGwErr(String(e)); });
  };
  useEffect(() => {
    load();
    const offs = [on('models:changed', load), on('gateway:changed', load)];
    return () => offs.forEach((f) => f());
  }, []);
  if (err) return <section><div className="section-head"><span>Models</span></div><div className="error">{err}</div></section>;
  if (!v) return <Spinner />;
  const installed = v.local.filter((m) => m.have).map((m) => m.name);
  return (
    <>
      <section className="kv">
        <div className="section-head"><span>Models</span></div>
        {gwErr && <p className="dim small">Hosted models need the AI Gateway: connect it above. ({gwErr})</p>}
        {v.groups.map((g) => <GroupEditor key={g.key} g={g} gw={gw} local={installed} />)}
      </section>
      <LocalModels v={v} />
    </>
  );
}

const thinking = [['', 'default (medium for Claude)'], ['low', 'low'], ['medium', 'medium'], ['high', 'high']];

function GroupEditor({ g, gw, local }: { g: GroupView; gw: GatewayModel[]; local: string[] }) {
  const [choices, setChoices] = useState(g.slots.map((s) => s.current));
  const [fallbacks, setFallbacks] = useState(g.slots.map((s) => s.fallback));
  const [effort, setEffort] = useState(g.slots[0]?.effort ?? '');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  useEffect(() => {
    setChoices(g.slots.map((s) => s.current));
    setFallbacks(g.slots.map((s) => s.fallback));
    setEffort(g.slots[0]?.effort ?? '');
  }, [g]);
  const changed = choices.some((c, i) => c !== g.slots[i].current) || fallbacks.some((f, i) => f !== g.slots[i].fallback) || effort !== (g.slots[0]?.effort ?? '');
  const hostedChat = choices.some((c, i) => g.slots[i].kind === 'chat' && c.startsWith('gw:'));
  const save = () => {
    setBusy(true); setMsg('');
    api.setModels(g.key, choices, fallbacks, hostedChat ? effort : '').then(
      () => { setBusy(false); setMsg('Saved; new calls use it.'); },
      (e) => { setBusy(false); setMsg(String(e)); },
    );
  };
  return (
    <div className="model-group">
      <div className="model-group-head"><b>{g.title}</b> <span className="dim small">{g.does}</span></div>
      {g.slots.map((s, i) => (
        <div key={s.sections.join()} className="model-slot">
          {g.slots.length > 1 && <span className="dim small">{s.kind === 'decision' ? 'relevance' : 'suggestions and summaries'}</span>}
          <label className="small">Preferred
            <ModelSelect slot={s} value={choices[i]} gw={gw} local={local} onChange={(c) => setChoices(choices.map((x, j) => (j === i ? c : x)))} />
          </label>
          {s.hasFallback && (
            <label className="small">Fallback
              <ModelSelect slot={s} value={fallbacks[i]} gw={gw} local={local} fallback onChange={(c) => setFallbacks(fallbacks.map((x, j) => (j === i ? c : x)))} />
            </label>
          )}
          <PriceNote id={choices[i]} gw={gw} />
        </div>
      ))}
      {hostedChat && (
        <label className="small">Thinking
          <select value={effort} onChange={(e) => setEffort(e.target.value)}>
            {thinking.map(([k, t]) => <option key={k} value={k}>{t}</option>)}
          </select>
        </label>
      )}
      {(changed || msg) && (
        <div className="row-buttons">
          {changed && <button className="primary" disabled={busy} onClick={save}>{busy ? <><Spinner /> Saving…</> : 'Save'}</button>}
          {msg && <span className="dim small">{msg}</span>}
        </div>
      )}
    </div>
  );
}

function ModelSelect({ slot, value, gw, local, fallback, onChange }: { slot: SlotView; value: string; gw: GatewayModel[]; local: string[]; fallback?: boolean; onChange: (v: string) => void }) {
  const hosted = slot.kind === 'local' ? [] : gw.filter((m) => (slot.kind === 'decision') === (m.kind === 'decision'));
  const claude = hosted.filter((m) => m.provider === 'anthropic');
  const other = hosted.filter((m) => m.provider !== 'anthropic');
  const mac = slot.kind === 'decision' ? [] : local;
  const custom = value === 'custom' || (!fallback && slot.current === 'custom');
  const known = value === '' || value === 'custom' || hosted.some((m) => 'gw:' + m.id === value) || mac.some((n) => 'local:' + n === value);
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)}>
      {(fallback || slot.kind !== 'local') && <option value="">{fallback ? 'none' : 'off'}</option>}
      {custom && <option value="custom">as in the config{slot.customText && !fallback ? `: ${slot.customText}` : ''}</option>}
      {!known && <option value={value}>{value.replace(/^(gw|local):/, '')} (not available now)</option>}
      {claude.length > 0 && <optgroup label="Claude, on the gateway">{claude.map((m) => <option key={m.id} value={'gw:' + m.id}>{m.id}</option>)}</optgroup>}
      {other.length > 0 && <optgroup label="On the gateway">{other.map((m) => <option key={m.id} value={'gw:' + m.id}>{m.id}{m.retires ? ` (retires ${m.retires})` : ''}</option>)}</optgroup>}
      {mac.length > 0 && <optgroup label="On this Mac">{mac.map((n) => <option key={n} value={'local:' + n}>{n}</option>)}</optgroup>}
    </select>
  );
}

function PriceNote({ id, gw }: { id: string; gw: GatewayModel[] }) {
  const m = gw.find((x) => 'gw:' + x.id === id);
  if (!m) return null;
  const soon = m.retires && new Date(m.retires).getTime() - Date.now() < 60 * 86400e3;
  const usd = (n: number) => `$${n < 0.1 ? n.toFixed(3) : n.toFixed(2)}`;
  const parts = [
    m.priceIn ? `${usd(m.priceIn)} in${m.priceOut ? ` · ${usd(m.priceOut)} out` : ''} a million tokens` : '',
    m.retires ? `Cloudflare retires it ${m.retires}` : '',
    m.note ?? '',
  ].filter(Boolean);
  return parts.length ? <div className={'small ' + (soon ? 'warn' : 'dim')}>{parts.join(' · ')}</div> : null;
}

const gb = (n: number) => (n ? `${(n / 1e9).toFixed(1)} GB` : '');

// LocalModels: what Ollama has on this Mac. Required models download by
// themselves; others can be fetched or removed here.
function LocalModels({ v }: { v: ModelsView }) {
  const [progress, setProgress] = useState<Record<string, string>>({});
  const [err, setErr] = useState('');
  useEffect(() => on<{ name: string; status: string; pct: number }>('models:pull', (p) => {
    setProgress((o) => ({ ...o, [p.name]: p.pct ? `${p.status} ${Math.round(p.pct)}%` : p.status }));
  }), []);
  const pulling = new Set(v.pulls ?? []);
  const remove = (m: LocalModel) => {
    if (confirm(`Remove ${m.name} from this Mac? It can be downloaded again.`)) api.removeModel(m.name).catch((e) => setErr(String(e)));
  };
  return (
    <section className="kv">
      <div className="section-head"><span>Models on this Mac</span></div>
      {!v.ollama && <div className="warn small">Ollama isn&apos;t running, so local models can&apos;t be listed or downloaded.</div>}
      {v.local.map((m) => (
        <div key={m.name} className="kv-row">
          <span className="k">{m.name}{m.why ? <span className="dim"> · {m.why}</span> : null}</span>
          <span className="v">
            {pulling.has(m.name) || progress[m.name] && !m.have ? <span className="dim small">{progress[m.name] || 'starting…'}</span>
              : m.have ? <>{gb(m.size)} {!m.required && <button className="small" onClick={() => remove(m)}>Remove</button>}</>
              : <button className="small" disabled={!v.ollama} onClick={() => api.pullModel(m.name).catch((e) => setErr(String(e)))}>Download</button>}
          </span>
        </div>
      ))}
      {err && <div className="error small">{err}</div>}
    </section>
  );
}
