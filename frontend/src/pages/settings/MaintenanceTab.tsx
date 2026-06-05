import { useEffect, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Modal, Button, Stack } from '@/components/ui';
import { api, type MaintenanceWindow } from '@/lib/api';
import { Field, Row } from './shared';

// ── Maintenance windows tab ────────────────────────────────────────────────
//
// Operator-declared time ranges that suppress alert
// notifications for matching (service, severity) tuples.
// Problems still open + auto-resolve as usual — only the
// live channel fan-out (Slack / email / Zoom / etc.) is
// skipped. After the window expires the /anomalies +
// /incidents pages still show the full timeline.

export function MaintenanceTab() {
  const [items, setItems] = useState<MaintenanceWindow[] | null | undefined>(undefined);
  const [showAll, setShowAll] = useState(false);
  const [creating, setCreating] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const load = () => {
    setItems(undefined);
    api.listMaintenanceWindows(showAll)
      .then(r => setItems(r ?? []))
      .catch(() => setItems(null));
  };
  useEffect(load, [showAll]);

  const del = async (id: string) => {
    if (!confirm('Delete this maintenance window? Alerts will resume firing immediately.')) return;
    try {
      await api.deleteMaintenanceWindow(id);
      setMsg({ kind: 'ok', text: 'Window removed' });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : 'Delete failed' });
    }
  };

  const now = Date.now() * 1e6;
  return (
    <div>
      <div style={{ marginBottom: 12, fontSize: 12, color: 'var(--text2)' }}>
        While an active window matches a problem's <code>(service, severity)</code>,
        the live channel fan-out is suppressed. Problems still open + auto-resolve
        so the post-window review on <code>/anomalies</code> + <code>/incidents</code>
        is intact. Service supports <code>*</code> (all), an exact name, or a
        <code>name*</code> prefix.
      </div>
      <div className="controls" style={{ marginBottom: 12 }}>
        <Button variant="primary" onClick={() => setCreating(true)}>+ New maintenance window</Button>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 5,
                        color: 'var(--text2)', cursor: 'pointer', marginLeft: 'auto' }}>
          <input type="checkbox" checked={showAll}
                 onChange={e => setShowAll(e.target.checked)} />
          Show past / disabled (last 30d)
        </label>
      </div>
      {msg && (
        <div style={{
          marginBottom: 10, padding: '6px 10px', borderRadius: 4, fontSize: 12,
          color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)',
          background: msg.kind === 'ok' ? 'rgba(63,185,80,0.10)' : 'rgba(220,38,38,0.08)',
          border: `1px solid ${msg.kind === 'ok' ? 'rgba(63,185,80,0.35)' : 'rgba(220,38,38,0.3)'}`,
        }}>{msg.text}</div>
      )}
      {items === undefined && <Spinner />}
      {items !== undefined && (!items || items.length === 0) && (
        <Empty icon="◯" title="No maintenance windows">
          Declare a window before a planned deploy to silence alerts on the
          affected services. They auto-expire — no clean-up needed.
        </Empty>
      )}
      {items && items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead><tr>
              <th>Service</th><th>Severity</th>
              <th>Starts</th><th>Ends</th><th>Reason</th>
              <th>By</th><th>Status</th><th style={{ textAlign: 'right' }}>Actions</th>
            </tr></thead>
            <tbody>
              {items.map(w => {
                const active = !w.disabled && w.startAt <= now && now <= w.endAt;
                const upcoming = !w.disabled && w.startAt > now;
                return (
                  <tr key={w.id}>
                    <td style={{ fontFamily: 'monospace', fontWeight: 600 }}>{w.service}</td>
                    <td className="mono" style={{ fontSize: 11, textTransform: 'uppercase' }}>{w.severity}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{new Date(w.startAt / 1e6).toLocaleString()}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{new Date(w.endAt / 1e6).toLocaleString()}</td>
                    <td style={{ fontSize: 12, color: 'var(--text2)' }}>{w.reason || '—'}</td>
                    <td style={{ fontSize: 11, color: 'var(--text3)', fontFamily: 'monospace' }}>{w.createdBy || '—'}</td>
                    <td>
                      {w.disabled ? <span className="badge b-err" style={{ fontSize: 9 }}>DISABLED</span>
                        : active   ? <span className="badge b-warn" style={{ fontSize: 9 }}>ACTIVE</span>
                        : upcoming ? <span className="badge b-info" style={{ fontSize: 9 }}>UPCOMING</span>
                        :            <span className="badge b-ok" style={{ fontSize: 9 }}>PAST</span>}
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      {!w.disabled && (
                        <Button variant="danger" size="sm" onClick={() => del(w.id)}>End / delete</Button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {creating && (
        <NewMaintenanceModal onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); load(); setMsg({ kind: 'ok', text: 'Window created' }); }} />
      )}
    </div>
  );
}

function NewMaintenanceModal({ onClose, onCreated }: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [service, setService] = useState('*');
  const [severity, setSeverity] = useState('*');
  // Default to "right now → +60 min". datetime-local needs YYYY-MM-DDTHH:MM
  // formatted in the operator's local zone.
  const toLocalInput = (d: Date) => {
    const off = d.getTimezoneOffset();
    return new Date(d.getTime() - off * 60_000).toISOString().slice(0, 16);
  };
  const [startAt, setStartAt] = useState(() => toLocalInput(new Date()));
  const [endAt, setEndAt] = useState(() => toLocalInput(new Date(Date.now() + 60 * 60_000)));
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const startMs = new Date(startAt).getTime();
      const endMs = new Date(endAt).getTime();
      if (!isFinite(startMs) || !isFinite(endMs)) throw new Error('Invalid date');
      if (endMs <= startMs) throw new Error('End must be after start');
      await api.createMaintenanceWindow({
        service: service.trim() || '*',
        severity: severity || '*',
        startAt: startMs * 1e6,
        endAt: endMs * 1e6,
        reason: reason.trim(),
      });
      onCreated();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Create failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open onClose={onClose} title="New maintenance window" size="md"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-mw-form" loading={busy}>Create</Button>
        </>
      }>
      <form id="new-mw-form" onSubmit={submit}>
        <Stack gap={3}>
          <Field label='Service ("*" for global · exact name · "payment*" prefix)'>
            <input required value={service}
              onChange={e => setService(e.target.value)}
              style={{ width: '100%' }} />
          </Field>
          <Field label="Severity">
            <select value={severity} onChange={e => setSeverity(e.target.value)}
              style={{ width: '100%' }}>
              <option value="*">All severities</option>
              <option value="info">info only</option>
              <option value="warning">warning only</option>
              <option value="critical">critical only</option>
            </select>
          </Field>
          <Row>
            <Field label="Starts at" flex={1}>
              <input type="datetime-local" required value={startAt}
                onChange={e => setStartAt(e.target.value)}
                style={{ width: '100%' }} />
            </Field>
            <Field label="Ends at" flex={1}>
              <input type="datetime-local" required value={endAt}
                onChange={e => setEndAt(e.target.value)}
                style={{ width: '100%' }} />
            </Field>
          </Row>
          <Field label='Reason (optional) — e.g. "deploy payment-api v2.34"'>
            <input value={reason}
              onChange={e => setReason(e.target.value)}
              style={{ width: '100%' }} />
          </Field>
          {error && (
            <div style={{
              padding: 8, borderRadius: 4, fontSize: 12,
              color: 'var(--err)', background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)',
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
