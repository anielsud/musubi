import { useEffect, useMemo, useRef, useState } from 'react';
import { fmtDate } from '../api';
import type { MapTopic, TopicCluster, TopicEdge, TopicMapData } from '../types';
import { Empty } from './common';

export type TopicViewKind = 'graph' | 'meaning' | 'timeline' | 'clusters';
const viewNames: [TopicViewKind, string, string][] = [
  ['graph', 'Graph', 'Topics as a network: links are discussion, shared customers or projects, and meaning; colours are clusters'],
  ['meaning', 'Meaning', 'Topics placed by what they say: near each other means alike'],
  ['timeline', 'Timeline', 'When each topic came up, meeting by meeting, grouped by cluster'],
  ['clusters', 'Clusters', 'The larger subjects the links form, and what each holds'],
];

// Cluster colours: hues spread around the wheel, readable on the dark
// background; topics linked to nothing are grey.
export function clusterColor(c: number): string {
  if (c < 0) return 'var(--text-faint)';
  const hues = [168, 262, 38, 205, 330, 95, 18, 285, 140, 55, 230, 0];
  const h = hues[c % hues.length];
  const l = c < hues.length ? 66 : 52;
  return `hsl(${h} 62% ${l}%)`;
}

const edgeLabel: Record<string, string> = {
  together: 'discussed back to back',
  shared: 'share statements',
  same: 'same customer or project',
  similar: 'alike in meaning',
};

export function useWide(): boolean {
  const q = '(min-width: 1000px)';
  const [wide, setWide] = useState(() => window.matchMedia(q).matches);
  useEffect(() => {
    const m = window.matchMedia(q);
    const f = () => setWide(m.matches);
    m.addEventListener('change', f);
    return () => m.removeEventListener('change', f);
  }, []);
  return wide;
}

