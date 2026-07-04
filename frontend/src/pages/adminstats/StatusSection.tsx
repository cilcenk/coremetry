// Live status-section pieces for /admin/stats (split out of
// AdminStats.tsx — refactor batch item 2, v0.8.269). Kept verbatim
// from the old /status page so the visual rhythm — banner colour,
// dot, chip styling — stays consistent with what operators are
// used to. Banner / ComponentRow / Legend / statusHeadline are the
// public surface; the dot/pill/chip atoms stay private.

import type { ComponentHealth, StatusComponent } from '@/lib/types';

export function statusHeadline(s: ComponentHealth): string {
  switch (s) {
    case 'operational': return 'All systems operational';
    case 'degraded':    return 'Some systems experiencing issues';
    case 'outage':      return 'Major outage in one or more systems';
  }
}

export function Banner({ status, headline }: { status: ComponentHealth; headline: string }) {
  return (
    <div className={`status-banner status-banner-${status}`}>
      <span className={`status-pill status-pill-${status}`}>
        <StatusIcon status={status} />
      </span>
      <span style={{ fontWeight: 700, fontSize: 18 }}>{headline}</span>
    </div>
  );
}

export function ComponentRow({ c }: { c: StatusComponent }) {
  const infoEntries = Object.entries(c.info ?? {});
  return (
    <div className={`status-row status-row-${c.status}`}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, flexWrap: 'wrap' }}>
        <StatusDot status={c.status} />
        <span style={{ fontWeight: 600 }}>{c.name}</span>
        {c.message && (
          <span style={{ color: 'var(--text3)', fontSize: 12, maxWidth: 360,
                         overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                title={c.message}>
            · {c.message}
          </span>
        )}
        {c.ratePerSec !== undefined && (
          <InfoChip k="rate" v={`${c.ratePerSec.toFixed(1)}/s`} highlight />
        )}
        {infoEntries.map(([k, v]) => <InfoChip key={k} k={k} v={v} />)}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0 }}>
        {c.latencyMs !== undefined && c.latencyMs > 0 && (
          <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'monospace' }}
                title="Probe latency">
            {c.latencyMs}ms
          </span>
        )}
        <span className={`status-pill status-pill-${c.status}`}>{labelOf(c.status)}</span>
      </div>
    </div>
  );
}

function InfoChip({ k, v, highlight }: { k: string; v: string; highlight?: boolean }) {
  return (
    <span style={{
      fontSize: 11, fontFamily: 'monospace', padding: '1px 6px', borderRadius: 4,
      background: highlight ? 'rgba(56,139,253,.14)' : 'var(--bg3)',
      color: highlight ? 'var(--accent)' : 'var(--text2)',
      border: highlight ? '1px solid rgba(56,139,253,.30)' : '1px solid var(--border)',
      whiteSpace: 'nowrap',
    }}>
      <span style={{ opacity: .65, marginRight: 4 }}>{k}:</span>{v}
    </span>
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
  switch (status) {
    case 'operational':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M6.7 11.3 3.4 8l1.4-1.4 1.9 1.9 4.5-4.5 1.4 1.4z"/></svg>;
    case 'degraded':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M8 1l7 13H1zm-1 5v4h2V6zm0 5v2h2v-2z"/></svg>;
    case 'outage':
      return <svg width="14" height="14" viewBox="0 0 16 16"><path fill="currentColor" d="M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1zm-1 3h2v5H7zm0 6h2v2H7z"/></svg>;
  }
}

export function Legend() {
  return (
    <div style={{ marginTop: 14, display: 'flex', gap: 16, fontSize: 11, color: 'var(--text3)' }}>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="operational" /> Operational
      </span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="degraded" /> Degraded
      </span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
        <StatusDot status="outage" /> Outage
      </span>
    </div>
  );
}
