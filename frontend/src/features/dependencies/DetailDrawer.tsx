import { lazy, Suspense, useMemo } from 'react';
import { msSyncKey } from '@/lib/chart/syncNamespace';
import { messagingTracesHref, statementTracesHref } from '@/lib/pivotHref';
import { Link } from 'react-router-dom';
import { Spinner } from '@/components/Spinner';
import { LazyMount } from '@/components/LazyMount';
import { useDepDetail } from '@/lib/queries/dependencies';
import { fmtNum, fmtNs, timeRangeToNs } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, DBDetail, MessagingDetail, DBOpStat, MsgOperationStat } from '@/lib/types';
import { Stat } from './panels/shared';
import { traceHref } from '@/lib/traceHref';
import { OraclePanel } from './panels/OraclePanel';
import { PostgresPanel } from './panels/PostgresPanel';
import { MySQLPanel } from './panels/MySQLPanel';
import { RedisPanel } from './panels/RedisPanel';
import { KafkaClientsSection } from './KafkaClientsSection'; // v0.10.551
// v0.10.575 — CallerSection ARTIK PAYLAŞILIYOR: aynı (servis, pod) kırılımını
// /messaging/topic sayfası da çiziyor. Bileşen buradan çıkarıldı, davranış
// bayt-bayt aynı (yeni propların ikisi de opsiyonel ve çekmece geçmiyor).
import { CallerSection } from './CallerSection';
import { kindSeries } from './msgSeries'; // v0.10.575 — saniye→ns tek yerde
import { messagingTopicHref } from '@/pages/messaging/topicHref'; // v0.10.575
import { opLabelTR, isOpMissing, msgOperationRows, OP_MISSING_TITLE } from './msgOperations'; // v0.10.563

// v0.9.814 — drawer'ın mini panelleri de CorePanel. LAZY: @grafana/*
// statik import edilseydi /messaging + /databases vendor chunk'ı ~1 MB
// büyürdü ve drawer AÇILMADAN da ödenirdi (Overview.tsx:26-29 ölçümü).
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// msOrDash — v0.9.263. A duration the backend didn't send is '—', never
// "0.0 ms". These fields are optional precisely because a warm cached payload
// from a pre-v0.9.263 backend lacks them during a rolling deploy, and 0.0 ms
// would read as "instantaneous" rather than "not reported".
function msOrDash(v?: number): string {
  return v === undefined ? '—' : `${v.toFixed(1)} ms`;
}

