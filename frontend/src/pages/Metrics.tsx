import { useMemo, useState } from 'react';
import { Navigate, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { MetricQueryEditor } from '@/components/viz/MetricQueryEditor';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { useDebouncedValue } from '@/lib/perf/useDebouncedValue';
import { api } from '@/lib/api';
import { decodeRange } from '@/lib/urlState';
import { storedRangeString } from '@/lib/useUrlRange';
import { classifyMetric } from '@/lib/metricTemplates';
import { metricCatalogueHref } from './explore/urlCodec';
import type { DataTableColumn } from '@/lib/dataTable';
import type { MetricInfo, TimeRange } from '@/lib/types';

// Metrics — v0.8.x Phase-5 collapse. /metrics is now a CATALOGUE: a
// server-side-searchable, sortable index of every metric name (name / type /
// unit / description). Picking a row opens it in Explore as a real builder
// query A (source=metric) via metricCatalogueHref — Explore is the one place a
// metric is actually charted, so the old in-page builder + dual-mode explorer
// are gone. Two escape valves remain:
//   • ?editor=1 — the full MetricQueryEditor as a page (power users).
//   • legacy /metrics?metric=&service=&agg= bookmarks / saved views collapse
//     to the canonical /explore?q= seed (Navigate replace).

type MGroup = 'http' | 'rpc' | 'runtime' | 'db' | 'messaging' | 'other';
const GROUPS: { key: MGroup | 'all'; label: string }[] = [
  { key: 'all', label: 'All' }, { key: 'http', label: 'HTTP' }, { key: 'rpc', label: 'RPC' },
  { key: 'runtime', label: 'Runtime' }, { key: 'db', label: 'Database' }, { key: 'messaging', label: 'Messaging' },
];

// Classify a metric into a facet group by its OTel name prefix (moved verbatim
// from the retired pages/metrics/MetricsExplorer).
function metricGroup(name: string): MGroup {
  const n = name.toLowerCase();
  if (n.startsWith('http')) return 'http';
  if (n.startsWith('rpc')) return 'rpc';
  if (n.startsWith('db') || n.startsWith('database') || /(redis|oracle|postgres|mysql|mongo)/.test(n)) return 'db';
  if (n.startsWith('messaging') || /(kafka|rabbit|queue|consumer)/.test(n)) return 'messaging';
  if (/^(jvm|process|go\.|system|runtime|dotnet|nodejs|python)/.test(n)) return 'runtime';
  return 'other';
}

const CATALOG_COLS: DataTableColumn<MetricInfo>[] = [
  { id: 'name', label: 'Metric',      sortValue: m => m.name,             naturalDir: 'asc', width: 360 },
  { id: 'type', label: 'Type',        sortValue: m => m.type,             naturalDir: 'asc', width: 120 },
  { id: 'unit', label: 'Unit',        sortValue: m => m.unit || '',       naturalDir: 'asc', width: 100 },
  { id: 'desc', label: 'Description', sortValue: m => m.description || '', naturalDir: 'asc', width: 460 },
];

export default function MetricsPage() {
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();

  const editor = searchParams.get('editor') === '1';
  const legacyMetric = searchParams.get('metric') ?? '';

  const [range, setRange] = useState<TimeRange>(() =>
    decodeRange(searchParams.get('range') ?? storedRangeString(), { preset: '30m' }));
  const [search, setSearch] = useState('');
  const [facet, setFacet] = useState<MGroup | 'all'>('all');

  // Legacy deep-link → canonical Explore seed. Computed pre-render; the
  // Navigate return sits AFTER every hook so rules-of-hooks holds.
  const redirectTo = !editor && legacyMetric
    ? metricCatalogueHref(legacyMetric, {
        service: searchParams.get('service') || undefined,
        agg: searchParams.get('agg') || undefined,
      }) + (searchParams.get('range') ? `&range=${encodeURIComponent(searchParams.get('range')!)}` : '')
    : null;

  // SERVER-SIDE search (scale-audit #10) — debounced, bounded to 200 rows. The
  // eager api.metricNames('') full-catalogue load is fatal at 10k+ names.
  const dq = useDebouncedValue(search.trim(), 250);
  const catalogQ = useQuery({
    queryKey: ['metric-catalog', dq],
    queryFn: () => api.metricNamesSearch('', dq || undefined, 200, 0),
    staleTime: 60_000,
    enabled: !redirectTo && !editor,
  });
  const catalog = useMemo<MetricInfo[]>(() => catalogQ.data?.names ?? [], [catalogQ.data]);
  const hasMore = catalogQ.data?.hasMore ?? false;
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const m of catalog) { const g = metricGroup(m.name); c[g] = (c[g] ?? 0) + 1; }
    return c;
  }, [catalog]);
  const filtered = useMemo(
    () => catalog.filter(m => facet === 'all' || metricGroup(m.name) === facet),
    [catalog, facet]);

  const dt = useDataTable<MetricInfo>({
    storageKey: 'metric-catalog',
    columns: CATALOG_COLS,
    rows: filtered,
    initialSort: { id: 'name', dir: 'asc' },
  });

  if (redirectTo) return <Navigate replace to={redirectTo} />;

  // classifyMetric picks the default agg (e.g. p99 for a histogram) so the
  // chart lands right; metricCatalogueHref encodes it into the ?q= seed.
  const openMetric = (m: MetricInfo) =>
    navigate(metricCatalogueHref(m.name, { agg: classifyMetric(m)?.agg }));

  return (
    <>
      <Topbar title="Metrics" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
          <div style={{ fontSize: 12, color: 'var(--text2)' }}>
            Metric catalogue — pick one to open it in Explore.
          </div>
          <div style={{ flex: 1 }} />
          <Button variant={editor ? 'primary' : 'secondary'} size="sm"
            onClick={() => navigate(editor ? '/metrics' : '/metrics?editor=1')}>
            {editor ? '← Catalogue' : 'Advanced query editor →'}
          </Button>
        </div>

        {editor ? (
          <MetricQueryEditor range={range} />
        ) : (
          <>
            <div className="controls" style={{ marginBottom: 10 }}>
              <input className="field" placeholder="Search metrics…" value={search}
                onChange={e => setSearch(e.target.value)} style={{ width: 280 }} autoFocus />
              <div className="ov-logbar" style={{ gap: 4, marginBottom: 0 }}>
                {GROUPS.map(g => (
                  <span key={g.key}
                    className={'ov-facet' + (facet === g.key ? ' on' : '')}
                    onClick={() => setFacet(g.key)}>
                    {g.label}{g.key !== 'all' && <span className="n">{counts[g.key] ?? 0}</span>}
                  </span>
                ))}
              </div>
            </div>

            {catalogQ.isLoading ? <Spinner />
              : filtered.length === 0 ? (
                <Empty icon="∿" title="No metrics match">
                  Try a different search, or check <code>OTEL_EXPORTER_OTLP_ENDPOINT</code> apps are pushing.
                </Empty>
              ) : (
                <>
                  <div className="table-wrap">
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={dt} />
                      <DataTableHead dt={dt} />
                      <tbody>
                        {dt.sortedRows.map((m, i) => (
                          <tr key={m.name} {...dt.rowProps(i)}
                            onClick={() => openMetric(m)}
                            style={{ cursor: 'pointer' }}
                            title={`Open ${m.name} in Explore`}>
                            <td className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                              {m.name}
                            </td>
                            <td>{m.type}</td>
                            <td className="mono">{m.unit || '·'}</td>
                            <td style={{ color: 'var(--text2)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                              title={m.description}>
                              {m.description || '—'}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {hasMore && (
                    <div style={{ padding: '8px 4px', color: 'var(--text3)', fontSize: 11 }}>
                      More results — refine your search…
                    </div>
                  )}
                </>
              )}
          </>
        )}
      </div>
    </>
  );
}
