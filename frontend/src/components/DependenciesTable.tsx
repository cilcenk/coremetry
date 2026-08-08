import { Fragment, useEffect, useMemo, useState, type ReactNode } from 'react';
import { messagingTracesHref, dbTracesHref } from '@/lib/pivotHref';
import { Link, useSearchParams } from 'react-router-dom';
import { Empty } from './Spinner';
import { Sparkline } from './Sparkline';
import { TrendDelta } from './TrendDelta';
import { Button } from './ui/Button';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { trendsEnabled, latencyPresent } from '@/lib/depsTable';
import { msgBalance, msgP99Delta, type MsgBalanceState } from '@/lib/msgBalance';
import { useDataTable, DataTableHead, DataTableColgroup } from './DataTable';
import { DetailDrawer } from '@/features/dependencies/DetailDrawer';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, DBTrend } from '@/lib/types';

// Row is the shape both /databases and /messaging hand to this
// component. We type-erase the row-specific labelling (Instance
// vs Destination, System vs System) via the props above; the
// table logic is otherwise identical.
export interface DepRow {
  system: string;
  // Messaging-only — physical cluster identifier. Empty / undefined
  // for DB rows. Surfaced as a Cluster column when any row in
  // the set has it populated so a Kafka deployment with one
  // cluster doesn't gain an empty column.
  cluster?: string;
  // EITHER instance (DB) OR destination (messaging). Both are
  // optional on the type so the caller can wire whichever it
  // has — the table renders whichever is non-empty.
  instance?: string;
  destination?: string;
  // v0.5.315 — per-database split. One DB host can serve many
  // databases (Oracle SIDs, PostgreSQL / MongoDB / MSSQL DBs).
  // When present, surface as a chip next to the instance so
  // operator sees (host, database) as a single addressable
  // unit instead of collapsing every DB on a host into one row.
  dbName?: string;
  spanCount: number;
  errorCount: number;
  errorRate: number;
  avgDurationMs: number;
  p99DurationMs: number;
  callers: string[];
  // source: where the row came from. 'receiver' rows are
  // surfaced with a badge and zero RED stats — the drill-down
  // panel (e.g. OracleDB receiver) is the actionable surface.
  source?: 'spans' | 'receiver';
  // v0.8.364 (Stage-2 M1) — messaging-only producer/consumer split,
  // p50, and prior-window deltas. The PAGE computes the /min rates
  // (it owns the window length); raw counts ride along for the
  // error-percentage tooltips. All optional — DB rows never set
  // them and the columns only render for kind='queue'.
  producePerMin?: number;
  consumePerMin?: number;
  produceCount?: number;
  consumeCount?: number;
  produceErrors?: number;
  consumeErrors?: number;
  p50DurationMs?: number;
  // v0.9.816 — kind'a ayrışmış p95: publish (üretim) ve process
  // (işleme) ayrı kolonlar. undefined = ölçüm yok → '—'.
  produceP95Ms?: number;
  consumeP95Ms?: number;
  // v0.9.259 — p95 rode the /api/messaging payload from the day the
  // MV shipped (dependencies.go:863, index 2 of the 3-wide state) and
  // was declared in types.ts, but DepRow never carried it so nothing
  // could render it. There is deliberately no priorP95Ms twin below:
  // MessagingInstance only computes PriorP50Ms / PriorP99Ms.
  p95DurationMs?: number;
  // Prior equal-length window (compare=prior) — undefined when the
  // row had no prior twin, so delta badges stay hidden.
  priorSpanCount?: number;
  priorErrorCount?: number;
  priorProducePerMin?: number;
  priorConsumePerMin?: number;
  priorAvgMs?: number;
  priorP50Ms?: number;
  priorP99Ms?: number;
}

type SortKey = 'system' | 'cluster' | 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99';
const NATURAL: Record<SortKey, 'asc' | 'desc'> = {
  system: 'asc', cluster: 'asc', name: 'asc', spanCount: 'desc',
  errorRate: 'desc', avg: 'desc', p99: 'desc',
};

