import { useEffect, useState } from 'react';
import { api, on } from '../../api';
import type { GroupView, ModelRow, RecModelField, RecordingModels, SlotView } from '../../types';
import { Spinner } from '../common';
import { formatName, sourceName, tokens, usd } from './shared';

// An option in a model menu: "<source>|<format>|<model>", as the app writes
// it; a model a provider serves in both formats is offered in each.
interface Option { value: string; label: string; row: ModelRow; format: string }

function options(rows: ModelRow[], kind: SlotView['kind']): Option[] {
  const out: Option[] = [];
  for (const r of rows) {
    if (r.note === 'not available now') continue;
    if (kind === 'decision' ? r.kind !== 'decision' : r.kind !== 'chat') continue;
    if (kind === 'local' && r.source !== 'ollama') continue;
    for (const f of r.formats ?? []) {
      const both = (r.formats ?? []).length > 1;
      out.push({ value: `${r.source}|${f}|${r.id}`, label: both ? `${r.id} · ${formatName(f)}` : r.id, row: r, format: f });
    }
  }
  return out;
}

// ProcessingTab chooses which model does each kind of work: a preferred
// one and a fallback for when it fails, each with its thinking level.
export function ProcessingTab() {
  const [groups, setGroups] = useState<GroupView[] | null>(null);
  const [rows, setRows] = useState<ModelRow[]>([]);
  const [err, setErr] = useState('');
  const [applying, setApplying] = useState(false);
  const load = () => {
    api.groups().then((g) => setGroups(g ?? []), (e) => setErr(String(e)));
    api.models().then((c) => setRows(c.rows ?? []));
  };
  useEffect(() => {
    load();
    const offs = [on('models:changed', load), on('gateway:changed', load), on('providers:changed', load)];
    return () => offs.forEach((f) => f());
  }, []);
  if (err) return <div className="error">{err}</div>;
  if (!groups) return <Spinner />;
  const recommended = () => {
    if (!confirm('Use the recommended models for every group? Claude Haiku 5.5 through the AI Gateway, GLM on Baseten as the fallback, local models for Live. Your other settings stay.')) return;
    setApplying(true);
    api.applyRecommended().then(() => setApplying(false), (e) => { setApplying(false); setErr(String(e)); });
  };
  return (
    <section className="kv">
      <div className="section-head">
        <span>What does each job</span>
        <button disabled={applying} onClick={recommended}>{applying ? <><Spinner /> Applying…</> : 'Use recommended'}</button>
      </div>
      <p className="dim small">
        Costs are typical tokens in and out per meeting hour (or per question), measured on real meetings, times the model&apos;s price.
        They leave out thinking, which some models spend many times more tokens on.
      </p>
      {groups.map((g) => <GroupRow key={g.key} g={g} rows={rows} />)}
      <RecordingGroup />
    </section>
  );
}

