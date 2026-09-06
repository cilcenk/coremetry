import { useMemo } from 'react';
import { rowActivation } from '@/lib/a11y'; // v0.10.455 (dış denetim D3 dilim 3)
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import { Sparkline } from '@/components/Sparkline';
import { Spinner, Empty } from '@/components/Spinner';
import type { DataTableColumn } from '@/lib/dataTable';
import type { OperationSummary, DBQueryStat, TimeRange } from '@/lib/types';
import { operationTracesHref } from '@/lib/pivotHref';
import { tracesURL } from '@/components/DBQueriesPanel';
import { databasesFilterHref } from '@/pages/databases/databaseParam';
import { serviceHref } from '@/lib/serviceHref';

// Service Overview tables (v0.7.96) — the compact Operations + Top DB
// statements pair from the design handoff. Both use the shared
// useDataTable primitive (sortable + resizable). Operations comes from the
// already-fetched service bundle; DB statements fetch once here.

function errBadge(rate: number): string {
  return `badge ${rate > 5 ? 'b-err' : rate > 1 ? 'b-warn' : 'b-ok'}`;
}

// ── Operations (compact) ────────────────────────────────────────────────
const OP_COLS: DataTableColumn<OperationSummary>[] = [
  { id: 'name', label: 'Operation', sortValue: r => r.name, naturalDir: 'asc', width: 280 },
  { id: 'calls', label: 'Calls', sortValue: r => r.spanCount, numeric: true, width: 80 },
  { id: 'err', label: 'Err %', sortValue: r => r.errorRate, numeric: true, width: 76 },
  { id: 'p99', label: 'P99', sortValue: r => r.p99DurationMs, numeric: true, width: 80 },
  { id: 'trend', label: 'Trend', width: 96 },
];

// overviewOpHref — v0.9.861 (UX denetimi Ö6). Overview'un Operations
// kartından /traces'e operasyon pivotu.
//
// Öncesi: `search=<op>` — serbest metin. `search` trace'in HERHANGİ bir
// span'ında eşleşir ve bu satır KÖK operasyonu gösterdiğinden listeye başka
// operasyonların trace'leri de düşüyordu. Operatör "bu operasyonun
// trace'leri" sanıp yanlış teşhis kuruyordu.
//
// Aynı hata v0.8.488'de operatör raporuyla Operations TABLOSUNDA
// düzeltilmişti; bu kart kalan yarısıydı — iki yüzey aynı hedefe gittiğini
// söylerken farklı sorular soruyordu. Kesin isim filtresi tek üreticiden.
//
// Modül seviyesinde: bileşenin içinde kalsaydı düzeltme test edilemezdi.
export function overviewOpHref(service: string, range: TimeRange, op: string): string {
  return operationTracesHref({ window: range, operation: op, service });
}

