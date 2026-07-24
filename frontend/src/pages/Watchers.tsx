import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Button, Drawer, DrawerSection } from '@/components/ui';
import { useAuth } from '@/components/AuthProvider';
import { useAlertRules, useWatchersSummary, useWatcherHistory } from '@/lib/queries';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { AlertRule, WatcherSummaryEntry } from '@/lib/types';
import { fmtAgoNs, fmtDurShort, tsLong } from '@/lib/utils';
import { buildWatcherTimeline, summarizeWatcherHistory, type WatcherTimelineEntry } from '@/lib/watcherTimeline';
import { WatcherImportModal } from './alerts/WatcherImportModal';

// /watchers (v0.9.196) — dedicated surface for the imported ES
// Watcher fleet (~300 rules in prod; operator decision 2026-07-23).
// The Alerts page keeps its Watcher chip for the mixed rule list;
// THIS page is the operational home: per-watcher last-fire / 24h
// fire count / open-problem state at a glance, and a Kibana
// execution-history-style drawer (fire → notifications → resolve
// timeline + the definition panel) per row.
//
// Data shape (mockup watcher-events-mock.html, approved):
//   • list       — /api/alert-rules (existing endpoint, client-side
//                  metric=='watcher' filter; a few hundred rows) merged
//                  with /api/watchers/summary (ONE bulk rollup — never
//                  per-row history calls).
//   • drawer     — /api/watchers/{id}/history, fetched on OPEN only.
//   • deep link  — ?watcher=<ruleId> rides the URL (replace:true), so
//                  a copied link opens the same drawer. Read straight
//                  from searchParams — no local mirror, no sig-guard
//                  needed.
// Viewer role sees everything read-only; only the Import affordance
// is editor+.

type WatcherRow = AlertRule & {
  lastFire: number;
  fires24h: number;
  openNow: boolean;
  disabledReason?: string;
};

const EMPTY_SUMMARY: WatcherSummaryEntry = { lastFire: 0, fires24h: 0, openNow: false };

// watchIndices — the index pattern(s) from the stored watch
// definition for the drawer's definition panel. The stored JSON is
// normalized/valid (import path guarantees it); still guarded so a
// hand-edited row degrades to '—' rather than crashing the drawer.
function watchIndices(watcherJson?: string): string {
  try {
    const w = JSON.parse(watcherJson ?? '') as {
      input?: { search?: { request?: { indices?: unknown } } };
    };
    const idx = w?.input?.search?.request?.indices;
    return Array.isArray(idx) ? idx.join(', ') : '';
  } catch {
    return '';
  }
}

