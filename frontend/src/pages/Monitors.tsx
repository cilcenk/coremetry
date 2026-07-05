import { useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { CopyButton } from '@/components/CopyButton';
import { Modal, Field, SelectField, Button, Stack, Row as UiRow } from '@/components/ui';
import {
  useMonitors, useMonitorTimeline,
  useCreateMonitor, useUpdateMonitor, useDeleteMonitor,
} from '@/lib/queries';
import { tsLong } from '@/lib/utils';
import type { Monitor, MonitorRow, MonitorStats, MonitorType } from '@/lib/types';

// /monitors — synthetic uptime + heartbeat dashboard.
//
// Two monitor types share this page:
//   - http      → server polls a URL on a schedule; row shows HTTP code,
//                 latency, last-checked time + 200-result status timeline.
//   - heartbeat → row shows a CURL command the operator can paste into
//                 the cron job. State flips down when the gap exceeds
//                 the monitor's `intervalSec` (treated as grace window).
export default function MonitorsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  const [showNew, setShowNew] = useState(false);
  const [editing, setEditing] = useState<MonitorRow | null>(null);
  const [openTimeline, setOpenTimeline] = useState<string | null>(null);

  // useMonitors polls every 30s (changed from the previous
  // 10s — the probe runner ticks on the order of minutes,
  // so 10s was over-polling). Mutations auto-invalidate.
  const monitorsQ = useMonitors();
  const items = monitorsQ.isLoading ? undefined : monitorsQ.isError ? null : monitorsQ.data ?? [];
  const deleteMonitor = useDeleteMonitor();

  return (
    <>
      <Topbar title="Monitors" />
      <div id="content">
        {isAdmin && (
          <div className="controls" style={{ marginBottom: 12 }}>
            <Button variant="primary" onClick={() => setShowNew(true)}>+ New monitor</Button>
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
              {items?.length ?? 0} monitors
            </span>
          </div>
        )}
        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="◉" title="No monitors yet">
            {isAdmin ? (
              <>Create an HTTP, TCP-port, SSL-certificate, or keyword monitor (actively probed on a
                schedule), or a Heartbeat monitor (your cron job posts a beat to a token URL —
                Coremetry alerts when it stops).</>
            ) : 'Ask an admin to create monitors.'}
          </Empty>
        )}
        {items && items.length > 0 && (
          <div className="status-grid">
            {items.map(m => (
              <MonitorCard key={m.id} m={m} isAdmin={isAdmin}
                onEdit={() => setEditing(m)}
                onDelete={async () => {
                  if (!confirm(`Delete monitor "${m.name}"?`)) return;
                  await deleteMonitor.mutateAsync(m.id);
                }}
                onTimeline={() => setOpenTimeline(openTimeline === m.id ? null : m.id)}
                showTimeline={openTimeline === m.id} />
            ))}
          </div>
        )}
        {(showNew || editing) && (
          <MonitorModal
            initial={editing}
            onClose={() => { setShowNew(false); setEditing(null); }}
            onSaved={() => { setShowNew(false); setEditing(null); }} />
        )}
      </div>
    </>
  );
}