function useWidth(ref: React.RefObject<HTMLElement>): number {
  const [w, setW] = useState(420);
  useEffect(() => {
    if (!ref.current) return;
    const ro = new ResizeObserver((es) => setW(Math.max(280, Math.floor(es[0].contentRect.width))));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, [ref]);
  return w;
}

function loadView(): TopicViewKind {
  try {
    const v = localStorage.getItem('musubi.topicView');
    if (v && viewNames.some(([k]) => k === v)) return v as TopicViewKind;
  } catch { /* storage unavailable */ }
  return 'graph';
}

interface Props {
  map: TopicMapData;
  highlight: Set<number> | null; // from the filter box
  selected: number | null;
  onOpen: (id: number) => void;
  wide: boolean;
}

// TopicViews draws the topic map in one of four views, picked with the
// small buttons underneath; clusters can be picked to focus on.
export function TopicViews({ map, highlight, selected, onOpen, wide }: Props) {
  const [view, setView] = useState<TopicViewKind>(loadView);
  const [focusCluster, setFocusCluster] = useState<number | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const width = useWidth(ref);
  const pick = (v: TopicViewKind) => {
    setView(v);
    try { localStorage.setItem('musubi.topicView', v); } catch { /* fine */ }
  };
  const topics = map.topics ?? [];
  const lit = useMemo(() => {
    if (!highlight && focusCluster === null) return null;
    return new Set(topics.filter((t) => (!highlight || highlight.has(t.id)) && (focusCluster === null || t.cluster === focusCluster)).map((t) => t.id));
  }, [topics, highlight, focusCluster]);
  const height = wide ? Math.max(420, Math.min(Math.round(width * 0.6), window.innerHeight - 250)) : Math.round(width * 0.9);
  const common = { map, lit, selected, onOpen, width, height, wide };

  return (
    <div className="tviews" ref={ref}>
      <div className="tview-switch">
        {viewNames.map(([k, name, tip]) => (
          <button key={k} className={view === k ? 'on' : ''} title={tip} onClick={() => pick(k)}>{name}</button>
        ))}
        <span className="tview-count">{topics.length} topics · {(map.edges ?? []).length} links · {(map.clusters ?? []).length} clusters</span>
      </div>
      <div className="tview-stage">
        {view === 'graph' && <GraphMap {...common} />}
        {view === 'meaning' && (map.meaning ? <MeaningMap {...common} /> : <Empty>This dataset's items have no vectors, so there's no meaning layout. Graph and Timeline still work.</Empty>)}
        {view === 'timeline' && <Timeline {...common} />}
        {view === 'clusters' && <ClusterCards {...common} />}
      </div>
      {view !== 'clusters' && (map.clusters?.length ?? 0) > 0 && (
        <div className="cluster-legend">
          {map.clusters!.map((c) => (
            <button key={c.id} className={'cl-chip' + (focusCluster === c.id ? ' on' : '')} title={`${c.topics.length} topics · ${c.statements} statements`}
              onClick={() => setFocusCluster(focusCluster === c.id ? null : c.id)}>
              <span className="cl-dot" style={{ background: clusterColor(c.id) }} />{short(c.label, 28)}
              <span className="dim"> {c.topics.length}</span>
            </button>
          ))}
        </div>
      )}
      {(view === 'graph' || view === 'meaning') && (
        <div className="edge-legend">
          {Object.entries(edgeLabel).map(([k, l]) => <span key={k}><i className={'el el-' + k} />{l}</span>)}
        </div>
      )}
    </div>
  );
}

interface ViewProps {
  map: TopicMapData;
  lit: Set<number> | null;
  selected: number | null;
  onOpen: (id: number) => void;
  width: number;
  height: number;
  wide: boolean;
}

// layout places topics with a force simulation: each cluster gets a home
// on a ring (linked clusters next to each other), its topics pull together
// along their links and push each other apart, and links between clusters
// pull only lightly, so clusters read as regions. Topics linked to nothing
// go in a band along the bottom. Seeded, so the picture is stable.
function layout(map: TopicMapData): Map<number, [number, number]> {
  const ts = map.topics ?? [];
  const es = map.edges ?? [];
  const pos = new Map<number, [number, number]>();
  const linkedTs = ts.filter((t) => t.cluster >= 0);
  const loose = ts.filter((t) => t.cluster < 0);
  const n = linkedTs.length;
  const band = loose.length ? 0.1 : 0;
  loose.forEach((t, i) => {
    const per = Math.max(1, Math.ceil(loose.length / 2));
    pos.set(t.id, [(i % per + 0.5) / per, 1 - band / 2 + (Math.floor(i / per) - 0.5) * band * 0.45]);
  });
  if (!n) return pos;
  const idx = new Map(linkedTs.map((t, i) => [t.id, i]));
  const clusters = map.clusters ?? [];
  const nc = Math.max(1, clusters.length);
  // Ring order: start from the biggest cluster, then repeatedly the
  // cluster most linked to the last one placed.
  const cw = new Map<string, number>();
  const cl = (id: number) => ts.find((t) => t.id === id)?.cluster ?? -1;
  for (const e of es) {
    const a = cl(e.a), b = cl(e.b);
    if (a >= 0 && b >= 0 && a !== b) { cw.set(a + ':' + b, (cw.get(a + ':' + b) ?? 0) + e.w); cw.set(b + ':' + a, (cw.get(b + ':' + a) ?? 0) + e.w); }
  }
  const order: number[] = [];
  const left = new Set(clusters.map((c) => c.id));
  let last = clusters.length ? clusters[0].id : 0;
  while (left.size) {
    let best = -1, bw = -1;
    for (const c of left) { const w = cw.get(last + ':' + c) ?? 0; if (w > bw || (w === bw && c < best)) { best = c; bw = w; } }
    if (!order.length) best = last;
    order.push(best); left.delete(best); last = best;
  }
  const slot = new Map(order.map((c, i) => [c, i]));
  const home = (c: number): [number, number] => {
    if (nc === 1) return [0.5, 0.5];
    const a = ((slot.get(c) ?? 0) / nc) * Math.PI * 2 - Math.PI / 2;
    return [0.5 + Math.cos(a) * 0.36, 0.5 + Math.sin(a) * 0.36];
  };
  let seed = 7;
  const rnd = () => ((seed = (seed * 16807) % 2147483647) / 2147483647);
  const x = new Float64Array(n), y = new Float64Array(n);
  const hx = new Float64Array(n), hy = new Float64Array(n), cid = new Int32Array(n);
  linkedTs.forEach((t, i) => {
    [hx[i], hy[i]] = home(t.cluster);
    cid[i] = t.cluster;
    x[i] = hx[i] + (rnd() - 0.5) * 0.1; y[i] = hy[i] + (rnd() - 0.5) * 0.1;
  });
  const maxW = Math.max(1e-9, ...es.map((e) => e.w));
  const links = es.map((e) => [idx.get(e.a), idx.get(e.b), e.w / maxW] as const)
    .filter((l): l is readonly [number, number, number] => l[0] !== undefined && l[1] !== undefined);
  const k = 0.75 * Math.sqrt(1 / n) * (nc > 1 ? 0.8 : 1.2);
  const dx = new Float64Array(n), dy = new Float64Array(n);
  const iters = 360;
  for (let it = 0; it < iters; it++) {
    const temp = 0.05 * (1 - it / iters) + 0.001;
    dx.fill(0); dy.fill(0);
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        let ddx = x[i] - x[j], ddy = y[i] - y[j];
        let d2 = ddx * ddx + ddy * ddy;
        if (d2 < 1e-8) { ddx = (rnd() - 0.5) * 1e-3; ddy = (rnd() - 0.5) * 1e-3; d2 = 1e-6; }
        const f = (k * k) / d2 * (cid[i] === cid[j] ? 1 : 1.6);
        dx[i] += ddx * f; dy[i] += ddy * f; dx[j] -= ddx * f; dy[j] -= ddy * f;
      }
    }
    for (const [a, b, w] of links) {
      const ddx = x[a] - x[b], ddy = y[a] - y[b];
      const d = Math.sqrt(ddx * ddx + ddy * ddy) + 1e-9;
      const f = (d / k) * (0.3 + w) * (cid[a] === cid[b] ? 1 : 0.06);
      dx[a] -= ddx * f; dy[a] -= ddy * f; dx[b] += ddx * f; dy[b] += ddy * f;
    }
    for (let i = 0; i < n; i++) {
      dx[i] += (hx[i] - x[i]) * 0.9; dy[i] += (hy[i] - y[i]) * 0.9;
      const d = Math.sqrt(dx[i] * dx[i] + dy[i] * dy[i]) + 1e-9;
      const s = Math.min(d, temp) / d;
      x[i] += dx[i] * s; y[i] += dy[i] * s;
    }
  }
  let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity;
  for (let i = 0; i < n; i++) { x0 = Math.min(x0, x[i]); x1 = Math.max(x1, x[i]); y0 = Math.min(y0, y[i]); y1 = Math.max(y1, y[i]); }
  linkedTs.forEach((t, i) => pos.set(t.id, [(x[i] - x0) / (x1 - x0 || 1), ((y[i] - y0) / (y1 - y0 || 1)) * (1 - band * 1.4)]));
  return pos;
}

