'use client';
import { useEffect, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { api } from '@/lib/api';
import { tsLong } from '@/lib/utils';
import type { Monitor, MonitorRow, MonitorResult, MonitorStats } from '@/lib/types';

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
  const [items, setItems] = useState<MonitorRow[] | null | undefined>(undefined);
  const [showNew, setShowNew] = useState(false);
  const [editing, setEditing] = useState<MonitorRow | null>(null);
  const [openTimeline, setOpenTimeline] = useState<string | null>(null);

  const refresh = () => {
    setItems(undefined);
    api.listMonitors().then(d => setItems(d ?? [])).catch(() => setItems(null));
  };
  useEffect(() => {
    refresh();
    // Re-poll every 10s so the dashboard auto-updates as probes run.
    const t = setInterval(refresh, 10_000);
    return () => clearInterval(t);
  }, []);

  return (
    <>
      <Topbar title="Monitors" />
      <div id="content">
        {isAdmin && (
          <div className="controls" style={{ marginBottom: 12 }}>
            <button onClick={() => setShowNew(true)}>+ New monitor</button>
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
              {items?.length ?? 0} monitors
            </span>
          </div>
        )}
        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="◉" title="No monitors yet">
            {isAdmin ? (
              <>Create an HTTP monitor (URL pinged on a schedule) or a Heartbeat monitor
                (your cron job posts a beat to a token URL — Coremetry alerts when it stops).</>
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
                  await api.deleteMonitor(m.id); refresh();
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
            onSaved={() => { setShowNew(false); setEditing(null); refresh(); }} />
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
        <span style={{
          fontSize: 10, padding: '1px 6px', borderRadius: 3, background: 'var(--bg3)',
          color: 'var(--text2)', textTransform: 'uppercase', letterSpacing: '.4px',
        }}>{m.type}</span>
        {!m.enabled && <span style={{ fontSize: 11, color: 'var(--text3)' }}>(disabled)</span>}
        {m.type === 'http' && (
          <span style={{
            fontSize: 11, fontFamily: 'monospace', color: 'var(--text3)',
            maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }} title={m.url}>{m.url}</span>
        )}
        {m.type === 'heartbeat' && m.heartbeatToken && (
          <code style={{
            fontSize: 11, padding: '2px 6px', borderRadius: 3,
            background: 'var(--bg0)', color: 'var(--text2)', cursor: 'pointer',
          }}
          title="Click to copy cron-friendly URL"
          onClick={() => {
            const url = `${window.location.origin}/api/heartbeats/${m.heartbeatToken}`;
            navigator.clipboard?.writeText(url);
          }}>
            /api/heartbeats/{m.heartbeatToken!.slice(0, 8)}…
          </code>
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
        {m.lastResult?.latencyMs !== undefined && m.lastResult.latencyMs > 0 && (
          <span style={{ color: 'var(--text3)', fontSize: 11, fontFamily: 'monospace' }} title="Last probe latency">
            {m.lastResult.latencyMs}ms
          </span>
        )}
        <span style={{ color: 'var(--text3)', fontSize: 11 }} title={`Last checked ${lastChecked}`}>
          every {m.intervalSec}s
        </span>
        <span className={`status-pill status-pill-${cls}`}>{status === 'unknown' ? 'PENDING' : status.toUpperCase()}</span>
        <button className="sec" onClick={onTimeline} style={{ padding: '4px 10px', fontSize: 11 }}>
          {showTimeline ? '▲' : 'History ▼'}
        </button>
        {isAdmin && (
          <>
            <button className="sec" onClick={onEdit} style={{ padding: '4px 10px', fontSize: 11 }}>Edit</button>
            <button className="sec" onClick={onDelete} style={{ padding: '4px 10px', fontSize: 11, color: 'var(--err)' }}>Delete</button>
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

function Timeline({ monitorId }: { monitorId: string }) {
  const [rows, setRows] = useState<MonitorResult[]>([]);
  useEffect(() => {
    api.monitorTimeline(monitorId, 60).then(r => setRows(r ?? [])).catch(() => setRows([]));
  }, [monitorId]);
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
    setBusy(true); setError(null);
    try {
      if (initial) await api.updateMonitor(initial.id, m);
      else         await api.createMonitor(m);
      onSaved();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Save failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 480, padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>
          {initial ? `Edit monitor — ${initial.name}` : 'New monitor'}
        </div>
        <form onSubmit={submit}>
          <Field label="Name">
            <input required autoFocus value={m.name ?? ''}
              onChange={e => setM({ ...m, name: e.target.value })}
              placeholder="api.example.com health" style={{ width: '100%' }} />
          </Field>
          <Row>
            <Field label="Type" flex={1}>
              <select value={m.type ?? 'http'}
                onChange={e => setM({ ...m, type: e.target.value as 'http' | 'heartbeat' })}>
                <option value="http">HTTP probe</option>
                <option value="heartbeat">Heartbeat (passive)</option>
              </select>
            </Field>
            <Field label={m.type === 'heartbeat' ? 'Grace window (sec)' : 'Probe interval (sec)'} flex={1}>
              <input required type="number" min={5} value={m.intervalSec ?? 60}
                onChange={e => setM({ ...m, intervalSec: Number(e.target.value) })}
                style={{ width: '100%' }} />
            </Field>
          </Row>
          {m.type === 'http' && (
            <>
              <Field label="URL">
                <input required value={m.url ?? ''}
                  onChange={e => setM({ ...m, url: e.target.value })}
                  placeholder="https://api.example.com/health" style={{ width: '100%' }} />
              </Field>

              {/* Advanced section — Method / Expected status / Timeout
                  hidden by default to keep the basic form short. The
                  reveal toggle keeps "the simple case is one URL +
                  one interval" as the default UX. */}
              <button type="button" onClick={() => setShowAdvanced(s => !s)}
                style={{
                  marginTop: 12, padding: '4px 0', background: 'transparent',
                  border: 'none', color: 'var(--text2)', fontSize: 12,
                  fontWeight: 600, letterSpacing: '0.3px', textTransform: 'uppercase',
                  cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6,
                }}>
                <span style={{ fontSize: 9 }}>{showAdvanced ? '▼' : '▶'}</span>
                Advanced settings
              </button>
              {showAdvanced && (
                <Row>
                  <Field label="Method" flex={1}>
                    <select value={m.method ?? 'GET'}
                      onChange={e => setM({ ...m, method: e.target.value })}>
                      {['GET', 'HEAD', 'POST'].map(x => <option key={x} value={x}>{x}</option>)}
                    </select>
                  </Field>
                  <Field label="Expected status" flex={1}>
                    <input type="number" min={100} max={599} value={m.expectedStatus ?? 200}
                      onChange={e => setM({ ...m, expectedStatus: Number(e.target.value) })}
                      style={{ width: '100%' }} />
                  </Field>
                  <Field label="Timeout (sec)" flex={1}>
                    <input type="number" min={1} max={60} value={m.timeoutSec ?? 5}
                      onChange={e => setM({ ...m, timeoutSec: Number(e.target.value) })}
                      style={{ width: '100%' }} />
                  </Field>
                </Row>
              )}
            </>
          )}
          {m.type === 'heartbeat' && (
            <p style={{ fontSize: 11, color: 'var(--text3)', marginTop: -4 }}>
              On save you'll get a unique URL. POST or GET to it from your cron job at least every <b>{m.intervalSec}s</b>.
              If Coremetry sees no beat for longer than that, the monitor flips to <b style={{ color: 'var(--err)' }}>down</b> and the alert fires.
            </p>
          )}
          <label style={{ display: 'flex', gap: 6, alignItems: 'center', color: 'var(--text2)', fontSize: 12, marginTop: 6 }}>
            <input type="checkbox" checked={m.enabled ?? true}
              onChange={e => setM({ ...m, enabled: e.target.checked })} />
            Enabled
          </label>
          {error && <div className="trp-error" style={{ marginTop: 10 }}>{error}</div>}
          <div style={{ display: 'flex', gap: 8, marginTop: 16, justifyContent: 'flex-end' }}>
            <button type="button" className="sec" onClick={onClose}>Cancel</button>
            <button type="submit" disabled={busy}>{busy ? 'Saving…' : initial ? 'Save' : 'Create'}</button>
          </div>
        </form>
      </div>
    </div>
  );
}

function Field({ label, children, flex }: { label: string; children: React.ReactNode; flex?: number }) {
  return (
    <label style={{ display: 'block', marginTop: 10, flex }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 3 }}>{label}</div>
      {children}
    </label>
  );
}
function Row({ children }: { children: React.ReactNode }) {
  return <div style={{ display: 'flex', gap: 10 }}>{children}</div>;
}
