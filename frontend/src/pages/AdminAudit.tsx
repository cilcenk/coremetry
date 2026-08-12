import { useState, useMemo } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { QueryError } from '@/components/QueryError';
import { readState } from '@/lib/readState';
import { Button, LinkButton } from '@/components/ui';
import { useAuth } from '@/components/AuthProvider';
import { useAuditLog } from '@/lib/queries';
import { tsLong, type GoDuration } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { AuditEntry } from '@/lib/types';
import { PageControls } from '@/components/ui/PageControls';

// Columns for the shared sortable + resizable DataTable (v0.7.53
// primitive). Default sort is Time desc — newest first, the
// implicit append-only ordering an audit log should land on. The
// Details column is non-sortable: it's a free-text JSON blob with
// no meaningful order key, and it doubles as the expand-toggle
// cell (click → pretty-printed JSON in place). Body cell order
// below MUST match this column order.
const AUDIT_COLS: DataTableColumn<AuditEntry>[] = [
  { id: 'time',    label: 'Time',    sortValue: e => e.time,       naturalDir: 'desc', width: 170 },
  { id: 'actor',   label: 'Actor',   sortValue: e => e.actorEmail, naturalDir: 'asc', width: 220 },
  { id: 'action',  label: 'Action',  sortValue: e => e.action,     naturalDir: 'asc', width: 200 },
  { id: 'target',  label: 'Target',  sortValue: e => e.targetKind, naturalDir: 'asc', width: 200 },
  // v0.9.648 — ESNEK kolon: serbest metin, artan genişliği emsin.
  // Sabit genişlik toplamı 1280px'di ve tablo taşıyordu; details flex
  // olunca sabit toplam 920px'e düşüyor ve tablo her genişlikte sığıyor.
  // Kolon SİLİNMEDİ, genişliği daraltılmadı — yalnız artanı emiyor.
  { id: 'details', label: 'Details', flex: true },
  { id: 'ip',      label: 'IP',      sortValue: e => e.ip,         naturalDir: 'asc', width: 130 },
];

// Curated catalog of every action the API currently emits via
// s.audit(). Kept in lock-step with internal/api/*; new entries
// added here as soon as a new audit call ships so the action
// dropdown is fully populated even when the current window is
// empty. Discovered values from the response are still merged
// at render time as a safety net.
const KNOWN_ACTIONS = [
  'alert_rule.create',
  'alert_rule.update',
  'alert_rule.delete',
  'alert_rule.enable',
  'anomaly_silence.bulk_delete',
  'anomaly_silence.create',
  'anomaly_silence.delete',
  'dashboard.create',
  'dashboard.update',
  'dashboard.delete',
  'notification_channel.create',
  'notification_channel.update',
  'notification_channel.delete',
  'problem.acknowledge',
  'saved_view.create',
  'saved_view.delete',
  'settings.ai.update',
  'settings.ldap.update',
  'settings.sampling.update',
  'settings.smtp.update',
  'sql.query',
  'user.create',
  'user.delete',
  'user.reset_password',
  'user.set_role',
  'user.set_team',
];

