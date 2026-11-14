import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../../api';
import type { PhoneView } from '../../types';
import { Spinner } from '../common';
import { Row } from './shared';

// Phone: sync with paired phones (docs/phone.md). Off by default; while
// on, Musubi looks for phones on the local network and connects to them
// (it never listens for connections itself).
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
    if (!confirm(`Unpair ${name}? It stops syncing with this Mac. What is already on the phone stays there.`)) return;
    api.phoneRevoke(id).then(load, (e) => setErr(String(e)));
  };
  const devices = v.devices ?? [];

  return (
    <>
      <section className="kv">
        <div className="section-head"><span>Phone</span></div>
        <p className="dim small">
          A paired phone records meetings where this Mac isn&apos;t (on site, in person); when both are on the same
          network again (home or office Wi-Fi, or Tailscale on both), Musubi connects to the phone, fetches the recordings
          to be transcribed here, and gives the phone your agenda and its briefs. Musubi only connects out, to private
          addresses, over encrypted connections that both sides check.
        </p>
        <Row k="phone sync" v={!v.enabled ? 'off' : v.looking ? 'on, looking for phones' : `on, not looking for phones${v.lookError ? ` (${v.lookError})` : ''}`} />
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
            Pairing starts on the phone: tap <b>Pair with your Mac</b> there, and Musubi asks you to allow it, with a code
            to compare with the phone&apos;s.
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
              {d.last_sync ? `last sync ${fmtDate(d.last_sync)}` : `paired ${fmtDate(d.paired)}, not synced yet`}
              {d.last_outcome ? ` (${d.last_outcome})` : ''}
              {(d.addresses ?? []).length > 0 ? `; last addresses ${(d.addresses ?? []).join(', ')}` : ''}{' '}
              <button className="small" onClick={() => revoke(d.id, d.name)}>Unpair</button>
            </span>
          </div>
        ))}
      </section>
      {err && <div className="error small">{err}</div>}
    </>
  );
}