function neighbours(edges: TopicEdge[], id: number | null): Set<number> {
  const s = new Set<number>();
  if (id === null) return s;
  for (const e of edges) {
    if (e.a === id) s.add(e.b);
    if (e.b === id) s.add(e.a);
  }
  return s;
}

function GraphMap({ map, lit, selected, onOpen, width, height, wide }: ViewProps) {
  const pos = useMemo(() => layout(map), [map]);
  return <NodeLink map={map} pos={pos} lit={lit} selected={selected} onOpen={onOpen} width={width} height={height} wide={wide} edgeAlpha={1} />;
}

function MeaningMap({ map, lit, selected, onOpen, width, height, wide }: ViewProps) {
  // Half linear, half by rank: a few outlying topics would otherwise
  // squeeze everything else into the middle.
  const pos = useMemo(() => {
    const ts = map.topics ?? [];
    const axis = (v: number[]) => {
      const lo = Math.min(...v), hi = Math.max(...v);
      const order = v.map((x, i) => [x, i] as const).sort((a, b) => a[0] - b[0]);
      const rank = new Array<number>(v.length);
      order.forEach(([, i], r) => { rank[i] = v.length > 1 ? r / (v.length - 1) : 0.5; });
      return v.map((x, i) => 0.5 * ((x - lo) / (hi - lo || 1)) + 0.5 * rank[i]);
    };
    const xs = axis(ts.map((t) => t.x)), ys = axis(ts.map((t) => t.y));
    return new Map(ts.map((t, i) => [t.id, [xs[i], ys[i]] as [number, number]]));
  }, [map]);
  return <NodeLink map={map} pos={pos} lit={lit} selected={selected} onOpen={onOpen} width={width} height={height} wide={wide} edgeAlpha={0.35} />;
}

