import { lazy, Suspense, useEffect, useMemo, useState } from 'react';
import { messagingTracesHref } from '@/lib/pivotHref';
import { Link } from 'react-router-dom';
import { Spinner } from '@/components/Spinner';
import { LazyMount } from '@/components/LazyMount';
import { api } from '@/lib/api';
import { fmtNum, fmtNs, timeRangeToNs } from '@/lib/utils';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, DBDetail, MessagingDetail, SpanMetricSeries } from '@/lib/types';
import { Stat, statementTracesHref } from './panels/shared';
import { OraclePanel } from './panels/OraclePanel';
import { PostgresPanel } from './panels/PostgresPanel';
import { MySQLPanel } from './panels/MySQLPanel';
import { RedisPanel } from './panels/RedisPanel';

// v0.9.814 — drawer'ın mini panelleri de CorePanel. LAZY: @grafana/*
// statik import edilseydi /messaging + /databases vendor chunk'ı ~1 MB
// büyürdü ve drawer AÇILMADAN da ödenirdi (Overview.tsx:26-29 ölçümü).
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// kindSeries — {timeS, …} kovalarını CorePanel'in tek serisine çevirir.
// time unix NANOSANİYE (SpanMetricSeries sözleşmesi).
function kindSeries<T extends { timeS: number }>(
  points: T[], pick: (p: T) => number, label: string,
): SpanMetricSeries[] {
  return [{
    groupKey: [label],
    points: points.map(p => ({ time: p.timeS * 1e9, value: pick(p) })),
  }];
}

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
  const [data, setData] = useState<D | null | undefined>(undefined);

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    const p = kind === 'db'
      // v0.9.821 — kimlik ÜÇLÜ ve GÖRÜNEN ETİKETTEN bağımsız:
      // (system, instance, dbName). instance verilmemişse (messaging
      // tarafı, eski çağıranlar) etikete düşülür.
      ? api.databaseDetail(system, instance ?? name, dbName ?? '', from, to)
      : api.messagingDetail(system, cluster, name, from, to);
    p.then(r => setData(r ?? null))
     .catch(() => setData(null));
  }, [system, cluster, name, instance, dbName, kind, range]);

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
  const drawerSync = `msg-drawer:${system}|${cluster}|${name}-ms`;

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
  const allTopOps  = data.topOps ?? [];

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

  // v0.8.364 (Stage-2 M1) — produce/consume series off
  // messaging_caller_summary_5m (kind × time_bucket). Optional
  // (`?? []`): a stale pre-M1 cached payload simply renders the
  // drawer without sparklines mid-rolling-deploy.
  const msgSeries = kind === 'queue' && 'series' in data
    ? (data as MessagingDetail).series ?? []
    : [];

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

  return (
    <div>
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
      {(msgSeries.length > 1 || e2eSeries.length > 1) && (
        <div className="ov-grid ov-charts-3 ov-mb">
          {msgSeries.length > 1 && (
            <LazyMount minHeight={170}>
              <Suspense fallback={<div style={{ height: 170, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
                <CorePanelMultiLazy
                  title="Üretim vs Tüketim"
                  storageKey="msg-drawer-rate"
                  height={150} unit="cpm" xRange={drawerXRange} syncKey={drawerSync}
                  items={[
                    { name: 'Üretim', role: 'data', series: kindSeries(msgSeries, p => p.produceCount / 5, 'Üretim') },
                    { name: 'Tüketim', role: 'success', series: kindSeries(msgSeries, p => p.consumeCount / 5, 'Tüketim') },
                  ]} />
              </Suspense>
            </LazyMount>
          )}
          {e2eSeries.length > 1 && (
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
                  to={`/trace?id=${encodeURIComponent(e2e.slowestConsumerTraceId)}`}
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
            tone="producer" />
          <CallerSection
            title={`Consumers · ${consumers.length} ${consumers.length === 1 ? 'row' : 'rows'}`}
            rows={consumers}
            emptyMessage="No consumer spans for this destination in the window."
            tone="consumer" />
          {otherClients.length > 0 && (
            <CallerSection
              title={`Other clients · ${otherClients.length}`}
              rows={otherClients}
              emptyMessage=""
              tone="other" />
          )}
        </>
      ) : (
        <CallerSection
          title={`By client (service + pod) · ${callers.length} ${callers.length === 1 ? 'row' : 'rows'}`}
          rows={callers}
          emptyMessage="No callers in this window."
          tone="db" />
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
          <div className="table-wrap" style={{ maxHeight: 240, overflowY: 'auto' }}>
            <table>
              <thead style={{ position: 'sticky', top: 0, background: 'var(--bg1)', zIndex: 1 }}>
                <tr>
                  <th>{kind === 'db' ? 'Statement' : 'Operation'}</th>
                  <th className="num">Count</th>
                  <th className="num">Avg</th>
                </tr>
              </thead>
              <tbody>
                {allTopOps.map((o, i) => (
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
                              ? statementTracesHref(o.statement)
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

// CallerSection renders one labelled table of (service, pod) rows
// with their RED metrics. Tone colours the header so producers /
// consumers / DB clients read at a glance. Empty sections render
// a one-line placeholder so the operator sees that we did look
// for the data and there just isn't any in this window.
//
// Sort headers added in v0.4.92 — sorting is client-side because
// the backend caps the result at LIMIT 100, so a 100-row sort
// is O(n log n) ≈ 700 comparisons on the operator's machine.
// At enterprise scale (5k+ pods) the same cap protects: the
// drawer always shows the top-100-by-calls cohort and the
// operator sorts within that page, no full-fleet re-fetch
// trip. Default sort is Calls desc so the heaviest caller
// keeps surfacing on first paint.
//
// v0.7.x — the last bespoke sort-header variant in the app
// retired: these nested tables now ride the shared useDataTable
// primitive (same depCols + DataTableHead/DataTableColgroup
// contract the page-level table above uses). The client-side
// search filter is preserved and feeds filtered rows into the
// primitive; sort + column-resize layout persist per-tone.

function CallerSection({ title, rows, emptyMessage, tone }: {
  title: string;
  rows: import('@/lib/types').DBCallerBreakdown[];
  emptyMessage: string;
  tone: 'producer' | 'consumer' | 'other' | 'db';
}) {
  type Caller = import('@/lib/types').DBCallerBreakdown;
  const dotColor =
    tone === 'producer' ? 'var(--accent2)' :
    tone === 'consumer' ? 'var(--ok)' :
    tone === 'other'    ? 'var(--text3)' :
                          'var(--accent2)';
  const hasRole = rows.some(r => r.role);
  // Client-side search across service / pod / role so an
  // operator with hundreds of pods hitting one DB can pinpoint
  // a specific instance fast. Backend caps the result set at
  // LIMIT 500 (v0.5.12) so filtering 500 rows stays
  // sub-millisecond.
  const [search, setSearch] = useState('');
  const filtered = useMemo(() => {
    const t = search.trim().toLowerCase();
    if (!t) return rows;
    return rows.filter(r =>
      r.service.toLowerCase().includes(t) ||
      r.pod.toLowerCase().includes(t) ||
      (r.role ?? '').toLowerCase().includes(t));
  }, [rows, search]);

  // Caller columns mirror the prior CallerSortTh sort keys +
  // CALLER_NATURAL directions. Role is conditional on hasRole
  // (messaging-only). Default sort = Calls desc (preserved).
  const callerCols = useMemo<DataTableColumn<Caller>[]>(() => [
    { id: 'service', label: 'Service',    sortValue: r => r.service, naturalDir: 'asc', width: 200 },
    { id: 'pod',     label: 'Pod / host', sortValue: r => r.pod,     naturalDir: 'asc', width: 200 },
    ...(hasRole
      ? [{ id: 'role', label: 'Role', sortValue: (r: Caller) => r.role ?? '', naturalDir: 'asc', width: 110 } as DataTableColumn<Caller>]
      : []),
    { id: 'calls',   label: 'Calls', sortValue: r => r.spanCount,     numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'errRate', label: 'Err %', sortValue: r => r.errorRate,     numeric: true, naturalDir: 'desc', width: 90 },
    { id: 'avg',     label: 'Avg',   sortValue: r => r.avgDurationMs, numeric: true, naturalDir: 'desc', width: 84 },
    // v0.9.273 — P50, and v0.9.263 P95, on the shared DBCallerBreakdown. BOTH
    // producer queries project them; if only one did, the other drawer would
    // render a hard 0.0ms here. Order matches the aggregate strip above:
    // Avg → P50 → P95 → P99.
    { id: 'p50',     label: 'P50',   sortValue: r => r.p50DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p95',     label: 'P95',   sortValue: r => r.p95DurationMs ?? 0, numeric: true, naturalDir: 'desc', width: 84 },
    { id: 'p99',     label: 'P99',   sortValue: r => r.p99DurationMs, numeric: true, naturalDir: 'desc', width: 84 },
  ], [hasRole]);

  // Distinct storageKey per tone so the Publishers / Consumers /
  // Other / DB instances rendered side-by-side don't share (and
  // clobber) each other's sort + width layout.
  const dt = useDataTable<Caller>({
    storageKey: `deps-callers-${tone}`,
    columns: callerCols,
    rows: filtered,
    initialSort: { id: 'calls', dir: 'desc' },
  });

  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8,
        fontSize: 12, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
      }}>
        <span aria-hidden style={{
          width: 8, height: 8, borderRadius: 2, background: dotColor,
        }} />
        {title}
        {rows.length > 10 && (
          <input value={search} onChange={e => setSearch(e.target.value)}
            placeholder="Search service / pod / role…"
            style={{
              marginLeft: 'auto', fontSize: 11, padding: '3px 8px',
              width: 200, fontWeight: 400,
            }} />
        )}
        {search && (
          <span style={{ fontSize: 10, color: 'var(--text3)', fontWeight: 400 }}>
            {dt.sortedRows.length} of {rows.length}
          </span>
        )}
      </div>
      {rows.length === 0 ? (
        emptyMessage && <div style={{ fontSize: 12, color: 'var(--text3)' }}>{emptyMessage}</div>
      ) : (
        // v0.7.x — adopting the shared primitive retires the inner-scroll
        // wrapper (its sticky <thead> went with the bespoke header). Rows
        // are capped at LIMIT 500 server-side; content-visibility:auto on
        // each row lets the browser skip off-screen ones per CLAUDE.md's
        // "tables > 100 rows" guidance, matching the Service.tsx (v0.7.54)
        // adopter pattern.
        <div className="table-wrap">
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <DataTableColgroup dt={dt} />
            <DataTableHead dt={dt} />
            <tbody>
              {dt.sortedRows.map((c, i) => {
                const errCls = c.errorRate > 5 ? 'err' : c.errorRate > 0 ? 'warn' : 'ok';
                return (
                  <tr key={`${c.service}|${c.pod}|${c.role ?? ''}|${i}`}
                      style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                    <td>
                      <Link to={`/service?name=${encodeURIComponent(c.service)}`}
                            style={{ fontFamily: 'monospace', fontSize: 12 }}>
                        {c.service}
                      </Link>
                    </td>
                    <td style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text2)' }}>
                      {c.pod}
                    </td>
                    {hasRole && (
                      <td>
                        {c.role && <RoleBadge role={c.role} />}
                      </td>
                    )}
                    <td className="num mono">{fmtNum(c.spanCount)}</td>
                    <td className="num mono">
                      <span className={`badge b-${errCls}`} style={{ fontSize: 9 }}>
                        {c.errorRate.toFixed(2)}%
                      </span>
                    </td>
                    <td className="num mono">{c.avgDurationMs.toFixed(1)}ms</td>
                    <td className="num mono">
                      {c.p50DurationMs === undefined
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : <>{c.p50DurationMs.toFixed(1)}ms</>}
                    </td>
                    <td className="num mono">
                      {c.p95DurationMs === undefined
                        ? <span style={{ color: 'var(--text3)' }}>—</span>
                        : <>{c.p95DurationMs.toFixed(1)}ms</>}
                    </td>
                    <td className="num mono">{c.p99DurationMs.toFixed(1)}ms</td>
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

function RoleBadge({ role }: { role: string }) {
  const r = role.toLowerCase();
  const tone =
    r === 'producer' ? { bg: 'color-mix(in srgb, var(--accent) 15%, transparent)', fg: 'var(--accent2)' } :
    r === 'consumer' ? { bg: 'color-mix(in srgb, var(--ok) 15%, transparent)', fg: 'var(--ok)' } :
    r === 'client'   ? { bg: 'var(--bg3)',            fg: 'var(--text2)' } :
                       { bg: 'var(--bg3)',            fg: 'var(--text3)' };
  return (
    <span style={{
      fontSize: 10, padding: '1px 6px', borderRadius: 3, fontWeight: 600,
      fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      background: tone.bg, color: tone.fg,
      textTransform: 'uppercase', letterSpacing: '.5px',
    }}>{role}</span>
  );
}