function MonitorCard({ m, isAdmin, onEdit, onDelete, onTimeline, showTimeline }: {
  m: MonitorRow;
  isAdmin: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onTimeline: () => void;
  showTimeline: boolean;
}) {
  const status = m.lastResult?.status ?? 'unknown';
  const cls = status === 'up' ? 'operational' : status === 'down' ? 'outage' : 'degraded';
  const lastChecked = m.lastResult?.time ? tsLong(m.lastResult.time) : '—';
  return (
    <div className={`status-row status-row-${cls}`}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0, flexWrap: 'wrap' }}>
        <span className={`status-dot status-dot-${cls}`} />
        <span style={{ fontWeight: 600 }}>{m.name}</span>
        <span className="badge b-gray" style={{ textTransform: 'uppercase', letterSpacing: '.4px' }}>{m.type}</span>
        {!m.enabled && <span style={{ fontSize: 11, color: 'var(--text3)' }}>(disabled)</span>}
        {(m.type === 'http' || m.type === 'keyword') && m.url && (
          <span style={{
            fontSize: 11, fontFamily: 'monospace', color: 'var(--text3)',
            maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }} title={m.url}>{m.url}</span>
        )}
        {(m.type === 'tcp' || m.type === 'ssl-cert') && m.target && (
          <span style={{
            fontSize: 11, fontFamily: 'monospace', color: 'var(--text3)',
            maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }} title={m.target}>{m.target}</span>
        )}
        {m.type === 'keyword' && m.keyword && (
          <span className="badge b-gray" style={{ fontSize: 10 }}
            title={m.keywordInvert ? 'Down when this string IS present' : 'Down when this string is missing'}>
            {m.keywordInvert ? '≠' : '∋'} "{m.keyword}"
          </span>
        )}
        {m.type === 'heartbeat' && m.heartbeatToken && (
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <code style={{
              fontSize: 11, padding: '2px 6px', borderRadius: 3,
              background: 'var(--bg0)', color: 'var(--text2)',
            }}>
              /api/heartbeats/{m.heartbeatToken.slice(0, 8)}…
            </code>
            <CopyButton
              value={`${window.location.origin}/api/heartbeats/${m.heartbeatToken}`}
              title="Copy cron-friendly heartbeat URL" />
          </span>
        )}
        {m.lastResult?.message && (
          <span style={{ color: 'var(--text3)', fontSize: 12, maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={m.lastResult.message}>
            · {m.lastResult.message}
          </span>
        )}
        {showTimeline && <Timeline monitorId={m.id} />}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0 }}>
        {/* Uptime % rollup — 1h / 24h side-by-side. Coloured by
            health: ≥99.9% green, ≥99% amber, below = red. Standard
            SLO three-band visual signal. Hidden when no probe data
            yet (fresh monitor). */}
        {m.stats && m.stats.probes24h > 0 && (
          <UptimeChip stats={m.stats} />
        )}
        {m.type === 'ssl-cert' && m.lastResult?.detail !== undefined && (
          <CertDaysChip days={m.lastResult.detail} warnDays={m.certWarnDays ?? 14} />
        )}
        {m.lastResult?.latencyMs !== undefined && m.lastResult.latencyMs > 0 && (
          <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'monospace' }} title="Last probe latency">
            {m.lastResult.latencyMs}ms
          </span>
        )}
        <span style={{ color: 'var(--text3)', fontSize: 11 }} title={`Last checked ${lastChecked}`}>
          every {m.intervalSec}s
        </span>
        <span className={`status-pill status-pill-${cls}`}>{status === 'unknown' ? 'PENDING' : status.toUpperCase()}</span>
        <Button variant="secondary" size="sm" onClick={onTimeline}>
          {showTimeline ? '▲' : 'History ▼'}
        </Button>
        {isAdmin && (
          <>
            <Button variant="secondary" size="sm" onClick={onEdit}>Edit</Button>
            <Button variant="danger" size="sm" onClick={onDelete}>Delete</Button>
          </>
        )}
      </div>
    </div>
  );
}

