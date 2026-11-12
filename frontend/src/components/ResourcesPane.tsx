import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { api, on } from '../api';
import type { ResReading, ResSample, ResSnapshot } from '../types';

// The resource readout ([diagnostics] resources): CPU and memory per
// component. The panel watches while it is open (the backend samples only
// then) and renews the watch, since a closed window can't say so.

const window15 = 15 * 60 * 1000;
const keep = 450;
const renewEvery = 15_000;

// Colour follows the component, never its rank: a component that comes
// and goes keeps its line colour.
const order = ['musubi', 'webview', 'recorder', 'ollama', 'tomoe'];
const color = (key: string) => `var(--series-${order.indexOf(key) + 1 || 6})`;

export interface Resources {
  snap: ResSnapshot | null;
  samples: ResSample[];
}

// useResources watches for as long as the component using it is mounted
// (the diagnostics panel), so history builds while another tab is shown.
export function useResources(enabled: boolean): Resources {
  const [snap, setSnap] = useState<ResSnapshot | null>(null);
  const [samples, setSamples] = useState<ResSample[]>([]);
  useEffect(() => {
    if (!enabled) return;
    let live = true;
    const watch = () => api.watchResources().then((s) => {
      if (!live) return;
      setSnap(s);
      // Events may arrive before the history does: keep both, in order.
      setSamples((prev) => {
        const h = s.history ?? [];
        if (!prev.length) return h;
        const first = Date.parse(prev[0].at);
        return [...h.filter((x) => Date.parse(x.at) < first), ...prev].slice(-keep);
      });
    }, () => {});
    watch();
    const t = setInterval(watch, renewEvery);
    const off = on<ResSample>('diag:resources', (s) => setSamples((prev) => [...prev, s].slice(-keep)));
    return () => { live = false; clearInterval(t); off(); api.unwatchResources(); };
  }, [enabled]);
  return { snap, samples };
}

export function fmtCPU(v: number): string {
  if (v >= 10) return `${Math.round(v)}%`;
  if (v >= 0.05) return `${v.toFixed(1)}%`;
  return '0%';
}

export function fmtMem(b: number): string {
  if (b >= 1 << 30) return `${(b / (1 << 30)).toFixed(1)} GB`;
  return `${Math.round(b / (1 << 20))} MB`;
}

// mem is what the memory chart and column show: macOS's footprint (what
// Activity Monitor calls Memory) when there is one, else resident.
const mem = (r: { footprint: number; rss: number }) => (r.footprint > 0 ? r.footprint : r.rss);

const clock = (t: number) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const hhmm = (t: number) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

export function ResourcesPane({ res }: { res: Resources }) {
  const { snap, samples } = res;
  const [hover, setHover] = useState<number | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});

  const series = useMemo(() => {
    const seen = new Map<string, string>();
    for (const s of samples) for (const c of s.components ?? []) if (!seen.has(c.key)) seen.set(c.key, c.name);
    return order.filter((k) => seen.has(k)).map((k) => ({ key: k, name: seen.get(k)! }));
  }, [samples]);
  const pts = useMemo(() => samples.map((s) => ({ t: Date.parse(s.at), by: Object.fromEntries((s.components ?? []).map((c) => [c.key, c])) as Record<string, ResReading> })), [samples]);

  if (!snap) return <div className="empty">Reading processes…</div>;
  if (!snap.supported) return <div className="empty">{snap.reason || 'Not available.'}</div>;

  const last = samples[samples.length - 1];
  const hovered = hover !== null ? pts[hover] : null;
  const shown = hovered ?? (pts.length ? pts[pts.length - 1] : null);
  const interval = snap.intervalMs || 2000;
  const m = snap.machine;

  return (
    <div className="res">
      <p className="res-help">
        CPU: 100% is one core fully busy{m.cores ? ` (this Mac: ${m.cores} cores${m.memory ? `, ${fmtMem(m.memory)} memory` : ''})` : ''}.
        Memory is what Activity Monitor shows; resident also counts mapped file pages (for Musubi, its database), which macOS can drop at any time.
      </p>

      {!last ? <div className="empty">The first reading arrives in {interval / 1000} s.</div> : <>
        <table className="dt res-table">
          <thead>
            <tr>
              <th>{hovered ? `at ${clock(hovered.t)}` : 'now'}</th>
              <th className="num">CPU</th>
              <th className="num" title="phys_footprint: what Activity Monitor shows as Memory">memory</th>
              <th className="num" title="resident: includes mapped file pages">resident</th>
            </tr>
          </thead>
          <tbody>
            {series.map(({ key, name }) => {
              const r = shown?.by[key];
              const many = (r?.parts?.length ?? 0) > 1;
              return [
                <tr key={key} className={many ? 'res-group' : ''} onClick={() => many && setOpen((o) => ({ ...o, [key]: !o[key] }))}>
                  <td><span className="res-key" style={{ background: color(key) }} />{name}{many && <span className="dim"> · {r!.parts.length} {open[key] ? '▾' : '▸'}</span>}</td>
                  <td className="num">{r ? fmtCPU(r.cpu) : '–'}</td>
                  <td className="num">{r ? fmtMem(mem(r)) : '–'}</td>
                  <td className="num dim">{r ? fmtMem(r.rss) : '–'}</td>
                </tr>,
                ...(many && open[key] ? r!.parts.map((p) => (
                  <tr key={`${key}-${p.pid}`} className="res-part">
                    <td title={`pid ${p.pid}`}>{p.name}</td>
                    <td className="num">{fmtCPU(p.cpu)}</td>
                    <td className="num">{fmtMem(mem(p))}</td>
                    <td className="num dim">{fmtMem(p.rss)}</td>
                  </tr>
                )) : []),
              ];
            })}
          </tbody>
        </table>

        <div className="res-legend" aria-label="legend">
          {series.map((s) => <span key={s.key}><i style={{ background: color(s.key) }} />{s.name}</span>)}
        </div>
        <Chart title="CPU, % of one core" pts={pts} series={series} value={(r) => r.cpu} fmt={fmtCPU} floor={100}
          height={150} interval={interval} hover={hover} setHover={setHover} />
        <Chart title="Memory" pts={pts} series={series} value={mem} fmt={fmtMem} floor={1 << 30}
          height={110} interval={interval} hover={hover} setHover={setHover} bytes />
      </>}
    </div>
  );
}

