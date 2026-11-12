import { useEffect, useRef, useState } from 'react';
import { api, fmtDate, kindIcon } from '../api';
import type { ItemView } from '../types';
import { LabelBadge, Spinner } from './common';
import { Name } from './people';

export interface SourceRef {
  itemId: string;
  chunkId?: number;
  scope: string;
  // at, in seconds into the meeting: scroll to the passage it falls in
  // (a voice clip's start), when no chunkId is given.
  at?: number;
}

// SourceDrawer shows a source item's passages under the same scope the
// citation came from, with the cited passage highlighted.
export function SourceDrawer({ src, onClose }: { src: SourceRef; onClose: () => void }) {
  const [item, setItem] = useState<ItemView | null | undefined>(undefined);
  const [err, setErr] = useState('');
  const target = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setItem(undefined);
    api.item(src.itemId, src.scope).then(setItem, (e) => setErr(String(e)));
  }, [src.itemId, src.scope]);

  // The passage to show: the cited one, or the last one starting at or
  // before src.at (passages are in meeting order).
  let cited = src.chunkId;
  if (cited === undefined && src.at !== undefined && item) {
    for (const c of item.chunks ?? []) {
      if (c.start <= src.at + 0.5) cited = c.chunkId;
      else break;
    }
  }

  useEffect(() => {
    target.current?.scrollIntoView({ block: 'center' });
  }, [item, cited]);

  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', k);
    return () => window.removeEventListener('keydown', k);
  }, [onClose]);

  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <aside className="drawer" onClick={(e) => e.stopPropagation()}>
        <header className="drawer-head">
          <div>
            <div className="drawer-title">
              {item ? (kindIcon[item.kind] ?? '') + ' ' + item.title : 'Source'}
            </div>
            {item && (
              <div className="drawer-sub">
                {fmtDate(item.occurredAt)} · {item.chunks.length} passages visible · <LabelBadge label={item.label} />
              </div>
            )}
          </div>
          <button className="icon-btn" onClick={onClose} title="Close (Esc)">✕</button>
        </header>
        <div className="drawer-body">
          {err && <div className="error">{err}</div>}
          {item === undefined && !err && <Spinner />}
          {item === null && <div className="empty">Not visible in this scope.</div>}
          {item?.chunks.map((c) => (
            <div key={c.chunkId} ref={c.chunkId === cited ? target : undefined}
              data-item={src.itemId} data-chunk={c.chunkId} data-title={item.title} data-at={item.occurredAt}
              className={'passage' + (c.chunkId === cited ? ' cited' : '')}>
              <div className="passage-meta">
                #{c.seq} {c.start > 0 && <>· {Math.floor(c.start / 60)}:{String(Math.floor(c.start % 60)).padStart(2, '0')}</>}
                <LabelBadge label={c.label} compact />
              </div>
              {c.text.split('\n').map((line, i) => {
                const m = line.match(/^([^:]{1,60}): (.*)$/);
                return m ? (
                  <p key={i}><span className="who"><Name name={m[1].replace(/^Person \d+ \((.+)\)$/, '$1')} /></span> {m[2]}</p>
                ) : (
                  <p key={i}>{line}</p>
                );
              })}
            </div>
          ))}
        </div>
      </aside>
    </div>
  );
}