// UptimeChip — 1h / 24h uptime percentages displayed side-by-side
// next to the status pill. Uses a three-band SLO visual: ≥99.9%
// green (operational), ≥99% amber (degraded), below = red. The
// breakpoints match the de-facto industry convention (Pingdom /
// BetterStack / UptimeRobot all use the same 99 / 99.9 split).
function UptimeChip({ stats }: { stats: MonitorStats }) {
  const fmt = (v: number) => {
    if (v >= 99.99) return '100%';
    if (v >= 99)    return `${v.toFixed(2)}%`;
    return `${v.toFixed(1)}%`;
  };
  const tone = (v: number) =>
    v >= 99.9 ? 'var(--ok)' :
    v >= 99   ? 'var(--warn)' :
                'var(--err)';
  return (
    <span title={`Uptime · 1h ${fmt(stats.uptime1h)} · 24h ${fmt(stats.uptime24h)}\n` +
                 `Avg latency · 1h ${stats.avgLatencyMs1h}ms · 24h ${stats.avgLatencyMs24h}ms\n` +
                 `Sample (24h): ${stats.probes24h} probes`}
          style={{
            display: 'inline-flex', alignItems: 'baseline', gap: 8,
            padding: '2px 8px', borderRadius: 4,
            background: 'var(--bg3)', border: '1px solid var(--border)',
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
            fontVariantNumeric: 'tabular-nums', fontSize: 11,
          }}>
      <span style={{ color: 'var(--text3)', fontSize: 9, fontWeight: 700, letterSpacing: '0.4px', textTransform: 'uppercase' }}>1h</span>
      <span style={{ color: tone(stats.uptime1h), fontWeight: 600 }}>{fmt(stats.uptime1h)}</span>
      <span style={{ color: 'var(--border)' }}>·</span>
      <span style={{ color: 'var(--text3)', fontSize: 9, fontWeight: 700, letterSpacing: '0.4px', textTransform: 'uppercase' }}>24h</span>
      <span style={{ color: tone(stats.uptime24h), fontWeight: 600 }}>{fmt(stats.uptime24h)}</span>
    </span>
  );
}

// CertDaysChip — days remaining until the leaf cert expires, colour-banded
// against the monitor's warn threshold. Negative = already expired.
function CertDaysChip({ days, warnDays }: { days: number; warnDays: number }) {
  const tone = days < 0 ? 'var(--err)' : days < warnDays ? 'var(--warn)' : 'var(--ok)';
  const label = days < 0 ? `expired ${-days}d ago` : `${days}d left`;
  return (
    <span title={`Certificate ${days < 0 ? 'expired' : 'expires in ' + days + ' day(s)'} · warn < ${warnDays}d`}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 4,
        padding: '2px 8px', borderRadius: 4,
        background: 'var(--bg3)', border: '1px solid var(--border)',
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
        fontVariantNumeric: 'tabular-nums', fontSize: 11, color: tone, fontWeight: 600,
      }}>
      🔒 {label}
    </span>
  );
}

function Timeline({ monitorId }: { monitorId: string }) {
  const rows = useMonitorTimeline(monitorId, 60).data ?? [];
  if (rows.length === 0) return <span style={{ fontSize: 11, color: 'var(--text3)' }}>· no history yet</span>;
  // Status-bar style — 60 little blocks, leftmost = oldest, rightmost = newest.
  const ordered = [...rows].reverse();
  return (
    <span style={{ display: 'inline-flex', gap: 1, alignItems: 'center', marginLeft: 8 }}>
      {ordered.map(r => {
        const c = r.status === 'up' ? 'var(--ok)' : r.status === 'down' ? 'var(--err)' : 'var(--warn)';
        const t = `${r.status.toUpperCase()} · ${tsLong(r.time)}${r.latencyMs ? ` · ${r.latencyMs}ms` : ''}${r.message ? ` · ${r.message}` : ''}`;
        return (
          <span key={r.time} title={t}
            style={{ width: 4, height: 14, background: c, borderRadius: 1, opacity: .85 }} />
        );
      })}
    </span>
  );
}