// /admin/audit shows the append-only audit_log: who did what, when.
// Admin-only — the API also enforces this server-side, but the SPA
// hides the page from non-admin sidebars.
export default function AuditPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [target, setTarget] = useState('');
  // v0.9.261 — GoDuration, and the 7d/30d option values are now hours.
  // Go's time.ParseDuration has no day unit, so `since=30d` failed to parse
  // and internal/api.parseDuration answered with its 24h default: the picker
  // said "Last 30d" while the table showed one day, with nothing admitting it.
  const [since, setSince] = useState<GoDuration>('24h');
  // v0.5.348 — free-text search across every visible column.
  // Filters client-side after the backend filter pass; lets
  // the operator "grep" within the current window without
  // forcing a full re-query.
  const [search, setSearch] = useState('');
  // v0.5.348 — expanded-details set. Click a Details cell to
  // toggle the row into a full pre-formatted JSON block. Set-
  // backed so a previously expanded row stays expanded on
  // re-render.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  // useAuditLog re-keys on every filter change so each
  // (actor, action, target, since) tuple caches separately;
  // tabbing back to a previously viewed filter is instant.
  const auditQ = useAuditLog(since, {
    actor: actor.trim() || undefined,
    action: action.trim() || undefined,
    target: target.trim() || undefined,
  });
  // v0.9.866 (tutarlılık denetimi MT1) — YASAK ≠ HATALI ≠ BOŞ. `!isAdmin`
  // burada null'a katlanıyordu, yani "yetkin yok" ile "okuma başarısız" aynı
  // değerdi; üstelik null hiçbir render dalına girmediği için hata durumu
  // BOMBOŞ EKRANDI. Yasak dalının kendi erken dönüşü zaten aşağıda (Admin
  // only), bu ifade artık yalnız okumanın üç durumunu taşıyor.
  const data = auditQ.isLoading ? undefined
    : auditQ.isError ? null
    : auditQ.data ?? [];

  // Seed the dropdown with every action the API currently emits
  // so an empty window doesn't hide a filter the operator is
  // looking for (e.g. "who acknowledged this morning's noise" —
  // if no acks landed in the default 24h slice, the option would
  // otherwise vanish). Discovered actions from the current page
  // get merged in case the backend ships a new one ahead of us.
  const distinctActions = useMemo(() => {
    const s = new Set<string>(KNOWN_ACTIONS);
    (data ?? []).forEach(e => s.add(e.action));
    return Array.from(s).sort();
  }, [data]);

  // Client-side search — case-insensitive substring across
  // actor / action / target / details / ip. Empty search = no
  // filtering, so the typical empty-input case is a no-op.
  const visible = useMemo(() => {
    const rows = data ?? [];
    const q = search.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter(e => {
      const hay = [
        e.actorEmail, e.actorRole, e.action,
        e.targetKind, e.targetId, e.details, e.ip,
      ].filter(Boolean).join(' ').toLowerCase();
      return hay.includes(q);
    });
  }, [data, search]);

  const toggleExpand = (id: string) => {
    setExpanded(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  // Shared sortable + resizable table. Fed the client-FILTERED
  // `visible` array so search narrows BEFORE the sort. Hook is
  // unconditional and lives ABOVE the admin early-return below
  // (rules-of-hooks). Default Time desc = newest first.
  const dt = useDataTable<AuditEntry>({
    storageKey: 'admin-audit',
    columns: AUDIT_COLS,
    rows: visible ?? [],
    initialSort: { id: 'time', dir: 'desc' },
  });

  if (!isAdmin) {
    return (
      <>
        <div><Empty icon="◇" title="Admin only">
          The audit log records every state-changing action by a
          user. Restricted to the admin role.
        </Empty></div>
      </>
    );
  }

  return (
    <>
      <div>
        <PageControls sticky style={{ marginBottom: 8 }}>
          <select value={since} onChange={e => setSince(e.target.value as GoDuration)} aria-label="Time range">
            <option value="1h">Last 1h</option>
            <option value="24h">Last 24h</option>
            <option value="168h">Last 7d</option>
            <option value="720h">Last 30d</option>
          </select>
          <input placeholder="Actor (email or id)…" aria-label="Filter by actor (email or id)"
            value={actor} onChange={e => setActor(e.target.value)} style={{ width: 220 }} />
          <select value={action} onChange={e => setAction(e.target.value)} aria-label="Filter by action">
            <option value="">All actions</option>
            {distinctActions.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
          <input placeholder="Target kind (e.g. alert_rule)" aria-label="Filter by target kind"
            value={target} onChange={e => setTarget(e.target.value)} style={{ width: 200 }} />
          <input placeholder="Search within results…" aria-label="Search within results"
            value={search} onChange={e => setSearch(e.target.value)} style={{ width: 220 }} />
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {search.trim() && data
              ? `${visible.length} / ${data.length} entries`
              : `${data?.length ?? 0} entries`}
          </span>
          {/* CSV export uses the same filter shape as the JSON view —
              just a different endpoint that streams to download.
              Anchor element with attributes built inline so the
              browser handles the download path without us juggling
              a Blob; auth cookies travel with the request. */}
          <a href={`/api/admin/audit/export?since=${encodeURIComponent(since)}`
            + (actor.trim() ? `&actor=${encodeURIComponent(actor.trim())}` : '')
            + (action.trim() ? `&action=${encodeURIComponent(action.trim())}` : '')
            + (target.trim() ? `&target=${encodeURIComponent(target.trim())}` : '')}
            className="sec"
            style={{ fontSize: 11, padding: '4px 10px', textDecoration: 'none' }}
            title="Download current view as CSV">
            ↓ Export CSV
          </a>
        </PageControls>

        {readState(data) === 'loading' && <Spinner />}
        {readState(data) === 'error' && (
          <QueryError onRetry={() => auditQ.refetch()}>
            The audit log could not be loaded — this is a failed read, not an
            empty range. Recorded actions are still there.
          </QueryError>
        )}
        {readState(data) === 'empty' && (
          <Empty icon="◇" title="No audit entries in this range" />
        )}
        {data && data.length > 0 && visible.length === 0 && (
          <Empty icon="◇" title="No entries match your search">
            <Button variant="secondary" onClick={() => setSearch('')}
              style={{ marginTop: 8 }}>
              Clear search
            </Button>
          </Empty>
        )}
        {data && visible.length > 0 && (
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(e => {
                  const isExpanded = expanded.has(e.id);
                  return (
                    // v0.9.137 (scale-audit 2026-07-20) — server caps to 200
                    // rows but that's above the 100-row content-visibility
                    // threshold (CLAUDE.md); skip off-screen row layout.
                    <tr key={e.id} style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 44px' }}>
                      <td className="mono" style={{ fontSize: 11, whiteSpace: 'nowrap' }}>
                        {tsLong(e.time)}
                      </td>
                      <td>
                        <div style={{ fontWeight: 600, fontSize: 12 }}>
                          {e.actorEmail
                            ? <FilterClick onClick={() => setActor(e.actorEmail)}>{e.actorEmail}</FilterClick>
                            : '—'}
                        </div>
                        <div style={{ fontSize: 10, color: 'var(--text3)' }}>{e.actorRole}</div>
                      </td>
                      <td className="mono" style={{ fontSize: 11 }}>
                        <FilterClick onClick={() => setAction(e.action)}>{e.action}</FilterClick>
                      </td>
                      <td className="mono" style={{ fontSize: 11 }}>
                        <FilterClick onClick={() => setTarget(e.targetKind)}>{e.targetKind}</FilterClick>
                        {e.targetId && <span style={{ color: 'var(--text3)' }}> · {e.targetId}</span>}
                      </td>
                      <td onClick={() => e.details && toggleExpand(e.id)}
                          onKeyDown={ev => {
                            // Keyboard parity with the click affordance —
                            // Enter/Space toggles the expanded JSON the
                            // same way a click does, when there are details.
                            if (e.details && (ev.key === 'Enter' || ev.key === ' ')) {
                              ev.preventDefault();
                              toggleExpand(e.id);
                            }
                          }}
                          role={e.details ? 'button' : undefined}
                          tabIndex={e.details ? 0 : undefined}
                          aria-expanded={e.details ? isExpanded : undefined}
                          style={{ fontSize: 11, color: 'var(--text2)',
                                   // Fixed layout governs the column width via
                                   // the colgroup; the global td ellipsis clips
                                   // the collapsed state. Expanded → wrap the
                                   // pretty-printed JSON within the (resizable)
                                   // column so the operator reads it inline.
                                   whiteSpace: isExpanded ? 'pre-wrap' : 'nowrap',
                                   overflowWrap: isExpanded ? 'anywhere' : undefined,
                                   fontFamily: 'monospace',
                                   cursor: e.details ? 'pointer' : 'default' }}
                          title={isExpanded ? 'Click to collapse' : (e.details || '')}>
                        {isExpanded
                          ? <span>{prettyJSON(e.details)}</span>
                          : (e.details || '—')}
                      </td>
                      <td className="mono" style={{ fontSize: 10, color: 'var(--text3)' }}>{e.ip || '—'}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}

// FilterClick wraps a cell value as a clickable affordance —
// click → pre-fill the matching filter input above. Same UX
// hint pattern Grafana / Datadog use on tag chips. Lets an
// operator iterate "who else did this" / "what else hit this
// target" without retyping.
function FilterClick({ children, onClick }: { children: React.ReactNode; onClick: () => void }) {
  return (
    <LinkButton onClick={onClick} underline="dotted"
      title="Click to filter by this value">
      {children}
    </LinkButton>
  );
}

// prettyJSON returns the input as a 2-space indented JSON
// string when it parses, otherwise the raw value. Used by the
// expanded details row so blob payloads ({appName, primaryColor,
// …}) get readable formatting without forcing the operator to
// paste into a separate JSON viewer.
function prettyJSON(s: string): string {
  if (!s) return '';
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
