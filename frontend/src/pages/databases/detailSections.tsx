import { lazy, Suspense, useMemo } from 'react';
import { msSyncKey } from '@/lib/chart/syncNamespace';
import { Link } from 'react-router-dom';
import { Turtle } from 'lucide-react';
import { Card, PanelTitle, StatTile } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { QueryError } from '@/components/QueryError';
import { TableSkeleton } from '@/components/Skeleton';
import { LazyMount } from '@/components/LazyMount';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import { dbTracesHref } from '@/lib/pivotHref';
import { logsHref } from '@/lib/logsUrl';
import { serviceHref } from '@/lib/serviceHref';
import { fmtNum } from '@/lib/utils';
import { OraclePanel } from '@/features/dependencies/panels/OraclePanel';
import { PostgresPanel } from '@/features/dependencies/panels/PostgresPanel';
import { MySQLPanel } from '@/features/dependencies/panels/MySQLPanel';
import { RedisPanel } from '@/features/dependencies/panels/RedisPanel';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  DBCallerBreakdown, DBDetail, DBTrend, SlowQueryRow, SpanMetricSeries, TimeRange,
} from '@/lib/types';
import type { DatabaseRef } from './databaseParam';
import { addressScopeNotice } from './addressScope';
import type { PhysicalAddrs } from '@/lib/types';

// detailSections — the /database page's body (v0.9.1366).
//
// PROMOTED, not copied. `pages/endpoints/detailSections.tsx:24-31` is the
// working precedent and this file is its sibling: the sections that were
// inlined in `pages/DatabaseDetail.tsx` moved here whole. Behaviour is
// unchanged section by section; what changed is that there is now ONE body
// with a name, instead of a page-length return tree.
//
// ── "ÇEKMECE VE TAM SAYFA AYNI ANDA" — KARAR (denetim §5.3(3)) ───────────
//
// Soru: bir veritabanı hem çekmece hem sayfa olarak yaşarsa ne paylaşılır?
// CEVAP, DB İÇİN: yaşamıyor — ve bu HEAD'de yeniden ölçüldü, denetimin
// sözüne güvenilmedi. `features/dependencies/DetailDrawer.tsx`in
// `kind === 'db'` dalı ÇALIŞMA ZAMANINDA ERİŞİLEMEZ:
// `DependenciesTable.tsx:469` `isOpen = !onRowNavigate && …` hesaplıyor ve
// /databases'in İKİ mount noktası da (`Databases.tsx:339`, `:381`)
// `onRowNavigate` geçiyor. Geçen tek gerçek tüketici `/messaging`
// (`Messaging.tsx:214`, `kind="queue"`, kontrollü kip). Yani db tarafında
// çekmece v0.9.840'ta emekli oldu; ikisi AYNI ANDA açık OLAMAZ.
//
// Bu yüzden buradaki sözleşme KAPSAYICI değil, SÖZ DAĞARCIĞI:
//
//   · Bölümler `mode` prop'u TAŞIMAZ ve kabuk hakkında hiçbir şey bilmez.
//     Bir varlık ileride gerçekten iki yüzeyde birden yaşarsa (denetim
//     F4.4, /messaging'in tam sayfaya terfisi), aynı bölümler İKİ AYRI
//     kabuğa sarılır — çekmece kabuğu `onClose` + odak tuzağı + `?open=`
//     yazar, sayfa kabuğu başlık + breadcrumb + PageShell yazar. İki kabuğu
//     tek bileşende `mode` prop'uyla birleştirmek, §6.1'in DÖRT ısırığının
//     (v0.9.256 / 821 / 846 / 873) tam olarak geldiği yerdir.
//   · VERİ ÇEKİMİ SAYFADA DURUR. Bölüm saf çizer; sorgu durumları
//     (`pending`/`error`/`rows`) prop olarak iner. Denetimin §1.4(b)
//     çözümü — daraltılmış bir uç eklenirse değişen tek yer sayfa olur.
//   · URL SAHİPLİĞİ: ebeveyn AÇILIŞ parametresini sahiplenir (`?stmt=`),
//     bölüm yalnız KENDİ alt-eksenini. Bugün hiçbir bölümün alt-ekseni yok,
//     o yüzden hiçbiri `useSearchParams` çağırmıyor — ihtiyaç kanıtlanmadan
//     API büyütülmüyor (`ui/PageShell.tsx:35-38`).
//
// KAPSAM DIŞI, bilinçli: `Stat` üçlüsünün birleştirilmesi (üçü bayt-bayt
// DEĞİL — `slowqueries/StmtDetailDrawer.tsx:169`de `minWidth: 0` ve
// `textTransform` YOK, birleştirme onun görünümünü DEĞİŞTİRİR) ve dört
// `CallersTable` nüshasının tek gövdeye inmesi (kolonlar gerçekten farklı,
// `Impact` iki ayrı anlamda: burada istemci hesabı, endpoints'te sunucudan
// gelen `sharePct`). İkisi de mockup ister; buraya taşınırken AYNEN
// kopyalandılar.
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// ── kimlik başlığı ───────────────────────────────────────────────────────

