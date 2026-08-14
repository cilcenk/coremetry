import { useEffect, useState, type FormEvent } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { QueryError } from '@/components/QueryError';
import { readState } from '@/lib/readState';
import { Button, Modal, Stack, useConfirm } from '@/components/ui';
import { api, type MaintenanceWindow } from '@/lib/api';
import { Field, Row } from './shared';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { tsLong } from '@/lib/utils';
import { formatDateTime, parseDateTime } from '@/lib/rangePicker';

// v0.9.871 (tutarlılık denetimi MT7) — 30 günlük pencere listesi paylaşılan
// primitife geçti. Kolon kümesi/sıra/etiket AYNEN korundu.
//
// Durum rozetinin mantığı tek yere alındı: hem hücre hem sortValue buradan
// okuyor. Önceden `active`/`upcoming` satır içinde türetiliyordu ve sıralama
// diye bir şey yoktu; ikisini ayrı yazmak, rozetin gösterdiğiyle sıralamanın
// ayrışacağı klasik zemin.
type WindowStatus = 'disabled' | 'active' | 'upcoming' | 'past';

export function maintenanceStatus(w: MaintenanceWindow, nowNs: number): WindowStatus {
  if (w.disabled) return 'disabled';
  if (w.startAt <= nowNs && nowNs <= w.endAt) return 'active';
  if (w.startAt > nowNs) return 'upcoming';
  return 'past';
}

// Aciliyet sırası: şu an susturan pencere en üstte.
const STATUS_RANK: Record<WindowStatus, number> = { active: 3, upcoming: 2, past: 1, disabled: 0 };

// Kolonlar MODÜL kapsamında: `now` her render'da değişiyor ve kolonları
// ona bağlamak columnLayoutSig'i her render'da tazeleyip operatörün
// sürüklediği genişlikleri atardı. `now` yalnız sortValue'nun İÇİNDE,
// sıralama anında okunuyor.
const WINDOW_COLS: DataTableColumn<MaintenanceWindow>[] = [
  { id: 'service',  label: 'Service',  sortValue: w => w.service,        naturalDir: 'asc', width: 180 },
  { id: 'severity', label: 'Severity', sortValue: w => w.severity,       naturalDir: 'asc', width: 110 },
  { id: 'starts',   label: 'Starts',   sortValue: w => w.startAt,        width: 175 },
  { id: 'ends',     label: 'Ends',     sortValue: w => w.endAt,          width: 175 },
  { id: 'reason',   label: 'Reason',   sortValue: w => w.reason ?? '',   naturalDir: 'asc', flex: true },
  { id: 'by',       label: 'By',       sortValue: w => w.createdBy ?? '', naturalDir: 'asc', width: 150 },
  { id: 'status',   label: 'Status',
    sortValue: w => STATUS_RANK[maintenanceStatus(w, Date.now() * 1e6)], width: 110 },
];

// ── Maintenance windows tab ────────────────────────────────────────────────
//
// Operator-declared time ranges that suppress alert
// notifications for matching (service, severity) tuples.
// Problems still open + auto-resolve as usual — only the
// live channel fan-out (Slack / email / Zoom / etc.) is
// skipped. After the window expires the /anomalies +
// /incidents pages still show the full timeline.

