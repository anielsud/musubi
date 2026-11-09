import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react';
import { api, fmtDate, on } from '../api';
import type { Hit, ThreadRef } from '../types';
import { CitedText, HitRow, Spinner } from './common';

interface Turn {
  id: string;
  q: string;
  text: string;
  sources: Hit[];
  threads: ThreadRef[];
  done: boolean;
  stopped?: boolean;
  error?: string;
  scope: string;
}

const examples = [
  'Where do we stand on pipeline for Q4?',
  'What changed on hiring this month?',
  'What are the open questions with Microsoft?',
  'What did I agree to follow up on this week?',
];

export function AskView({ onOpen, scope, onTopic, prefill }: {
  onOpen: (h: Hit, scope: string) => void; scope: string; onTopic: (id: number) => void;
  // prefill puts a question in the box (from a text selection) to edit or send.
  prefill?: { text: string; n: number } | null;
}) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [q, setQ] = useState('');
  const box = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (!prefill) return;
    setQ(prefill.text);
    setTimeout(() => { box.current?.focus(); box.current?.select(); }, 0);
  }, [prefill?.n]);
  const end = useRef<HTMLDivElement>(null);
  const busy = turns.some((t) => !t.done);

  useEffect(() => {
    const offs = [
      on<{ id: string; sources?: Hit[]; threads?: ThreadRef[] }>('ask:found', (r) =>
        setTurns((ts) => ts.map((t) => (t.id === r.id ? { ...t, sources: r.sources ?? [], threads: r.threads ?? [] } : t))),
      ),
      on<{ id: string; text: string }>('ask:token', ({ id, text }) =>
        setTurns((ts) => ts.map((t) => (t.id === id ? { ...t, text: t.text + text } : t))),
      ),
      on<{ id: string; text?: string; sources?: Hit[]; threads?: ThreadRef[]; error?: string; stopped?: boolean }>('ask:done', (r) =>
        setTurns((ts) =>
          ts.map((t) =>
            t.id === r.id ? { ...t, done: true, text: r.text || t.text, sources: r.sources?.length ? r.sources : t.sources, threads: r.threads?.length ? r.threads : t.threads, error: r.error, stopped: r.stopped } : t,
          ),
        ),
      ),
    ];
    return () => offs.forEach((f) => f());
  }, []);

  // Braces: scrollIntoView may return a Promise (Chrome does), which React
  // would take for a cleanup function.
  useEffect(() => { end.current?.scrollIntoView({ block: 'end', behavior: 'smooth' }); }, [turns]);

  const send = (question: string) => {
    const text = question.trim();
    if (!text || busy) return;
    const id = String(Date.now());
    setTurns((ts) => [...ts, { id, q: text, text: '', sources: [], threads: [], done: false, scope }]);
    setQ('');
    api.ask(id, text, scope).catch((e) =>
      setTurns((ts) => ts.map((t) => (t.id === id ? { ...t, done: true, error: String(e) } : t))),
    );
  };

  return (
    <div className="view ask">
      <div className="turns">
        {turns.length === 0 && (
          <div className="ask-empty">
            <div className="idle-art" aria-hidden>問</div>
            <p>Ask about your meetings, people and customers. Answers start from where each subject stands now, then the record itself, with sources.</p>
            <div className="examples">
              {examples.map((e) => <button key={e} className="chip" onClick={() => send(e)}>{e}</button>)}
            </div>
          </div>
        )}
        {turns.map((t) => (
          <div key={t.id} className="turn">
            <div className="q">{t.q}{t.scope !== 'owner' && <span className="scope-pill">{t.scope}</span>}</div>
            {t.threads.length > 0 && (t.done ? (
              t.threads[0].status && (
                <details className="found-done">
                  <summary>Where {t.threads[0].name} stands, as written</summary>
                  <StatusCard th={t.threads[0]} onTopic={onTopic} />
                </details>
              )
            ) : (
              <div className="found">
                {t.threads[0].status && <StatusCard th={t.threads[0]} onTopic={onTopic} />}
                {t.threads.slice(t.threads[0].status ? 1 : 0).map((th) => (
                  <button key={th.id} className="found-row" onClick={() => onTopic(th.id)} title="Open the topic's page">
                    <b>{th.name}</b> <span className="dim">· {fmtDate(th.asOf)}</span>
                    {th.now && <div className="found-now">{th.now}</div>}
                  </button>
                ))}
                <div className="dim small">Writing the answer…</div>
              </div>
            ))}
            <div className="a">
              {t.text ? <Answer text={t.text} hits={t.sources} onCite={(h) => onOpen(h, t.scope)} /> : !t.done && <Spinner />}
              {!t.done && t.text && <span className="caret" />}
              {t.stopped && <div className="dim small">stopped</div>}
              {t.error && <div className="error small">{t.error}</div>}
            </div>
            {t.done && t.threads.length > 0 && (
              <div className="subject-chips">
                <span className="dim small">From:</span>
                {t.threads.map((th) => (
                  <button key={th.id} className="chip" onClick={() => onTopic(th.id)} title="Open the topic's page">
                    {th.name} <span className="dim">· {fmtDate(th.asOf)}</span>
                  </button>
                ))}
              </div>
            )}
            {t.done && t.sources.length > 0 && <Sources t={t} onOpen={(x) => onOpen(x, t.scope)} />}
          </div>
        ))}
        <div ref={end} />
      </div>
      <form className="composer" onSubmit={(e) => { e.preventDefault(); send(q); }}>
        <textarea
          ref={box}
          value={q}
          rows={2}
          placeholder="Ask anything…"
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault();
              send(q);
            }
          }}
        />
        {busy ? (
          <button type="button" className="icon-btn" onClick={() => api.stopAsk()} title="Stop">■</button>
        ) : (
          <button type="submit" className="primary" disabled={!q.trim()}>Ask</button>
        )}
      </form>
    </div>
  );
}

