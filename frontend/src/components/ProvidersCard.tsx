import { useEffect, useState } from 'react';
import { api, on } from '../api';
import type { ProviderView, TestResult } from '../types';
import { Spinner } from './common';

// ProvidersCard: hosted endpoints other than the AI Gateway (Baseten,
// say), set up once by name; models then use them from Processing. Keys
// stay in the Keychain.
export function ProvidersCard() {
  const [ps, setPs] = useState<ProviderView[] | null>(null);
  const [editing, setEditing] = useState<ProviderView | 'new' | null>(null);
  const [tests, setTests] = useState<Record<string, TestResult | 'running'>>({});
  const [err, setErr] = useState('');
  const load = () => api.providers().then((v) => setPs(v ?? []));
  useEffect(() => {
    load();
    return on('providers:changed', load);
  }, []);
  if (!ps) return <Spinner />;
  const test = (name: string) => {
    setTests((t) => ({ ...t, [name]: 'running' }));
    api.testProvider(name).then((r) => setTests((t) => ({ ...t, [name]: r })));
  };
  const remove = (p: ProviderView) => {
    if (confirm(`Remove ${p.name}? Its key stays in the Keychain.`)) api.deleteProvider(p.name).then(load, (e) => setErr(String(e)));
  };
  return (
    <section className="kv">
      <div className="section-head"><span>Other providers</span>{!editing && <button className="small" onClick={() => setEditing('new')}>Add a provider</button>}</div>
      {ps.length === 0 && !editing && <p className="dim small">None. A provider that serves models in the OpenAI or Anthropic format (Baseten, say) can be added here, then chosen under Processing.</p>}
      {ps.map((p) => (
        <div key={p.name} className="provider">
          <div className="kv-row"><span className="k"><b>{p.name}</b> · {p.format === 'anthropic' ? 'Anthropic format' : 'OpenAI format'}</span><span className="v">{p.url}</span></div>
          <div className="kv-row"><span className="k">key</span><span className="v">{p.hasKey ? `✓ in ${p.keyRef.replace('keychain:', 'Keychain: ')}` : `✗ none at ${p.keyRef}`}</span></div>
          {p.headers && Object.keys(p.headers).length > 0 && <div className="kv-row"><span className="k">headers</span><span className="v">{Object.keys(p.headers).join(', ')}</span></div>}
          <div className="kv-row"><span className="k">used by</span><span className="v">{p.inUse?.length ? p.inUse.join(', ') : 'nothing yet'}</span></div>
          <div className="row-buttons">
            <button className="small" onClick={() => test(p.name)}>Test</button>
            <button className="small" onClick={() => setEditing(p)}>Edit</button>
            <button className="small" onClick={() => remove(p)}>Remove</button>
            <TestNote r={tests[p.name]} />
          </div>
        </div>
      ))}
      {editing && <ProviderForm p={editing === 'new' ? null : editing} onDone={() => { setEditing(null); load(); }} />}
      {err && <div className="error small">{err}</div>}
    </section>
  );
}

export function TestNote({ r }: { r?: TestResult | 'running' }) {
  if (!r) return null;
  if (r === 'running') return <span className="dim small"><Spinner /> testing…</span>;
  return r.ok ? <span className="dim small">✓ {r.models} models, {r.ms} ms</span> : <span className="error small">{r.error}</span>;
}

function ProviderForm({ p, onDone }: { p: ProviderView | null; onDone: () => void }) {
  const [name, setName] = useState(p?.name ?? '');
  const [format, setFormat] = useState<string>(p?.format ?? 'openai');
  const [url, setUrl] = useState(p?.url ?? '');
  const [headers, setHeaders] = useState(Object.entries(p?.headers ?? {}).map(([k, v]) => `${k}: ${v}`).join('\n'));
  const [key, setKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const save = () => {
    const h: Record<string, string> = {};
    for (const line of headers.split('\n')) {
      const i = line.indexOf(':');
      if (i > 0) h[line.slice(0, i).trim()] = line.slice(i + 1).trim();
    }
    setBusy(true); setErr('');
    api.saveProvider(name, format, url, h, key).then(() => { setBusy(false); onDone(); }, (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <div className="gw-form">
      <label className="small">Name <input value={name} disabled={!!p} placeholder="baseten" onChange={(e) => setName(e.target.value)} /></label>
      <label className="small">Format
        <select value={format} onChange={(e) => setFormat(e.target.value)}>
          <option value="openai">OpenAI (/v1/chat/completions)</option>
          <option value="anthropic">Anthropic (/v1/messages)</option>
        </select>
      </label>
      <label className="small">Address <input value={url} placeholder="https://inference.baseten.co" onChange={(e) => setUrl(e.target.value)} /></label>
      <label className="small">Key{p?.hasKey ? ' (leave empty to keep the one in the Keychain)' : ''} <input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} /></label>
      <label className="small">Extra headers, one per line (name: value) <textarea rows={2} value={headers} onChange={(e) => setHeaders(e.target.value)} /></label>
      {err && <div className="error small">{err}</div>}
      <div className="row-buttons">
        <button className="primary" disabled={busy || !name || !url} onClick={save}>{busy ? <><Spinner /> Saving…</> : 'Save'}</button>
        <button onClick={onDone}>Cancel</button>
      </div>
    </div>
  );
}
