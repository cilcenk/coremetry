import { useMemo, useState } from 'react';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { DependenciesTable } from '@/components/DependenciesTable';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange, MessagingInstance } from '@/lib/types';

// /messaging — top-level queue / topic technologies overview.
// Kafka brokers, RabbitMQ vhosts, IBM MQ queues, NATS subjects,
// SQS / Kinesis streams — anything OTel's messaging.system
// semconv touches.
//
// useQuery + keepPreviousData: revisits land instantly, range
// changes paint the old data underneath while the new payload
// fetches. staleTime aligns with the backend's 30s cache TTL
// so most refetches hit the warm slot.
export default function MessagingPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  // Memoize on range identity — without this, a relative range
  // resolved fresh every render reshuffles the useQuery key
  // and the table refetches on every paint.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['messaging', from, to],
    queryFn: () => api.messaging(from, to).then(r => r ?? []),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });

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
            rows={(q.data as MessagingInstance[]).map(d => ({
              system: d.system,
              cluster: d.cluster,
              destination: d.destination,
              spanCount: d.spanCount,
              errorCount: d.errorCount,
              errorRate: d.errorRate,
              avgDurationMs: d.avgDurationMs,
              p99DurationMs: d.p99DurationMs,
              callers: d.callers ?? [],
            }))}
            kind="queue"
            range={range} />
        )}
      </div>
    </>
  );
}
