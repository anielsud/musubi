import { useEffect, useRef, useState } from 'react';
import { api, on } from '../api';
import type { MeetingSamples, ProfileSamples, VoiceProfile, VoiceSample } from '../types';
import { Play, type Act } from './voices';

// A voice profile's samples: the stretches of each meeting it learnt
// from, worked out again by the recorder (op profile_samples). A card
// shows how many and whether they sound like more than one voice; each
// meeting lists the samples that fit the profile worst, the rest behind
// "+N more that fit well", each with Play and "Not them".

// fitWell: a sample at least this like the profile isn't listed until
// asked for (the owner's choice, 2026-11-12).
const fitWell = 0.85;
// minJudge: a sample of fewer fingerprints (a second or two) scores low
// against its own profile as often as not, like a thin meeting (the
// recorder's MinLearn, 10): such samples are counted, not listed as
// misfits.
const minJudge = 10;
// clipCap matches the recorder's voice_clip cap (clipSeconds).
const clipCap = 8;

// Samples are asked for one profile at a time: each reads the meetings'
// fingerprints.
let queue: Promise<unknown> = Promise.resolve();
const queued = <T,>(f: () => Promise<T>): Promise<T> => {
  const run = queue.then(f, f);
  queue = run.catch(() => {});
  return run;
};

export type SamplesState = { view: ProfileSamples | null; err: string; load: () => void };

// useProfileSamples is a profile's samples, loaded once asked for
// (load), and again when the profile or its voices change.
export function useProfileSamples(p: VoiceProfile): SamplesState {
  const [wanted, setWanted] = useState(false);
  const [view, setView] = useState<ProfileSamples | null>(null);
  const [err, setErr] = useState('');
  const [bump, setBump] = useState(0);
  const key = p.model + '/' + p.id + '/' + (p.heard ?? []).map((h) => h.session + ':' + h.fingerprints).join(',');
  useEffect(() => {
    if (!wanted) return;
    let live = true;
    queued(() => api.profileSamples(p.model, p.id)).then((v) => { if (live) { setView(v); setErr(''); } }, (e) => { if (live) setErr(String(e)); });
    return () => { live = false; };
  }, [wanted, key, bump]);
  useEffect(() => (wanted ? on('voices:changed', () => setBump((b) => b + 1)) : undefined), [wanted]);
  return { view, err, load: () => setWanted(true) };
}

// SamplesSummary is the card's line: total samples, and how many voices
// when a meeting's sound like more than one. It loads the samples once
// it's on screen.
export function SamplesSummary({ s }: { s: SamplesState }) {
  const el = useRef<HTMLSpanElement | null>(null);
  useEffect(() => {
    const node = el.current;
    if (!node || s.view) return;
    if (typeof IntersectionObserver === 'undefined') { s.load(); return; }
    const io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) { s.load(); io.disconnect(); } });
    io.observe(node);
    return () => io.disconnect();
  }, [s.view]);
  const v = s.view;
  return (
    <span ref={el}>
      {v && v.samples > 0 && <> · {v.samples} {v.samples === 1 ? 'sample' : 'samples'}</>}
      {v && v.voices > 1 && <> · <span className="warn-badge" title="A meeting's samples sound like more than one person: open the meetings to hear them">sounds like {v.voices} voices</span></>}
    </span>
  );
}

const clock = (t: number) => {
  const s = Math.max(0, Math.floor(t));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
};
const pct = (x: number) => (x < -1 ? '–' : `${Math.round(x * 100)}%`);
const shortLabel = (l = '') => l.replace(/ \(.*\)$/, '');

// MeetingSamplesRow is one meeting's samples under its row in the card.
export function MeetingSamplesRow({ s, p, session, editable, a }: { s: SamplesState; p: VoiceProfile; session: string; editable: boolean; a: Act }) {
  const [open, setOpen] = useState(false);
  const [all, setAll] = useState(false);
  const m: MeetingSamples | undefined = s.view?.meetings?.find((x) => x.session === session);
  if (!m) return null;
  if (m.noDiarization) return <div className="dim small">Only this clip: the meeting&apos;s fingerprints aren&apos;t saved</div>;
  if (m.note) return <div className="dim small">{m.note}</div>;
  const samples = m.samples ?? [];
  const kept = samples.filter((x) => !x.excluded), out = samples.filter((x) => x.excluded);
  if (samples.length === 0) return null;
  const judged = kept.filter((x) => x.fingerprints >= minJudge), short = kept.filter((x) => x.fingerprints < minJudge);
  const worse = judged.filter((x) => x.similarity < fitWell), fine = judged.filter((x) => x.similarity >= fitWell);
  const head = worse.length === 0 ? `${kept.length} ${kept.length === 1 ? 'sample' : 'samples'}, all fit well` : `${kept.length} ${kept.length === 1 ? 'sample' : 'samples'}`;
  const key = `s/${p.id}`;
  const edit = (x: VoiceSample, exclude: boolean) =>
    a.act(key, () => api.voiceSample(exclude, { model: p.model, id: p.id, session, start: x.start, end: x.end }), exclude ? `Took out the sample at ${clock(x.start)}.` : `Put back the sample at ${clock(x.start)}.`);
  const row = (x: VoiceSample) => (
    <div key={x.start + '-' + x.end} className={'vs-row' + (x.excluded ? ' taken-out' : '')}>
      <Play clip={{ session, start: x.start, end: Math.min(x.end, x.start + clipCap) }} />
      <span className="vs-bar" title={`${pct(x.similarity)} like the profile${x.restSimilarity >= -1 ? `, ${pct(x.restSimilarity)} like the rest of this meeting` : ''}`}>
        <span style={{ width: `${Math.max(0, Math.min(1, x.similarity)) * 100}%` }} className={x.similarity < fitWell ? 'low' : ''} />
      </span>
      <span className="vs-num">{pct(x.similarity)}</span>
      <span className="vs-time">{clock(x.start)}</span>
      <span className="vs-label dim" title={x.label}>{shortLabel(x.label)}</span>
      {editable && (x.excluded
        ? <button className="vs-btn" disabled={a.busy === key} title="Put this sample back in the voice" onClick={() => edit(x, false)}>Put back</button>
        : <button className="vs-btn" disabled={a.busy === key} title="This stretch isn't them: leave it out of the voice" onClick={() => edit(x, true)}>Not them</button>)}
    </div>
  );
  return (
    <div className="vs">
      <button className="link" onClick={() => setOpen(!open)}>{open ? 'hide samples' : head}</button>
      {m.voices > 1 && <span className="small warn-badge"> · sounds like {m.voices} voices</span>}
      {open && (
        <div className="vs-list">
          {m.voices > 1 && <div className="small warn">These samples sound like {m.voices} voices</div>}
          {m.rebuilt !== m.fingerprints && <div className="dim small">Worked out again from the saved meeting: {m.rebuilt} of the {m.fingerprints} fingerprints learnt</div>}
          {worse.map(row)}
          {fine.length > 0 && (worse.length === 0 || all
            ? fine.map(row)
            : <button className="link small" onClick={() => setAll(true)}>+{fine.length} more that fit well</button>)}
          {short.length > 0 && (all
            ? <><div className="dim small">Too short to judge ({short.length})</div>{short.map(row)}</>
            : <div className="dim small">+{short.length} too short to judge (under {minJudge} fingerprints)</div>)}
          {out.length > 0 && <><div className="dim small">Taken out ({out.length})</div>{out.map(row)}</>}
        </div>
      )}
    </div>
  );
}
