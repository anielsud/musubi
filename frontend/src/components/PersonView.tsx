import { useEffect, useRef, useState } from 'react';
import { api, fmtDate } from '../api';
import type { DirectoryPerson, PersonPage } from '../types';
import { Spinner } from './common';
import { Name } from './people';
import { ActNotes, ReviewItems, VoiceProfileCard, VoicesError, readOnlyWhy, useAct, useEditable, useVoices } from './voices';
import type { VoicesView } from '../types';

// PersonView is a person's page: who they are (Microsoft 365 directory),
// what they look and sound like (from Tomoe's recordings), and what the
// owner and they have in common. Owner scope only.
export function PersonView({ id, scope, onPick, onOpenItem, onTopic }: {
  id: string; scope: string; onPick: (id: string) => void;
  onOpenItem: (itemId: string, scope: string) => void; onTopic?: (id: number) => void;
}) {
  const [p, setP] = useState<PersonPage | null | undefined>(undefined);
  useEffect(() => {
    setP(undefined);
    api.personPage(id, scope).then((x) => setP(x), () => setP(null));
  }, [id, scope]);
  if (p === undefined) return <Spinner />;
  if (!p) return null;
  const d = p.directory;
  const who = (x: DirectoryPerson) => (
    <button key={x.email} className="link" onClick={() => onPick('person:' + x.email)}>{x.name || x.email}</button>
  );
  const list = (xs: DirectoryPerson[] | null, max: number) =>
    (xs ?? []).slice(0, max).map((x, i) => <span key={x.email}>{i > 0 && ', '}{who(x)}</span>);
  const place = [d?.office, d?.city, d?.country].filter(Boolean).filter((v, i, a) => a.indexOf(v) === i).join(' · ');
  return (
    <div className="person-page">
      <div className="person-top">
        {p.headshot ? <img className="headshot" src={p.headshot} alt="" /> : <div className="headshot none">{initials(p.name)}</div>}
        <div className="person-id">
          <div className="person-title">{[d?.title, d?.department].filter(Boolean).join(' · ') || p.email}</div>
          {place && <div className="dim small">{place}</div>}
          <div className="small">
            {p.email && <a href={'mailto:' + p.email}>{p.email}</a>}
            {(d?.phones ?? []).concat(d?.mobile ? [d.mobile] : []).map((ph) => <span key={ph}> · <a href={'tel:' + ph}>{ph}</a></span>)}
          </div>
          {d?.hireDate && <div className="dim small">joined {new Date(d.hireDate).toLocaleDateString(undefined, { year: 'numeric', month: 'long' })}</div>}
          {p.voice && <Voice clip={p.voice} />}
        </div>
      </div>

      {(p.chain?.length ?? 0) > 0 && <div className="small">reports to {p.chain!.slice(0, 4).map((m, i) => <span key={m.email}>{i > 0 && ' → '}{who(m)}</span>)}</div>}
      {(p.reports?.length ?? 0) > 0 && <div className="small dim">{p.reports!.length} direct report{p.reports!.length > 1 ? 's' : ''}: {list(p.reports, 12)}{p.reports!.length > 12 && ' …'}</div>}
      {(p.peers?.length ?? 0) > 0 && <div className="small dim">works alongside {list(p.peers, 8)}{p.peers!.length > 8 && ' …'}</div>}
      {(p.seenWith?.length ?? 0) > 0 && <div className="small dim" title="Who shares the most meetings with them, small meetings counting for more">commonly seen with {p.seenWith!.map((q, i) => <span key={q.id}>{i > 0 && ', '}<button className="link" onClick={() => onPick(q.id)}>{q.name}</button></span>)}</div>}

      <div className="section-head"><span>Together</span></div>
      <div className="small">{p.together > 0 ? `In ${p.together} of your meetings${p.first && !p.first.startsWith('0001') ? ` since ${fmtDate(p.first)}` : ''}.` : 'No meetings together in the record.'}</div>
      {(p.upcoming?.length ?? 0) > 0 && <>
        <div className="dim small">coming up</div>
        <div className="timeline">{p.upcoming!.map((m) => <div key={m.itemId} className="tl-row person-tl static"><span className="tl-date">{fmtDate(m.at)}</span><span className="tl-title">{m.title}</span></div>)}</div>
      </>}
      {(p.recent?.length ?? 0) > 0 && <>
        <div className="dim small">recently</div>
        <div className="timeline">{p.recent!.map((m) => (
          <button key={m.itemId} className="tl-row person-tl" onClick={() => onOpenItem(m.itemId, scope)}><span className="tl-date">{fmtDate(m.at)}</span><span className="tl-title">{m.title}</span></button>
        ))}</div>
      </>}

      {(p.topics?.length ?? 0) > 0 && <>
        <div className="section-head"><span>Comes up in</span></div>
        <div className="chips">{p.topics!.map((t) => (
          <button key={t.id} className="chip" onClick={() => onTopic?.(t.id)} title={`${t.claims} of their points`}>{t.label}</button>
        ))}</div>
      </>}

      {(p.nearby?.length ?? 0) > 0 && <>
        <div className="section-head" title="Threads linked to theirs that they aren't in"><span>Nearby</span></div>
        <ul className="status-list">{p.nearby!.map((t) => (
          <li key={t.id}>
            <button className="link" onClick={() => onTopic?.(t.id)}>{t.label}</button>
            <span className="dim small"> · {nearbyWhy(t)}{t.links > 1 ? ` (and ${t.links - 1} more of theirs)` : ''}</span>
          </li>
        ))}</ul>
      </>}

      {(p.todos?.length ?? 0) > 0 && <>
        <div className="section-head"><span>Their open to-dos</span></div>
        <ul className="status-list">{p.todos!.map((t) => (
          <li key={t.id}>{t.text}<span className="dim small"> · {t.itemTitle}{t.due ? (/due/i.test(t.due) ? ` · ${t.due}` : ` · due ${t.due}`) : ''}{t.overdue ? ' · overdue' : ''}</span></li>
        ))}</ul>
      </>}

      <PersonVoice names={[p.name, ...p.names]} />

      {p.names.length > 1 && <div className="dim small">also known as {p.names.filter((n) => n !== p.name.toLowerCase()).slice(0, 6).map((n, i) => <span key={n}>{i > 0 && ', '}<Name name={n} /></span>)}</div>}
    </div>
  );
}