/**
 * DatabaseIdentityHeader — engine rozeti, ad, instance, pivotlar ve KAPSAM
 * satırı. Kapsam satırı v0.9.821'in dürüstlüğü: boş bir `db.name` GERÇEK
 * bir hâl ("bu instance üzerindeki her veritabanı"), eksik bir değer değil.
 */
export function DatabaseIdentityHeader({ refObj, env, range, physicalAddrs }: {
  refObj: DatabaseRef;
  env: string;
  range: TimeRange;
  /** v0.10.19 (F0.8) — ölçülmüş fiziksel adres kümesi; yoksa beyan yok. */
  physicalAddrs?: PhysicalAddrs;
}) {
  // v0.10.19 — `instance` bir MAKİNE DEĞİL, peer.service etiketi. Aynı
  // etiketi paylaşan adresler MV'de tek satıra çöküyor ve Scope satırı
  // bunu söylemiyordu (addressScope.ts).
  const addrNotice = addressScopeNotice(physicalAddrs, refObj.instance);
  return (
    <>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 10,
        flexWrap: 'wrap', marginBottom: 4,
      }}>
        <span className="badge b-info" style={{ fontSize: 10, textTransform: 'uppercase' }}>
          {refObj.system}
        </span>
        <span className="mono" style={{ fontSize: 15, fontWeight: 600 }}>
          {refObj.dbName || refObj.instance}
        </span>
        <span className="badge b-gray mono" style={{ fontSize: 10 }}
          title="db_summary_5m resolves the instance through six rungs (peer.service → server.address → net.peer.name → db.host → db.name → service.name); this is whichever one it found.">
          {refObj.instance}
        </span>
        {refObj.source === 'receiver' && (
          <span className="badge b-info" style={{ fontSize: 10 }}
            title="Discovered via an OpenTelemetry database receiver — the engine panel below reads its metrics directly.">
            via receiver
          </span>
        )}
        {env && (
          <span className="badge b-info" style={{ fontSize: 10 }}
            title="The overview this row came from was narrowed to this environment.">
            env={env}
          </span>
        )}
        {/* v0.9.1210 — EndpointDetail ile aynı bağlantı-düğme tedavisi
            (kardeş detay sayfası; operatör bildirimi "belli olmuyor").
            v0.9.1372 — operatör isteği ("Traces top statements mavi
            olsun"), yine EndpointDetail ile aynı adımda: `.sec` → `.accent`.
            İki sayfa aynı anda taşınıyor; birini bırakmak, kardeş detay
            sayfalarını farklı görsel dile ayırırdı. */}
        <span style={{ marginLeft: 'auto', display: 'flex', gap: 8, fontSize: 12 }}>
          <Link className="accent" style={{ fontSize: 12, padding: '3px 10px' }}
            to={dbTracesHref({
              window: range, system: refObj.system,
              instance: refObj.instance, dbName: refObj.dbName || undefined,
            })}>Traces →</Link>
          <Link className="accent" style={{ fontSize: 12, padding: '3px 10px', gap: 5 }}
            to={`/databases/slow-queries?dbsys=${encodeURIComponent(refObj.system)}`
              + (refObj.dbName ? `&dbname=${encodeURIComponent(refObj.dbName)}` : '')}>
            <Turtle size={13} strokeWidth={1.75} /> Top statements →
          </Link>
        </span>
      </div>

      {/* SCOPE LINE — the drawer's v0.9.821 honesty, carried over. An
          empty db.name is a real state, not a missing value, and it
          means something much wider than the row label suggests. */}
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 12 }}>
        Scope:{' '}
        <span className="mono" style={{ color: 'var(--text2)' }}>
          {refObj.system} / {refObj.instance}
        </span>
        {refObj.dbName
          ? <> · database <span className="mono" style={{ color: 'var(--text2)' }}>{refObj.dbName}</span></>
          : <> · <b>every database</b> on this instance (all db.name values)</>}
        {/* v0.10.19 (F0.8) — ADRES KAPSAMI. Yalnız ÖLÇÜLDÜYSE çıkar;
            ölçülmemiş bir sonucu "tek adres" diye okumak tekilliği
            yanlış yere iddia etmek olurdu. */}
        {addrNotice && (
          <> · <span
            className={addrNotice.multiple ? 'badge b-warn' : 'badge b-gray'}
            style={{ fontSize: 10 }}
            title={addrNotice.detail}>
            {addrNotice.label}
          </span></>
        )}
      </div>
    </>
  );
}

