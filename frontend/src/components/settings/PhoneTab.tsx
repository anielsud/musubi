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
  const [now, setNow] = useState(Date.now());
  const load = () => api.phoneState().then((x) => { setV(x); setErr(''); }, (e) => setErr(String(e)));
  useEffect(() => {
    load();
    const offs = [on('phone:changed', load), on('prefs:changed', load)];
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => { offs.forEach((f) => f()); clearInterval(t); };
  }, []);
  const expired = !!v?.pairing && new Date(v.pairing.expires).getTime() <= now;
  useEffect(() => { if (expired) load(); }, [expired]);
  if (err && !v) return <div className="error">{err}</div>;
  if (!v) return <Spinner />;

  const toggle = () => {
    setBusy(true); setErr('');
    api.setPhoneEnabled(!v.enabled).then(() => { setBusy(false); load(); }, (e) => { setBusy(false); setErr(String(e)); load(); });
  };
  const pair = () => { setErr(''); api.phonePair().then(load, (e) => setErr(String(e))); };
  const cancel = () => api.phoneCancelPairing().then(load);
  const revoke = (id: string, name: string) => {
    if (!confirm(`Unpair ${name}? It stops syncing at once. What is already on the phone stays there.`)) return;
    api.phoneRevoke(id).then(load, (e) => setErr(String(e)));
  };
  const p = v.pairing;
  const left = p ? Math.max(0, Math.round((new Date(p.expires).getTime() - now) / 1000)) : 0;
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
        {v.error && <div className="error small">{v.error}</div>}
        {v.enabled && !v.ofRecord && (
          <div className="dim small">Recordings from the phone go to Musubi&apos;s recorder; Musubi takes them in once it records your meetings (Settings → Recording).</div>
        )}
        <div className="row-buttons">
          <button className={v.enabled ? '' : 'primary'} disabled={busy} onClick={toggle}>
            {busy ? <><Spinner /> Saving…</> : v.enabled ? 'Turn off' : 'Turn on'}
          </button>
          {v.enabled && v.listening && !p && <button className="primary" onClick={pair}>Pair a phone</button>}
        </div>
      </section>

      {p && left > 0 && (
        <section className="kv">
          <div className="section-head"><span>Pair a phone</span></div>
          <p className="dim small">
            On the phone, open Mac → Pair and enter an address below. Check that the fingerprint the phone shows matches this one
            before you enter the code: if it doesn&apos;t, something else answered, so don&apos;t pair.
          </p>
          <Row k="address" v={(p.addresses ?? []).length ? (p.addresses ?? []).map((a) => `${a}:${p.port}`).join(', ') : 'no private network address found'} />
          <Row k="fingerprint" v={<code>{p.fingerprint}</code>} />
          <Row k="code" v={<code className="pair-code">{p.code.slice(0, 4)} {p.code.slice(4)}</code>} />
          <Row k="expires in" v={`${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`} />
          <div className="dim small">One phone, one use. Five wrong codes close this window.</div>
          <div className="row-buttons"><button onClick={cancel}>Cancel</button></div>
        </section>
      )}

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
