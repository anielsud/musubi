import { useEffect, useRef, useState, type ReactNode } from 'react';
import { api, clipURL, on, tileURL } from '../api';
import type { NamedByVoice, RelabelResult, ReturnVoiceResult, SameNamePair, SampleArgs, TileRef, UnknownVoice, VoiceArgs, VoiceClip, VoiceDoubt, VoicePair, VoiceProfile, VoicesView } from '../types';
import { Spinner } from './common';
import { MeetingLink, recordingItem, useItemsExist } from './nav';
import { FullName } from './people';
import { MeetingSamplesRow, SamplesSummary, useProfileSamples } from './voicesamples';

// Voices and speakers from the recorder's voice store: the review queue
// (named by voice, window or voice, who's this, same person, two profiles
// of one name) and voice
// profiles, with each clip and the person's tile. Edits go through the
// recorder and are offered only once Musubi's recorder is of record.

export const readOnlyWhy = 'Corrections work here once Musubi records your meetings (Settings → Recording); until then they are made in Tomoe.';

// A meeting less like the profile's other meetings than this is flagged:
// across saved meetings, 95% of one person's meetings agree at 0.68 or more.
const agreeFlag = 0.6;
// odd: a meeting worth checking: one that teaches the profile (learned:
// enough voice; a few seconds score low even for the right person, so
// they name speakers but don't teach) and sounds unlike the rest, not
// already confirmed by the owner.
// compared === false: nothing else teaches the profile to measure it
// against, so its score is no evidence either way.
const odd = (h: { agreement: number; learned?: boolean; confirmed?: boolean; compared?: boolean }, n: number) => n > 1 && !h.confirmed && h.learned !== false && h.compared !== false && h.agreement < agreeFlag;

export function useVoices() {
  const [view, setView] = useState<VoicesView | null>(null);
  const [err, setErr] = useState('');
  const load = () => api.voices().then((v) => { setView(v); setErr(''); }, (e) => { setView(null); setErr(String(e)); });
  // The recorder gathers the review lists once after it starts: asked
  // again until they are in.
  useEffect(() => {
    if (!view?.building) return;
    const t = setTimeout(load, 2000);
    return () => clearTimeout(t);
  }, [view]);
  useEffect(() => {
    load();
    // recorder:conn: the recorder came back (a restart, or Musubi opened
    // while it was starting): what failed to load loads now.
    const offs = [on('voices:changed', load), on('prefs:changed', load), on('update:done', load), on<boolean>('recorder:conn', (up) => { if (up) load(); })];
    return () => offs.forEach((f) => f());
  }, []);
  return { view, err, reload: load };
}

// useEditable: edits are offered once Musubi's recorder is of record.
export function useEditable() {
  const [ok, setOk] = useState(false);
  useEffect(() => {
    const load = () => api.recorderState().then((r) => setOk(r.editable), () => setOk(false));
    load();
    return on('prefs:changed', load);
  }, []);
  return ok;
}

// useRelabel: a renamed profile's name can go onto its meetings' labels
// ([recorder] relabel).
export function useRelabel() {
  const [ok, setOk] = useState(false);
  useEffect(() => {
    const load = () => api.recorderState().then((r) => setOk(!!r.editable && !!r.relabel), () => setOk(false));
    load();
    return on('prefs:changed', load);
  }, []);
  return ok;
}

// relabelled is how many meetings a relabel changed.
const relabelled = (r: RelabelResult) => new Set((r.recorder?.changed ?? []).map((c) => c.session)).size;

// oldNames are a profile's earlier names still worth taking off labels.
const oldNames = (p: VoiceProfile) => (p.aliases ?? []).filter((n) => n.trim() && n.trim().toLowerCase() !== p.name.trim().toLowerCase());

// openReviews counts what waits for the owner's word.
export function openReviews(v: VoicesView | null, session?: string) {
  if (!v) return 0;
  const f = <T extends { session: string }>(xs: T[] | null) => (xs ?? []).filter((x) => !session || x.session === session).length;
  return f(v.namedByVoice) + f(v.doubts) + f(v.unknown) + (session ? 0 : (v.pairs ?? []).length + (v.sameName ?? []).length);
}

export function VoicesError({ err }: { err: string }) {
  if (!err) return null;
  return <div className="dim small">{/not running/.test(err) ? 'The recorder is not running, so voices can’t be shown.' : err}</div>;
}

// useAct runs one edit at a time, keyed, with what came of it.
export function useAct() {
  const [busy, setBusy] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const [err, setErr] = useState('');
  // done is the note, or makes it from what f returned.
  const act = <T,>(key: string, f: () => Promise<T>, done: string | ((r: T) => string) = '') => {
    setBusy(key); setErr(''); setNote('');
    f().then((r) => { setBusy(null); setNote(typeof done === 'function' ? done(r) : done); }, (e) => { setBusy(null); setErr(String(e)); });
  };
  return { busy, note, err, act };
}
export type Act = ReturnType<typeof useAct>;

export function ActNotes({ a }: { a: Act }) {
  return <>{a.note && <div className="ok-text small">{a.note}</div>}{a.err && <div className="error small">{a.err}</div>}</>;
}

// One clip plays at a time.
let playing: HTMLAudioElement | null = null;
let stopPlaying: (() => void) | null = null;

