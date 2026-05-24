import { useEffect, useState, useMemo } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';

// LogFacetsPanel — v0.5.400. Honeycomb / Discover-style cardinality-
// aware side panel for /logs. Renders the top-N (value, count)
// buckets per facet dimension (service, severity, pod, container,
// cluster, namespace, deployment). Click a value → adds it as a
// filter clause; the same toggleSearchClause path the row's ⊕
// button uses, so the filter UI stays a single source of truth.
//
// Why this beats "click value in row":
//   - You see the WHOLE distribution before deciding (top error
//     services first, not just whatever row you happened to scroll
//     past).
//   - "What pods are emitting these logs?" — answered without
//     opening a row.
//   - Cardinality cues: a facet showing 50+ distinct values with
//     long-tail counts is itself a signal (mis-set service.name,
//     ID leaking into a tag).
//
// Why this isn't the OLD facets sidebar (removed v0.5.235):
//   - It's on-demand collapsible. Default-collapsed sections don't
//     fetch their data, so the page TTFI isn't burdened.
//   - It uses the same /api/logs/facets endpoint that's been live
//     since v0.5.226 — backend's been ready, frontend just lost
//     the surface in the cleanup. We're not re-introducing the
//     scale problem (long-tail truncation); we're surfacing the
//     facets where the cardinality is meaningfully bounded
//     (severity, service, pod) and letting the operator opt-in
//     to the rest.

type Filter = {
  service: string; search: string; severity: number;
  traceId: string; spanId: string;
};

const FACET_LABELS: Record<string, string> = {
  service:    'Service',
  severity:   'Severity',
  pod:        'Pod',
  container:  'Container',
  cluster:    'Cluster',
  namespace:  'Namespace',
  deployment: 'Deployment',
};
// Default-expanded sections — high-signal, low-cardinality.
// The rest collapse so the operator opens them on demand.
const DEFAULT_OPEN = new Set(['service', 'severity']);

// Filter key mapping — what UI label the row's ⊕ button uses for
// each facet. Pod / container / cluster live in resource attrs;
// the facets backend returns the canonical OTel key per dimension,
// but the operator's filter chip just needs the dimension name.
const FILTER_KEY: Record<string, string> = {
  service:    'service.name',
  severity:   'severity_text',
  pod:        'k8s.pod.name',
  container:  'k8s.container.name',
  cluster:    'k8s.cluster.name',
  namespace:  'k8s.namespace.name',
  deployment: 'k8s.deployment.name',
};