// ── altın-sinyal şeridi ──────────────────────────────────────────────────

/** DatabaseSignalStrip — sekiz karo, `/api/databases/detail` payload'ından. */
export function DatabaseSignalStrip({ d }: { d: DBDetail }) {
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(104px, 1fr))',
      gap: 8, marginBottom: 14,
    }}>
      <StatTile label="Calls">{fmtNum(d.spanCount)}</StatTile>
      <StatTile label="Errors">{fmtNum(d.errorCount)}</StatTile>
      <StatTile label="Err rate"
        tone={d.errorRate > 5 ? 'err' : d.errorRate > 0 ? 'warn' : undefined}>
        {`${d.errorRate.toFixed(2)}%`}
      </StatTile>
      <StatTile label="Avg">{`${d.avgDurationMs.toFixed(1)} ms`}</StatTile>
      <StatTile label="P50">{msOrDash(d.p50DurationMs)}</StatTile>
      <StatTile label="P95">{msOrDash(d.p95DurationMs)}</StatTile>
      <StatTile label="P99">{`${d.p99DurationMs.toFixed(1)} ms`}</StatTile>
      {/* Total time — the mockup's extra tile, and the number
          that actually ranks a database against its siblings:
          calls × avg is the wall-clock this DB cost the fleet
          inside the window. */}
      <StatTile label="Total time">
        {fmtTotal(d.spanCount * d.avgDurationMs)}
      </StatTile>
    </div>
  );
}

// ── üç seri kartı ────────────────────────────────────────────────────────

/**
 * DatabaseTrendCards — Calls/s · Error % · P99, `/api/databases/trends`
 * payload'ının bu kimliğe düşen satırından. Fetch SAYFADA: uç filo geneli
 * ve /databases tablosunun tam olarak aynısı, yani sayfa tablonun sıcak
 * cache'ini miras alıyor ve paylaşılan uca satır-başına parametre eklenmiyor.
 */