// label names whose clip it is, where a row offers more than one.
export function Play({ clip, label }: { clip: VoiceClip; label?: string }) {
  const [on, setOn] = useState(false);
  const el = useRef<HTMLAudioElement | null>(null);
  useEffect(() => () => { if (playing === el.current) { playing?.pause(); playing = null; } }, []);
  const toggle = () => {
    if (on) { el.current?.pause(); setOn(false); return; }
    playing?.pause();
    stopPlaying?.();
    // One fetch, played from memory: WebKit otherwise streams the clip in
    // range requests and waits on each before it starts.
    setOn(true);
    fetch(clipURL(clip)).then((r) => (r.ok ? r.blob() : Promise.reject(r.status))).then((b) => {
      const url = URL.createObjectURL(b);
      const a = new Audio(url);
      el.current = a;
      playing = a;
      stopPlaying = () => setOn(false);
      const done = () => { setOn(false); URL.revokeObjectURL(url); };
      a.onended = done;
      a.onpause = done;
      return a.play();
    }).catch(() => setOn(false));
  };
  if (label) return <button className="icon-btn play labelled" title={`Play a few seconds: ${label}`} onClick={toggle}>{on ? '■' : '▶'} {label}</button>;
  return <button className="icon-btn play" title="Play a few seconds" onClick={toggle}>{on ? '■' : '▶'}</button>;
}

// Heard is a row's voice: one Play, or, when there are profiles to hear
// it against, a labelled Play for it and for each profile.
type Other = { clip: VoiceClip; label: string };
function Heard({ clip, others }: { clip: VoiceClip; others: (Other | null)[] }) {
  const os = others.filter((o, i): o is Other => !!o && others.findIndex((x) => x?.label === o.label) === i);
  if (os.length === 0) return <Play clip={clip} />;
  return <div className="voice-compare"><Play clip={clip} label="this voice" />{os.map((o) => <Play key={o.label} clip={o.clip} label={o.label} />)}</div>;
}

// TileImg is the person's tile in the meeting window, the height fixed
// and the shape kept (a video is wide, a picture square).
export function TileImg({ tile, size, title }: { tile?: TileRef; size: number; title?: string }) {
  const [broken, setBroken] = useState(false);
  if (!tile || broken) return null;
  return <img className="voice-tile" src={tileURL(tile.session, tile.look)} style={{ height: size, maxWidth: size * 1.8 }} title={title} alt="" onError={() => setBroken(true)} />;
}

const day = (iso: string) => { const d = new Date(iso); return isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }); };
const spoke = (s: number) => (s >= 60 ? `${Math.round(s / 60)} min` : `${Math.round(s)} s`);
const bare = (label: string) => label.replace(/ \(.*\)$/, '');

// NameForm asks who it is.
function NameForm({ busy, onName, onCancel, list, start = '', button = 'Name', title }: {
  busy: boolean; onName: (n: string) => void; onCancel?: () => void; list?: string; start?: string; button?: string; title?: string;
}) {
  const [n, setN] = useState(start);
  return (
    <form className="voice-name" onSubmit={(e) => { e.preventDefault(); if (n.trim()) onName(n.trim()); }}>
      <input value={n} list={list} autoFocus={!!onCancel} placeholder="Who is it?" onChange={(e) => setN(e.target.value)} disabled={busy} />
      <button className="primary" type="submit" disabled={busy || !n.trim()} title={title}>{button}</button>
      {onCancel && <button type="button" onClick={onCancel} title="Change nothing">Cancel</button>}
    </form>
  );
}