export function OpsCard({ service, range, operations }: {
  service: string; range: TimeRange; operations: OperationSummary[];
}) {
  const navigate = useNavigate();
  // Drill an operation into /traces (service + name pre-filtered), the same
  // destination the full Operations tab uses. onOpen also lights up j/k/Enter
  // keyboard nav (UX#4); no searchRef here — the compact card has no filter
  // input of its own.
  const opHref = (op: string) => overviewOpHref(service, range, op);
  const dt = useDataTable<OperationSummary>({
    storageKey: 'svc-ov-ops',
    columns: OP_COLS,
    rows: operations,
    initialSort: { id: 'calls', dir: 'desc' },
    onOpen: (op) => navigate(opHref(op.name)),
  });
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>Operations</h3>
        <span className="ov-right">
          {/* v0.9.211 — carry &tab=operations. Without it the link resolves
              to the default tab, i.e. straight back to the Overview the
              operator clicked "View all" from. */}
          <Link className="ov-sub" to={serviceHref(service, { range, tab: 'operations' })}>
            View all {operations.length} →
          </Link>
        </span>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.slice(0, 8).map((r, i) => (
              <tr key={r.name} {...dt.rowProps(i)} style={{ cursor: 'pointer' }}
                  {...rowActivation(() => navigate(opHref(r.name)))}>
                <td><span className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'block' }} title={r.name}>{r.name}</span></td>
                <td className="num">{r.spanCount >= 1000 ? `${(r.spanCount / 1000).toFixed(1)}K` : r.spanCount}</td>
                <td className="num"><span className={errBadge(r.errorRate)}>{r.errorRate.toFixed(2)}%</span></td>
                <td className="num mono">{r.p99DurationMs.toFixed(0)} ms</td>
                <td><div style={{ width: 84, marginLeft: 'auto' }}><Sparkline values={r.sparkline ?? []} width={84} height={22} /></div></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ── Top DB statements (compact) ─────────────────────────────────────────
const DB_COLS: DataTableColumn<DBQueryStat>[] = [
  { id: 'stmt', label: 'Statement', sortValue: r => r.statement, naturalDir: 'asc', width: 240 },
  { id: 'calls', label: 'Calls', sortValue: r => r.count, numeric: true, width: 80 },
  { id: 'p99', label: 'P99', sortValue: r => r.p99Ms, numeric: true, width: 80 },
  { id: 'time', label: 'Time/req', sortValue: r => r.avgMs, numeric: true, width: 120 },
];

export function DbCard({ service, range, from, to }: { service: string; range: TimeRange; from: number; to: number }) {
  const dbQ = useQuery({
    queryKey: ['service-overview-db', service, from, to],
    queryFn: () => api.serviceDBQueries(service, { from, to, limit: 50 }),
    enabled: !!service,
    staleTime: 30_000,
  });
  const rows = useMemo(() => dbQ.data ?? [], [dbQ.data]);
  const maxTime = useMemo(() => Math.max(1, ...rows.map(r => r.avgMs)), [rows]);
  // v0.9.960 (UX denetimi G1/K12) — kart TAMAMEN ÇIKIŞSIZDI: satır tıkı
  // yok, başlıkta "View all" yok. Operatör servisinin en pahalı DB
  // ifadesini görüyor ve derinleşmek için sidebar → /databases → ifadeyi
  // filo katalogda GÖZLE yeniden bulmak zorunda kalıyordu. Hemen
  // yanındaki Operations kartı ikisini de veriyor, yani kart "bozuk"
  // okunuyordu.
  //
  // Satır hedefi DBQueriesPanel'in tracesURL'i — YENİ bir üretici
  // yazılmadı: aynı satır tipinden aynı soruyu soran bir link zaten
  // vardı (LIKE deseniyle sınıfın TÜM literal varyantları + view=list +
  // rootOnly=false; db span'leri hiçbir zaman kök değildir). İkinci bir
  // üretici iki yüzeyin sessizce farklı sorular sorması demekti.
  const navigate = useNavigate();
  const dbHref = (r: DBQueryStat) => tracesURL(service, r, { fromNs: from, toNs: to });
  const dt = useDataTable<DBQueryStat>({
    storageKey: 'svc-ov-db',
    columns: DB_COLS,
    rows,
    initialSort: { id: 'time', dir: 'desc' },
    // onOpen j/k/Enter klavye gezinmesini de açar (OpsCard emsali).
    onOpen: (r) => navigate(dbHref(r)),
  });
  return (
    <div className="card">
      <div className="ov-card-h">
        <h3>Top DB statements</h3>
        {/* Kartın kendi sayfa-içi devamı: Details sekmesindeki tam
            Database bölümü (DetailsToc'un 'dtl-db' çapası). tab=details
            ŞART — çapa tek başına varsayılan sekmede çözülür ve operatör
            "View all"a bastığı Overview'a geri döner (v0.9.211'in dersi). */}
        {rows.length > 0 && (
          <span className="ov-right">
            <Link className="ov-sub"
              to={serviceHref(service, { range, tab: 'details', hash: 'dtl-db' })}>
              View all {rows.length} →
            </Link>
          </span>
        )}
      </div>
      {dbQ.isLoading ? (
        <div className="ov-card-b" style={{ display: 'grid', placeItems: 'center', padding: 16 }}><Spinner /></div>
      ) : dbQ.isError ? (
        <div className="ov-card-b" style={{ color: 'var(--err)', fontSize: 13 }}>
          Failed to load DB statements.
        </div>
      ) : rows.length === 0 ? (
        <div className="ov-card-b">
          <Empty compact icon="◯" title={`No db.statement spans for ${service} in this window`} />
        </div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.slice(0, 8).map((r, i) => (
                <tr key={i} {...dt.rowProps(i)} style={{ cursor: 'pointer' }}
                    {...rowActivation(() => navigate(dbHref(r)))}>
                  <td>
                    <div className="mono" style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.sampleStatement || r.statement}>{r.statement}</div>
                    {/* v0.9.964 (UX denetimi Ö9 / G2) — engine sub-label is
                        the bridge into the database catalogue; the row
                        itself keeps going to /traces (v0.9.960), so the
                        click has to stop here. */}
                    <div className="ov-st">
                      {r.dbSystem ? (
                        <Link to={databasesFilterHref(r, { range: encodeRange(range) })}
                          onClick={e => e.stopPropagation()}
                          title={`Open the database catalogue filtered to ${r.dbSystem}`}
                          style={{ color: 'inherit', textDecoration: 'none', borderBottom: '1px dotted var(--text3)' }}>
                          {r.dbSystem}
                        </Link>
                      ) : '—'}
                    </div>
                  </td>
                  <td className="num">{r.count >= 1000 ? `${(r.count / 1000).toFixed(1)}K` : r.count}</td>
                  <td className="num mono">{r.p99Ms.toFixed(0)} ms</td>
                  <td>
                    <div className="ov-barcell">
                      <span className="mono" style={{ minWidth: 52 }}>{r.avgMs.toFixed(1)} ms</span>
                      <span className="ov-minibar"><i style={{ width: `${(r.avgMs / maxTime) * 100}%`, background: r.errorCount > 0 ? 'var(--warn)' : 'var(--teal)' }} /></span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