type Pt = { t: number; by: Record<string, ResReading> };

// niceStep rounds a tick step to 1, 2 or 5 times a power of ten (of 1024³
// for bytes, so ticks fall on whole gigabytes where they can).
function niceStep(max: number, ticks: number, bytes: boolean): number {
  const unit = bytes ? 1 << 30 : 1;
  const raw = max / unit / ticks;
  const p = Math.pow(10, Math.floor(Math.log10(raw)));
  const f = raw / p;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p * unit;
}

function Chart({ title, pts, series, value, fmt, floor, height, interval, hover, setHover, bytes = false }: {
  title: string; pts: Pt[]; series: { key: string; name: string }[]; value: (r: ResReading) => number;
  fmt: (v: number) => string; floor: number; height: number; interval: number;
  hover: number | null; setHover: (i: number | null) => void; bytes?: boolean;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(420);
  // The crosshair shows in both charts; the tooltip only in the one
  // under the pointer.
  const [here, setHere] = useState(false);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    setW(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  const pad = { l: 44, r: 8, t: 6, b: 18 };
  const iw = Math.max(40, w - pad.l - pad.r);
  const ih = height - pad.t - pad.b;
  const end = pts.length ? pts[pts.length - 1].t : Date.now();
  const start = end - window15;
  let max = floor;
  for (const p of pts) for (const s of series) { const r = p.by[s.key]; if (r) max = Math.max(max, value(r)); }
  const step = niceStep(max, 3, bytes);
  const top = Math.ceil(max / step) * step;
  const x = (t: number) => pad.l + ((t - start) / window15) * iw;
  const y = (v: number) => pad.t + ih - (v / top) * ih;
  const ticks: number[] = [];
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v);
  const fmtTick = bytes ? (v: number) => `${Math.round(v / (1 << 30))} GB` : (v: number) => `${Math.round(v)}%`;
  const xTicks: number[] = [];
  const five = 5 * 60 * 1000;
  for (let t = Math.ceil(start / five) * five; t <= end; t += five) xTicks.push(t);

  // A line breaks where sampling stopped (the panel was closed) or the
  // component wasn't running, rather than drawing across the gap.
  const paths = series.map(({ key }) => {
    let d = '';
    let prevT = -Infinity;
    for (const p of pts) {
      if (p.t < start) continue;
      const r = p.by[key];
      if (!r) { prevT = -Infinity; continue; }
      const cmd = p.t - prevT > interval * 3 ? 'M' : 'L';
      d += `${cmd}${x(p.t).toFixed(1)},${y(value(r)).toFixed(1)}`;
      prevT = p.t;
    }
    return { key, d };
  });

  const nearest = (clientX: number) => {
    const el = box.current;
    if (!el || !pts.length) return null;
    const px = clientX - el.getBoundingClientRect().left;
    const t = start + ((px - pad.l) / iw) * window15;
    let best = -1, bd = Infinity;
    pts.forEach((p, i) => { if (p.t >= start && Math.abs(p.t - t) < bd) { bd = Math.abs(p.t - t); best = i; } });
    return best >= 0 ? best : null;
  };
  const hp = hover !== null ? pts[hover] : null;
  const tipLeft = hp ? x(hp.t) > w / 2 : false;

  return (
    <figure className="res-chart">
      <figcaption>{title}</figcaption>
      <div ref={box} className="res-plot" onPointerMove={(e) => { setHere(true); setHover(nearest(e.clientX)); }} onPointerLeave={() => { setHere(false); setHover(null); }}>
        <svg width={w} height={height} role="img" aria-label={`${title} over the last 15 minutes`}>
          {ticks.map((v) => (
            <g key={v}>
              <line className="res-grid" x1={pad.l} x2={pad.l + iw} y1={y(v)} y2={y(v)} />
              <text className="res-axis" x={pad.l - 6} y={y(v) + 3.5} textAnchor="end">{fmtTick(v)}</text>
            </g>
          ))}
          {xTicks.map((t) => <text key={t} className="res-axis" x={x(t)} y={height - 4} textAnchor="middle">{hhmm(t)}</text>)}
          {paths.map((p) => <path key={p.key} d={p.d} fill="none" stroke={color(p.key)} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />)}
          {hp && <>
            <line className="res-cross" x1={x(hp.t)} x2={x(hp.t)} y1={pad.t} y2={pad.t + ih} />
            {series.map((s) => {
              const r = hp.by[s.key];
              return r ? <circle key={s.key} cx={x(hp.t)} cy={y(value(r))} r={4} fill={color(s.key)} className="res-dot" /> : null;
            })}
          </>}
        </svg>
        {hp && here && (
          <div className="res-tip" style={tipLeft ? { right: w - x(hp.t) + 8 } : { left: x(hp.t) + 8 }}>
            <div className="dim">{clock(hp.t)}</div>
            {series.map((s) => {
              const r = hp.by[s.key];
              return r ? <div key={s.key} className="res-tip-row"><i style={{ background: color(s.key) }} /><b>{fmt(value(r))}</b><span className="dim">{s.name}</span></div> : null;
            })}
          </div>
        )}
      </div>
    </figure>
  );
}
