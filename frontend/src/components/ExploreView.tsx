import { useEffect, useRef, useState } from 'react';
import { api, fmtDate, kindIcon } from '../api';
import type { EntityRef, Graph, Hit, PersonRow } from '../types';
import { PersonView, initials } from './PersonView';
import { Empty, HitRow, LabelBadge, Spinner } from './common';
import { GraphView } from './GraphView';
import { ClaimResults, TopicsPane } from './Knowledge';
import { CorrectionsView } from './CorrectionsView';
import { ActNotes, AllVoices, RelearnNote, ReviewItems, VoicesError, openReviews, readOnlyWhy, useAct, useEditable, useVoices } from './voices';

interface Props {
  scope: string;
  focus: string | null;
  setFocus: (id: string | null) => void;
  onOpen: (h: Hit, scope: string) => void;
  onOpenItem: (itemId: string, scope: string) => void;
  // topicReq opens a topic's page (from Ask, Live or prep).
  topicReq?: { id: number; n: number } | null;
  // correctReq opens Corrections on a word (from a text selection).
  correctReq?: { variant: string; n: number } | null;
}

export function ExploreView({ scope, focus, setFocus, onOpen, onOpenItem, topicReq, correctReq }: Props) {
  const [q, setQ] = useState('');
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [ents, setEnts] = useState<EntityRef[]>([]);
  const [graph, setGraph] = useState<Graph | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [trail, setTrail] = useState<EntityRef[]>([]);
  const [mode, setMode] = useState<'topics' | 'search' | 'corrections' | 'people' | 'voices'>('topics');
  const [topicWant, setTopicWant] = useState<{ id: number; n: number } | null>(null);
  const openTopicHere = (id: number) => { setFocus(null); setTopicWant({ id, n: Date.now() }); setMode('topics'); };
  const [lastQ, setLastQ] = useState('');
  const openSource = (itemId: string, chunkId: number) => onOpen({ itemId, chunkId } as unknown as Hit, scope);
  const voices = useVoices();
  const reviews = openReviews(voices.view);

  useEffect(() => {
    if (topicReq) setMode('topics');
  }, [topicReq]);
  useEffect(() => {
    if (correctReq && scope === 'owner') setMode('corrections');
  }, [correctReq]);

  useEffect(() => {
    if (!focus) {
      setGraph(undefined);
      return;
    }
    setGraph(undefined);
    api.graph(focus, scope).then((g) => {
      setGraph(g);
      if (g) setTrail((t) => [...t.filter((x) => x.id !== g.entity.id), g.entity].slice(-6));
    }, (e) => setErr(String(e)));
  }, [focus, scope]);

  const run = async (text: string) => {
    if (!text.trim()) return;
    setMode('search');
    setLastQ(text);
    setBusy(true);
    setErr('');
    try {
      const [h, e] = await Promise.all([api.search(text, scope, 15), api.entities(text, scope)]);
      setHits(h ?? []);
      setEnts(e ?? []);
      if (e && e.length === 1) setFocus(e[0].id);
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (focus && mode === 'topics') setMode(focus.startsWith('person:') && scope === 'owner' ? 'people' : 'search');
  return (
    <div className="view explore">
      <div className="segmented">
        <button className={mode === 'topics' ? 'on' : ''} onClick={() => { setMode('topics'); setFocus(null); }}>Topics</button>
        {scope === 'owner' && <button className={mode === 'people' ? 'on' : ''} onClick={() => { setMode('people'); setFocus(null); }}>People</button>}
        {scope === 'owner' && <button className={mode === 'voices' ? 'on' : ''} onClick={() => { setMode('voices'); setFocus(null); }} title="Voice profiles, and voices to name or check">Voices{reviews > 0 && <span className="count" title="Voices to check"> {reviews}</span>}</button>}
        <button className={mode === 'search' ? 'on' : ''} onClick={() => setMode('search')}>Search</button>
        {scope === 'owner' && <button className={mode === 'corrections' ? 'on' : ''} onClick={() => { setMode('corrections'); setFocus(null); }} title="Misheard or misspelled names and terms to confirm or reject">Corrections</button>}
      </div>
      {mode === 'topics' && <TopicsPane scope={scope} onSource={openSource} want={topicWant ?? topicReq} />}
      {mode === 'people' && scope === 'owner' && (focus ? (
        <section className="entity">
          <div className="entity-head">
            <button className="link" onClick={() => setFocus(null)}>← People</button>
          </div>
          {graph && <div className="entity-name">{graph.entity.name}</div>}
          <PersonView id={focus} scope={scope} onPick={setFocus} onOpenItem={onOpenItem} onTopic={openTopicHere} />
        </section>
      ) : <PeoplePane onPick={setFocus} />)}
      {mode === 'voices' && scope === 'owner' && <><VoiceReviews voices={voices} /><AllVoices voices={voices} /></>}
      {mode === 'corrections' && scope === 'owner' && <CorrectionsView onSource={openSource} start={correctReq} />}
      {mode === 'search' && <>
      <form className="lookup" onSubmit={(e) => { e.preventDefault(); run(q); }}>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search passages, people, customers…" />
        {busy && <Spinner />}
      </form>
      {err && <div className="error small">{err}</div>}

      {trail.length > 1 && (
        <div className="trail">
          {trail.map((t) => (
            <button key={t.id} className={'crumb' + (t.id === focus ? ' on' : '')} onClick={() => setFocus(t.id)}>{t.name}</button>
          ))}
        </div>
      )}

      {ents.length > 1 && (
        <div className="chips">
          {ents.map((e) => (
            <button key={e.id} className="chip" onClick={() => setFocus(e.id)} title={e.id}>◉ {e.name} <span className="dim">{e.type}</span></button>
          ))}
        </div>
      )}

      {focus && (
        <section className="entity">
          {graph === undefined && <Spinner />}
          {graph === null && <Empty>Not visible in this scope.</Empty>}
          {graph && (
            <>
              <div className="entity-head">
                <div>
                  <div className="entity-name">{graph.entity.name}</div>
                  <div className="dim">{graph.entity.type} · {graph.entity.items} items · last {fmtDate(graph.entity.lastSeen)}</div>
                </div>
                <button className="icon-btn" onClick={() => setFocus(null)} title="Close">✕</button>
              </div>
              {graph.entity.type === 'person' && scope === 'owner' && <PersonView id={graph.entity.id} scope={scope} onPick={setFocus} onOpenItem={onOpenItem} onTopic={openTopicHere} />}
              {(graph.neighbors?.length ?? 0) > 0 ? (
                <GraphView graph={graph} onPick={(n) => setFocus(n.id)} />
              ) : (
                <Empty>No connections yet.</Empty>
              )}
              <div className="section-head"><span>Timeline</span></div>
              <div className="timeline">
                {(graph.items ?? []).map((it) => (
                  <button key={it.id} className="tl-row" onClick={() => onOpenItem(it.id, scope)}>
                    <span className="tl-date">{fmtDate(it.occurredAt)}</span>
                    <span className="tl-kind">{kindIcon[it.kind] ?? '·'}</span>
                    <span className="tl-title">{it.title}</span>
                    <span className="tl-meta">{it.participant ? 'attended' : `${it.mentions} mention${it.mentions === 1 ? '' : 's'}`}</span>
                    <LabelBadge label={it.label} compact />
                  </button>
                ))}
              </div>
            </>
          )}
        </section>
      )}

      {hits && (
        <section>
          <div className="section-head"><span>Passages</span></div>
          {hits.length === 0 && <Empty>Nothing found.</Empty>}
          {hits.map((h) => <HitRow key={h.chunkId} hit={h} onOpen={(x) => onOpen(x, scope)} />)}
        </section>
      )}

      {lastQ && <ClaimResults q={lastQ} scope={scope} onSource={openSource} />}

      {!hits && !focus && (
        <Empty>Search your meetings, or name a person or customer to see who and what they connect to.</Empty>
      )}
      </>}
    </div>
  );
}

// VoiceReviews is the open voice review across meetings, from the
// recorder: voices to name or confirm, and profiles that may be one person.
function VoiceReviews({ voices }: { voices: ReturnType<typeof useVoices> }) {
  const a = useAct();
  const editable = useEditable();
  const [open, setOpen] = useState(true);
  const n = openReviews(voices.view);
  if (voices.err) return <VoicesError err={voices.err} />;
  if (!voices.view) return null;
  return (
    <section className="voice-reviews">
      <div className="section-head">
        <span>Voices to check ({n})</span>
        <span>
          {n > 0 && <button className="link" onClick={() => setOpen(!open)}>{open ? 'hide' : 'show'}</button>}
        </span>
      </div>
      <RelearnNote view={voices.view} />
      {!voices.view.enabled && <div className="dim small">Voice profiles are off (Settings → Recording): meetings don&apos;t teach them and nobody is named by voice.</div>}
      {!editable && n > 0 && <div className="dim small">{readOnlyWhy}</div>}
      {n === 0 && !voices.view.building && <div className="dim small">Nothing to check.</div>}
      {open && <ReviewItems view={voices.view} editable={editable} a={a} />}
      <ActNotes a={a} />
    </section>
  );
}

// PeoplePane shows the people the owner meets as tiles, most met first,
// with a filter on name, title and team.
function PeoplePane({ onPick }: { onPick: (id: string) => void }) {
  const [rows, setRows] = useState<PersonRow[] | null>(null);
  const [f, setF] = useState('');
  useEffect(() => { api.people('owner').then((r) => setRows(r ?? []), () => setRows([])); }, []);
  if (!rows) return <Spinner />;
  const words = f.toLowerCase().split(/\s+/).filter(Boolean);
  const shown = rows.filter((r) => words.every((w) => `${r.name} ${r.title ?? ''} ${r.department ?? ''} ${r.email ?? ''}`.toLowerCase().includes(w)));
  return (
    <div className="people-list">
      <form className="lookup" onSubmit={(e) => e.preventDefault()}>
        <input value={f} onChange={(e) => setF(e.target.value)} placeholder={`Filter ${rows.length} people by name, title or team…`} />
      </form>
      <div className="person-tiles">
        {shown.slice(0, 200).map((r) => <PersonTile key={r.id} r={r} onPick={onPick} />)}
      </div>
      {shown.length > 200 && <div className="dim small">{shown.length - 200} more; filter to narrow.</div>}
    </div>
  );
}

// Headshots are chosen on first ask (face detection over a person's meeting
// tiles takes a few seconds), so tiles ask only once on screen, two at a
// time; what came back is kept for the session.
const shots = new Map<string, string>();
const shotQueue: (() => Promise<void>)[] = [];
let shotsRunning = 0;
function nextShot() {
  while (shotsRunning < 2 && shotQueue.length) {
    shotsRunning++;
    shotQueue.shift()!().finally(() => { shotsRunning--; nextShot(); });
  }
}

function PersonTile({ r, onPick }: { r: PersonRow; onPick: (id: string) => void }) {
  const [shot, setShot] = useState(shots.get(r.id));
  const el = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (shots.has(r.id) || r.together === 0 || !el.current) return;
    let gone = false;
    const io = new IntersectionObserver(([e]) => {
      if (!e.isIntersecting) return;
      io.disconnect();
      shotQueue.push(() => (gone ? Promise.resolve() : api.headshot(r.id, 'owner').then((s) => { shots.set(r.id, s ?? ''); if (!gone) setShot(s ?? ''); }, () => {})));
      nextShot();
    });
    io.observe(el.current);
    return () => { gone = true; io.disconnect(); };
  }, [r.id, r.together]);
  return (
    <button ref={el} className="person-tile" onClick={() => onPick(r.id)} title={r.email}>
      {shot ? <img className="tile-shot" src={shot} alt="" /> : <div className="tile-shot none">{initials(r.name)}</div>}
      <div className="tile-name">{r.name}</div>
      <div className="sub">{[r.title, r.department].filter(Boolean).join(' · ') || r.email}</div>
      <div className="sub">{r.together > 0 ? `${r.together} meetings` : ''}{r.lastTogether && !r.lastTogether.startsWith('0001') ? ` · last ${fmtDate(r.lastTogether)}` : ''}</div>
    </button>
  );
}
