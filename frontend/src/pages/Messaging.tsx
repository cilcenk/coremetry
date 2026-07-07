import { useCallback, useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { DependenciesTable, type DepRow } from '@/components/DependenciesTable';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { encodeDestinationParam, decodeDestinationParam } from './messaging/destinationParam';
import type { MessagingInstance } from '@/lib/types';

// /messaging — top-level queue / topic technologies overview.
// Kafka brokers, RabbitMQ vhosts, IBM MQ queues, NATS subjects,
// SQS / Kinesis streams — anything OTel's messaging.system
// semconv touches.
//
// useQuery + keepPreviousData: revisits land instantly, range
// changes paint the old data underneath while the new payload
// fetches. staleTime aligns with the backend's 30s cache TTL
// so most refetches hit the warm slot.
//
// v0.8.364 (Stage-2 M1):
//   • Produce/min + Consume/min + P50 columns (producer/consumer
//     split off messaging_caller_summary_5m; p50 off the existing
//     TDigest state).
//   • "Compare vs prior" toggle → TrendDelta badges (the endpoints
//     v0.5.404 pattern; opt-in, doubles the backend scan).
//   • ?destination= URL param drives the topic detail drawer
//     (URL-first house rule; replace:true, Esc/✕ clears).
export default function MessagingPage() {
  const [range, setRange] = useUrlRange('1h');
  const [params, setParams] = useSearchParams();
  // Prior-window comparison — off by default (second CH scan);
  // session-local like the endpoints toggle.
  const [compare, setCompare] = useState<boolean>(false);
  // Memoize on range identity — without this, a relative range
  // resolved fresh every render reshuffles the useQuery key
  // and the table refetches on every paint.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['messaging', from, to, compare],
    queryFn: () => api.messaging(from, to, compare ? 'prior' : undefined).then(r => r ?? []),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });

  // v0.8.364 — URL-first topic detail drawer. ?destination=
  // encodes the row's full identity (system|cluster|destination,
  // each field URI-encoded — see destinationParam.ts); a copied
  // link reopens the same drawer. replace:true so drawer churn
  // doesn't pile history entries.
  const destRef = useMemo(
    () => decodeDestinationParam(params.get('destination')),
    [params],
  );
  const setOpenRow = useCallback((row: DepRow | null) => {
    setParams(prev => {
      const next = new URLSearchParams(prev);
      if (row) {
        next.set('destination', encodeDestinationParam({
          system: row.system,
          cluster: row.cluster ?? '(default)',
          destination: row.destination ?? '',
        }));
      } else {
        next.delete('destination');
      }
      return next;
    }, { replace: true });
  }, [setParams]);
  // Same `system|cluster|name` key shape DependenciesTable builds
  // internally — the controlled drawer joins on it.
  const openRowKey = destRef
    ? `${destRef.system}|${destRef.cluster}|${destRef.destination}`
    : null;

  // Esc clears the drawer param (✕ inside the drawer does the same
  // through onOpenRowChange(null)).
  useEffect(() => {
    if (!destRef) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpenRow(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [destRef, setOpenRow]);

  // The backend ships raw window counts (window-independent,
  // compare-safe); the page owns the window so it derives the
  // /min rates here. Prior windows are equal-length by
  // construction, so the same divisor applies.
  const windowMins = Math.max((to - from) / 60e9, 1 / 60);
  const rows = useMemo<DepRow[]>(() => {
    const list = (q.data as MessagingInstance[] | undefined) ?? [];
    return list.map(d => {
      const hasPrior = d.priorSpanCount !== undefined;
      return {
        system: d.system,
        cluster: d.cluster,
        destination: d.destination,
        spanCount: d.spanCount,
        errorCount: d.errorCount,
        errorRate: d.errorRate,
        avgDurationMs: d.avgDurationMs,
        p99DurationMs: d.p99DurationMs,
        p50DurationMs: d.p50DurationMs,
        producePerMin: (d.produceCount ?? 0) / windowMins,
        consumePerMin: (d.consumeCount ?? 0) / windowMins,
        produceCount: d.produceCount,
        consumeCount: d.consumeCount,
        produceErrors: d.produceErrors,
        consumeErrors: d.consumeErrors,
        // Prior fields only when the row had a prior twin —
        // otherwise the delta badges must stay hidden (a zeroed
        // prior would render a bogus NEW badge). omitempty on the
        // backend guarantees priorSpanCount is present iff matched.
        priorSpanCount: d.priorSpanCount,
        priorErrorCount: hasPrior ? (d.priorErrorCount ?? 0) : undefined,
        priorProducePerMin: hasPrior ? (d.priorProduceCount ?? 0) / windowMins : undefined,
        priorConsumePerMin: hasPrior ? (d.priorConsumeCount ?? 0) / windowMins : undefined,
        priorAvgMs: d.priorAvgMs,
        priorP50Ms: d.priorP50Ms,
        priorP99Ms: d.priorP99Ms,
        callers: d.callers ?? [],
      };
    });
  }, [q.data, windowMins]);

  return (
    <>
      <Topbar title="Messaging" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 14, fontSize: 12, color: 'var(--text2)' }}>
          Every queue / topic the platform's services produced to or consumed from
          in the selected window. Derived from spans with a populated
          {' '}<code>messaging.system</code> attribute. Click a row to drill into
          matching traces.
        </div>
        {q.isPending && <Spinner />}
        {q.isError && (
          <Empty icon="⚠" title="Couldn't load messaging overview">
            The messaging query failed. Check ClickHouse connectivity and retry —
            the range selector above re-runs the fetch.
          </Empty>
        )}
        {q.data && (q.data as MessagingInstance[]).length === 0 && (
          <Empty icon="◯" title="No messaging activity in this window">
            No spans with a <code>messaging.system</code> attribute landed in the
            selected range. Widen the time range, or instrument a producer /
            consumer with the OTel messaging semconv to see queues and topics here.
          </Empty>
        )}
        {q.data && (q.data as MessagingInstance[]).length > 0 && (
          <DependenciesTable
            rows={rows}
            kind="queue"
            range={range}
            compare={compare}
            openRowKey={openRowKey}
            onOpenRowChange={setOpenRow}
            extraControls={
              <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}
                title="Compare current window against the immediately-preceding equal-length window. Adds a second backend scan; off by default.">
                <input type="checkbox"
                  checked={compare}
                  onChange={e => setCompare(e.target.checked)} />
                Compare vs prior
              </label>
            } />
        )}
      </div>
    </>
  );
}