function NodeLink({ map, pos, lit, selected, onOpen, width, height, wide, edgeAlpha }: ViewProps & { pos: Map<number, [number, number]>; edgeAlpha: number }) {
  const [hover, setHover] = useState<number | null>(null);
  const ts = map.topics ?? [];
  const es = map.edges ?? [];
  const pad = 26;
  const P = (id: number): [number, number] => {
    const p = pos.get(id) ?? [0.5, 0.5];
    return [pad + p[0] * (width - 2 * pad), pad + p[1] * (height - 2 * pad)];
  };
  const maxS = Math.max(1, ...ts.map((t) => t.statements));
  const maxW = Math.max(1e-9, ...es.map((e) => e.w));
  const focus = hover ?? selected;
  const near = neighbours(es, focus);
  const clusterOf = new Map(ts.map((t) => [t.id, t.cluster]));
  const dim = (id: number) => (lit && !lit.has(id)) || (focus !== null && id !== focus && !near.has(id));
  const rOf = (t: MapTopic) => 3 + (wide ? 11 : 8) * Math.sqrt(t.statements / maxS);
  const maxLen = wide ? 34 : 24;
  // Labels: the focused topic and its neighbours first, then the biggest,
  // each only where it doesn't cover one already placed.
  const labelled = useMemo(() => {
    const prio = (t: MapTopic) => (t.id === focus ? 3 : near.has(t.id) ? 2 : lit?.has(t.id) && lit.size <= 25 ? 1 : 0);
    const cand = ts.filter((t) => !dim(t.id)).sort((a, b) => prio(b) - prio(a) || b.statements - a.statements);
    const boxes: [number, number, number, number][] = [];
    const out = new Set<number>();
    const limit = focus !== null ? 40 : wide ? 40 : 14;
    for (const t of cand) {
      if (out.size >= limit) break;
      const [x, y] = P(t.id);
      const r = rOf(t);
      const w = Math.min(t.label.length, maxLen) * (wide ? 6.1 : 5.6) + 4;
      const b: [number, number, number, number] = [x - w / 2, y - r - 16, x + w / 2, y - r - 2];
      if (prio(t) < 3 && boxes.some((o) => b[0] < o[2] && b[2] > o[0] && b[1] < o[3] && b[3] > o[1])) continue;
      boxes.push(b);
      out.add(t.id);
    }
    return out;
  }, [map, pos, focus, lit, width, height, wide]);
  return (
    <svg className="topic-map" width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
      {es.map((e) => {
        const [ax, ay] = P(e.a), [bx, by] = P(e.b);
        const on = focus !== null && (e.a === focus || e.b === focus);
        const faded = (lit && !(lit.has(e.a) && lit.has(e.b))) || (focus !== null && !on);
        const across = clusterOf.get(e.a) !== clusterOf.get(e.b);
        return (
          <line key={e.a + '-' + e.b} x1={ax} y1={ay} x2={bx} y2={by} className={'edge e-' + e.kind}
            strokeWidth={0.6 + 2.6 * (e.w / maxW)} strokeOpacity={faded ? 0.05 : on ? 0.9 : (across ? 0.12 : 0.4) * edgeAlpha}>
            <title>{edgeLabel[e.kind]}{e.via?.length ? ': ' + e.via.join(', ') : ''}</title>
          </line>
        );
      })}
      {ts.map((t) => {
        const [x, y] = P(t.id);
        const r = rOf(t);
        const d = dim(t.id);
        const showLabel = labelled.has(t.id);
        return (
          <g key={t.id} className={'tnode' + (t.id === selected ? ' sel' : '')} opacity={d ? 0.18 : 1} onClick={() => onOpen(t.id)}
            onMouseEnter={() => setHover(t.id)} onMouseLeave={() => setHover(null)}>
            <circle cx={x} cy={y} r={r} fill={clusterColor(t.cluster)} fillOpacity={0.8}
              stroke={t.statuses.contested ? 'var(--lvl-confidential)' : t.id === selected ? 'var(--text)' : 'var(--bg)'} strokeWidth={t.id === selected ? 2 : 1} />
            {showLabel && <text x={x} y={y - r - 4} textAnchor="middle" className={t.id === focus ? 'hot' : ''}>{short(t.label, maxLen)}</text>}
            <title>{t.label} · {t.statements} statements · last {fmtDate(t.last)}</title>
          </g>
        );
      })}
    </svg>
  );
}

