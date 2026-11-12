import { useEffect, useRef, useState } from 'react';
import { api, fmtDate, on, speakerName } from '../api';
import type { Brief, Card, Hit, LiveStatus, Segment, Session, Suggestion, TopicCard, TopicLine } from '../types';
import { CitedText, Empty, HitRow, Spinner } from './common';
import { StatusView } from './Knowledge';
import { Names } from './people';
import { AskCard, askExamples, useAsk } from './AskView';
import type { ThreadDetail } from '../types';
import { SourceBanner, useRecorder } from './recorder';

const kindGlyph: Record<string, string> = { llm: '✦', entity: '◉', phrase: '¶', typed: '›' };

interface Props {
  status: LiveStatus | null;
  onOpen: (h: Hit, scope: string) => void;
  onEntity: (id: string) => void;
  onTopic: (id: number) => void;
  // scope is the panel's (the screen-safe toggle), for questions asked.
  scope: string;
  // prefill puts a question in the box (from a text selection) to edit or send.
  prefill?: { text: string; n: number } | null;
}

export function LiveView({ status, onOpen, onEntity, onTopic, scope: askScope, prefill }: Props) {
  const [segs, setSegs] = useState<Segment[]>([]);
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const [briefs, setBriefs] = useState<Brief[]>([]);
  const [cards, setCards] = useState<Card[]>([]);
  const [topics, setTopics] = useState<TopicCard[]>([]);
  const [typed, setTyped] = useState('');
  const [showAll, setShowAll] = useState(false);
  const [showSearches, setShowSearches] = useState(false);
  // Earlier cards and lookups opened by a click (focus).
  const [opened, setOpened] = useState<Set<string>>(new Set());
  const [allEarlier, setAllEarlier] = useState(false);
  const [err, setErr] = useState('');
  const tail = useRef<HTMLDivElement>(null);
  const box = useRef<HTMLTextAreaElement>(null);
  const ask = useAsk(askScope);
  const rec = useRecorder();
  // Renaming a speaker reaches the recording in progress only when it is
  // the Musubi recorder's (the one Live then follows).
  const canRename = !!rec?.editable && status?.mode === 'live';
  useEffect(() => {
    if (!prefill) return;
    setTyped(prefill.text);
    setTimeout(() => { box.current?.focus(); box.current?.select(); }, 0);
  }, [prefill?.n]);

  useEffect(() => {
    const offs = [
      on<Session>('live:reset', () => {
        setSegs([]);
        setSuggestions([]);
        setBriefs([]);
        setCards([]);
        setTopics([]);
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
      on<TopicCard[]>('live:topics', (t) => setTopics(t ?? [])),
      on<Brief>('live:brief', (b) =>
        setBriefs((prev) => {
          const rest = prev.filter((x) => x.query.toLowerCase() !== b.query.toLowerCase());
          // Keep the panel focused: empty lookups drop out quietly.
          if (b.state === 'empty') return rest;
          // With relevance, lookups wait to be judged: keep more of them.
          return [b, ...rest].slice(0, b.rated || prev.some((x) => x.rated) ? 40 : 8);
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
  const focus = !!status?.focus;
  const topicKey = (t: TopicCard) => (t.anchor?.term ? 'term:' + t.anchor.term.toLowerCase() : 'topic:' + (t.anchor?.topic ?? t.topicId));
  const toggleOpened = (k: string) => setOpened((o) => { const n = new Set(o); if (n.has(k)) n.delete(k); else n.add(k); return n; });
  // Focus: two cards open (the server picks them), two lookups open (the
  // newest); the rest one line each under "Earlier in this meeting".
  // Picking: a card is three lines, so the recent picks all stay up, the
  // current one marked; only cards pushed off go to the menu.
  const pick = focus && !!status?.pick;
  const openTopics = !focus ? topics : topics.filter((t) => (pick ? !t.kept : t.active) || opened.has(topicKey(t)));
  const earlierTopics = focus ? topics.filter((t) => !openTopics.includes(t)) : [];
  // When the server has ranked them (live.pick.brief), the menu shows the
  // few most worth raising; the rest are a click away.
  const ranked = earlierTopics.filter((t) => t.rank).sort((a, b) => (a.rank ?? 0) - (b.rank ?? 0));
  const menuTopics = ranked.length > 0 && !allEarlier ? ranked : earlierTopics;
  const moreEarlier = earlierTopics.length - menuTopics.length;
  // With relevance on, the server gives lookups their places (b.open);
  // those it never showed stay off the panel. Without, the newest two.
  const rel = focus && !!status?.relevance;
  const briefOpen = (b: Brief, i: number) => (rel ? !!b.open : i < 2) || opened.has('q:' + b.query);
  const shownBriefs = focus ? briefs.filter(briefOpen) : briefs;
  const earlierBriefs = focus ? briefs.filter((b, i) => !briefOpen(b, i) && (!rel || b.wasOpen)) : [];
  const active = status && (status.mode === 'live' || status.mode === 'replay' || status.mode === 'ended');
  const visibleSegs = showAll ? segs : segs.slice(-6);

  const lookUp = (q: string, why = 'you asked', kind = 'typed') => {
    if (!q.trim()) return;
    api.lookUp(q.trim(), why, kind);
  };
  // What's typed is asked: the answer is a card at the top of the panel.
  const send = () => { if (ask.send(typed)) setTyped(''); };

  return (
    // Without a meeting there is nothing for a second column: one column,
    // centred, with the box and what was asked.
    <div className={'view live' + (active ? '' : ' idle-mode')}>
      <div className="live-left">
      <MeetingHeader status={status} />
      <SourceBanner rec={rec} />
      {active && <WithLine invited={status?.session?.invited ?? []} segs={segs} />}

      {!active && (
        <div className="idle">
          <div className="idle-art" aria-hidden>結</div>
          <p>When Tomoe records a meeting, the conversation streams in here and Musubi briefs you on what it recognises. Ask anything below, during a meeting or not: answers start from where each subject stands now, then the record itself, with sources.</p>
          <p className="dim">Tomoe → Settings → Sharing → <b>Live feed</b> must be on.</p>
          {ask.turns.length === 0 && (
            <div className="examples">
              {askExamples.map((e) => <button key={e} className="chip" onClick={() => ask.send(e)}>{e}</button>)}
            </div>
          )}
        </div>
      )}

      {active && (
        <section className="transcript">
          <div className="section-head">
            <span>Conversation</span>
            <button className="link" onClick={() => setShowAll(!showAll)}>{showAll ? 'recent' : `all ${segs.length}`}</button>
          </div>
          <div className={'lines' + (showAll ? ' all' : '')}>
            {visibleSegs.map((s) => (
              <div key={s.id} className={'line ' + (s.status === 'live' ? 'draft' : s.status === 'pending' ? 'pending' : 'final')}>
                {canRename && s.speaker && s.speaker !== 'You' ? <RenameSpeaker label={s.speaker} /> : <span className="who">{speakerName(s.speaker) || '…'}</span>} {s.text}
              </div>
            ))}
            {segs.length === 0 && <div className="line draft">Waiting for speech…</div>}
            <div ref={tail} />
          </div>
        </section>
      )}

      <form className="lookup" onSubmit={(e) => { e.preventDefault(); send(); }}>
        <textarea ref={box} value={typed} rows={1} onChange={(e) => setTyped(e.target.value)} placeholder="Ask anything…"
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); } }} />
        {ask.busy && <button type="button" className="icon-btn" onClick={() => api.stopAsk()} title="Stop">■</button>}
      </form>

      {suggestions.length > 0 && focus && (
        <button className="link small searches" onClick={() => setShowSearches(!showSearches)}>
          {showSearches ? 'hide searches' : `searches (${suggestions.length})`}
        </button>
      )}
      {suggestions.length > 0 && (!focus || showSearches) && (
        <section>
          {!focus && <div className="section-head"><span>Suggested</span></div>}
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
      </div>

      <div className="live-right">
      {ask.turns.length > 0 && (
        <section className="asked">
          {ask.turns.map((t) => <AskCard key={t.id} t={t} onOpen={onOpen} onTopic={onTopic} onClose={() => ask.dismiss(t.id)} />)}
        </section>
      )}

      {openTopics.length > 0 && (
        <section className="topics">
          {openTopics.map((t) => (
            pick ? (
              <PickCardView key={topicKey(t)} card={t} scope={scope} onTopic={onTopic} onSource={(itemId, chunkId) => onOpen({ itemId, chunkId } as Hit, scope)}
                onClose={opened.has(topicKey(t)) && t.kept ? () => toggleOpened(topicKey(t)) : undefined} />
            ) : (
            <TopicCardView key={topicKey(t)} card={t} onTopic={onTopic}
              onClose={focus && opened.has(topicKey(t)) && !t.active ? () => toggleOpened(topicKey(t)) : undefined} />
            )
          ))}
        </section>
      )}

      <section className="briefs">
        {shownBriefs.map((b) => (
          <BriefCard key={b.query + b.at} brief={b} onOpen={(h) => onOpen(h, scope)} onTopic={onTopic} />
        ))}
        {active && briefs.length === 0 && topics.length === 0 && (
          <Empty>Briefings appear here as the conversation names people, customers and topics you have history on.</Empty>
        )}
      </section>

      {(earlierTopics.length > 0 || earlierBriefs.length > 0) && (
        <section className="earlier">
          <div className="section-head"><span>Earlier in this meeting</span></div>
          {earlierTopics.length > 0 && (
            <div className="topic-stubs">
              {menuTopics.map((t) => <TopicStub key={topicKey(t)} card={t} onOpen={() => toggleOpened(topicKey(t))} onTopic={onTopic} />)}
            </div>
          )}
          {(moreEarlier > 0 || allEarlier) && ranked.length > 0 && (
            <button className="link small" onClick={() => setAllEarlier(!allEarlier)}>{allEarlier ? 'only the most worth raising' : `${moreEarlier} more`}</button>
          )}
          <div className="chips">
            {earlierTopics.map((t) => (
              <button key={topicKey(t)} className="chip earlier-chip topic-chip" onClick={() => toggleOpened(topicKey(t))} title={t.quote ? '“' + t.quote + '”' : t.why}>
                {t.title}{t.stale && <span className="dim"> · old</span>}
              </button>
            ))}
            {earlierBriefs.map((b) => (
              <button key={'q:' + b.query} className="chip earlier-chip k-brief" onClick={() => toggleOpened('q:' + b.query)} title={b.quote ? '“' + b.quote + '”' : b.why}>
                {b.status?.name ?? b.query}
              </button>
            ))}
          </div>
        </section>
      )}

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
    </div>
  );
}

// RenameSpeaker is a line's speaker; a click names them in the recording
// in progress (every line of that speaker, and the voice from then on).
function RenameSpeaker({ label }: { label: string }) {
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState('');
  const [err, setErr] = useState('');
  if (!editing) {
    return <button className="who who-edit" title="Name this speaker" onClick={() => { setName(speakerName(label).startsWith('Person ') ? '' : speakerName(label)); setEditing(true); }}>{speakerName(label) || '…'}</button>;
  }
  const save = () => {
    if (!name.trim()) return;
    api.recorderSpeaker('rename_speaker', '', label, name.trim()).then(() => setEditing(false), (e) => setErr(String(e)));
  };
  return (
    <form className="rename-inline" onSubmit={(e) => { e.preventDefault(); save(); }}>
      <input value={name} autoFocus placeholder="Who is it?" onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setEditing(false)} />
      <button type="submit" className="link">save</button>
      <button type="button" className="link" onClick={() => setEditing(false)}>cancel</button>
      {err && <span className="error small">{err}</span>}
    </form>
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

// withInCall is who's in the call: everyone invited when it's a small
// meeting (and anyone else heard), else those heard so far and how many
// more were invited; with no calendar event, those heard.
const smallMeeting = 6;
export function withInCall(invited: string[], segs: Segment[]): { names: string[]; more: number } {
  const key = (n: string) => n.trim().toLowerCase();
  const heard: string[] = [];
  for (const s of segs) {
    const n = speakerName(s.speaker).trim();
    if (!n || n === 'You' || /^(Person|Speaker) \d+$/.test(n) || heard.some((h) => key(h) === key(n))) continue;
    heard.push(n);
  }
  const isHeard = (n: string) => heard.some((h) => key(h) === key(n));
  if (invited.length > 0 && invited.length <= smallMeeting) {
    return { names: [...invited, ...heard.filter((h) => !invited.some((n) => key(n) === key(h)))], more: 0 };
  }
  return { names: heard, more: invited.filter((n) => !isHeard(n)).length };
}

// WithLine names who's in the call, each a link to their page.
function WithLine({ invited, segs }: { invited: string[]; segs: Segment[] }) {
  const { names, more } = withInCall(invited, segs);
  if (names.length === 0 && more === 0) return null;
  return (
    <div className="live-with small">
      <span className="dim">With </span>
      <Names names={names} />
      {more > 0 && <span className="dim">{names.length > 0 ? ' ' : ''}+{more} invited</span>}
    </div>
  );
}

function BriefCard({ brief, onOpen, onTopic }: { brief: Brief; onOpen: (h: Hit) => void; onTopic: (id: number) => void }) {
  const [open, setOpen] = useState(false);
  const hits = brief.hits ?? [];
  // Until (or without) a summary or a status, the passages are the brief.
  const showHits = open !== (brief.state === 'done' && !brief.summary && !brief.status);
  const st = brief.status;
  const lag = brief.spokenAgo >= 0 ? `${brief.spokenAgo.toFixed(1)}s after it was said` : '';
  return (
    <article className={'brief state-' + brief.state}>
      <header>
        <span className={'glyph k-' + brief.kind}>{kindGlyph[brief.kind] ?? '·'}</span>
        <span className="brief-q">{brief.query}</span>
        {(brief.state === 'loading' || brief.summarizing) && <Spinner />}
      </header>
      {brief.quote ? (
        <div className="card-quote">“{brief.quote}”{lag && <span className="dim"> · {lag}</span>}</div>
      ) : (brief.why && brief.why !== 'model') || lag ? (
        <div className="brief-why">{[brief.why !== 'model' ? brief.why : '', lag].filter(Boolean).join(' · ')}</div>
      ) : null}
      {st && (
        <div className="brief-status">
          <button className="link" onClick={() => onTopic(st.topic)} title="Open the topic's page">{st.name}</button>
          <span className="dim small"> · as of {fmtDate(st.asOf)}</span>
          <p className="brief-sum">{st.now}</p>
          {(st.open?.length ?? 0) > 0 && <ul className="status-list">{st.open!.map((o, i) => <li key={i}>{o}</li>)}</ul>}
        </div>
      )}
      {brief.summarizing && <div className="brief-why">summarising…</div>}
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

const sectionLabel: Record<string, string> = {
  Watch: 'Don\u2019t repeat', Now: 'Where it stands', Due: 'Due', Yours: 'Yours', Open: 'Open', Rules: 'Rules',
};

// TopicCardView is a subject's card: where it stands, what is due, what is
// yours, what is open, the rules, what must not be repeated, and who to
// ask. On a shared screen, cards about people's jobs, pay or health, or
// restricted ones, show their title only until opened (the backend sends
// them uncollapsed in private).
function TopicCardView({ card, onTopic, onClose }: { card: TopicCard; onTopic: (id: number) => void; onClose?: () => void }) {
  if (card.focus && !card.collapsed) return <FocusCardView card={card} onTopic={onTopic} onClose={onClose} />;
  return <FullCardView card={card} onTopic={onTopic} />;
}

// PickCardView is a picked topic in short: where it stands and what to
// raise. Opened, it shows the other things worth raising and the topic's
// written status, each point citing the statements it rests on.
function PickCardView({ card, scope, onTopic, onSource, onClose }: { card: TopicCard; scope: string; onTopic: (id: number) => void; onSource: (itemId: string, chunkId: number) => void; onClose?: () => void }) {
  const [more, setMore] = useState(false);
  const [detail, setDetail] = useState<ThreadDetail | null | undefined>(undefined);
  const sensitive = card.label.level === 'restricted' || card.label.level === 'confidential';
  useEffect(() => {
    if (!more || detail !== undefined || card.topicId <= 0) return;
    api.topic(card.topicId, scope).then((t) => setDetail(t?.thread ?? null), () => setDetail(null));
  }, [more, detail, card.topicId, scope]);
  return (
    <article className={'topic pick' + (card.active ? ' current' : '') + (card.stale ? ' stale' : '')}>
      <header>
        <span className="topic-title">{card.title}</span>
        {sensitive && <span className={'sens-dot lvl-' + card.label.level} title={card.label.level} />}
        {card.topicId > 0 && <button className="link" onClick={() => onTopic(card.topicId)} title="Open the topic's page">page →</button>}
        {onClose && <button className="link" onClick={onClose} title="Collapse it again">×</button>}
      </header>
      {card.headline ? <p className="card-now">{card.headline}</p> : card.now && <p className="card-now">{card.now}</p>}
      {card.raise && <div className="card-raise">{card.raise}</div>}
      {more && (card.raiseMore ?? []).map((r, i) => <div key={i} className="card-raise">{r}</div>)}
      {more && (detail === undefined ? <Spinner /> : detail?.status ? <StatusView d={detail} onSource={onSource} /> : <div className="dim small">No written status for this topic yet.</div>)}
      <button className="link small" onClick={() => setMore(!more)}>{more ? 'less' : 'more'}</button>
    </article>
  );
}

// TopicStub is a card that has left the panel, collapsed to what it is
// and what raised it: in landscape, the menu of what else to raise. A
// click opens it again.
function TopicStub({ card, onOpen, onTopic }: { card: TopicCard; onOpen: () => void; onTopic: (id: number) => void }) {
  return (
    <article className={'topic stub' + (card.stale ? ' stale' : '')} onClick={onOpen} title="Open the card">
      <header>
        <span className="topic-title">{card.title}</span>
        {card.topicId > 0 && (
          <button className="link" onClick={(e) => { e.stopPropagation(); onTopic(card.topicId); }} title="Open the topic's page">page →</button>
        )}
      </header>
      {card.raise ? <div className="card-raise">{card.raise}</div> : card.quote && <div className="card-quote">“{card.quote}”</div>}
      <div className="brief-why">
        {card.stale && <span className="old">old · </span>}last discussed {fmtDate(card.last)} · {card.meetings} meeting{card.meetings === 1 ? '' : 's'}
      </div>
    </article>
  );
}

// FocusCardView is a card cut to the conversation: what was said that
// raised it, the subject's age, and its few lines closest to the talk;
// the whole card is a click away.
function FocusCardView({ card, onTopic, onClose }: { card: TopicCard; onTopic: (id: number) => void; onClose?: () => void }) {
  const [more, setMore] = useState(false);
  const sensitive = card.label.level === 'restricted' || card.label.level === 'confidential';
  const lines = card.lines ?? [];
  return (
    <article className={'topic focus' + (card.stale ? ' stale' : '')}>
      <header>
        <span className="topic-title">{card.title}</span>
        {sensitive && <span className={'sens-dot lvl-' + card.label.level} title={card.label.level + (card.label.categories && card.label.categories !== '-' ? ' · ' + card.label.categories : '')} />}
        {card.topicId > 0 && <button className="link" onClick={() => onTopic(card.topicId)} title="Open the topic's page">page →</button>}
        {onClose && <button className="link" onClick={onClose} title="Collapse it again">×</button>}
      </header>
      {!card.headline && card.quote && <div className="card-quote">“{card.quote}”</div>}
      <div className="brief-why">
        {card.stale && <span className="old">old · </span>}last discussed {fmtDate(card.last)} · {card.meetings} meeting{card.meetings === 1 ? '' : 's'}
      </div>
      {/* With a brief, the headline and what to raise stand for the lines,
          which read as peripheral when the topic is right. */}
      {!more && card.headline && <p className="card-now">{card.headline}</p>}
      {!more && card.raise && <div className="card-raise">{card.raise}</div>}
      {!more && !card.headline && lines.map((l) => (
        <div key={l.claimId} className="focus-line">
          {['Due', 'Yours', 'Open', 'Watch'].includes(l.section) && <span className="focus-sec">{sectionLabel[l.section] ?? l.section}</span>}
          <TopicLineView line={l} />
        </div>
      ))}
      {!more && !card.headline && lines.length === 0 && card.now && <p className="card-now">{card.now}</p>}
      {more && <CardBody card={card} />}
      <button className="link small" onClick={() => setMore(!more)}>{more ? 'less' : 'more'}</button>
    </article>
  );
}

function CardBody({ card }: { card: TopicCard }) {
  return (
    <>
      {card.now && <p className="card-now">{card.now}</p>}
      {card.sections.map((s) => (
        <div key={s.name} className={'topic-sec s-' + s.name.toLowerCase()}>
          <div className="topic-sec-name">{sectionLabel[s.name] ?? s.name}</div>
          {s.lines.map((l) => <TopicLineView key={l.claimId} line={l} />)}
        </div>
      ))}
      {card.people && card.people.length > 0 && (
        <div className="topic-people"><span className="topic-sec-name">Ask</span> {card.people.join(' · ')}</div>
      )}
    </>
  );
}

function FullCardView({ card, onTopic }: { card: TopicCard; onTopic: (id: number) => void }) {
  const [reveal, setReveal] = useState(false);
  const hidden = card.collapsed && !reveal;
  const lag = card.spokenAgo >= 0 ? `${card.spokenAgo.toFixed(1)}s after it was said` : '';
  const sensitive = card.label.level === 'restricted' || card.collapsed;
  return (
    <article className={'topic' + (hidden ? ' hidden' : '')}>
      <header>
        <span className="topic-title">{card.title}</span>
        {sensitive && <span className={'lvl lvl-' + card.label.level}>{card.label.level}{card.label.categories && card.label.categories !== '-' ? ' · ' + card.label.categories : ''}</span>}
        {!hidden && card.topicId > 0 && <button className="link" onClick={() => onTopic(card.topicId)} title="Open the topic's page">page →</button>}
      </header>
      <div className="brief-why">
        {[card.why, `last discussed ${fmtDate(card.last)}`, `${card.meetings} meeting${card.meetings === 1 ? '' : 's'}`, lag].filter(Boolean).join(' · ')}
      </div>
      {!hidden && card.now && <p className="card-now">{card.now}</p>}
      {hidden ? (
        <button className="link reveal" onClick={() => setReveal(true)}>Show — not for a shared screen</button>
      ) : (
        <>
          {card.sections.map((s) => (
            <div key={s.name} className={'topic-sec s-' + s.name.toLowerCase()}>
              <div className="topic-sec-name">{sectionLabel[s.name] ?? s.name}</div>
              {s.lines.map((l) => <TopicLineView key={l.claimId} line={l} />)}
            </div>
          ))}
          {card.people && card.people.length > 0 && (
            <div className="topic-people"><span className="topic-sec-name">Ask</span> {card.people.join(' · ')}</div>
          )}
          {card.collapsed && <button className="link" onClick={() => setReveal(false)}>hide</button>}
        </>
      )}
    </article>
  );
}

function TopicLineView({ line }: { line: TopicLine }) {
  const [open, setOpen] = useState(false);
  const soon = line.due && /today|tomorrow|in \d+ days/.test(line.due);
  return (
    <div className={'topic-line' + (line.mine ? ' mine' : '') + (open ? ' open' : '')} onClick={() => setOpen(!open)} title={line.meeting}>
      {line.due && <span className={'due' + (soon ? ' soon' : line.due.startsWith('was') ? ' past' : '')}>{line.due}</span>}
      <span className="topic-text">{line.text}</span>
      <span className="topic-meta">{[line.who, fmtDate(line.said), line.note].filter(Boolean).join(' · ')}</span>
    </div>
  );
}