// ReviewItems are the review queue's rows, for one meeting (session) or
// all of them.
export function ReviewItems({ view, session, editable, a, showTitle = true }: {
  view: VoicesView; session?: string; editable: boolean; a: Act; showTitle?: boolean;
}) {
  const mine = <T extends { session: string }>(xs: T[] | null) => (xs ?? []).filter((x) => !session || x.session === session);
  const named = mine(view.namedByVoice), doubts = mine(view.doubts), unknown = mine(view.unknown);
  const pairs = session ? [] : view.pairs ?? [];
  const sameName = session ? [] : view.sameName ?? [];
  // After "Different people", one of the two is offered a new name.
  const [renaming, setRenaming] = useState<SameNamePair | null>(null);
  const byId: Record<string, VoiceProfile> = {};
  (view.profiles ?? []).forEach((p) => { byId[`${p.model}/${p.id}`] = p; });
  // A profile's clip to hear a voice against: by profile id where the row
  // has one, else by name when exactly one profile has it (two Kevins'
  // profiles are two people: neither is offered).
  const clipOf = (name?: string, model?: string, id?: string): Other | null => {
    const p = (model && id ? byId[`${model}/${id}`] : undefined) ?? (() => {
      const ps = (view.profiles ?? []).filter((x) => !!name && x.name === name && !x.startedOver);
      return ps.length === 1 ? ps[0] : undefined;
    })();
    return p && !p.startedOver ? { clip: p.clip, label: p.name } : null;
  };
  const sp = (op: string, s: string, label: string, name: string, done: string) => a.act(`${s}/${label}`, () => api.recorderSpeaker(op, s, label, name), done);
  // Which rows' meetings Musubi has, asked once for the list.
  const found = useItemsExist(showTitle ? [...named, ...doubts, ...unknown].map((x) => recordingItem(x.session)) : []);
  const where = (x: { session: string; title: string; at: string; clip?: VoiceClip }, lead = ' in '): ReactNode => (showTitle
    ? <>{lead}<MeetingLink session={x.session} title={x.title || x.session.slice(0, 8)} found={found} at={x.clip?.start} /> · {day(x.at)}</> : null);
  return (
    <>
      {view.building && <div className="dim small"><Spinner /> Gathering voices to check from recent meetings: building…</div>}
      {named.length > 0 && <>
        <div className="review-head">Named by voice</div>
        <p className="dim small">A voice profile named these speakers, not the meeting window or you. Play them and say if they&apos;re right: either answer teaches the profile.</p>
        {named.map((n) => <NamedRow key={`n/${n.session}/${n.label}`} n={n} others={[clipOf(n.name, n.model, n.profile)]} where={where(n)} editable={editable} busy={a.busy === `${n.session}/${n.label}`}
          onRight={() => sp('rename_speaker', n.session, n.label, n.name, `Confirmed ${n.name}.`)}
          onNotThem={() => sp('not_them', n.session, n.label, '', `${n.name}'s profile won't name that voice again.`)}
          onName={(name) => sp('rename_speaker', n.session, n.label, name, `Named them ${name}.`)}
          onSkip={() => sp('skip_voice_name', n.session, n.label, '', 'Won’t ask about that one again.')} />)}
      </>}
      {doubts.length > 0 && <>
        <div className="review-head">Window or voice?</div>
        <p className="dim small">The meeting window named these speakers, but the voice sounds a little like someone else. Until you say, they keep the window&apos;s name.</p>
        {doubts.map((d) => <DoubtRow key={`d/${d.session}/${d.label}`} d={d} others={[clipOf(d.name), d.noProfile ? null : clipOf(d.window)]} where={where(d)} editable={editable} busy={a.busy === `${d.session}/${d.label}`}
          onName={(name) => sp('rename_speaker', d.session, d.label, name, `Named them ${name}.`)}
          onSkip={() => sp('skip_voice_doubt', d.session, d.label, '', 'Won’t ask about that one again; it keeps the window’s name.')} />)}
      </>}
      {unknown.length > 0 && <>
        <div className="review-head">Who&apos;s this?</div>
        <p className="dim small">Voices nobody has named, and samples you said aren&apos;t the voice they were in. Naming a speaker names them in that meeting and teaches their profile; naming a sample teaches the profile only.</p>
        {unknown.map((u) => {
          const k = unknownKey(u);
          if (u.sample) {
            const args: SampleArgs = { model: u.sample.model, id: u.sample.profile, session: u.session, start: u.sample.start, end: u.sample.end };
            return <UnknownRow key={k} u={u} others={[clipOf(u.soundsLike)]} where={where(u, 'in ')} editable={editable} busy={a.busy === k}
              onName={(name) => a.act(k, () => api.nameReturnedSample(args, name), (r) => (r.created ? `Started a voice profile for ${r.name} with that sample.` : `That sample now teaches ${r.name}'s voice.`))}
              onSkip={() => a.act(k, () => api.skipReturnedSample(args), 'Won’t ask about that sample again; it stays out of the voice it was in.')} />;
          }
          return <UnknownRow key={k} u={u} others={[clipOf(u.soundsLike)]} where={where(u, 'in ')} editable={editable} busy={a.busy === `${u.session}/${u.label}`}
            onName={(name) => sp('rename_speaker', u.session, u.label, name, `Named ${bare(u.label)} ${name}.`)}
            onSkip={() => sp('skip_returned_voice', u.session, u.label, '', 'Won’t ask about that one again.')} />;
        })}
      </>}
      {pairs.length > 0 && <>
        <div className="review-head">Same person?</div>
        <p className="dim small">These profiles sound alike: one person under two names, or a name the meeting window misread?</p>
        {pairs.map((pr) => {
          const pa = byId[`${pr.model}/${pr.a}`], pb = byId[`${pr.model}/${pr.b}`];
          return pa && pb ? <PairRow key={`p/${pr.a}/${pr.b}`} pair={pr} a={pa} b={pb} editable={editable} act={a} /> : null;
        })}
      </>}
      {sameName.length > 0 && <>
        <div className="review-head">Two profiles, one name?</div>
        <p className="dim small">These profiles have the same name but were learnt from voices unlike each other: one person, or two people with one name?</p>
        {sameName.map((pr) => {
          const pa = byId[`${pr.model}/${pr.a}`], pb = byId[`${pr.model}/${pr.b}`];
          return pa && pb ? <SameNameRow key={`s/${pr.a}/${pr.b}`} pair={pr} a={pa} b={pb} editable={editable} act={a} onApart={() => setRenaming(pr)} /> : null;
        })}
      </>}
      {renaming && byId[`${renaming.model}/${renaming.a}`] && byId[`${renaming.model}/${renaming.b}`] &&
        <RenameOne pair={renaming} a={byId[`${renaming.model}/${renaming.a}`]} b={byId[`${renaming.model}/${renaming.b}`]} act={a} onDone={() => setRenaming(null)} />}
    </>
  );
}

