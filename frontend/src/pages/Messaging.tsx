import { useMemo, useState } from 'react';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner } from '@/components/Spinner';
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
          <div style={{ color: 'var(--err)', fontSize: 12 }}>
            Failed to load messaging overview.
          </div>
        )}
        {q.data && (
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
