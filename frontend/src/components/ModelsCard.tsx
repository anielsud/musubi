import { useEffect, useState } from 'react';
import { api, on } from '../api';
import type { GatewayModel, GroupView, LocalModel, ModelsView, ProviderView, SlotView } from '../types';
import { Spinner } from './common';

// What the model menus can offer: the gateway's models, each custom
// provider's, and the ones on this Mac.
interface Catalog {
  gw: GatewayModel[];
  providers: { name: string; models: string[] }[];
  local: string[];
}

function useCatalog(v: ModelsView | null): { cat: Catalog; gwErr: string } {
  const [gw, setGw] = useState<GatewayModel[]>([]);
  const [gwErr, setGwErr] = useState('');
  const [providers, setProviders] = useState<{ name: string; models: string[] }[]>([]);
  useEffect(() => {
    api.gatewayModels().then((m) => { setGw(m ?? []); setGwErr(''); }, (e) => { setGw([]); setGwErr(String(e)); });
    api.providers().then(async (ps: ProviderView[] | null) => {
      const out = [];
      for (const p of ps ?? []) {
        const models = await api.providerModels(p.name).catch(() => [] as string[]);
        out.push({ name: p.name, models: models ?? [] });
      }
      setProviders(out);
    });
  }, [v]);
  return { cat: { gw, providers, local: (v?.local ?? []).filter((m) => m.have).map((m) => m.name) }, gwErr };
}

// ProcessingCard chooses which model does each kind of work: a preferred
// one and a fallback for when it fails, each with its thinking level.
export function ProcessingCard() {
  const [v, setV] = useState<ModelsView | null>(null);
  const [err, setErr] = useState('');
  const [applying, setApplying] = useState(false);
  const load = () => api.models().then(setV, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    const offs = [on('models:changed', load), on('gateway:changed', load), on('providers:changed', load)];
    return () => offs.forEach((f) => f());
  }, []);
  const { cat, gwErr } = useCatalog(v);
  if (err) return <section><div className="error">{err}</div></section>;
  if (!v) return <Spinner />;
  const recommended = () => {
    if (!confirm('Use the recommended models for every group? Claude Haiku 5.5 through the AI Gateway, GLM on Baseten as the fallback, local models for Live. Your other settings stay.')) return;
    setApplying(true);
    api.applyRecommended().then(() => setApplying(false), (e) => { setApplying(false); setErr(String(e)); });
  };
  return (
    <section className="kv">
      <div className="section-head">
        <span>Models</span>
        <button className="small" disabled={applying} onClick={recommended}>{applying ? <><Spinner /> Applying…</> : 'Use recommended'}</button>
      </div>
      <p className="dim small">
        Costs are typical tokens in and out per meeting hour (or per question), measured on real meetings, times the model&apos;s price.
        They leave out thinking, which some models spend many times more tokens on.
      </p>
      {gwErr && <p className="dim small">Gateway models need the AI Gateway: connect it under AI providers.</p>}
      {v.groups.map((g) => <GroupRow key={g.key} g={g} cat={cat} />)}
    </section>
  );
}

const k = (n: number) => (n >= 1000 ? `${Math.round(n / 1000)}K` : String(n));
const usd = (n: number) => (n < 0.01 ? '<$0.01' : `$${n.toFixed(2)}`);

