import { useEffect, useState } from 'react';
import { fmtDate } from '../api';

// Picked is a text selection and where it was made: the statement or
// passage around it (data-statement, data-item, data-chunk, data-title,
// data-at on the element), when there is one.
export interface Picked {
  text: string;
  statement?: number;
  editable?: boolean;
  itemId?: string;
  chunkId?: number;
  title?: string;
  at?: string;
}

// SelectionMenu replaces the context menu on selected text with what can
// be done with it: ask about it, correct its spelling, edit the statement
// it is in, or copy it with its source.
export function SelectionMenu({ onAsk, onCorrect, onSource }: {
  onAsk: (p: Picked) => void;
  onCorrect: (p: Picked) => void;
  onSource: (itemId: string, chunkId: number) => void;
}) {
  const [menu, setMenu] = useState<{ x: number; y: number; p: Picked } | null>(null);

  useEffect(() => {
    const open = (e: MouseEvent) => {
      const sel = window.getSelection();
      const text = sel?.toString().replace(/\s+/g, ' ').trim() ?? '';
      if (!text || text.length > 600 || !sel?.anchorNode) return;
      const from = sel.anchorNode instanceof Element ? sel.anchorNode : sel.anchorNode.parentElement;
      // Not in fields being typed into: their own menu is the right one.
      if (from?.closest('input, textarea')) return;
      e.preventDefault();
      const el = from?.closest('[data-item]') as HTMLElement | null;
      const d = el?.dataset;
      setMenu({
        x: Math.min(e.clientX, window.innerWidth - 230), y: Math.min(e.clientY, window.innerHeight - 170),
        p: {
          text, itemId: d?.item, title: d?.title, at: d?.at,
          chunkId: d?.chunk ? Number(d.chunk) : undefined,
          statement: d?.statement ? Number(d.statement) : undefined,
          editable: d?.editable === '1',
        },
      });
    };
    const close = () => setMenu(null);
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') close(); };
    document.addEventListener('contextmenu', open);
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', key);
    window.addEventListener('blur', close);
    return () => {
      document.removeEventListener('contextmenu', open);
      document.removeEventListener('mousedown', close);
      document.removeEventListener('keydown', key);
      window.removeEventListener('blur', close);
    };
  }, []);

  if (!menu) return null;
  const { p } = menu;
  // A spelling correction is about a word or a short name, not a sentence.
  const short = p.text.split(' ').length <= 3 && p.text.length <= 40;
  const run = (f: () => void) => (e: React.MouseEvent) => { e.preventDefault(); setMenu(null); f(); };
  const copy = () => {
    const src = p.title ? ` (${p.title}${p.at ? ', ' + fmtDate(p.at) : ''})` : '';
    navigator.clipboard?.writeText(`“${p.text}”${src}`).catch(() => {});
  };
  return (
    <div className="sel-menu" style={{ left: menu.x, top: menu.y }} onMouseDown={(e) => e.stopPropagation()}>
      <button onClick={run(() => onAsk(p))}>Ask about this</button>
      {short && <button onClick={run(() => onCorrect(p))} title="Review every use of this word and decide what it should read as">Correct spelling…</button>}
      {p.statement && p.editable && (
        <button onClick={run(() => window.dispatchEvent(new CustomEvent('musubi:edit-statement', { detail: p.statement })))}>Edit this statement</button>
      )}
      {p.itemId && p.chunkId !== undefined && <button onClick={run(() => onSource(p.itemId!, p.chunkId!))}>Open the source</button>}
      <button onClick={run(copy)}>{p.title ? 'Copy with source' : 'Copy'}</button>
    </div>
  );
}
