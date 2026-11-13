import { useEffect, useState } from 'react';
import { api, on } from '../../api';
import type { ProviderView, TestResult } from '../../types';
import { Spinner } from '../common';
import { formatName, Row, TestNote, UsedBy } from './shared';

// ProvidersTab: where models come from — the AI Gateway, other providers
// such as Baseten, Ollama on this Mac, and ONNX (the recorder's models,
// run in its own process) — each on the same kind of card.
export function ProvidersTab() {
  const [ps, setPs] = useState<ProviderView[] | null>(null);
  const [adding, setAdding] = useState(false);
  const load = () => api.providers().then((v) => setPs(v ?? []));
  useEffect(() => {
    load();
    const offs = [on('providers:changed', load), on('gateway:changed', load), on('models:changed', load)];
    return () => offs.forEach((f) => f());
  }, []);
  if (!ps) return <Spinner />;
  return (
    <>
      {ps.map((p) => <ProviderCard key={p.name} p={p} onChanged={load} />)}
      {adding ? <CustomForm p={null} onDone={() => { setAdding(false); load(); }} /> : (
        <div className="row-buttons"><button onClick={() => setAdding(true)}>Add a provider</button></div>
      )}
    </>
  );
}

function ProviderCard({ p, onChanged }: { p: ProviderView; onChanged: () => void }) {
  const [editing, setEditing] = useState(false);
  const [test, setTest] = useState<TestResult | 'running'>();
  const [err, setErr] = useState('');
  const runTest = () => { setTest('running'); api.testProvider(p.name).then(setTest); };
  const status = p.kind === 'ollama' ? (p.ready ? '✓ running' : '✗ not running')
    : p.kind === 'onnx' ? (p.ready ? '✓ recorder running' : '✗ recorder not running')
    : p.ready ? '✓ ready' : p.kind === 'gateway' && !p.configured ? '✗ not connected' : '✗ no key';
  const remove = () => {
    const msg = p.kind === 'gateway'
      ? 'Disconnect from the AI Gateway? The token is deleted from your Keychain, and models on the gateway stop working until you connect again.'
      : `Remove ${p.name}? Its key stays in the Keychain.`;
    if (!confirm(msg)) return;
    (p.kind === 'gateway' ? api.gatewayDisconnect() : api.deleteProvider(p.name)).then(onChanged, (e) => setErr(String(e)));
  };
  return (
    <section className="kv provider">
      <div className="section-head"><span>{p.title}</span><span className={p.ready ? 'ok-text small' : 'warn small'}>{status}</span></div>
      {p.kind === 'onnx' ? <OnnxBody p={p} test={test} onTest={runTest} /> : p.kind === 'gateway' && !p.configured ? <GatewayForm p={p} onDone={onChanged} /> : (
        <>
          <Row k="address" v={p.url} />
          {p.kind !== 'ollama' && <Row k="key" v={p.hasKey ? `in your Keychain (${(p.keyRef ?? '').replace('keychain:', '')})` : `none at ${p.keyRef}`} />}
          {p.kind === 'gateway' && <Row k="charged to" v={p.tokenId ? `token ${p.tokenId}` : 'unknown: replace the token to fix'} />}
          {p.headers && Object.keys(p.headers).length > 0 && <Row k="extra headers" v={Object.keys(p.headers).join(', ')} />}
          {p.kind === 'ollama' && <Row k="starts with Musubi" v={p.startOllama ? 'yes' : 'no'} />}
          {p.kind === 'ollama' && <Row k="downloads models in use" v={p.pullMissing ? 'yes' : 'no'} />}
          <Row k="formats" v={p.formats.map(formatName).join(', ')} />
          <Row k="used by" v={<UsedBy list={p.usedBy} />} />
          {editing && p.kind === 'gateway' && <GatewayForm p={p} onDone={() => { setEditing(false); onChanged(); }} />}
          {editing && p.kind === 'custom' && <CustomForm p={p} onDone={() => { setEditing(false); onChanged(); }} />}
          {editing && p.kind === 'ollama' && <OllamaForm p={p} onDone={() => { setEditing(false); onChanged(); }} />}
          {!editing && (
            <div className="row-buttons">
              <button onClick={runTest}>Test</button>
              <button onClick={() => setEditing(true)}>{p.kind === 'gateway' ? 'Replace token' : 'Edit'}</button>
              {p.kind !== 'ollama' && <button onClick={remove}>{p.kind === 'gateway' ? 'Disconnect' : 'Remove'}</button>}
              <TestNote r={test} />
            </div>
          )}
        </>
      )}
      {err && <div className="error small">{err}</div>}
    </section>
  );
}

