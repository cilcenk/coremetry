import { useEffect, useMemo, useState, FormEvent } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { Modal, Field, SelectField, Button, Stack } from '@/components/ui';
import { useSLOs, useCreateSLO, useDeleteSLO } from '@/lib/queries';
import { api } from '@/lib/api';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { SLIType, SLORow } from '@/lib/types';

// v0.6.44 — sortable columns on /slos. Forecast + 7d trend stay
// non-sortable because their values are loaded asynchronously per
// row (separate /api/slos/{id}/forecast + /burn-series fetches) so
// the SLORow object doesn't carry the numbers needed to compare.

export default function SLOsPage() {
  const { user } = useAuth();
  const [services, setServices] = useState<string[]>([]);
  const [showNew, setShowNew] = useState(false);
  const [showAuto, setShowAuto] = useState(false);
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';

  // useSLOs polls every 60s + auto-invalidates on
  // create/delete via the hook's onSuccess.
  const slosQ = useSLOs();
  const items = slosQ.isLoading ? undefined : slosQ.isError ? null : slosQ.data ?? [];
  const deleteSLO = useDeleteSLO();

  // Shared sortable + resizable table. Columns built per-render so the
  // SLI header reflects the window + the admin actions column appears
  // only for editors/admins. Numeric accessors preserve the prior
  // sort semantics; null status (no telemetry yet) sinks to the bottom.
  const windowDays = items && items.length > 0 ? items[0].windowDays : 0;
  const sloCols = useMemo<DataTableColumn<SLORow>[]>(() => [
    { id: 'name',     label: 'Name',        sortValue: o => o.name?.toLowerCase() ?? '',    naturalDir: 'asc', width: 220 },
    { id: 'service',  label: 'Service',     sortValue: o => o.service?.toLowerCase() ?? '', naturalDir: 'asc', width: 150 },
    { id: 'target',   label: 'Target',      sortValue: o => o.target,                       naturalDir: 'desc', width: 96 },
    { id: 'sli',      label: `SLI (${windowDays}d)`, sortValue: o => o.status?.sli ?? null,  naturalDir: 'desc', width: 110 },
    { id: 'budget',   label: 'Budget left', sortValue: o => o.status?.budgetRemaining ?? null, naturalDir: 'desc', width: 130 },
    { id: 'burn',     label: 'Burn rate',   sortValue: o => o.status?.burnRate ?? null,      naturalDir: 'desc', width: 110 },
    { id: 'forecast', label: 'Forecast',    width: 120 },
    { id: 'trend',    label: '7d trend',    width: 100 },
    { id: 'status',   label: 'Status',      sortValue: o => (o.status ? (o.status.healthy ? 1 : 0) : null), naturalDir: 'desc', width: 110 },
    ...(isAdmin ? [{ id: 'actions', label: '', width: 130 } as DataTableColumn<SLORow>] : []),
  ], [windowDays, isAdmin]);

  const dt = useDataTable<SLORow>({
    storageKey: 'slos',
    columns: sloCols,
    rows: items ?? [],
    initialSort: { id: 'name', dir: 'asc' },
  });

  // Service list for the picker — one-shot lookup, not
  // worth a hook abstraction.
  useEffect(() => {
    api.services({ from: 0, to: 0 })
      .then(s => setServices((s ?? []).map(x => x.name))).catch(() => {});
  }, []);

  const onDelete = async (id: string) => {
    if (!confirm('Delete this SLO?')) return;
    await deleteSLO.mutateAsync(id);
  };

  return (
    <>
      <Topbar title="SLOs" />
      <div id="content">
        <div className="controls">
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Service Level Objectives — track availability and latency targets with error-budget burn down.
          </span>
          {isAdmin && (
            <>
              <Button variant="secondary" size="sm" onClick={() => setShowAuto(true)}
                style={{ marginLeft: 'auto' }}
                title="Scan recent telemetry and propose baseline-grounded availability + latency SLOs">
                ✨ Auto-create
              </Button>
              <Button variant="primary" size="sm" onClick={() => setShowNew(true)}>+ New SLO</Button>
            </>
          )}
        </div>

        {items === undefined && <Spinner />}
        {items !== undefined && (!items || items.length === 0) && (
          <Empty icon="◉" title="No SLOs defined">
            {isAdmin ? 'Create one to start tracking error budgets.' : 'Ask an admin to define SLOs.'}
          </Empty>
        )}
        {items && items.length > 0 && (
          <div className="table-wrap">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(o => (
                  <tr key={o.id}>
                    <td>
                      <div style={{ fontWeight: 600 }}>{o.name}</div>
                      <div style={{ fontSize: 11, color: 'var(--text3)' }}>
                        {o.sliType === 'latency'
                          ? `latency ≤ ${o.thresholdMs}ms`
                          : 'availability'}
                        {o.operation && <> · op=<code>{o.operation}</code></>}
                      </div>
                    </td>
                    <td className="mono">{o.service}</td>
                    <td className="mono">{(o.target * 100).toFixed(2)}%</td>
                    <td className="mono">
                      {o.status ? (o.status.sli * 100).toFixed(3) + '%' : '—'}
                    </td>
                    <td className="mono">
                      {o.status ? <BudgetBar value={o.status.budgetRemaining} /> : '—'}
                    </td>
                    <td className="mono">
                      {o.status ? <BurnBadge rate={o.status.burnRate} /> : '—'}
                    </td>
                    <td>
                      <ForecastChip sloId={o.id} />
                    </td>
                    <td>
                      <BurnSparkline sloId={o.id} />
                    </td>
                    <td>
                      {o.status?.healthy
                        ? <span className="badge b-ok">Healthy</span>
                        : <span className="badge b-err">Breached</span>}
                    </td>
                    {isAdmin && (
                      <td style={{ display: 'flex', gap: 6 }}>
                        <BurnExplainButton sloId={o.id} />
                        <Button variant="secondary" size="sm" onClick={() => onDelete(o.id)}>Delete</Button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {showNew && isAdmin && (
          <NewSLOModal services={services}
            onClose={() => setShowNew(false)}
            onCreated={() => setShowNew(false)} />
        )}
        {showAuto && isAdmin && (
          <AutoSLOModal onClose={() => setShowAuto(false)} onCreated={() => {
            setShowAuto(false);
            slosQ.refetch();
          }} />
        )}
      </div>
    </>
  );
}

// AutoSLOModal (v0.5.147) walks the operator through dry-run →
// review → commit. Two phases: dry-run lists every (service,
// sliType) pair the autocreate pass proposes with a measured
// baseline + a generated target. Commit re-POSTs with no
// dry_run flag and bulk-writes the non-skipped suggestions.
function AutoSLOModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [preview, setPreview] = useState<Awaited<ReturnType<typeof api.autocreateSLOs>>['suggestions'] | null>(null);
  useEffect(() => {
    setRunning(true);
    api.autocreateSLOs(true)
      .then(d => setPreview(d.suggestions ?? []))
      .catch(e => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setRunning(false));
  }, []);
  const commit = async () => {
    setRunning(true); setError(null);
    try {
      await api.autocreateSLOs(false);
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setRunning(false);
    }
  };
  const proposed = preview ? preview.filter(p => !p.skipped) : [];
  const skipped = preview ? preview.filter(p => p.skipped) : [];
  return (
    <div role="dialog" style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)',
      display: 'grid', placeItems: 'center', zIndex: 50,
    }}>
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 16, maxWidth: 760, width: '90%',
        maxHeight: '80vh', overflowY: 'auto',
      }}>
        <div style={{ display: 'flex', alignItems: 'baseline', marginBottom: 8 }}>
          <span style={{ fontSize: 14, fontWeight: 700 }}>✨ Auto-create SLOs</span>
          <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
            Baseline window: last 7 days
          </span>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 10, lineHeight: 1.5 }}>
          Scans the top services by recent traffic and stamps an availability +
          latency SLO for each one that doesn't already have one. Targets are
          measured-minus-buffer (availability) or p99 × 1.5 rounded up
          (latency) so a fresh SLO isn't already in the red on day one.
          Existing SLOs are never overwritten.
        </div>
        {running && <Spinner />}
        {error && (
          <div style={{ color: 'var(--err)', fontSize: 12, marginBottom: 8 }}>{error}</div>
        )}
        {preview && proposed.length === 0 && (
          <Empty icon="◇" title="Nothing to propose">
            Either no services have traffic in the last 7d, or every service
            already has both an availability and a latency SLO.
          </Empty>
        )}
        {preview && proposed.length > 0 && (
          <div className="table-wrap" style={{ maxHeight: '50vh' }}>
            <table>
              <thead><tr>
                <th>Service</th><th>SLI</th><th>Target / Threshold</th><th>Baseline</th><th>Reason</th>
              </tr></thead>
              <tbody>
                {proposed.map((p, i) => (
                  <tr key={i}>
                    <td className="mono">{p.service}</td>
                    <td>{p.sliType}</td>
                    <td className="mono">
                      {p.sliType === 'latency'
                        ? `≤ ${p.thresholdMs?.toFixed(0)} ms @ ${(p.target * 100).toFixed(1)}%`
                        : `${(p.target * 100).toFixed(2)}%`}
                    </td>
                    <td className="mono" style={{ color: 'var(--text3)' }}>
                      {p.sliType === 'latency'
                        ? `${p.baselineMs?.toFixed(1)} ms`
                        : `${((p.baselineSli ?? 0) * 100).toFixed(3)}%`}
                    </td>
                    <td style={{ fontSize: 11, color: 'var(--text2)' }}>{p.reason}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {preview && skipped.length > 0 && (
          <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)' }}>
            {skipped.length} service{skipped.length === 1 ? '' : 's'} skipped — existing SLOs not overwritten.
          </div>
        )}
        <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
          <Button variant="secondary" size="sm" onClick={onClose}>Cancel</Button>
          <Button variant="primary" size="sm"
            disabled={running || !preview || proposed.length === 0}
            onClick={commit}
            style={{ marginLeft: 'auto' }}
            title="Create the SLOs listed above (existing SLOs untouched)">
            Create {proposed.length} SLO{proposed.length === 1 ? '' : 's'}
          </Button>
        </div>
      </div>
    </div>
  );
}

function BudgetBar({ value }: { value: number }) {
  const pct = Math.max(0, Math.min(1, value)) * 100;
  const color = pct > 50 ? 'var(--ok)' : pct > 20 ? 'var(--warn)' : 'var(--err)';
  return (
    <div title={`${pct.toFixed(1)}% of error budget remaining`} style={{
      display: 'inline-block', width: 100, height: 10, position: 'relative',
      background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: 3,
      verticalAlign: 'middle',
    }}>
      <div style={{
        position: 'absolute', left: 0, top: 0, bottom: 0, width: `${pct}%`,
        background: color, borderRadius: 2,
      }} />
    </div>
  );
}

function BurnBadge({ rate }: { rate: number }) {
  if (!isFinite(rate)) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const cls = rate > 2 ? 'b-err' : rate > 1 ? 'b-warn' : 'b-ok';
  return <span className={`badge ${cls}`}>{rate.toFixed(2)}×</span>;
}

// v0.6.30 — forecast chip. Reads /api/slos/{id}/forecast lazily
// per row (server-cached 60s collapses parallel requests across
// rows). Three visual states:
//   • Safe — burn rate ≤ 1, budget grows back. Quiet "OK" pill.
//   • Soft warn — burn rate > 1 but exhaust > 24h away. Amber.
//   • Hard alert — exhaust ≤ 24h OR budget already 0. Red.
// Tooltip carries the projected hours so the operator can pick
// "12h" out of the amber chip's "soon" qualitative read.
function ForecastChip({ sloId }: { sloId: string }) {
  const [data, setData] = useState<{
    burnRate: number; hoursToExhaust: number;
    willBreachWithin24h: boolean; safeBurn: boolean;
  } | null>(null);
  useEffect(() => {
    let cancelled = false;
    api.sloForecast(sloId).then(d => { if (!cancelled) setData(d); }).catch(() => {});
    return () => { cancelled = true; };
  }, [sloId]);
  if (!data) return <span style={{ color: 'var(--text3)' }}>…</span>;
  if (data.safeBurn) {
    return (
      <span className="badge b-ok"
        title={`Current burn rate ${data.burnRate.toFixed(2)}× — at or below replenishment, budget is stable`}>
        OK
      </span>
    );
  }
  const cls = data.willBreachWithin24h ? 'b-err' : 'b-warn';
  const label = fmtHoursToExhaust(data.hoursToExhaust);
  return (
    <span className={`badge ${cls}`}
      title={`At burn rate ${data.burnRate.toFixed(2)}×, the error budget will be exhausted in ~${data.hoursToExhaust.toFixed(1)}h`}>
      ⌛ {label}
    </span>
  );
}

function fmtHoursToExhaust(h: number): string {
  if (h <= 0) return 'breached';
  if (h < 1) return '<1h';
  if (h < 24) return `${Math.round(h)}h`;
  const days = h / 24;
  if (days < 7) return `${days.toFixed(1)}d`;
  return `${Math.round(days)}d`;
}

function NewSLOModal({ services, onClose, onCreated }: {
  services: string[]; onClose: () => void; onCreated: () => void;
}) {
  const [name, setName] = useState('');
  const [service, setService] = useState('');
  const [sliType, setSliType] = useState<SLIType>('availability');
  const [target, setTarget] = useState('99.0');
  const [windowDays, setWindowDays] = useState('30');
  const [thresholdMs, setThresholdMs] = useState('500');
  const [operation, setOperation] = useState('');
  const [error, setError] = useState<string | null>(null);

  // useCreateSLO handles busy state via isPending and
  // auto-invalidates the SLOs list on success — no manual
  // refresh() in the parent.
  const createSLO = useCreateSLO();
  const busy = createSLO.isPending;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await createSLO.mutateAsync({
        name, service, sliType,
        target: parseFloat(target) / 100,
        windowDays: parseInt(windowDays || '30'),
        thresholdMs: sliType === 'latency' ? parseFloat(thresholdMs) : 0,
        operation,
      });
      onCreated();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      try { setError(JSON.parse(msg.replace(/^HTTP \d+:\s*/, ''))?.error ?? msg); }
      catch { setError(msg); }
    }
  };

  return (
    <Modal
      open={true}
      onClose={onClose}
      title="New SLO"
      size="md"
      initialFocus="input[name=name]"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-slo-form" loading={busy}>Create</Button>
        </>
      }>
      <form id="new-slo-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field
            label="Name"
            name="name"
            required
            value={name}
            onChange={e => setName(e.target.value)} />
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <div className="field">
              <label className="field-label">Service</label>
              <ServicePicker value={service} onChange={setService} placeholder="…" />
            </div>
            <Field
              label="Operation (optional)"
              value={operation}
              onChange={e => setOperation(e.target.value)} />
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <SelectField
              label="SLI type"
              value={sliType}
              onChange={e => setSliType(e.target.value as SLIType)}>
              <option value="availability">Availability</option>
              <option value="latency">Latency</option>
            </SelectField>
            <Field
              label="Window (days)"
              type="number" min={1} max={365}
              value={windowDays}
              onChange={e => setWindowDays(e.target.value)} />
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <Field
              label="Target %"
              hint="e.g. 99.9"
              required type="number" min={0} max={100} step="0.001"
              value={target}
              onChange={e => setTarget(e.target.value)} />
            {sliType === 'latency' && (
              <Field
                label="Threshold (ms)"
                required type="number" min={0} step="0.1"
                value={thresholdMs}
                onChange={e => setThresholdMs(e.target.value)} />
            )}
          </div>
          {error && (
            <div style={{ color: 'var(--err)', fontSize: 12 }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}

// BurnExplainButton — feeds the SLO's current status + fast
// + slow burn-rate samples to /api/copilot/explain-slo and
// renders the model's verdict inline. Self-hides when the
// copilot isn't configured (same gate the other CopilotExplain
// surfaces use). Operator clicks → modal with budget
// trajectory + recommended first investigation.
function BurnExplainButton({ sloId }: { sloId: string }) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const [resp, setResp] = useState<Awaited<ReturnType<typeof api.copilotExplainSLO>> | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);
  if (enabled !== true) return null;

  const run = async () => {
    setBusy(true); setError(null); setResp(null); setOpen(true);
    try {
      const r = await api.copilotExplainSLO(sloId);
      setResp(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Explain failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <button onClick={run} disabled={busy} className="sec"
        title="Ask copilot whether this SLO's budget is on track or burning fast"
        style={{ fontSize: 12, padding: '4px 10px', color: 'var(--accent2)' }}>
        ✨ Explain burn
      </button>
      {open && (
        <Modal open={open} onClose={() => setOpen(false)} title="SLO burn analysis">
          {busy && <Spinner />}
          {error && <div style={{ color: 'var(--err)', fontSize: 12 }}>{error}</div>}
          {resp && (
            <div style={{ fontSize: 13, lineHeight: 1.5 }}>
              <div style={{
                display: 'flex', gap: 12, fontSize: 11,
                color: 'var(--text3)', marginBottom: 10,
                fontFamily: 'ui-monospace, monospace',
              }}>
                <span>fast burn: {resp.fastBurn.toFixed(2)}×</span>
                <span>slow burn: {resp.slowBurn.toFixed(2)}×</span>
                {resp.status && (
                  <>
                    <span>SLI: {(resp.status.sli * 100).toFixed(3)}%</span>
                    <span>budget: {(resp.status.budgetRemaining * 100).toFixed(2)}%</span>
                  </>
                )}
              </div>
              <div style={{ whiteSpace: 'pre-wrap' }}>{resp.explanation}</div>
            </div>
          )}
        </Modal>
      )}
    </>
  );
}

// BurnSparkline — small inline SVG showing the last 7 days of
// burn-rate (one bucket per day). Stroke color is keyed off the
// max burn-rate in the series so an operator scanning the SLO
// list spots "this one's been hot for days" at a glance without
// opening the detail view. Endpoint serves a 60s-cached series
// so even 30 SLOs only hit the chstore once a minute.
function BurnSparkline({ sloId }: { sloId: string }) {
  type Pt = { time: number; total: number; good: number; burnRate: number };
  const [series, setSeries] = useState<Pt[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let cancelled = false;
    api.sloBurnSeries(sloId, 7)
      .then(d => { if (!cancelled) setSeries(d.series ?? []); })
      .catch(() => { if (!cancelled) setFailed(true); });
    return () => { cancelled = true; };
  }, [sloId]);
  if (failed) return <span style={{ color: 'var(--text3)' }}>—</span>;
  if (!series) return <span style={{ color: 'var(--text3)', fontSize: 11 }}>…</span>;
  if (series.length === 0) return <span style={{ color: 'var(--text3)' }}>—</span>;

  const W = 84, H = 18, PAD = 1;
  const maxRate = series.reduce((m, p) => Math.max(m, p.burnRate), 0);
  // Use 1.0 as the upper anchor so a healthy SLO renders flat near the
  // bottom rather than self-rescaling and looking dramatic.
  const yMax = Math.max(1, maxRate);
  const stepX = series.length > 1 ? (W - PAD * 2) / (series.length - 1) : 0;
  const yOf = (v: number) => H - PAD - (v / yMax) * (H - PAD * 2);
  const path = series.map((p, i) => {
    const x = PAD + i * stepX;
    return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${yOf(p.burnRate).toFixed(1)}`;
  }).join(' ');
  const color = maxRate > 1 ? 'var(--err)' : maxRate > 0.5 ? 'var(--warn)' : 'var(--ok)';
  const tooltip = `7d burn rate — max ${maxRate.toFixed(2)}×`;
  return (
    <svg width={W} height={H} style={{ display: 'block' }}
      role="img" aria-label={tooltip}>
      <title>{tooltip}</title>
      {/* Reference line at burn=1 (budget consumed exactly at allowed rate) */}
      {yMax > 1 && (
        <line x1={0} x2={W} y1={yOf(1)} y2={yOf(1)}
          stroke="var(--border)" strokeWidth={0.5} strokeDasharray="2,2" />
      )}
      <path d={path} fill="none" stroke={color} strokeWidth={1.5}
        strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