function NamedRow({ n, others, where, editable, busy, onRight, onNotThem, onName, onSkip }: {
  n: NamedByVoice; others: (Other | null)[]; where: ReactNode; editable: boolean; busy: boolean; onRight: () => void; onNotThem: () => void; onName: (n: string) => void; onSkip: () => void;
}) {
  const [naming, setNaming] = useState(false);
  return (
    <div className="voice-row">
      <Heard clip={n.clip} others={others} />
      <div className="voice-body">
        <div><b><FullName name={n.name} /></b> <span className="dim small">as {bare(n.label)}{where} · spoke {spoke(n.seconds)}</span></div>
        <div className="dim small">{Math.round(n.similarity * 100)}% like {n.name}&apos;s profile{n.window && <> · the meeting window said <FullName name={n.window} /></>}</div>
      </div>
      {editable && (naming ? <NameForm busy={busy} onName={onName} onCancel={() => setNaming(false)} /> : (
        <div className="voice-actions">
          <button disabled={busy} onClick={onRight} title={`It is ${n.name}: the speaker keeps the name, and ${n.name}'s voice learns from this meeting`}>Right</button>
          <button disabled={busy} onClick={onNotThem} title={`Not ${n.name}: the speaker loses the name, and ${n.name}'s voice won't name a voice like this again`}>Not them</button>
          <button disabled={busy} onClick={() => setNaming(true)} title="Name who it is: the speaker gets that name, and that person's voice learns from this meeting">Someone else…</button>
          <button disabled={busy} onClick={onSkip} title={`Noise, crosstalk or too short: stop asking; the speaker keeps the name, and ${n.name}'s voice learns nothing from it`}>Can&apos;t tell</button>
        </div>
      ))}
    </div>
  );
}

function DoubtRow({ d, others, where, editable, busy, onName, onSkip }: {
  d: VoiceDoubt; others: (Other | null)[]; where: ReactNode; editable: boolean; busy: boolean; onName: (n: string) => void; onSkip: () => void;
}) {
  const [naming, setNaming] = useState(false);
  return (
    <div className="voice-row">
      <Heard clip={d.clip} others={others} />
      <div className="voice-body">
        <div><b><FullName name={d.window} />?</b> <span className="dim small">as {bare(d.label)}{where} · spoke {spoke(d.seconds)}</span></div>
        <div className="dim small">The window said {d.window}; the voice sounds {d.similarity >= 0.7 ? '' : 'a little '}like <FullName name={d.name} /> ({Math.round(d.similarity * 100)}%){d.noProfile && <>, and {d.window} has no voice profile yet</>}</div>
      </div>
      {editable && (naming ? <NameForm busy={busy} onName={onName} onCancel={() => setNaming(false)} /> : (
        <div className="voice-actions">
          <button disabled={busy} onClick={() => onName(d.window)} title={`Name the speaker ${d.window}, as the window said; ${d.window}'s voice learns from this meeting`}>It&apos;s {d.window}</button>
          <button disabled={busy} onClick={() => onName(d.name)} title={`Name the speaker ${d.name}, as the voice suggests; ${d.name}'s voice learns from this meeting`}>It&apos;s {d.name}</button>
          <button disabled={busy} onClick={() => setNaming(true)} title="Name who it is: the speaker gets that name, and that person's voice learns from this meeting">Someone else…</button>
          <button disabled={busy} onClick={onSkip} title="Keep the window's name, stop asking, and teach no profile">Can&apos;t tell</button>
        </div>
      ))}
    </div>
  );
}

// unknownKey tells "Who's this?" rows apart: a speaker, or one sample.
const unknownKey = (u: UnknownVoice) => (u.sample ? `u/${u.session}/${u.sample.profile}/${u.sample.start}` : `u/${u.session}/${u.label}`);

function UnknownRow({ u, others, where, editable, busy, onName, onSkip }: { u: UnknownVoice; others: (Other | null)[]; where: ReactNode; editable: boolean; busy: boolean; onName: (n: string) => void; onSkip: () => void }) {
  const from = u.returnedFrom ? <FullName name={u.returnedFrom} /> : u.sample ? 'the voice it was in' : 'the voice profile that named them';
  const nameTitle = u.sample
    ? 'Name who it is: this sample teaches their voice (one already of that name it sounds like, or a new one). The transcript is unchanged.'
    : 'Name who it is: the speaker gets that name in this meeting, and their voice learns from it';
  return (
    <div className="voice-row">
      <Heard clip={u.clip} others={others} />
      <div className="voice-body">
        {u.sample
          ? <div><b>A sample at {clockOf(u.sample.start)}</b> <span className="dim small">{u.label && <>under {bare(u.label)} </>}{where}{where ? ' · ' : ''}{spoke(u.seconds)}</span></div>
          : <div><b>{bare(u.label)}</b> <span className="dim small">{where}{where ? ' · ' : ''}spoke {spoke(u.seconds)}</span></div>}
        {u.returned && <div className="dim small">You said this isn&apos;t {from}: who is it?</div>}
        {u.soundsLike && (
          <div className="dim small">
            {(u.similarity ?? 0) >= 0.7 ? 'Sounds like' : 'Sounds a little like'} <FullName name={u.soundsLike} /> ({Math.round((u.similarity ?? 0) * 100)}%)
            {editable && <> <button className="link" disabled={busy} title={u.sample ? `It is ${u.soundsLike}: this sample teaches ${u.soundsLike}'s voice. The transcript is unchanged.` : `It is ${u.soundsLike}: the speaker gets that name in this meeting, and ${u.soundsLike}'s voice learns from it`} onClick={() => onName(u.soundsLike!)}>It&apos;s {u.soundsLike}</button></>}
          </div>
        )}
      </div>
      {editable && <div className="voice-actions">
        <NameForm busy={busy} onName={onName} title={nameTitle} />
        {u.returned && <button disabled={busy} onClick={onSkip}
          title={u.sample ? 'Noise, crosstalk or you don\'t know: stop asking; the sample stays out of the voice it was in and teaches no one' : 'Noise, crosstalk or you don\'t know: stop asking; the speaker stays unnamed'}>Can&apos;t tell</button>}
      </div>}
    </div>
  );
}

