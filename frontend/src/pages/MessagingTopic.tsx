// MessagingTopic.tsx — /messaging/topic, tek bir topic'in tam sayfa detayı
// (v0.10.575, operatör mockup onayı 2026-09-09).
//
// NEDEN SAYFA. /messaging satırının çekmecesi bugün BEŞ tabloyu ve iki grafiği
// tablonun altındaki 12px dolgulu tek bir hücrede taşıyor: yayıncılar,
// tüketiciler, diğer istemciler, operasyon kırılımı, Kafka istemci blokları ve
// span adları. Aynı geçişi /databases v0.9.840'ta, /endpoints bir sürüm
// öncesinde yaptı. FARK: burada ÇEKMECE KALIYOR (operatör kararı) — satır
// tıklaması bugünkü gibi çalışıyor, sayfa ikinci bir kapı. İkisi aynı anda
// açılabildiği için ortak gövde PAYLAŞILIYOR, kopyalanmıyor:
// `features/dependencies/CallerSection.tsx` v0.10.575'te tam bu yüzden dışarı
// çıkarıldı (pages/databases/detailSections.tsx başlığındaki "iki kabuk, tek
// bölüm" sözleşmesi).
//
// YENİ UÇ YOK. İki okuma da mevcut: /api/messaging/detail (skor şeridi, e2e,
// çağıranlar, operasyonlar, span adları — TEK çağrı, açılışta) ve
// /api/messaging/clients (VM seam). MALİYET DİSİPLİNİ ikincisinde: açılışta
// yalnız `set=chart` (İKİ soru — gönderim ve tüketim hızı), `set=clients`
// (bağlantı/gecikme/rebalance) YALNIZ o sekme seçilince. Kalan üç sekme ek
// istek YAPMAZ; verileri açılıştaki detay yükünden geliyor.
//
// KİMLİK ÜÇLÜ ve URL'de AÇIK: `?system=&cluster=&destination=`. Cluster
// eksikse sayfa "linkte cluster yok" der — sessizce "(default)" VARSAYMAZ
// (v0.9.973: varsayılan cluster, çok-cluster kurulumda canlı bir topic için
// sıfırlanmış sayı üretiyordu ve sıfır "topic boşta" ile ayırt edilemiyordu).
import { lazy, Suspense, useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { PageShell } from '@/components/ui/PageShell';
import { StatTile, TabStrip } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { LazyMount } from '@/components/LazyMount';
import { useDataTable, DataTableHead, DataTableColgroup, ResetLayoutButton } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { CallerSection } from '@/features/dependencies/CallerSection';
import { KafkaClientsSection } from '@/features/dependencies/KafkaClientsSection';
import { kindSeries } from '@/features/dependencies/msgSeries';
import { kafkaChartItems, kafkaDegradeTR } from '@/features/dependencies/kafkaClients';
import { opLabelTR, isOpMissing, msgOperationRows, OP_MISSING_TITLE } from '@/features/dependencies/msgOperations';
import { useMessagingClients, useMessagingTopicDetail } from '@/lib/queries/messaging';
import { messagingTracesHref } from '@/lib/pivotHref';
import { traceHref } from '@/lib/traceHref';
import { navHref } from '@/lib/navHref';
import { msSyncKey } from '@/lib/chart/syncNamespace';
import { fmtNum, fmtNs, timeRangeToNs } from '@/lib/utils';
import { useUrlRange } from '@/lib/useUrlRange';
import { parseTopicRef, parseTopicTab, type MsgTopicTab } from './messaging/topicHref';
import type { DBOpStat, MsgOperationStat, TimeRange } from '@/lib/types';

// Çekmecedeki gerekçenin aynısı (v0.9.814): @grafana paketleri statik bağlanırsa bu
// rotanın chunk'ı ~1 MB büyür ve grafik GÖRÜNMESE de ödenir.
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// msOrDash — v0.9.263 sözleşmesi: sunucunun GÖNDERMEDİĞİ süre '—', asla
// "0.0 ms". Alanlar opsiyonel çünkü rolling deploy sırasında ısınmış önbellek
// onları taşımayabiliyor ve 0.0 ms "anlık" diye okunur.
function msOrDash(v?: number): string {
  return v === undefined ? '—' : `${v.toFixed(1)} ms`;
}

export default function MessagingTopicPage() {
  const [params, setParams] = useSearchParams();
  const [range, setRange] = useUrlRange('1h');
  // `search` bir STRING: memo kimliği ancak URL gerçekten değişince değişir
  // (useSearchParams her render'da yeni bir nesne verir).
  const search = params.toString();
  const ref = useMemo(() => parseTopicRef(search), [search]);
  const tab = parseTopicTab(params.get('tab'));

  // URL = tek doğruluk kaynağı. Yazım `prev`den TÜRETİLİYOR (yabancı param —
  // env, gelecekteki filtreler — korunur) ve `replace: true` (sekme gezinmesi
  // geçmişe yığılmaz; geri tuşu ÖNCEKİ SAYFAYA döner). Varsayılan sekme
  // param'ı SİLER: "tab yok" ile "tab=producers" tek adres olur.
  const setTab = (next: MsgTopicTab) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (next === 'producers') p.delete('tab'); else p.set('tab', next);
    return p;
  }, { replace: true });

  // timeRangeToNs YALNIZ memo içinde — çıplak JSX'te sonsuz refetch (v0.5.184).
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);
  const system = ref?.system ?? '';
  const cluster = ref?.cluster ?? '';
  const destination = ref?.destination ?? '';
  // Sync grubu topic BAŞINA + '-ms' motor ad alanı (v0.9.789): iki farklı
  // topic'in panelleri aynı gruba düşerse imleç komşunun grafiğinde gezer.
  const syncKey = msSyncKey(ref ? `msg-topic:${system}|${cluster}|${destination}` : '');

  const detailQ = useMessagingTopicDetail({
    system, cluster, destination, fromNs: from, toNs: to, enabled: !!ref,
  });
  // AÇILIŞTA YALNIZ `set=chart` — iki soru. Beş soruluk çekmece kümesi bu
  // sayfada hiç istenmiyor; istemci aileleri kendi sekmesini bekliyor.
  const chartQ = useMessagingClients({
    system, cluster, destination, fromNs: from, toNs: to, set: 'chart', enabled: !!ref,
  });

  if (!ref) {
    return (
      <>
        <Topbar title="Topic" range={range} onRangeChange={setRange} />
        <PageShell>
          <Empty icon="⚠" title="Bu linkte topic yok">
            Adres <code>system</code>, <code>cluster</code> ya da{' '}
            <code>destination</code> taşımıyor. Cluster BİLEREK zorunlu:
            varsayılan bir cluster, çok-cluster bir kurulumda canlı bir topic
            için sıfır gösterir.
            {' '}<Link to={navHref('/messaging', search)}>Messaging listesine dön →</Link>
          </Empty>
        </PageShell>
      </>
    );
  }

  const d = detailQ.isPending ? undefined : detailQ.isError ? null : (detailQ.data ?? null);
  const callers = d?.callers ?? [];
  const producers = callers.filter(c => c.role === 'producer');
  const consumers = callers.filter(c => c.role === 'consumer');
  const others = callers.filter(c => c.role && c.role !== 'producer' && c.role !== 'consumer');
  const msgOps = msgOperationRows(d?.operations);
  const topOps = d?.topOps ?? [];
  const e2e = d?.e2e;

  return (
    <>
      {/* Env "uygulanıyor" bayrağı BİLEREK verilmiyor: messaging okumaları
          `?env=` uygulamıyor ve uygulanmayan bir kapsamı aktif göstermek,
          operatöre olmayan bir daraltma vaat eder (§4.3(b) dürüstlük
          sözleşmesi; kapısı lib/ altındaki env bayrağı testi). Bayrağın ADI hiç
          geçmiyor: o kapı kaynağı YORUMLARIYLA tarıyor, yani bir açıklama
          cümlesi bile iddia sayılır ("gate kendi metnini ısırır"). */}
      <Topbar title="Topic" range={range} onRangeChange={setRange} />
      <PageShell>
        <div className="mtp-crumb">
          {/* Geri linki PENCEREYİ TAŞIR (navHref): bağlam değiştiren bir geri
              linki, geri linki olmaktan çıkar (Pod.tsx:206-209, v0.9.965). */}
          <Link to={navHref('/messaging', search)}>Messaging</Link> › topic detayı
        </div>

        {/* KİMLİK: sistem ve cluster soluk, destination vurgulu — operatörün
            aradığı sözcük, adresin taşıdığı üç alandan biri. */}
        <div className="mtp-id">
          <span className="mtp-id-dim mono" title="messaging.system">{system}</span>
          <span aria-hidden className="mtp-id-sep">›</span>
          <span className="mtp-id-dim mono" title="messaging.kafka.cluster.name">{cluster}</span>
          <span aria-hidden className="mtp-id-sep">›</span>
          <span className="mtp-id-name mono">{destination}</span>
          <Link className="ud-pill mtp-id-act"
            to={messagingTracesHref({ window: range, system, destination })}
            title="Bu topic'in trace'leri — aynı pencere, süzgeçler düzenlenebilir çip olarak">
            Traces →
          </Link>
        </div>

        {detailQ.isPending && <Spinner label="Topic detayı yükleniyor…" />}
        {d === null && (
          <Empty icon="⚠" title="Detay sorgusu başarısız">
            /api/messaging/detail isteği hata döndü. Pencereyi değiştirerek
            yeniden dene ya da ClickHouse bağlantısını kontrol et.
          </Empty>
        )}

        {d && (
          <>
            {/* SKOR ŞERİDİ — satırdaki sayılarla aynı, ama sayfa kendi başına
                okunsun diye tekrarlanıyor (postmortem'e ekran görüntüsü
                girdiğinde bağlam sayfada olmalı). */}
            <div className="mtp-kpis">
              <StatTile label="Span">{fmtNum(d.spanCount)}</StatTile>
              <StatTile label="Hata %"
                tone={d.errorRate > 5 ? 'err' : d.errorRate > 0 ? 'warn' : undefined}>
                {`${d.errorRate.toFixed(2)} %`}
              </StatTile>
              <StatTile label="P50">{msOrDash(d.p50DurationMs)}</StatTile>
              <StatTile label="P95">{msOrDash(d.p95DurationMs)}</StatTile>
              <StatTile label="P99">{`${d.p99DurationMs.toFixed(1)} ms`}</StatTile>
              {/* Uçtan uca p95 span gecikmesiyle AYNI BÜYÜKLÜK DEĞİL:
                  produce→consume, span_links korelasyonu. Link yoksa '—' —
                  sıfır yazmak "anında teslim" diye okunurdu. */}
              <StatTile label="Uçtan uca P95">
                {e2e && !e2e.linkless ? fmtNs(e2e.p95Ms * 1e6) : '—'}
              </StatTile>
            </div>
            <div className="mtp-cap">
              üretici / tüketici <b>{producers.length}</b> / <b>{consumers.length}</b>
              {others.length > 0 && <> · diğer istemci <b>{others.length}</b></>}
              {' '}· seçili pencere, <code className="mono">messaging_caller_summary_5m</code>
            </div>

            <div className="grid-2 mtp-charts">
              {/* SOL — üretim/tüketim hızı, METRİK tarafı (`set=chart`). */}
              <div className="mtp-card">
                <ChartPanel q={chartQ} xRange={xRange} syncKey={syncKey} />
              </div>
              {/* SAĞ — uçtan uca gecikme, SPAN tarafı. Çekmecedeki panelin
                  ikizi; storageKey AYRI (msg-topic-e2e) ki iki yüzeyin
                  sürüklenen genişlik/lejant durumu birbirine taşmasın. */}
              <div className="mtp-card">
                <E2EPanel e2e={e2e} range={range} xRange={xRange} syncKey={syncKey} />
              </div>
            </div>

            <TabStrip ariaLabel="Topic sekmeleri" value={tab} onChange={setTab} tabs={[
              { key: 'producers', label: <>Üreticiler<span className="tab-count">{producers.length}</span></> },
              { key: 'consumers', label: <>Tüketiciler<span className="tab-count">{consumers.length}</span></> },
              { key: 'operations', label: <>Operasyonlar<span className="tab-count">{msgOps.length}</span></> },
              { key: 'clients', label: 'Kafka istemcileri',
                title: 'Metrik tarafı — bağlantı, gecikme, rebalance. Yalnız bu sekme seçilince istenir.' },
              { key: 'spannames', label: <>Span adları<span className="tab-count">{topOps.length}</span></> },
            ]} />

            {tab === 'producers' && (
              <CallerSection
                title={`Üreticiler · ${producers.length} satır`}
                rows={producers}
                emptyMessage="Bu pencerede bu destination'a üretici span'i yok."
                tone="producer" range={range}
                storageKey="msg-topic-producers" showReset />
            )}
            {tab === 'consumers' && (
              <CallerSection
                title={`Tüketiciler · ${consumers.length} satır`}
                rows={consumers}
                emptyMessage="Bu pencerede bu destination'a tüketici span'i yok."
                tone="consumer" range={range}
                storageKey="msg-topic-consumers" showReset />
            )}
            {tab === 'operations' && <OperationsTable rows={msgOps} />}
            {/* Sekme seçili DEĞİLKEN bileşen hiç mount edilmiyor: VM sorgusu
                ne kurulur ne de "arka planda hazır" tutulur. `enabled` de
                geçiliyor — kapı iki katlı, çünkü ileride bir sekme önizlemesi
                bileşeni erken mount ederse maliyet sessizce geri gelirdi. */}
            {tab === 'clients' && (
              <KafkaClientsSection
                system={system} cluster={cluster} destination={destination}
                range={range} xRange={xRange} syncKey={syncKey}
                set="clients" enabled title="Kafka istemcileri" />
            )}
            {tab === 'spannames' && (
              <SpanNamesTable rows={topOps} range={range} system={system} destination={destination} />
            )}
          </>
        )}
      </PageShell>
    </>
  );
}