export function DatabaseTrendCards({ trend, pending, xRange }: {
  trend: DBTrend | undefined;
  pending: boolean;
  xRange: { from: number; to: number };
}) {
  const series = useMemo(() => trendSeries(trend), [trend]);
  return (
    <div className="ov-grid ov-charts-3 ov-mb">
      {(['calls', 'errors', 'p99'] as const).map(k => (
        <LazyMount key={k} minHeight={170}>
          <Suspense fallback={<div style={{ height: 170, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              title={SERIES_TITLE[k]}
              storageKey={`database-detail-${k}`}
              height={150}
              unit={SERIES_UNIT[k]}
              xRange={xRange}
              // '-ms' = engine namespace (v0.9.789); one group so
              // the crosshair correlates the three.
              syncKey={msSyncKey('database-detail')}
              emptyReason={series[k].length ? undefined
                : (pending ? 'Yükleniyor…'
                  : 'Bu pencerede bu veritabanı için kova yok')}
              items={[{
                name: SERIES_TITLE[k],
                role: k === 'errors' ? 'error' : 'data',
                series: series[k],
              }]} />
          </Suspense>
        </LazyMount>
      ))}
    </div>
  );
}

// ── "Who calls this" ─────────────────────────────────────────────────────

/**
 * DatabaseCallersSection — "who calls this database", the panel /endpoint
 * copied in v0.9.839. Impact = calls × avg, the Elastic-APM reading the
 * drawer sorted by: a 200ms call made 10k times outweighs a 5s call made
 * twice for cumulative load on the backend.
 *
 * ── LOG PİVOTU NEDEN BURADA, BAŞLIKTA DEĞİL (v0.9.1367) ─────────────────
 *
 * Brief başlıkta, `dbTracesHref`in yanında bir "Logs →" istiyordu. Kod buna
 * itiraz ediyor ve itiraz ÖLÇÜLDÜ:
 *
 *   1. Bir veritabanının KENDİ logu YOK. Coremetry motor logu ingest etmiyor
 *      (waits & locks paneli tam bu yüzden v0.9.852'de söküldü). "Bu DB'nin
 *      logları" ancak "ona konuşanların logları" demek olabilir, ve bunu
 *      hangi çağırana atfettiğini SÖYLEMEYEN bir link yalan söyler.
 *   2. Çoklu çağıranı taşıyan taşınabilir bir log kapsamı YOK.
 *      `logstore.Filter` (internal/logstore/logstore.go:36) TEK bir
 *      `Service` alanı taşıyor — çoklu-servis kapsamı iki backend'de de
 *      ifade edilemiyor.
 *   3. ⚠ `q` ALAN SÖZDİZİMİ ES'E ÖZGÜ. ClickHouse tarafında serbest metin
 *      `multiSearchAnyCaseInsensitive(body, [?])` (clickhouse.go:310) —
 *      yani DÜZ BİR ALT DİZE. `q=service.name:"foo"` CH backend'inde
 *      body'de o literal metni arar ve boş döner. Instance adını `q`ya
 *      koyan bir başlık linki de aynı sınıfa girer: uygulama o adı log
 *      satırına yazmıyorsa link ÖLÜDÜR — v0.9.256/268'in iki kez ödenmiş
 *      hatası.
 *
 * Geriye kapsamı BELİRSİZ olmayan tek yer kalıyor: çağıranın kendi satırı.
 * `service` her iki backend'de de YAPISAL bir kolon yüklemi
 * (CH `service_name = ?`, ES servis alan fan-out'u), yani link taşınabilir.
 * Üstelik satır MV'den geliyor (`db_caller_summary_5m`) — tahmin değil,
 * ölçüm; yeni sorgu yok, ham `spans` yok, G6'ya dokunulmuyor.
 *
 * ⚠ Zaman payı BURADA İSTEMCİ HESABI (satırın payı, YÜKLENMİŞ satırların
 * toplamına göre); endpoints tarafında aynı kolon SUNUCUDAN gelen
 * `sharePct` ve paydası rotanın kendi toplamı. v0.9.1376'ya dek ikisinin
 * de başlığı `Impact`ti — aynı kelime iki ayrı büyüklüğün üstünde ve
 * hiçbirinin hangi büyüklük olduğunu söylemeden. Şimdi ikisi de `Time %`
 * ve PAYDALARI hücre başlıklarında yazılı; dört `CallersTable` nüshasının
 * tek gövdeye inmesi yine de bir mockup kararı, mekanik birleştirme
 * değil.
 */
export function DatabaseCallersSection({ callers, range, env }: {
  callers: DBCallerBreakdown[];
  // v0.9.967 — the page window, so a caller pill opens the service on the
  // same slice these impact numbers were computed over.
  range: TimeRange;
  /** v0.9.1367 — paylaşılan log linkini dürüst tutar (env kapsamı taşınır). */
  env?: string;
}) {
  const total = useMemo(
    () => callers.reduce((s, c) => s + c.spanCount * c.avgDurationMs, 0),
    [callers]);
  const dt = useDataTable<DBCallerBreakdown>({
    storageKey: 'database-detail-callers',
    columns: CALLER_COLS,
    rows: callers,
    initialSort: { id: 'impact', dir: 'desc' },
  });
  return (
    <Card header={<PanelTitle sub="service · pod, by time share">Who calls this</PanelTitle>}>
      {callers.length === 0 ? (
        <Empty icon="↘" title="No caller in this window">
          No application span reached this database in the selected range —
          either nothing called it, or the callers are not instrumented.
        </Empty>
      ) : (
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((c, i) => {
                const errCls = c.errorRate > 5 ? 'b-err' : c.errorRate > 0 ? 'b-warn' : 'b-ok';
                const impact = c.spanCount * c.avgDurationMs;
                return (
                  <tr key={`${c.service}|${c.pod}|${i}`}
                    style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                    <td style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                      title={c.service}>
                      <Link to={serviceHref(c.service, { range })}
                        className="mono" style={{ fontSize: 11.5 }}>
                        {c.service}
                      </Link>
                      {/* v0.9.1367 — LOG PİVOTU. Trace pivotu sayfanın
                          başlığında zaten vardı (dbTracesHref); eksik olan
                          buydu. Kolon EKLENMİYOR: glif, v0.9.257'nin
                          emsali ("bir tık ötedeki eylem, bir kolon
                          harcamadan görünür olur"). */}
                      <Link to={logsHref({ window: range, service: c.service, env })}
                        aria-label={`${c.service} loglarını aç`}
                        title={`${c.service} loglarını bu pencerede aç.\n\nBUNLAR ÇAĞIRANIN LOGLARI, veritabanının DEĞİL: Coremetry motor logu ingest etmiyor, dolayısıyla bir veritabanının "kendi" logu yok. Bir DB yavaşladığında sorulan soru zaten bu — hangi istemci, ve o istemci bu pencerede ne yazdı.`}
                        style={{
                          marginLeft: 6, fontSize: 10, whiteSpace: 'nowrap',
                          color: 'var(--accent2)', fontWeight: 500,
                        }}>
                        ≡ logs
                      </Link>
                    </td>
                    <td className="mono" style={{
                      fontSize: 11, color: 'var(--text2)',
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }} title={c.pod}>{c.pod}</td>
                    <td className="num mono">{fmtNum(c.spanCount)}</td>
                    <td className="num mono">
                      <span className={`badge ${errCls}`} style={{ fontSize: 9 }}>
                        {c.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">
                      {c.p95DurationMs === undefined
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : `${c.p95DurationMs.toFixed(1)} ms`}
                    </td>
                    <td className="num mono"
                      title={`Bu çağıranın duvar-saati payı (çağrı × ortalama).\n\nPAYDA YÜKLENMİŞ SATIRLAR: yüzdeler bu tablodaki çağıranların toplamına göre, veritabanının tüm trafiğine göre DEĞİL. Satır sayısı değişirse her yüzde değişir.`}>
                      {total > 0 ? `${((impact / total) * 100).toFixed(1)}%` : '—'}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ── "Top statements" ─────────────────────────────────────────────────────

/**
 * DatabaseStatementsSection — bu DB'ye daraltılmış ifade listesi.
 *
 * Sorgu durumu prop olarak iniyor (fetch sayfada); satır tıklaması
 * `onOpen`'a gidiyor çünkü `?stmt=` AÇILIŞ parametresi ve onun sahibi
 * ebeveyn (denetim §5.3(2)).
 */
export function DatabaseStatementsSection({
  refObj, rows, pending, error, errorMessage, onRetry, onOpen,
}: {
  refObj: DatabaseRef;
  rows: SlowQueryRow[] | undefined;
  pending: boolean;
  error: boolean;
  /**
   * `undefined` HATA YOKLUĞU DEĞİL: hata Error değilse (bir string
   * fırlatıldıysa) mesaj gerçekten yok ve `QueryError` kendi genel
   * metnini yazar. İki alanı tek bir `Error | null`a katlamak, o dalda
   * uydurma bir mesaj basmak olurdu — çıktı bugünküyle aynı kalmıyordu.
   */
  errorMessage?: string;
  onRetry: () => void;
  onOpen: (r: SlowQueryRow) => void;
}) {
  // v0.9.874 (tutarlılık denetimi BT18) — kardeşi CallersCard primitifteydi,
  // bu tablo elle <thead>'liydi. Genişlikler eski <th style={{width}}>
  // değerlerinden AYNEN alındı; Statement kolonu esner.
  const dt = useDataTable<SlowQueryRow>({
    storageKey: 'database-detail-statements', columns: DB_DETAIL_STMT_COLS,
    rows: rows ?? [], initialSort: { id: 'total', dir: 'desc' },
  });
  return (
    // v0.9.961 (UX denetimi G6/Ö11) — KAPSAM BEYANI. Bu panel instance'ı
    // HİÇ filtrelemiyor: sorgu yalnız (db_system, db_name) ile gidiyor ve
    // API imzasında instance yok. Sayfanın Scope satırı ise instance
    // vadediyor. Aynı motordan iki instance'lı bir kurulumda operatör DİĞER
    // makinenin ifadesini bu makineye atfediyordu. Tam düzeltme (uca
    // opsiyonel instance) OPERATÖR KARARIYLA kapsam dışı — G6, "bugünkü
    // kapsamla yaşa" (MV cerrahisi = 90g statement geçmişi feda). v0.9.821'in
    // aynı sayfadaki emsali gibi, kapsam ÖNCE söyleniyor.
    <Card header={
      <PanelTitle sub={refObj.dbName
        ? `${refObj.dbName} · all ${refObj.system} instances · click a row for detail`
        : `every database here · all ${refObj.system} instances · click a row for detail`}>
        Top statements
      </PanelTitle>
    }>
      {pending && <TableSkeleton rows={5} cols={4} wideFirst />}
      {/* v0.9.865 (tutarlılık denetimi MT1) — hata dalı HİÇ YOKTU:
          sorgu 500'lediğinde kart gövdesi bomboş kalıyordu (ne
          iskelet, ne mesaj), yani "bu pencerede pahalı ifade yok"
          diye okunuyordu. Boş dalın kardeşi olarak aynı Empty
          anatomisini kullanıyoruz. */}
      {error && (
        <QueryError onRetry={onRetry} message={errorMessage}>
          Top statements could not be loaded — this is a failed read,
          not an idle database.
        </QueryError>
      )}
      {rows && rows.length === 0 && (
        <Empty icon="◷" title="No statement in this window">
          Nothing matched <code>{refObj.system}</code>
          {refObj.dbName ? <> / <code>{refObj.dbName}</code></> : null} in the
          selected range.
        </Empty>
      )}
      {(rows ?? []).length > 0 && (
        <div className="table-wrap">
          <table style={{ width: '100%', fontSize: 12, tableLayout: 'fixed' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((r, i) => (
                <tr key={r.stmtHash ?? i}
                  onClick={() => onOpen(r)}
                  title={`${r.sampleStatement || r.statement}\n\ncalled by ${r.service}`}
                  style={{ cursor: r.stmtHash ? 'pointer' : 'default' }}>
                  <td className="mono" style={{
                    maxWidth: 0, overflow: 'hidden',
                    textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }}>{r.statement}</td>
                  <td className="num mono">{fmtNum(r.count)}</td>
                  <td className="num mono">{r.p95Ms.toFixed(1)} ms</td>
                  <td className="num mono"><b>{(r.totalMs / 1000).toFixed(1)} s</b></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ── motor paneli ─────────────────────────────────────────────────────────

/**
 * DatabaseEnginePanels — receiver rows ONLY. A span-derived row has no
 * receiver behind it, and rendering the panel anyway would bleed another
 * instance's engine metrics into a row that never had any (the drawer's
 * `source` gate, kept).
 */
export function DatabaseEnginePanels({ refObj, range }: {
  refObj: DatabaseRef;
  range: TimeRange;
}) {
  if (refObj.source !== 'receiver') return null;
  const engine = refObj.system.toLowerCase();
  return (
    <>
      {engine === 'oracle' && <OraclePanel instance={refObj.instance} range={range} />}
      {(engine === 'postgresql' || engine === 'postgres') && (
        <PostgresPanel instance={refObj.instance} range={range} />
      )}
      {(engine === 'mysql' || engine === 'mariadb') && (
        <MySQLPanel instance={refObj.instance} range={range} />
      )}
      {engine === 'redis' && <RedisPanel instance={refObj.instance} range={range} />}
    </>
  );
}

// ── dosya-yerel atomlar ve sabitler ──────────────────────────────────────
//
// AYNEN taşındı. `Stat` bilinçli olarak yerel kalıyor: repo genelindeki
// sekiz nüsha bayt-bayt DEĞİL (`slowqueries/StmtDetailDrawer.tsx:169`de
// `minWidth: 0` ve `textTransform` yok), birleştirme görünür bir değişiklik
// + bir prop yüzeyi kararı (`value` mü `children` mi) ister. Mockup şartı.

const SERIES_TITLE = { calls: 'Calls / s', errors: 'Error %', p99: 'P99 latency' } as const;
const SERIES_UNIT = { calls: 'reqps', errors: 'percent', p99: 'ms' } as const;

// v0.9.874 (tutarlılık denetimi BT18). Genişlikler eski elle yazılmış
// <th style={{width}}> değerlerinden AYNEN alındı.
const DB_DETAIL_STMT_COLS: DataTableColumn<SlowQueryRow>[] = [
  { id: 'statement', label: 'Statement', sortValue: r => r.statement, naturalDir: 'asc', flex: true },
  { id: 'calls',     label: 'Calls',     sortValue: r => r.count,     numeric: true, width: 80 },
  { id: 'p95',       label: 'P95',       sortValue: r => r.p95Ms,     numeric: true, width: 90 },
  { id: 'total',     label: 'Total',     sortValue: r => r.totalMs,   numeric: true, width: 90 },
];

const CALLER_COLS: DataTableColumn<DBCallerBreakdown>[] = [
  { id: 'service', label: 'Service',    sortValue: r => r.service,        naturalDir: 'asc', width: 190 },
  { id: 'pod',     label: 'Pod / host', sortValue: r => r.pod,            naturalDir: 'asc', width: 170 },
  { id: 'calls',   label: 'Calls',      sortValue: r => r.spanCount,      numeric: true, width: 80 },
  { id: 'errRate', label: 'Err %',      sortValue: r => r.errorRate,      numeric: true, width: 76 },
  { id: 'p95',     label: 'P95',        sortValue: r => r.p95DurationMs ?? 0, numeric: true, width: 82 },
  // v0.9.1376 — 'Impact' → 'Time %'. Eski başlık hangi büyüklüğü
  // gösterdiğini SÖYLEMİYORDU ve aynı kelime endpoints'te BAŞKA bir
  // sayının üstünde duruyordu. Hücre zaten yüzde basıyor
  // (impact / total × 100); değer DEĞİŞMEDİ, yalnız adı doğru oldu.
  { id: 'impact',  label: 'Time %',     sortValue: r => r.spanCount * r.avgDurationMs, numeric: true, width: 80 },
];

/** trendSeries — one DBTrend's buckets into the three CorePanel series.
 *  DBTrendPoint.t is already unix NANOSECONDS (the SpanMetricSeries
 *  contract), so there is no unit conversion here to get wrong. */
function trendSeries(t: DBTrend | undefined): {
  calls: SpanMetricSeries[]; errors: SpanMetricSeries[]; p99: SpanMetricSeries[];
} {
  const pts = t?.points ?? [];
  if (pts.length === 0) return { calls: [], errors: [], p99: [] };
  const build = (pick: (p: typeof pts[number]) => number, label: string): SpanMetricSeries[] =>
    [{ groupKey: [label], points: pts.map(p => ({ time: p.t, value: pick(p) })) }];
  return {
    calls: build(p => p.rps, 'Calls / s'),
    errors: build(p => p.errorRate, 'Error %'),
    p99: build(p => p.p99Ms, 'P99 ms'),
  };
}

// msOrDash — v0.9.263, kept verbatim: a duration the backend did not
// send is '—', never "0.0 ms". 0.0 would read as "instantaneous"
// rather than "not reported".
function msOrDash(v?: number): string {
  return v === undefined ? '—' : `${v.toFixed(1)} ms`;
}

/** fmtTotal — window-wide wall clock, in the largest unit that keeps
 *  the number readable. */
function fmtTotal(ms: number): string {
  if (ms < 1000) return `${ms.toFixed(0)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} min`;
  return `${(ms / 3_600_000).toFixed(1)} h`;
}


