import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../../api';
import type { RecField, RecordingSettings, RecSources } from '../../types';
import { Spinner } from '../common';
import { useRecorder } from '../recorder';

// Recording: which recorder Musubi's meetings come from, and the
// recorder's settings. A setting saved here wins over Tomoe's config for
// the recorder; one never set here shows Tomoe's value, or with Tomoe off
// ([recorder] tomoe = "off") the recorder's built-in default.
export function RecordingTab() {
  const [s, setS] = useState<RecordingSettings | null>(null);
  const [src, setSrc] = useState<RecSources | null>(null);
  const [err, setErr] = useState('');
  const load = () => {
    api.recordingSettings().then(setS, (e) => setErr(String(e)));
    api.recorderSources().then(setSrc, () => setSrc(null));
  };
  useEffect(() => {
    load();
    const offs = [on('prefs:changed', load), on('recording:changed', load)];
    return () => offs.forEach((f) => f());
  }, []);
  if (err) return <div className="error">{err}</div>;
  if (!s) return <Spinner />;
  return (
    <>
      <OfRecord s={s} />
      {s.groups.map((g) => (
        <section key={g.title} className="kv">
          <div className="section-head"><span>{g.title}</span></div>
          {g.fields.map((f) => <Field key={f.table + '.' + f.key} f={f} src={src} tomoeOff={s.tomoe === 'off'} />)}
        </section>
      ))}
      <p className="dim small">
        {s.tomoe === 'off'
          ? <>Settings not changed here are the recorder&apos;s built-in defaults; it reads Musubi&apos;s config only, never Tomoe&apos;s. A change applies at once,</>
          : <>Settings not changed here come from Tomoe&apos;s config ({s.tomoeConfig}), which the recorder reads with these on top. A change applies at once,</>}
        or from the next recording for what can&apos;t change mid-meeting; one marked &ldquo;after a restart&rdquo; waits until the recorder restarts (Apply, below, once one is waiting).
      </p>
      <RestartBar />
    </>
  );
}

// RestartBar appears once a changed setting waits for the recorder to
// restart, with the warning and the button that applies it.
function RestartBar() {
  const rec = useRecorder();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const waiting = rec?.status?.restart ?? [];
  if (!rec?.running || waiting.length === 0) return err ? <div className="error small">{err}</div> : null;
  const recording = !!rec.status?.recording;
  const apply = () => {
    setBusy(true); setErr('');
    api.restartRecorder().then(() => setBusy(false), (e) => { setBusy(false); setErr(String(e)); });
  };
  return (
    <div className="restart-bar">
      <div className="small">
        <b>Waiting for a restart:</b> the new {waiting.join(', ')} settings. Restarting the recorder reloads its models, which takes up to a
        minute; a meeting that starts meanwhile isn&apos;t noticed until it&apos;s back, so don&apos;t restart just before a call.
        {recording && <> It is recording a meeting now: restart once the meeting is over.</>}
      </div>
      <button className="primary" disabled={busy || recording} onClick={apply}>{busy ? <><Spinner /> Restarting…</> : 'Apply: restart the recorder'}</button>
      {err && <div className="error small">{err}</div>}
    </div>
  );
}

function OfRecord({ s }: { s: RecordingSettings }) {
  const [want, setWant] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const musubi = s.ofRecord === 'musubi';
  const go = (v: string) => {
    setBusy(true); setErr('');
    api.setOfRecord(v).then(() => { setBusy(false); setWant(null); }, (e) => { setBusy(false); setErr(String(e)); });
  };
  const since = s.since && !s.since.startsWith('0001') ? fmtDate(s.since) : '';
  return (
    <section className="kv">
      <div className="section-head"><span>Recorder of record</span></div>
      <p className="small">
        {musubi
          ? <>Musubi&apos;s meetings and live panel come from its own recorder{since ? `, for meetings recorded since ${since}` : ''}. Speakers and voices are corrected here.</>
          : <>Musubi&apos;s meetings and live panel come from Tomoe. Musubi&apos;s recorder runs alongside; its state, sources and health are on Status.</>}
      </p>
      {!want && (
        <div className="row-buttons">
          <button onClick={() => setWant(musubi ? 'tomoe' : 'musubi')}>{musubi ? 'Go back to Tomoe' : 'Switch to Musubi’s recorder'}</button>
        </div>
      )}
      {want === 'musubi' && (
        <div className="confirm-row">
          <span className="small">
            From now on Musubi imports meetings from its own recorder, only those recorded after now (earlier ones came from Tomoe and are not imported
            twice), and the live panel follows it. Voice and speaker corrections work here from then on: they can&apos;t before, because while Tomoe is
            of record the recorder copies Tomoe&apos;s voices at each meeting. Tomoe&apos;s recordings are no longer imported.
          </span>
          <button className="primary" disabled={busy} onClick={() => go('musubi')}>{busy ? <Spinner /> : 'Switch'}</button>
          <button onClick={() => setWant(null)}>Cancel</button>
        </div>
      )}
      {want === 'tomoe' && (
        <div className="confirm-row">
          <span className="small">Musubi goes back to Tomoe&apos;s meetings and live feed. Meetings already imported from its own recorder stay; corrections here stop.</span>
          <button className="primary" disabled={busy} onClick={() => go('tomoe')}>{busy ? <Spinner /> : 'Go back'}</button>
          <button onClick={() => setWant(null)}>Cancel</button>
        </div>
      )}
      {err && <div className="error small">{err}</div>}
    </section>
  );
}

