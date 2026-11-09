import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../api';
import type { M365View } from '../types';
import { Spinner } from './common';

export function SettingsView() {
  return (
    <div className="view status-page">
      <M365Card />
    </div>
  );
}

function M365Card() {
  const [m, setM] = useState<M365View | null>(null);
  const [err, setErr] = useState('');
  const load = () => api.m365().then(setM);
  useEffect(() => {
    load();
    const offs = [
      on('m365:changed', () => load()),
      on<string>('m365:error', (e) => setErr(e)),
      on('update:done', () => load()),
    ];
    return () => offs.forEach((off) => off());
  }, []);
  if (!m) return <Spinner />;
  const connect = () => { setErr(''); api.m365Connect(); };
  const signOut = () => { if (confirm('Sign out of Microsoft 365? What Musubi has already read stays.')) api.m365SignOut(); };
  return (
    <section className="kv">
      <div className="section-head"><span>Microsoft 365</span></div>
      {m.connected ? (
        <>
          <Row k="account" v={m.account ? `${m.account.name} · ${m.account.username}` : '—'} />
          <Row k="connected" v={m.account ? fmtDate(m.account.connectedAt) : '—'} />
          <Row k="last sync" v={m.lastSync && !m.lastSync.startsWith('0001') ? fmtDate(m.lastSync) : 'not yet'} />
          {m.lastReport && <Row k="read" v={m.lastReport} />}
        </>
      ) : (
        <p className="dim small">
          Not connected. Connecting signs in with Microsoft&apos;s Graph Command Line Tools app, which IT has already approved.
          Musubi only reads: your calendar, Teams meeting transcripts and the directory. The sign-in is kept in your Keychain.
        </p>
      )}
      {m.needsSignIn && <div className="error">The Microsoft 365 sign-in has lapsed. Sign in again to keep syncing.</div>}
      {!m.enabled && <div className="dim small">Syncing is off in the config ([m365] enabled = false).</div>}
      {(err || m.lastError) && !m.needsSignIn && <div className="error">{err || m.lastError}</div>}
      <div className="row-buttons">
        <button className="primary" disabled={m.connecting} onClick={connect}>
          {m.connecting ? <><Spinner /> Waiting for the browser…</> : m.connected && !m.needsSignIn ? 'Reconnect' : m.needsSignIn ? 'Sign in again' : 'Connect to Microsoft 365'}
        </button>
        {m.connected && <button onClick={() => api.update()}>Sync now</button>}
        {m.connected && <button onClick={signOut}>Sign out</button>}
      </div>
    </section>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}