export default function WatchersPage() {
  const { user } = useAuth();
  const canEdit = user?.role === 'admin' || user?.role === 'editor';
  const [showImport, setShowImport] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const selectedId = searchParams.get('watcher');
  const openWatcher = (id: string | null) =>
    setSearchParams(prev => {
      const p = new URLSearchParams(prev); // preserve foreign params
      if (id) p.set('watcher', id); else p.delete('watcher');
      return p;
    }, { replace: true });

  const rulesQ = useAlertRules();
  const summaryQ = useWatchersSummary();

  // Merge rules × summary into stable row objects so sortValue
  // accessors stay trivial. Summary may lag the rules list (separate
  // caches) — missing entries zero-fill.
  const rows: WatcherRow[] | undefined = useMemo(() => {
    if (rulesQ.isLoading) return undefined;
    const summary = summaryQ.data ?? {};
    return (rulesQ.data ?? [])
      .filter(r => r.metric === 'watcher')
      .map(r => ({ ...r, ...(summary[r.id] ?? EMPTY_SUMMARY) }));
  }, [rulesQ.isLoading, rulesQ.data, summaryQ.data]);

  const cols = useMemo<DataTableColumn<WatcherRow>[]>(() => [
    { id: 'name',      label: 'Name',        sortValue: r => r.name,      naturalDir: 'asc',  width: 240 },
    { id: 'condition', label: 'Condition',   sortValue: r => r.threshold, naturalDir: 'desc', width: 190 },
    { id: 'schedule',  label: 'Schedule',    sortValue: r => r.windowSec, naturalDir: 'asc',  width: 100 },
    { id: 'status',    label: 'Status',      sortValue: r => (r.enabled ? 1 : 0), naturalDir: 'desc', width: 90 },
    { id: 'lastFire',  label: 'Last fire',   sortValue: r => r.lastFire,  naturalDir: 'desc', width: 150 },
    { id: 'fires24h',  label: 'Fires (24h)', sortValue: r => r.fires24h,  naturalDir: 'desc', numeric: true, width: 100 },
  ], []);
  const dt = useDataTable<WatcherRow>({
    storageKey: 'watchers', columns: cols,
    rows: rows ?? [], initialSort: { id: 'lastFire', dir: 'desc' },
  });

  const selected = selectedId ? rows?.find(r => r.id === selectedId) : undefined;
  const failed = rulesQ.isError;

  return (
    <>
      <Topbar title="Watchers" />
      <div id="content">
        <div className="controls" style={{ marginBottom: 14 }}>
          <span style={{ color: 'var(--text2)', fontSize: 12 }}>
            Imported ES Watcher definitions, evaluated on their own schedule against the
            log backend. Click a row for its fire / notification / resolve history.
          </span>
          {canEdit && (
            <Button variant="secondary" onClick={() => setShowImport(true)}
              style={{ marginLeft: 'auto' }}
              title="Paste an Elasticsearch Watcher definition (PUT _watcher/watch body) and import it as an alert rule">
              ⤓ Import ES watcher
            </Button>
          )}
        </div>

        {showImport && (
          <WatcherImportModal
            onClose={() => setShowImport(false)}
            onImported={() => rulesQ.refetch()} />
        )}

        {rows === undefined && !failed && <Spinner />}
        {failed && <Empty icon="⚠" title="Failed to load watchers" />}
        {/* v0.9.196 review-fix: yükleme HATASINDA "No watchers yet" +
            Import CTA'sı basılmaz — hata boş-filo gibi sunulmaz. */}
        {rows && rows.length === 0 && !failed && (
          <Empty icon="👁" title="No watchers yet"
            action={canEdit
              ? <Button variant="primary" onClick={() => setShowImport(true)}>⤓ Import ES watcher</Button>
              : undefined}>
            Import your Elasticsearch Watcher definitions (the exact PUT _watcher/watch
            body) and Coremetry evaluates them on their own schedule — fires open
            Problems and route through the notification channels.
          </Empty>
        )}
        {rows && rows.length > 0 && (
          <div className="table-wrap">
            {/* v0.9.196 review-fix: summary rollup hatası sessiz sıfır-dolgu
                olarak sunulmaz — sütunların güvenilmez olduğu söylenir. */}
            {summaryQ.isError && (
              <div style={{
                padding: '6px 10px', marginBottom: 8, fontSize: 12,
                color: 'var(--warn)', border: '1px solid var(--warn)',
                borderRadius: 6, background: 'rgba(198,144,38,.08)',
              }}>
                ⚠ Summary rollup unavailable — "Last fire" / "Fires (24h)" columns may be stale or empty.
              </div>
            )}
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} />
              <DataTableHead dt={dt} />
              <tbody>
                {dt.sortedRows.map(r => (
                  <tr key={r.id}
                    onClick={() => openWatcher(r.id)}
                    style={{ cursor: 'pointer', contentVisibility: 'auto', containIntrinsicSize: 'auto 38px' }}
                    title="Open fire / notification / resolve history">
                    <td><b>{r.name}</b></td>
                    <td className="mono" style={{ fontSize: 12 }} title={r.watcherJson ? 'Imported ES watch — condition projected from the stored definition' : undefined}>
                      hits.total {r.comparator} {r.threshold}
                    </td>
                    <td style={{ fontSize: 12 }}>{fmtDurShort(r.windowSec)}</td>
                    <td>
                      {r.enabled
                        ? <span className="badge b-ok">ON</span>
                        : <span className="badge b-gray"
                            title={r.disabledReason || 'Disabled by operator'}>OFF</span>}
                    </td>
                    <td style={{ fontSize: 12, whiteSpace: 'nowrap' }}
                        title={r.lastFire ? tsLong(r.lastFire) : undefined}>
                      {r.lastFire ? fmtAgoNs(r.lastFire) : <span style={{ color: 'var(--text3)' }}>—</span>}
                      {r.openNow && <span className="badge b-err" style={{ marginLeft: 6 }}>OPEN</span>}
                    </td>
                    <td className="num mono" style={{ fontSize: 12 }}>
                      {r.fires24h > 0 ? r.fires24h : <span style={{ color: 'var(--text3)' }}>0</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {selected && (
          <WatcherHistoryDrawer watcher={selected} onClose={() => openWatcher(null)} />
        )}
      </div>
    </>
  );
}

// ── History drawer (mockup step 2) ──────────────────────────────────
// Fire → notification → resolve timeline of ONE watcher, with the
// definition panel above it. History fetches on open only.

function WatcherHistoryDrawer({ watcher, onClose }: {
  watcher: WatcherRow;
  onClose: () => void;
}) {
  const historyQ = useWatcherHistory(watcher.id);
  const history = historyQ.isPending ? undefined : historyQ.isError ? null : historyQ.data;

  const timeline: WatcherTimelineEntry[] = useMemo(
    () => history ? buildWatcherTimeline(history.problems, history.notifications) : [],
    [history]);
  const sum = useMemo(
    () => history
      ? summarizeWatcherHistory(history.problems, history.notifications, Date.now() * 1e6)
      : null,
    [history]);

  const indices = watchIndices(watcher.watcherJson);

  return (
    <Drawer onClose={onClose} width={640}
      header={
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
          <b style={{ fontSize: 14, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {watcher.name}
          </b>
          <span className="badge b-watcher">ES WATCHER</span>
          {watcher.enabled
            ? <span className="badge b-ok">ON</span>
            : <span className="badge b-gray" title={watcher.disabledReason || 'Disabled by operator'}>OFF</span>}
          {watcher.openNow && <span className="badge b-err">OPEN</span>}
        </div>
      }>
      {/* Summary strip — mockup's .sum row. v0.9.196 review-fix: 24h
          fire sayısı + son fire, satırın SUNUCU rollup'ından gelir
          (/api/watchers/summary — kırpılmamış 24h penceresi); history
          dilimi LIMIT'li (50 problem / 100 bildirim) olduğundan flap'li
          bir watcher'da "24h" iddiasıyla çelişirdi. Bildirim sayısı
          dilim-tabanlı kalır ve "recent" diye etiketlenir. */}
      <div style={{
        display: 'flex', gap: 16, flexWrap: 'wrap', fontSize: 12,
        color: 'var(--text2)', padding: '8px 0 12px',
        borderBottom: '1px solid var(--border)', marginBottom: 14,
      }}>
        <span>Last 24h: <b style={{ color: 'var(--text)' }}>{watcher.fires24h} fires</b></span>
        <span>Recent: <b style={{ color: 'var(--text)' }}>{sum ? sum.notifs24h : '…'} notifications</b>
          {sum && sum.notifFails24h > 0 && <span style={{ color: 'var(--err)' }}> ({sum.notifFails24h} failed)</span>}
        </span>
        <span>Last fire: <b style={{ color: 'var(--text)' }}>
          {watcher.lastFire ? fmtAgoNs(watcher.lastFire) : 'never'}
        </b></span>
      </div>

      <DrawerSection title={`Definition — ${watcher.name}`}>
        <div style={{
          display: 'grid', gridTemplateColumns: '110px 1fr',
          gap: '4px 10px', fontSize: 12.5,
        }}>
          <span style={{ color: 'var(--text3)' }}>Condition</span>
          <span className="mono">hits.total {watcher.comparator} {watcher.threshold}</span>
          <span style={{ color: 'var(--text3)' }}>Schedule</span>
          <span>{fmtDurShort(watcher.windowSec)}<span style={{ color: 'var(--text3)' }}> (watch interval)</span></span>
          <span style={{ color: 'var(--text3)' }}>Index</span>
          <span className="mono">{indices || '—'}</span>
          <span style={{ color: 'var(--text3)' }}>Severity</span>
          <span>{watcher.severity}</span>
          <span style={{ color: 'var(--text3)' }}>Cooldown</span>
          <span>{watcher.cooldownSec ? fmtDurShort(watcher.cooldownSec) : '—'}</span>
          <span style={{ color: 'var(--text3)' }}>Status</span>
          <span>
            {watcher.enabled
              ? <span style={{ color: 'var(--ok)' }}>● enabled</span>
              : <span style={{ color: 'var(--text2)' }}>○ disabled{watcher.disabledReason ? ` — ${watcher.disabledReason}` : ''}</span>}
          </span>
        </div>
      </DrawerSection>

      <DrawerSection title="History — fire · notification · resolve">
        {history === undefined && <Spinner />}
        {history === null && <Empty icon="⚠" title="Failed to load history" />}
        {history && timeline.length === 0 && (
          <Empty icon="◇" title="No fires recorded">
            When this watch breaches its condition a Problem opens (fire), the
            notification channels dispatch, and the resolve lands here when the
            condition clears.
          </Empty>
        )}
        {timeline.length > 0 && <Timeline entries={timeline} />}
      </DrawerSection>
    </Drawer>
  );
}

// Timeline — the mockup's dotted vertical list. Colours: fire = err,
// notification = info, resolve = ok (legend order matches the dots).
function Timeline({ entries }: { entries: WatcherTimelineEntry[] }) {
  return (
    <>
      <ul style={{ listStyle: 'none', margin: 0, padding: 0, position: 'relative' }}>
        {entries.map((e, i) => (
          <li key={`${e.kind}-${e.ts}-${i}`}
            style={{
              position: 'relative', padding: '0 0 14px 26px',
              // Rows stay mounted for the copy-link case (short list, ≤150 rows)
              borderLeft: i === entries.length - 1 ? '2px solid transparent' : '2px solid var(--bg3)',
              marginLeft: 6,
            }}>
            <span style={{
              position: 'absolute', left: -7, top: 2, width: 12, height: 12,
              borderRadius: '50%', border: '2px solid var(--bg1)',
              background: e.kind === 'fire' ? 'var(--err)' : e.kind === 'resolve' ? 'var(--ok)' : 'var(--info)',
            }} />
            <TimelineBody e={e} />
          </li>
        ))}
      </ul>
      <div style={{ display: 'flex', gap: 14, marginTop: 4, color: 'var(--text3)', fontSize: 11 }}>
        <span><Dot c="var(--err)" /> fire</span>
        <span><Dot c="var(--info)" /> notification</span>
        <span><Dot c="var(--ok)" /> resolve</span>
      </div>
    </>
  );
}

function Dot({ c }: { c: string }) {
  return <span style={{
    display: 'inline-block', width: 8, height: 8, borderRadius: '50%',
    background: c, marginRight: 4,
  }} />;
}

function TimelineBody({ e }: { e: WatcherTimelineEntry }) {
  const ts = <span className="mono" style={{ color: 'var(--text3)', fontSize: 11, whiteSpace: 'nowrap' }}
    title={tsLong(e.ts)}>{fmtAgoNs(e.ts)}</span>;
  if (e.kind === 'fire') {
    return (
      <div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
          <b style={{ fontSize: 12.5 }}>Fire</b>{ts}
        </div>
        <div style={{ color: 'var(--text2)', fontSize: 12 }}>
          hits.total <span className="mono">{e.problem.value} (threshold {e.problem.threshold})</span>
          {' · '}
          <Link to={`/problems?problem=${encodeURIComponent(e.problem.id)}`}
            style={{ color: 'var(--accent2)' }}
            title="Open the problem this fire opened">
            problem ↗
          </Link>
        </div>
      </div>
    );
  }
  if (e.kind === 'resolve') {
    return (
      <div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
          <b style={{ fontSize: 12.5 }}>Resolve</b>{ts}
        </div>
        <div style={{ color: 'var(--text2)', fontSize: 12 }}>
          open for {fmtDurShort(e.openForSec)} · last value <span className="mono">{e.problem.value}</span>
        </div>
      </div>
    );
  }
  const n = e.entry;
  return (
    <div>
      <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <b style={{ fontSize: 12.5 }}>Notification</b>
        <span className="badge b-info">{n.channelKind}</span>
        {ts}
      </div>
      <div style={{ color: 'var(--text2)', fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis' }}>
        <span className="mono" title={n.target}>{n.target || n.channelName || '—'}</span>
        {' — '}
        {n.ok
          ? <span style={{ color: 'var(--ok)' }}>✓ sent</span>
          : <span style={{ color: 'var(--err)' }} title={n.error}>✗ failed</span>}
      </div>
    </div>
  );
}