function GroupRow({ g, rows }: { g: GroupView; rows: ModelRow[] }) {
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
  const rowFor = (ch: string) => {
    const [src, , id] = ch.split('|');
    return rows.find((r) => r.source === src && r.id === id);
  };
  return (
    <div className="model-group">
      <div className="model-group-head">
        <b>{g.title}</b>
        <span className="dim small">{g.does}</span>
        {g.rate && <CostLine rate={g.rate} rows={[rowFor(st.choices[0]), rowFor(st.fallbacks[0])]} local={st.choices[0]?.startsWith('ollama|')} />}
      </div>
      {g.slots.map((s, i) => (
        <div key={s.sections.join()} className="slot-row">
          {g.slots.length > 1 && <span className="dim small slot-label">{s.kind === 'decision' ? 'relevance' : 'suggestions and summaries'}</span>}
          <Pick label="Preferred" slot={s} value={st.choices[i]} effort={st.efforts[i]} text={s.customText} rows={rows}
            onModel={(c) => set('choices', i, c)} onEffort={(e) => set('efforts', i, e)} />
          {s.hasFallback && (
            <Pick label="Fallback" slot={s} fallback value={st.fallbacks[i]} effort={st.fbEfforts[i]} text={s.fallbackText} rows={rows}
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

function CostLine({ rate, rows, local }: { rate: { in: number; out: number; per: string }; rows: (ModelRow | undefined)[]; local: boolean }) {
  const cost = (r?: ModelRow) => (r?.priceIn ? usd((rate.in * r.priceIn + rate.out * (r.priceOut || 0)) / 1e6) : null);
  const [p, f] = rows.map(cost);
  return (
    <span className="dim small">
      ≈ {tokens(rate.in)} in{rate.out ? ` · ${tokens(rate.out)} out` : ''} tokens per {rate.per}
      {local ? ' → free (this Mac)' : p ? ` → ${p}` : ' → price not known'}{f && ` (fallback ${f})`}
    </span>
  );
}

function Pick({ label, slot, value, effort, text, rows, fallback, onModel, onEffort }: {
  label: string; slot: SlotView; value: string; effort: string; text?: string; rows: ModelRow[]; fallback?: boolean;
  onModel: (v: string) => void; onEffort: (v: string) => void;
}) {
  const opts = options(rows, fallback ? 'chat' : slot.kind);
  const bySource = new Map<string, Option[]>();
  for (const o of opts) bySource.set(o.row.source, [...(bySource.get(o.row.source) ?? []), o]);
  const known = value === '' || value === 'custom' || opts.some((o) => o.value === value);
  const cur = opts.find((o) => o.value === value);
  // Thinking: the levels the model takes; none for local and decision models.
  const efforts = cur && cur.format !== 'ollama' ? cur.row.efforts ?? [] : value === 'custom' && slot.kind === 'chat' ? ['low', 'medium', 'high'] : [];
  const unverified = cur?.row.note?.includes('unverified') || value === 'custom';
  const def = cur?.row.defaultEffort ? `default (${cur.row.defaultEffort})` : 'default';
  return (
    <div className="model-pick">
      <span className="dim small model-pick-label">{label}</span>
      <select value={value} onChange={(e) => onModel(e.target.value)}>
        {(fallback || slot.kind !== 'local') && <option value="">{fallback ? 'none' : 'off'}</option>}
        {value === 'custom' && <option value="custom">{text ? `as set: ${text}` : 'as set in the config file'}</option>}
        {!known && <option value={value}>{value.split('|')[2]} (not available now)</option>}
        {[...bySource.entries()].map(([src, os]) => (
          <optgroup key={src} label={sourceName(src)}>
            {os.map((o) => <option key={o.value} value={o.value}>{o.label}{o.row.retires ? ` (retires ${o.row.retires})` : ''}</option>)}
          </optgroup>
        ))}
      </select>
      <select className="effort" value={efforts.length ? effort : ''} disabled={!efforts.length}
        title={efforts.length ? (unverified ? 'Thinking (levels unverified)' : 'Thinking') : 'This model has no thinking setting'} onChange={(e) => onEffort(e.target.value)}>
        <option value="">{efforts.length ? def : 'no thinking setting'}</option>
        {efforts.map((e) => <option key={e} value={e}>{e}{unverified ? ' (unverified)' : ''}</option>)}
        {effort && efforts.length > 0 && !efforts.includes(effort) && <option value={effort}>{effort} (not listed)</option>}
      </select>
    </div>
  );
}

// RecordingGroup: the recorder's transcription and speaker voice models
// (ONNX, run in the recorder on this Mac), written to [recorder.*] like
// the settings in Settings → Recording and read when the recorder starts.
function RecordingGroup() {
  const [rm, setRm] = useState<RecordingModels | null>(null);
  const [err, setErr] = useState('');
  const load = () => api.recordingModels().then(setRm, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    const offs = [on('recording:changed', load), on('models:changed', load), on('recorder:conn', load)];
    return () => offs.forEach((f) => f());
  }, []);
  if (err) return <div className="error small">{err}</div>;
  if (!rm) return null;
  return (
    <div className="model-group">
      <div className="model-group-head">
        <b>Recording</b>
        <span className="dim small">Transcribes meetings and tells voices apart, on this Mac (ONNX, in the recorder). Applies after a recorder restart.</span>
        {rm.error && <span className="dim small">Which models are downloaded isn&apos;t known: {rm.error}.</span>}
      </div>
      <div className="slot-row rec-models">{rm.fields.map((f) => <RecModelPick key={f.table + '.' + f.key} f={f} tomoeOff={rm.tomoe === 'off'} />)}</div>
    </div>
  );
}

function RecModelPick({ f, tomoeOff }: { f: RecModelField; tomoeOff: boolean }) {
  const [v, setV] = useState(String(f.value ?? ''));
  const [state, setState] = useState<'' | 'saving' | 'saved'>('');
  const [err, setErr] = useState('');
  useEffect(() => { setV(String(f.value ?? '')); }, [f.value]);
  const missing = new Set(f.missing ?? []);
  const opts = f.options ?? [];
  const save = (val: string) => {
    setV(val); setState('saving'); setErr('');
    api.setRecordingSetting(f.table, f.key, val).then(() => setState('saved'), (e) => { setState(''); setErr(String(e)); setV(String(f.value ?? '')); });
  };
  const nameOf = (id?: string) => opts.find((o) => o.value === id)?.label ?? id;
  return (
    <div className="model-pick">
      <span className="dim small model-pick-label">
        {f.label}{!f.set && (tomoeOff
          ? <span title="Not set in Musubi: the recorder's default"> · default</span>
          : <span title="Not set in Musubi: Tomoe's value"> · as Tomoe</span>)}
        {state === 'saving' ? <> <Spinner /></> : state === 'saved' ? <span className="ok-text"> ✓ saved; after a restart</span> : null}
      </span>
      <select value={v} onChange={(e) => save(e.target.value)}>
        {opts.map((o) => <option key={o.value} value={o.value}>{o.label}{missing.has(o.value) ? ' (not downloaded)' : ''}</option>)}
        {!opts.some((o) => o.value === v) && <option value={v}>{v} (not offered)</option>}
      </select>
      <span className="dim small model-pick-label">
        {f.help}
        {f.inUse && <> Now using {nameOf(f.inUse)}.</>}
        {missing.has(v) && <> Not downloaded yet: download it in Settings → Models.</>}
      </span>
      {err && <span className="error small model-pick-label">{err}</span>}
    </div>
  );
}