// cited lists the source numbers an answer cites, in order.
function cited(text: string): number[] {
  const out: number[] = [];
  for (const m of text.matchAll(/\[(\d+)\]/g)) {
    const n = parseInt(m[1], 10);
    if (!out.includes(n)) out.push(n);
  }
  return out;
}

// Sources: what the answer cites, then the rest folded away.
function Sources({ t, onOpen }: { t: Turn; onOpen: (h: Hit) => void }) {
  const used = cited(t.text).filter((n) => n >= 1 && n <= t.sources.length);
  const rest = t.sources.map((_, i) => i + 1).filter((n) => !used.includes(n));
  return (
    <details className="sources">
      <summary>{used.length ? `${used.length} cited` : `${t.sources.length} sources`}{used.length && rest.length ? ` · ${rest.length} more read` : ''}</summary>
      {(used.length ? used : rest).map((n) => <HitRow key={n} hit={t.sources[n - 1]} n={n} onOpen={onOpen} />)}
      {used.length > 0 && rest.length > 0 && (
        <details className="sources more">
          <summary>{rest.length} more read</summary>
          {rest.map((n) => <HitRow key={n} hit={t.sources[n - 1]} n={n} onOpen={onOpen} />)}
        </details>
      )}
    </details>
  );
}

// Answer renders the little markdown answers use (paragraphs, bullet
// lists, bold), with citations as chips.
export function Answer({ text, hits, onCite }: { text: string; hits: Hit[]; onCite: (h: Hit) => void }) {
  const inline = (s: string, key: number): ReactNode => {
    const parts = s.split(/(\*\*[^*]+\*\*)/g);
    return (
      <Fragment key={key}>
        {parts.map((p, i) =>
          p.startsWith('**') && p.endsWith('**') ? <b key={i}><CitedText text={p.slice(2, -2)} hits={hits} onCite={onCite} /></b> : <CitedText key={i} text={p} hits={hits} onCite={onCite} />,
        )}
      </Fragment>
    );
  };
  const blocks: ReactNode[] = [];
  let list: { depth: number; text: string }[] = [];
  const flush = () => {
    if (!list.length) return;
    blocks.push(
      <ul key={blocks.length}>
        {list.map((l, i) => <li key={i} style={l.depth ? { marginLeft: 14 * l.depth } : undefined}>{inline(l.text, i)}</li>)}
      </ul>,
    );
    list = [];
  };
  for (const line of text.split('\n')) {
    const m = line.match(/^(\s*)[*-]\s+(.*)$/);
    if (m) {
      list.push({ depth: Math.floor(m[1].length / 2), text: m[2] });
      continue;
    }
    flush();
    if (line.trim()) blocks.push(<p key={blocks.length}>{inline(line.replace(/^#+\s*/, ''), 0)}</p>);
  }
  flush();
  return <div className="answer-md">{blocks}</div>;
}

// StatusCard: a subject's written status, shown before the answer.
function StatusCard({ th, onTopic }: { th: ThreadRef; onTopic: (id: number) => void }) {
  const st = th.status!;
  return (
    <section className="thread-status">
      <div className="section-head">
        <button className="link" onClick={() => onTopic(th.id)}>{th.name}</button>
        <span className="dim">as of {fmtDate(th.asOf)}</span>
      </div>
      <p className="status-now">{st.now}</p>
      {(st.changed?.length ?? 0) > 0 && (
        <>
          <div className="status-head">Changed recently</div>
          <ul className="status-list">{st.changed!.slice(0, 3).map((c, i) => <li key={i}>{c.date && <span className="dim">{c.date} · </span>}{c.text}</li>)}</ul>
        </>
      )}
      {(st.open?.length ?? 0) > 0 && (
        <>
          <div className="status-head">Open</div>
          <ul className="status-list">{st.open!.slice(0, 4).map((o, i) => <li key={i}>{o.text}{o.who && <span className="dim"> — {o.who}</span>}</li>)}</ul>
        </>
      )}
    </section>
  );
}

