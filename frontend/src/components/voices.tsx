import { useEffect, useRef, useState, type ReactNode } from 'react';
import { api, clipURL, on, tileURL } from '../api';
import type { NamedByVoice, SameNamePair, TileRef, UnknownVoice, VoiceArgs, VoiceClip, VoiceDoubt, VoicePair, VoiceProfile, VoicesView } from '../types';
import { Spinner } from './common';

// Voices and speakers from the recorder's voice store: the review queue
// (named by voice, window or voice, who's this, same person, two profiles
// of one name) and voice
// profiles, with each clip and the person's tile. Edits go through the
// recorder and are offered only once Musubi's recorder is of record.

export const readOnlyWhy = 'Corrections work here once Musubi records your meetings (Settings → Recording); until then they are made in Tomoe.';

// A meeting less like the profile's other meetings than this is flagged:
// across saved meetings, 95% of one person's meetings agree at 0.68 or more.
const agreeFlag = 0.6;

export function useVoices() {
  const [view, setView] = useState<VoicesView | null>(null);
  const [err, setErr] = useState('');
  const load = () => api.voices().then((v) => { setView(v); setErr(''); }, (e) => { setView(null); setErr(String(e)); });
  useEffect(() => {
    load();
    const offs = [on('voices:changed', load), on('prefs:changed', load), on('update:done', load)];
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
  const act = (key: string, f: () => Promise<unknown>, done = '') => {
    setBusy(key); setErr(''); setNote('');
    f().then(() => { setBusy(null); setNote(done); }, (e) => { setBusy(null); setErr(String(e)); });
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

export function Play({ clip }: { clip: VoiceClip }) {
  const [on, setOn] = useState(false);
  const el = useRef<HTMLAudioElement | null>(null);
  useEffect(() => () => { if (playing === el.current) { playing?.pause(); playing = null; } }, []);
  const toggle = () => {
    if (on) { el.current?.pause(); setOn(false); return; }
    playing?.pause();
    stopPlaying?.();
    const a = new Audio(clipURL(clip));
    el.current = a;
    playing = a;
    stopPlaying = () => setOn(false);
    a.onended = () => setOn(false);
    a.onpause = () => setOn(false);
    a.play().then(() => setOn(true), () => setOn(false));
  };
  return <button className="icon-btn play" title="Play a few seconds" onClick={toggle}>{on ? '■' : '▶'}</button>;
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
function NameForm({ busy, onName, onCancel, list, start = '', button = 'Name' }: {
  busy: boolean; onName: (n: string) => void; onCancel?: () => void; list?: string; start?: string; button?: string;
}) {
  const [n, setN] = useState(start);
  return (
    <form className="voice-name" onSubmit={(e) => { e.preventDefault(); if (n.trim()) onName(n.trim()); }}>
      <input value={n} list={list} autoFocus={!!onCancel} placeholder="Who is it?" onChange={(e) => setN(e.target.value)} disabled={busy} />
      <button className="primary" type="submit" disabled={busy || !n.trim()}>{button}</button>
      {onCancel && <button type="button" onClick={onCancel}>Cancel</button>}
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
  const sp = (op: string, s: string, label: string, name: string, done: string) => a.act(`${s}/${label}`, () => api.recorderSpeaker(op, s, label, name), done);
  const where = (x: { title: string; at: string }) => (showTitle ? ` in ${x.title} · ${day(x.at)}` : '');
  return (
    <>
      {named.length > 0 && <>
        <div className="review-head">Named by voice</div>
        <p className="dim small">A voice profile named these speakers, not the meeting window or you. Play them and say if they&apos;re right: either answer teaches the profile.</p>
        {named.map((n) => <NamedRow key={`n/${n.session}/${n.label}`} n={n} where={where(n)} editable={editable} busy={a.busy === `${n.session}/${n.label}`}
          onRight={() => sp('rename_speaker', n.session, n.label, n.name, `Confirmed ${n.name}.`)}
          onNotThem={() => sp('not_them', n.session, n.label, '', `${n.name}'s profile won't name that voice again.`)}
          onName={(name) => sp('rename_speaker', n.session, n.label, name, `Named them ${name}.`)}
          onSkip={() => sp('skip_voice_name', n.session, n.label, '', 'Won’t ask about that one again.')} />)}
      </>}
      {doubts.length > 0 && <>
        <div className="review-head">Window or voice?</div>
        <p className="dim small">The meeting window named these speakers, but the voice sounds a little like someone else. Until you say, they keep the window&apos;s name.</p>
        {doubts.map((d) => <DoubtRow key={`d/${d.session}/${d.label}`} d={d} where={where(d)} editable={editable} busy={a.busy === `${d.session}/${d.label}`}
          onName={(name) => sp('rename_speaker', d.session, d.label, name, `Named them ${name}.`)}
          onSkip={() => sp('skip_voice_doubt', d.session, d.label, '', 'Won’t ask about that one again; it keeps the window’s name.')} />)}
      </>}
      {unknown.length > 0 && <>
        <div className="review-head">Who&apos;s this?</div>
        <p className="dim small">Voices nobody has named. Naming one names them in that meeting and teaches their profile.</p>
        {unknown.map((u) => <UnknownRow key={`u/${u.session}/${u.label}`} u={u} where={where(u)} editable={editable} busy={a.busy === `${u.session}/${u.label}`}
          onName={(name) => sp('rename_speaker', u.session, u.label, name, `Named ${bare(u.label)} ${name}.`)} />)}
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

function NamedRow({ n, where, editable, busy, onRight, onNotThem, onName, onSkip }: {
  n: NamedByVoice; where: string; editable: boolean; busy: boolean; onRight: () => void; onNotThem: () => void; onName: (n: string) => void; onSkip: () => void;
}) {
  const [naming, setNaming] = useState(false);
  return (
    <div className="voice-row">
      <Play clip={n.clip} />
      <div className="voice-body">
        <div><b>{n.name}</b> <span className="dim small">as {bare(n.label)}{where} · spoke {spoke(n.seconds)}</span></div>
        <div className="dim small">{Math.round(n.similarity * 100)}% like {n.name}&apos;s profile{n.window && <> · the meeting window said {n.window}</>}</div>
      </div>
      {editable && (naming ? <NameForm busy={busy} onName={onName} onCancel={() => setNaming(false)} /> : (
        <div className="voice-actions">
          <button disabled={busy} onClick={onRight}>Right</button>
          <button disabled={busy} onClick={onNotThem}>Not them</button>
          <button disabled={busy} onClick={() => setNaming(true)}>Someone else…</button>
          <button disabled={busy} onClick={onSkip} title="Noise, crosstalk or too short: stop asking, and teach the profile nothing">Can&apos;t tell</button>
        </div>
      ))}
    </div>
  );
}

function DoubtRow({ d, where, editable, busy, onName, onSkip }: {
  d: VoiceDoubt; where: string; editable: boolean; busy: boolean; onName: (n: string) => void; onSkip: () => void;
}) {
  const [naming, setNaming] = useState(false);
  return (
    <div className="voice-row">
      <Play clip={d.clip} />
      <div className="voice-body">
        <div><b>{d.window}?</b> <span className="dim small">as {bare(d.label)}{where} · spoke {spoke(d.seconds)}</span></div>
        <div className="dim small">The window said {d.window}; the voice sounds {d.similarity >= 0.7 ? '' : 'a little '}like {d.name} ({Math.round(d.similarity * 100)}%){d.noProfile && <>, and {d.window} has no voice profile yet</>}</div>
      </div>
      {editable && (naming ? <NameForm busy={busy} onName={onName} onCancel={() => setNaming(false)} /> : (
        <div className="voice-actions">
          <button disabled={busy} onClick={() => onName(d.window)}>It&apos;s {d.window}</button>
          <button disabled={busy} onClick={() => onName(d.name)}>It&apos;s {d.name}</button>
          <button disabled={busy} onClick={() => setNaming(true)}>Someone else…</button>
          <button disabled={busy} onClick={onSkip} title="Keep the window's name, stop asking, and teach no profile">Can&apos;t tell</button>
        </div>
      ))}
    </div>
  );
}

function UnknownRow({ u, where, editable, busy, onName }: { u: UnknownVoice; where: string; editable: boolean; busy: boolean; onName: (n: string) => void }) {
  return (
    <div className="voice-row">
      <Play clip={u.clip} />
      <div className="voice-body">
        <div><b>{bare(u.label)}</b> <span className="dim small">{where.replace(/^ in /, 'in ')} · spoke {spoke(u.seconds)}</span></div>
        {u.soundsLike && (
          <div className="dim small">
            {(u.similarity ?? 0) >= 0.7 ? 'Sounds like' : 'Sounds a little like'} {u.soundsLike} ({Math.round((u.similarity ?? 0) * 100)}%)
            {editable && <> <button className="link" disabled={busy} onClick={() => onName(u.soundsLike!)}>It&apos;s {u.soundsLike}</button></>}
          </div>
        )}
      </div>
      {editable && <NameForm busy={busy} onName={onName} />}
    </div>
  );
}

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
          <button disabled={busy} onClick={() => same(a, b)}>Same: {a.name}</button>
          <button disabled={busy} onClick={() => same(b, a)}>Same: {b.name}</button>
          <button disabled={busy} onClick={() => act.act(key, () => api.recorderVoice('keep_apart', { model: pair.model, id: pair.a, other: pair.b }))}>Different people</button>
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
          <button disabled={busy} onClick={() => act.act(key, () => api.recorderVoice('merge_voices', { model: pair.model, id: into.id, other: from.id }), `The two ${pair.name} profiles are now one.`)}>Same person</button>
          <button disabled={busy} onClick={() => { onApart(); act.act(key, () => api.recorderVoice('keep_apart', { model: pair.model, id: pair.a, other: pair.b }), 'Kept apart.'); }}>Different people</button>
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
                <span key={x.p.id}><Play clip={x.clip} /> <button disabled={busy} onClick={() => setWhich(x.p)}>Rename the one in {meetings(x.n)}…</button></span>
              ))}
              <button disabled={busy} onClick={onDone}>Leave both</button>
            </div>}
      </div>
    </div>
  );
}

// MoveForm picks where a meeting's voice goes: another profile (by ID,
// so two of one name are told apart) or a new profile with a name.
function MoveForm({ p, profiles, busy, onMove, onCancel }: {
  p: VoiceProfile; profiles: VoiceProfile[]; busy: boolean; onMove: (to: { to?: string; name?: string }, label: string) => void; onCancel: () => void;
}) {
  const others = profiles.filter((x) => x.model === p.model && x.id !== p.id).sort((x, y) => x.name.localeCompare(y.name) || y.meetings - x.meetings);
  const dup = new Set(others.filter((x, i) => others.some((y, j) => j !== i && y.name === x.name)).map((x) => x.name));
  const [to, setTo] = useState('');
  const [name, setName] = useState('');
  const isNew = to === 'new';
  const ok = isNew ? !!name.trim() : !!to;
  const go = () => {
    if (isNew) onMove({ name: name.trim() }, `a new profile, ${name.trim()}`);
    else { const x = others.find((o) => o.id === to); if (x) onMove({ to: x.id }, x.name); }
  };
  return (
    <form className="voice-name" onSubmit={(e) => { e.preventDefault(); if (ok) go(); }}>
      <select value={to} autoFocus disabled={busy} onChange={(e) => setTo(e.target.value)} aria-label="Move to">
        <option value="">Move to…</option>
        {others.map((x) => <option key={x.id} value={x.id}>{x.name}{dup.has(x.name) ? ` (${x.meetings} ${x.meetings === 1 ? 'meeting' : 'meetings'}, last heard ${day(x.lastHeard)})` : ''}</option>)}
        <option value="new">A new profile named…</option>
      </select>
      {isNew && <input value={name} placeholder="Who is it?" onChange={(e) => setName(e.target.value)} disabled={busy} />}
      <button className="primary" type="submit" disabled={busy || !ok}>Move</button>
      <button type="button" onClick={onCancel}>Cancel</button>
    </form>
  );
}

// VoiceProfileCard is one person's voice profile: its clip and tile, the
// meetings it learnt from, and its edits.
export function VoiceProfileCard({ p, profiles, editable, a }: { p: VoiceProfile; profiles: VoiceProfile[]; editable: boolean; a: Act }) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState<'forget' | 'startOver' | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [moving, setMoving] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const key = `r/${p.id}`;
  const busy = a.busy === key;
  const v = (op: string, extra: Partial<VoiceArgs>, done = '') => a.act(key, () => api.recorderVoice(op, { model: p.model, id: p.id, ...extra }), done);
  const heard = p.heard ?? [], out = p.takenOut ?? [];
  const from = [p.fromTiles ? `own tile in ${p.fromTiles}` : '', p.fromNamed ? `named by you in ${p.fromNamed}` : ''].filter(Boolean).join(', ');
  const flagged = heard.filter((h) => heard.length > 1 && h.agreement < agreeFlag).length;
  const confirmRow = (text: string, button: string, go: () => void, keep: () => void): ReactNode => (
    <div className="confirm-row"><span className="small">{text}</span><button className="primary" disabled={busy} onClick={go}>{button}</button><button onClick={keep}>Keep</button></div>
  );
  return (
    <div className="voice-profile">
      <div className="voice-row">
        <Play clip={p.clip} />
        <TileImg tile={p.face} size={44} title={`${p.name} as the meeting window showed them`} />
        <div className="voice-body">
          {editing ? <NameForm busy={busy} start={p.name} button="Save" onName={(n) => { setEditing(false); v('rename_voice', { name: n }, `Renamed to ${n}.`); }} onCancel={() => setEditing(false)} /> : (
            <div><b>Voice: {p.name}</b>
              {flagged > 0 && <span className="badge warn-badge" title="A meeting sounds unlike the rest: open the meetings to check"> {flagged} to check</span>}
              {!p.inUse && !p.startedOver && p.meetings > 0 && <span className="badge" title="Heard in one meeting: it names nobody until heard in another, or you confirm the clip"> not used yet</span>}
            </div>
          )}
          <div className="dim small">
            {p.startedOver ? 'Nothing learnt yet: learns from new meetings' : <>{p.meetings} {p.meetings === 1 ? 'meeting' : 'meetings'}{from && ` (${from})`} · last heard {day(p.lastHeard)}</>}
            {p.rejected > 0 && <> · {p.rejected} {p.rejected === 1 ? 'voice' : 'voices'} you said aren&apos;t them</>}
            {(heard.length > 0 || out.length > 0) && <> · <button className="link" onClick={() => setOpen(!open)}>{open ? 'hide meetings' : 'meetings'}</button></>}
          </div>
        </div>
        {editable && !editing && !confirming && (
          <div className="voice-actions">
            {!p.inUse && !p.startedOver && p.meetings > 0 && <button className="primary" disabled={busy} title="The clip is them: use this profile now" onClick={() => v('confirm_voice', {}, `${p.name}'s voice now names speakers.`)}>Confirm</button>}
            <button disabled={busy} onClick={() => setEditing(true)}>Rename</button>
            <button disabled={busy || p.startedOver} onClick={() => setConfirming('startOver')}>Start over</button>
            <button disabled={busy} onClick={() => setConfirming('forget')}>Forget</button>
          </div>
        )}
      </div>
      {confirming === 'forget' && confirmRow('Forget this voice for good? The name won’t be learnt again unless you name someone it.', 'Forget', () => { setConfirming(null); v('forget_voice', {}, `Forgot ${p.name}'s voice.`); }, () => setConfirming(null))}
      {confirming === 'startOver' && confirmRow('Drop what’s learnt so far and learn from new meetings only?', 'Start over', () => { setConfirming(null); v('start_over', {}, `${p.name} starts over.`); }, () => setConfirming(null))}
      {open && (
        <div className="voice-meetings">
          {heard.map((h) => {
            const odd = heard.length > 1 && h.agreement < agreeFlag;
            return (
              <div key={h.session} className="voice-row">
                <Play clip={h.clip} />
                {(h.tiles ?? []).slice(0, 3).map((t) => <TileImg key={t.look} tile={t} size={40} title="Their tile while their voice was learnt" />)}
                <div className="voice-body">
                  <div className="small">{h.title || h.session.slice(0, 8)} <span className="dim">{day(h.at)} · {h.source === 'named' ? 'named by you' : 'own tile'}</span></div>
                  <div className={'small ' + (odd ? 'warn' : 'dim')}>{heard.length > 1 ? `${Math.round(h.agreement * 100)}% like the other meetings${odd ? ': may be someone else' : ''}` : 'Only meeting'}</div>
                </div>
                {editable && (moving === h.session
                  ? <MoveForm p={p} profiles={profiles} busy={busy} onMove={(to, label) => { setMoving(null); v('move_voice_meeting', { session: h.session, ...to }, `Moved that meeting to ${label}.`); }} onCancel={() => setMoving(null)} />
                  : removing === h.session
                    ? confirmRow(`Not ${p.name}: discard what this meeting taught, for good?`, 'Discard', () => { setRemoving(null); v('remove_voice_meeting', { session: h.session }, 'Discarded that meeting.'); }, () => setRemoving(null))
                    : <div className="voice-actions">
                        <button disabled={busy} title="This meeting's voice is someone else: teach their profile instead" onClick={() => setMoving(h.session)}>Someone else…</button>
                        <button disabled={busy} title="Not them, and you can't tell who (or noise): throw it away" onClick={() => setRemoving(h.session)}>Discard</button>
                      </div>)}
              </div>
            );
          })}
          {out.map((h) => (
            <div key={'out/' + h.session} className="voice-row taken-out">
              <DiscardedPlay p={p} session={h.session} />
              <div className="voice-body"><div className="small">{h.title || h.session.slice(0, 8)} <span className="dim">discarded: not learnt from</span></div></div>
              {editable && (moving === h.session
                ? <MoveForm p={p} profiles={profiles} busy={busy} onMove={(to, label) => { setMoving(null); v('move_voice_meeting', { session: h.session, ...to }, `Moved that meeting to ${label}.`); }} onCancel={() => setMoving(null)} />
                : <div className="voice-actions"><button disabled={busy} onClick={() => setMoving(h.session)}>Someone else…</button></div>)}
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

// LearnButton teaches the profiles from every saved meeting.
export function LearnButton({ a }: { a: Act }) {
  return (
    <button disabled={a.busy !== null} onClick={() => a.act('learn', () => api.learnSaved(), 'Learnt from every saved meeting.')}>
      {a.busy === 'learn' ? <><Spinner /> Learning…</> : 'Learn from saved meetings'}
    </button>
  );
}

// AllVoices is every voice profile the recorder has (Tomoe's Voices
// panel): find one by name, see which are confirmed or not used yet, and
// open any to rename, confirm, start over, forget or fix its meetings.
export function AllVoices({ voices }: { voices: ReturnType<typeof useVoices> }) {
  const a = useAct();
  const editable = useEditable();
  const [q, setQ] = useState('');
  const [show, setShow] = useState<'all' | 'confirmed' | 'unused' | 'check'>('all');
  const all = voices.view?.profiles ?? [];
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  const flaggedOf = (p: VoiceProfile) => { const h = p.heard ?? []; return h.filter((x) => h.length > 1 && x.agreement < agreeFlag).length; };
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
      {shown.map((p) => <VoiceProfileCard key={p.model + '/' + p.id} p={p} profiles={all} editable={editable} a={a} />)}
      <ActNotes a={a} />
    </section>
  );
}