export function MaintenanceTab() {
  const confirm = useConfirm();
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
    if (!await confirm({
      title: 'Bakım penceresi silinsin mi?',
      body: <>Pencere kaldırılacak ve bastırılan uyarılar <b>ANINDA</b>
        yeniden ateşlemeye başlayacak.</>,
      confirmLabel: 'Pencereyi sil',
      danger: true,
    })) return;
    try {
      await api.deleteMaintenanceWindow(id);
      setMsg({ kind: 'ok', text: 'Window removed' });
      load();
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : 'Delete failed' });
    }
  };

  const now = Date.now() * 1e6;
  const dt = useDataTable<MaintenanceWindow>({
    storageKey: 'settings-maintenance-windows', columns: WINDOW_COLS, rows: items ?? [],
    initialSort: { id: 'starts', dir: 'desc' },
  });
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
          background: msg.kind === 'ok' ? 'color-mix(in srgb, var(--ok) 10%, transparent)' : 'color-mix(in srgb, var(--err) 8%, transparent)',
          border: `1px solid ${msg.kind === 'ok' ? 'color-mix(in srgb, var(--ok) 35%, transparent)' : 'color-mix(in srgb, var(--err) 30%, transparent)'}`,
        }}>{msg.text}</div>
      )}
      {readState(items) === 'loading' && <Spinner />}
      {/* v0.9.865 (tutarlılık denetimi MT1) — `!items` null için de doğru
          olduğundan okuma hatası "No maintenance windows" oluyordu. En pahalı
          yanlış-boş: operatör aktif bir bakım penceresi olmadığını sanıp
          deploy'a giriyor, susturulacak alarmlar fan-out ediyor. */}
      {readState(items) === 'error' && (
        <QueryError onRetry={load}>
          Maintenance windows could not be loaded — this is a failed read, not
          an empty list. An active window may still be suppressing alerts.
        </QueryError>
      )}
      {readState(items) === 'empty' && (
        <Empty icon="◯" title="No maintenance windows">
          Declare a window before a planned deploy to silence alerts on the
          affected services. They auto-expire — no clean-up needed.
        </Empty>
      )}
      {items && items.length > 0 && (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} trailing={[150]} />
            <DataTableHead dt={dt} trailing={<th style={{ textAlign: 'right' }}>Actions</th>} />
            <tbody>
              {dt.sortedRows.map(w => {
                const status = maintenanceStatus(w, now);
                return (
                  <tr key={w.id}>
                    <td style={{ fontFamily: 'monospace', fontWeight: 600 }}>{w.service}</td>
                    <td className="mono" style={{ fontSize: 11, textTransform: 'uppercase' }}>{w.severity}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{tsLong(w.startAt)}</td>
                    <td className="mono" style={{ fontSize: 11 }}>{tsLong(w.endAt)}</td>
                    <td style={{ fontSize: 12, color: 'var(--text2)' }}>{w.reason || '—'}</td>
                    <td style={{ fontSize: 11, color: 'var(--text3)', fontFamily: 'monospace' }}>{w.createdBy || '—'}</td>
                    <td>
                      {status === 'disabled' ? <span className="badge b-err" style={{ fontSize: 9 }}>DISABLED</span>
                        : status === 'active'   ? <span className="badge b-warn" style={{ fontSize: 9 }}>ACTIVE</span>
                        : status === 'upcoming' ? <span className="badge b-info" style={{ fontSize: 9 }}>UPCOMING</span>
                        :                         <span className="badge b-ok" style={{ fontSize: 9 }}>PAST</span>}
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
  // Default to "right now → +60 min", local zone.
  // v0.9.879 — these were `<input type="datetime-local">`, whose time half
  // renders with an AM/PM segment on an en-US browser; the operator wants
  // 24-hour everywhere and `lang` is not a reliable override in Chrome.
  // Hand-drawn text fields now, on the SAME "YYYY-MM-DD HH:mm:ss" grammar
  // (and the same parser) as the global time picker's absolute inputs.
  // Trade accepted per brief: no native drop-down calendar here.
  const [startAt, setStartAt] = useState(() => formatDateTime(Date.now()));
  const [endAt, setEndAt] = useState(() => formatDateTime(Date.now() + 60 * 60_000));
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setError(null);
    try {
      const startMs = parseDateTime(startAt);
      const endMs = parseDateTime(endAt);
      if (startMs === null || endMs === null) {
        throw new Error('Invalid date — expected YYYY-MM-DD HH:mm:ss');
      }
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
          <Button variant="primary" type="submit" form="new-mw-form" loading={busy}>Create</Button>
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
              <input type="text" required value={startAt} spellCheck={false}
                placeholder="2026-08-10 14:00:00" inputMode="numeric"
                onChange={e => setStartAt(e.target.value)}
                style={{
                  width: '100%', fontFamily: 'monospace',
                  ...(parseDateTime(startAt) === null ? { borderColor: 'var(--err)' } : {}),
                }} />
            </Field>
            <Field label="Ends at" flex={1}>
              <input type="text" required value={endAt} spellCheck={false}
                placeholder="2026-08-10 15:00:00" inputMode="numeric"
                onChange={e => setEndAt(e.target.value)}
                style={{
                  width: '100%', fontFamily: 'monospace',
                  ...(parseDateTime(endAt) === null ? { borderColor: 'var(--err)' } : {}),
                }} />
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
              color: 'var(--err)', background: 'color-mix(in srgb, var(--err) 8%, transparent)',
              border: '1px solid color-mix(in srgb, var(--err) 30%, transparent)',
            }}>{error}</div>
          )}
        </Stack>
      </form>
    </Modal>
  );
}
