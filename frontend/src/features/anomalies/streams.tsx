// Anomaly stream sections — shared between the Problems page (which
// historically had them inline) and the standalone /anomalies page.
// Live early-warning signals: log-pattern spikes, trace-op error
// spikes, service-level metric z-score deviations, plus the
// silences strip and the 24h detection history.
//
// These render as compact cards rather than tables because they're
// volatile (a row can disappear within a minute when the underlying
// signal clears) — the operator scans them rather than triages
// them. For triage, see the assignable exception inbox on /problems.

import { useEffect, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Card, Badge, Row } from '@/components/ui';
import { ClusterChips } from '@/components/ClusterChips';
import { CopilotExplain } from '@/components/CopilotExplain';
import {
  useLogPatternAnomalies, useTraceOpAnomalies, useMetricAnomalies,
  useAnomalyEvents, useAnomalySilences,
  useCreateAnomalySilence, useDeleteAnomalySilence,
  useBulkDeleteAnomalySilences,
} from '@/lib/queries';
import { fmtNum, tsLong } from '@/lib/utils';
import type {
  LogPatternAnomaly, TraceOpAnomaly, Problem, AnomalyEvent,
  AnomalySilence,
} from '@/lib/types';

// AnomalyStreams renders all five anomaly sections in their canonical
// order. Each section is independently driven by its own React
// Query hook (mute/unmute mutations invalidate the matching cache).
// Returns a single render block — the consumer wraps it in a Topbar
// + #content if used as a top-level page.
export function AnomalyStreams() {
  const logPatterns = useLogPatternAnomalies().data;
  const traceOps    = useTraceOpAnomalies().data;
  const metrics     = useMetricAnomalies().data;
  const history     = useAnomalyEvents().data;
  const silences    = useAnomalySilences().data;
  const createSilence = useCreateAnomalySilence();
  const deleteSilence = useDeleteAnomalySilence();
  const bulkDeleteSilences = useBulkDeleteAnomalySilences();

  const onMute = async (kind: string, pattern: string, service: string, durationSec: number) => {
    await createSilence.mutateAsync({
      fingerprint: `${kind}|${pattern}|${service}`,
      kind, pattern, service, durationSec,
    });
  };
  const onUnmute = async (id: string) => {
    await deleteSilence.mutateAsync(id);
  };
  const onUnmuteAll = async (ids: string[]) => {
    await bulkDeleteSilences.mutateAsync(ids);
  };

  return (
    <>
      <SilencesSection items={silences} onUnmute={onUnmute} onUnmuteAll={onUnmuteAll} />
      <LogPatternsSection items={logPatterns} onMute={onMute} />
      <TraceOpsSection    items={traceOps}    onMute={onMute} />
      <MetricSection      items={metrics} />
      <HistorySection items={history} />
    </>
  );
}

