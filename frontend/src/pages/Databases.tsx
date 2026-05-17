import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner } from '@/components/Spinner';
import { DependenciesTable } from '@/components/DependenciesTable';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange, DBInstance } from '@/lib/types';

// /databases — two distinct panels driven by data origin:
//
//   Panel 1: "Called from services" — Dynatrace-style overview
//     of every (db_system, instance) the platform's services
//     have called. Rows derived from spans with a populated
//     db.system attribute. This is the "what depends on what"
//     view for application-side SREs.
//
//   Panel 2: "DB receiver instances" — every database
//     instance discovered via an OpenTelemetry database
//     receiver (oracledb / postgresql / mysql / redis)
//     regardless of whether the application traced it. The
//     DBA-team view: surface every monitored DB even when
//     no app-side SDK is yet in place.
//
// Splitting them prevents the two data origins (span-driven
// vs receiver-driven) from colliding in one list; each
// audience scans the panel that matches their question.
export default function DatabasesPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  // Memoize on range identity — without this, a relative range
  // resolved fresh every render reshuffles the useQuery key
  // and the table refetches on every paint.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['databases', from, to],
    queryFn: () => api.databases(from, to).then(r => r ?? []),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });

  // Split rows by origin. Span-derived rows go to the top
  // panel; receiver-discovered rows go to the bottom. Either
  // panel can be empty — we render the heading + an empty
  // state so the operator sees that we did look.
  const { spanRows, receiverRows } = useMemo(() => {
    const all = (q.data ?? []) as DBInstance[];
    const span: DBInstance[] = [];
    const recv: DBInstance[] = [];
    for (const d of all) {
      if (d.source === 'receiver') recv.push(d);
      else span.push(d);
    }
    return { spanRows: span, receiverRows: recv };
  }, [q.data]);

  const toRow = (d: DBInstance) => ({
    system: d.system,
    instance: d.instance,
    spanCount: d.spanCount,
    errorCount: d.errorCount,
    errorRate: d.errorRate,
    avgDurationMs: d.avgDurationMs,
    p99DurationMs: d.p99DurationMs,
    callers: d.callers ?? [],
    source: d.source,
  });

  return (
    <>
      <Topbar title="Databases" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 8, marginBottom: 12 }}>
          <Link to="/databases/slow-queries" className="sec"
            style={{
              fontSize: 12, padding: '5px 12px', borderRadius: 6,
              border: '1px solid var(--border)', background: 'var(--bg3)',
              color: 'var(--accent2)', textDecoration: 'none',
            }}
            title="Cross-service slow-query catalog — what's burning the most DB time globally">
            🐢 Slow queries →
          </Link>
        </div>
        {q.isPending && <Spinner />}
        {q.isError && (
          <div style={{ color: 'var(--err)', fontSize: 12 }}>
            Failed to load databases overview.
          </div>
        )}
        {q.data && (
          <>
            <SectionHeader
              title={`Called from services (${spanRows.length})`}
              subtitle={`Derived from spans with a populated `}
              code="db.system"
              tail=" attribute. Click a row to drill into matching traces." />
            {spanRows.length === 0 ? (
              <EmptyHint>
                No service-emitted database spans in this window.
                Wire an OTel SDK into one of the application services to see this section populate.
              </EmptyHint>
            ) : (
              <DependenciesTable rows={spanRows.map(toRow)} kind="db" range={range} />
            )}

            <div style={{ height: 24 }} />

            <SectionHeader
              title={`DB receiver instances (${receiverRows.length})`}
              subtitle="OpenTelemetry database-receiver instances — discovered from "
              code="oracledb.* / postgresql.* / mysql.* / redis.*"
              tail=" metric_points. Expand a row to see receiver-specific drill-downs (sessions, wait classes, buffer pool, keyspaces…)." />
            {receiverRows.length === 0 ? (
              <EmptyHint>
                No receiver-detected instances in this window.
                Point an OpenTelemetry database receiver (oracledb / postgresql / mysql / redis)
                at one of your databases and the discovered instance will appear here.
              </EmptyHint>
            ) : (
              <DependenciesTable rows={receiverRows.map(toRow)} kind="db" range={range} />
            )}
          </>
        )}
      </div>
    </>
  );
}

function SectionHeader({ title, subtitle, code, tail }: {
  title: string;
  subtitle: string;
  code: string;
  tail: string;
}) {
  return (
    <>
      <div style={{
        fontSize: 13, fontWeight: 700, marginBottom: 4,
        color: 'var(--text)',
      }}>{title}</div>
      <div style={{ marginBottom: 12, fontSize: 12, color: 'var(--text2)' }}>
        {subtitle}<code>{code}</code>{tail}
      </div>
    </>
  );
}

function EmptyHint({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      padding: 14, borderRadius: 6, marginBottom: 8,
      background: 'var(--bg2)', border: '1px dashed var(--border)',
      fontSize: 12, color: 'var(--text3)',
    }}>{children}</div>
  );
}