// Timeline: one row per topic, one column per meeting (oldest first); a
// dot's size is how much of that meeting was on the topic. Rows are grouped
// by cluster, and within a cluster by when the topic first came up.
function Timeline({ map, lit, selected, onOpen, width, wide }: ViewProps) {
  // Clusters start folded when there are many rows: a folded cluster is
  // one row, its topics' activity summed per meeting.
  const [open, setOpen] = useState<Set<number> | 'all' | null>(null);
  const items = map.items ?? [];
  const col = new Map(items.map((it, i) => [it.id, i]));
  const ts = (map.topics ?? []).filter((t) => !lit || lit.has(t.id));
  const clusters = [...(map.clusters ?? []), { id: -1, label: 'Not linked', topics: [], statements: 0, last: '' } as TopicCluster];
  const first = (t: MapTopic) => Math.min(...(t.activity ?? []).map((a) => col.get(a.item) ?? 1e9));
  const groups = clusters.map((c) => ({
    c, rows: ts.filter((t) => t.cluster === c.id).sort((a, b) => first(a) - first(b) || b.statements - a.statements),
  })).filter((g) => g.rows.length > 0);
  const labelW = wide ? 260 : 150;
  const colW = Math.max(18, (width - labelW - 12) / Math.max(1, items.length));
  const rowH = 17, headH = 46, groupH = 20;
  const maxN = Math.max(1, ...ts.flatMap((t) => (t.activity ?? []).map((a) => a.n)));
  // Lit topics (a filter, a cluster chip) open their clusters.
  const folding = !lit && ts.length > 40;
  const isOpen = (id: number) => !folding || open === 'all' || (open instanceof Set && open.has(id));
  const toggle = (id: number) => {
    const cur = open === 'all' ? new Set(groups.map((g) => g.c.id)) : new Set(open ?? []);
    if (cur.has(id)) cur.delete(id);
    else cur.add(id);
    setOpen(cur);
  };
  // The names stay put while the meetings scroll sideways: names and dots
  // are two drawings side by side in one scrolling box, the names sticky.
  const cx = (i: number) => colW * (i + 0.5);
  const dotsW = Math.max(width - labelW, colW * items.length + 12);
  // Dates label every few columns so they don't overlap.
  const step = Math.max(1, Math.ceil(48 / colW));
  let yy = headH;
  const names: JSX.Element[] = [];
  const dots: JSX.Element[] = [];
  for (const g of groups) {
    const opened = isOpen(g.c.id);
    // A folded cluster's dots: its topics' statements per meeting.
    const sum = new Map<number, number>();
    if (!opened) {
      for (const t of g.rows) for (const a of t.activity ?? []) {
        const i = col.get(a.item);
        if (i !== undefined) sum.set(i, (sum.get(i) ?? 0) + a.n);
      }
    }
    const maxSum = Math.max(1, ...sum.values());
    const click = () => folding && toggle(g.c.id);
    const cls = 'tl-grow' + (folding ? ' foldable' : '');
    names.push(
      <g key={'g' + g.c.id} className={cls} onClick={click}>
        <rect x={0} y={yy} width={labelW} height={groupH} className="tl-hit" />
        <rect x={0} y={yy + 4} width={4} height={groupH - 8} fill={clusterColor(g.c.id)} rx={2} />
        <text x={10} y={yy + 14} className="tl-group">{folding ? (opened ? '▾ ' : '▸ ') : ''}{short(g.c.label, wide ? 34 : 18)} · {g.rows.length}<title>{g.c.label}</title></text>
      </g>,
    );
    dots.push(
      <g key={'g' + g.c.id} className={cls} onClick={click}>
        <rect x={0} y={yy} width={dotsW} height={groupH} className="tl-hit" />
        {[...sum.entries()].map(([i, n]) => <circle key={i} cx={cx(i)} cy={yy + groupH / 2} r={2 + 5.5 * Math.sqrt(n / maxSum)} fill={clusterColor(g.c.id)} fillOpacity={0.8}><title>{n} statements</title></circle>)}
      </g>,
    );
    yy += groupH;
    if (!opened) continue;
    for (const t of g.rows) {
      const y = yy + rowH / 2;
      const cols = (t.activity ?? []).map((a) => ({ i: col.get(a.item) ?? -1, n: a.n })).filter((a) => a.i >= 0).sort((a, b) => a.i - b.i);
      const tcls = 'tl-trow' + (t.id === selected ? ' sel' : '');
      names.push(
        <g key={t.id} className={tcls} onClick={() => onOpen(t.id)}>
          <rect x={0} y={yy} width={labelW} height={rowH} className="tl-hit" />
          <text x={10} y={y + 4} className="tl-label">{short(t.label, wide ? 40 : 22)}<title>{t.label}</title></text>
        </g>,
      );
      dots.push(
        <g key={t.id} className={tcls} onClick={() => onOpen(t.id)}>
          <rect x={0} y={yy} width={dotsW} height={rowH} className="tl-hit" />
          {cols.length > 1 && <line x1={cx(cols[0].i)} x2={cx(cols[cols.length - 1].i)} y1={y} y2={y} stroke={clusterColor(t.cluster)} strokeOpacity={0.35} strokeWidth={2} />}
          {cols.map((a) => <circle key={a.i} cx={cx(a.i)} cy={y} r={2 + 5.5 * Math.sqrt(a.n / maxN)} fill={clusterColor(t.cluster)}><title>{a.n} statements</title></circle>)}
        </g>,
      );
      yy += rowH;
    }
  }
  if (!items.length) return <Empty>No dated sources to lay out.</Empty>;
  return (
    <>
    {folding && (
      <div className="tl-fold">
        <button className="link" onClick={() => setOpen('all')}>open all clusters</button>
        <button className="link" onClick={() => setOpen(null)}>fold all</button>
        <span className="dim small">click a cluster to open it</span>
      </div>
    )}
    <div className="tl-scroll" style={{ maxHeight: wide ? window.innerHeight - 260 : 560 }}>
      <div className="tl-split">
        <svg className="topic-map tl tl-names" width={labelW} height={yy + 8}>{names}</svg>
        <svg className="topic-map tl" width={dotsW} height={yy + 8}>
          {items.map((it, i) => (
            <g key={it.id}>
              <line x1={cx(i)} x2={cx(i)} y1={headH - 6} y2={yy} className="tl-col" />
              {i % step === 0 && <text x={i === 0 ? 2 : cx(i)} y={16} textAnchor={i === 0 ? 'start' : 'middle'} className="tl-head">{fmtDate(it.occurredAt)}</text>}
              {colW > 70 && <text x={cx(i)} y={30} textAnchor="middle" className="tl-sub">{short(it.title, Math.floor(colW / 6.5))}</text>}
              <title>{it.title}</title>
            </g>
          ))}
          {dots}
        </svg>
      </div>
    </div>
    </>
  );
}

