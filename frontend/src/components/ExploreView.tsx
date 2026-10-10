import { useEffect, useState } from 'react';
import { api, fmtDate, kindIcon } from '../api';
import type { EntityRef, Graph, Hit } from '../types';
import { Empty, HitRow, LabelBadge, Spinner } from './common';
import { GraphView } from './GraphView';
import { ClaimResults, TopicsPane } from './Knowledge';

interface Props {
  scope: string;
  focus: string | null;
  setFocus: (id: string | null) => void;
  onOpen: (h: Hit, scope: string) => void;
  onOpenItem: (itemId: string, scope: string) => void;
}

export function ExploreView({ scope, focus, setFocus, onOpen, onOpenItem }: Props) {
  const [q, setQ] = useState('');
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [ents, setEnts] = useState<EntityRef[]>([]);
  const [graph, setGraph] = useState<Graph | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [trail, setTrail] = useState<EntityRef[]>([]);
  const [mode, setMode] = useState<'topics' | 'search'>('topics');
  const [lastQ, setLastQ] = useState('');
  const openSource = (itemId: string, chunkId: number) => onOpen({ itemId, chunkId } as unknown as Hit, scope);

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

  if (focus && mode === 'topics') setMode('search');
  return (
    <div className="view explore">
      <div className="segmented">
        <button className={mode === 'topics' ? 'on' : ''} onClick={() => { setMode('topics'); setFocus(null); }}>Topics</button>
        <button className={mode === 'search' ? 'on' : ''} onClick={() => setMode('search')}>Search</button>
      </div>
      {mode === 'topics' && <TopicsPane scope={scope} onSource={openSource} />}
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