// ── üst grafik: üretim / tüketim (METRİK) ───────────────────────────────────

function ChartPanel({ q, xRange, syncKey }: {
  q: ReturnType<typeof useMessagingClients>;
  xRange: { from: number; to: number };
  syncKey?: string;
}) {
  if (q.isPending) {
    return <div className="kc-line" role="status" aria-busy="true"><Spinner /> Üretim / tüketim…</div>;
  }
  const data = q.isError ? null : (q.data ?? null);
  const degrade = kafkaDegradeTR(data);
  // Seri yoksa <Empty> DEĞİL: bu bir sayfa değil, iki karodan biri. Boş-durum
  // kutusu paneli sayfa boyunda bir "hata" gibi gösterirdi; çekmecedeki dil
  // (tek satır soluk not) burada da doğru ölçek.
  if (degrade || !data) {
    return <div className="kc-line" title={data?.note}>◌ {degrade}</div>;
  }
  const { items, truncated } = kafkaChartItems(data.blocks);
  const note = truncated ? `${data.note} · +${truncated} seri gösterilmiyor` : data.note;
  return (
    <LazyMount minHeight={190}>
      <Suspense fallback={<div className="kc-fallback"><Spinner /></div>}>
        <CorePanelMultiLazy
          title={`Üretim / tüketim · METRİK (${data.source})`}
          storageKey="msg-topic-rate"
          height={170} xRange={xRange} syncKey={syncKey}
          note={note}
          emptyReason={items.length === 0 ? 'Bu pencerede kayıt/sn serisi yok' : undefined}
          items={items} />
      </Suspense>
    </LazyMount>
  );
}