// ClusterCards: each cluster with its topics, and the clusters it links to
// most.
function ClusterCards({ map, onOpen, selected }: ViewProps) {
  const ts = new Map((map.topics ?? []).map((t) => [t.id, t]));
  const clusterOf = (id: number) => ts.get(id)?.cluster ?? -1;
  const between = new Map<string, number>();
  for (const e of map.edges ?? []) {
    const a = clusterOf(e.a), b = clusterOf(e.b);
    if (a === b || a < 0 || b < 0) continue;
    for (const [x, y] of [[a, b], [b, a]]) between.set(x + ':' + y, (between.get(x + ':' + y) ?? 0) + e.w);
  }
  const clusters = map.clusters ?? [];
  const unlinked = (map.topics ?? []).filter((t) => t.cluster < 0);
  return (
    <div className="cluster-cards">
      {clusters.map((c) => {
        const links = clusters.filter((o) => o.id !== c.id && between.get(c.id + ':' + o.id))
          .sort((a, b) => (between.get(c.id + ':' + b.id) ?? 0) - (between.get(c.id + ':' + a.id) ?? 0)).slice(0, 3);
        return (
          <div key={c.id} className="cluster-card" style={{ borderTopColor: clusterColor(c.id) }}>
            <div className="cc-head"><span className="cl-dot" style={{ background: clusterColor(c.id) }} />{c.label}</div>
            <div className="dim small">{c.topics.length} topics · {c.statements} statements · last {fmtDate(c.last)}</div>
            <div className="cc-topics">
              {c.topics.slice(0, 10).map((id) => (
                <button key={id} className={'cc-topic' + (id === selected ? ' sel' : '')} onClick={() => onOpen(id)}>
                  {ts.get(id)?.label}<span className="dim"> {ts.get(id)?.statements}</span>
                </button>
              ))}
              {c.topics.length > 10 && <span className="dim small">and {c.topics.length - 10} more</span>}
            </div>
            {links.length > 0 && <div className="dim small">linked to {links.map((o) => short(o.label, 30)).join(' · ')}</div>}
          </div>
        );
      })}
      {unlinked.length > 0 && (
        <div className="cluster-card" style={{ borderTopColor: 'var(--text-faint)' }}>
          <div className="cc-head"><span className="cl-dot" style={{ background: 'var(--text-faint)' }} />Not linked</div>
          <div className="dim small">{unlinked.length} topics with no link to any other</div>
          <div className="cc-topics">
            {unlinked.slice(0, 12).map((t) => <button key={t.id} className="cc-topic" onClick={() => onOpen(t.id)}>{t.label}</button>)}
          </div>
        </div>
      )}
    </div>
  );
}

function short(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}
