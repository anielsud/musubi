import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { api, on } from '../api';

// Names anywhere in the app open the person they mean: PeopleProvider
// loads the names that mean exactly one person (owner scope), and <Name>
// renders a name as a link when it is one of them.

type People = { match: (name: string) => string | undefined; open: (id: string) => void };

const Ctx = createContext<People>({ match: () => undefined, open: () => {} });

// norm folds a name the way the backend does (engine.normName), and reads
// Tomoe's "Person 3 (Kai Moreno)" as Kai Moreno.
function norm(s: string): string {
  let t = s.trim();
  const m = /^(Person|Speaker) \d+ \((.+)\)$/.exec(t);
  if (m) t = m[2];
  t = t.replace(/\s*\([^)]*\)/g, '');
  return t.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter(Boolean).join(' ');
}

export function PeopleProvider({ scope, onOpen, children }: { scope: string; onOpen: (id: string) => void; children: ReactNode }) {
  const [names, setNames] = useState<Record<string, string>>({});
  useEffect(() => {
    const load = () => api.peopleNames(scope).then((m) => setNames(m ?? {}), () => setNames({}));
    load();
    return on('update:done', load);
  }, [scope]);
  const match = (name: string) => names[norm(name)] ?? names[name.trim().toLowerCase()];
  return <Ctx.Provider value={{ match, open: onOpen }}>{children}</Ctx.Provider>;
}

export function usePeople() { return useContext(Ctx); }

// Name is one person's name: a link to their page when known.
export function Name({ name }: { name: string }) {
  const { match, open } = usePeople();
  const id = match(name);
  if (!id) return <>{name}</>;
  // A span, not a button: names sit inside clickable rows too.
  return <span className="person-link" role="link" tabIndex={0} title="Open their page"
    onClick={(e) => { e.stopPropagation(); open(id); }}
    onKeyDown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); open(id); } }}>{name}</span>;
}

// Names is a list of names, comma-separated, each a link when known.
export function Names({ names, sep = ', ' }: { names: string[]; sep?: string }) {
  return <>{names.map((n, i) => <span key={n + i}>{i > 0 && sep}<Name name={n} /></span>)}</>;
}
