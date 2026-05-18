import { useEffect, useState } from 'react';
import { AggregatedStructure } from './AggregatedStructure';
import { AggregateFlame } from './AggregateFlame';
import { AggregateTopology } from './AggregateTopology';
import { Spinner } from './Spinner';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import type { AggSpanNode } from '@/lib/types';

// Grafana-Drilldown-style multi-trace path-aggregated structure.
// Each unique `(parent_path → service → operation)` triple appears
// once with `×N` for tight loops / fan-outs.
//
// Two views over the same data:
//   • Tree (default) — chronological waterfall-style layout with
//     time bars. Best for "what does this service actually do
//     end-to-end".
//   • Flame — Datadog-signature icicle. Width = total time spent
//     on that path (count × avgMs). Best for "where is my time
//     going" hot-path identification across the sample.
//
// The panel starts collapsed so /service makes zero structure-
// related round-trips until the operator opens it.
type View = 'tree' | 'flame' | 'topology';

// Scope = "what spans does the aggregation walk into".
//   • cross    — every descendant of the focused-service entry
//                points, including downstream service / DB /
//                queue spans. Default; reads as "where does
//                this service's request time end up going".
//   • internal — clip the walk at the service boundary; the
//                aggregation only sees spans owned by this
//                service. Reads as "where does this service
//                spend its OWN time, ignoring how slow the
//                things it calls are".
type Scope = 'cross' | 'internal';

export function ServiceStructure({ service, since = '10m' }: {
  service: string;
  since?: string;
}) {
  const [open, setOpen] = useState(false);
  const [view, setView] = useState<View>('tree');
  const [scope, setScope] = useState<Scope>('cross');
  const [data, setData] = useState<{
    roots?: AggSpanNode[];
    sampledFrom: number;
    totalSpans: number;
  } | null | undefined>(undefined);

  useEffect(() => {
    if (!open || !service) return;
    setData(undefined);
    api.serviceStructure(service, since, 50, scope === 'internal')
      .then(setData)
      .catch(() => setData(null));
  }, [open, service, since, scope]);

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, marginBottom: 14,
    }}>
      <button type="button" onClick={() => setOpen(o => !o)}
        style={{
          display: 'flex', alignItems: 'center', gap: 12,
          width: '100%', padding: 14,
          background: 'transparent', border: 'none', cursor: 'pointer',
          textAlign: 'left', color: 'var(--text)',
          borderBottom: open ? '1px solid var(--border)' : 'none',
        }}>
        <span style={{
          width: 14, color: 'var(--text2)', fontSize: 11,
          fontFamily: 'ui-monospace, monospace',
        }}>{open ? '▼' : '▶'}</span>
        <span style={{ fontSize: 12, color: 'var(--text2)', fontWeight: 600 }}>
          Structure for <span style={{ color: 'var(--text)' }}>{service}</span>
        </span>
        {open && data && (
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>
            aggregated from {data.sampledFrom} trace{data.sampledFrom === 1 ? '' : 's'}
            {' · '}{fmtNum(data.totalSpans)} spans inspected
          </span>
        )}
        <span style={{ flex: 1 }} />
        {!open && (
          <span style={{ fontSize: 11, color: 'var(--text3)', fontStyle: 'italic' }}>
            click to expand
          </span>
        )}
      </button>

      {open && (
        <div style={{ padding: 14, paddingTop: 10 }}>
          {data === undefined && (
            <div style={{ minHeight: 120, display: 'grid', placeItems: 'center' }}>
              <Spinner />
            </div>
          )}
          {(data === null || (data && (!data.roots || data.roots.length === 0))) && (
            <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic', padding: '12px 4px' }}>
              No traces involving <code>{service}</code> in this window.
            </div>
          )}
          {data?.roots && data.roots.length > 0 && (
            <>
              <div style={{
                display: 'flex', gap: 6, marginBottom: 10,
                fontSize: 12, flexWrap: 'wrap', alignItems: 'center',
              }}>
                <ViewTab active={view === 'tree'}  onClick={() => setView('tree')}  label="Tree"
                         hint="Chronological waterfall — what the service does end-to-end" />
                <ViewTab active={view === 'flame'} onClick={() => setView('flame')} label="Flame"
                         hint="Where time is actually spent — width = total time across sampled traces" />
                <ViewTab active={view === 'topology'} onClick={() => setView('topology')} label="Topology"
                         hint="Service-to-service projection of the same sampled traces — who this service actually calls" />
                {/* Scope toggle. Same idea as Datadog APM's
                    "service profile" vs "trace flame" split:
                    do you want to see only this service's
                    internal time, or include the downstream
                    services' contribution too. */}
                <span style={{ flex: 1 }} />
                <span style={{ color: 'var(--text3)', fontSize: 11 }}>Scope</span>
                <ViewTab active={scope === 'cross'}    onClick={() => setScope('cross')}    label="Cross-service"
                         hint="Include downstream service / DB / queue spans called from this service — total round-trip cost" />
                <ViewTab active={scope === 'internal'} onClick={() => setScope('internal')} label="Internal only"
                         hint="Clip the walk at the service boundary — what this service does in its own process" />
              </div>
              {view === 'tree'     && <AggregatedStructure roots={data.roots} />}
              {view === 'flame'    && <AggregateFlame      roots={data.roots} />}
              {view === 'topology' && <AggregateTopology   roots={data.roots} />}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function ViewTab({ active, onClick, label, hint }: {
  active: boolean; onClick: () => void; label: string; hint: string;
}) {
  return (
    <button onClick={onClick} title={hint}
      style={{
        padding: '4px 12px',
        background: active ? 'var(--bg2)' : 'transparent',
        color: active ? 'var(--text)' : 'var(--text2)',
        border: '1px solid var(--border)',
        borderRadius: 6,
        cursor: 'pointer',
        fontWeight: active ? 600 : 400,
      }}>
      {label}
    </button>
  );
}