// choices for the fields picked from what the recorder can use now.
function choices(f: RecField, src: RecSources | null): { value: string; label: string }[] | null {
  const cur = String(f.value ?? '');
  let opts: { value: string; label: string }[] | null = null;
  if (f.table === 'audio' && f.key === 'device') {
    opts = [{ value: 'default', label: 'System default' }, ...(src?.mics ?? []).filter((m) => m.name).map((m) => ({ value: m.name, label: m.name }))];
  } else if (f.key === 'monitor_device') {
    // Only sources that last: one app's audio (a process id) is chosen per
    // recording, on Status.
    opts = [{ value: 'everything', label: 'All system audio' }, { value: 'none', label: 'Microphone only' },
      ...(src?.system ?? []).filter((x) => !/^\d+$/.test(x.id) && x.id !== 'everything' && x.id !== 'none').map((x) => ({ value: x.id, label: x.name || x.id }))];
  } else if (f.key === 'video_hint_window') {
    opts = [{ value: '', label: 'Teams or Zoom meeting (automatic)' }, { value: 'none', label: 'Off' }];
  } else if (f.type === 'select') {
    opts = f.options ?? [];
  }
  if (opts && !opts.some((o) => o.value === cur)) opts.push({ value: cur, label: f.key === 'monitor_device' && cur === '' ? 'Microphone only' : `${cur} (not available now)` });
  return opts;
}

function Field({ f, src, tomoeOff }: { f: RecField; src: RecSources | null; tomoeOff: boolean }) {
  const [v, setV] = useState<unknown>(f.value);
  const [state, setState] = useState<'' | 'saving' | 'saved'>('');
  const [err, setErr] = useState('');
  const [other, setOther] = useState(false);
  useEffect(() => { setV(f.value); }, [f.value]);
  const save = (val: boolean | number | string) => {
    if (val === f.value) return;
    setState('saving'); setErr('');
    api.setRecordingSetting(f.table, f.key, val).then(() => setState('saved'), (e) => { setState(''); setErr(String(e)); setV(f.value); });
  };
  const opts = choices(f, src);
  let input;
  if (f.type === 'bool') {
    input = <input type="checkbox" checked={!!v} onChange={(e) => { setV(e.target.checked); save(e.target.checked); }} />;
  } else if (f.type === 'int' || f.type === 'float') {
    const num = (x: unknown) => typeof x === 'number' && isFinite(x);
    input = <input className="num" type="number" min={f.min} step={f.step ?? 1} value={num(v) ? String(v) : ''}
      onChange={(e) => setV(e.target.value === '' ? '' : Number(e.target.value))}
      onBlur={() => num(v) && save(v as number)} onKeyDown={(e) => e.key === 'Enter' && num(v) && save(v as number)} />;
  } else if (opts && !other) {
    input = (
      <select value={String(v ?? '')} onChange={(e) => {
        if (e.target.value === '\u0000other') { setOther(true); return; }
        setV(e.target.value); save(e.target.value);
      }}>
        {opts.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        {f.key === 'video_hint_window' && <option value={'\u0000other'}>Another app…</option>}
      </select>
    );
  } else {
    input = <input value={String(v ?? '')} placeholder={f.key === 'video_hint_window' ? "The app's name" : ''} onChange={(e) => setV(e.target.value)}
      onBlur={() => save(String(v ?? ''))} onKeyDown={(e) => e.key === 'Enter' && save(String(v ?? ''))} />;
  }
  return (
    <div className="pref">
      <label className="pref-main">
        <span className="pref-label">{f.label}{!f.set && (tomoeOff
          ? <span className="dim small" title="Not set here: the recorder's default"> · default</span>
          : <span className="dim small" title="Not set here: Tomoe's value"> · as Tomoe</span>)}{f.restart && <span className="dim small" title="The recorder reads this when it starts"> · after a restart</span>}</span>
        <span className="pref-input">{input}{state === 'saving' ? <Spinner /> : state === 'saved' ? <span className="ok-text small"> ✓</span> : null}</span>
      </label>
      {f.help && <div className="dim small">{f.help}</div>}
      {err && <div className="error small">{err}</div>}
    </div>
  );
}