const clockOf = (t: number) => {
  const s = Math.max(0, Math.floor(t));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
};

function PairRow({ pair, a, b, editable, act }: { pair: VoicePair; a: VoiceProfile; b: VoiceProfile; editable: boolean; act: Act }) {
  const key = `p/${pair.a}/${pair.b}`;
  const busy = act.busy === key;
  const same = (into: VoiceProfile, from: VoiceProfile) => act.act(key, () => api.recorderVoice('merge_voices', { model: pair.model, id: into.id, other: from.id }), `${from.name} is now part of ${into.name}.`);
  return (
    <div className="voice-row">
      <div className="voice-body">
        <div><Play clip={a.clip} /> {a.name} <span className="dim small">({a.meetings} meetings)</span> · <Play clip={b.clip} /> {b.name} <span className="dim small">({b.meetings} meetings)</span></div>
        <div className="dim small">{Math.round(pair.similarity * 100)}% alike</div>
      </div>
      {editable && (
        <div className="voice-actions">
          <button disabled={busy} onClick={() => same(a, b)} title={`One person, ${a.name}: ${b.name}'s voice joins ${a.name}'s, which keeps both voices' meetings and answers to both names`}>Same: {a.name}</button>
          <button disabled={busy} onClick={() => same(b, a)} title={`One person, ${b.name}: ${a.name}'s voice joins ${b.name}'s, which keeps both voices' meetings and answers to both names`}>Same: {b.name}</button>
          <button disabled={busy} title="Two people: both voices stay as they are, and these two aren't offered as one person again" onClick={() => act.act(key, () => api.recorderVoice('keep_apart', { model: pair.model, id: pair.a, other: pair.b }))}>Different people</button>
        </div>
      )}
    </div>
  );
}

// SameNameRow asks whether two profiles of one name are one person.
function SameNameRow({ pair, a, b, editable, act, onApart }: { pair: SameNamePair; a: VoiceProfile; b: VoiceProfile; editable: boolean; act: Act; onApart: () => void }) {
  const key = `s/${pair.a}/${pair.b}`;
  const busy = act.busy === key;
  // The one heard in more meetings keeps the other's.
  const [into, from] = a.meetings >= b.meetings ? [a, b] : [b, a];
  const meetings = (n: number) => `${n} ${n === 1 ? 'meeting' : 'meetings'}`;
  return (
    <div className="voice-row">
      <div className="voice-body">
        <div><b>Two profiles named {pair.name}?</b></div>
        <div className="small"><Play clip={pair.clipA} /> {a.name} <span className="dim">({meetings(pair.meetingsA)}, last heard {day(a.lastHeard)})</span> · <Play clip={pair.clipB} /> {b.name} <span className="dim">({meetings(pair.meetingsB)}, last heard {day(b.lastHeard)})</span></div>
        {pair.similarity > -2 && <div className="dim small">{Math.round(pair.similarity * 100)}% alike</div>}
      </div>
      {editable && (
        <div className="voice-actions">
          <button disabled={busy} title={`One person: the two ${pair.name} voices become one, keeping both voices' meetings`} onClick={() => act.act(key, () => api.recorderVoice('merge_voices', { model: pair.model, id: into.id, other: from.id }), `The two ${pair.name} profiles are now one.`)}>Same person</button>
          <button disabled={busy} title={`Two people named ${pair.name}: both voices stay as they are and aren't offered as one again; you can then rename one`} onClick={() => { onApart(); act.act(key, () => api.recorderVoice('keep_apart', { model: pair.model, id: pair.a, other: pair.b }), 'Kept apart.'); }}>Different people</button>
        </div>
      )}
    </div>
  );
}

// RenameOne, after "Different people", offers to rename one of the two
// so they can be told apart.
function RenameOne({ pair, a, b, act, onDone }: { pair: SameNamePair; a: VoiceProfile; b: VoiceProfile; act: Act; onDone: () => void }) {
  const [which, setWhich] = useState<VoiceProfile | null>(null);
  const busy = act.busy !== null;
  const meetings = (n: number) => `${n} ${n === 1 ? 'meeting' : 'meetings'}`;
  return (
    <div className="voice-row">
      <div className="voice-body">
        <div className="small">Two people named {pair.name}: rename one so you can tell them apart?</div>
        {which
          ? <NameForm busy={busy} start={which.name} button="Rename" onCancel={() => setWhich(null)}
              onName={(n) => { act.act(`r/${which.id}`, () => api.recorderVoice('rename_voice', { model: which.model, id: which.id, name: n }), `Renamed to ${n}.`); onDone(); }} />
          : <div className="voice-actions">
              {[{ p: a, clip: pair.clipA, n: pair.meetingsA }, { p: b, clip: pair.clipB, n: pair.meetingsB }].map((x) => (
                <span key={x.p.id}><Play clip={x.clip} /> <button disabled={busy} title="Give this voice another name; what it learnt is kept" onClick={() => setWhich(x.p)}>Rename the one in {meetings(x.n)}…</button></span>
              ))}
              <button disabled={busy} title="Keep both names as they are" onClick={onDone}>Leave both</button>
            </div>}
      </div>
    </div>
  );
}