// OnnxBody: the recorder's models, run in its process by sherpa-onnx and
// ONNX Runtime. Nothing to set up: they are downloaded in Settings →
// Models and chosen in Settings → Processing.
function OnnxBody({ p, test, onTest }: { p: ProviderView; test?: TestResult | 'running'; onTest: () => void }) {
  return (
    <>
      <Row k="runs in" v="Musubi Recorder (sherpa-onnx, ONNX Runtime)" />
      <Row k="installed" v={p.ready ? (p.models?.length ? <span className="v-list">{p.models.map((m) => <span key={m}>{m}</span>)}</span> : <span className="dim">none</span>) : <span className="dim">{p.error || 'unknown'}</span>} />
      <Row k="formats" v={p.formats.map(formatName).join(', ')} />
      <Row k="used by" v={<UsedBy list={p.usedBy} />} />
      {!p.ready && <p className="dim small">The recorder starts at login; Status shows its state. Its models are listed once it answers.</p>}
      <div className="row-buttons">
        <button onClick={onTest}>Test</button>
        <TestNote r={test} />
      </div>
    </>
  );
}

// GatewayForm connects with a personal token: Musubi checks it with
// Cloudflare, keeps it in the Keychain, and charges calls to it.
function GatewayForm({ p, onDone }: { p: ProviderView; onDone: () => void }) {
  const [url, setUrl] = useState(p.url);
  const [account, setAccount] = useState('');
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  useEffect(() => { api.gateway().then((g) => { setUrl(g.url); setAccount(g.account); }); }, []);
  const save = () => {
    setBusy(true); setErr('');
    api.gatewayConnect(url, account, token).then(() => { setBusy(false); setToken(''); onDone(); }, (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <div className="gw-form">
      {!p.configured && (
        <p className="dim small">
          One personal token from the gateway&apos;s mint workflow gives Musubi Claude, GLM and the other hosted models, charged to you.
          Musubi checks it with Cloudflare and keeps it in your Keychain.
        </p>
      )}
      {!p.configured && <label className="small">Gateway address <input value={url} onChange={(e) => setUrl(e.target.value)} /></label>}
      {!p.configured && <label className="small">Cloudflare account <input value={account} onChange={(e) => setAccount(e.target.value)} /></label>}
      <label className="small">Token <input type="password" placeholder="cfat_…" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} /></label>
      {err && <div className="error small">{err}</div>}
      <div className="row-buttons">
        <button className="primary" disabled={busy || !token.trim()} onClick={save}>{busy ? <><Spinner /> Checking the token…</> : p.configured ? 'Save the new token' : 'Connect'}</button>
        {p.configured && <button onClick={onDone}>Cancel</button>}
      </div>
    </div>
  );
}

// CustomForm adds or edits a provider such as Baseten: one address for both
// formats; the key goes in the Keychain.
function CustomForm({ p, onDone }: { p: ProviderView | null; onDone: () => void }) {
  const [name, setName] = useState(p?.name ?? '');
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
    api.saveProvider(name, url, h, key).then(() => { setBusy(false); onDone(); }, (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <section className={p ? '' : 'kv provider'}>
      {!p && <div className="section-head"><span>New provider</span></div>}
      <div className="gw-form">
        <label className="small">Name <input value={name} disabled={!!p} placeholder="baseten" onChange={(e) => setName(e.target.value)} /></label>
        <label className="small">Address (serves both formats: /v1/chat/completions and /v1/messages)
          <input value={url} placeholder="https://inference.baseten.co" onChange={(e) => setUrl(e.target.value)} />
        </label>
        <label className="small">Key{p?.hasKey ? ' (leave empty to keep the one in your Keychain)' : ''} <input type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} /></label>
        <label className="small">Extra headers, one per line (name: value) <textarea rows={2} value={headers} onChange={(e) => setHeaders(e.target.value)} /></label>
        {err && <div className="error small">{err}</div>}
        <div className="row-buttons">
          <button className="primary" disabled={busy || !name || !url} onClick={save}>{busy ? <><Spinner /> Saving…</> : 'Save'}</button>
          <button onClick={onDone}>Cancel</button>
        </div>
      </div>
    </section>
  );
}

// OllamaForm: where Ollama is, whether Musubi starts it, and whether it
// downloads the models in use.
function OllamaForm({ p, onDone }: { p: ProviderView; onDone: () => void }) {
  const [url, setUrl] = useState(p.url);
  const [start, setStart] = useState(p.startOllama);
  const [pull, setPull] = useState(p.pullMissing);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const save = () => {
    setBusy(true); setErr('');
    api.setOllama(url, start, pull).then(() => { setBusy(false); onDone(); }, (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <div className="gw-form">
      <label className="small">Address <input value={url} onChange={(e) => setUrl(e.target.value)} /></label>
      <label className="check small"><input type="checkbox" checked={start} onChange={(e) => setStart(e.target.checked)} /> Start Ollama when Musubi opens, if it isn&apos;t running</label>
      <label className="check small"><input type="checkbox" checked={pull} onChange={(e) => setPull(e.target.checked)} /> Download the models in use that this Mac doesn&apos;t have (fallbacks only by hand)</label>
      {!p.ready && <p className="warn small">Ollama isn&apos;t answering at this address. Install it from ollama.com/download (or <code>brew install ollama</code>).</p>}
      {err && <div className="error small">{err}</div>}
      <div className="row-buttons">
        <button className="primary" disabled={busy} onClick={save}>{busy ? <><Spinner /> Saving…</> : 'Save'}</button>
        <button onClick={onDone}>Cancel</button>
      </div>
    </div>
  );
}