function GroupRow({ g, cat }: { g: GroupView; cat: Catalog }) {
  const init = () => ({
    choices: g.slots.map((s) => s.current),
    fallbacks: g.slots.map((s) => s.fallback),
    efforts: g.slots.map((s) => s.effort),
    fbEfforts: g.slots.map((s) => s.fallbackEffort),
  });
  const [st, setSt] = useState(init);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  useEffect(() => { setSt(init()); }, [g]);
  const set = (key: keyof ReturnType<typeof init>, i: number, val: string) => setSt((o) => ({ ...o, [key]: o[key].map((x, j) => (j === i ? val : x)) }));
  const orig = init();
  const changed = (Object.keys(st) as (keyof typeof st)[]).some((key) => st[key].some((x, i) => x !== orig[key][i]));
  const save = () => {
    setBusy(true); setMsg('');
    api.setModels(g.key, st.choices, st.fallbacks, st.efforts, st.fbEfforts).then(
      () => { setBusy(false); setMsg('Saved; new calls use it.'); },
      (e) => { setBusy(false); setMsg(String(e)); },
    );
  };
  return (
    <div className="model-group">
      <div className="model-group-head">
        <b>{g.title}</b>
        <span className="dim small">{g.does}</span>
        {g.rate && <RateLine rate={g.rate} ids={[st.choices[0], st.fallbacks[0]]} cat={cat} />}
      </div>
      {g.slots.map((s, i) => (
        <div key={s.sections.join()} className="slot-row">
          {g.slots.length > 1 && <span className="dim small slot-label">{s.kind === 'decision' ? 'relevance' : 'suggestions'}</span>}
          <ModelPick label="Preferred" slot={s} value={st.choices[i]} effort={st.efforts[i]} text={s.customText} cat={cat}
            onModel={(c) => set('choices', i, c)} onEffort={(e) => set('efforts', i, e)} />
          {s.hasFallback && (
            <ModelPick label="Fallback" slot={s} fallback value={st.fallbacks[i]} effort={st.fbEfforts[i]} text={s.fallbackText} cat={cat}
              onModel={(c) => set('fallbacks', i, c)} onEffort={(e) => set('fbEfforts', i, e)} />
          )}
        </div>
      ))}
      {(changed || msg) && (
        <div className="row-buttons">
          {changed && <button className="primary" disabled={busy} onClick={save}>{busy ? <><Spinner /> Saving…</> : 'Save'}</button>}
          {changed && <button onClick={() => { setSt(init()); setMsg(''); }}>Undo</button>}
          {msg && <span className="dim small">{msg}</span>}
        </div>
      )}
    </div>
  );
}

function RateLine({ rate, ids, cat }: { rate: { in: number; out: number; per: string }; ids: string[]; cat: Catalog }) {
  const cost = (id: string) => {
    if (id.startsWith('local:')) return 'free (this Mac)';
    const m = cat.gw.find((x) => 'gw:' + x.id === id);
    if (!m || !m.priceIn) return null;
    return usd((rate.in * m.priceIn + rate.out * (m.priceOut || 0)) / 1e6);
  };
  const [p, f] = ids.map((id) => (id ? cost(id) : null));
  return (
    <span className="dim small">
      ≈ {k(rate.in)} in{rate.out ? ` · ${k(rate.out)} out` : ''} tokens per {rate.per}
      {p && <> → {p}</>}{f && <> (fallback {f})</>}
    </span>
  );
}

function ModelPick({ label, slot, value, effort, text, cat, fallback, onModel, onEffort }: {
  label: string; slot: SlotView; value: string; effort: string; text?: string; cat: Catalog; fallback?: boolean;
  onModel: (v: string) => void; onEffort: (v: string) => void;
}) {
  const hosted = slot.kind === 'local' ? [] : cat.gw.filter((m) => (slot.kind === 'decision') === (m.kind === 'decision'));
  const claude = hosted.filter((m) => m.provider === 'anthropic');
  const other = hosted.filter((m) => m.provider !== 'anthropic');
  const mac = slot.kind === 'decision' ? [] : cat.local;
  const provs = slot.kind === 'chat' ? cat.providers : [];
  const options = new Set<string>(['', 'custom', ...hosted.map((m) => 'gw:' + m.id), ...mac.map((n) => 'local:' + n),
    ...provs.flatMap((p) => p.models.map((m) => `via:${p.name}:${m}`))]);
  // Thinking: the model's own levels; none for local and decision models.
  const gm = cat.gw.find((m) => 'gw:' + m.id === value);
  let efforts: string[] = [];
  let note = '';
  if (gm) efforts = gm.efforts ?? [];
  else if (value.startsWith('via:') || (value === 'custom' && slot.kind === 'chat')) { efforts = ['low', 'medium', 'high']; note = ' (unverified)'; }
  const def = gm?.defaultEffort ? `default (${gm.defaultEffort})` : 'default';
  return (
    <div className="pick">
      <span className="dim small pick-label">{label}</span>
      <select value={value} onChange={(e) => onModel(e.target.value)}>
        {(fallback || slot.kind !== 'local') && <option value="">{fallback ? 'none' : 'off'}</option>}
        {value === 'custom' && <option value="custom">{text ? `as set: ${text}` : 'as set in the config file'}</option>}
        {!options.has(value) && <option value={value}>{value.replace(/^(gw|local|via):/, '')} (not available now)</option>}
        {claude.length > 0 && <optgroup label="Claude, on the gateway">{claude.map((m) => <option key={m.id} value={'gw:' + m.id}>{m.id}</option>)}</optgroup>}
        {other.length > 0 && <optgroup label="On the gateway">{other.map((m) => <option key={m.id} value={'gw:' + m.id}>{m.id}{m.retires ? ` (retires ${m.retires})` : ''}</option>)}</optgroup>}
        {provs.filter((p) => p.models.length).map((p) => (
          <optgroup key={p.name} label={`On ${p.name}`}>{p.models.map((m) => <option key={m} value={`via:${p.name}:${m}`}>{m}</option>)}</optgroup>
        ))}
        {mac.length > 0 && <optgroup label="On this Mac">{mac.map((n) => <option key={n} value={'local:' + n}>{n}</option>)}</optgroup>}
      </select>
      <select className="effort" value={efforts.length ? effort : ''} disabled={!efforts.length} title={efforts.length ? 'Thinking' + note : 'This model has no thinking setting'} onChange={(e) => onEffort(e.target.value)}>
        <option value="">{efforts.length ? def : 'no thinking setting'}</option>
        {efforts.map((e) => <option key={e} value={e}>{e}{note}</option>)}
        {effort && efforts.length > 0 && !efforts.includes(effort) && <option value={effort}>{effort} (not listed)</option>}
      </select>
    </div>
  );
}

