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
import { Check, ChevronRight, ChevronDown, ArrowDownToLine } from 'lucide-react';
import { Card, Badge, Row, Button } from '@/components/ui';
import { ClusterChips } from '@/components/ClusterChips';
import { CopilotExplain } from '@/components/CopilotExplain';
import { useAuth } from '@/components/AuthProvider';
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
  const { user } = useAuth();
  // Editor+ can mute/unmute; viewers see the silence chips and
  // anomaly cards read-only (invariant #7). The backend enforces
  // the same gate on POST/DELETE /api/anomalies/silences — hiding
  // the buttons here keeps viewers from clicking into a 403.
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
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
      <SilencesSection items={silences} onUnmute={onUnmute} onUnmuteAll={onUnmuteAll} canEdit={canEdit} />
      <LogPatternsSection items={logPatterns} onMute={onMute} canEdit={canEdit} />
      <TraceOpsSection    items={traceOps}    onMute={onMute} canEdit={canEdit} />
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

function TraceOpsSection({ items, onMute, canEdit }: {
  items: TraceOpAnomaly[] | undefined;
  onMute: (kind: string, pattern: string, service: string, durationSec: number) => void;
  canEdit: boolean;
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
              {canEdit && <SnoozeButton onMute={d => onMute('trace_op', a.operation, a.service, d)} />}
            </Row>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              <Link to={`/service?name=${encodeURIComponent(a.service)}`}
                    className="mono" style={{ color: 'var(--text)', textDecoration: 'none' }}>
                {a.service}
              </Link>
              {' · '}<span className="mono">{fmtNum(a.currentErrors)}</span> errors now
              {a.baselineErrors > 0 && <> · <span className="mono">{fmtNum(a.baselineErrors)}</span> prev</>}
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
    <span style={{ position: 'relative', flexShrink: 0 }}>
      <Button variant="ghost" size="sm"
        onClick={() => setOpen(o => !o)}
        title="Mute this anomaly">
        Mute
      </Button>
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
              {p.description || <>value <span className="mono">{p.value.toFixed(2)}</span> vs threshold <span className="mono">{p.threshold.toFixed(2)}</span></>}
            </div>
          </Card>
        ))}
      </div>
    </AnomalyShell>
  );
}