// DependenciesTable renders the system+instance+RED+callers grid
// shared by /databases and /messaging. Kind controls the column
// header label and the click-through DSL pre-filter so a row
// click lands on /explore scoped to that system+instance.
export function DependenciesTable({
  rows, kind, range, compare, extraControls, openRowKey, onOpenRowChange,
}: {
  rows: DepRow[];
  // 'db' → uses instance + filters by db.system; 'queue' → uses
  // destination + filters by messaging.system.
  kind: 'db' | 'queue';
  // Time range — drives the detail drawer's per-(service, pod)
  // breakdown query. Same window the parent /databases or
  // /messaging page uses for the overview.
  range: TimeRange;
  // v0.8.364 (Stage-2 M1) — when true, rows carry prior* fields and
  // the metric cells render TrendDelta badges (endpoints pattern).
  compare?: boolean;
  // Extra page-owned controls (e.g. the "Compare vs prior" toggle)
  // rendered inside the filter row so the page keeps one strip.
  extraControls?: ReactNode;
  // v0.8.364 — controlled drawer mode for URL-first pages. When
  // onOpenRowChange is provided the parent owns which row is open
  // (openRowKey, same `system|cluster|name` shape as the internal
  // key) and receives the clicked row (null = close). Uncontrolled
  // pages (/databases) keep the internal useState behaviour.
  openRowKey?: string | null;
  onOpenRowChange?: (row: DepRow | null) => void;
}) {
  // v0.9.813 — System filtresi + arama URL'e taşındı (?msys= / ?q=).
  //
  // Bunlar sayfanın GÖRÜNÜMÜNÜ belirleyen seçimlerdi ama yalnız bileşen
  // state'inde yaşıyordu: kopyalanan bir link filtrelenmemiş tabloyu
  // açıyor, SavedViewsBar ise "kaydedilmiş görünüm" diye filtresiz bir
  // görünüm kaydediyordu — yani kaydedilen şey ekranda duran şey
  // DEĞİLDİ.
  //
  // YEREL AYNA YOK, bu yüzden sig-guard'a da gerek yok (v0.8.253'ün
  // konusu URL→state kopyalayan desendi): tek doğruluk kaynağı URL,
  // okuma doğrudan oradan. replace:true — her tuş vuruşu geçmişe
  // girmemeli.
  const [tparams, setTparams] = useSearchParams();
  const systemFilter = tparams.get('msys') ?? '';
  const search = tparams.get('q') ?? '';
  const setParam = (key: string, v: string) => setTparams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set(key, v); else next.delete(key);
    return next;
  }, { replace: true });
  const setSystemFilter = (v: string) => setParam('msys', v);
  const setSearch = (v: string) => setParam('q', v);
  // Which row's drawer is open. Stores `system|cluster|name` so the
  // drawer survives sort + filter changes (stable identifiers).
  // Controlled mode (v0.8.364) hands ownership to the parent so
  // /messaging can drive it from the ?destination= URL param.
  const [openKeyState, setOpenKeyState] = useState<string | null>(null);
  const controlled = onOpenRowChange !== undefined;
  const openKey = controlled ? (openRowKey ?? null) : openKeyState;
  const setOpen = (row: DepRow | null, key: string | null) => {
    if (controlled) onOpenRowChange!(row);
    else setOpenKeyState(key);
  };
  // #1 sparkline + #6 health-chip source. One DBTrend per
  // (dbSystem, instance, dbName); we join to the overview rows
  // by (system, instance, dbName) — see trendFor below. null =
  // backend returned null / fetch failed (render the '—'
  // placeholder), undefined = not yet loaded.
  const [trends, setTrends] = useState<Map<string, DBTrend> | null | undefined>(undefined);

  const systems = useMemo(() => {
    const s = new Set<string>();
    for (const r of rows) s.add(r.system);
    return Array.from(s).sort();
  }, [rows]);
  // Show the Cluster column only when at least one row carries
  // a non-default cluster. Single-Kafka-cluster deployments
  // skip the column entirely.
  const hasClusterCol = useMemo(
    () => rows.some(r => r.cluster && r.cluster !== '(default)'),
    [rows]);

  // v0.5.349 — when instance is the legacy 'unknown' sentinel
  // and a real db.name is available, surface the db.name as
  // the primary label. The MV migration in store.go rewrites
  // 'unknown' → server.address / net.peer.name / etc. for
  // fresh data, but past 5-min buckets keep the literal
  // 'unknown' until they roll out of the retention window.
  // This client-side fallback gives operators readable labels
  // on existing data immediately.
  const nameOf = (r: DepRow) => {
    const inst = r.instance ?? r.destination ?? '';
    if (inst === 'unknown' && r.dbName && r.dbName !== 'default') {
      return r.dbName;
    }
    return inst;
  };

  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    return rows.filter(r => {
      if (systemFilter && r.system !== systemFilter) return false;
      if (term) {
        return r.system.toLowerCase().includes(term)
            || (r.cluster ?? '').toLowerCase().includes(term)
            || nameOf(r).toLowerCase().includes(term)
            || r.callers.some(c => c.toLowerCase().includes(term));
      }
      return true;
    });
  }, [rows, systemFilter, search]);

  // Shared sortable + resizable table. Columns built per-render so the
  // name label (Instance vs Destination) + the optional Cluster column
  // track kind/hasClusterCol. Accessors mirror the prior sort keys.
  const depCols = useMemo<DataTableColumn<DepRow>[]>(() => [
    { id: 'system', label: 'System', sortValue: r => r.system, naturalDir: NATURAL.system, width: 150 },
    ...(hasClusterCol
      ? [{ id: 'cluster', label: 'Cluster', sortValue: (r: DepRow) => r.cluster ?? '', naturalDir: NATURAL.cluster, width: 120 } as DataTableColumn<DepRow>]
      : []),
    // db.name only makes sense for databases — Kafka/RabbitMQ/etc.
    // (kind === 'queue') have no db.name, so the column is db-only.
    // v0.8.368 (operator-requested): Database sits BEFORE Instance —
    // System | Database | Instance | Calls reads engine → logical DB
    // → physical host, matching how operators scan the page.
    ...(kind === 'db'
      ? [{ id: 'database', label: 'Database', sortValue: (r: DepRow) => r.dbName ?? '', naturalDir: 'asc', width: 120 } as DataTableColumn<DepRow>]
      : []),
    { id: 'name', label: kind === 'db' ? 'Instance' : 'Destination', sortValue: r => nameOf(r), naturalDir: NATURAL.name, width: 210 },
    { id: 'spanCount', label: 'Calls', sortValue: r => r.spanCount, numeric: true, naturalDir: NATURAL.spanCount, width: 96 },
    // v0.8.364 (Stage-2 M1) — messaging-only producer/consumer
    // split. Rates are precomputed by the page (it owns the window
    // length); zero-produce / zero-consume destinations sort to the
    // bottom naturally.
    ...(kind === 'queue'
      ? [
          { id: 'produce', label: 'Produce/min', sortValue: (r: DepRow) => r.producePerMin ?? 0, numeric: true, naturalDir: 'desc', width: 116 } as DataTableColumn<DepRow>,
          { id: 'consume', label: 'Consume/min', sortValue: (r: DepRow) => r.consumePerMin ?? 0, numeric: true, naturalDir: 'desc', width: 116 } as DataTableColumn<DepRow>,
          // v0.9.815 — Denge. Produce/min ile Consume/min YAN YANA iki
          // sayıydı ve aralarındaki ilişkiyi kurmayı operatörün gözüne
          // bırakıyordu: 1.240 ile 1.190 arasındaki farkın önemli olup
          // olmadığı iki sayıya bakarak anlaşılmaz. Bu kolon farkı orana
          // çevirip eşiğe vurur. sortValue = oran, yani DESC sıralama
          // "en çok birikenler" demek; ölçülemeyen satırlar null ile
          // en alta düşer.
          { id: 'balance', label: 'Denge', sortValue: (r: DepRow) => msgBalance(r.produceCount, r.consumeCount).ratio, naturalDir: 'desc', width: 118 } as DataTableColumn<DepRow>,
          // v0.9.815 — P99 Δ. compare=prior açıkken önceki eşit pencereye
          // göre kötüleşme oranı; kapalıyken HER satır null döner, sortRows
          // hepsini kararlı bırakır ve sıra sunucudan gelen spanCount DESC
          // olarak kalır (brief'teki "prior yoksa spanCount DESC'e düş").
          { id: 'p99delta', label: 'P99 Δ', sortValue: (r: DepRow) => msgP99Delta(r.p99DurationMs, r.priorP99Ms), numeric: true, naturalDir: 'desc', width: 92 } as DataTableColumn<DepRow>,
          // v0.9.816 — GECİKME AYRIŞMASI. Aşağıdaki tek P95 kolonu
          // üretici + tüketici span'lerini TEK dağılımda topluyor:
          // publish (broker'a yazma, hızlı) ile process (iş mantığı,
          // yavaş) aynı sayıda eriyor ve "bu topic yavaş" deyip NEREDE
          // yavaş olduğunu söylemiyor. Bu ikisi soruyu bitiriyor.
          // sortValue null → ölçümsüz satırlar en alta (0 DEĞİL: 0 ms
          // "anında" diye okunurdu, v0.9.262 dersi).
          { id: 'producep95', label: 'Üretim P95', sortValue: (r: DepRow) => r.produceP95Ms ?? null, numeric: true, naturalDir: 'desc', width: 104 } as DataTableColumn<DepRow>,
          { id: 'consumep95', label: 'İşleme P95', sortValue: (r: DepRow) => r.consumeP95Ms ?? null, numeric: true, naturalDir: 'desc', width: 104 } as DataTableColumn<DepRow>,
        ]
      : []),
    { id: 'errorRate', label: 'Err %', sortValue: r => r.errorRate, numeric: true, naturalDir: NATURAL.errorRate, width: 96 },
    { id: 'avg', label: 'Avg', sortValue: r => r.avgDurationMs, numeric: true, naturalDir: NATURAL.avg, width: 90 },
    // P50 + P95 alongside P99. v0.8.364 added P50 for queues only; v0.9.259
    // added P95 there; v0.9.262 opens both to the DB grid too, since
    // db_summary_5m carries the identical 3-wide TDigest state and
    // GetDatabases now projects indices 1 and 2 off the same merge — no extra
    // ClickHouse scan on either page. Order reads Avg → P50 → P95 → P99.
    { id: 'p50', label: 'P50', sortValue: (r: DepRow) => r.p50DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p95', label: 'P95', sortValue: (r: DepRow) => r.p95DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p99', label: 'P99', sortValue: r => r.p99DurationMs, numeric: true, naturalDir: NATURAL.p99, width: 90 },
    // #1 — non-sortable RED sparkline column. No sortValue so the
    // shared DataTable head renders it as a plain (un-clickable)
    // header. Body cell joins the row to its DBTrend via trendFor.
    //
    // v0.9.258 — DB-only. The trend data comes from
    // /api/databases/trends → db_summary_5m, whose MV is defined
    // `WHERE db_system != ''` (store.go:2513). A messaging span
    // carries messaging.system and leaves db_system empty, so a
    // queue row's join key (`kafka|orders-topic|`) can never exist
    // in that table. On /messaging this column rendered the muted
    // '—' 100% of the time while the page paid a LIMIT 200000 scan
    // for it on every range change.
    ...(trendsEnabled(kind)
      ? [{ id: 'trend', label: 'Trend', width: 140 } as DataTableColumn<DepRow>]
      : []),
    { id: 'callers', label: 'Top callers', width: 240 },
    // eslint-disable-next-line react-hooks/exhaustive-deps
  ], [hasClusterCol, kind]);

  const dt = useDataTable<DepRow>({
    storageKey: `deps-${kind}`,
    columns: depCols,
    rows: filtered,
    // v0.9.815 — /messaging varsayılanı "KÖTÜLEŞENLER ÖNCE" (Endpoints
    // v0.9.761 kararının messaging'e uygulanmışı): sayfayı açma sebebi
    // neredeyse hep "ne bozuldu", hacim bir tık uzakta.
    //
    // Fallback AYRI BİR KOD YOLU DEĞİL: p99delta'nın sortValue'su prior
    // yokken null döner, sortRows null'ları en alta koyar ve kendi
    // aralarında GELDİĞİ sırayı korur — yani sunucudan gelen spanCount
    // DESC. compare kapalıyken TÜM satırlar null olur ve tablo bugünkü
    // sırasında kalır.
    //
    // /databases DEĞİŞMEDİ: p99delta kolonu yalnız kind='queue' için
    // kuruluyor, DB tarafı spanCount varsayılanını koruyor.
    initialSort: kind === 'queue'
      ? { id: 'p99delta', dir: 'desc' }
      : { id: 'spanCount', dir: 'desc' },
    // v0.9.404 (desen paritesi) — j/k/Enter klavye gezinmesi: Enter satır
    // drawer'ını Endpoints'teki gibi açar/kapar. /databases + /messaging
    // bu tablo üzerinden aynı anda kazandı.
    onOpen: (row) => {
      const rowKey = `${row.system}|${row.cluster ?? ''}|${nameOf(row)}`;
      setOpen(openKey === rowKey ? null : row, openKey === rowKey ? null : rowKey);
    },
  });

  // Click-through DSL — pre-filters /explore by the chosen
  // system + instance. For DBs the key is db.system; for
  // messaging it's messaging.system + messaging.destination.name.
  const exploreHref = (r: DepRow) => {
    if (kind === 'db') {
      // v0.9.268 — this link was DEAD for any row the MV named from a rung
      // below peer_service, and it is the unfixed sibling of the messaging
      // bug repaired in v0.9.256, in this very function. It hard-coded
      // `peer.service = <instance>` while db_summary_5m resolves instance
      // through a SIX-way coalesce. Measured on live data: the clickhouse
      // row had 2201 spans in a 30-minute window and the link matched 0.
      // It also carried no window, so the destination fell back to its own
      // default range. Both fixed by the shared helper.
      return dbTracesHref({
        window: range,
        system: r.system,
        instance: r.instance ?? '',
        dbName: r.dbName,
      });
    }
    // v0.9.256 (operatör: "messaging kısmında tracelere erişemiyorum") —
    // bu link ÖLÜYDÜ. Yalnız `messaging.destination.name` filtreliyordu; MV
    // ise üç kademeli coalesce ile üretiyor ve canlı veride o alan SIFIR
    // satır (eski `messaging.destination` 1280). Üstüne `range=` de
    // taşımıyordu, yani hedef kendi 30dk varsayılanına düşüyordu. Artık
    // paylaşılan helper: üç adın hepsini OR'luyor + pencereyi taşıyor +
    // /traces'e gidiyor (opak /explore DSL'i yerine, orada operatör
    // filtreleri çip olarak GÖRÜP düzenleyebiliyor).
    return messagingTracesHref({
      window: range,
      system: r.system,
      destination: r.destination ?? '',
    });
  };

  // #1 + #6 — fetch per-row RED trends on range change. Kept
  // unconditional (above the rows.length===0 early return) so the
  // hook order is stable. Stores two keys per trend in the Map:
  // the precise (system|instance|dbName) and a looser
  // (system|instance) fallback — db.name on the trend ('' /
  // 'default') doesn't always line up with the row's dbName, so
  // trendFor tries exact first then falls back to (system,
  // instance). cluster is empty for DB rows so it isn't part of
  // the join.
  useEffect(() => {
    let live = true;
    // v0.9.258 — the fetch is DB-only, but the hook itself stays
    // unconditional (see the stable-hook-order note above). null,
    // not undefined: undefined is the "loading" state and would
    // park a spinner on a column that isn't even rendered.
    if (!trendsEnabled(kind)) { setTrends(null); return; }
    setTrends(undefined);
    const { from, to } = timeRangeToNs(range);
    // v0.9.434 — endpoint kind'a göre: db → databases/trends (davranış
    // bayt-bayt eski), queue → messaging/trends (yeni ikiz). Join
    // anahtarları da kind'a göre: messaging kimliği (system, cluster,
    // destination) — cluster'sız loose anahtar farklı cluster'daki aynı
    // destination'ı ezerdi.
    const fetchTrends = kind === 'db' ? api.dbTrends : api.msgTrends;
    fetchTrends(from, to)
      .then(list => {
        if (!live) return;
        if (!list) { setTrends(null); return; }
        const m = new Map<string, DBTrend>();
        for (const t of list) {
          if (kind === 'db') {
            m.set(`${t.dbSystem}|${t.instance}|${t.dbName}`, t);
            // Looser fallback key — first writer wins so a real
            // db.name'd trend isn't clobbered by a 'default' sibling.
            const loose = `${t.dbSystem}|${t.instance}`;
            if (!m.has(loose)) m.set(loose, t);
          } else {
            m.set(`${t.dbSystem}|${t.cluster}|${t.instance}`, t);
          }
        }
        setTrends(m);
      })
      .catch(() => { if (live) setTrends(null); });
    return () => { live = false; };
  }, [range, kind]);

  // trendFor — join one overview row to its DBTrend. Match on
  // (system, instance, dbName) exactly; fall back to
  // (system, instance) when dbName doesn't line up (trend's
  // db.name may be '' / 'default' while the row carries a real
  // one, or vice-versa). nameOf(r) is the instance/destination.
  const trendFor = (r: DepRow): DBTrend | undefined => {
    if (!trends) return undefined;
    if (kind === 'queue') {
      // v0.9.434 — messaging kimliği (system, cluster, destination).
      return trends.get(`${r.system}|${r.cluster ?? ''}|${r.destination ?? ''}`);
    }
    const inst = nameOf(r);
    const db = r.dbName ?? '';
    return trends.get(`${r.system}|${inst}|${db}`)
        ?? trends.get(`${r.system}|${inst}`);
  };

  if (rows.length === 0) {
    return (
      <Empty icon="◯" title={kind === 'db'
        ? 'No database calls in this window'
        : 'No messaging activity in this window'}>
        {kind === 'db'
          ? 'Coremetry derives this view from spans with a populated db.system attribute.'
          : 'Derived from spans with a populated messaging.system attribute.'}
      </Empty>
    );
  }

  return (
    <>
      <div className="controls" style={{ marginBottom: 12, flexWrap: 'wrap' }}>
        <span style={{ color: 'var(--text2)', fontSize: 12 }}>System:</span>
        <select value={systemFilter} onChange={e => setSystemFilter(e.target.value)}
                style={{ fontSize: 12 }}>
          <option value="">All ({rows.length})</option>
          {systems.map(s => <option key={s} value={s}>{s}</option>)}
        </select>
        <input value={search} onChange={e => setSearch(e.target.value)}
               placeholder={kind === 'db'
                 ? 'Search system / instance / caller…'
                 : 'Search system / destination / caller…'}
               style={{ width: 280 }} />
        {extraControls}
        <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 'auto' }}>
          {dt.sortedRows.length} of {rows.length}
        </span>
      </div>

      <div className="table-wrap">
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} leading={[24]} />
          <DataTableHead dt={dt} leading={<th style={{ width: 24 }} aria-label="Expand"></th>} />
          <tbody>
            {dt.sortedRows.map((r, i) => {
              const errCls = r.errorRate > 5 ? 'err' : r.errorRate > 0 ? 'warn' : 'ok';
              // Key includes cluster so two rows with the same
              // (system, destination) but different physical
              // Kafka clusters don't collide in expansion state.
              const rowKey = `${r.system}|${r.cluster ?? ''}|${nameOf(r)}`;
              const isOpen = openKey === rowKey;
              return (
                <Fragment key={`${rowKey}|${i}`}>
                  <tr onClick={() => setOpen(isOpen ? null : r, isOpen ? null : rowKey)}
                      style={{ cursor: 'pointer',
                               // scale-audit v0.8.203 — skip off-screen rows
                               // (matches the instance table below); at a bank
                               // with many DB schemas this list reaches 1000s.
                               contentVisibility: 'auto', containIntrinsicSize: 'auto 32px',
                               background: isOpen ? 'var(--bg2)' : undefined }}>
                    <td style={{ color: 'var(--text3)', width: 24, textAlign: 'center' }}>
                      {isOpen ? '▾' : '▸'}
                    </td>
                    <td>
                      <SystemBadge system={r.system} kind={kind} />
                      {r.source === 'receiver' && (
                        <span title="Discovered via OpenTelemetry database receiver (oracledb / postgres / mysql / …). No application spans yet — drill down to see receiver metrics directly."
                              style={{
                                marginLeft: 6, fontSize: 9, padding: '1px 6px',
                                borderRadius: 3, fontWeight: 600,
                                background: 'color-mix(in srgb, var(--accent) 15%, transparent)',
                                color: 'var(--accent2)',
                                fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                                textTransform: 'uppercase', letterSpacing: '.5px',
                                verticalAlign: 'middle',
                              }}>via receiver</span>
                      )}
                    </td>
                    {hasClusterCol && (
                      <td style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text2)' }}>
                        {r.cluster === '(default)' ? (
                          <span style={{ color: 'var(--text3)' }}>—</span>
                        ) : (
                          r.cluster
                        )}
                      </td>
                    )}
                    {/* v0.5.315 / dedicated Database column — one host
                        serving N databases (Oracle SID/service,
                        PostgreSQL / MongoDB / MSSQL DB) → row is keyed
                        on (host, dbName). Surfaced as its own column
                        (was an inline ⛁ chip) so the operator can scan
                        + sort by database. '—' for the 'default'
                        fallback (OTel instrumentation didn't emit
                        db.name). v0.8.368: rendered BEFORE Instance to
                        match the reordered header. */}
                    {kind === 'db' && (
                      <td>
                        {r.dbName && r.dbName !== 'default' ? (
                          <span title={`db.name = ${r.dbName}`}
                            style={{
                              fontSize: 10,
                              padding: '1px 6px', borderRadius: 3,
                              background: 'var(--bg3)',
                              border: '1px solid var(--border)',
                              color: 'var(--text2)',
                              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                              verticalAlign: 'middle',
                            }}>
                            ⛁ {r.dbName}
                          </span>
                        ) : (
                          <span style={{ color: 'var(--text3)' }}>—</span>
                        )}
                      </td>
                    )}
                    <td onClick={e => e.stopPropagation()}>
                      <Link to={exploreHref(r)}
                            style={{ fontFamily: 'monospace', fontSize: 12, fontWeight: 500 }}
                            title={r.instance === 'unknown'
                              ? `peer.service was empty on these spans — label sourced from ${r.dbName && r.dbName !== 'default' ? 'db.name' : 'fallback'}`
                              : kind === 'queue'
                                ? 'Open the traces on this topic (same window, editable filter chips)'
                                : 'Open in Explore (spans pre-filtered)'}>
                        {nameOf(r) || <span style={{ color: 'var(--text3)' }}>(anonymous)</span>}
                      </Link>
                      {/* v0.9.257 — the destination label WAS the only
                          trace pivot on the row, and a bare monospace
                          name does not read as an action; the tooltip
                          still said "Explore" after v0.9.256 moved the
                          target to /traces. The glyph makes the
                          affordance visible without adding a column. */}
                      {kind === 'queue' && (
                        <span aria-hidden style={{ marginLeft: 4, fontSize: 10, color: 'var(--text3)' }}>↗</span>
                      )}
                      {/* v0.5.349 — surface a small chip when the
                          label is a fallback so the operator
                          knows the peer.service attribute is
                          missing on the source spans (actionable:
                          tell the SDK team to set it). Hidden
                          for fresh rows that carry a real
                          instance identifier. */}
                      {r.instance === 'unknown' && (
                        <span title="peer.service missing on these spans — backend fell back to db.name / host / service_name"
                          style={{
                            marginLeft: 6, fontSize: 9,
                            padding: '1px 5px', borderRadius: 3,
                            background: 'var(--bg3)',
                            border: '1px solid var(--border)',
                            color: 'var(--text3)',
                            verticalAlign: 'middle',
                            fontWeight: 700,
                          }}>
                          fallback
                        </span>
                      )}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {fmtNum(r.spanCount)}
                      {compare && <TrendDelta cur={r.spanCount} prior={r.priorSpanCount} kind="neutral" />}
                    </td>
                    {/* v0.8.364 (Stage-2 M1) — producer/consumer split.
                        Per-kind error % badge (scale-free, same
                        semantics as the row's Err % column) instead of
                        raw error counts; the tooltip carries the raw
                        numbers for the postmortem screenshot. */}
                    {kind === 'queue' && (
                      <>
                        <KindRateCell perMin={r.producePerMin} count={r.produceCount}
                          errors={r.produceErrors} priorPerMin={r.priorProducePerMin}
                          compare={compare} what="produce" />
                        <KindRateCell perMin={r.consumePerMin} count={r.consumeCount}
                          errors={r.consumeErrors} priorPerMin={r.priorConsumePerMin}
                          compare={compare} what="consume" />
                        <BalanceCell produce={r.produceCount} consume={r.consumeCount} />
                        <P99DeltaCell cur={r.p99DurationMs} prior={r.priorP99Ms} compare={compare} />
                        {/* v0.9.816 — ayrışmış p95. '—' iki ayrı yokluğu
                            kapsar: bu pencerede o kind'da span yok, ya da
                            payload eski (rolling deploy). İkisi de "0 ms"
                            DEĞİL. */}
                        <KindP95Cell v={r.produceP95Ms} what="üretim (publish)" />
                        <KindP95Cell v={r.consumeP95Ms} what="işleme (process)" />
                      </>
                    )}
                    <td className="mono" style={{ textAlign: 'right' }}>
                      <span className={`badge b-${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                      {compare && <TrendDelta cur={r.errorCount} prior={r.priorErrorCount} kind="lowerBetter" />}
                    </td>
                    {/* v0.9.262 — every latency cell goes through LatencyCell,
                        which renders '—' for a receiver-discovered row. Those
                        rows are built from metric_points and carry NO duration
                        data; the Go fields are plain float64, so they marshal
                        as 0 and used to print "0.0ms" — a database with no
                        application traffic reading as instant. The badge next
                        to the system name said "receiver", but the numbers
                        contradicted it. Adding P50/P95 would have doubled that
                        lie, so all four cells are fixed together (SENTEZ #5,
                        "receiver RED'i —"). */}
                    <LatencyCell v={r.avgDurationMs} row={r}
                      delta={compare && <TrendDelta cur={r.avgDurationMs} prior={r.priorAvgMs} kind="lowerBetter" />} />
                    <LatencyCell v={r.p50DurationMs} row={r}
                      delta={compare && r.p50DurationMs !== undefined
                        && <TrendDelta cur={r.p50DurationMs} prior={r.priorP50Ms} kind="lowerBetter" />} />
                    {/* No TrendDelta on P95 on purpose: neither MessagingInstance
                        nor DBInstance computes a PriorP95Ms (only P50/P99), so
                        binding one would imply a comparison the data can't make. */}
                    <LatencyCell v={r.p95DurationMs} row={r} />
                    <LatencyCell v={r.p99DurationMs} row={r}
                      delta={compare && <TrendDelta cur={r.p99DurationMs} prior={r.priorP99Ms} kind="lowerBetter" />} />
                    {/* #1 Trend + #6 health chips. Sparkline plots
                        the call-rate (rps) over the window; it flips
                        red/amber when the trend's CURRENT error rate
                        is elevated so a row reads "unhealthy" without
                        a mouse-over. Under it, compact p99 + err
                        chips surface the latest-bucket gauge. '—'
                        when no trend joins (or trends failed/loading). */}
                    {trendsEnabled(kind) && (
                      <td onClick={e => e.stopPropagation()}>
                        <TrendCell trend={trendFor(r)} loading={trends === undefined} />
                      </td>
                    )}
                    <td style={{ fontSize: 11 }} onClick={e => e.stopPropagation()}>
                      {r.callers.length === 0
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : r.callers.slice(0, 3).map((c, idx) => (
                            <span key={c}>
                              <Link to={`/service?name=${encodeURIComponent(c)}`}
                                    style={{ fontFamily: 'monospace' }}>{c}</Link>
                              {idx < Math.min(2, r.callers.length - 1) && <span style={{ color: 'var(--text3)' }}>, </span>}
                            </span>
                          ))}
                      {r.callers.length > 3 && (
                        <span style={{ color: 'var(--text3)' }}> +{r.callers.length - 3}</span>
                      )}
                    </td>
                  </tr>
                  {isOpen && (
                    <tr>
                      {/* v0.9.258 — derived from the column list instead of
                          hand-counted. The old arithmetic (base 9 + cluster
                          + Database + produce/consume/p50) was already one
                          too high — db without cluster gave 10 for 9 real
                          columns — which browsers silently clamp, so it
                          never showed. Making the Trend column conditional
                          would have skewed it again; depCols is the single
                          source the header and colgroup already use. */}
                      <td colSpan={depCols.length} style={{
                        background: 'var(--bg1)', padding: '12px 16px',
                        borderTop: '1px solid var(--border)',
                      }}>
                        {/* v0.8.364 — explicit ✕ affordance (pairs with
                            the page-level Esc handler on /messaging;
                            row re-click still toggles too). */}
                        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 2 }}>
                          <Button variant="ghost" size="sm" aria-label="Close detail"
                            title="Close detail (Esc)"
                            onClick={() => setOpen(null, null)}>✕</Button>
                        </div>
                        <DetailDrawer
                          system={r.system}
                          cluster={r.cluster ?? '(default)'}
                          name={nameOf(r)}
                          kind={kind}
                          source={r.source ?? 'spans'}
                          range={range} />
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}

// fmtPerMin — Produce/min · Consume/min cells (v0.8.364). Sub-10
// rates keep one decimal so a trickle topic doesn't read "0";
// larger rates round to locale ints (the endpoints fmtRate shape).
// '—' when the backend predates the split (rolling deploy).
function fmtPerMin(v?: number): string {
  if (v === undefined || v === null) return '—';
  return v < 10 ? v.toFixed(1) : fmtNum(Math.round(v));
}

// KindRateCell — one Produce/min or Consume/min cell (v0.8.364,
// Stage-2 M1). Renders the rate, a per-kind error % badge when that
// side errored (percentage, not raw count: scale-free and it reads
// on the same axis as the row's Err % column — the raw counts live
// in the tooltip), and the compare=prior delta badge.
function KindRateCell({ perMin, count, errors, priorPerMin, compare, what }: {
  perMin?: number;
  count?: number;
  errors?: number;
  priorPerMin?: number;
  compare?: boolean;
  what: 'produce' | 'consume';
}) {
  const errPct = count && count > 0 && errors ? (errors / count) * 100 : 0;
  const errTone = errPct > 5 ? 'err' : errPct > 0 ? 'warn' : null;
  return (
    <td className="mono" style={{ textAlign: 'right' }}>
      {fmtPerMin(perMin)}
      {errTone && (
        <span className={`badge b-${errTone}`} style={{ marginLeft: 4, fontSize: 9 }}
          title={`${fmtNum(errors ?? 0)} of ${fmtNum(count ?? 0)} ${what} spans errored`}>
          {errPct.toFixed(1)}%
        </span>
      )}
      {compare && <TrendDelta cur={perMin ?? 0} prior={priorPerMin} kind="neutral" />}
    </td>
  );
}

// KindP95Cell — kind'a ayrışmış p95 hücresi (v0.9.816).
//
// undefined → '—', ASLA "0.0ms". İki ayrı yokluk aynı işarete iner
// (bu pencerede o kind'da span yok / payload eski bir backend'den) ve
// ikisi de "anında tamamlandı" demek değil — LatencyCell'in v0.9.262'de
// öğrendiği ders, yeni kolonlarda tekrarlanmasın.
function KindP95Cell({ v, what }: { v?: number; what: string }) {
  if (v === undefined || v === null || !(v > 0)) {
    return (
      <td className="mono" style={{ textAlign: 'right' }}>
        <span style={{ color: 'var(--text3)' }}
          title={`Bu pencerede ${what} span'i ölçülmedi.`}>—</span>
      </td>
    );
  }
  return (
    <td className="mono" style={{ textAlign: 'right' }}
      title={`${what} span süresi, p95`}>
      {v.toFixed(1)}ms
    </td>
  );
}

// BALANCE_UI — Denge durumunun tek görsel sözlüğü (v0.9.815). Çip
// metni DURUMU söyler, sayıyı değil: "%12" tek başına yön taşımıyor,
// "birikiyor" taşıyor. Oranın kendisi tooltip'te ve sıralamada.
const BALANCE_UI: Record<MsgBalanceState, { label: string; glyph: string; tone: string }> = {
  accumulating: { label: 'birikiyor', glyph: '▲', tone: 'warn' },
  balanced:     { label: 'dengede',   glyph: '=', tone: 'ok' },
  draining:     { label: 'boşalıyor', glyph: '▼', tone: 'gray' },
  unknown:      { label: '—',         glyph: '',  tone: 'gray' },
};

// BalanceCell — üretim/tüketim dengesi çipi.
//
// "boşalıyor" UYARI DEĞİL: tüketimin üretimi geçmesi normalde iyi
// haberdir (birikmiş iş eriyor) ya da pencere sınırı artefaktıdır
// (üretim pencereden önce olmuş). Yalnız "birikiyor" sarı — backlog
// büyüyor demek.
function BalanceCell({ produce, consume }: { produce?: number; consume?: number }) {
  const b = msgBalance(produce, consume);
  const ui = BALANCE_UI[b.state];
  if (b.state === 'unknown') {
    return (
      <td>
        <span style={{ color: 'var(--text3)' }}
          title="Bu satırda producer/consumer ayrımı oluşmadı — span'ler kind taşımıyor ya da yalnız broker chatter'ı var.">—</span>
      </td>
    );
  }
  const pct = Math.abs((b.ratio ?? 0) * 100);
  return (
    <td>
      <span className={`badge b-${ui.tone}`} style={{ textTransform: 'none', letterSpacing: 0 }}
        title={`üretim ${Math.round(produce ?? 0).toLocaleString()} · tüketim ${Math.round(consume ?? 0).toLocaleString()} span — (üretim−tüketim)/üretim = ${((b.ratio ?? 0) * 100).toFixed(1)}%. Bu bir consumer LAG değil: broker metriği ingest edilmiyor, oran yalnız span sayılarından.`}>
        {ui.glyph} {ui.label}{pct >= 1 ? ` ${pct.toFixed(0)}%` : ''}
      </span>
    </td>
  );
}

// P99DeltaCell — önceki eşit pencereye göre p99 kötüleşmesi.
// compare kapalıyken prior HİÇ gelmez, o yüzden hücre '—' der ve
// "değişmedi" demez: ölçülmeyen ile değişmeyen aynı şey değil.
function P99DeltaCell({ cur, prior, compare }: {
  cur: number; prior?: number; compare?: boolean;
}) {
  const d = msgP99Delta(cur, prior);
  if (d === null) {
    return (
      <td className="mono" style={{ textAlign: 'right' }}>
        <span style={{ color: 'var(--text3)' }}
          title={compare
            ? 'Önceki pencerede bu destination yok — karşılaştırılacak taban değeri de yok.'
            : 'Karşılaştırma kapalı. "Compare vs prior" ile önceki eşit pencereye göre kötüleşme gelir.'}>—</span>
      </td>
    );
  }
  const pct = d * 100;
  const tone = pct >= 20 ? 'var(--err)' : pct >= 5 ? 'var(--warn)' : pct <= -5 ? 'var(--ok)' : 'var(--text2)';
  return (
    <td className="mono" style={{ textAlign: 'right', color: tone }}
      title={`p99 ${cur.toFixed(1)}ms · önceki ${(prior ?? 0).toFixed(1)}ms`}>
      {pct > 0 ? '+' : ''}{Math.abs(pct) < 10 ? pct.toFixed(1) : pct.toFixed(0)}%
    </td>
  );
}

// LatencyCell — one right-aligned monospace duration cell, with the honest
// no-data case built in (v0.9.262).
//
// Two distinct absences collapse to the same '—':
//   · source === 'receiver' — the row came from discoverReceiverInstances,
//     which reads metric_points and has no duration data at all. The Go
//     fields are plain float64, so they marshal as 0 and the cell used to
//     print "0.0ms": a database with zero application traffic rendering as
//     the fastest row on the page, directly contradicting its own badge.
//   · undefined — a warm cached payload from a backend that predates the
//     field (rolling deploy).
//
// Both mean "no measurement", and neither is 0 ms.
function LatencyCell({ v, row, delta }: {
  v?: number;
  row: DepRow;
  delta?: React.ReactNode;
}) {
  const present = latencyPresent(row.source, v);
  return (
    <td className="mono" style={{ textAlign: 'right' }}>
      {present
        ? <>{v.toFixed(1)}ms</>
        : <span style={{ color: 'var(--text3)' }}
                title={row.source === 'receiver'
                  ? 'Receiver-discovered — no application spans, so no latency measurement'
                  : 'Not reported by this backend version'}>—</span>}
      {present && delta}
    </td>
  );
}

// TrendCell renders the #1 RED sparkline + #6 latest-bucket
// health chips for one overview row. Plots the call-rate (rps)
// series; the sparkline tints red when the trend's CURRENT error
// rate is high (>5) / amber (>1) reusing the badge tone vars.
// Under the sparkline, compact p99 + err chips surface the
// latest-bucket gauge (curP99Ms tinted by threshold; the err
// chip only shows when curErrorRate > 0). A missing trend (no
// join / failed / still loading) renders a muted '—'.
function TrendCell({ trend, loading }: {
  trend: DBTrend | undefined;
  loading: boolean;
}) {
  if (!trend) {
    return (
      <span title={loading ? 'loading trend…' : 'no trend in this window'}
        style={{ color: 'var(--text3)', fontSize: 11 }}>—</span>
    );
  }
  const rps = trend.points.map(p => p.rps);
  // Health tone from the latest-bucket gauge — drives both the
  // sparkline colour and the err chip. Mirrors the row's errCls
  // thresholds (>5 err, >0 warn) so the eye doesn't recalibrate.
  const errTone: 'err' | 'warn' | 'ok' =
    trend.curErrorRate > 5 ? 'err'
    : trend.curErrorRate > 1 ? 'warn' : 'ok';
  const sparkColor =
    errTone === 'err' ? 'var(--err)'
    : errTone === 'warn' ? 'var(--warn)'
    : undefined; // undefined → Sparkline's default --accent2
  // p99 chip tone — same ms thresholds the drawer's Stat tiles
  // use elsewhere wouldn't fit (those are domain-specific); a
  // generic latency band reads fine here: >500ms err, >200ms warn.
  const p99Tone: 'err' | 'warn' | 'ok' =
    trend.curP99Ms > 500 ? 'err'
    : trend.curP99Ms > 200 ? 'warn' : 'ok';
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
      <Sparkline
        values={rps}
        width={120}
        height={20}
        color={sparkColor}
        unit="/s"
        title={`call-rate · cur ${trend.curRps.toFixed(1)}/s`} />
      <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
        <span className={`badge b-${p99Tone}`} style={{ fontSize: 9 }}
          title="latest-bucket p99">
          {trend.curP99Ms.toFixed(0)}ms
        </span>
        {trend.curErrorRate > 0 && (
          <span className={`badge b-${errTone}`} style={{ fontSize: 9 }}
            title="latest-bucket error rate">
            {trend.curErrorRate.toFixed(1)}%
          </span>
        )}
      </div>
    </div>
  );
}

