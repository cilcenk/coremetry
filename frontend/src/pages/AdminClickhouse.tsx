import { useRef, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import { fmtNum, fmtBytes } from '@/lib/utils';
import { useClickhouseHealth, useCHCoordinators, useDDLQueueHealth, useRollupStatus } from '@/lib/queries';
import { useQuery } from '@tanstack/react-query';
import { makeBaseline, nodeWorkView, type Baseline, type NodeWorkRow } from '@/lib/chNodeWork';
import { Button, Modal } from '@/components/ui';
import type {
  RollupActionResult, RollupPreflightResult, RollupTableStatus, RollupTarget,
} from '@/lib/types';

// AdminClickhouse — v0.5.329. Datadog-style CH self-stats:
// slow queries, in-flight merges, part hotspots, replication lag.
// Reads from /api/admin/clickhouse (server caches 5s, polls every
// 10s here so the operator can watch merge pressure ease/spike in
// near-real-time). Pauses on document.hidden per CLAUDE.md.

type Slow = {
  query: string; elapsedMs: number; memoryMb: number;
  readRows: number; resultRows: number; eventTimeNs: number; user: string;
};
type Merge = {
  // host (v0.9.540) — merge'i koşturan node. 4 node'lu kümede "hangi
  // node merge'e boğulmuş" sorusunun cevabı; küme yoksa bağlı node.
  host: string;
  database: string; table: string;
  elapsedSec: number; progressPct: number;
  rowsRead: number; mergedSizeBytes: number;
};
type PartHot = {
  database: string; table: string;
  parts: number; rowsTotal: number; bytesTotal: number;
};
type RepLag = {
  database: string; table: string;
  queueSize: number; absoluteDelaySec: number;
};
type AsyncIns = {
  database: string; table: string;
  totalBytes: number; entriesCount: number;
  firstUpdateMsAgo: number;
};
type ClusterNode = {
  cluster: string; shardNum: number; replicaNum: number;
  hostName: string; hostAddress?: string; port: number; isLocal: boolean;
};
type Topology = {
  mode: 'cluster' | 'standalone';
  configuredCluster?: string;
  database: string;
  connectedHosts?: string[];
  nodes?: ClusterNode[];
  distributedTables: number;
  localReplicated: number;
  plainMergeTree: number;
  zookeeperConnected: boolean;
  // v0.5.419 — resolved per-table shard policy. Operator audits
  // which expression each Distributed wrapper actually got.
  shardPolicy?: Record<string, string>;
  // v0.5.428 — populated when the system.clusters probe itself
  // failed (timeout / disconnect). Empty when the probe completed
  // (whether or not it returned rows). Drives the soft "probe
  // failed" banner so the hard "misconfigured" banner only fires
  // on a genuine empty-result.
  clusterProbeError?: string;
  // v0.5.439 — set when the live probe failed but a recent
  // cached snapshot filled `nodes` in. Renders an inline "last
  // refreshed N min ago" pill instead of the warn banner so a
  // transient timeout doesn't flash a red box at the operator.
  // Age is the cache-snapshot age in milliseconds.
  clusterNodesStale?: boolean;
  clusterNodesAgeMs?: number;
};
// v0.6.22 — in-flight mutations panel. Healthy queue is empty;
// growing queue → time to swap the offending ALTER UPDATE/
// DELETE pattern for a tombstone or ReplacingMergeTree shape.
type Mutation = {
  database: string;
  table: string;
  command: string;
  parts: number;
  elapsedMs: number;
  latestFail?: string;
};

type CHHealth = {
  topology: Topology;
  slowQueries: Slow[] | null;
  merges: Merge[] | null;
  partHotspots: PartHot[] | null;
  replicationLag?: RepLag[] | null;
  asyncInserts?: AsyncIns[] | null;
  mutations?: Mutation[] | null;
  generatedAt: number;
};

// Shard-policy table iterates a Record<table, expr>; flatten to a
// row type so it can ride the shared sortable + resizable primitive.
type ShardPolicyRow = { table: string; expr: string };

// Column defs for the shared DataTable primitive. Body cell ORDER on
// each table must match its COLS order. Free-text columns (Query /
// Command / Failure / shard expr) rely on the global td ellipsis under
// table-layout:fixed — no per-cell maxWidth/nowrap needed.
const SLOW_COLS: DataTableColumn<Slow>[] = [
  { id: 'time',     label: 'Time',      sortValue: q => q.eventTimeNs, naturalDir: 'desc', width: 110 },
  { id: 'user',     label: 'User',      sortValue: q => q.user ?? '',  naturalDir: 'asc',  width: 120 },
  { id: 'elapsed',  label: 'Elapsed',   sortValue: q => q.elapsedMs, numeric: true, naturalDir: 'desc', width: 100 },
  { id: 'memory',   label: 'Memory',    sortValue: q => q.memoryMb,  numeric: true, naturalDir: 'desc', width: 100 },
  { id: 'readRows', label: 'Read rows', sortValue: q => q.readRows,  numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'query',    label: 'Query',     sortValue: q => q.query,     naturalDir: 'asc',  width: 540 },
];

const MERGE_COLS: DataTableColumn<Merge>[] = [
  { id: 'host',     label: 'Node',        sortValue: m => m.host,     naturalDir: 'asc',  width: 150 },
  { id: 'database', label: 'Database',    sortValue: m => m.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',       sortValue: m => m.table,    naturalDir: 'asc',  width: 200 },
  { id: 'elapsed',  label: 'Elapsed',     sortValue: m => m.elapsedSec,      numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'progress', label: 'Progress',    sortValue: m => m.progressPct,     numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'rowsRead', label: 'Rows read',   sortValue: m => m.rowsRead,        numeric: true, naturalDir: 'desc', width: 130 },
  { id: 'merged',   label: 'Merged size', sortValue: m => m.mergedSizeBytes, numeric: true, naturalDir: 'desc', width: 130 },
];

const PARTHOT_COLS: DataTableColumn<PartHot>[] = [
  { id: 'database', label: 'Database', sortValue: p => p.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',    sortValue: p => p.table,    naturalDir: 'asc',  width: 240 },
  { id: 'parts',    label: 'Parts',    sortValue: p => p.parts,      numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'rows',     label: 'Rows',     sortValue: p => p.rowsTotal,  numeric: true, naturalDir: 'desc', width: 140 },
  { id: 'bytes',    label: 'Bytes',    sortValue: p => p.bytesTotal, numeric: true, naturalDir: 'desc', width: 140 },
];

const ASYNC_COLS: DataTableColumn<AsyncIns>[] = [
  { id: 'database', label: 'Database',       sortValue: a => a.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',          sortValue: a => a.table,    naturalDir: 'asc',  width: 240 },
  { id: 'bytes',    label: 'Bytes buffered', sortValue: a => a.totalBytes,      numeric: true, naturalDir: 'desc', width: 150 },
  { id: 'entries',  label: 'Entries',        sortValue: a => a.entriesCount,    numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'oldest',   label: 'Oldest',         sortValue: a => a.firstUpdateMsAgo, numeric: true, naturalDir: 'desc', width: 110 },
];

const MUTATION_COLS: DataTableColumn<Mutation>[] = [
  { id: 'table',   label: 'Table',          sortValue: m => `${m.database}.${m.table}`, naturalDir: 'asc',  width: 240 },
  { id: 'parts',   label: 'Parts left',     sortValue: m => m.parts,     numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'elapsed', label: 'Elapsed',        sortValue: m => m.elapsedMs, numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'command', label: 'Command',        sortValue: m => m.command,   naturalDir: 'asc', width: 360 },
  { id: 'failure', label: 'Latest failure', sortValue: m => m.latestFail ?? '', naturalDir: 'asc', width: 240 },
];

const REPLAG_COLS: DataTableColumn<RepLag>[] = [
  { id: 'database', label: 'Database',       sortValue: r => r.database, naturalDir: 'asc',  width: 160 },
  { id: 'table',    label: 'Table',          sortValue: r => r.table,    naturalDir: 'asc',  width: 240 },
  { id: 'queue',    label: 'Queue',          sortValue: r => r.queueSize,        numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'delay',    label: 'Absolute delay', sortValue: r => r.absoluteDelaySec, numeric: true, naturalDir: 'desc', width: 140 },
];

const NODE_COLS: DataTableColumn<ClusterNode>[] = [
  { id: 'shard',   label: 'Shard',   sortValue: n => n.shardNum,   numeric: true, naturalDir: 'asc', width: 90 },
  { id: 'replica', label: 'Replica', sortValue: n => n.replicaNum, numeric: true, naturalDir: 'asc', width: 90 },
  { id: 'host',    label: 'Host',    sortValue: n => n.hostName,        naturalDir: 'asc', width: 220 },
  { id: 'address', label: 'Address', sortValue: n => n.hostAddress ?? '', naturalDir: 'asc', width: 200 },
  { id: 'port',    label: 'Port',    sortValue: n => n.port, numeric: true, naturalDir: 'asc', width: 90 },
  { id: 'local',   label: 'Local',   sortValue: n => (n.isLocal ? 1 : 0), numeric: false, naturalDir: 'desc', width: 90 },
];

const SHARD_POLICY_COLS: DataTableColumn<ShardPolicyRow>[] = [
  { id: 'table', label: 'Table',            sortValue: r => r.table, naturalDir: 'asc', width: 240 },
  { id: 'expr',  label: 'Shard expression', sortValue: r => r.expr,  naturalDir: 'asc', width: 400 },
];

// v0.9.494 — koordinatör dağılımı. Satır = bir CH node'u, sayılar o
// node'un GİRİŞ NOKTASI olduğu sorgular (is_initial_query=1). Shard
// olarak yaptığı alt-sorgular buraya girmez — panel taramanın değil,
// koordinasyon yükünün dağılımını ölçer.
type Coord = {
  host: string; initial: number; selects: number; inserts: number; other: number;
  readRows: number; memoryMB: number; p50Ms: number; p95Ms: number;
  uptimeS?: number;
};

const COORD_COLS: DataTableColumn<Coord>[] = [
  // v0.9.649 — ESNEK: host adı değişken uzunlukta, artanı emer.
  { id: 'host',    label: 'Host',      sortValue: c => c.host,     naturalDir: 'asc', flex: true },
  // Entry = bu node'un GİRİŞ NOKTASI olduğu sorgu sayısı. Panelin asıl
  // rakamı: shard alt-sorguları buna girmez, yani saf koordinatör yükü.
  { id: 'initial', label: 'Entry',     sortValue: c => c.initial,  numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'selects', label: 'SELECT',    sortValue: c => c.selects,  numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'inserts', label: 'INSERT',    sortValue: c => c.inserts,  numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'other',   label: 'Other',     sortValue: c => c.other,    numeric: true, naturalDir: 'desc', width: 90 },
  { id: 'rows',    label: 'Read rows', sortValue: c => c.readRows, numeric: true, naturalDir: 'desc', width: 130 },
  { id: 'mem',     label: 'Memory',    sortValue: c => c.memoryMB, numeric: true, naturalDir: 'desc', width: 110 },
  { id: 'p50',     label: 'SELECT p50', sortValue: c => c.p50Ms,   numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'p95',     label: 'SELECT p95', sortValue: c => c.p95Ms,   numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'uptime',  label: 'Uptime',     sortValue: c => c.uptimeS ?? 0, numeric: true, naturalDir: 'desc', width: 100 },
];

// Kümülatif sayaç yolunda uptime OKUNMAK ZORUNDA: yeni restart etmiş bir
// node düşük sayı gösterir ve dengesizlik olduğundan büyük görünür.
function fmtUptime(s?: number): string {
  if (!s) return '—';
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600);
  return d > 0 ? `${d}g ${h}s` : `${h}s`;
}

// Dengesizlik = maxNode / ortalamaNode. 1.00 kusursuz; N node'da tek
// node her şeyi alıyorsa N'e yaklaşır. Eşikler kaba ama operatörün
// "bakmam lazım mı?" sorusunu tek renkte cevaplıyor.
function imbalanceTone(v: number): string {
  if (v <= 1.25) return 'b-ok';
  if (v <= 2) return 'b-warn';
  return 'b-err';
}

const COORD_WINDOWS: Array<{ s: number; label: string }> = [
  { s: 3600,  label: 'Last 1h' },
  { s: 21600, label: 'Last 6h' },
  { s: 86400, label: 'Last 24h' },
];

// NodeWorkPanel — v0.9.543. "Hangi node ne yapıyor, ve CPU'su neden
// diğerlerinden yüksek?"
//
// Operatör soruşturması (prod, 2026-08-02): dbp01'in CPU'su 6 saattir
// 2-3x. Elle koşulan sorgularla dört hipotez öldü ve merge ORANSIZ
// çıktı (merge 1.9x iken CPU 2.76x); eşleşen tek sinyal insert oldu
// (2.93x). Bu panel o ölçümü kalıcı hale getiriyor.
//
// Dürüstlük sözleşmesi (lib/chNodeWork'te pinli): restart eden node
// "0 iş" göstermez, ölçülemeyen "dengeli" sayılmaz, ve dengesizlik
// SHARD İÇİNDE hesaplanır — aynı shard'ın replikaları aynı veriyi
// tutar, farklı shard'lar tanım gereği farklı.
function NodeWorkPanel() {
  const q = useQuery({
    queryKey: ['ch-nodework'],
    queryFn: () => api.chNodeWork(),
    refetchInterval: 30_000, staleTime: 25_000,
  });
  const raw = q.isPending ? undefined : q.isError ? null : q.data ?? null;

  const baseRef = useRef<Baseline | null>(null);
  const [, bump] = useState(0);
  if (raw && raw.nodes.length > 0 && !baseRef.current) {
    baseRef.current = makeBaseline(raw.nodes, raw.generatedAt);
  }
  const view = raw ? nodeWorkView(raw.nodes, baseRef.current, raw.generatedAt) : null;

  if (raw === undefined) return <Section title="Node work spread"><Spinner /></Section>;
  if (raw === null) {
    return <Section title="Node work spread">
      <Empty icon="⚠" title="Ölçüm okunamadı">system.events sorgusu başarısız.</Empty>
    </Section>;
  }

  const fmt = (v: number | null, d = 2) => v == null ? '—' : v.toFixed(d);
  const fmtInt = (v: number | null) => v == null ? '—' : v.toLocaleString();

  return (
    <Section title="Node work spread">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
        <span className="badge b-info" title={
          'Sayaçlar sunucu açılışından beri kümülatif; panel ilk okumayı taban alıp FARKI ' +
          'gösteriyor. Açık kaldıkça pencere büyür ve okuma keskinleşir.'}>
          delta · {view && view.elapsedMs >= 1000
            ? (view.elapsedMs < 60_000
              ? `${Math.round(view.elapsedMs / 1000)}sn`
              : `${Math.round(view.elapsedMs / 60_000)}dk`)
            : 'taban alındı'}
        </span>
        <button type="button" onClick={() => {
          if (raw) { baseRef.current = makeBaseline(raw.nodes, raw.generatedAt); bump(n => n + 1); }
        }} style={{
          background: 'transparent', color: 'var(--accent2)', border: '1px solid var(--border)',
          borderRadius: 6, fontSize: 11, padding: '3px 8px', cursor: 'pointer',
        }}>tabanı sıfırla</button>
        {/* Dengesizlik rozetleri YALNIZ ölçüm varken. null → '—' + gri:
            "ölçülemedi" ile "dengeli" aynı şey değil. */}
        {view?.cpuImbalance != null && (
          <span className={`badge ${imbalanceTone(view.cpuImbalance)}`}
            title="Shard İÇİ en yüksek CPU dengesizliği (max/ortalama).">
            CPU spread ×{view.cpuImbalance.toFixed(2)}
          </span>
        )}
        {view?.insertImbalance != null && (
          <span className={`badge ${imbalanceTone(view.insertImbalance)}`}
            title="Shard İÇİ en yüksek insert dengesizliği. CPU ile AYNI yönde ise sebep yazma dağılımıdır.">
            Insert spread ×{view.insertImbalance.toFixed(2)}
          </span>
        )}
        {view && !view.measurable && (
          <span className="badge b-gray">henüz ölçüm yok — taban alındı, birkaç saniye bekle</span>
        )}
        {!raw.shardsKnown && raw.mode === 'cluster' && (
          <span className="badge b-warn" title="system.clusters okunamadı; dengesizlik shard İÇİ değil TÜM node'lar üzerinden hesaplandı.">
            shard bilinmiyor
          </span>
        )}
        {raw.expectedNodes > raw.nodes.length && (
          <span className="badge b-err">
            {raw.nodes.length}/{raw.expectedNodes} node — EKSİK, oranlar güvenilmez
          </span>
        )}
      </div>
      {raw.note && (
        <div style={{ fontSize: 11.5, color: 'var(--text2)', marginBottom: 8 }}>{raw.note}</div>
      )}
      <div className="table-wrap">
        <table style={{ width: '100%' }}>
          <thead><tr>
            <th style={{ textAlign: 'left' }}>Node</th>
            <th style={{ textAlign: 'left' }}>Shard/Rep</th>
            <th className="num">CPU (cores)</th>
            <th className="num">Merge (threads)</th>
            <th className="num">Inserted rows</th>
            <th className="num">Part fetches</th>
          </tr></thead>
          <tbody>
            {view?.rows.map((r: NodeWorkRow) => (
              <tr key={r.host} style={{ opacity: r.restarted ? 0.55 : 1 }}>
                <td className="mono">{r.host}</td>
                <td className="mono" style={{ color: 'var(--text3)' }}>
                  {r.shard ? `${r.shard}${r.replica ? ' / ' + r.replica : ''}` : '—'}
                </td>
                <td className="num mono">{fmt(r.cpuCores, 3)}</td>
                <td className="num mono" title="CPU DEĞİL — merge thread'inin meşgul geçirdiği süre (I/O beklemesi dahil). CPU'yu aşabilir.">
                  {fmt(r.mergeThreads, 3)}
                </td>
                <td className="num mono">{fmtInt(r.insertedRows)}</td>
                <td className="num mono">{fmtInt(r.partFetches)}</td>
              </tr>
            ))}
            {view?.rows.some((r: NodeWorkRow) => r.restarted) && (
              <tr><td colSpan={6} style={{ fontSize: 11, color: 'var(--text3)', paddingTop: 6 }}>
                Soluk satır = sayaç sıfırlanmış (node yeniden başladı). O tur ölçüm yok ve
                dengesizlik hesabına GİRMİYOR — 0 saymak "iş yapmıyor" demek olurdu.
              </td></tr>
            )}
          </tbody>
        </table>
      </div>
    </Section>
  );
}

// Koordinatör dağılımı paneli. Kendi ucu + kendi 60s poll'u var
// (gövde okuması 10s'te dönüyor; bu okuma pencerede TÜM sorguları
// grupluyor, çok daha geniş bir satır kümesi — 10s'te koşturmanın

// DDLQueuePanel — v0.9.613. Dağıtık DDL kuyruğu sağlığı.
//
// Üç gecelik prod vakasının ürünleşmiş teşhisi: verdict SUNUCUDA
// davranıştan türer (host geride mi / atlıyor mu / ulaşılamıyor mu —
// kalibrasyon chstore/ddl_queue_health.go), panel yalnız çizer ve
// eylem cümlesini olduğu gibi gösterir. Tek-node kurulumda kart
// kendini "tek düğüm" notuna indirger — boş panel yok.
// fmtDurTR — 'ago'suz süre: fmtAge "5m ago" basar, Türkçe panelde yaş
// kolonu süre ister (verify bulgusu).
function fmtDurTR(sec: number): string {
  if (sec < 90) return `${Math.max(0, Math.round(sec))}sn`;
  if (sec < 5400) return `${Math.round(sec / 60)}dk`;
  return `${(sec / 3600).toFixed(1)}sa`;
}

function DDLQueuePanel() {
  const q = useDDLQueueHealth();
  const d = q.isPending ? undefined : q.isError ? null : q.data ?? null;

  const tone: Record<string, string> = {
    healthy: 'b-ok', single_node: 'b-gray',
    worker_stuck: 'b-err', worker_skipping: 'b-err',
    unreachable: 'b-err', probe_failed: 'b-warn',
  };
  const label: Record<string, string> = {
    healthy: 'SAĞLIKLI', single_node: 'TEK DÜĞÜM',
    worker_stuck: 'WORKER TAKILI', worker_skipping: 'WORKER ATLIYOR',
    unreachable: 'HOST ULAŞILAMAZ', probe_failed: 'PROBE DÜŞTÜ',
  };

  return (
    <Section title="Dağıtık DDL kuyruğu">
      {d === undefined && <Spinner />}
      {d === null && <Empty icon="✗" title="Okunamadı" />}
      {d && (
        <div style={{ display: 'grid', gap: 10 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span className={`badge ${tone[d.verdict] ?? 'b-gray'}`}>{label[d.verdict] ?? d.verdict}</span>
            {d.stuckCount > 0 && (
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                {d.stuckCountApprox ? 'en az ' : ''}{d.stuckCount.toLocaleString()} bekleyen girdi
                {d.oldestAgeSeconds ? ` · en eskisi ${fmtDurTR(d.oldestAgeSeconds)}` : ''}
              </span>
            )}
          </div>
          {/* Eylem cümlesi sunucudan — rozet tek başına gece 3'te ne
              yapılacağını söylemez. */}
          <div style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--text2)' }}>{d.detail}</div>

          {d.hosts && d.hosts.length > 0 && d.stuckCount > 0 && (
            <div className="table-wrap">
            <table style={{ width: "100%" }}>
              <thead><tr><th>Host</th><th>İşlenen</th><th>Geride</th></tr></thead>
              <tbody>
                {d.hosts.map(h => (
                  <tr key={h.host}>
                    <td className="mono">{h.host}</td>
                    <td style={{ textAlign: 'right' }}>{h.processed.toLocaleString()}</td>
                    <td style={{ textAlign: 'right', color: h.behind > 0 ? 'var(--err)' : 'var(--text3)' }}>
                      {h.behind > 0 ? h.behind.toLocaleString() : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          )}

          {d.verdict === 'worker_skipping' && (
            <div style={{ fontSize: 11.5, color: 'var(--text3)' }}>
              Kuyruk girdilerinin beklediği adlar:{' '}
              <span className="mono">{(d.queueHosts ?? []).join(', ') || '—'}</span>
              {' '}· cluster tanımı:{' '}
              <span className="mono">{(d.clusterHosts ?? []).join(', ') || '—'}</span>
            </div>
          )}

          {d.entries && d.entries.length > 0 && (
            <details>
              <summary style={{ fontSize: 12, cursor: 'pointer', color: 'var(--text3)' }}>
                Kuyruğun başı ({d.entries.length} girdi{d.stuckCount > d.entries.length ? `, toplam ${d.stuckCount}` : ''})
              </summary>
              <div className="table-wrap" style={{ marginTop: 6 }}>
              <table style={{ width: "100%" }}>
                <thead><tr><th>Girdi</th><th>Host</th><th>Durum</th><th>Yaş</th><th>Sorgu</th></tr></thead>
                <tbody>
                  {d.entries.map((e, i) => (
                    <tr key={`${e.entry}-${e.host}-${i}`}>
                      <td className="mono">{e.entry}</td>
                      <td className="mono">{e.host}</td>
                      <td>{e.status}</td>
                      <td>{fmtDurTR(e.ageSeconds)}</td>
                      <td className="mono" style={{ maxWidth: 380, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.query}>{e.query}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              </div>
            </details>
          )}

          {d.probeErrors && d.probeErrors.length > 0 && (
            <div style={{ fontSize: 11.5, color: 'var(--warn)' }}>
              Probe hataları: {d.probeErrors.join(' · ')}
            </div>
          )}
        </div>
      )}
    </Section>
  );
}

// teşhis değeri yok, maliyeti var).
function CoordinatorPanel() {
  // Pencere sabit basamaklardan seçilir — sunucu cache anahtarına
  // giren parametrenin kardinalitesi sınırlı olmalı (v0.8.270).
  const [windowS, setWindowS] = useState(COORD_WINDOWS[0].s);
  const q = useCHCoordinators(windowS);
  const raw = q.isPending ? undefined : q.isError ? null : q.data ?? null;

  // v0.9.506 — FARK MODU. events yolunda sayaçlar sunucu açılışından
  // beri kümülatif: prod'da ilk node'da birikmiş milyonlarca giriş
  // sorgusu var ve bir düzeltme mükemmel çalışsa bile o yığın günlerce
  // toplamı domine eder — panel eski dengesizliği göstermeye devam eder.
  // Yani "az önceki değişiklik yükü dengeledi mi?" sorusunu kümülatif
  // sayı CEVAPLAYAMAZ.
  //
  // Çözüm sunucuda durum tutmak değil: ilk gelen örnek tarayıcıda taban
  // olarak saklanıyor, sonraki her yenilemede FARK gösteriliyor. Panel
  // açık kaldıkça pencere büyür ve okuma keskinleşir. query_log yolunda
  // gerek yok — orası zaten pencereli.
  const baselineRef = useRef<{ at: number; by: Record<string, Coord> } | null>(null);
  const [baselineAt, setBaselineAt] = useState<number | null>(null);
  const deltaMode = raw?.source === 'events';

  if (deltaMode && raw && raw.nodes.length > 0 && !baselineRef.current) {
    baselineRef.current = {
      at: Date.now(),
      by: Object.fromEntries(raw.nodes.map(n => [n.host, n])),
    };
  }

  const resetBaseline = () => {
    if (!raw) return;
    baselineRef.current = { at: Date.now(), by: Object.fromEntries(raw.nodes.map(n => [n.host, n])) };
    setBaselineAt(Date.now());
  };
  void baselineAt; // yalnız yeniden render tetiklemek için

  // Fark satırları. Bir node yeniden başladıysa sayacı sıfırlanır ve
  // fark NEGATİF çıkar — o durumda 0'a kelepçeleyip tabanı o node için
  // tazeliyoruz, yoksa panel eksi sayı gösterir.
  const baseline = baselineRef.current;
  const elapsedMs = baseline ? Date.now() - baseline.at : 0;
  const data = (() => {
    if (!raw || !deltaMode || !baseline) return raw;
    const nodes = raw.nodes.map(n => {
      const b = baseline.by[n.host];
      if (!b) return { ...n, initial: 0, selects: 0, inserts: 0, other: 0 };
      const d = (cur: number, prev: number) => Math.max(0, cur - prev);
      return {
        ...n,
        initial: d(n.initial, b.initial),
        selects: d(n.selects, b.selects),
        inserts: d(n.inserts, b.inserts),
        other: d(n.other, b.other),
      };
    });
    const imb = (vals: number[]) => {
      if (vals.length < 2) return 1;
      const sum = vals.reduce((a, v) => a + v, 0);
      if (sum === 0) return 1;
      const mean = sum / vals.length;
      return Math.round((Math.max(...vals) / mean) * 100) / 100;
    };
    return {
      ...raw, nodes,
      initialImbalance: imb(nodes.map(n => n.initial)),
      selectImbalance:  imb(nodes.map(n => n.selects)),
      insertImbalance:  imb(nodes.map(n => n.inserts)),
    };
  })();

  const dt = useDataTable<Coord>({
    storageKey: 'ch-coordinators', columns: COORD_COLS,
    rows: data?.nodes ?? [], initialSort: { id: 'selects', dir: 'desc' },
  });

  return (
    <Section title="Query coordination spread">
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
        <select
          value={windowS}
          onChange={e => setWindowS(Number(e.target.value))}
          style={{
            background: 'var(--bg)', color: 'var(--text)',
            border: '1px solid var(--border)', borderRadius: 6,
            fontSize: 12, padding: '4px 8px',
          }}
        >
          {COORD_WINDOWS.map(w => <option key={w.s} value={w.s}>{w.label}</option>)}
        </select>
        {deltaMode && (
          <>
            <span className="badge b-info" title={
              'query_log bu kümede kapalı, sayaçlar açılıştan beri kümülatif. ' +
              'Panel ilk okumayı taban alıp FARKI gösteriyor — açık kaldıkça pencere büyür.'
            }>
              delta · {elapsedMs < 60_000
                ? `${Math.max(1, Math.round(elapsedMs / 1000))}sn`
                : `${Math.round(elapsedMs / 60_000)}dk`}
            </span>
            <button type="button" onClick={resetBaseline} style={{
              background: 'transparent', color: 'var(--accent2)', border: '1px solid var(--border)',
              borderRadius: 6, fontSize: 11, padding: '3px 8px', cursor: 'pointer',
            }}>tabanı sıfırla</button>
          </>
        )}
        {data && (
          <>
            <span className={`badge ${imbalanceTone(data.initialImbalance)}`}>
              Entry spread ×{data.initialImbalance.toFixed(2)}
            </span>
            <span className={`badge ${imbalanceTone(data.selectImbalance)}`}>
              SELECT spread ×{data.selectImbalance.toFixed(2)}
            </span>
            <span className={`badge ${imbalanceTone(data.insertImbalance)}`}>
              INSERT spread ×{data.insertImbalance.toFixed(2)}
            </span>
            <span className="badge b-gray">{data.mode}</span>
            <span className="badge b-gray" title={
              data.source === 'query_log'
                ? 'system.query_log — seçilen pencere'
                : data.source === 'events'
                ? 'system.events — taban alındıktan sonraki FARK (ham sayaçlar açılıştan beri kümülatif)'
                : 'ölçüm okunamadı'
            }>{data.source === 'query_log' ? 'windowed' : data.source === 'events' ? 'events' : 'no source'}</span>
          </>
        )}
        <span style={{ fontSize: 11, color: 'var(--text3)', marginLeft: 'auto' }}>
          entry-point queries only (is_initial_query) · ×1.00 = even
        </span>
      </div>
      <p style={{ fontSize: 12, color: 'var(--text2)', margin: '0 0 10px' }}>
        Which node was the <strong>entry point</strong> for each query — where parse,
        fan-out, partial-state merge and final aggregation land. Sub-queries a node runs
        as a shard are excluded, so this measures coordination load, not scan work.
        INSERT coordination is spread by the round-robin ingest pool; SELECT coordination
        rides the round-robin read pool from v0.9.496 onward.
      </p>
      {deltaMode && (
        <p style={{ fontSize: 12, color: 'var(--text2)', margin: '0 0 10px' }}>
          Bu kümede <code>system.query_log</code> kapalı, sayaçlar sunucu açılışından beri
          kümülatif — birikmiş eski dengesizlik yeni davranışı gizler. Panel bu yüzden ilk
          okumayı <strong>taban</strong> alıp farkı gösteriyor: açık bıraktıkça pencere büyür
          ve okuma keskinleşir. Bir değişikliğin etkisini ölçmek için deploy sonrası tabanı
          sıfırla ve 15–20 dk bekle.
        </p>
      )}
      {data === undefined && <Spinner />}
      {data === null && <EmptyNote text="Failed to load coordination spread" />}
      {data && data.note && <EmptyNote text={data.note} />}
      {data && !data.note && data.nodes.length > 0 && (
        <div className="table-wrap is-fit">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(c => (
                <tr key={c.host}>
                  <td className="mono" style={{ fontSize: 11 }} title={c.host}>{c.host || '—'}</td>
                  <td className="num mono"><strong>{fmtNum(c.initial)}</strong></td>
                  <td className="num mono">{fmtNum(c.selects)}</td>
                  <td className="num mono">{fmtNum(c.inserts)}</td>
                  <td className="num mono">{fmtNum(c.other)}</td>
                  <td className="num mono">{fmtNum(c.readRows)}</td>
                  <td className="num mono">{c.memoryMB.toFixed(0)} MB</td>
                  <td className="num mono">{c.p50Ms.toFixed(0)} ms</td>
                  <td className="num mono">{c.p95Ms.toFixed(0)} ms</td>
                  <td className="num mono">{fmtUptime(c.uptimeS)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}

export default function AdminClickhousePage() {
  // 10s poll via the hook's refetchInterval; hidden tabs pause
  // automatically (refetchIntervalInBackground defaults false).
  const healthQ = useClickhouseHealth();
  const data: CHHealth | null | undefined =
    healthQ.isPending ? undefined : healthQ.isError ? null : healthQ.data as CHHealth ?? null;

  // Highest-volume merge — surfaced in the page header so the
  // operator sees pressure without reading the table.
  const peakMergeSec = data?.merges?.reduce((m, x) => Math.max(m, x.elapsedSec), 0) ?? 0;
  const peakParts = data?.partHotspots?.reduce((m, x) => Math.max(m, x.parts), 0) ?? 0;

  // Shared sortable + resizable tables. Hooks are UNCONDITIONAL
  // (rules-of-hooks) — they live above the data === undefined/null
  // render branches and feed each table its already-fetched array.
  const slowDt = useDataTable<Slow>({
    storageKey: 'ch-slowqueries', columns: SLOW_COLS,
    rows: data?.slowQueries ?? [], initialSort: { id: 'elapsed', dir: 'desc' },
  });
  const mergeDt = useDataTable<Merge>({
    storageKey: 'ch-merges', columns: MERGE_COLS,
    rows: data?.merges ?? [], initialSort: { id: 'elapsed', dir: 'desc' },
  });
  const partDt = useDataTable<PartHot>({
    storageKey: 'ch-parthotspots', columns: PARTHOT_COLS,
    rows: data?.partHotspots ?? [], initialSort: { id: 'parts', dir: 'desc' },
  });
  const asyncDt = useDataTable<AsyncIns>({
    storageKey: 'ch-asyncinserts', columns: ASYNC_COLS,
    rows: data?.asyncInserts ?? [], initialSort: { id: 'bytes', dir: 'desc' },
  });
  const mutationDt = useDataTable<Mutation>({
    storageKey: 'ch-mutations', columns: MUTATION_COLS,
    rows: data?.mutations ?? [], initialSort: { id: 'parts', dir: 'desc' },
  });
  const repLagDt = useDataTable<RepLag>({
    storageKey: 'ch-replicationlag', columns: REPLAG_COLS,
    rows: data?.replicationLag ?? [], initialSort: { id: 'delay', dir: 'desc' },
  });

  return (
    <>
      <Topbar title="ClickHouse" />
      <div id="content">
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
          gap: 12, marginBottom: 18,
        }}>
          <KPI label="Slow queries · 1h" value={fmtNum(data?.slowQueries?.length ?? 0)}
               sub=">500ms" />
          <KPI label="Active merges" value={fmtNum(data?.merges?.length ?? 0)}
               sub={peakMergeSec > 0 ? `peak ${peakMergeSec.toFixed(0)}s` : ''} />
          <KPI label="Part hotspots" value={fmtNum(data?.partHotspots?.length ?? 0)}
               sub={peakParts > 0 ? `max ${peakParts} parts` : ''}
               cls={peakParts > 300 ? 'warn' : peakParts > 600 ? 'err' : ''} />
          <KPI label="Replication lag rows"
               value={fmtNum(data?.replicationLag?.length ?? 0)}
               sub="cluster only" />
          <KPI label="Pending mutations"
               value={fmtNum(data?.mutations?.length ?? 0)}
               sub="ALTER … DELETE/UPDATE"
               cls={(data?.mutations?.length ?? 0) > 0 ? 'warn' : ''} />
        </div>

        {data === undefined && <Spinner />}
        {data === null && <Empty icon="⚠" title="Failed to load ClickHouse health" />}

        {data && (
          <>
            <TopologyPanel topology={data.topology} />

            {/* v0.9.494 — topolojinin hemen altında: operatör kaç
                node'u olduğunu okuduktan hemen sonra o node'ların
                gerçekte eşit yüklenip yüklenmediğini görsün. */}
            <DDLQueuePanel />
            <CoordinatorPanel />
            {/* v0.9.543 — koordinatör paneli "sorgular nereye giriyor"u
                ölçüyor; bu panel "o node ne İŞ yapıyor"u. Yan yana
                okunuyorlar: giriş dengeliyken CPU dengesizse cevap
                sorgu yolunda değil, yazma/merge tarafındadır. */}
            <NodeWorkPanel />

            {/* v0.9.770 — rollup kurulum sihirbazı. Topolojinin hemen
                altında değil BURADA: operatör önce kümenin sağlıklı
                olduğunu (DDL kuyruğu boş, node'lar dengeli) okumalı;
                tıkalı bir DDL kuyruğuna 19 ifade daha göndermek en
                kötü sıralama olurdu. */}
            <RollupWizardPanel />

            <Section title="Slow queries (>500ms, last 1h)">
              {(!data.slowQueries || data.slowQueries.length === 0)
                ? <EmptyNote text="No slow queries in the last hour" />
                : (
                  <div className="table-wrap is-fit">
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={slowDt} />
                      <DataTableHead dt={slowDt} />
                      <tbody>
                        {slowDt.sortedRows.map((q, i) => (
                          <tr key={i}>
                            <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                              {new Date(q.eventTimeNs / 1e6).toLocaleTimeString()}
                            </td>
                            <td className="mono" style={{ fontSize: 11 }}>{q.user || '—'}</td>
                            <td className="num mono">{q.elapsedMs.toFixed(0)} ms</td>
                            <td className="num mono">{q.memoryMb.toFixed(0)} MB</td>
                            <td className="num mono">{fmtNum(q.readRows)}</td>
                            <td className="mono" style={{ fontSize: 11 }} title={q.query}>
                              {q.query.replace(/\s+/g, ' ').slice(0, 200)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            <Section title="In-flight merges">
              {(!data.merges || data.merges.length === 0)
                ? <EmptyNote text="No merges in flight — CH idle or up-to-date" />
                : (
                  <div className="table-wrap is-fit">
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={mergeDt} />
                      <DataTableHead dt={mergeDt} />
                      <tbody>
                        {mergeDt.sortedRows.map((m, i) => (
                          <tr key={i}>
                            <td className="mono">{m.host}</td>
                            <td className="mono">{m.database}</td>
                            <td className="mono">{m.table}</td>
                            <td className="num mono">{m.elapsedSec.toFixed(1)}s</td>
                            <td className="num mono">{m.progressPct.toFixed(0)}%</td>
                            <td className="num mono">{fmtNum(m.rowsRead)}</td>
                            <td className="num mono">{fmtBytes(m.mergedSizeBytes)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            <Section title="Part hotspots (active parts per table, top 15)">
              {(!data.partHotspots || data.partHotspots.length === 0)
                ? <EmptyNote text="No part data available" />
                : (
                  <div className="table-wrap is-fit">
                    <table style={{ tableLayout: 'fixed', width: '100%' }}>
                      <DataTableColgroup dt={partDt} />
                      <DataTableHead dt={partDt} />
                      <tbody>
                        {partDt.sortedRows.map((p, i) => (
                          <tr key={i}>
                            <td className="mono">{p.database}</td>
                            <td className="mono">{p.table}</td>
                            <td className="num mono" style={{
                              color: p.parts > 300 ? 'var(--err)' : p.parts > 150 ? 'var(--warn)' : 'var(--text)',
                              fontWeight: p.parts > 150 ? 600 : 400,
                            }}>{fmtNum(p.parts)}</td>
                            <td className="num mono">{fmtNum(p.rowsTotal)}</td>
                            <td className="num mono">{fmtBytes(p.bytesTotal)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
            </Section>

            {data.asyncInserts && data.asyncInserts.length > 0 && (
              <Section title="Async insert buffer">
                <div className="table-wrap is-fit">
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={asyncDt} />
                    <DataTableHead dt={asyncDt} />
                    <tbody>
                      {asyncDt.sortedRows.map((a, i) => (
                        <tr key={i}>
                          <td className="mono">{a.database}</td>
                          <td className="mono">{a.table}</td>
                          <td className="num mono">{fmtNum(a.totalBytes)}</td>
                          <td className="num mono">{fmtNum(a.entriesCount)}</td>
                          <td className="num mono">{a.firstUpdateMsAgo}ms</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Section>
            )}

            {/* v0.6.22 — pending mutations panel. Empty on a
                healthy install; rows here mean an ALTER … DELETE/
                UPDATE is being rewritten by CH. Slow / sustained
                non-zero is the early-warning shape for the
                operator: time to swap the mutation pattern. */}
            {data.mutations && data.mutations.length > 0 && (
              <Section title={`Pending mutations (${data.mutations.length})`}>
                <p style={{ fontSize: 11, color: 'var(--text2)', margin: '0 0 8px' }}>
                  In-flight ALTER … DELETE / UPDATE rewriting parts. Healthy queue is empty;
                  a sustained non-zero row count usually means the table needs a tombstone or
                  ReplacingMergeTree pattern instead of in-place mutation.
                </p>
                <div className="table-wrap is-fit">
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={mutationDt} />
                    <DataTableHead dt={mutationDt} />
                    <tbody>
                      {mutationDt.sortedRows.map((m, i) => (
                        <tr key={i}>
                          <td className="mono">{m.database}.{m.table}</td>
                          <td className="num mono">{fmtNum(m.parts)}</td>
                          <td className="num mono">{fmtAge(m.elapsedMs)}</td>
                          <td className="mono" style={{ fontSize: 11 }} title={m.command}>{m.command}</td>
                          <td className="mono" style={{
                            fontSize: 11, color: m.latestFail ? 'var(--err)' : 'var(--text3)',
                          }} title={m.latestFail}>{m.latestFail || '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Section>
            )}

            {data.replicationLag && data.replicationLag.length > 0 && (
              <Section title="Replication lag (cluster only)">
                <div className="table-wrap is-fit">
                  <table style={{ tableLayout: 'fixed', width: '100%' }}>
                    <DataTableColgroup dt={repLagDt} />
                    <DataTableHead dt={repLagDt} />
                    <tbody>
                      {repLagDt.sortedRows.map((r, i) => (
                        <tr key={i}>
                          <td className="mono">{r.database}</td>
                          <td className="mono">{r.table}</td>
                          <td className="num mono">{fmtNum(r.queueSize)}</td>
                          <td className="num mono">{r.absoluteDelaySec}s</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </Section>
            )}

            {/* v0.6.8 — AI query optimizer. Operator pastes raw CH
                SQL; Copilot rewrites it against Coremetry's MV
                catalogue + hard-constraint checklist. Returns a
                cleaned-up query the operator reviews + copies into
                their CH client. Not a query runner — we don't
                want a half-helpful UI that makes it tempting to
                hand un-reviewed AI output a CH session. */}
            <CHQueryOptimizer />
          </>
        )}
      </div>
    </>
  );
}

// ── Rollup kurulum sihirbazı — v0.9.770 ─────────────────────────────
//
// Operatörün sorusu: "sen kontrol edip yapabilir misin". Öncesinde 0001
// + 0003 elle yapıştırılan ~300 satır DDL'di; cluster adı yanlış
// yazıldığında DDL dağıtık kuyrukta süresiz bekliyordu (v0.9.613 vakası).
//
// Kart üç adımda ilerler ve HER adım geri alınabilir:
//   1. Durum — hangi tablo var, kaç satır (kurulmuşsa "Kur" no-op).
//   2. Ön kontrol — küme listesi + kaynak tablo/kolon denetimi. Hüküm
//      SUNUCUDA verilir (Supported); UI yalnız çizer.
//   3. Kur / Geri Al — onaylı, ifade-ifade sonuçlu.
//
// OTOMATİK DEĞİL: boot'ta asla koşmaz. MV kurmak ingest yoluna yazar;
// bozuk bir MV write_failed üretir ve ingest'i düşürür. O kararın
// sahibi operatör.

const ROLLUP_COLS: DataTableColumn<RollupTableStatus>[] = [
  { id: 'table',  label: 'Tablo',   sortValue: r => r.table,  naturalDir: 'asc', width: 250 },
  { id: 'family', label: 'Aile',    sortValue: r => r.family, naturalDir: 'asc', width: 100 },
  { id: 'exists', label: 'Durum',   sortValue: r => (r.exists ? 1 : 0), numeric: true, naturalDir: 'desc', width: 120 },
  { id: 'rows',   label: 'Satır',   sortValue: r => r.rows,   numeric: true, naturalDir: 'desc', width: 130 },
  { id: 'minTs',  label: 'En eski', sortValue: r => r.minTsMs, numeric: true, naturalDir: 'asc', width: 200 },
];

const TARGET_LABEL: Record<RollupTarget, string> = {
  both: 'Her ikisi (0001 + 0003)',
  narrow: 'Yalnız dar span zinciri (0001)',
  metrics: 'Yalnız metrik zinciri (0003)',
};

function RollupWizardPanel() {
  const status = useRollupStatus();
  // Ön kontrol / eylem durumu YEREL: bunlar paylaşılabilir bir GÖRÜNÜM
  // değil, tek seferlik bir kurulum akışı. URL'e yazmak "linki aç,
  // kurulum onayı hazır beklesin" gibi tehlikeli bir şey üretirdi.
  const [pre, setPre] = useState<RollupPreflightResult | null>(null);
  const [preBusy, setPreBusy] = useState(false);
  const [preErr, setPreErr] = useState<string | null>(null);
  const [cluster, setCluster] = useState('');
  const [target, setTarget] = useState<RollupTarget>('both');
  const [confirmKind, setConfirmKind] = useState<'apply' | 'rollback' | null>(null);
  // busyKind — HANGİ eylemin uçtuğu. Düz bir boolean, "Kur"a basıldığında
  // "Geri Al"ı da spinner'a sokardı; operatör hangi işin koştuğunu
  // görmeli (DDL dakikalar sürebiliyor).
  const [busyKind, setBusyKind] = useState<'apply' | 'rollback' | null>(null);
  const busy = busyKind !== null;
  const [action, setAction] = useState<{ kind: 'apply' | 'rollback'; res: RollupActionResult } | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);

  const rows = status.data?.tables ?? [];
  const dt = useDataTable<RollupTableStatus>({
    storageKey: 'ch-rollup-status', columns: ROLLUP_COLS, rows,
  });

  const runPreflight = async () => {
    setPreBusy(true); setPreErr(null);
    try {
      const r = await api.rollupPreflight();
      setPre(r);
      // Ön-seçim: Coremetry'nin KENDİ cluster adı listede varsa o.
      // Yoksa tek küme varsa o. Birden çok küme varsa BOŞ bırakılır —
      // yanlış kümeye DDL göndermek sessiz bir kayıp olurdu, seçimi
      // operatör yapsın.
      const suggested = r.suggestedCluster && r.clusters.includes(r.suggestedCluster)
        ? r.suggestedCluster
        : r.clusters.length === 1 ? r.clusters[0] : '';
      setCluster(suggested);
    } catch (e: unknown) {
      setPreErr(e instanceof Error ? e.message : String(e));
      setPre(null);
    } finally {
      setPreBusy(false);
    }
  };

  const runAction = async (kind: 'apply' | 'rollback') => {
    setConfirmKind(null);
    setBusyKind(kind); setActionErr(null); setAction(null);
    try {
      const res = kind === 'apply'
        ? await api.rollupApply(cluster, target)
        : await api.rollupRollback(cluster, target);
      setAction({ kind, res });
    } catch (e: unknown) {
      setActionErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusyKind(null);
      // Durum eylemden SONRA tazelenir — kartın gösterdiği tablo
      // listesi eylemin gerçek sonucudur, iddiası değil.
      status.refetch();
      // Ön kontrol de bayatladı (narrowInstalled/metricsInstalled
      // değişmiş olabilir); sessizce yenile.
      void runPreflight();
    }
  };

  const canApply = !!pre?.supported && !!cluster && !busy;

  return (
    <Section title="Rollup katmanı (0001 + 0003)">
      <p style={{ fontSize: 12, color: 'var(--text2)', margin: '0 0 10px', lineHeight: 1.55 }}>
        Dar span rollup zinciri (10s→1m→5m→1h) ve metrik rollup zinciri (1m→5m→1h).
        Okuma katmanı zaten bu tabloları arıyor; yoksa ham yola düşüyor. Kurulum{' '}
        <strong>otomatik değildir</strong> — boot'ta asla koşmaz, tek tetikleyici bu buton.
      </p>

      {/* ── 1. Durum ── */}
      {status.isPending && <Spinner />}
      {status.isError && <Empty icon="⚠" title="Rollup durumu okunamadı" />}
      {status.data && (
        <div className="table-wrap is-fit" style={{ marginBottom: 10 }}>
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map(t => (
                <tr key={t.table}>
                  <td className="mono">{t.table}</td>
                  <td style={{ fontSize: 11, color: 'var(--text3)' }}>{t.family}</td>
                  <td>
                    {t.err
                      ? <span className="badge b-warn" title={t.err}>OKUNAMADI</span>
                      : t.exists
                        ? <span className="badge b-ok">VAR</span>
                        : <span className="badge b-gray">YOK</span>}
                  </td>
                  <td className="num mono">{t.exists && !t.err ? fmtNum(t.rows) : '—'}</td>
                  <td className="mono" style={{ fontSize: 11, color: 'var(--text3)' }}>
                    {t.minTsMs > 0 ? new Date(t.minTsMs).toLocaleString() : '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* ── 2. Ön kontrol ── */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBottom: 10 }}>
        <Button variant="secondary" size="sm" onClick={runPreflight} loading={preBusy}>
          Ön kontrol
        </Button>
        <Button variant="ghost" size="sm" onClick={() => status.refetch()} disabled={status.isFetching}>
          Durumu yenile
        </Button>
        {preErr && <span style={{ color: 'var(--err)', fontSize: 12 }}>{preErr}</span>}
      </div>

      {pre && (
        <div style={{
          padding: '12px 14px', borderRadius: 6, marginBottom: 12,
          border: `1px solid ${pre.supported ? 'var(--ok)' : 'var(--warn)'}`,
          background: pre.supported ? 'rgba(34, 197, 94, 0.08)' : 'rgba(250, 204, 21, 0.10)',
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
            <span className={`badge ${pre.supported ? 'b-ok' : 'b-warn'}`}>
              {pre.supported ? 'UYGULANABİLİR' : 'UYGULANAMAZ'}
            </span>
            <span style={{ fontSize: 12.5, color: 'var(--text2)', lineHeight: 1.5 }}>{pre.detail}</span>
          </div>
          <div className="table-wrap" style={{ marginBottom: 8 }}>
            <table style={{ width: '100%' }}>
              <thead><tr><th>Kontrol</th><th>Sonuç</th></tr></thead>
              <tbody>
                <PreRow label="spans_local" ok={pre.spansLocal} />
                <PreRow label="metric_points_local" ok={pre.metricPointsLocal} />
                <PreRow label="Kaynak kolonlar tam"
                        ok={pre.missingColumns.length === 0}
                        note={pre.missingColumns.join(', ')} />
                <PreRow label="Tanımlı küme"
                        ok={pre.clusters.length > 0}
                        note={pre.clusters.join(', ')} />
                <PreRow label="rollup_spans_narrow_10s zaten kurulu"
                        ok={pre.narrowInstalled} neutral />
                <PreRow label="rollup_metrics_1m zaten kurulu"
                        ok={pre.metricsInstalled} neutral />
              </tbody>
            </table>
          </div>
          {pre.probeErrors && pre.probeErrors.length > 0 && (
            <div style={{ fontSize: 11.5, color: 'var(--warn)' }}>
              Probe hataları: {pre.probeErrors.join(' · ')}
            </div>
          )}
        </div>
      )}

      {/* ── 3. Kur / Geri Al ── */}
      <div style={{ display: 'flex', gap: 10, alignItems: 'flex-end', flexWrap: 'wrap' }}>
        <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text3)' }}>
          Küme
          {/* Sabit ve küçük küme (genelde 1-2 ad) → düz select
              (frontend-conventions §3). Elle yazdırmıyoruz: yanlış ad
              DDL'i kuyrukta süresiz bekletir. */}
          <select value={cluster} onChange={e => setCluster(e.target.value)}
                  disabled={!pre || pre.clusters.length === 0}>
            <option value="">{pre ? '— seçiniz —' : 'önce ön kontrol'}</option>
            {(pre?.clusters ?? []).map(c => <option key={c} value={c}>{c}</option>)}
          </select>
        </label>
        <label style={{ display: 'grid', gap: 4, fontSize: 11, color: 'var(--text3)' }}>
          Hedef
          <select value={target} onChange={e => setTarget(e.target.value as RollupTarget)}>
            {(['both', 'narrow', 'metrics'] as RollupTarget[]).map(t => (
              <option key={t} value={t}>{TARGET_LABEL[t]}</option>
            ))}
          </select>
        </label>
        <Button onClick={() => setConfirmKind('apply')} disabled={!canApply}
                loading={busyKind === 'apply'}>
          Kur
        </Button>
        {/* Geri Al HER ZAMAN görünür (ön kontrolden bağımsız): bozuk bir
            MV ingest'i düşürürken önce ön kontrol koşturmak gerekmemeli. */}
        <Button variant="danger" onClick={() => setConfirmKind('rollback')} disabled={busy}
                loading={busyKind === 'rollback'}>
          Geri Al (MV'leri düşür)
        </Button>
      </div>

      {actionErr && (
        <div style={{ marginTop: 10, fontSize: 12, color: 'var(--err)' }}>{actionErr}</div>
      )}

      {action && (
        <div style={{ marginTop: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6, flexWrap: 'wrap' }}>
            <span className={`badge ${action.res.ok ? 'b-ok' : 'b-err'}`}>
              {action.kind === 'apply' ? 'KURULUM' : 'GERİ ALMA'} — {action.res.ok ? 'TAMAM' : 'HATA'}
            </span>
            <span style={{ fontSize: 11.5, color: 'var(--text3)' }}>
              {action.res.statements.filter(s => s.ok).length} / {action.res.statements.length} ifade
            </span>
          </div>
          <div className="table-wrap">
            <table style={{ width: '100%' }}>
              <thead><tr><th style={{ width: 60 }}>#</th><th>İfade</th><th style={{ width: 90 }}>Sonuç</th></tr></thead>
              <tbody>
                {action.res.statements.map((s, i) => (
                  <tr key={i}>
                    <td className="mono" style={{ color: 'var(--text3)' }}>{i + 1}</td>
                    <td className="mono" style={{ fontSize: 11 }} title={s.err || s.head}>
                      {s.head}
                      {s.err && (
                        <div style={{ color: 'var(--err)', fontSize: 11, marginTop: 2, whiteSpace: 'normal' }}>
                          {s.err}
                        </div>
                      )}
                    </td>
                    <td>{s.ok ? <span style={{ color: 'var(--ok)' }}>✓</span> : <span style={{ color: 'var(--err)' }}>✗</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {action.kind === 'apply' && !action.res.ok && (
            <div style={{
              marginTop: 10, padding: '10px 12px', borderRadius: 6,
              border: '1px solid var(--err)', background: 'rgba(239, 68, 68, 0.08)',
              fontSize: 12, color: 'var(--text)', lineHeight: 1.55,
            }}>
              <strong style={{ color: 'var(--err)' }}>Kurulum yarıda kesildi.</strong>{' '}
              İfadeler SIRAYLA koşar ve ilk hatada durur — yukarıdaki ✗ satırı gerçek
              ilk nedendir. Zincirin yarısı kurulmuş olabilir: kurulan MV'ler ingest'e
              yazmaya BAŞLAMIŞ durumda. Nedeni gidermeden önce{' '}
              <strong>Geri Al</strong> ile yazımı kesin.
            </div>
          )}
        </div>
      )}

      {/* Kalıcı not — kartın en altında, her durumda görünür. */}
      <div style={{
        marginTop: 14, padding: '10px 12px', borderRadius: 6,
        background: 'var(--bg2)', border: '1px dashed var(--border)',
        fontSize: 11.5, color: 'var(--text2)', lineHeight: 1.6,
      }}>
        <strong>Kur sonrası 5 dk boyunca <code className="mono">/api/health</code>'te{' '}
        <code className="mono">write_failed=0</code> doğrulayın.</strong>{' '}
        Artış = MV bozuk (kolon/tip uyuşmazlığı ingest INSERT'ünü düşürüyor) →{' '}
        <strong>Geri Al</strong>. Geri alma YALNIZ MV'leri düşürür; tablolar ve
        içlerindeki veri kalır, okuma katmanı boş tabloyu görüp ham yola döner.
      </div>

      <Modal
        open={confirmKind !== null}
        onClose={() => setConfirmKind(null)}
        title={confirmKind === 'apply' ? 'Rollup DDL uygulansın mı?' : 'MV\'ler düşürülsün mü?'}
        footer={
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <Button variant="secondary" onClick={() => setConfirmKind(null)}>Vazgeç</Button>
            <Button variant={confirmKind === 'apply' ? 'primary' : 'danger'}
                    onClick={() => confirmKind && runAction(confirmKind)}>
              {confirmKind === 'apply' ? 'Uygula' : 'Geri Al'}
            </Button>
          </div>
        }
      >
        {confirmKind === 'apply' ? (
          <div style={{ fontSize: 13, lineHeight: 1.6 }}>
            DDL <strong>prod ClickHouse üzerinde</strong> koşacak.
            <div style={{ marginTop: 8 }}>
              Küme: <code className="mono">{cluster}</code><br />
              Hedef: <code className="mono">{TARGET_LABEL[target]}</code>
            </div>
            <div style={{ marginTop: 8, color: 'var(--text2)' }}>
              Tablolar + MV'ler yaratılır (hepsi <code>IF NOT EXISTS</code>).
              MV'ler yaratıldığı andan itibaren ingest yoluna yazmaya başlar.
            </div>
          </div>
        ) : (
          <div style={{ fontSize: 13, lineHeight: 1.6 }}>
            <strong>{target === 'both' ? 7 : target === 'narrow' ? 4 : 3}</strong> materialized
            view düşürülecek{cluster ? <> (<code className="mono">{cluster}</code>)</> : ' (ON CLUSTER\'sız)'}.
            <div style={{ marginTop: 8, color: 'var(--text2)' }}>
              Tablolar ve içlerindeki veri <strong>kalır</strong> — bu "kurulumu geri al"
              değil, "yazımı kes" düğmesi.
            </div>
          </div>
        )}
      </Modal>
    </Section>
  );
}

function PreRow({ label, ok, note, neutral }: {
  label: string; ok: boolean; note?: string; neutral?: boolean;
}) {
  return (
    <tr>
      <td className="mono" style={{ fontSize: 11.5 }}>{label}</td>
      <td>
        <span style={{ color: ok ? 'var(--ok)' : neutral ? 'var(--text3)' : 'var(--err)' }}>
          {ok ? '✓' : neutral ? '—' : '✗'}
        </span>
        {note && <span style={{ marginLeft: 8, fontSize: 11, color: 'var(--text3)' }}>{note}</span>}
      </td>
    </tr>
  );
}

function CHQueryOptimizer() {
  const [query, setQuery] = useState('');
  const [result, setResult] = useState<{
    optimized: string; explanation: string;
    warning?: string; raw?: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const run = async () => {
    const q = query.trim();
    if (!q) return;
    setBusy(true); setErr(null); setResult(null);
    try {
      const r = await api.optimizeCHQuery(q);
      setResult(r);
    } catch (e: any) {
      setErr(e?.message || String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section style={{
      marginTop: 24, padding: 16, background: 'var(--bg1)',
      border: '1px solid var(--border)', borderRadius: 8,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <h3 style={{ margin: 0, fontSize: 14 }}>AI query optimizer</h3>
        <span className="badge b-info">v0.6.8</span>
        <span style={{ fontSize: 11, color: 'var(--text3)', marginLeft: 'auto' }}>
          MV bypass · LIMIT · max_execution_time · time-bounded WHERE
        </span>
      </div>
      <p style={{ fontSize: 12, color: 'var(--text2)', margin: '0 0 10px' }}>
        Paste a ClickHouse query. AI rewrites it against Coremetry's MV catalogue
        (<code>service_summary_5m</code>, <code>topology_edges_5m</code>, …) and the
        hard-constraint checklist. Review before running — output is a suggestion, not auto-applied.
      </p>
      <textarea
        value={query}
        onChange={e => setQuery(e.target.value)}
        placeholder="SELECT service_name, count() FROM spans WHERE time >= now() - INTERVAL 1 HOUR GROUP BY service_name"
        rows={6}
        spellCheck={false}
        style={{
          width: '100%', boxSizing: 'border-box',
          fontFamily: 'ui-monospace, monospace', fontSize: 12,
          padding: 10, background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 6,
        }}
      />
      <div style={{ display: 'flex', gap: 8, marginTop: 8, alignItems: 'center' }}>
        <button onClick={run} disabled={busy || !query.trim()}>
          {busy ? 'Optimizing…' : 'Optimize'}
        </button>
        {err && <span style={{ color: 'var(--err)', fontSize: 12 }}>{err}</span>}
      </div>
      {result && (
        <div style={{ marginTop: 14 }}>
          {result.warning && (
            <div className="b-warn" style={{
              fontSize: 12, marginBottom: 8,
              padding: '6px 10px', borderRadius: 4,
            }}>{result.warning}</div>
          )}
          {result.optimized && (
            <>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>
                Optimized SQL
              </div>
              <pre style={{
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                padding: 10, background: 'var(--bg)', color: 'var(--text)',
                border: '1px solid var(--border)', borderRadius: 6,
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              }}>{result.optimized}</pre>
            </>
          )}
          {result.explanation && (
            <>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 10, marginBottom: 4 }}>
                Explanation
              </div>
              <p style={{ fontSize: 12, color: 'var(--text)', margin: 0, lineHeight: 1.5 }}>
                {result.explanation}
              </p>
            </>
          )}
          {!result.optimized && result.raw && (
            <>
              <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 10, marginBottom: 4 }}>
                Raw model output
              </div>
              <pre style={{
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                padding: 10, background: 'var(--bg)', color: 'var(--text3)',
                border: '1px solid var(--border)', borderRadius: 6,
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              }}>{result.raw}</pre>
            </>
          )}
        </div>
      )}
    </section>
  );
}

// TopologyPanel — first thing on /admin/clickhouse. Operator
// answers "are we talking to a cluster?" in under a second. The
// banner colour reflects the live agreement between the
// configured cluster name and what system.clusters reports:
//   • green  — configuredCluster set, nodes detected
//   • blue   — standalone install (no cluster configured)
//   • amber  — configuredCluster set but system.clusters is
//              empty → misconfig (env var on the app side,
//              <remote_servers> missing on CH side)
function TopologyPanel({ topology: t }: { topology: Topology }) {
  // v0.5.428 — only flag misconfig when the probe actually
  // returned an empty set. If the probe itself failed
  // (timeout / disconnect), we can't make a misconfig claim —
  // render a softer "probe failed" banner instead.
  // v0.5.439 — if the probe failed BUT a cached snapshot filled
  // `nodes` in (`clusterNodesStale`), drop the warn banner
  // entirely; the cluster is still healthy, we just couldn't
  // refresh the topology this tick. A small inline "stale"
  // pill carries the freshness signal at a softer volume.
  const probeFailed = !!t.clusterProbeError;
  const cacheStale = !!t.clusterNodesStale;
  const misconfigured = !!t.configuredCluster && (!t.nodes || t.nodes.length === 0) && !probeFailed && !cacheStale;

  // Shared sortable + resizable tables for the cluster-nodes list and
  // the resolved shard policy. Both hooks are unconditional; the tables
  // themselves render conditionally below.
  const nodesDt = useDataTable<ClusterNode>({
    storageKey: 'ch-clusternodes', columns: NODE_COLS,
    rows: t.nodes ?? [], initialSort: { id: 'shard', dir: 'asc' },
  });
  const shardRows: ShardPolicyRow[] = Object.entries(t.shardPolicy ?? {})
    .map(([table, expr]) => ({ table, expr }));
  const shardDt = useDataTable<ShardPolicyRow>({
    storageKey: 'ch-shardpolicy', columns: SHARD_POLICY_COLS,
    rows: shardRows, initialSort: { id: 'table', dir: 'asc' },
  });
  const bannerCls = misconfigured || probeFailed ? 'warn' : (t.mode === 'cluster' ? 'ok' : 'info');
  const bannerColor =
    bannerCls === 'ok' ? 'var(--ok)' :
    bannerCls === 'warn' ? 'var(--warn)' : 'var(--accent2)';
  const bannerBg =
    bannerCls === 'ok' ? 'rgba(34, 197, 94, 0.08)' :
    bannerCls === 'warn' ? 'rgba(250, 204, 21, 0.10)' : 'rgba(96, 165, 250, 0.08)';

  return (
    <div style={{ marginBottom: 24 }}>
      <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8 }}>Topology</h3>
      <div style={{
        padding: '12px 14px', borderRadius: 6,
        border: `1px solid ${bannerColor}`, background: bannerBg,
        marginBottom: 10,
      }}>
        <div style={{ fontSize: 13, color: 'var(--text)', marginBottom: 4 }}>
          {misconfigured && (
            <>
              <strong style={{ color: 'var(--warn)' }}>⚠ Cluster misconfigured</strong> —
              cluster <code className="mono">{t.configuredCluster}</code> is configured
              but not present in <code>system.clusters</code>. Check the CH server's
              <code> &lt;remote_servers&gt;</code> block.
            </>
          )}
          {probeFailed && (
            <>
              <strong style={{ color: 'var(--warn)' }}>⚠ Cluster probe failed</strong> —
              couldn't confirm cluster <code className="mono">{t.configuredCluster}</code>{' '}
              via <code>system.clusters</code>: <span className="mono" style={{ fontSize: 11 }}>{t.clusterProbeError}</span>.
              {' '}Usually transient (CH busy). Retry on next refresh.
            </>
          )}
          {!misconfigured && !probeFailed && t.mode === 'cluster' && (
            <>
              <strong style={{ color: 'var(--ok)' }}>● Cluster mode</strong> —
              connected to cluster <code className="mono">{t.configuredCluster}</code>
              {' '}with <strong>{t.nodes?.length ?? 0}</strong> registered node{(t.nodes?.length ?? 0) === 1 ? '' : 's'}.
              {cacheStale && (
                <span
                  title="Live system.clusters probe failed this tick — showing the last successful snapshot."
                  style={{
                    marginLeft: 8, padding: '1px 6px', borderRadius: 3,
                    background: 'rgba(250, 204, 21, 0.18)',
                    color: 'var(--warn)', fontSize: 11, fontWeight: 600,
                  }}>
                  stale: {fmtAge(t.clusterNodesAgeMs ?? 0)}
                </span>
              )}
            </>
          )}
          {!misconfigured && t.mode === 'standalone' && (
            <>
              <strong style={{ color: 'var(--accent2)' }}>● Standalone mode</strong> —
              no <code>ON CLUSTER</code> name configured; Coremetry is
              writing to a single ClickHouse server.
            </>
          )}
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          Database: <code className="mono">{t.database}</code>
          {' · '}
          Driver hosts: <code className="mono">{(t.connectedHosts ?? []).join(', ') || '—'}</code>
        </div>
      </div>

      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
        gap: 10, marginBottom: 10,
      }}>
        <MiniStat label="Distributed tables" value={fmtNum(t.distributedTables)}
                  hint={t.distributedTables > 0 ? 'cluster wrapper in use' : 'no Distributed wrapper'} />
        <MiniStat label="Replicated local tables" value={fmtNum(t.localReplicated)}
                  hint={t.localReplicated > 0 ? 'ReplicatedMergeTree' : 'no replicas'} />
        <MiniStat label="Plain MergeTree tables" value={fmtNum(t.plainMergeTree)} />
        <MiniStat label="ZooKeeper / Keeper" value={t.zookeeperConnected ? 'connected' : 'not detected'}
                  cls={t.mode === 'cluster' && !t.zookeeperConnected ? 'warn' : ''}
                  hint={t.mode === 'cluster' && !t.zookeeperConnected
                    ? 'cluster requires ZK/Keeper'
                    : t.mode === 'standalone' ? 'not required' : ''} />
      </div>

      {t.nodes && t.nodes.length > 0 && (
        <div className="table-wrap is-fit">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={nodesDt} />
            <DataTableHead dt={nodesDt} />
            <tbody>
              {nodesDt.sortedRows.map((n, i) => (
                <tr key={i}>
                  <td className="num mono">{n.shardNum}</td>
                  <td className="num mono">{n.replicaNum}</td>
                  <td className="mono">{n.hostName}</td>
                  <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>
                    {n.hostAddress || '—'}
                  </td>
                  <td className="num mono">{n.port}</td>
                  <td>
                    {n.isLocal
                      ? <span style={{ color: 'var(--ok)' }}>● self</span>
                      : <span style={{ color: 'var(--text3)' }}>—</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* v0.5.419 — resolved per-table shard policy. Operator
          confirms which expression each Distributed wrapper got
          without `SHOW CREATE TABLE` round-trips. */}
      {t.shardPolicy && Object.keys(t.shardPolicy).length > 0 && (
        <div style={{ marginTop: 12 }}>
          <div style={{
            fontSize: 10, fontWeight: 700,
            textTransform: 'uppercase', letterSpacing: 0.4,
            color: 'var(--text2)', marginBottom: 6,
          }}>
            Shard policy (resolved)
          </div>
          <div className="table-wrap is-fit">
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={shardDt} />
              <DataTableHead dt={shardDt} />
              <tbody>
                {shardDt.sortedRows.map(({ table, expr }) => (
                  <tr key={table}>
                    <td className="mono">{table}</td>
                    <td className="mono" style={{
                      fontSize: 11,
                      color: expr === 'rand()' ? 'var(--text3)' : 'var(--text)',
                    }}>{expr}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 6 }}>
            Resolution: <code>COREMETRY_CH_SHARD_KEY</code> env (uniform override) →
            built-in Datadog-style per-table defaults → <code>rand()</code>.
            Change requires a one-time <code>COREMETRY_CH_RESET_SCHEMA=1</code> boot
            since <code>ENGINE = Distributed(…, shard_key)</code> freezes the
            expression at table creation.
          </div>
        </div>
      )}
    </div>
  );
}

function MiniStat({ label, value, hint, cls }: {
  label: string; value: string; hint?: string; cls?: string;
}) {
  return (
    <div style={{
      padding: '8px 12px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: 16, fontWeight: 600, marginTop: 2,
        color: cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {hint && (
        <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 2 }}>{hint}</div>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 24 }}>
      <h3 style={{ fontSize: 13, fontWeight: 700, marginBottom: 8 }}>{title}</h3>
      {children}
    </div>
  );
}

function EmptyNote({ text }: { text: string }) {
  return (
    <div style={{
      padding: '14px 16px', borderRadius: 6,
      background: 'var(--bg2)', border: '1px dashed var(--border)',
      fontSize: 12, color: 'var(--text3)',
    }}>{text}</div>
  );
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '10px 14px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', textTransform: 'uppercase', letterSpacing: 0.4 }}>
        {label}
      </div>
      <div style={{
        fontSize: 22, fontWeight: 700, marginTop: 4,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>
      )}
    </div>
  );
}


// v0.5.439 — short age string for the stale-cache pill. Drops
// sub-second precision; rounds to the unit that reads cleanest
// in a chip.
function fmtAge(ms: number): string {
  if (ms < 60_000) return `${Math.round(ms / 1000)}s ago`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)}m ago`;
  return `${Math.round(ms / 3_600_000)}h ago`;
}
