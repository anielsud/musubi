import { useEffect, useRef, useState } from 'react';
import { api, on } from '../api';
import type { Hit } from '../types';
import { CitedText, HitRow, Spinner } from './common';

interface Turn {
  id: string;
  q: string;
  text: string;
  sources: Hit[];
  done: boolean;
  error?: string;
  scope: string;
}

const examples = [
  'What did we decide about patch management for banking customers?',
  'What are the open questions with Microsoft?',
  'Who have I met with most this week, and about what?',
  'What did I agree to follow up on this week?',
];

export function AskView({ onOpen, scope }: { onOpen: (h: Hit, scope: string) => void; scope: string }) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [q, setQ] = useState('');
  const end = useRef<HTMLDivElement>(null);
  const busy = turns.some((t) => !t.done);

  useEffect(() => {
    const offs = [
      on<{ id: string; text: string }>('ask:token', ({ id, text }) =>
        setTurns((ts) => ts.map((t) => (t.id === id ? { ...t, text: t.text + text } : t))),
      ),
      on<{ id: string; text?: string; sources?: Hit[]; error?: string }>('ask:done', (r) =>
        setTurns((ts) =>
          ts.map((t) =>
            t.id === r.id ? { ...t, done: true, text: r.text || t.text, sources: r.sources ?? [], error: r.error } : t,
          ),
        ),
      ),
    ];
    return () => offs.forEach((f) => f());
  }, []);

  useEffect(() => end.current?.scrollIntoView({ block: 'end', behavior: 'smooth' }), [turns]);

  const send = (question: string) => {
    const text = question.trim();
    if (!text || busy) return;
    const id = String(Date.now());
    setTurns((ts) => [...ts, { id, q: text, text: '', sources: [], done: false, scope }]);
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
            <p>Ask about your meetings, people and customers. Answers come from your own records, with sources, from a model running on this Mac.</p>
            <div className="examples">
              {examples.map((e) => <button key={e} className="chip" onClick={() => send(e)}>{e}</button>)}
            </div>
          </div>
        )}
        {turns.map((t) => (
          <div key={t.id} className="turn">
            <div className="q">{t.q}{t.scope !== 'owner' && <span className="scope-pill">{t.scope}</span>}</div>
            <div className="a">
              {t.text ? <CitedText text={t.text} hits={t.sources} onCite={(h) => onOpen(h, t.scope)} /> : !t.done && <Spinner />}
              {!t.done && t.text && <span className="caret" />}
              {t.error && <div className="error small">{t.error}</div>}
            </div>
            {t.done && t.sources.length > 0 && (
              <details className="sources">
                <summary>{t.sources.length} sources</summary>
                {t.sources.map((h, i) => <HitRow key={h.chunkId} hit={h} n={i + 1} onOpen={(x) => onOpen(x, t.scope)} />)}
              </details>
            )}
          </div>
        ))}
        <div ref={end} />
      </div>
      <form className="composer" onSubmit={(e) => { e.preventDefault(); send(q); }}>
        <textarea
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
