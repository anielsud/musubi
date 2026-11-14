import { useEffect, useState } from 'react';
import { api, on } from '../api';
import type { PhonePairPrompt } from '../types';

// A phone asks to pair (docs/phone.md, "Pairing, confirmed on both"): the
// owner compares the code with the phone's and allows or denies it. Shown
// over whatever screen is open; the app brings its window forward.
export function PairPrompt() {
  const [p, setP] = useState<PhonePairPrompt | null>(null);
  const [now, setNow] = useState(Date.now());
  const [err, setErr] = useState('');
  useEffect(() => {
    const load = () => api.phonePairRequest().then((x) => setP(x ?? null), () => {});
    load();
    const offs = [
      on<PhonePairPrompt>('phone:pair', (x) => { setErr(''); setP(x); }),
      on('phone:changed', load),
    ];
    return () => offs.forEach((f) => f());
  }, []);
  useEffect(() => {
    if (!p) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [p]);
  if (!p || new Date(p.expires).getTime() <= now) return null;

  const answer = (allow: boolean) => {
    api.phoneAnswerPair(p.id, allow).then(() => setP(null), (e) => setErr(String(e)));
  };
  return (
    <div className="modal-backdrop">
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="pair-title">
        <h3 id="pair-title">Pair {p.deviceName}?</h3>
        <p>Code <code className="pair-code">{p.code.slice(0, 3)} {p.code.slice(3)}</code></p>
        <p className="dim small">
          Allow only if the phone shows the same code and you asked it to pair. A different code means something else
          answered the phone: deny.
        </p>
        {err && <div className="error small">{err}</div>}
        <div className="row-buttons">
          <button className="primary" onClick={() => answer(true)}>Allow</button>
          <button onClick={() => answer(false)}>Deny</button>
        </div>
      </div>
    </div>
  );
}
