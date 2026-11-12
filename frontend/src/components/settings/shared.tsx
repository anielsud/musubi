import type { TestResult } from '../../types';
import { Spinner } from '../common';

// Names shown for sources and formats, the same on every tab.
export const sourceName = (s: string) => (s === 'gateway' ? 'AI Gateway' : s === 'ollama' ? 'Ollama (this Mac)' : s === 'onnx' ? 'ONNX (this Mac)' : s);
// sourceShort is the source chip on a model row and its filter button.
export const sourceShort = (s: string) => (({ gateway: 'Gateway', ollama: 'Ollama', onnx: 'ONNX' } as Record<string, string>)[s] ?? s);
// taskName is what a recorder (ONNX) model does.
export const taskName = (t?: string) =>
  ({ transcription: 'Transcription', live: 'Live text', speaker: 'Speaker voices', vad: 'Speech detection', segmentation: 'Speaker segmentation', ocr: 'Text reading' } as Record<string, string>)[t ?? ''] ?? t ?? '';
export const formatName = (f: string) =>
  ({ anthropic: 'Anthropic', openai: 'OpenAI', ollama: 'Ollama', jev: 'decision', clef: 'decision', onnx: 'ONNX' } as Record<string, string>)[f] ?? f;

export function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return <div className="kv-row"><span className="k">{k}</span><span className="v">{v}</span></div>;
}

export function UsedBy({ list }: { list: string[] | null | undefined }) {
  return <>{list?.length ? list.join(', ') : <span className="dim">nothing</span>}</>;
}

export function TestNote({ r }: { r?: TestResult | 'running' }) {
  if (!r) return null;
  if (r === 'running') return <span className="dim small"><Spinner /> testing…</span>;
  return r.ok ? <span className="ok-text small">✓ {r.models} models listed, {r.ms} ms</span> : <span className="error small">{r.error}</span>;
}

export const usd = (n: number) => (n < 0.01 ? '<$0.01' : `$${n.toFixed(2)}`);
export const price = (n: number) => (n ? `$${n < 0.1 ? n.toFixed(3) : n.toFixed(2)}` : '');
export const tokens = (n: number) => (n ? (n >= 1e6 ? `${Math.round(n / 1e6)}M` : `${Math.round(n / 1000)}K`) : '');
export const bytes = (n: number) => (n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : n >= 1e6 ? `${Math.round(n / 1e6)} MB` : n ? `${Math.round(n / 1e3)} KB` : '');