// ── uçtan uca gecikme (SPAN, span_links korelasyonu) ────────────────────────

function E2EPanel({ e2e, range, xRange, syncKey }: {
  e2e: import('@/lib/types').MsgE2E | undefined;
  range: TimeRange;
  xRange: { from: number; to: number };
  syncKey?: string;
}) {
  const series = e2e?.series ?? [];
  // linkless = HİÇ çift korele olmadı. Bu 0 ms DEĞİL: SDK'lar messaging span
  // link'i yaymıyor demek, ve ikisini aynı ekrana yazmak yanlış teşhis üretir.
  if (!e2e || e2e.linkless) {
    return (
      <div className="kc-line">
        ◌ Bu pencerede üretici→tüketici span link&#39;i yok — SDK&#39;lar messaging
        span link&#39;i yaymıyor, uçtan uca gecikme korele edilemiyor.
      </div>
    );
  }
  return (
    <>
      {series.length > 1 ? (
        <LazyMount minHeight={190}>
          <Suspense fallback={<div className="kc-fallback"><Spinner /></div>}>
            <CorePanelMultiLazy
              title="Uçtan uca gecikme · kova ortalaması"
              storageKey="msg-topic-e2e"
              height={170} unit="ms" xRange={xRange} syncKey={syncKey}
              note="produce→consume, span_links korelasyonu; kova başına ORTALAMA (p50/p95 aşağıda, pencere geneli)"
              items={[{ name: 'E2E lag', role: 'data', series: kindSeries(series, p => p.avgMs, 'E2E lag') }]} />
          </Suspense>
        </LazyMount>
      ) : (
        <div className="kc-line">◌ Seri tek kovada — eğri çizilemiyor, aşağıdaki pencere değerleri geçerli.</div>
      )}
      <div className="mtp-e2e">
        <span className="badge b-gray mono">p50 {fmtNs(e2e.p50Ms * 1e6)}</span>
        <span className="badge b-gray mono">p95 {fmtNs(e2e.p95Ms * 1e6)}</span>
        <span className="badge b-gray mono">p99 {fmtNs(e2e.p99Ms * 1e6)}</span>
        <span className="mtp-e2e-count">{fmtNum(e2e.count)} korele çift</span>
        {e2e.slowestConsumerTraceId && (
          <Link to={traceHref(e2e.slowestConsumerTraceId, { pageRange: range })}
            title={e2e.slowestProducerTraceId
              ? `En yavaş korele çift — tüketicinin trace'ini açar (üretici trace ${e2e.slowestProducerTraceId})`
              : "En yavaş korele çift — tüketicinin trace'ini açar"}
            className="mtp-e2e-link">
            en yavaş {fmtNs((e2e.slowestLagMs ?? 0) * 1e6)} → trace
          </Link>
        )}
      </div>
    </>
  );
}