const gb = (n: number) => (n ? `${(n / 1e9).toFixed(1)} GB` : '');

// LocalCard: Ollama on this Mac. Required models download by themselves;
// others can be fetched by name or removed.
export function LocalCard() {
  const [v, setV] = useState<ModelsView | null>(null);
  const [progress, setProgress] = useState<Record<string, string>>({});
  const [name, setName] = useState('');
  const [err, setErr] = useState('');
  const load = () => api.models().then(setV, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    const offs = [
      on('models:changed', load),
      on<{ name: string; status: string; pct: number }>('models:pull', (p) => setProgress((o) => ({ ...o, [p.name]: p.pct ? `${p.status} ${Math.round(p.pct)}%` : p.status }))),
    ];
    return () => offs.forEach((f) => f());
  }, []);
  if (!v) return err ? <div className="error small">{err}</div> : <Spinner />;
  const pulling = new Set(v.pulls ?? []);
  const pull = (n: string) => { setErr(''); api.pullModel(n).then(load, (e) => setErr(String(e))); };
  const remove = (m: LocalModel) => {
    if (confirm(`Remove ${m.name} from this Mac? It can be downloaded again.`)) api.removeModel(m.name).catch((e) => setErr(String(e)));
  };
  const library = () => window.runtime?.BrowserOpenURL('https://ollama.com/library');
  return (
    <section className="kv">
      <div className="section-head"><span>This Mac (Ollama)</span><span className="dim small">{v.ollama ? '✓ running' : '✗ not running'}</span></div>
      {!v.ollama && (
        <p className="warn small">
          Ollama isn&apos;t running, so local models can&apos;t be listed or downloaded. Install it from ollama.com/download
          (or <code>brew install ollama</code>), then reopen Musubi: it starts Ollama itself.
        </p>
      )}
      {v.local.map((m) => (
        <div key={m.name} className="kv-row">
          <span className="k">{m.name}{m.required ? ' (needed)' : ''}{m.why ? <span className="dim"> · {m.why}</span> : null}</span>
          <span className="v">
            {pulling.has(m.name) || (progress[m.name] && !m.have) ? <span className="dim small">{progress[m.name] || 'starting…'}</span>
              : m.have ? <>{gb(m.size)} {!m.required && <button className="small" onClick={() => remove(m)}>Remove</button>}</>
              : <button className="small" disabled={!v.ollama} onClick={() => pull(m.name)}>Download</button>}
          </span>
        </div>
      ))}
      <div className="row-buttons">
        <input placeholder="another model, e.g. qwen3:14b" value={name} onChange={(e) => setName(e.target.value)} />
        <button disabled={!v.ollama || !name.trim()} onClick={() => { pull(name.trim()); setName(''); }}>Download</button>
        <button className="small" onClick={library}>Browse Ollama&apos;s library</button>
      </div>
      {err && <div className="error small">{err}</div>}
    </section>
  );
}