// AnomalyShell standardises the look across the live sections.
// Renders nothing when count==0 OR items===undefined (still loading)
// — keeps the page from reflowing as feeds load asynchronously.
function AnomalyShell({ title, hint, count, children }: {
  title: string; hint: string; count: number; children: React.ReactNode;
}) {
  if (count === 0) return null;
  return (
    <Card style={{ marginBottom: 16 }}>
      <Row gap={3} style={{ alignItems: 'baseline', marginBottom: 10 }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>{title}</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>{hint}</span>
      </Row>
      {children}
    </Card>
  );
}

function TraceOpsSection({ items, onMute }: {
  items: TraceOpAnomaly[] | undefined;
  onMute: (kind: string, pattern: string, service: string, durationSec: number) => void;
}) {
  if (items === undefined) return null;
  return (
    <AnomalyShell
      title="Trace operation anomalies"
      hint={`${items.length} operation${items.length === 1 ? '' : 's'} with new or doubled error rate`}
      count={items.length}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 10 }}>
        {items.map((a, i) => (
          <Card key={i} density="tight"
                style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
            <Row gap={2} style={{ minWidth: 0 }}>
              {a.kind === 'new_error'
                ? <Badge tone="warning" style={{ fontSize: 10, flexShrink: 0 }}>NEW ERROR</Badge>
                : <Badge tone="danger"  style={{ fontSize: 10, flexShrink: 0 }}>SPIKE ×{a.ratio.toFixed(1)}</Badge>}
              <span style={{
                fontWeight: 600, fontSize: 12,
                flex: 1, minWidth: 0,
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={a.operation || '(unnamed)'}>
                {a.operation || '(unnamed)'}
              </span>
              {a.sampleTraceId && (
                <Link to={`/trace?id=${a.sampleTraceId}`}
                      style={{ fontSize: 11, color: 'var(--accent2)', flexShrink: 0 }}>
                  trace ↗
                </Link>
              )}
              <SnoozeButton onMute={d => onMute('trace_op', a.operation, a.service, d)} />
            </Row>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              <Link to={`/service?name=${encodeURIComponent(a.service)}`}
                    style={{ fontFamily: 'monospace', color: 'var(--text)', textDecoration: 'none' }}>
                {a.service}
              </Link>
              {' · '}{fmtNum(a.currentErrors)} errors now
              {a.baselineErrors > 0 && <> · {fmtNum(a.baselineErrors)} prev</>}
            </div>
          </Card>
        ))}
      </div>
    </AnomalyShell>
  );
}

function SnoozeButton({ onMute }: { onMute: (durationSec: number) => void }) {
  const [open, setOpen] = useState(false);
  const opts: { label: string; sec: number }[] = [
    { label: '1 hour',  sec: 3600 },
    { label: '8 hours', sec: 8 * 3600 },
    { label: '24 hours', sec: 24 * 3600 },
    { label: '7 days', sec: 7 * 24 * 3600 },
  ];
  return (
    <span style={{ position: 'relative' }}>
      <button type="button"
        onClick={() => setOpen(o => !o)}
        title="Mute this anomaly"
        style={{
          fontSize: 10, padding: '2px 8px', borderRadius: 3,
          background: 'var(--bg3)', border: '1px solid var(--border)',
          color: 'var(--text2)', cursor: 'pointer',
        }}>
        Mute
      </button>
      {open && (
        <div style={{
          position: 'absolute', top: '100%', right: 0,
          marginTop: 4, padding: 4, borderRadius: 4, zIndex: 10,
          background: 'var(--bg1)', border: '1px solid var(--border)',
          boxShadow: '0 6px 18px rgba(0,0,0,0.25)',
          display: 'flex', flexDirection: 'column', gap: 2,
        }} onClick={e => e.stopPropagation()}>
          {opts.map(o => (
            <button key={o.sec} type="button"
              onClick={() => { setOpen(false); onMute(o.sec); }}
              style={{
                fontSize: 11, padding: '4px 10px', textAlign: 'left',
                background: 'transparent', border: 'none',
                color: 'var(--text)', cursor: 'pointer', whiteSpace: 'nowrap',
              }}>
              {o.label}
            </button>
          ))}
        </div>
      )}
    </span>
  );
}

function MetricSection({ items }: { items: Problem[] | undefined }) {
  if (items === undefined) return null;
  return (
    <AnomalyShell
      title="Metric anomalies"
      hint={`${items.length} service-level z-score deviation${items.length === 1 ? '' : 's'} open`}
      count={items.length}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))', gap: 10 }}>
        {items.map(p => (
          <Card key={p.id} density="tight">
            <Row gap={2} style={{ marginBottom: 4 }}>
              <Badge tone={p.severity === 'critical' ? 'danger' : 'warning'} style={{ fontSize: 10 }}>
                {p.severity.toUpperCase()}
              </Badge>
              <span style={{ fontWeight: 600, fontSize: 12 }}>{p.metric}</span>
              <span style={{ flex: 1 }} />
              <Link to={`/service?name=${encodeURIComponent(p.service)}`} style={{ fontSize: 11, color: 'var(--accent2)' }}>
                {p.service} ↗
              </Link>
            </Row>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              {p.description || `value ${p.value.toFixed(2)} vs threshold ${p.threshold.toFixed(2)}`}
            </div>
          </Card>
        ))}
      </div>
    </AnomalyShell>
  );
}

function SilencesSection({ items, onUnmute, onUnmuteAll }: {
  items: AnomalySilence[] | undefined;
  onUnmute: (id: string) => void;
  onUnmuteAll: (ids: string[]) => void;
}) {
  if (!items || items.length === 0) return null;
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: '8px 12px', marginTop: 4, marginBottom: 12,
      display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
    }}>
      <span style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 600 }}>
        Muted ({items.length})
      </span>
      {items.length > 1 && (
        <button type="button"
          onClick={() => {
            if (window.confirm(`Unmute all ${items.length} silences?`)) {
              onUnmuteAll(items.map(s => s.id));
            }
          }}
          title="Unmute every active silence"
          style={{
            fontSize: 10, padding: '2px 6px',
            background: 'transparent', border: '1px solid var(--border)',
            borderRadius: 3, color: 'var(--text2)', cursor: 'pointer',
          }}>
          Unmute all
        </button>
      )}
      {items.map(s => {
        const remaining = Math.max(0, s.untilAt / 1e6 - Date.now());
        const remainStr = remaining > 24 * 3600 * 1000
          ? `${Math.floor(remaining / (24 * 3600 * 1000))}d`
          : remaining > 3600 * 1000
            ? `${Math.floor(remaining / (3600 * 1000))}h`
            : `${Math.floor(remaining / 60000)}m`;
        return (
          <span key={s.id} style={{
            display: 'inline-flex', alignItems: 'center', gap: 6,
            padding: '2px 8px', borderRadius: 3, fontSize: 11,
            background: 'var(--bg3)', border: '1px solid var(--border)',
            fontFamily: 'monospace',
          }}>
            <span title={`${s.kind} · ${s.pattern}`}>
              {s.pattern}
              {s.service && <span style={{ color: 'var(--text3)' }}> @ {s.service}</span>}
            </span>
            <span style={{ color: 'var(--text3)' }}>{remainStr} left</span>
            <button type="button" onClick={() => onUnmute(s.id)}
              title="Unmute now"
              style={{
                background: 'transparent', border: 'none', color: 'var(--text3)',
                cursor: 'pointer', padding: 0, fontSize: 11, lineHeight: 1,
              }}>×</button>
          </span>
        );
      })}
    </div>
  );
}

