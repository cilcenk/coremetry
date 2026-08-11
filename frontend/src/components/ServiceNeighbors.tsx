import { useEffect, useState } from 'react';
import { seriesColor } from '@/lib/chartFmt';
import { Link } from 'react-router-dom';
import { Spinner } from './Spinner';
import { DisclosureButton } from '@/components/ui';
import { api } from '@/lib/api';
import { fmtNum, type GoDuration } from '@/lib/utils';
import type { NeighborStat, TimeRange } from '@/lib/types';
import { serviceHref } from '@/lib/serviceHref';

// Service-level upstream / downstream summary derived from sampled
// trace topology — no peer.service heuristic. Renders as a compact
// arrow flow:
//
//   [caller-a] [caller-b] ──► <service> ──► [callee-x] [callee-y]
//
// Each chip carries a (×N traces · M calls) badge so the operator
// reads call frequency at a glance. Panel starts collapsed; the
// first open lazy-fetches the data, identical to ServiceStructure.
export function ServiceNeighbors({ service, since = '10m', capped = false, defaultOpen = false, range }: {
  service: string;
  // v0.9.967 — the PAGE window the operator is reading, carried by every
  // neighbour chip.
  //
  // Deliberately NOT `since`. That is this panel's own sampling window (a
  // Go duration, default '10m') and '10m' is not a PRESET_SECONDS key —
  // decodeRange accepts any non-custom string as a preset, so emitting it
  // would silently land the destination on the 86400s fallback: a link
  // claiming ten minutes and rendering twenty-four hours. The honest
  // window for "open this neighbour" is the one the operator is looking
  // at, which is also the only one that can be a brushed `custom:`.
  range?: import('@/lib/types').TimeRange;
  since?: GoDuration;
  // v0.9.257 — true when the caller clamped `since` below the operator's
  // selected range (this panel samples raw spans; see NEIGHBORS_CAP_S in
  // pages/service/Overview.tsx). Surfaced in the header so a narrowed
  // window is stated, not silent.
  capped?: boolean;
  // v0.5.294 — when true, the panel renders expanded on first
  // paint instead of waiting for the operator to click the
  // header. Used from the Service detail Details tab where
  // the operator has already signalled "show me everything"
  // by switching tabs.
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const [data, setData] = useState<{
    upstream?: NeighborStat[];
    downstream?: NeighborStat[];
    sampledFrom: number;
    totalSpans: number;
  } | null | undefined>(undefined);
  // Bumped to force a refetch with refresh=1 (cache bypass).
  const [refreshTick, setRefreshTick] = useState(0);

  useEffect(() => {
    if (!open || !service) return;
    setData(undefined);
    api.serviceNeighbors(service, since, 50, refreshTick > 0)
      .then(setData)
      .catch(() => setData(null));
  }, [open, service, since, refreshTick]);

  const upstream = data?.upstream ?? [];
  const downstream = data?.downstream ?? [];

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, marginBottom: 14,
    }}>
      <DisclosureButton anatomy="section" expanded={open} onClick={() => setOpen(o => !o)}>
        <span style={{ fontSize: 12, color: 'var(--text2)', fontWeight: 600 }}>
          Upstream / downstream for <span style={{ color: 'var(--text)' }}>{service}</span>
        </span>
        {open && data && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            {upstream.length} upstream · {downstream.length} downstream
            {' · '}last {since}{capped && ' (capped)'}
            {' · '}sampled from {data.sampledFrom} trace{data.sampledFrom === 1 ? '' : 's'}
            {' · '}{fmtNum(data.totalSpans)} spans inspected
          </span>
        )}
        <span style={{ flex: 1 }} />
        {open && data !== undefined && (
          <span
            role="button"
            tabIndex={0}
            onClick={e => { e.stopPropagation(); setRefreshTick(t => t + 1); }}
            onKeyDown={e => { if (e.key === 'Enter') { e.stopPropagation(); setRefreshTick(t => t + 1); } }}
            title="Bypass the cached result and recompute now"
            style={{
              fontSize: 11, color: 'var(--accent2)',
              background: 'var(--bg3)', border: '1px solid var(--border)',
              borderRadius: 4, padding: '3px 10px', cursor: 'pointer',
            }}>
            ↻ Refresh
          </span>
        )}
        {!open && (
          <span style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
            click to expand
          </span>
        )}
      </DisclosureButton>

      {open && (
        <div style={{ padding: 14, paddingTop: 10 }}>
          {data === undefined && (
            <div style={{ minHeight: 80, display: 'grid', placeItems: 'center' }}>
              <Spinner />
            </div>
          )}
          {data === null && (
            <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic', padding: '12px 4px' }}>
              Failed to load upstream / downstream.
            </div>
          )}
          {data && upstream.length === 0 && downstream.length === 0 && (
            <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic', padding: '12px 4px' }}>
              No upstream or downstream services found in the sampled traces.
              <code style={{ marginLeft: 6 }}>{service}</code> may be a leaf service in this window.
            </div>
          )}
          {data && (upstream.length > 0 || downstream.length > 0) && (
            <div style={{
              display: 'flex', alignItems: 'center', gap: 16,
              fontSize: 12, flexWrap: 'wrap',
            }}>
              <Column
                range={range}
                label={`Upstream (${upstream.length})`}
                items={upstream}
                emptyText="No upstream callers"
                align="right"
              />
              <Arrow />
              <SelfChip name={service} />
              <Arrow />
              <Column
                range={range}
                label={`Downstream (${downstream.length})`}
                items={downstream}
                emptyText="No downstream callees"
                align="left"
              />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Column({ label, items, emptyText, align, range }: {
  label: string;
  items: NeighborStat[];
  emptyText: string;
  align: 'left' | 'right';
  range?: TimeRange;
}) {
  return (
    <div style={{
      display: 'flex', flexDirection: 'column',
      gap: 4, alignItems: align === 'right' ? 'flex-end' : 'flex-start',
      minWidth: 180,
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase',
        letterSpacing: 0.4, fontWeight: 600,
      }}>{label}</div>
      {items.length === 0 ? (
        <span style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
          {emptyText}
        </span>
      ) : items.map(n => (
        <Chip key={n.service} stat={n} range={range} />
      ))}
    </div>
  );
}

function Chip({ stat, range }: { stat: NeighborStat; range?: TimeRange }) {
  const color = seriesColor(stat.service);
  return (
    <Link to={serviceHref(stat.service, { range })}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 8,
            padding: '4px 10px', borderRadius: 6,
            border: '1px solid var(--border)',
            background: 'var(--bg2)', color: 'var(--text)',
            textDecoration: 'none', fontSize: 12,
          }}
          title={`${stat.service}\n${stat.traceCount} trace${stat.traceCount === 1 ? '' : 's'} · ${stat.spanCount} call${stat.spanCount === 1 ? '' : 's'}`}>
      <span style={{
        width: 8, height: 8, borderRadius: '50%',
        background: color, flexShrink: 0,
      }} />
      <span>{stat.service}</span>
      <span style={{ color: 'var(--text3)', fontSize: 10, fontFamily: 'ui-monospace, monospace' }}>
        ×{stat.traceCount} · {stat.spanCount}
      </span>
    </Link>
  );
}

function SelfChip({ name }: { name: string }) {
  const color = seriesColor(name);
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 8,
      padding: '6px 12px', borderRadius: 6,
      border: `2px solid ${color}`,
      background: 'var(--bg3)', color: 'var(--text)',
      fontSize: 13, fontWeight: 600,
    }}>
      <span style={{
        width: 10, height: 10, borderRadius: '50%',
        background: color, flexShrink: 0,
      }} />
      {name}
    </span>
  );
}

function Arrow() {
  return (
    <span style={{ color: 'var(--text3)', fontSize: 18, fontFamily: 'ui-monospace, monospace' }}>
      →
    </span>
  );
}