export function LogFacetsPanel({
  filter, range, onPick,
}: {
  filter: Filter;
  range: { from?: number; to?: number };
  onPick: (key: string, value: string) => void;
}) {
  const [data, setData] = useState<Record<string, Array<{ value: string; count: number }>> | null | undefined>(undefined);
  const [open, setOpen] = useState<Set<string>>(DEFAULT_OPEN);
  const [collapsed, setCollapsed] = useState(false);

  useEffect(() => {
    if (collapsed) return;
    let cancelled = false;
    setData(undefined);
    api.logsFacets({
      from: range.from, to: range.to,
      service: filter.service || undefined,
      search:  filter.search  || undefined,
      severity: filter.severity > 0 ? filter.severity : undefined,
      traceId: filter.traceId || undefined,
      spanId:  filter.spanId  || undefined,
      topN: 10,
    })
      .then(d => { if (!cancelled) setData(d ?? {}); })
      .catch(() => { if (!cancelled) setData(null); });
    return () => { cancelled = true; };
  }, [
    range.from, range.to,
    filter.service, filter.search, filter.severity,
    filter.traceId, filter.spanId, collapsed,
  ]);

  const sections = useMemo(() => {
    const all = Object.keys(FACET_LABELS);
    return all.filter(k => {
      const rows = data?.[k];
      return rows && rows.length > 0;
    });
  }, [data]);

  if (collapsed) {
    return (
      <div style={{
        width: 28, flexShrink: 0,
        borderLeft: '1px solid var(--border)',
        background: 'var(--bg1)',
      }}>
        <button type="button" onClick={() => setCollapsed(false)}
          title="Show field summary"
          style={{
            all: 'unset', cursor: 'pointer',
            width: '100%', padding: '8px 6px',
            color: 'var(--text2)', fontSize: 11,
            writingMode: 'vertical-rl', transform: 'rotate(180deg)',
          }}>
          ▶ Field summary
        </button>
      </div>
    );
  }

  return (
    <div style={{
      width: 260, flexShrink: 0,
      borderLeft: '1px solid var(--border)',
      background: 'var(--bg1)',
      maxHeight: '80vh', overflowY: 'auto',
    }}>
      <div style={{
        position: 'sticky', top: 0, zIndex: 1,
        padding: '8px 12px', background: 'var(--bg1)',
        borderBottom: '1px solid var(--border)',
        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
      }}>
        <span style={{
          fontSize: 11, fontWeight: 700, color: 'var(--text2)',
          textTransform: 'uppercase', letterSpacing: 0.4,
        }}>Field summary</span>
        <button type="button" onClick={() => setCollapsed(true)}
          title="Hide" style={{
            all: 'unset', cursor: 'pointer', fontSize: 11,
            color: 'var(--text3)', padding: '0 4px',
          }}>×</button>
      </div>
      {data === undefined && (
        <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
          Loading…
        </div>
      )}
      {data === null && (
        <div style={{ padding: 12, fontSize: 11, color: 'var(--err)' }}>
          Failed to load facets.
        </div>
      )}
      {data && sections.length === 0 && (
        <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
          No facet data in this window.
        </div>
      )}
      {data && sections.map(k => {
        const rows = data[k] ?? [];
        const isOpen = open.has(k);
        const totalCount = rows.reduce((s, r) => s + r.count, 0);
        return (
          <div key={k} style={{ borderBottom: '1px solid var(--border)' }}>
            <button type="button"
              onClick={() => setOpen(prev => {
                const next = new Set(prev);
                next.has(k) ? next.delete(k) : next.add(k);
                return next;
              })}
              style={{
                all: 'unset', cursor: 'pointer',
                display: 'flex', alignItems: 'center', gap: 6,
                width: '100%', padding: '6px 12px',
                fontSize: 11, color: 'var(--text2)',
              }}>
              <span style={{ fontSize: 9 }}>{isOpen ? '▼' : '▶'}</span>
              <span style={{ fontWeight: 600 }}>{FACET_LABELS[k]}</span>
              <span style={{ color: 'var(--text3)', marginLeft: 'auto', fontFamily: 'ui-monospace, monospace' }}>
                {rows.length}
              </span>
            </button>
            {isOpen && (
              <div style={{ padding: '2px 6px 8px' }}>
                {rows.map(row => {
                  const pct = totalCount > 0 ? (row.count / totalCount) * 100 : 0;
                  return (
                    <button key={row.value}
                      type="button"
                      onClick={() => onPick(FILTER_KEY[k] || k, row.value)}
                      title={`Click to filter: ${FILTER_KEY[k] || k}:${row.value}`}
                      style={{
                        all: 'unset', cursor: 'pointer',
                        display: 'block', width: '100%',
                        padding: '3px 8px', position: 'relative',
                        fontSize: 11, fontFamily: 'ui-monospace, monospace',
                        borderRadius: 3,
                      }}>
                      {/* Proportion bar background */}
                      <div style={{
                        position: 'absolute', top: 0, bottom: 0, left: 0,
                        width: `${pct}%`,
                        background: 'rgba(56,139,253,0.08)',
                        borderRadius: 3,
                      }} />
                      <span style={{
                        position: 'relative',
                        display: 'flex', alignItems: 'baseline', gap: 6,
                      }}>
                        <span style={{
                          flex: 1, overflow: 'hidden',
                          textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                          color: 'var(--text)',
                        }} title={row.value}>
                          {row.value || '(empty)'}
                        </span>
                        <span style={{ color: 'var(--text3)', fontSize: 10 }}>
                          {fmtNum(row.count)}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