function MonitorModal({ initial, onClose, onSaved }: {
  initial: MonitorRow | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [m, setM] = useState<Partial<Monitor>>(initial ?? {
    type: 'http', name: '', url: '', method: 'GET',
    expectedStatus: 200, timeoutSec: 5, intervalSec: 60, enabled: true,
  });
  const [error, setError] = useState<string | null>(null);
  // Mutation hooks: isPending drives the busy spinner;
  // onSuccess invalidates the monitors list automatically.
  const createMonitor = useCreateMonitor();
  const updateMonitor = useUpdateMonitor();
  const busy = createMonitor.isPending || updateMonitor.isPending;
  // Advanced section is collapsed by default for new monitors so the
  // form stays approachable; auto-expand when editing an existing
  // monitor that has non-default advanced values.
  const [showAdvanced, setShowAdvanced] = useState(() => {
    if (!initial) return false;
    return initial.method !== 'GET'
        || initial.expectedStatus !== 200
        || initial.timeoutSec !== 5;
  });

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      if (initial) await updateMonitor.mutateAsync({ id: initial.id, patch: m });
      else         await createMonitor.mutateAsync(m);
      onSaved();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Save failed');
    }
  };

  return (
    <Modal
      open={true}
      onClose={onClose}
      title={initial ? `Edit monitor — ${initial.name}` : 'New monitor'}
      size="md"
      initialFocus="input[name=name]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="monitor-form" loading={busy}>
            {initial ? 'Save' : 'Create'}
          </Button>
        </>
      }>
      <form id="monitor-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="Name"
            name="name"
            required
            value={m.name ?? ''}
            onChange={e => setM({ ...m, name: e.target.value })}
            placeholder="api.example.com health" />
          <UiRow gap={3}>
            <div style={{ flex: 1 }}>
              <SelectField
                label="Type"
                value={m.type ?? 'http'}
                onChange={e => {
                  const t = e.target.value as MonitorType;
                  setM({
                    ...m, type: t,
                    // Seed the ssl-cert warn threshold so the field isn't
                    // blank the moment the operator picks the type.
                    ...(t === 'ssl-cert' && !m.certWarnDays ? { certWarnDays: 14 } : {}),
                  });
                }}>
                <option value="http">HTTP probe</option>
                <option value="tcp">TCP port</option>
                <option value="ssl-cert">SSL certificate</option>
                <option value="keyword">Keyword (HTTP body)</option>
                <option value="heartbeat">Heartbeat (passive)</option>
              </SelectField>
            </div>
            <div style={{ flex: 1 }}>
              <Field
                label={m.type === 'heartbeat' ? 'Grace window (sec)' : 'Probe interval (sec)'}
                required type="number" min={5}
                value={m.intervalSec ?? 60}
                onChange={e => setM({ ...m, intervalSec: Number(e.target.value) })} />
            </div>
          </UiRow>
          {m.type === 'tcp' && (
            <UiRow gap={3}>
              <div style={{ flex: 2 }}>
                <Field
                  label="Target (host:port)"
                  required
                  value={m.target ?? ''}
                  onChange={e => setM({ ...m, target: e.target.value })}
                  placeholder="db.internal:5432" />
              </div>
              <div style={{ flex: 1 }}>
                <Field
                  label="Timeout (sec)"
                  type="number" min={1} max={30}
                  value={m.timeoutSec ?? 5}
                  onChange={e => setM({ ...m, timeoutSec: Number(e.target.value) })} />
              </div>
            </UiRow>
          )}
          {m.type === 'ssl-cert' && (
            <>
              <UiRow gap={3}>
                <div style={{ flex: 2 }}>
                  <Field
                    label="Target (host[:443])"
                    required
                    value={m.target ?? ''}
                    onChange={e => setM({ ...m, target: e.target.value })}
                    placeholder="example.com" />
                </div>
                <div style={{ flex: 1 }}>
                  <Field
                    label="Warn days"
                    type="number" min={1} max={3650}
                    value={m.certWarnDays ?? 14}
                    onChange={e => setM({ ...m, certWarnDays: Number(e.target.value) })} />
                </div>
                <div style={{ flex: 1 }}>
                  <Field
                    label="Timeout (sec)"
                    type="number" min={1} max={30}
                    value={m.timeoutSec ?? 5}
                    onChange={e => setM({ ...m, timeoutSec: Number(e.target.value) })} />
                </div>
              </UiRow>
              <p style={{ fontSize: 11, color: 'var(--text3)' }}>
                Flips to <b style={{ color: 'var(--err)' }}>down</b> when the leaf certificate is expired
                or has fewer than <b>{m.certWarnDays ?? 14}</b> days remaining. Trust chain is not
                validated — this checks expiry only.
              </p>
            </>
          )}
          {m.type === 'keyword' && (
            <>
              <Field
                label="URL"
                required
                value={m.url ?? ''}
                onChange={e => setM({ ...m, url: e.target.value })}
                placeholder="https://api.example.com/status" />
              <UiRow gap={3}>
                <div style={{ flex: 2 }}>
                  <Field
                    label="Keyword"
                    required
                    value={m.keyword ?? ''}
                    onChange={e => setM({ ...m, keyword: e.target.value })}
                    placeholder='e.g. "operational"' />
                </div>
                <div style={{ flex: 1 }}>
                  <Field
                    label="Timeout (sec)"
                    type="number" min={1} max={30}
                    value={m.timeoutSec ?? 5}
                    onChange={e => setM({ ...m, timeoutSec: Number(e.target.value) })} />
                </div>
              </UiRow>
              <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12 }}>
                <input type="checkbox" checked={m.keywordInvert ?? false}
                  onChange={e => setM({ ...m, keywordInvert: e.target.checked })} />
                Invert — alert when the keyword IS present (must-not-contain)
              </label>
            </>
          )}
          {m.type === 'http' && (
            <>
              <Field
                label="URL"
                required
                value={m.url ?? ''}
                onChange={e => setM({ ...m, url: e.target.value })}
                placeholder="https://api.example.com/health" />

              {/* Advanced section — Method / Expected status / Timeout
                  hidden by default to keep the basic form short. The
                  reveal toggle keeps "the simple case is one URL +
                  one interval" as the default UX. */}
              <Button
                variant="ghost" size="sm"
                onClick={() => setShowAdvanced(s => !s)}
                style={{ alignSelf: 'flex-start', textTransform: 'uppercase', letterSpacing: '0.3px' }}
                aria-expanded={showAdvanced}
                leftIcon={<span style={{ fontSize: 9 }}>{showAdvanced ? '▼' : '▶'}</span>}>
                Advanced settings
              </Button>
              {showAdvanced && (
                <UiRow gap={3}>
                  <div style={{ flex: 1 }}>
                    <SelectField
                      label="Method"
                      value={m.method ?? 'GET'}
                      onChange={e => setM({ ...m, method: e.target.value })}>
                      {['GET', 'HEAD', 'POST'].map(x => <option key={x} value={x}>{x}</option>)}
                    </SelectField>
                  </div>
                  <div style={{ flex: 1 }}>
                    <Field
                      label="Expected status"
                      type="number" min={100} max={599}
                      value={m.expectedStatus ?? 200}
                      onChange={e => setM({ ...m, expectedStatus: Number(e.target.value) })} />
                  </div>
                  <div style={{ flex: 1 }}>
                    <Field
                      label="Timeout (sec)"
                      type="number" min={1} max={60}
                      value={m.timeoutSec ?? 5}
                      onChange={e => setM({ ...m, timeoutSec: Number(e.target.value) })} />
                  </div>
                </UiRow>
              )}
            </>
          )}
          {m.type === 'heartbeat' && (
            <p style={{ fontSize: 11, color: 'var(--text3)' }}>
              On save you'll get a unique URL. POST or GET to it from your cron job at least every <b>{m.intervalSec}s</b>.
              If Coremetry sees no beat for longer than that, the monitor flips to <b style={{ color: 'var(--err)' }}>down</b> and the alert fires.
            </p>
          )}
          <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12 }}>
            <input type="checkbox" checked={m.enabled ?? true}
              onChange={e => setM({ ...m, enabled: e.target.checked })} />
            Enabled
          </label>
          {error && <div className="trp-error">{error}</div>}
        </Stack>
      </form>
    </Modal>
  );
}
