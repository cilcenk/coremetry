import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { TimeRange } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';

// LogsFacetSidebar — Kibana-Discover-style facet panel. Renders
// top-N (value, count) buckets per dimension scoped to the
// currently-applied log filter. Click a bucket = the parent's
// applyFacet callback decides how it folds into the filter
// (typically: appends a KQL clause to the search box).
//
// Backend: /api/logs/facets (v0.5.226). One round-trip returns
// all four dimensions; we drive the chip list with stable
// per-section keys so a refresh doesn't shuffle the rows.

type FacetBucket = { value: string; count: number };
type Facets = Record<string, FacetBucket[]>;

type FacetField = 'service' | 'severity' | 'pod' | 'container' | 'cluster';

const FACET_TITLES: Record<FacetField, string> = {
  service:   'Service',
  severity:  'Severity',
  pod:       'Pod',
  container: 'Container',
  cluster:   'Cluster',
};

const FACET_QUERY_FIELD: Record<FacetField, string> = {
  service:   'service.name',
  severity:  'level',
  // Pod / container / cluster use the operator's actual shipper
  // field names — matches the LogTable display chain.
  // expandShorthand on the backend also accepts these as
  // shorthand keys (pod:, container:, cluster:) and rewrites to
  // an OR group across the candidate fields so installs using
  // different shapes still match.
  pod:       'kubernetes.pod_name',
  container: 'kubernetes.container_name',
  cluster:   'openshift.labels.cluster',
};

export function LogsFacetSidebar({
  range, filter, onApplyValue,
}: {
  range: TimeRange;
  filter: {
    service: string;
    search: string;
    severity: number;
    traceId: string;
    spanId: string;
  };
  // applyValue(field, value) — caller folds the bucket into its
  // filter state (service picker for "service", or appending
  // "key:value" to the search box for the rest).
  onApplyValue: (field: FacetField, value: string) => void;
}) {
  const [data, setData] = useState<Facets | null | undefined>(undefined);

  useEffect(() => {
    setData(undefined);
    const { from, to } = filter.traceId
      ? { from: undefined as number | undefined, to: undefined as number | undefined }
      : timeRangeToNs(range);
    api.logsFacets({
      from, to,
      service: filter.service || undefined,
      search: filter.search || undefined,
      severity: filter.severity > 0 ? filter.severity : undefined,
      traceId: filter.traceId || undefined,
      spanId:  filter.spanId  || undefined,
      topN: 10,
    } as unknown as Parameters<typeof api.logsFacets>[0])
      .then(d => setData((d ?? {}) as Facets))
      .catch(() => setData(null));
    // Reasonable dep set — refetch when ANY filter or range
    // changes so the counts always match what the table shows.
  }, [range, filter.service, filter.search, filter.severity, filter.traceId, filter.spanId]);

  return (
    <aside style={{
      width: 240, flexShrink: 0,
      borderRight: '1px solid var(--border)',
      paddingRight: 12, marginRight: 16,
      fontSize: 12,
    }}>
      <div style={{
        fontSize: 11, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4,
        marginBottom: 10,
      }}>
        Narrow by
      </div>
      {data === undefined && (
        <div style={{ color: 'var(--text3)', fontStyle: 'italic' }}>Loading…</div>
      )}
      {data === null && (
        <div style={{ color: 'var(--err)', fontSize: 11 }}>
          Facets unavailable
        </div>
      )}
      {data && (Object.keys(FACET_TITLES) as FacetField[]).map(f => (
        <FacetGroup key={f}
          title={FACET_TITLES[f]}
          buckets={data[f] ?? []}
          activeValue={getActiveValue(f, filter)}
          onClick={v => onApplyValue(f, v)} />
      ))}
    </aside>
  );
}

function FacetGroup({ title, buckets, activeValue, onClick }: {
  title: string;
  buckets: FacetBucket[];
  activeValue?: string;
  onClick: (v: string) => void;
}) {
  if (buckets.length === 0) {
    return (
      <div style={{ marginBottom: 14 }}>
        <div style={{ fontWeight: 600, color: 'var(--text2)', marginBottom: 4 }}>{title}</div>
        <div style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
          no values
        </div>
      </div>
    );
  }
  const max = buckets[0]?.count ?? 1;
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{ fontWeight: 600, color: 'var(--text2)', marginBottom: 4 }}>{title}</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        {buckets.map(b => {
          const isActive = activeValue === b.value;
          const ratio = max > 0 ? b.count / max : 0;
          return (
            <button key={b.value} type="button"
              onClick={() => onClick(b.value)}
              title={isActive ? `Click to clear ${title.toLowerCase()} = ${b.value}` : `Filter ${title.toLowerCase()} = ${b.value}`}
              style={{
                all: 'unset', cursor: 'pointer',
                position: 'relative',
                display: 'flex', alignItems: 'center', gap: 6,
                padding: '3px 6px', borderRadius: 4,
                background: isActive ? 'rgba(56,139,253,0.18)' : 'transparent',
                color: isActive ? 'var(--accent2)' : 'var(--text)',
                fontWeight: isActive ? 600 : 400,
                fontSize: 11,
                overflow: 'hidden',
              }}>
              {/* Inline bar: a coloured stripe behind the row scaled
                  to count/max. Subtle so the value+count stay readable. */}
              <span aria-hidden="true" style={{
                position: 'absolute', left: 0, top: 0, bottom: 0,
                width: `${Math.max(2, ratio * 100)}%`,
                background: isActive ? 'rgba(56,139,253,0.10)' : 'rgba(148,163,184,0.10)',
                pointerEvents: 'none',
                zIndex: 0,
              }} />
              <span style={{
                position: 'relative', zIndex: 1,
                flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              }}>
                {b.value}
              </span>
              <span style={{
                position: 'relative', zIndex: 1,
                color: 'var(--text3)', fontSize: 10,
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              }}>
                {fmtNum(b.count)}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

// getActiveValue — does the current filter already pin a value for
// this facet? If yes, the matching chip renders highlighted +
// clicking it clears the filter rather than re-applying. We detect
// via simple substring on the search string (for KQL-style
// `key:value` clauses) plus exact match on the service field.
function getActiveValue(field: FacetField, filter: {
  service: string; search: string;
}): string | undefined {
  if (field === 'service' && filter.service) return filter.service;
  const key = FACET_QUERY_FIELD[field];
  // Match `key:value` or `key:"value"` somewhere in the search box.
  const re = new RegExp(escapeRe(key) + ':"?([^"\\s]+)"?');
  const m = filter.search.match(re);
  return m ? m[1] : undefined;
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

export { FACET_QUERY_FIELD };
export type { FacetField };