// DetailDrawer fetches and renders the per-(service, pod) caller
// breakdown + top operations for one (system, instance) tuple.
// Lazy — only fires when the row is expanded; bounded server-
// side at LIMIT 100 callers / LIMIT 20 ops so the response stays
// cheap even for a 50-pod fleet.
// Split out of the DependenciesTable monolith (v0.8.252 refactor)
// verbatim.
export function DetailDrawer({ system, cluster, name, instance, dbName, kind, source, range }: {
  system: string;
  // Cluster identifier — only meaningful for queue/messaging
  // rows. DB callers pass "(default)" and the backend ignores
  // it (DB queries don't have a cluster dimension).
  cluster: string;
  /** GÖRÜNEN etiket. DB tarafında SORGU KİMLİĞİ DEĞİL (aşağıya bak). */
  name: string;
  /**
   * v0.9.821 — DB satırının GERÇEK instance'ı. `name` bunun yerine
   * kullanılamaz: DependenciesTable'ın nameOf'u instance 'unknown'
   * olduğunda ETİKET olarak db.name'i basıyor, yani çekmece
   * instance=<db.name> soruyor ve MV'de instance='unknown' olduğu için
   * HİÇBİR ŞEY eşleşmiyordu — sessizce boş bir çekmece.
   */
  instance?: string;
  /**
   * v0.9.821 — satır kimliğinin ÜÇÜNCÜ alanı. Olmadan bir host'ta N
   * veritabanı olan kurulumlarda her satır AYNI çekmeceyi açıyor ve
   * host'un TOPLAMINI gösteriyordu.
   */
  dbName?: string;
  kind: 'db' | 'queue';
  // 'spans' = row came from app-emitted traces; 'receiver' = row
  // came from an OTel DB receiver. We only render the receiver-
  // specific metric panel (oracle / postgres / mysql / redis)
  // for receiver rows so the upper "Called from services" panel
  // doesn't bleed receiver-side metrics into a span-derived row.
  source: 'spans' | 'receiver';
  range: TimeRange;
}) {
  type D = DBDetail | MessagingDetail;
  // v0.10.576 — React Query. Eskisi çıplak useEffect + then/catch idi ve
  // iptal YOKTU: operatör çekmeceyi kapatınca ya da başka satıra geçince
  // sunucudaki okuma sonuna kadar koşuyordu. Artık signal iletiliyor.
  //
  // ÜÇ DURUM aynen korunuyor: undefined = yükleniyor, null = boş/başarısız,
  // veri = yük. Çekmece yalnız AÇIKKEN mount ediliyor, o yüzden ayrı bir
  // `enabled` kapısına gerek yok — fetch-on-open zaten sağlanmış durumda.
  // timeRangeToNs MEMO içinde: çıplak JSX'te sonsuz refetch (v0.5.184).
  const detailWindow = useMemo(() => timeRangeToNs(range), [range]);
  // v0.9.821 — kimlik ÜÇLÜ ve GÖRÜNEN ETİKETTEN bağımsız: (system, instance,
  // dbName). instance verilmemişse (messaging tarafı, eski çağıranlar)
  // etikete düşülür; bu dallanma artık hook'un içinde, tek yerde.
  const detailQ = useDepDetail({
    kind, system, cluster, name, instance, dbName,
    fromNs: detailWindow.from, toNs: detailWindow.to,
  });
  const data: D | null | undefined = detailQ.isPending ? undefined : (detailQ.data ?? null);

  // v0.9.814 — mini panellerin x ekseni sorgu penceresine sabitlenir
  // (v0.9.83 kuralı): veri seyrekse eksen kendi kendine daralıp iki
  // paneli farklı zaman aralığına yayardı ve imleç senkronu yalan olurdu.
  // timeRangeToNs MEMO içinde — çıplak JSX'te sonsuz refetch (v0.5.184).
  // ERKEN DÖNÜŞLERDEN ÖNCE: hook sırası sabit kalmalı.
  const drawerXRange = useMemo(() => {
    const { from, to } = timeRangeToNs(range);
    return { from: from / 1e9, to: to / 1e9 };
  }, [range]);
  // Sync grubu destination BAŞINA: iki farklı satırın panelleri aynı
  // gruba düşerse imleç komşu destination'ın grafiğinde gezinir.
  // '-ms' soneki motor ad alanı (v0.9.789).
  const drawerSync = msSyncKey(`msg-drawer:${system}|${cluster}|${name}`); // v0.10.289

  // ERKEN DÖNÜŞLERDEN ÖNCE (rules-of-hooks): topOps türetmesi ve iki
  // hook, v0.9.873'ten beri erken dönüşlerin ARKASINDAydı — data
  // yüklenirken render hook'ları atlıyor, sıra kayıyordu. `data`
  // burada undefined/null olabilir, o yüzden `?.` + `?? []`.
  const allTopOps = data?.topOps ?? [];
  // v0.9.873 (tutarlılık denetimi BT8). Kolon ETİKETİ `kind`e bağlı, bu
  // yüzden storageKey de türetiliyor (R2 / `deps-callers-${tone}` emsali):
  // aynı anahtar altında iki farklı kolon kümesi saklanırsa kaydedilen
  // genişlikler diğer kipe taşar.
  const topOpsCols = useMemo<DataTableColumn<DBOpStat>[]>(() => [
    { id: 'statement', label: kind === 'db' ? 'Statement' : 'Operation',
      sortValue: o => o.statement, naturalDir: 'asc', flex: true },
    // v0.10.553 — messaging: operasyon türü (messaging.operation.type → .name
    // → .operation, okuma-anında coalesce). DB kipinde kolon yok.
    ...(kind === 'queue'
      ? [{ id: 'op', label: 'Type', sortValue: (o: DBOpStat) => o.operation ?? '', naturalDir: 'asc', width: 96 } as DataTableColumn<DBOpStat>]
      : []),
    { id: 'count', label: 'Count', sortValue: o => o.count,          numeric: true, width: 110 },
    { id: 'avg',   label: 'Avg',   sortValue: o => o.avgDurationMs,  numeric: true, width: 110 },
  ], [kind]);
  const topOpsDt = useDataTable<DBOpStat>({
    storageKey: `deps-topops-${kind}`, columns: topOpsCols, rows: allTopOps,
    initialSort: { id: 'count', dir: 'desc' },
  });

  // v0.10.563 (Faz 4b) — messaging_summary_5m'in OPERATION kırılımı.
  // ERKEN DÖNÜŞLERDEN ÖNCE (rules-of-hooks): `data` burada undefined
  // (yükleniyor) ya da null (sorgu düştü) olabilir; o hâlde rows [] —
  // hook sırası sabit kalır. Bu dosya v0.9.873'te tam bu tuzağa düşmüştü.
  const msgOps = useMemo<MsgOperationStat[]>(
    () => msgOperationRows(kind === 'queue' && data && 'operations' in data
      ? (data as MessagingDetail).operations
      : undefined),
    [kind, data]);
  // Kolonlar SABİT (kind'e bağlı değil): tablo yalnız queue dalında
  // render ediliyor, o yüzden storageKey'i türetmeye gerek yok — tek
  // kolon kümesi, tek anahtar.
  const msgOpsCols = useMemo<DataTableColumn<MsgOperationStat>[]>(() => [
    { id: 'operation', label: 'Operasyon', sortValue: o => o.operation, naturalDir: 'asc', flex: true, minWidth: 140 },
    { id: 'count',   label: 'Calls', sortValue: o => o.spanCount,      numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'errRate', label: 'Err %', sortValue: o => o.errorRate,      numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'avg',     label: 'Avg',   sortValue: o => o.avgDurationMs,  numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p50',     label: 'P50',   sortValue: o => o.p50DurationMs,  numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p95',     label: 'P95',   sortValue: o => o.p95DurationMs,  numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p99',     label: 'P99',   sortValue: o => o.p99DurationMs,  numeric: true, naturalDir: 'desc', width: 84 },
  ], []);
  const msgOpsDt = useDataTable<MsgOperationStat>({
    storageKey: 'deps-msg-ops', columns: msgOpsCols, rows: msgOps,
    initialSort: { id: 'count', dir: 'desc' },
  });

  if (data === undefined) return <Spinner />;
  if (data === null) return (
    <div style={{ fontSize: 12, color: 'var(--err)' }}>
      Detail query failed.
    </div>
  );

  // Defensive null-coalesce — pre-v0.4.87 the backend returned
  // null for empty slices (Go nil → JSON null), which crashed
  // [...data.callers]. The store now emits [] but we keep the
  // guard in case the cache returns a stale payload across an
  // upgrade.
  const allCallers = data.callers ?? [];

  // Worst-impact callers first — operator's first triage
  // question is "which client is hitting this DB hardest?".
  // We sort by spanCount × avgMs (impact, Elastic-APM style)
  // since a 200ms call made 10k times beats a 5s call made
  // twice for cumulative load on the backend.
  const callers = [...allCallers].sort((a, b) =>
    (b.spanCount * b.avgDurationMs) - (a.spanCount * a.avgDurationMs));

  // For messaging detail we split Producers / Consumers visually
  // — the SRE's "who's publishing" and "who's consuming"
  // questions are different (publisher is usually the load
  // generator, consumer is where back-pressure shows up).
  const producers = kind === 'queue'
    ? callers.filter(c => c.role === 'producer')
    : [];
  const consumers = kind === 'queue'
    ? callers.filter(c => c.role === 'consumer')
    : [];
  const otherClients = kind === 'queue'
    ? callers.filter(c => c.role && c.role !== 'producer' && c.role !== 'consumer')
    : callers;

  // v0.10.101 — "Üretim vs Tüketim" grafiği KALDIRILDI (operatör:
  // "yazmasına gerek yok"). Payload'daki series alanı telde duruyor
  // (aynı MV okumasının yan ürünü, ayrı sorgu değil); yalnız görsel
  // tüketicisi kalktı. Geri getirme = bu bloğun git geçmişi.

  // v0.8.372 (Stage-2 M2) — span_links-correlated end-to-end
  // produce→consume latency. Tri-state: undefined = backend read
  // failed or stale pre-M2 cache (section simply absent, drawer
  // never blocks on it); linkless = zero pairs correlated (honest
  // hint instead of a fake 0ms); else chips + sparkline + the
  // slowest-pair trace pivot.
  const e2e = kind === 'queue' && 'e2e' in data
    ? (data as MessagingDetail).e2e
    : undefined;
  const e2eSeries = e2e?.series ?? [];
  const e2eLagSeries = kindSeries(e2eSeries, p => p.avgMs, 'E2E lag');

  // v0.9.973 — sunucu cluster'ı VARSAYDI (istek cluster taşımıyordu).
  // Cluster yüklemi TAM EŞİTLİK, yani çok-cluster kurulumda aşağıdaki
  // her sayı canlı bir topic için sıfır olabilir; sıfırlanmış çekmece
  // "bu topic boşta" ile ayırt edilemez. Bunu söylemek, sessizce sıfır
  // göstermekten iyidir.
  const assumedCluster = kind === 'queue' && 'assumedCluster' in data
    ? (data as MessagingDetail).assumedCluster === true
    : false;

  return (
    <div>
      {assumedCluster && (
        <div style={{
          marginBottom: 12, padding: '6px 10px', fontSize: 11,
          color: 'var(--text2)', background: 'var(--bg2)',
          border: '1px solid var(--border)', borderRadius: 4,
        }}>
          Cluster belirtilmedi — <span className="mono">(default)</span> varsayıldı.
          Çok-cluster bir kurulumda bu topic başka bir cluster'da yaşıyorsa
          aşağıdaki sayılar boş görünür.
        </div>
      )}
      {/* v0.9.257 (operator-reported, second round: "messaging
          sayfasından tracelere ulaşamıyorum bulamıyorum") — v0.9.256
          repaired the link but left it UNFINDABLE. A row click opens
          THIS drawer, and the drawer had no trace affordance at all:
          the only pivots were the destination label in the row (whose
          tooltip still advertised Explore) and two links buried below
          the fold. So the operator landed here and had nowhere to go.
          A dead link and an invisible link fail identically from the
          operator's chair. The action sits ABOVE the stat strip because
          the drawer is the landing surface, not a leaf. */}
      {kind === 'queue' && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 12 }}>
          {/* v0.10.575 — ÇEKMECEDEN SAYFAYA. Çekmece KALIYOR (satır tıklaması
              bugünkü gibi); bu link yalnız ikinci bir kapı açıyor: 12px'lik
              bir hücrede yaşayan beş tablo + iki grafik, tam sayfada sekmelere
              yayılıyor. /databases'in v0.9.840'ta yaptığı geçişin aynısı, ama
              çekmeceyi EMEKLİ ETMEDEN — operatör kararı. */}
          <Link className="ud-pill"
                to={messagingTopicHref({ system, cluster, destination: name, range })}
                title="Bu topic'in tam sayfa detayı — üreticiler, tüketiciler, operasyonlar, Kafka istemcileri">
            Detay sayfası →
          </Link>
          <Link className="ud-pill"
                to={messagingTracesHref({ window: range, system, destination: name })}
                title="Open the traces behind this topic — same window, filters shown as editable chips">
            View traces →
          </Link>
          {data.errorCount > 0 && (
            <Link className="ud-pill"
                  style={{ color: 'var(--err)' }}
                  to={messagingTracesHref({
                    window: range, system, destination: name, hasError: true,
                  })}
                  title="Only the failing spans on this topic">
              Failed traces ({fmtNum(data.errorCount)}) →
            </Link>
          )}
        </div>
      )}

      {/* v0.9.821 — KAPSAM SATIRI. Çekmecenin hangi kimliği anlattığı
          artık YAZILI. Eskiden çekmece (system, instance) soruyordu ama
          satır (system, instance, db_name) idi: bir host'ta N
          veritabanı olan kurulumlarda hangi satıra tıklanırsa tıklansın
          aynı toplam açılıyordu ve hiçbir şey bunu söylemiyordu. Boş
          dbName hâlâ mümkün (eski derin linkler) ve o zaman da AÇIKÇA
          "tüm veritabanları" deniyor — sessiz bir kapsam bir daha
          olmasın. */}
      {kind === 'db' && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          Kapsam:{' '}
          <span className="mono" style={{ color: 'var(--text2)' }}>
            {system} / {instance ?? name}
          </span>
          {dbName
            ? <> · veritabanı <span className="mono" style={{ color: 'var(--text2)' }}>{dbName}</span></>
            : <> · <b>tüm veritabanları</b> (bu instance üzerindeki her db.name)</>}
        </div>
      )}

      {/* Aggregate strip on top — same numbers as the row but
          repeated here so the drawer reads on its own when
          screenshotted into a postmortem. */}
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(120px, 1fr))',
        gap: 10, marginBottom: 14,
      }}>
        <Stat label="Calls"     value={fmtNum(data.spanCount)} />
        <Stat label="Errors"    value={fmtNum(data.errorCount)} />
        <Stat label="Err rate"  value={`${data.errorRate.toFixed(2)}%`}
              tone={data.errorRate > 5 ? 'err' : data.errorRate > 0 ? 'warn' : 'ok'} />
        <Stat label="Avg"       value={`${data.avgDurationMs.toFixed(1)} ms`} />
        {/* v0.9.263 — P50 / P95 come off the SAME db_caller_summary_5m /
            messaging_caller_summary_5m TDigest merge that already produced
            P99 (indices 1 and 2 of the 3-wide state), so the drawer answers
            "typical vs tail" without a second query. '—' rather than 0.0 ms
            when a warm cached payload predates the fields. */}
        <Stat label="P50"       value={msOrDash(data.p50DurationMs)} />
        <Stat label="P95"       value={msOrDash(data.p95DurationMs)} />
        <Stat label="P99"       value={`${data.p99DurationMs.toFixed(1)} ms`} />
      </div>

      {/* v0.8.364 — produce vs consume rate over the window (5-min
          buckets, rendered per-minute). Splits the aggregate call
          rate by span kind so a producer surge with a stalled
          consumer reads instantly. Colours mirror the RoleBadge
          tones below (producer = accent, consumer = ok).
          v0.8.372 — the third (pre-seated) slot carries the e2e
          lag sparkline: avg produce→consume latency per bucket. */}
      {/* v0.9.814 — üç sparkline CorePanel mini paneline döndü. Eski
          <Sparkline> 26px'lik, eksensiz, tooltip'siz bir şeritti: eğrinin
          ŞEKLİ görünüyor ama "ne zaman" ve "ne kadar" görünmüyordu, yani
          drawer'ı açan operatör olayın saatini yine tablodan tahmin
          etmek zorundaydı. Üçü AYNI sync grubunda (destination başına
          ayrı grup) — imleç birlikte gezer, yani üretim düşüşü ile
          gecikme sıçraması aynı x'te okunur. */}
      {e2eSeries.length > 1 && (
        <div className="ov-mb">
          {(
            <LazyMount minHeight={170}>
              <Suspense fallback={<div style={{ height: 170, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
                <CorePanelMultiLazy
                  // Bu panel UÇTAN UCA gecikme (produce→consume, span_links
                  // korelasyonu) — sayfa üstündeki "span gecikmesi"
                  // panelinden FARKLI bir büyüklük. Başlık ikisini
                  // karıştırmasın diye kaynağını söylüyor.
                  title="Uçtan uca gecikme · kova ortalaması"
                  storageKey="msg-drawer-e2e"
                  height={150} unit="ms" xRange={drawerXRange} syncKey={drawerSync}
                  note="produce→consume, span_links korelasyonu; kova başına ORTALAMA (p50/p95 aşağıdaki blokta, pencere geneli)"
                  items={[
                    { name: 'E2E lag', role: 'data', series: e2eLagSeries },
                  ]} />
              </Suspense>
            </LazyMount>
          )}
        </div>
      )}

      {/* v0.8.372 (Stage-2 M2) — end-to-end produce→consume latency
          off span_links (consumer spans link back to the producer
          span of the message they processed). The slowest correlated
          pair doubles as the exemplar pivot into the consumer's
          trace. Linkless renders the honest "SDKs aren't emitting
          links" hint instead of a meaningless 0ms. */}
      {e2e && (
        <div style={{ marginBottom: 14 }}>
          <div style={{ fontSize: 10, fontWeight: 700, color: 'var(--text3)',
                        textTransform: 'uppercase', letterSpacing: '.5px', marginBottom: 4 }}>
            End-to-end latency · produce → consume
          </div>
          {e2e.linkless ? (
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>
              No producer→consumer span links in this window — the SDKs
              aren&apos;t emitting messaging span links, so end-to-end latency
              can&apos;t be correlated.
            </div>
          ) : (
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <span className="badge b-gray" style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
                p50 {fmtNs(e2e.p50Ms * 1e6)}
              </span>
              <span className="badge b-gray" style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
                p95 {fmtNs(e2e.p95Ms * 1e6)}
              </span>
              <span className="badge b-gray" style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
                p99 {fmtNs(e2e.p99Ms * 1e6)}
              </span>
              <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                {fmtNum(e2e.count)} correlated {e2e.count === 1 ? 'pair' : 'pairs'}
              </span>
              {e2e.slowestConsumerTraceId && (
                <Link
                  to={traceHref(e2e.slowestConsumerTraceId, { pageRange: range })}
                  title={e2e.slowestProducerTraceId
                    ? `Slowest correlated pair — opens the consumer's trace (producer trace ${e2e.slowestProducerTraceId})`
                    : "Slowest correlated pair — opens the consumer's trace"}
                  style={{ fontSize: 11, color: 'var(--accent2)', fontWeight: 500,
                           whiteSpace: 'nowrap' }}>
                  slowest {fmtNs((e2e.slowestLagMs ?? 0) * 1e6)} → trace
                </Link>
              )}
            </div>
          )}
        </div>
      )}

      {/* Oracle-specific drill-down — only renders when the system
          is oracle. Reads the oracledb-receiver-flavoured metrics
          panel (sessions / processes / counters / tablespaces).
          The backend serves synthetic numbers with synthetic=true
          when no receiver data exists in the window so the panel
          still renders during integration setup. */}
      {/* Receiver-specific panels gated on source — app-derived
          rows (the "Called from services" panel) intentionally
          hide these so receiver-side metrics don't bleed in. */}
      {source === 'receiver' && kind === 'db' && system.toLowerCase() === 'oracle' && (
        <OraclePanel instance={name} range={range} />
      )}
      {source === 'receiver' && kind === 'db' && (system.toLowerCase() === 'postgresql' || system.toLowerCase() === 'postgres') && (
        <PostgresPanel instance={name} range={range} />
      )}
      {source === 'receiver' && kind === 'db' && (system.toLowerCase() === 'mysql' || system.toLowerCase() === 'mariadb') && (
        <MySQLPanel instance={name} range={range} />
      )}
      {source === 'receiver' && kind === 'db' && system.toLowerCase() === 'redis' && (
        <RedisPanel instance={name} range={range} />
      )}

      {/* Per-(service, pod) breakdown — the SRE's "which client
          is shouting at this DB / queue" answer. Sorted by impact
          (spanCount × avgMs) so the heaviest cumulative consumer
          surfaces first. For messaging we split Producers /
          Consumers since they answer different questions. */}
      {kind === 'queue' ? (
        <>
          <CallerSection
            title={`Publishers · ${producers.length} ${producers.length === 1 ? 'row' : 'rows'}`}
            rows={producers}
            emptyMessage="No producer spans for this destination in the window."
            tone="producer" range={range} />
          <CallerSection
            title={`Consumers · ${consumers.length} ${consumers.length === 1 ? 'row' : 'rows'}`}
            rows={consumers}
            emptyMessage="No consumer spans for this destination in the window."
            tone="consumer" range={range} />
          {otherClients.length > 0 && (
            <CallerSection
              title={`Other clients · ${otherClients.length}`}
              rows={otherClients}
              emptyMessage=""
              tone="other" range={range} />
          )}
          {/* v0.10.563 (Faz 4b) — OPERASYON kırılımı, messaging_summary_5m'in
              kendi operation boyutundan. Yukarıdaki Publishers/Consumers
              tabloları messaging_caller_summary_5m'den geliyor (çağıran
              boyutu); bu tablo AYNI pencereyi BAŞKA bir eksende kesiyor, o
              yüzden toplamlar birebir tutmayabilir ve bu bir hata DEĞİL.

              TRACE PİVOTU YOK — bilerek. messagingTracesHref'in `operation`
              parametresi span ADINA (`name` = …) çeviriyor; buradaki değer
              ise operasyon TÜRÜ (messaging.operation.type coalesce'u).
              İkisini eşitleyen bir link, var olamayacak satırlara işaret
              eden ölü bir link olurdu — v0.9.256'nın tam olarak kapattığı
              sınıf. Tür bazlı bir pivot ancak /traces o niteliği indeksli
              anahtar olarak kabul edince gelir. */}
          <div style={{ marginBottom: 14 }}>
            <div style={{
              display: 'flex', alignItems: 'center', gap: 8,
              fontSize: 12, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
            }}>
              <span aria-hidden style={{
                width: 8, height: 8, borderRadius: 2, background: 'var(--purple)',
              }} />
              Operasyonlar · MV · {msgOps.length} satır
              <span style={{ marginLeft: 'auto' }}>
                <ResetLayoutButton dt={msgOpsDt} />
              </span>
            </div>
            {msgOps.length === 0 ? (
              // Bölüm GİZLENMİYOR: yokluğu söylemek, bakılmamış gibi
              // görünmekten iyidir (boş küme kaybolur, sıfır olmaz).
              <div style={{ fontSize: 12, color: 'var(--text3)' }}>
                Bu pencerede MV&#39;de operasyon satırı yok.
              </div>
            ) : (
              <div className="table-wrap">
                <table style={{ tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={msgOpsDt} />
                  <DataTableHead dt={msgOpsDt} />
                  <tbody>
                    {msgOpsDt.sortedRows.map((o, i) => {
                      const errCls = o.errorRate > 5 ? 'err' : o.errorRate > 0 ? 'warn' : 'ok';
                      const missing = isOpMissing(o.operation);
                      return (
                        <tr key={`${o.operation}|${i}`}>
                          <td className="mono" style={{
                            fontSize: 11,
                            color: missing ? 'var(--text3)' : 'var(--text2)',
                          }} title={missing ? OP_MISSING_TITLE : o.operation}>
                            {opLabelTR(o.operation)}
                          </td>
                          <td className="num mono">{fmtNum(o.spanCount)}</td>
                          <td className="num mono">
                            <span className={`badge b-${errCls}`} style={{ fontSize: 9 }}
                                  title={`${fmtNum(o.errorCount)} hatalı span`}>
                              {o.errorRate.toFixed(2)}%
                            </span>
                          </td>
                          <td className="num mono">{o.avgDurationMs.toFixed(1)}ms</td>
                          <td className="num mono">{o.p50DurationMs.toFixed(1)}ms</td>
                          <td className="num mono">{o.p95DurationMs.toFixed(1)}ms</td>
                          <td className="num mono">{o.p99DurationMs.toFixed(1)}ms</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
          {/* v0.10.551 — Kafka istemci metrikleri (VM seam): span tarafından
              SONRA, Top operations'tan ÖNCE (operatör mockup onayı). */}
          <KafkaClientsSection system={system} cluster={cluster} destination={name}
            range={range} xRange={drawerXRange} syncKey={drawerSync} />
        </>
      ) : (
        <CallerSection
          title={`By client (service + pod) · ${callers.length} ${callers.length === 1 ? 'row' : 'rows'}`}
          rows={callers}
          emptyMessage="No callers in this window."
          tone="db" range={range} />
      )}

      {/* Top operations — for DBs the first 80 chars of
          db_statement (collapses unparameterised SQL); for
          messaging the span name (publish / consume / process). */}
      {allTopOps.length > 0 && (
        <div>
          <div style={{ fontSize: 12, fontWeight: 700, marginBottom: 6,
                         color: 'var(--text2)' }}>
            {kind === 'db'
              ? `Top ${allTopOps.length} statements (first 80 chars)`
              : `Top ${allTopOps.length} operations`}
          </div>
          {/* v0.9.821 — KİMLİK FARKI TEK SATIRDA. Bu tablo ham
              db_statement'ın İLK 80 KARAKTERİNE göre grupluyor; sayfadaki
              "En pahalı ifadeler" tablosu ve /databases/slow-queries ise
              NORMALİZE edilmiş ifadenin hash'ine (stmt_hash) göre. İki
              gruplama aynı SQL'i farklı sayıda satıra bölebilir —
              parametreleri satır içinde taşıyan iki sorgu ilk 80 karakterde
              aynıysa burada TEK satır, hash'te İKİ satır olur (ya da tam
              tersi: 80. karakterden sonra ayrışan iki sorgu burada iki
              satır, normalizasyondan sonra tek). Sayılar bu yüzden birebir
              tutmayabilir ve bu bir hata DEĞİL. */}
          {kind === 'db' && (
            <div style={{ fontSize: 10.5, color: 'var(--text3)', marginBottom: 6, lineHeight: 1.45 }}>
              Gruplama <b>ham ifadenin ilk 80 karakteri</b>. Sayfadaki
              &quot;En pahalı ifadeler&quot; ve Slow queries katalogu
              <b> normalize edilmiş ifadenin hash&#39;ine</b> göre gruplar —
              aynı SQL iki tabloda farklı sayıda satıra düşebilir; sayılar
              birebir tutmayabilir.
            </div>
          )}
          {/* v0.9.873 (tutarlılık denetimi BT8) — yapışkanlık `is-scroll`
              sınıfına devredildi. Satır içi `<thead style={{position:
              'sticky'}}>` primitife geçişte KAYBOLURDU (DataTableHead
              v0.9.697'den beri position'ı satır içi yazmıyor) — bu dosyanın
              kendi MT5 itirafının tekrarı olurdu. `is-fit` DEĞİL: kaydırma
              bu kabın İÇİNDE, referans sayfa barı değil. */}
          <div className="table-wrap is-scroll" style={{ maxHeight: 240, overflowY: 'auto' }}>
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={topOpsDt} />
              <DataTableHead dt={topOpsDt} />
              <tbody>
                {topOpsDt.sortedRows.map((o, i) => (
                  <tr key={i}>
                    <td style={{
                      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                      fontSize: 11, wordBreak: 'break-word', maxWidth: 600,
                    }}>
                      {o.statement
                        ? (
                          <>
                            {o.statement}
                            {/* Trace exemplars — DB rows only. No
                                per-statement service in this drawer
                                (it's the DB-side aggregate), so we
                                scope on db.statement LIKE + rootOnly=
                                false and leave service unset. */}
                            {/* v0.9.256 — bu bağlantı `kind === 'db'` ile
                                kapalıydı, yani mesajlaşmadaki EN doğal pivot
                                (bu operasyonun trace'leri) tek satır kod
                                yüzünden yoktu. DB tarafı statement'a göre
                                LIKE-öneki ile; kuyruk tarafı destination +
                                operasyon adına göre tam eşleşme — ikisi farklı
                                sorgu, o yüzden ayrı helper'lar. */}
                            <Link to={kind === 'db'
                              ? statementTracesHref({ window: range, statement: o.statement })
                              : messagingTracesHref({
                                window: range, system, destination: name,
                                operation: o.statement,
                              })}
                              title={kind === 'db'
                                ? 'Find traces running this statement (LIKE-prefix, best-effort)'
                                : 'Bu operasyonun trace\'lerini aç'}
                              style={{
                                marginLeft: 8, fontSize: 10, whiteSpace: 'nowrap',
                                color: 'var(--accent2)', fontWeight: 500,
                              }}>
                              → traces
                            </Link>
                          </>
                        )
                        : <span style={{ color: 'var(--text3)' }}>(empty)</span>}
                    </td>
                    {kind === 'queue' && (
                      <td className="mono" style={{ fontSize: 11, color: 'var(--text2)' }}>{o.operation ?? '—'}</td>
                    )}
                    <td className="num mono">{fmtNum(o.count)}</td>
                    <td className="num mono">{o.avgDurationMs.toFixed(1)}ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
