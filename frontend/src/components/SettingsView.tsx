import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../api';
import type { AppStatus, M365View } from '../types';
import { checkDoes, Spinner } from './common';
import { GatewayCard } from './GatewayCard';
import { LocalCard, ProcessingCard } from './ModelsCard';
import { ProvidersCard } from './ProvidersCard';

type Tab = 'accounts' | 'providers' | 'processing' | 'dataset';
const tabs: [Tab, string][] = [['accounts', 'Accounts'], ['providers', 'AI providers'], ['processing', 'Processing'], ['dataset', 'Dataset']];

export function SettingsView({ status }: { status: AppStatus | null }) {
  const [tab, setTab] = useState<Tab>(() => {
    try { return (localStorage.getItem('settings-tab') as Tab) || 'accounts'; } catch { return 'accounts'; }
  });
  const pick = (t: Tab) => { setTab(t); try { localStorage.setItem('settings-tab', t); } catch { /* not kept */ } };
  return (
    <div className="view status-page">
      <nav className="subtabs">
        {tabs.map(([t, label]) => <button key={t} className={tab === t ? 'active' : ''} onClick={() => pick(t)}>{label}</button>)}
      </nav>
      {tab === 'accounts' && <M365Card status={status} />}
      {tab === 'providers' && <><GatewayCard /><ProvidersCard /><LocalCard /></>}
      {tab === 'processing' && <ProcessingCard />}
      {tab === 'dataset' && <FilesCard status={status} />}
    </div>
  );
}

// FilesCard: where this dataset's config and data are, and the live panel's
// settings (read-only here; they live in the config file).
function FilesCard({ status }: { status: AppStatus | null }) {
  if (!status?.config) return null;
  return (
    <section className="kv">
      <div className="section-head"><span>Dataset</span></div>
      <Row k="dataset" v={status.dataset || '—'} />
      {Object.entries(status.config).map(([k, v]) => <Row key={k} k={k} v={v} />)}
    </section>
  );
}

function M365Card({ status }: { status: AppStatus | null }) {
  const [m, setM] = useState<M365View | null>(null);
  const [err, setErr] = useState('');
  const [syncing, setSyncing] = useState(false);
  const [done, setDone] = useState('');
  const load = () => api.m365().then(setM);
  useEffect(() => {
    load();
    api.status().then((s) => setSyncing(s.updating));
    const offs = [
      on('m365:changed', () => load()),
      on<string>('m365:error', (e) => setErr(e)),
      on<AppStatus>('app:status', (s) => setSyncing(s.updating)),
      on<string>('update:done', (msg) => { setDone(msg); load(); }),
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
      {syncing && <div className="dim small">Reading Microsoft 365 (calendar, Teams transcripts, people), then adding what&apos;s new to Musubi. The first sync after connecting takes a few minutes.</div>}
      {!syncing && done && <div className="dim small">Last update: {done}</div>}
      {m.needsSignIn && <div className="error">The Microsoft 365 sign-in has lapsed. Sign in again to keep syncing.</div>}
      {!m.enabled && <div className="dim small">Syncing is off in the config ([m365] enabled = false).</div>}
      {(err || m.lastError) && !m.needsSignIn && <div className="error">{err || m.lastError}</div>}
      <div className="row-buttons">
        <button className="primary" disabled={m.connecting} onClick={connect}>
          {m.connecting ? <><Spinner /> Waiting for the browser…</> : m.connected && !m.needsSignIn ? 'Reconnect' : m.needsSignIn ? 'Sign in again' : 'Connect to Microsoft 365'}
        </button>
        {m.connected && (
          <button disabled={syncing} title={checkDoes(status)} onClick={() => { setDone(''); setSyncing(true); api.update(); }}>
            {syncing ? <><Spinner /> Checking for new meetings…</> : 'Check for new meetings'}
          </button>
        )}
        {m.connected && <button onClick={signOut}>Sign out</button>}
      </div>
    </section>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}
