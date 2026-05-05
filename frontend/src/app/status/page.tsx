'use client';
import { useEffect, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner } from '@/components/Spinner';
import { api } from '@/lib/api';
import type { SystemStatus, ComponentHealth, StatusComponent } from '@/lib/types';

// Industry-standard status page: top banner showing the worst-of
// component status, component grid below, auto-refresh every 30s.
// Shape modeled after statuspage.io / instatus / better-uptime.
export default function StatusPage() {
  const [data, setData] = useState<SystemStatus | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      api.status()
        .then(d => { if (!cancelled) setData(d); })
        .catch(() => { if (!cancelled) setData(null); });
    };
    load();
    const t = setInterval(load, 30_000);
    return () => { cancelled = true; clearInterval(t); };
  }, []);

  return (
    <>
      <Topbar title="Status" />
      <div id="content">
        {data === undefined && <Spinner />}
        {data === null && (
          <Banner status="outage" headline="Could not reach Coremetry status endpoint" />
        )}
        {data && (
          <>
            <Banner status={data.status} headline={headline(data.status)} />
            <p style={{ color: 'var(--text3)', fontSize: 11, marginTop: 8 }}>
              Last checked {new Date(data.checkedAt).toLocaleString()} · auto-refreshes every 30s
            </p>
            <div className="status-grid">
              {data.components.map(c => <ComponentRow key={c.name} c={c} />)}
            </div>
            <Legend />
          </>
        )}
      </div>
    </>
  );
}

function headline(s: ComponentHealth): string {
  switch (s) {
    case 'operational': return 'All systems operational';
    case 'degraded':    return 'Some systems experiencing issues';
    case 'outage':      return 'Major outage in one or more systems';
  }
}

function Banner({ status, headline }: { status: ComponentHealth; headline: string }) {
  return (
    <div className={`status-banner status-banner-${status}`}>
      <span className={`status-pill status-pill-${status}`}>
        <StatusIcon status={status} />
      </span>
      <span style={{ fontWeight: 700, fontSize: 18 }}>{headline}</span>
    </div>
  );
}

function ComponentRow({ c }: { c: StatusComponent }) {
  return (
    <div className={`status-row status-row-${c.status}`}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
        <StatusDot status={c.status} />
        <span style={{ fontWeight: 600 }}>{c.name}</span>
        {c.message && (
          <span style={{ color: 'var(--text3)', fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            · {c.message}
          </span>
        )}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0 }}>
        {c.latencyMs !== undefined && c.latencyMs > 0 && (
          <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'monospace' }}>
            {c.latencyMs}ms
          </span>
        )}
        <span className={`status-pill status-pill-${c.status}`}>{labelOf(c.status)}</span>
      </div>
    </div>
  );
}

function labelOf(s: ComponentHealth): string {
  switch (s) {
    case 'operational': return 'Operational';
    case 'degraded':    return 'Degraded';
    case 'outage':      return 'Outage';
  }
}

function StatusDot({ status }: { status: ComponentHealth }) {
  return <span className={`status-dot status-dot-${status}`} aria-hidden />;
}

function StatusIcon({ status }: { status: ComponentHealth }) {
  // Tiny inline SVGs — no icon library dep. Filled circle / triangle / square.
  switch (status) {
    case 'operational':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M6.7 11.3 3.4 8l1.4-1.4 1.9 1.9 4.5-4.5 1.4 1.4z"/></svg>;
    case 'degraded':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M8 1l7 13H1zm-1 5v4h2V6zm0 5v2h2v-2z"/></svg>;
    case 'outage':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1zm-1 3h2v5H7zm0 6h2v2H7z"/></svg>;
  }
}

function Legend() {
  return (
    <div style={{ marginTop: 24, display: 'flex', gap: 16, fontSize: 11, color: 'var(--text3)' }}>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="operational" /> Operational — responding normally
      </span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="degraded" /> Degraded — high queue depth or slow response
      </span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="outage" /> Outage — probe failed
      </span>
    </div>
  );
}
