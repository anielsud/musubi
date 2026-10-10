import { useMemo, useState } from 'react';
import type { EntityRef, Graph } from '../types';

const typeColor: Record<string, string> = {
  person: 'var(--thread-person)',
  org: 'var(--thread-org)',
  project: 'var(--thread-term)',
  topic: 'var(--thread-term)',
  term: 'var(--thread-term)',
  group: 'var(--text-faint)',
};

// GraphView draws an entity at the centre and its connections as threads:
// closer and thicker the more items they share. Deterministic radial layout,
// so the picture doesn't jump between renders.
export function GraphView({ graph, onPick }: { graph: Graph; onPick: (e: EntityRef) => void }) {
  const [hover, setHover] = useState<string | null>(null);
  const nbs = (graph.neighbors ?? []).slice(0, 18);
  const W = 420, H = 340, cx = W / 2, cy = H / 2;
  const maxItems = Math.max(1, ...nbs.map((n) => n.items));

  const nodes = useMemo(() => {
    // Strongest ties on the inner ring, the rest outside; spread by angle.
    const inner = nbs.slice(0, 7), outer = nbs.slice(7);
    const place = (list: EntityRef[], r: number, offset: number) =>
      list.map((n, i) => {
        const a = offset + (i / Math.max(1, list.length)) * Math.PI * 2;
        const pull = 1 - 0.25 * (n.items / maxItems);
        return { n, x: cx + Math.cos(a) * r * pull, y: cy + Math.sin(a) * r * pull * 0.82 };
      });
    return [...place(inner, 105, -Math.PI / 2), ...place(outer, 160, -Math.PI / 2 + 0.35)];
  }, [graph]);

  return (
    <svg className="graph" viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`Connections of ${graph.entity.name}`}>
      <defs>
        <radialGradient id="glow">
          <stop offset="0%" stopColor="var(--accent)" stopOpacity="0.35" />
          <stop offset="100%" stopColor="var(--accent)" stopOpacity="0" />
        </radialGradient>
      </defs>
      <circle cx={cx} cy={cy} r={70} fill="url(#glow)" />
      {nodes.map(({ n, x, y }) => {
        const mx = (cx + x) / 2 + (y - cy) * 0.18, my = (cy + y) / 2 - (x - cx) * 0.18;
        const w = 0.8 + 3.2 * (n.items / maxItems);
        const hot = hover === n.id;
        return (
          <path key={'t' + n.id} d={`M${cx},${cy} Q${mx},${my} ${x},${y}`} fill="none"
            stroke={typeColor[n.type] ?? 'var(--text-faint)'} strokeOpacity={hot ? 0.95 : 0.45} strokeWidth={hot ? w + 1 : w} />
        );
      })}
      {nodes.map(({ n, x, y }) => {
        const r = 4 + 6 * (n.items / maxItems);
        return (
          <g key={n.id} className="gnode" onClick={() => onPick(n)} onMouseEnter={() => setHover(n.id)} onMouseLeave={() => setHover(null)}>
            <circle cx={x} cy={y} r={r} fill={typeColor[n.type] ?? 'var(--text-faint)'} />
            <text x={x} y={y + r + 11} textAnchor="middle" className={hover === n.id ? 'hot' : ''}>
              {short(n.name)}{hover === n.id ? ` · ${n.items}` : ''}
            </text>
          </g>
        );
      })}
      <g className="gcenter">
        <circle cx={cx} cy={cy} r={15} fill={typeColor[graph.entity.type] ?? 'var(--accent)'} stroke="var(--bg)" strokeWidth={3} />
        <text x={cx} y={cy + 32} textAnchor="middle">{graph.entity.name}</text>
      </g>
    </svg>
  );
}

function short(s: string): string {
  const parts = s.split(' ');
  const t = parts.length > 2 ? parts[0] + ' ' + parts[parts.length - 1] : s;
  return t.length > 18 ? t.slice(0, 17) + '…' : t;
}