// PersonVoice is the recorder's voice profile for this person, matched by
// name, with "Same person?" pairs it is in.
function PersonVoice({ names }: { names: string[] }) {
  const { view, err } = useVoices();
  const a = useAct();
  const editable = useEditable();
  const fold = (s: string) => s.trim().toLowerCase().replace(/\s+/g, ' ');
  const want = new Set(names.map(fold));
  const profiles = (view?.profiles ?? []).filter((v) => want.has(fold(v.name)));
  if (err) return <><div className="section-head"><span>Voice</span></div><VoicesError err={err} /></>;
  if (!view || profiles.length === 0) return null;
  const ids = new Set(profiles.map((x) => x.id));
  const pairs: VoicesView = { ...view, namedByVoice: [], doubts: [], unknown: [], pairs: (view.pairs ?? []).filter((x) => ids.has(x.a) || ids.has(x.b)) };
  const all = (view.profiles ?? []).map((x) => x.name);
  return (
    <>
      <div className="section-head"><span>Voice</span></div>
      {!editable && <div className="dim small">{readOnlyWhy}</div>}
      {profiles.map((v) => <VoiceProfileCard key={v.model + '/' + v.id} p={v} names={all} editable={editable} a={a} />)}
      <ReviewItems view={pairs} editable={editable} a={a} />
      <ActNotes a={a} />
    </>
  );
}

// nearbyWhy says how a nearby thread is linked to the closest of theirs.
function nearbyWhy(t: NonNullable<PersonPage['nearby']>[number]) {
  switch (t.kind) {
    case 'same': return t.via?.length ? `same ${t.via.join(', ')} as ${t.from}` : `same subject as ${t.from}`;
    case 'shared': return `shares points with ${t.from}`;
    case 'together': return `discussed alongside ${t.from}`;
    default: return `similar to ${t.from}`;
  }
}

export function initials(name: string) {
  return name.split(/[\s,]+/).filter(Boolean).slice(0, 2).map((w) => w[0]?.toUpperCase()).join('');
}

// Voice plays the clip of the person's voice from the Tomoe recording.
function Voice({ clip }: { clip: NonNullable<PersonPage['voice']> }) {
  const ref = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const play = () => {
    const a = ref.current;
    if (!a) return;
    if (playing) { a.pause(); setPlaying(false); return; }
    a.currentTime = clip.start;
    a.play().then(() => setPlaying(true), () => setPlaying(false));
  };
  return (
    <div className="voice small">
      <audio ref={ref} src={`/replay-audio/${clip.session}`} preload="none"
        onTimeUpdate={(e) => { if (e.currentTarget.currentTime >= clip.end) { e.currentTarget.pause(); setPlaying(false); } }}
        onPause={() => setPlaying(false)} />
      <button className="link" onClick={play}>{playing ? '■ stop' : '▶ voice'}</button>
      <span className="dim"> {Math.round(clip.end - clip.start)} s from {clip.title}, {fmtDate(clip.at)}: “{clip.text.length > 90 ? clip.text.slice(0, 90) + '…' : clip.text}”</span>
    </div>
  );
}