// MoreMenu is a "⋯" button opening a short menu of rarer actions.
function MoreMenu({ items, disabled }: { items: { label: string; title?: string; onClick: () => void }[]; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLSpanElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);
  return (
    <span className="more-menu" ref={ref}>
      <button disabled={disabled} title="More" aria-label="More" aria-expanded={open} onClick={() => setOpen(!open)}>⋯</button>
      {open && <span className="sel-menu" role="menu">
        {items.map((it) => <button key={it.label} role="menuitem" title={it.title} onClick={() => { setOpen(false); it.onClick(); }}>{it.label}</button>)}
      </span>}
    </span>
  );
}

// returned says what "Not them" did.
const returnedNote = (name: string, r: ReturnVoiceResult) => (r.relabelled
  ? `Took that meeting out of ${name}'s voice; the speaker is unnamed again and waits under “Who's this?”.`
  : `Took that meeting out of ${name}'s voice. Its speaker labels were left as they were${r.why ? `: ${r.why}` : '.'}`);

// VoiceProfileCard is one person's voice profile: its clip and tile, the
// meetings it learnt from, and its edits.
// forgetNames is every name Delete voice print blocks, quoted: the
// profile's own and the old names it kept when renamed (deleting a
// profile renamed from a person's name blocked that person's name too).
const forgetNames = (p: VoiceProfile) => {
  const all = [p.name, ...(p.aliases ?? [])].map((n) => `'${n}'`);
  return all.length === 1 ? `the name ${all[0]}` : `the names ${all.slice(0, -1).join(', ')} and ${all[all.length - 1]}`;
};

