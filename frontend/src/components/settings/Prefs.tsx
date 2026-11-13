import { useEffect, useState } from 'react';
import { api, fmtDate, on } from '../../api';
import type { BackupView, PrefField, PrefGroup, StorageView } from '../../types';
import { Spinner } from '../common';
import { bytes, Row } from './shared';

// Prefs shows a tab's settings, each saved to the config as it changes.
export function Prefs({ tab }: { tab: 'accounts' | 'dataset' }) {
  const [groups, setGroups] = useState<PrefGroup[] | null>(null);
  const [err, setErr] = useState('');
  const load = () => api.preferences(tab).then((g) => setGroups(g ?? []), (e) => setErr(String(e)));
  useEffect(() => {
    load();
    return on('prefs:changed', load);
  }, [tab]);
  if (err) return <div className="error">{err}</div>;
  if (!groups) return <Spinner />;
  return (
    <>
      {groups.map((g) => (
        <section key={g.title} className="kv">
          <div className="section-head"><span>{g.title}</span></div>
          {g.fields.map((f) => <Field key={f.section + '.' + f.key} f={f} />)}
        </section>
      ))}
    </>
  );
}

function Field({ f }: { f: PrefField }) {
  const [v, setV] = useState(f.value);
  const [state, setState] = useState<'' | 'saving' | 'saved'>('');
  const [err, setErr] = useState('');
  useEffect(() => { setV(f.value); }, [f.value]);
  const save = (val: boolean | number | string) => {
    if (val === f.value) return;
    setState('saving'); setErr('');
    api.setPreference(f.section, f.key, val).then(() => setState('saved'), (e) => { setState(''); setErr(String(e)); setV(f.value); });
  };
  const input = f.type === 'bool'
    ? <input type="checkbox" checked={!!v} onChange={(e) => { setV(e.target.checked); save(e.target.checked); }} />
    : f.type === 'int'
      ? <input className="num" type="number" min={0} value={String(v)} onChange={(e) => setV(e.target.value === '' ? '' : Number(e.target.value))}
          onBlur={() => typeof v === 'number' && save(v)} onKeyDown={(e) => e.key === 'Enter' && typeof v === 'number' && save(v)} />
      : <input value={String(v)} onChange={(e) => setV(e.target.value)} onBlur={() => save(String(v))} onKeyDown={(e) => e.key === 'Enter' && save(String(v))} />;
  return (
    <div className="pref">
      <label className="pref-main">
        <span className="pref-label">{f.label}</span>
        <span className="pref-input">{input}{f.unit && <span className="dim small"> {f.unit}</span>}{state === 'saving' ? <Spinner /> : state === 'saved' ? <span className="ok-text small"> ✓</span> : null}</span>
      </label>
      {f.help && <div className="dim small">{f.help}</div>}
      {err && <div className="error small">{err}</div>}
    </div>
  );
}

// StorageCard: where the dataset is, how much room it takes, and backups.
export function StorageCard() {
  const [s, setS] = useState<StorageView | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  const load = () => api.storage().then(setS);
  useEffect(() => {
    load();
    return on('storage:changed', load);
  }, []);
  if (!s) return <Spinner />;
  const backup = () => {
    setBusy(true); setMsg('');
    api.backup().then((dir) => { setBusy(false); setMsg(`Backed up to ${dir}`); }, (e) => { setBusy(false); setMsg(String(e)); });
  };
  const reveal = (p: string) => api.revealPath(p).catch((e) => setMsg(String(e)));
  const when = (name: string) => {
    const m = name.match(/(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})(\d{2})$/);
    return m ? fmtDate(`${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}`) : name;
  };
  return (
    <section className="kv">
      <div className="section-head"><span>Dataset: {s.dataset}</span><span className="dim small">{bytes(s.total)} on disk</span></div>
      <Row k="config file" v={<button className="link" onClick={() => reveal(s.config)}>{s.config}</button>} />
      <Row k="data folder" v={<button className="link" onClick={() => reveal(s.data)}>{s.data}</button>} />
      {(s.parts ?? []).map((p) => <Row key={p.label} k={p.label} v={bytes(p.bytes)} />)}
      <Row k="last backup" v={s.lastBackup ? <button className="link" onClick={() => reveal(`${s.backups}/${s.lastBackup}`)}>{when(s.lastBackup)}</button> : 'none yet'} />
      <div className="row-buttons">
        <button disabled={busy} onClick={backup}>{busy ? <><Spinner /> Backing up…</> : 'Back up now'}</button>
        {msg && <span className="dim small">{msg}</span>}
      </div>
      <p className="dim small">
        A backup copies the database, everything else in the data folder, the recorder&apos;s recordings and voices, and the config into {s.backups}.
        On this Mac&apos;s disk the files are cloned, so they take no extra room until one copy changes. The recorder&apos;s speech models
        are left out: they download again.
      </p>
      <Backups />
    </section>
  );
}

const kindName: Record<BackupView['kind'], string> = { manual: 'by hand', daily: 'daily', before: 'before a change', undo: 'before a restore' };

// Backups lists the dataset's backups, each with Restore.
function Backups() {
  const [list, setList] = useState<BackupView[] | null>(null);
  const [asking, setAsking] = useState<BackupView | null>(null);
  const [withRec, setWithRec] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  const load = () => api.backups().then((l) => setList(l ?? []), (e) => setMsg(String(e)));
  useEffect(() => {
    load();
    return on('storage:changed', load);
  }, []);
  if (!list) return <Spinner />;
  const restore = (b: BackupView) => {
    setAsking(null); setBusy(true); setMsg('');
    api.restoreBackup(b.name, withRec).then(
      (undo) => { setBusy(false); setMsg(`Restored. The dataset as it was is backed up as ${undo}: restore that to undo this.`); },
      (e) => { setBusy(false); setMsg(String(e)); },
    );
  };
  return (
    <>
      <div className="section-head"><span>Backups</span>{busy && <span className="dim small"><Spinner /> Restoring…</span>}</div>
      {list.length === 0 && <div className="dim small">none yet</div>}
      {list.map((b) => (
        <div key={b.name}>
          <Row k={fmtDate(b.created)} v={<>
            {kindName[b.kind] ?? b.kind}{b.kind === 'before' && b.reason ? `: ${b.reason}` : ''} · {bytes(b.bytes)}{' '}
            {b.legacy
              ? <span className="dim small">older layout: restore by hand</span>
              : <button className="link" disabled={busy} onClick={() => { setAsking(b); setWithRec(false); }}>Restore…</button>}
          </>} />
          {asking?.name === b.name && (
            <div className="confirm-row">
              <span className="small">
                Put this backup back? Musubi closes the dataset, backs it up as it is now (so this can be undone), copies the backup in and opens it
                again. Meetings recorded since stay with the recorder and are read in again at the next update. The config isn&apos;t changed.
              </span>
              {b.recorder && (
                <label className="small">
                  <input type="checkbox" checked={withRec} onChange={(e) => setWithRec(e.target.checked)} /> Also put back the recorder&apos;s recordings and
                  voices (not while a meeting is being recorded)
                </label>
              )}
              <button className="primary danger" onClick={() => restore(b)}>Restore</button>
              <button onClick={() => setAsking(null)}>Cancel</button>
            </div>
          )}
        </div>
      ))}
      {msg && <div className="dim small">{msg}</div>}
    </>
  );
}