function SilencesSection({ items, onUnmute, onUnmuteAll, canEdit }: {
  items: AnomalySilence[] | undefined;
  onUnmute: (id: string) => void;
  onUnmuteAll: (ids: string[]) => void;
  canEdit: boolean;
}) {
  if (!items || items.length === 0) return null;
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 8, padding: '10px 12px', marginTop: 4, marginBottom: 16,
      display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
    }}>
      <span style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 700,
                     textTransform: 'uppercase', letterSpacing: '.06em' }}>
        Muted
        <span className="mono" style={{ marginLeft: 6, color: 'var(--text3)', fontWeight: 600 }}>
          {items.length}
        </span>
      </span>
      {canEdit && items.length > 1 && (
        <Button variant="ghost" size="sm"
          onClick={() => {
            if (window.confirm(`Unmute all ${items.length} silences?`)) {
              onUnmuteAll(items.map(s => s.id));
            }
          }}
          title="Unmute every active silence">
          Unmute all
        </Button>
      )}
      {items.map(s => {
        const remaining = Math.max(0, s.untilAt / 1e6 - Date.now());
        const remainStr = remaining > 24 * 3600 * 1000
          ? `${Math.floor(remaining / (24 * 3600 * 1000))}d`
          : remaining > 3600 * 1000
            ? `${Math.floor(remaining / (3600 * 1000))}h`
            : `${Math.floor(remaining / 60000)}m`;
        return (
          <span key={s.id} className="badge b-gray mono" style={{ fontWeight: 600 }}>
            <span title={`${s.kind} · ${s.pattern}`}>
              {s.pattern}
              {s.service && <span style={{ color: 'var(--text3)' }}> @ {s.service}</span>}
            </span>
            <span style={{ color: 'var(--text3)' }}>{remainStr} left</span>
            {canEdit && (
              <button type="button" onClick={() => onUnmute(s.id)}
                title="Unmute now"
                style={{
                  background: 'transparent', border: 'none', color: 'var(--text3)',
                  cursor: 'pointer', padding: 0, fontSize: 12, lineHeight: 1,
                }}>×</button>
            )}
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

  // v0.5.279 — split active vs cleared. Operator-reported:
  // "çok fazla anomali gözüküyor aktif/cleared birlikte" —
  // the mixed list buried the firing rows under cleared
  // history. Now active rows render in a dedicated section
  // (always visible, loud red badges); cleared rows go into
  // a collapsible "Cleared" group that defaults to collapsed
  // when there are >10 of them.
  const [showCleared, setShowCleared] = useState(false);
  if (items === undefined || items.length === 0) return null;
  const active  = items.filter(e => e.status === 'active');
  const cleared = items.filter(e => e.status === 'cleared');
  // Default expanded when the cleared set is small enough to
  // glance at; collapsed when it's noisy.
  const defaultExpanded = cleared.length <= 10;
  const expanded = showCleared || defaultExpanded;
  return (
    <AnomalyShell
      title="Anomaly history (last 24h)"
      hint={`${active.length} active · ${cleared.length} cleared`}
      count={items.length}>
      {active.length > 0 && (
        <AnomalyTable rows={active}
          rowRefs={rowRefs} highlight={highlight}
          title={`Active (${active.length})`} />
      )}
      {active.length === 0 && (
        <div style={{
          padding: '12px 14px', fontSize: 12, color: 'var(--text2)',
          background: 'color-mix(in srgb, var(--ok) 6%, transparent)',
          border: '1px solid color-mix(in srgb, var(--ok) 24%, transparent)',
          borderRadius: 4, marginBottom: 12,
          display: 'flex', alignItems: 'center', gap: 6,
        }}>
          <Check size={13} strokeWidth={2} style={{ color: 'var(--ok)', flexShrink: 0 }} />
          No active anomalies in the last 24h.
          {cleared.length > 0 && ` ${cleared.length} cleared event${cleared.length === 1 ? '' : 's'} below.`}
        </div>
      )}
      {cleared.length > 0 && (
        <div style={{ marginTop: active.length > 0 ? 14 : 0 }}>
          <button type="button"
            onClick={() => setShowCleared(v => !v)}
            style={{
              all: 'unset', cursor: 'pointer',
              fontSize: 12, fontWeight: 600, color: 'var(--text2)',
              padding: '6px 0', display: 'inline-flex', alignItems: 'center', gap: 6,
            }}>
            {expanded
              ? <ChevronDown size={13} strokeWidth={1.75} />
              : <ChevronRight size={13} strokeWidth={1.75} />}
            Cleared ({cleared.length})
            <span style={{ color: 'var(--text3)', fontWeight: 400, marginLeft: 4 }}>
              — resolved anomalies, kept for forensics
            </span>
          </button>
          {expanded && (
            <div style={{ marginTop: 6, opacity: 0.85 }}>
              <AnomalyTable rows={cleared}
                rowRefs={rowRefs} highlight={highlight} />
            </div>
          )}
        </div>
      )}
    </AnomalyShell>
  );
}

// AnomalyTable — extracted from HistorySection so the active +
// cleared groups share one render path (v0.5.279). Same
// columns / styling as before; only the title row above the
// table is optional now.
function AnomalyTable({ rows, rowRefs, highlight, title }: {
  rows: AnomalyEvent[];
  rowRefs: React.MutableRefObject<Record<string, HTMLTableRowElement | null>>;
  highlight: string;
  title?: string;
}) {
  return (
    <div>
      {title && (
        <div style={{
          fontSize: 11, fontWeight: 700, color: 'var(--err)',
          textTransform: 'uppercase', letterSpacing: '.06em',
          marginBottom: 6,
        }}>{title}</div>
      )}
      <div className="table-wrap">
        {/* table-layout:fixed + colgroup keeps the 8-column history table
            inside the card (no horizontal scroll) while letting Pattern /
            Service flex; numerics + timestamps get fixed mono columns. */}
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <colgroup>
            <col style={{ width: 76 }} />
            <col style={{ width: '24%' }} />
            <col />
            <col style={{ width: 80 }} />
            <col style={{ width: 56 }} />
            <col style={{ width: 134 }} />
            <col style={{ width: 134 }} />
            <col style={{ width: 44 }} />
          </colgroup>
          <thead><tr>
            <th>Status</th>
            <th>Pattern</th>
            <th>Service</th>
            <th>Kind</th>
            <th className="num">Peak&nbsp;×</th>
            <th>Started</th>
            <th>Last seen</th>
            <th>AI</th>
          </tr></thead>
          <tbody>
            {rows.map(e => (
              <tr key={e.id}
                ref={el => { rowRefs.current[e.id] = el; }}
                // content-visibility skips off-screen rows on paint — the
                // 24h history can run long. Not virtualized: the ?event=<id>
                // deep-link scrolls a specific row into view, which needs the
                // target row's DOM node mounted (a windowed table wouldn't
                // have it). containIntrinsicSize keeps the scrollbar honest.
                style={highlight === e.id ? {
                  background: 'var(--accent-soft)',
                  outline: '1px solid var(--accent2)',
                  contentVisibility: 'auto',
                  containIntrinsicSize: 'auto 36px',
                } : {
                  contentVisibility: 'auto',
                  containIntrinsicSize: 'auto 36px',
                }}>
                <td>
                  <span className={`badge ${e.status === 'active' ? 'b-err' : 'b-ok'}`}>
                    {e.status === 'active' ? 'ACTIVE' : 'CLEARED'}
                  </span>
                </td>
                <td style={{ fontWeight: 600 }} title={e.pattern}>{e.pattern}</td>
                <td>
                  <Link to={`/service?name=${encodeURIComponent(e.service)}`}
                        className="mono" style={{ fontSize: 11.5 }}
                        title={e.service || '—'}>
                    {e.service || '—'}
                  </Link>
                  <ClusterChips clusters={e.clusters} />
                  {e.recentDeploy && (
                    <DeployChip d={e.recentDeploy} service={e.service} />
                  )}
                </td>
                <td>
                  <span className="badge b-gray" style={{ fontSize: 10 }}>
                    {e.kind === 'log_pattern' ? 'LOG'
                      : e.kind === 'elastic_ml' ? 'ELASTIC ML'
                      : e.kind === 'log_template_new' ? 'NEW SHAPE'
                      : 'TRACE OP'}
                  </span>
                </td>
                <td className="num mono" style={{ fontWeight: 700 }}>{e.peakRatio.toFixed(1)}</td>
                <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(e.startedAt)}</td>
                <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>{tsLong(e.lastSeen)}</td>
                <td>
                  <CopilotExplain kind="anomaly" id={e.id} label="AI" />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function LogPatternsSection({ items, onMute, canEdit }: {
  items: LogPatternAnomaly[] | undefined;
  onMute: (kind: string, pattern: string, service: string, durationSec: number) => void;
  canEdit: boolean;
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
              {/* v0.5.306 — link now narrows to the pattern's
                  body tokens too, not just the service. Builds
                  an OR query from a.tokens so a "Disk full"
                  anomaly lands the operator on the actual
                  matching log lines ("no space left" OR "disk
                  full" OR "enospc"). Falls back to service-only
                  when tokens absent (older backends). */}
              <Link to={logsLinkForPattern(a)}
                    style={{ fontSize: 11, color: 'var(--accent2)', flexShrink: 0 }}
                    title="Open /logs filtered to this pattern + service">
                logs ↗
              </Link>
              {canEdit && <SnoozeButton onMute={d => onMute('log_pattern', a.pattern, a.service, d)} />}
            </Row>
            <div style={{ fontSize: 11, color: 'var(--text2)' }}>
              <span className="mono">{a.service || 'unknown'}</span>
              {' · '}
              <span className="mono">{fmtNum(a.currentCount)}</span> now
              {a.baselineCount > 0 && <> · <span className="mono">{fmtNum(a.baselineCount)}</span> prev</>}
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

// logsLinkForPattern — v0.5.306. Builds a /logs URL that
// narrows to both the service AND the body substrings the
// detector pattern matches on. Token list comes from the
// curated patterns[] slice in internal/anomaly/log_patterns.go,
// exposed via LogPatternAnomaly.tokens. Multiple tokens are
// OR'd; single-token patterns get a bare body match. Falls
// back to service-only when tokens are absent so older API
// responses still produce a usable link.
function logsLinkForPattern(a: LogPatternAnomaly): string {
  // v0.5.311 — Operator-reported: service shouldn't pre-select
  // in the service picker dropdown; merge into the KQL query
  // instead. Cleaner state for the operator to widen/narrow
  // without first clearing the picker. Lucene/KQL on the Logs
  // page already handles service.name:"X" natively.
  const params = new URLSearchParams();
  const clauses: string[] = [];
  if (a.service) {
    clauses.push(`service.name:"${a.service.replace(/"/g, '\\"')}"`);
  }
  const toks = a.tokens ?? [];
  if (toks.length > 0) {
    const quoted = toks.map(t => `"${t.replace(/"/g, '\\"')}"`);
    clauses.push(toks.length === 1
      ? quoted[0]
      : `(${quoted.join(' OR ')})`);
  }
  if (clauses.length > 0) {
    params.set('q', clauses.join(' AND '));
  }
  return `/logs?${params.toString()}`;
}

// DeployChip — v0.5.286. Inline chip on the anomaly row when a
// service deploy landed within 30 min before the anomaly fired.
// Hot tint (red) for deploys ≤ 5 min before — the post-deploy
// smoking-gun window the Problem priority logic also uses
// (chstore/problem.go computeProblemPriority).
function DeployChip({ d, service }: {
  d: { version: string; ageSeconds: number; timeUnixNs: number };
  service: string;
}) {
  const ageMin = Math.max(1, Math.round(d.ageSeconds / 60));
  const ageLabel = ageMin >= 60
    ? `${Math.round(ageMin / 60)}h before`
    : `${ageMin}m before`;
  const hot = d.ageSeconds <= 5 * 60;
  const palette = hot
    ? {
        bg: 'color-mix(in srgb, var(--err) 14%, transparent)',
        border: 'color-mix(in srgb, var(--err) 50%, transparent)',
        color: 'var(--err)',
      }
    : {
        bg: 'color-mix(in srgb, var(--warn) 12%, transparent)',
        border: 'color-mix(in srgb, var(--warn) 38%, transparent)',
        color: 'var(--warn)',
      };
  return (
    <Link to={`/service?name=${encodeURIComponent(service)}#deploys`}
      title={`Service ${service} deployed v${d.version} at ${tsLong(d.timeUnixNs)} — ${ageLabel}. Likely-cause window: ≤ 5 min.`}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 4,
        marginLeft: 8, marginTop: 2,
        padding: '1px 7px', borderRadius: 10, fontSize: 10,
        background: palette.bg, border: `1px solid ${palette.border}`,
        color: palette.color, textDecoration: 'none',
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        verticalAlign: 'middle',
      }}>
      <ArrowDownToLine size={10} strokeWidth={2} />
      <span style={{ fontWeight: 700 }}>deploy</span>
      <span>{d.version}</span>
      <span style={{ opacity: 0.75 }}>· {ageLabel}</span>
    </Link>
  );
}