export function VoiceProfileCard({ p, editable, a }: { p: VoiceProfile; editable: boolean; a: Act }) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState<'forget' | 'delete' | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const relabel = useRelabel();
  const [alsoMeetings, setAlsoMeetings] = useState(true);
  const [done, setDone] = useState<RelabelResult | null>(null);
  const key = `r/${p.id}`;
  const busy = a.busy === key;
  const v = (op: string, extra: Partial<VoiceArgs>, done = '') => a.act(key, () => api.recorderVoice(op, { model: p.model, id: p.id, ...extra }), done);
  // relabelMeetings puts the profile's name on its meetings' labels, in
  // the recorder's files and Musubi's copies, with no model calls.
  const relabelMeetings = async () => {
    const r = await api.relabelProfileMeetings(p.model, p.id);
    setDone(relabelled(r) > 0 ? r : null);
    return r;
  };
  const rename = (n: string) => a.act(key, async () => {
    await api.recorderVoice('rename_voice', { model: p.model, id: p.id, name: n });
    if (relabel && alsoMeetings && p.meetings > 0) await relabelMeetings();
  }, `Renamed to ${n}.`);
  const undo = (r: RelabelResult) => a.act(key, async () => { await api.undoRelabel(r.batch); setDone(null); }, 'Put the old name back on those meetings.');
  const heard = p.heard ?? [], out = p.takenOut ?? [];
  const samples = useProfileSamples(p);
  // Which of its meetings Musubi has, asked once the meetings are open.
  const found = useItemsExist(open ? [...heard, ...out].map((h) => recordingItem(h.session)) : []);
  const from = [p.fromTiles ? `own tile in ${p.fromTiles}` : '', p.fromNamed ? `named by you in ${p.fromNamed}` : ''].filter(Boolean).join(', ');
  const flagged = heard.filter((h) => odd(h, heard.length)).length;
  const confirmRow = (text: string, button: string, go: () => void, keep: () => void): ReactNode => (
    <div className="confirm-row"><span className="small">{text}</span><button className="primary" disabled={busy} title={text} onClick={go}>{button}</button><button title="Change nothing" onClick={keep}>Keep</button></div>
  );
  return (
    <div className="voice-profile">
      <div className="voice-row">
        <Play clip={p.clip} />
        <TileImg tile={p.face} size={44} title={`${p.name} as the meeting window showed them`} />
        <div className="voice-body">
          {editing ? <>
            <NameForm busy={busy} start={p.name} button="Save" onName={(n) => { setEditing(false); rename(n); }} onCancel={() => setEditing(false)} />
            {relabel && p.meetings > 0 && <label className="small"><input type="checkbox" checked={alsoMeetings} onChange={(e) => setAlsoMeetings(e.target.checked)} /> Also rename them in the {p.meetings} {p.meetings === 1 ? 'meeting' : 'meetings'} where their voice was recognised</label>}
          </> : (
            <div><b>Voice: <FullName name={p.name} /></b>
              {flagged > 0 && <span className="badge warn-badge" title="A meeting sounds unlike the rest: open the meetings to check"> {flagged} to check</span>}
              {!p.inUse && !p.startedOver && p.meetings > 0 && <span className="badge" title="Heard in one meeting: it names nobody until heard in another, or you confirm the clip"> not used yet</span>}
            </div>
          )}
          <div className="dim small">
            {p.startedOver ? 'Nothing learnt yet: learns from new meetings' : <>{p.meetings} {p.meetings === 1 ? 'meeting' : 'meetings'}{from && ` (${from})`} · last heard {day(p.lastHeard)}</>}
            {p.rejected > 0 && <> · {p.rejected} {p.rejected === 1 ? 'voice' : 'voices'} you said aren&apos;t them</>}
            <SamplesSummary s={samples} />
            {(heard.length > 0 || out.length > 0) && <> · <button className="link" onClick={() => setOpen(!open)}>{open ? 'hide meetings' : 'meetings'}</button></>}
          </div>
        </div>
        {editable && !editing && !confirming && (
          <div className="voice-actions">
            {!p.inUse && !p.startedOver && p.meetings > 0 && <button className="primary" disabled={busy} title={`The clip is ${p.name}: this voice starts naming speakers now, without waiting to be heard in another meeting. What it learnt is kept.`} onClick={() => v('confirm_voice', {}, `${p.name}'s voice now names speakers.`)}>Confirm</button>}
            <button disabled={busy} title={`Change the name on this voice. What it learnt is kept, and the old name stays known for it${relabel ? '; you can also rename them in the meetings where their voice was recognised' : ''}`} onClick={() => setEditing(true)}>Rename</button>
            {relabel && oldNames(p).length > 0 && p.meetings > 0 && <button disabled={busy} title={`Put “${p.name}” on the speaker labels that still say ${oldNames(p).map((n) => `“${n}”`).join(' or ')}, in the meetings where this voice was recognised`} onClick={() => a.act(key, relabelMeetings)}>Rename in meetings</button>}
            <MoreMenu disabled={busy} items={[
              { label: 'Delete profile', title: `Delete ${p.name}'s voice profile (you'll be asked first). If they speak in a later meeting, a new profile is learnt; the meetings this one learnt from don't rebuild it. Recordings and transcripts are unchanged.`, onClick: () => setConfirming('delete') },
              { label: 'Never learn this name', title: `Delete this profile and never learn ${forgetNames(p)} again (you'll be asked first): for window text that isn't a person, a room, or someone who asked not to have their voice kept.`, onClick: () => setConfirming('forget') },
            ]} />
          </div>
        )}
      </div>
      {done && (
        <div className="confirm-row">
          <span className="small">Renamed in {relabelled(done)} {relabelled(done) === 1 ? 'meeting' : 'meetings'}{(done.recorder?.left ?? []).length > 0 && ` · ${(done.recorder?.left ?? []).length} other ${(done.recorder?.left ?? []).length === 1 ? 'speaker' : 'speakers'} with the old name left as they were`}{Object.keys(done.skipped ?? {}).length > 0 && ` · ${Object.keys(done.skipped ?? {}).length} to take in at the next update`}</span>
          <button disabled={busy} title="Put the old name back on those meetings' speakers" onClick={() => undo(done)}>Undo</button><button title="Keep the new name in those meetings" onClick={() => setDone(null)}>OK</button>
        </div>
      )}
      {confirming === 'delete' && confirmRow(`Delete ${p.name}'s voice profile? If they speak in a later meeting, a new profile is learnt. Recordings and transcripts are unchanged.`, 'Delete profile', () => { setConfirming(null); v('delete_voice', {}, `Deleted ${p.name}'s profile.`); }, () => setConfirming(null))}
      {confirming === 'forget' && confirmRow(`Delete this profile and never learn ${forgetNames(p)} again? For window text that isn't a person, a room, or someone who asked not to have their voice kept. Recordings and transcripts are unchanged; Allow again under "Names never learnt" undoes the block.`, 'Never learn', () => { setConfirming(null); v('forget_voice', {}, `${p.name}: profile deleted, name never learnt again.`); }, () => setConfirming(null))}
      {open && (
        <div className="voice-meetings">
          {heard.map((h) => {
            const check = odd(h, heard.length);
            const thin = h.learned === false;
            return (
              <div key={h.session} className="voice-row">
                <Play clip={h.clip} />
                {(h.tiles ?? []).slice(0, 3).map((t) => <TileImg key={t.look} tile={t} size={40} title="Their tile while their voice was learnt" />)}
                <div className="voice-body">
                  <div className="small"><MeetingLink session={h.session} title={h.title || h.session.slice(0, 8)} found={found} at={h.clip?.start} /> <span className="dim">{day(h.at)} · {h.source === 'named' ? 'named by you' : 'own tile'}</span></div>
                  <div className={'small ' + (check ? 'warn' : 'dim')}>
                    {heard.length <= 1 ? 'Only meeting'
                      : h.confirmed ? `${Math.round(h.agreement * 100)}% like the other meetings · confirmed by you`
                      : thin ? 'A few seconds of voice: names the speaker, too little to learn from'
                      : h.compared === false ? 'The only meeting it learns from: nothing to compare with yet'
                      : `${Math.round(h.agreement * 100)}% like the other meetings${check ? ': may be someone else' : ''}`}
                  </div>
                  <MeetingSamplesRow s={samples} p={p} session={h.session} editable={editable} a={a} />
                </div>
                {editable && (removing === h.session
                    ? confirmRow(`Not ${p.name}: take this meeting out of ${p.name}'s voice and ask who it is under “Who's this?”`, 'Not them', () => {
                        setRemoving(null);
                        a.act(key, () => api.returnVoiceMeeting(p.model, p.id, h.session), (r) => returnedNote(p.name, r));
                      }, () => setRemoving(null))
                    : <div className="voice-actions">
                        {check && <button className="primary" disabled={busy} title={`You listened: this meeting's voice is ${p.name}'s. It stays in the voice and isn't flagged again, also after the voice is learnt again.`} onClick={() => v('confirm_voice_meeting', { session: h.session }, `Kept: that meeting is ${p.name}.`)}>Right, it&apos;s them</button>}
                        <button disabled={busy} title={`Someone else: take this whole meeting out of ${p.name}'s voice for good and ask who it is under “Who's this?” (you'll be asked first). The other meetings stay.`} onClick={() => setRemoving(h.session)}>Not them</button>
                      </div>)}
              </div>
            );
          })}
          {out.map((h) => (
            <div key={'out/' + h.session} className="voice-row taken-out">
              <DiscardedPlay p={p} session={h.session} />
              <div className="voice-body"><div className="small"><MeetingLink session={h.session} title={h.title || h.session.slice(0, 8)} found={found} /> <span className="dim">not them: not learnt from</span></div></div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// DiscardedPlay plays a discarded meeting's voice, worked out again from
// the meeting when shown.
function DiscardedPlay({ p, session }: { p: VoiceProfile; session: string }) {
  const [clip, setClip] = useState<VoiceClip | null>(null);
  useEffect(() => {
    let live = true;
    api.discardedClip(p.model, p.id, session).then((c) => { if (live) setClip(c); }, () => {});
    return () => { live = false; };
  }, [p.model, p.id, session]);
  return clip ? <Play clip={clip} /> : <span className="icon-btn play dim">…</span>;
}

// relearnNoteDays is how long the note on a relearn for new rules shows.
const relearnNoteDays = 3;

// RelearnNote says, for a few days after one, that the profiles were
// learnt again under new learning rules and what changed.
export function RelearnNote({ view }: { view: VoicesView }) {
  const r = view.relearned;
  if (!r) return null;
  const at = new Date(r.at);
  if (isNaN(at.getTime()) || Date.now() - at.getTime() > relearnNoteDays * 86400e3) return null;
  return <div className="dim small">Voice profiles updated for new rules on {at.toLocaleDateString()}: {r.changed} changed, {r.added} new.</div>;
}

// AllVoices is every voice profile the recorder has (Tomoe's Voices
// panel): find one by name, see which are confirmed or not used yet, and
// open any to rename, confirm, delete its voice print or fix its meetings.
export function AllVoices({ voices }: { voices: ReturnType<typeof useVoices> }) {
  const a = useAct();
  const editable = useEditable();
  const [q, setQ] = useState('');
  const [show, setShow] = useState<'all' | 'confirmed' | 'unused' | 'check'>('all');
  const all = voices.view?.profiles ?? [];
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const flaggedOf = (p: VoiceProfile) => { const h = p.heard ?? []; return h.filter((x) => odd(x, h.length)).length; };
  const shown = all
    .filter((p) => words.every((w) => p.name.toLowerCase().includes(w)))
    .filter((p) => show === 'all' || (show === 'confirmed' ? p.confirmed : show === 'unused' ? !p.inUse : flaggedOf(p) > 0))
    .sort((x, y) => (y.lastHeard || '').localeCompare(x.lastHeard || '') || x.name.localeCompare(y.name));
  if (!voices.view) return null;
  const count = (f: (p: VoiceProfile) => boolean) => all.filter(f).length;
  return (
    <section className="all-voices">
      <div className="section-head"><span>All voices ({all.length})</span><span className="dim small">most recently heard first</span></div>
      <input className="search" placeholder="Find a voice by name" value={q} onChange={(e) => setQ(e.target.value)} />
      <div className="chips">
        {([['all', `all ${all.length}`], ['confirmed', `confirmed ${count((p) => p.confirmed)}`], ['unused', `not used yet ${count((p) => !p.inUse)}`], ['check', `to check ${count((p) => flaggedOf(p) > 0)}`]] as const).map(([k, label]) => (
          <button key={k} className={'chip' + (show === k ? ' on' : '')} onClick={() => setShow(k)}>{label}</button>
        ))}
      </div>
      {!editable && <div className="dim small">{readOnlyWhy}</div>}
      {shown.length === 0 && <div className="dim small">No voice matches.</div>}
      {shown.map((p) => <VoiceProfileCard key={p.model + '/' + p.id} p={p} editable={editable} a={a} />)}
      <NeverLearnt view={voices.view} editable={editable} a={a} />
      <ActNotes a={a} />
    </section>
  );
}

// NeverLearnt lists the names no profile learns (Never learn this name),
// each with Allow again.
function NeverLearnt({ view, editable, a }: { view: VoicesView; editable: boolean; a: Act }) {
  const [open, setOpen] = useState(false);
  const names = view.forgotten ?? [];
  if (names.length === 0) return null;
  return (
    <div className="never-learnt">
      <button className="link small" onClick={() => setOpen(!open)} title="Names the recorder never learns a voice for: window text, rooms, people who asked not to be kept">
        {open ? 'hide' : `Names never learnt (${names.length})`}
      </button>
      {open && names.map((n) => (
        <div key={n.model + '/' + n.name} className="voice-row small">
          <span className="voice-body">{n.name}</span>
          {editable && <button className="small" disabled={a.busy === 'allow/' + n.name} title={`Let the recorder learn a voice named '${n.name}' again from future meetings`}
            onClick={() => a.act('allow/' + n.name, () => api.allowVoiceName(n.model, n.name), `'${n.name}' can be learnt again.`)}>Allow again</button>}
        </div>
      ))}
    </div>
  );
}
