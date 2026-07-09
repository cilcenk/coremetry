import { useEffect, useMemo, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Spinner } from '@/components/Spinner';
import { getRecentMetrics, recordMetricPick } from '@/lib/recentMetrics';
import type { MetricInfo } from '@/lib/types';

// GroupedMetricPicker — searchable + faceted catalogue-metric picker.
// Extracted verbatim from MetricQueryEditor.tsx (explore-v2 Phase 2) so the
// Explore builder's metric-source query rows share the same picker. Styling
// rides the existing mqe-* classes.

type MGroup = 'http' | 'rpc' | 'db' | 'messaging' | 'runtime' | 'other';
function metricGroup(name: string): MGroup {
  const n = name.toLowerCase();
  if (n.startsWith('http')) return 'http';
  if (n.startsWith('rpc')) return 'rpc';
  if (n.startsWith('db') || n.startsWith('database') || /(redis|oracle|postgres|mysql|mongo)/.test(n)) return 'db';
  if (n.startsWith('messaging') || /(kafka|rabbit|queue|consumer)/.test(n)) return 'messaging';
  if (/^(jvm|process|go\.|system|runtime|dotnet|nodejs|python)/.test(n)) return 'runtime';
  return 'other';
}
const GROUP_FACETS: { key: 'all' | MGroup; label: string }[] = [
  { key: 'all', label: 'All' }, { key: 'http', label: 'HTTP' }, { key: 'rpc', label: 'RPC' },
  { key: 'runtime', label: 'Runtime' }, { key: 'db', label: 'Database' }, { key: 'messaging', label: 'Messaging' },
];

export function GroupedMetricPicker({ value, unit, onPick }: {
  value: string; unit: string; onPick: (m: MetricInfo) => void;
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const [dq, setDq] = useState('');
  const [facet, setFacet] = useState<'all' | MGroup>('all');
  const ref = useRef<HTMLDivElement>(null);
  // v0.8.5 (scale-audit) — server-side search, NOT an eager full-catalogue
  // load. Only fetch while the dropdown is open, keyed on the debounced
  // query, bounded to 200 server-side; the facet filter applies to the
  // bounded result.
  useEffect(() => {
    const t = window.setTimeout(() => setDq(q.trim()), 150);
    return () => clearTimeout(t);
  }, [q]);
  const catalogQ = useQuery({
    queryKey: ['metric-search', dq],
    queryFn: () => api.metricNamesSearch('', dq || undefined, 200, 0),
    enabled: open,
    staleTime: 60_000,
  });
  const catalog = catalogQ.data?.names ?? [];
  const hasMore = catalogQ.data?.hasMore ?? false;

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  // Facet narrows the server-bounded result; the substring is already
  // applied server-side via dq, kept here only for mid-debounce snappiness.
  const filtered = useMemo(() => {
    const ql = q.trim().toLowerCase();
    return catalog.filter(m =>
      (facet === 'all' || metricGroup(m.name) === facet) &&
      (!ql || m.name.toLowerCase().includes(ql)));
  }, [catalog, q, facet]);

  // v0.8.417 (DE2) — recently-picked ring, Dynatrace Data-Explorer
  // style. Shown only on the "browse" view (empty search, All facet)
  // so it never displaces search results. Read once per open — the
  // list can't change under an open popover except via our own pick,
  // which closes it.
  const recents = useMemo(
    () => (open ? getRecentMetrics() : []),
    [open]);
  const showRecents = recents.length > 0 && !q.trim() && facet === 'all';
  const pick = (m: MetricInfo) => {
    recordMetricPick(m);
    onPick(m);
    setOpen(false);
  };

  return (
    <div className="mqe-picker" ref={ref}>
      <button type="button" className="mqe-pickbtn" onClick={() => setOpen(o => !o)}
        aria-label={value ? `Metric: ${value}` : 'Pick a metric'} aria-expanded={open} title={value || 'Pick a metric'}>
        <span className="mqe-pickname">{value || 'Select metric…'}</span>
        {unit && <span className="mqe-unit">{unit}</span>}
        <span className="mqe-caret">▾</span>
      </button>
      {open && (
        <div className="mqe-pop">
          <input autoFocus className="mqe-search" placeholder="Search metrics…" value={q}
            onChange={e => setQ(e.target.value)} />
          <div className="mqe-facets">
            {GROUP_FACETS.map(f => (
              <button key={f.key} type="button" className={'mqe-facet' + (facet === f.key ? ' on' : '')}
                onClick={() => setFacet(f.key)}>{f.label}</button>
            ))}
          </div>
          <div className="mqe-list">
            {showRecents && (
              <>
                <div className="mqe-sect">Recent</div>
                {recents.map(m => (
                  <button key={'r:' + m.name} type="button"
                    className={'mqe-opt' + (m.name === value ? ' on' : '')}
                    title={m.description || m.name}
                    onClick={() => pick(m)}>
                    <span className="mqe-optcol">
                      <span className="mqe-optname">{m.name}</span>
                      {m.description && <span className="mqe-optdesc">{m.description}</span>}
                    </span>
                    {m.unit && <span className="mqe-unit">{m.unit}</span>}
                  </button>
                ))}
                <div className="mqe-sect">All metrics</div>
              </>
            )}
            {catalogQ.isLoading ? <div className="mqe-hint"><Spinner /></div>
              : filtered.length === 0 ? <div className="mqe-hint">No metrics match.</div>
              : <>
                {filtered.map(m => (
                  <button key={m.name} type="button" className={'mqe-opt' + (m.name === value ? ' on' : '')}
                    title={m.description || m.name}
                    onClick={() => pick(m)}>
                    <span className="mqe-optcol">
                      <span className="mqe-optname">{m.name}</span>
                      {m.description && <span className="mqe-optdesc">{m.description}</span>}
                    </span>
                    {m.unit && <span className="mqe-unit">{m.unit}</span>}
                  </button>
                ))}
                {hasMore && <div className="mqe-hint">More results — refine your search…</div>}
              </>}
          </div>
        </div>
      )}
    </div>
  );
}
