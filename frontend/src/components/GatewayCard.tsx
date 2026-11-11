import { useEffect, useState } from 'react';
import { api, on } from '../api';
import type { GatewayModel, GatewayView } from '../types';
import { Spinner } from './common';

// GatewayCard connects Musubi to the Optimizely AI Gateway: one personal
// token, kept in the Keychain, for every hosted model.
export function GatewayCard() {
  const [g, setG] = useState<GatewayView | null>(null);
  const [editing, setEditing] = useState(false);
  const [url, setUrl] = useState('');
  const [account, setAccount] = useState('');
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const load = () => api.gateway().then((v) => { setG(v); setUrl(v.url); setAccount(v.account); });
  useEffect(() => {
    load();
    return on('gateway:changed', load);
  }, []);
  if (!g) return <Spinner />;
  const ready = g.connected && g.configured;
  const connect = () => {
    setBusy(true); setErr('');
    api.gatewayConnect(url, account, token).then(
      () => { setToken(''); setEditing(false); setBusy(false); load(); },
      (e) => { setErr(String(e)); setBusy(false); load(); },
    );
  };
  const disconnect = () => {
    if (!confirm('Disconnect from the AI Gateway? The token is deleted from your Keychain, and models on the gateway stop working until you connect again.')) return;
    api.gatewayDisconnect().then(load, (e) => setErr(String(e)));
  };
  return (
    <section className="kv">
      <div className="section-head"><span>Optimizely AI Gateway</span></div>
      {ready ? (
        <>
          <Row k="address" v={g.url} />
          <Row k="account" v={g.account} />
          <Row k="token id" v={g.tokenId || 'not saved: replace the token to charge calls to you'} />
        </>
      ) : (
        <p className="dim small">
          One personal token from the gateway&apos;s mint workflow gives Musubi Claude, GLM and the other hosted models, charged to you.
          Paste it here: Musubi checks it with Cloudflare, keeps it in your Keychain, and every model on the gateway uses it.
          {g.connected && !g.configured && ' A token is already in your Keychain; connecting replaces it and saves the gateway in the config.'}
        </p>
      )}
      {(editing || !ready) && (
        <div className="gw-form">
          {!ready && (
            <>
              <label className="small">Gateway address <input value={url} onChange={(e) => setUrl(e.target.value)} /></label>
              <label className="small">Cloudflare account <input value={account} onChange={(e) => setAccount(e.target.value)} /></label>
            </>
          )}
          <label className="small">Token <input type="password" placeholder="cfat_…" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} /></label>
        </div>
      )}
      {err && <div className="error small">{err}</div>}
      <div className="row-buttons">
        {(editing || !ready) ? (
          <>
            <button className="primary" disabled={busy || !token.trim()} onClick={connect}>
              {busy ? <><Spinner /> Checking the token…</> : ready ? 'Save the new token' : 'Connect'}
            </button>
            {editing && <button onClick={() => { setEditing(false); setToken(''); setErr(''); }}>Cancel</button>}
          </>
        ) : (
          <>
            <button onClick={() => setEditing(true)}>Replace token</button>
            <button onClick={disconnect}>Disconnect</button>
          </>
        )}
      </div>
      {ready && <GatewayModels />}
    </section>
  );
}

const money = (n: number) => (n ? `$${n < 0.1 ? n.toFixed(3) : n.toFixed(2)}` : '—');
const ctx = (n: number) => (n ? (n >= 1e6 ? `${Math.round(n / 1e6)}M` : `${Math.round(n / 1000)}K`) : '—');

// GatewayModels lists what Musubi can use on the gateway, with live prices
// and planned retirement from Cloudflare's catalog.
function GatewayModels() {
  const [ms, setMs] = useState<GatewayModel[] | null>(null);
  const [err, setErr] = useState('');
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (open && !ms) api.gatewayModels().then((v) => setMs(v ?? []), (e) => setErr(String(e)));
  }, [open]);
  const soon = (d: string) => d && new Date(d).getTime() - Date.now() < 60 * 86400e3;
  return (
    <details className="gw-models" onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary className="small">Models on the gateway</summary>
      {err && <div className="error small">{err}</div>}
      {open && !ms && !err && <Spinner />}
      {ms && (
        <table className="models-table">
          <thead><tr><th>model</th><th>kind</th><th>$ / M in</th><th>$ / M out</th><th>context</th><th>retires</th></tr></thead>
          <tbody>
            {ms.map((m) => (
              <tr key={m.id} title={m.note}>
                <td>{m.id}</td>
                <td>{m.kind === 'decision' ? 'decision' : m.provider === 'anthropic' ? 'Claude' : 'chat'}</td>
                <td>{money(m.priceIn)}</td>
                <td>{m.kind === 'decision' ? '' : money(m.priceOut)}</td>
                <td>{ctx(m.context)}</td>
                <td className={soon(m.retires) ? 'warn' : ''}>{m.retires || ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {ms && <p className="dim small">Claude prices are the gateway&apos;s list (Haiku: prompts under 100K tokens); the rest come live from Cloudflare.</p>}
    </details>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}