// SystemBadge renders the system name in its conventional colour
// — Postgres blue, Redis red, Kafka dark, etc. — so an operator
// scanning the list recognises the technology at a glance.
function SystemBadge({ system, kind }: { system: string; kind: 'db' | 'queue' }) {
  const s = system.toLowerCase();
  const tone: Record<string, { bg: string; fg: string }> = {
    postgresql: { bg: 'rgba(51,103,145,0.18)', fg: '#5b8fb9' },
    postgres:   { bg: 'rgba(51,103,145,0.18)', fg: '#5b8fb9' },
    mysql:      { bg: 'rgba(0,117,143,0.18)',  fg: '#21a0a0' },
    mariadb:    { bg: 'rgba(0,117,143,0.18)',  fg: '#21a0a0' },
    oracle:     { bg: 'rgba(216,72,57,0.18)',  fg: '#d84839' },
    redis:      { bg: 'rgba(220,38,38,0.18)',  fg: '#dc2626' },
    mongodb:    { bg: 'rgba(76,175,80,0.18)',  fg: '#5cb85c' },
    mongo:      { bg: 'rgba(76,175,80,0.18)',  fg: '#5cb85c' },
    cassandra:  { bg: 'rgba(34,87,180,0.18)',  fg: '#5b8fff' },
    elasticsearch: { bg: 'rgba(0,127,127,0.18)', fg: '#1a8c8c' },
    clickhouse: { bg: 'rgba(252,212,52,0.18)', fg: '#e0b400' },
    kafka:      { bg: 'rgba(30,30,30,0.25)',   fg: 'var(--text)' },
    rabbitmq:   { bg: 'rgba(255,102,0,0.18)',  fg: '#ff6600' },
    ibmmq:      { bg: 'rgba(15,98,254,0.18)',  fg: '#0f62fe' },
    nats:       { bg: 'rgba(39,174,96,0.18)',  fg: '#27ae60' },
    sqs:        { bg: 'rgba(255,153,0,0.18)',  fg: '#ff9900' },
    kinesis:    { bg: 'rgba(255,153,0,0.18)',  fg: '#ff9900' },
  };
  const t = tone[s] ?? { bg: 'var(--bg3)', fg: 'var(--text2)' };
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 6,
      padding: '2px 8px', borderRadius: 4, fontSize: 11, fontWeight: 600,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      background: t.bg, color: t.fg,
      border: `1px solid ${t.fg}33`,
    }}>
      <span aria-hidden style={{ fontSize: 10 }}>{kind === 'db' ? '⛁' : '⌬'}</span>
      {system}
    </span>
  );
}
