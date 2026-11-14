import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../../api';
import type { PhoneView } from '../../types';
import { Spinner } from '../common';
import { Row } from './shared';

// Phone: the server paired phones sync with (docs/phone.md). Off by
// default; while on, Musubi listens on the local network (private
// addresses only) for paired phones.
export function PhoneTab() {
  const [v, setV] = useState<PhoneView | null>(null);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);
  const load = () => api.phoneState().then((x) => { setV(x); setErr(''); }, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    const offs = [on('phone:changed', load), on('prefs:changed', load)];
    return () => { offs.forEach((f) => f()); };
  }, []);
  if (err && !v) return <div className="error">{err}</div>;
  if (!v) return <Spinner />;

  const toggle = () => {
    setBusy(true); setErr('');
    api.setPhoneEnabled(!v.enabled).then(() => { setBusy(false); load(); }, (e) => { setBusy(false); setErr(String(e)); load(); });
  };
  const revoke = (id: string, name: string) => {
    if (!confirm(`Unpair ${name}? It stops syncing at once. What is already on the phone stays there.`)) return;
    api.phoneRevoke(id).then(load, (e) => setErr(String(e)));
  };
  const devices = v.devices ?? [];

  return (
    <>
      <section className="kv">
        <div className="section-head"><span>Phone</span></div>
        <p className="dim small">
          A paired phone records meetings where this Mac isn&apos;t (on site, in person) and sends them here to be transcribed
          when it can reach this Mac again; it also carries your agenda and its briefs. While this is on, Musubi listens on
          port {v.port} for paired phones, over HTTPS and from private networks only (home or office Wi-Fi, Tailscale).
        </p>
        <Row k="phone server" v={v.enabled ? (v.listening ? 'on, listening' : 'on, not listening') : 'off'} />
        {v.enabled && v.listening && (
          <Row k="on the local network" v={v.advertised
            ? `announced as “${v.advertisedAs}”`
            : `not announced${v.advertiseError ? ` (${v.advertiseError})` : ''}`} />
        )}
        {v.error && <div className="error small">{v.error}</div>}
        {v.enabled && !v.ofRecord && (
          <div className="dim small">Recordings from the phone go to Musubi&apos;s recorder; Musubi takes them in once it records your meetings (Settings → Recording).</div>
        )}
        <div className="row-buttons">
          <button className={v.enabled ? '' : 'primary'} disabled={busy} onClick={toggle}>
            {busy ? <><Spinner /> Saving…</> : v.enabled ? 'Turn off' : 'Turn on'}
          </button>
        </div>
        {v.enabled && (
          <div className="dim small">
            Pairing starts on the phone: open Mac, pick this Mac from the list, and Musubi asks you to allow it, with a
            code to compare with the phone&apos;s.
          </div>
        )}
      </section>

      <section className="kv">
        <div className="section-head"><span>Paired phones</span></div>
        {devices.length === 0 && <div className="dim small">None.</div>}
        {devices.map((d) => (
          <div key={d.id} className="kv-row">
            <span className="k">{d.name}</span>
            <span className="v">
              paired {fmtDate(d.paired)}{d.last_seen ? `, last seen ${fmtDate(d.last_seen)}` : ''}{' '}
              <button className="small" onClick={() => revoke(d.id, d.name)}>Unpair</button>
            </span>
          </div>
        ))}
      </section>
      {err && <div className="error small">{err}</div>}
    </>
  );
}