function HistorySection({ items }: { items: AnomalyEvent[] | undefined }) {
  const [searchParams] = useSearchParams();
  // ?event=<id> deep-link target. We scroll + flash the matching
  // row when it appears so inbox navigation lands the operator
  // on the right row visually, not just at the top of the list.
  const highlight = searchParams.get('event') ?? '';
  const rowRefs = useRef<Record<string, HTMLTableRowElement | null>>({});
  useEffect(() => {
    if (!highlight) return;
    const el = rowRefs.current[highlight];
    if (el) {
      el.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }
  }, [highlight, items]);

  if (items === undefined || items.length === 0) return null;
  const active  = items.filter(e => e.status === 'active');
  const cleared = items.filter(e => e.status === 'cleared');
  return (
    <AnomalyShell
      title="Anomaly history (last 24h)"
      hint={`${active.length} active · ${cleared.length} cleared`}
      count={items.length}>
      <div className="table-wrap">
        <table>
          <thead><tr>
            <th style={{ width: 70 }}>Status</th>
            <th>Pattern</th>
            <th>Service</th>
            <th>Kind</th>
            <th className="num">Peak ×</th>
            <th>Started</th>
            <th>Last seen</th>
            <th style={{ width: 70 }}>AI</th>
          </tr></thead>
          <tbody>
            {items.map(e => (
              <tr key={e.id}
                ref={el => { rowRefs.current[e.id] = el; }}
                style={highlight === e.id ? {
                  background: 'rgba(56,139,253,0.10)',
                  outline: '1px solid var(--accent2)',
                } : undefined}>
                <td>
                  <span className={`badge ${e.status === 'active' ? 'b-err' : 'b-ok'}`} style={{ fontSize: 10 }}>
                    {e.status === 'active' ? 'ACTIVE' : 'CLEARED'}
                  </span>
                </td>
                <td style={{ fontWeight: 600 }}>{e.pattern}</td>
                <td>
                  <Link to={`/service?name=${encodeURIComponent(e.service)}`}
                        style={{ fontFamily: 'monospace', fontSize: 11 }}>
                    {e.service || '—'}
                  </Link>
                  <ClusterChips clusters={e.clusters} />
                </td>
                <td style={{ fontSize: 11, color: 'var(--text2)' }}>
                  {e.kind === 'log_pattern' ? 'log'
                    : e.kind === 'elastic_ml' ? 'Elastic ML'
                    : 'trace op'}
                </td>
                <td className="num mono">{e.peakRatio.toFixed(1)}</td>
                <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(e.startedAt)}</td>
                <td className="mono" style={{ fontSize: 11 }}>{tsLong(e.lastSeen)}</td>
                <td>
                  <CopilotExplain kind="anomaly" id={e.id} label="AI" />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </AnomalyShell>
  );
}

function LogPatternsSection({ items, onMute }: {
  items: LogPatternAnomaly[] | undefined;
  onMute: (kind: string, pattern: string, service: string, durationSec: number) => void;
}) {
  if (items === undefined) return null;
  if (items.length === 0) return null;
  return (
    <Card style={{ marginBottom: 16 }}>
      <Row gap={3} style={{ alignItems: 'baseline', marginBottom: 10 }}>
        <span style={{ fontSize: 13, fontWeight: 700 }}>
          Log-pattern anomalies
        </span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {items.length} pattern{items.length === 1 ? '' : 's'} changed in the last 5 min
        </span>
      </Row>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 10 }}>
        {items.map((a, i) => (
          <Card key={i} density="tight"
                style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
            <Row gap={2} style={{ minWidth: 0 }}>
              {a.kind === 'new'
                ? <Badge tone="warning" style={{ fontSize: 10, flexShrink: 0 }}>NEW</Badge>
                : <Badge tone="danger"  style={{ fontSize: 10, flexShrink: 0 }}>SPIKE ×{a.ratio.toFixed(1)}</Badge>}
              <span style={{
                fontWeight: 600, fontSize: 12,
                flex: 1, minWidth: 0,
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={a.pattern}>
                {a.pattern}
              </span>
              <Link to={`/logs?service=${encodeURIComponent(a.service)}`}
                    style={{ fontSize: 11, color: 'var(--accent2)', flexShrink: 0 }}>
                logs ↗
              </Link>
              <SnoozeButton onMute={d => onMute('log_pattern', a.pattern, a.service, d)} />
            </Row>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              <span style={{ fontFamily: 'monospace' }}>{a.service || 'unknown'}</span>
              {' · '}
              {fmtNum(a.currentCount)} now
              {a.baselineCount > 0 && <> · {fmtNum(a.baselineCount)} prev</>}
            </div>
            {a.sample && (
              <div style={{
                fontSize: 11, color: 'var(--text3)',
                fontFamily: 'monospace',
                whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
              }} title={a.sample}>
                {a.sample}
              </div>
            )}
          </Card>
        ))}
      </div>
    </Card>
  );
}
