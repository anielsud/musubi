import { useEffect, useMemo, useRef, useState } from 'react';
import { api, fmtDate, on, speakerName } from '../api';
import type { Brief, Card, Hit, LiveStatus, MeetingRef, Segment, Session, Suggestion } from '../types';
import { CitedText, Empty, HitRow, Spinner } from './common';

const kindGlyph: Record<string, string> = { llm: '✦', entity: '◉', phrase: '¶', typed: '›' };

interface Props {
  status: LiveStatus | null;
  onOpen: (h: Hit, scope: string) => void;
  onEntity: (id: string) => void;
}

export function LiveView({ status, onOpen, onEntity }: Props) {
  const [segs, setSegs] = useState<Segment[]>([]);
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const [briefs, setBriefs] = useState<Brief[]>([]);
  const [cards, setCards] = useState<Card[]>([]);
  const [typed, setTyped] = useState('');
  const [showAll, setShowAll] = useState(false);
  const [err, setErr] = useState('');
  const tail = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const offs = [
      on<Session>('live:reset', () => {
        setSegs([]);
        setSuggestions([]);
        setBriefs([]);
        setCards([]);
        setErr('');
      }),
      on<Segment>('live:segment', (s) =>
        setSegs((prev) => {
          if (s.status === 'removed') return prev.filter((x) => x.id !== s.id);
          const i = prev.findIndex((x) => x.id === s.id);
          if (i < 0) return [...prev, s].slice(-400);
          const next = prev.slice();
          next[i] = s;
          return next;
        }),
      ),
      on<Suggestion[]>('live:suggestions', (s) => setSuggestions(s ?? [])),
      on<Card[]>('live:entities', (c) => setCards(c ?? [])),
      on<Brief>('live:brief', (b) =>
        setBriefs((prev) => {
          const rest = prev.filter((x) => x.query.toLowerCase() !== b.query.toLowerCase());
          // Keep the panel focused: empty lookups drop out quietly.
          if (b.state === 'empty') return rest;
          return [b, ...rest].slice(0, 8);
        }),
      ),
      on<string>('live:error', (e) => setErr(e)),
    ];
    return () => offs.forEach((f) => f());
  }, []);

  useEffect(() => {
    if (!showAll) tail.current?.scrollIntoView({ block: 'end' });
  }, [segs, showAll]);

  const scope = status?.scope ?? 'owner';
  const active = status && (status.mode === 'live' || status.mode === 'replay' || status.mode === 'ended');
  const visibleSegs = showAll ? segs : segs.slice(-6);

  const lookUp = (q: string, why = 'you asked', kind = 'typed') => {
    if (!q.trim()) return;
    api.lookUp(q.trim(), why, kind);
  };

  return (
    <div className="view live">
      <MeetingHeader status={status} />

      {!active && <ReplayPicker />}

      {active && (
        <section className="transcript">
          <div className="section-head">
            <span>Conversation</span>
            <button className="link" onClick={() => setShowAll(!showAll)}>{showAll ? 'recent' : `all ${segs.length}`}</button>
          </div>
          <div className={'lines' + (showAll ? ' all' : '')}>
            {visibleSegs.map((s) => (
              <div key={s.id} className={'line ' + (s.status === 'live' ? 'draft' : s.status === 'pending' ? 'pending' : 'final')}>
                <span className="who">{speakerName(s.speaker) || '…'}</span> {s.text}
              </div>
            ))}
            {segs.length === 0 && <div className="line draft">Waiting for speech…</div>}
            <div ref={tail} />
          </div>
        </section>
      )}

      <form className="lookup" onSubmit={(e) => { e.preventDefault(); lookUp(typed); setTyped(''); }}>
        <input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Look something up…" />
      </form>

      {suggestions.length > 0 && (
        <section>
          <div className="section-head"><span>Suggested</span></div>
          <div className="chips">
            {suggestions.map((s) => (
              <button key={s.query} className={'chip k-' + s.kind} title={s.why} onClick={() => lookUp(s.query, s.why, s.kind)}>
                <span className="glyph">{kindGlyph[s.kind] ?? '·'}</span>{s.query}
              </button>
            ))}
          </div>
        </section>
      )}

      {err && <div className="error small">{err}</div>}

      <section className="briefs">
        {briefs.map((b) => (
          <BriefCard key={b.query + b.at} brief={b} onOpen={(h) => onOpen(h, scope)} />
        ))}
        {active && briefs.length === 0 && (
          <Empty>Briefings appear here as the conversation names people, customers and topics you have history on.</Empty>
        )}
      </section>

      {cards.length > 0 && (
        <section>
          <div className="section-head"><span>In this conversation</span></div>
          <div className="cards">
            {cards.map((c) => (
              <button key={c.id} className={'card t-' + c.type} onClick={() => onEntity(c.id)}>
                <div className="card-name">{c.name}</div>
                <div className="card-meta">{c.type} · {c.items} items · last {fmtDate(c.lastSeen)}</div>
                {c.neighbors && c.neighbors.length > 0 && <div className="card-nb">with {c.neighbors.slice(0, 3).join(', ')}</div>}
                {c.recent?.map((r) => <div key={r} className="card-recent">{r}</div>)}
              </button>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function MeetingHeader({ status }: { status: LiveStatus | null }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  if (!status) return null;
  const s = status.session;
  const mode = status.mode;
  const elapsed = s ? Math.max(0, Math.floor((now - new Date(s.created_at).getTime()) / 1000)) : 0;
  const badge = mode === 'live' ? 'LIVE' : mode === 'replay' ? 'REPLAY' : mode === 'ended' ? 'ENDED' : 'IDLE';
  return (
    <div className={'meeting mode-' + mode}>
      <span className="mode-badge">{mode === 'live' && <span className="pulse" />}{badge}</span>
      <div className="meeting-title">
        {s ? s.title || 'Untitled meeting' : status.connected ? 'Tomoe connected · no meeting' : 'Not connected to Tomoe'}
      </div>
      {s && mode === 'live' && <span className="meeting-time">{Math.floor(elapsed / 60)}:{String(elapsed % 60).padStart(2, '0')}</span>}
      {mode === 'replay' && <button className="link" onClick={() => api.stopReplay()}>stop</button>}
      {status.scope !== 'owner' && <span className="scope-pill">screen-safe</span>}
    </div>
  );
}

function ReplayPicker() {
  const [meetings, setMeetings] = useState<MeetingRef[]>([]);
  const [pick, setPick] = useState('');
  const [speed, setSpeed] = useState(8);
  useEffect(() => {
    api.meetings().then((m) => {
      const ok = (m ?? []).filter((x) => x.chunks >= 8);
      setMeetings(ok);
      if (ok[0]) setPick(ok[0].ref);
    });
  }, []);
  const byDay = useMemo(() => meetings, [meetings]);
  return (
    <div className="idle">
      <div className="idle-art" aria-hidden>結</div>
      <p>When Tomoe records a meeting, the conversation streams in here and Musubi briefs you on what it recognises.</p>
      <p className="dim">Tomoe → Settings → Sharing → <b>Live feed</b> must be on.</p>
      <div className="replay">
        <div className="section-head"><span>Try it on a past meeting</span></div>
        <select value={pick} onChange={(e) => setPick(e.target.value)}>
          {byDay.map((m) => (
            <option key={m.ref} value={m.ref}>{fmtDate(m.occurredAt)} · {m.title}</option>
          ))}
        </select>
        <div className="row">
          <label>speed
            <select value={speed} onChange={(e) => setSpeed(Number(e.target.value))}>
              {[1, 2, 4, 8, 16, 32].map((n) => <option key={n} value={n}>{n}×</option>)}
            </select>
          </label>
          <button className="primary" disabled={!pick} onClick={() => api.replay(pick, speed)}>▶ Replay</button>
        </div>
      </div>
    </div>
  );
}

function BriefCard({ brief, onOpen }: { brief: Brief; onOpen: (h: Hit) => void }) {
  const [open, setOpen] = useState(false);
  const hits = brief.hits ?? [];
  // Without a model summary, the passages are the brief.
  const showHits = open !== (brief.state === 'done' && !brief.summary);
  return (
    <article className={'brief state-' + brief.state}>
      <header>
        <span className={'glyph k-' + brief.kind}>{kindGlyph[brief.kind] ?? '·'}</span>
        <span className="brief-q">{brief.query}</span>
        {brief.state === 'loading' && <Spinner />}
      </header>
      {brief.why && <div className="brief-why">{brief.why}</div>}
      {brief.summary && (
        <p className="brief-sum"><CitedText text={brief.summary} hits={hits} onCite={onOpen} /></p>
      )}
      {brief.state === 'error' && <div className="error small">{brief.summary}</div>}
      {hits.length > 0 && (
        <>
          <button className="link" onClick={() => setOpen(!open)}>{showHits ? 'hide sources' : `${hits.length} sources`}</button>
          {showHits && hits.map((h, i) => <HitRow key={h.chunkId} hit={h} n={i + 1} onOpen={onOpen} />)}
        </>
      )}
    </article>
  );
}
