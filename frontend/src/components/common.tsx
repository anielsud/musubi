import { Fragment, ReactNode } from 'react';
import type { Hit, Label } from '../types';
import { fmtDate, kindIcon } from '../api';

export function LabelBadge({ label, compact }: { label: Label; compact?: boolean }) {
  return (
    <span className={`badge lvl-${label.level}`} title={label.reason || label.level}>
      {compact ? label.level[0].toUpperCase() : label.level}
      {label.categories?.length ? <span className="cats"> · {label.categories.join(', ')}</span> : null}
    </span>
  );
}

// CitedText renders "[2]" and "[1][3]" as chips that open the cited source.
export function CitedText({ text, hits, onCite }: { text: string; hits: Hit[]; onCite: (h: Hit) => void }) {
  const parts: ReactNode[] = [];
  const re = /\[(\d+)\]/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    const n = parseInt(m[1], 10);
    const h = hits[n - 1];
    parts.push(
      h ? (
        <button key={m.index} className="cite" onClick={() => onCite(h)} title={`${h.title} · ${fmtDate(h.occurredAt)}`}>
          {n}
        </button>
      ) : (
        <Fragment key={m.index}>{m[0]}</Fragment>
      ),
    );
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <>{parts}</>;
}

export function HitRow({ hit, n, onOpen }: { hit: Hit; n?: number; onOpen: (h: Hit) => void }) {
  return (
    <button className="hit" onClick={() => onOpen(hit)}>
      <div className="hit-head">
        {n !== undefined && <span className="hit-n">{n}</span>}
        <span className="hit-kind">{kindIcon[hit.kind] ?? '·'}</span>
        <span className="hit-title">{hit.title || hit.itemId}</span>
        <span className="hit-date">{fmtDate(hit.occurredAt)}</span>
        <LabelBadge label={hit.label} compact />
      </div>
      <div className="hit-text">{snippet(hit.text, 220)}</div>
    </button>
  );
}

export function snippet(s: string, n: number): string {
  const t = s.replace(/\s+/g, ' ').replace(/Person \d+ \(([^)]+)\)/g, '$1').trim();
  return t.length > n ? t.slice(0, n) + '…' : t;
}

export function Spinner() {
  return <span className="spinner" aria-label="working" />;
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

// Spelled shows a statement's text as the owner's spelling rules read it,
// with what was written on hover when a rule changed it.
export function Spelled({ s }: { s: { text: string; original?: string } }) {
  if (!s.original) return <>{s.text}</>;
  return <span className="respelled" title={`Written as: ${s.original}`}>{s.text}</span>;
}