// ── Operasyonlar (messaging_summary_5m'in operation boyutu) ─────────────────

function OperationsTable({ rows }: { rows: MsgOperationStat[] }) {
  const cols = useMemo<DataTableColumn<MsgOperationStat>[]>(() => [
    { id: 'operation', label: 'Operasyon', sortValue: o => o.operation, naturalDir: 'asc', flex: true, minWidth: 140 },
    { id: 'count',   label: 'Calls', sortValue: o => o.spanCount,     numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'errRate', label: 'Err %', sortValue: o => o.errorRate,     numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'avg',     label: 'Avg',   sortValue: o => o.avgDurationMs, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p50',     label: 'P50',   sortValue: o => o.p50DurationMs, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p95',     label: 'P95',   sortValue: o => o.p95DurationMs, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p99',     label: 'P99',   sortValue: o => o.p99DurationMs, numeric: true, naturalDir: 'desc', width: 84 },
  ], []);
  const dt = useDataTable<MsgOperationStat>({
    storageKey: 'msg-topic-ops', columns: cols, rows,
    initialSort: { id: 'count', dir: 'desc' },
  });
  return (
    <div className="mtp-sec">
      <div className="mtp-sec-head">
        <span aria-hidden className="mtp-dot mtp-dot--op" />
        Operasyonlar · MV · {rows.length} satır
        <span className="mtp-sec-act"><ResetLayoutButton dt={dt} /></span>
      </div>
      {/* TRACE PİVOTU YOK — bilerek (çekmecedeki gerekçenin aynısı):
          messagingTracesHref'in `operation` parametresi span ADINA çevriliyor,
          buradaki değer ise operasyon TÜRÜ (messaging.operation.type coalesce).
          İkisini eşitleyen link, var olamayacak satırlara giden ölü bir link
          olurdu — v0.9.256'nın kapattığı sınıf. */}
      {rows.length === 0 ? (
        // Bölüm GİZLENMİYOR: yokluğu söylemek, bakılmamış gibi görünmekten iyi.
        <div className="mtp-empty">Bu pencerede MV&#39;de operasyon satırı yok.</div>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((o, i) => {
                const errCls = o.errorRate > 5 ? 'err' : o.errorRate > 0 ? 'warn' : 'ok';
                const missing = isOpMissing(o.operation);
                return (
                  <tr key={`${o.operation}|${i}`}
                      style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                    <td className={missing ? 'mono mtp-op-missing' : 'mono mtp-op'}
                        title={missing ? OP_MISSING_TITLE : o.operation}>
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
  );
}

// ── Span adları (topOps) ───────────────────────────────────────────────────
//
// Çekmecedeki "Top operations" tablosunun İKİZİ DEĞİL, kardeşi: oradaki tablo
// İKİ KİPLİ (db → statement + LIKE-önekli pivot / queue → span adı) ve kendi
// iç kaydırma kabında yaşıyor. Burada kip TEK ve tablo sayfa boyunda; ortak
// bir gövdeye indirmek, `kind` propuyla iki kabuğu tek bileşende birleştirmek
// olurdu — pages/databases/detailSections.tsx başlığının DÖRT ısırıkla
// reddettiği şey.

function SpanNamesTable({ rows, range, system, destination }: {
  rows: DBOpStat[]; range: TimeRange; system: string; destination: string;
}) {
  const cols = useMemo<DataTableColumn<DBOpStat>[]>(() => [
    { id: 'statement', label: 'Span adı', sortValue: o => o.statement, naturalDir: 'asc', flex: true, minWidth: 200 },
    { id: 'op', label: 'Type', sortValue: o => o.operation ?? '', naturalDir: 'asc', width: 96 },
    { id: 'count', label: 'Count', sortValue: o => o.count, numeric: true, naturalDir: 'desc', width: 110 },
    { id: 'avg', label: 'Avg', sortValue: o => o.avgDurationMs, numeric: true, naturalDir: 'desc', width: 110 },
  ], []);
  const dt = useDataTable<DBOpStat>({
    storageKey: 'msg-topic-spannames', columns: cols, rows,
    initialSort: { id: 'count', dir: 'desc' },
  });
  return (
    <div className="mtp-sec">
      <div className="mtp-sec-head">
        <span aria-hidden className="mtp-dot mtp-dot--span" />
        Span adları · {rows.length} satır
        <span className="mtp-sec-act"><ResetLayoutButton dt={dt} /></span>
      </div>
      {rows.length === 0 ? (
        <div className="mtp-empty">Bu pencerede span adı satırı yok.</div>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((o, i) => (
                <tr key={`${o.statement}|${i}`}
                    style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                  <td className="mono mtp-span-name">
                    {o.statement
                      ? (
                        <>
                          {o.statement}
                          {/* Span ADINA göre pivot — destination + name TAM
                              EŞLEŞME (v0.9.256'nın onardığı link). */}
                          <Link to={messagingTracesHref({
                            window: range, system, destination, operation: o.statement,
                          })}
                            title="Bu span adının trace'lerini aç"
                            className="mtp-row-link">
                            → traces
                          </Link>
                        </>
                      )
                      : <span className="mtp-op-missing">(boş)</span>}
                  </td>
                  <td className="mono mtp-op">{o.operation ?? '—'}</td>
                  <td className="num mono">{fmtNum(o.count)}</td>
                  <td className="num mono">{o.avgDurationMs.toFixed(1)}ms</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
