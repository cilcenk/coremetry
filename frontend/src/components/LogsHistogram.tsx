import { useEffect, useMemo, useState, useRef } from 'react';
import { api } from '@/lib/api';
import { logsBucketSec } from '@/lib/chartStep';
import { severityBandOf } from '@/lib/severityBand';
import { TimeChart, type TimeChartSeries } from '@/components/charts/TimeChart';

// LogsHistogram — log volume over time for /logs, the service Logs tab and
// the anomaly drawer.
//
// v0.9.218 — was 362 lines of hand-drawn SVG stacking one band per severity.
// That stack was the reason the chart couldn't answer its own question: bands
// are stacked linearly against a shared max, so at a realistic INFO:ERROR
// ratio of 100:1 the error band computes to well under a pixel and got
// clamped to a 0.5px hairline. The component's own header comment promised
// "a spike of errors stands out" — untrue anywhere the ratio is worse than
// about 1:20, i.e. in production. It also spent ~95% of its ink on INFO in
// the page's accent colour: the most mundane data got the loudest treatment.
//
// Now a thin adapter over the shared <TimeChart> primitive (VolumeChart is
// the precedent). Total volume in a neutral grey, the ERROR share overlaid
// in red, and the error RATE as a line on the right axis — so a spike that
// is invisible by count is unmissable by proportion. Switching to the shared
// primitive also inherits the y axis, gridlines, crosshair tooltip,
// double-click zoom-reset and theme re-resolution the hand-drawn version
// never had, and fixes a real brush bug: bar x positions were index-based
// while the selection interpolated linear time, so on the sparse series ES/CH
// actually return (min_doc_count: 1 drops empty buckets) dragging over a
// visible spike selected a DIFFERENT window — worst exactly when the ERROR
// facet was on and the series was at its sparsest.

type Filter = {
  service: string;
  cluster?: string; // v0.9.216 — was absent, so the chart ignored the toolbar's cluster select
  env?: string; // v0.8.400 — global ?env= deployment-environment filter
  search: string;
  severity: number;
  traceId: string;
  spanId: string;
  // v0.9.287 — was absent, exactly like `cluster` before v0.9.216, and
  // with the same consequence: "◆ With trace" narrowed the severity
  // chips and the table but NOT the chart between them. The parent
  // already spreads the value in; only the type and the request were
  // missing it.
  hasTrace?: boolean;
};

type Series = { name: string; points: { t: number; v: number }[] };

// v0.9.1220 (Kibana dilim 3) — histogram kırılımı. Kural: bir eksen
// ancak İKİ backend de gerçekten uyguladığında sunulur; tanınmayan
// groupBy tek '_total' serisine düşer ve o sessiz cevapsızlık, seçicide
// duran bir seçeneğin arkasında en kötü hâline gelir. 1220 bu yüzden
// yalnız seviye + servis ile çıktı.
//
// v0.9.1250 — cluster + namespace de backend'de gerçek oldu: CH'de
// chLogsGroupExpr (hazır cluster ifadesi + yeni namespace ifadesi),
// ES'te aday-alan-başına terms agg + Go birleştirmesi
// (es_group_fields.go). Whitelist artık handler'da da var
// (normalizeLogsGroupBy) — bu union oraya bağlı.
export type LogsBreakdown = 'severity' | 'service' | 'cluster' | 'namespace';

export const BREAKDOWNS: LogsBreakdown[] = ['severity', 'service', 'cluster', 'namespace'];

// Etiketler: "seviye|servis" TR, "cluster|namespace" olduğu gibi —
// ürün dili karışık ve bu ikisi zaten operatörün kubectl/OpenShift
// kelimeleri (çeviri onları tanınmaz yapardı).
export const BREAKDOWN_LABEL: Record<LogsBreakdown, string> = {
  severity: 'seviye', service: 'servis', cluster: 'cluster', namespace: 'namespace',
};

// parseBreakdown — URL / select değerini üniona daraltır. Bilinmeyen
// değer seviyeye düşer (sayfanın varsayılanı); backend'in aynı
// durumdaki davranışı _total, yani ikisi de "uydurma bir kırılım
// gösterme" tarafında.
export function parseBreakdown(v: string | null | undefined): LogsBreakdown {
  return BREAKDOWNS.includes(v as LogsBreakdown) ? (v as LogsBreakdown) : 'severity';
}

// histogramFeedsChips — /logs'un seviye ÇİPLERİNİ neyin beslediği
// kuralı (v0.9.1250, v0.9.358 sözleşmesinin tek kaynağı). Grafik
// onSeries'i YALNIZ seviye kırılımında yayar; seviye süzgeci açıkken
// de toplamlar süzülmüş alt-küme olur. İkisinden biri bozulursa
// ebeveyn kendi hacim sorgusuna (volumeQ) döner — cluster/namespace
// kırılımında olması gereken de budur.
export function histogramFeedsChips(bd: LogsBreakdown, severityFloor: number): boolean {
  return severityFloor === 0 && bd === 'severity';
}

export function LogsHistogram({ range, filter, onRangeSelect, onZoomReset, onSeries, breakdown, onBreakdown }: {
  range: { from?: number; to?: number };
  filter: Filter;
  // Drag-select a horizontal span → called with the selection as unix-ns
  // bounds; the parent narrows its time range. Omitted → hover-only.
  onRangeSelect?: (fromNs: number, toNs: number) => void;
  // v0.9.373 — çift-tık geri: TimeChart'ın zoom-yığını pop'una aynen
  // iletilir; verilmezse eski davranış (yalnız brush).
  onZoomReset?: () => void;
  // v0.9.358 — opsiyonel; verilirse fetch edilen serilerin bant toplamları
  // iletilir. SEVİYE-sözleşmeli: diğer eksenlerde ÇAĞRILMAZ (seri adları
  // artık bant değil — çip sayaçlarını servis adlarıyla beslemek v0.9.216
  // sınıfı bir sessiz-yanlış olurdu). v0.9.1220: fetch HATASI null iletir
  // (tri-state) — ebeveyn sonsuz '·' yerine hata/geri-düşüş gösterebilsin
  // (v0.9.215 "error leg renders nothing" sınıfı).
  onSeries?: (s: { name: string; total: number }[] | null) => void;
  // v0.9.1220 — kırılım ekseni; verilmezse seviye (eski davranış bire bir).
  breakdown?: LogsBreakdown;
  // Verilirse başlıkta kırılım seçicisi çizilir (yalnız /logs geçirir;
  // servis Logs sekmesi + anomali çekmecesi seviye modunda kalır).
  onBreakdown?: (b: LogsBreakdown) => void;
}) {
  const [data, setData] = useState<Series[] | null | undefined>(undefined);
  // v0.9.358 — the per-band series this chart ALREADY fetches, exposed to the
  // parent so chip counts can be honest without a second request. Ref'd (the
  // onZoomRef pattern) so a per-render callback identity doesn't re-run the
  // fetch effect.
  const onSeriesRef = useRef(onSeries); onSeriesRef.current = onSeries;
  const bd: LogsBreakdown = breakdown ?? 'severity';

  useEffect(() => {
    setData(undefined);
    if (!range.from && !range.to && !filter.traceId) {
      // No bounded window AND no trace pin → don't blow the chart up on full
      // retention; the table below applies its own bound.
      setData([]);
      return;
    }
    let alive = true;
    api.logsTimeseries({
      from: range.from, to: range.to,
      service: filter.service || undefined,
      cluster: filter.cluster || undefined, // v0.9.216
      env:     filter.env     || undefined, // v0.8.400 — global env filter
      search:  filter.search  || undefined,
      severity: filter.severity > 0 ? filter.severity : undefined,
      traceId: filter.traceId || undefined,
      hasTrace: filter.hasTrace || undefined, // v0.9.287
      groupBy: bd,
      bucketSec: pickBucket(range),
    })
      .then(d => {
        // v0.9.1220 review — bayat-yanıt korkuluğu: dep değişimiyle aşılmış
        // bir istek geç çözülünce ne grafiği (kırılım değişmişse seviye
        // bantları "servis" çizgisi olarak çizilirdi) ne çipleri (ERROR
        // tabanının alt-küme toplamları All sayımı olarak kalırdı) ezebilir.
        if (!alive) return;
        setData(d ?? []);
        // Seviye sözleşmesi (v0.9.358): servis kırılımında sessiz kal —
        // ebeveynin çip sayaçları o zaman kendi seviye sorgusuna döner.
        if (bd === 'severity') {
          onSeriesRef.current?.((d ?? []).map(sr => ({
            name: sr.name,
            total: sr.points.reduce((a, p) => a + p.v, 0),
          })));
        }
      })
      .catch(() => {
        if (!alive) return;
        setData(null);
        // v0.9.1220 — hata da bir sonuçtur: fold modundaki ebeveyn sonsuz
        // '·' yerine rozet gösterebilsin (v0.9.215 sınıfı).
        if (bd === 'severity') onSeriesRef.current?.(null);
      });
    return () => { alive = false; };
  }, [range.from, range.to, filter.service, filter.cluster, filter.env, filter.search, filter.severity, filter.traceId, filter.hasTrace, bd]);

  const { times, series, totals } = useMemo(
    // v0.9.1250 — seviye DIŞINDAKİ her eksen grup katlamasını kullanır
    // (bd === 'service' testi yeni iki ekseni sessizce seviye
    // bantlarına sokardı: cluster adları severityBandOf'tan geçip
    // OTHER'a yığılırdı).
    () => (bd === 'severity'
      ? collapse(data ?? [], filter.severity > 0)
      : collapseGroups(data ?? [])),
    [data, filter.severity, bd]);

  if (data === undefined) return <div style={{ height: 104, marginBottom: 10 }} />;
  // v0.9.1220 review — hata/boş durumda TAMAMEN kaybolmak, kırılım
  // seçicisini de götürüyordu: ?breakdown=service'te hatalı/boş bir
  // pencereye düşen operatörün seviyeye dönecek UI'ı kalmıyordu. Seçici
  // taşıyan kullanımda gövde yerine tek satırlık dürüst not çizilir;
  // seçicisiz kullanımlar (servis sekmesi, anomali çekmecesi) eski
  // davranışta (hiç çizme).
  const degraded = data === null ? 'histogram yüklenemedi'
    : times.length === 0 ? 'bu pencerede seri yok' : '';
  if (degraded && !onBreakdown) return null;

  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 8, marginBottom: 10,
    }}>
      <div style={{
        display: 'flex', justifyContent: onBreakdown ? 'space-between' : 'flex-end',
        alignItems: 'center', marginBottom: 4,
        fontSize: 10, color: 'var(--text-faint)',
        fontFamily: 'ui-monospace, monospace',
      }}>
        {/* v0.9.1220 — kırılım seçicisi (Kibana "Break down by"). Sabit
            küçük küme → düz select (frontend-conventions §3); v0.9.1250'de
            4 eksene çıktı, hâlâ picker eşiğinin altında. */}
        {onBreakdown && (
          <label style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
            kırılım
            <select value={bd} aria-label="Histogram kırılımı"
              onChange={e => onBreakdown(parseBreakdown(e.target.value))}
              style={{ fontSize: 10, padding: '1px 4px' }}>
              {BREAKDOWNS.map(b => (
                <option key={b} value={b}>{BREAKDOWN_LABEL[b]}</option>
              ))}
            </select>
          </label>
        )}
        {/* The hand-drawn version's only hint that it was draggable was a
            crosshair cursor. Say it. */}
        <span>
          {totals.ratePct !== null && totals.error > 0 && `${fmtPct(totals.ratePct)} hata · `}
          {/* v0.9.287 — a severity floor narrows the denominator, so the
              rate isn't the error rate any more. Say that instead of
              printing a plausible wrong number (or nothing at all). */}
          {bd === 'severity' && totals.ratePct === null && filter.severity > 0 && (
            <span title="Hata oranı tüm loglara göre hesaplanır. Seviye süzgeci açıkken payda zaten süzülmüş oluyor, o yüzden oran gösterilmiyor — süzgeci kaldırınca geri gelir.">
              seviye süzgeci açık — oran yok ·{' '}
            </span>
          )}
          {/* v0.9.373 — ipucu dürüst: kablosuz grafikte "sürükle" yazıp
              hiçbir şey yapmamak, tam hata sivrisini daraltmak isteyen
              operatörü sessizce yarı yolda bırakıyordu. */}
          {/* v0.9.431 — "çift tık = geri" YALNIZ reset bağlıyken vaat
              edilir (dürüst ipucu sınıfı, v0.9.373 devamı). */}
          {degraded ? degraded
            : onRangeSelect
              ? (onZoomReset ? 'sürükle = zaman seç · çift tık = geri' : 'sürükle = zaman seç')
              : 'hover = detay'}
        </span>
      </div>
      {!degraded && (
        <TimeChart
          times={times}
          series={series}
          height={140}
          rightUnit="%"
          fmtRight={fmtPct}
          onBrush={onRangeSelect
            ? (fromMs, toMs) => onRangeSelect(fromMs * 1e6, toMs * 1e6)
            : undefined}
          onZoomReset={onZoomReset}
          // v0.9.489 (operatör: "Series gözükmesine ihtiyacım yok") — seviye
          // kimliği zaten yüzeydeki level chip'lerinde/renklerde; alttaki
          // istatistik lejantı log yüzeylerinde tamamen kapalı.
          // v0.9.1220 — servis kırılımında AÇIK: seri kimliği artık renkten
          // okunamaz (servis adları), lejant o kimliğin tek taşıyıcısı.
          // v0.9.1250 — cluster/namespace de aynı sınıf: seviye dışındaki
          // her eksende lejant açık.
          hideLegend={bd === 'severity'}
        />
      )}
    </div>
  );
}

// pickBucket picks a histogram resolution from the window size — the same
// heuristic Explore uses, so the chart never has more than ~120 buckets
// (browser-friendly) or fewer than ~20 (looks empty).
function pickBucket(range: { from?: number; to?: number }): number {
  // v0.9.707 — Logs.pickVolumeBucket ile TEK kaynak: logsBucketSec.
  // "Aynı kalmalı" yorumu artık yapısal — kopya merdiven kalktı.
  if (!range.from || !range.to) return 30;
  return logsBucketSec((range.to - range.from) / 1_000_000_000);
}

function fmtPct(v: number): string {
  if (v <= 0) return '0%';
  if (v < 1) return `${v.toFixed(2)}%`;
  return `${v.toFixed(v < 10 ? 1 : 0)}%`;
}

// collapse folds the per-severity series into the three the chart draws:
// total volume, the ERROR share of it, and the error rate. Severity names
// vary by shipper (casing, numeric severity_number strings, custom
// vocabularies) so every name resolves through severityBandOf — the same
// classifier the chips and badges use, so the chart can never disagree
// with the numbers beside it.
//
// severityFiltered (v0.9.287) — the caller applied a severity floor, so
// the rows we got back are already a subset. The error RATE is
// undefined against that denominator and is withheld rather than
// guessed; totals.ratePct goes null for the same reason.
// collapseGroups (v0.9.1220) — seviye dışı kırılımlar (servis, v0.9.1250
// ile cluster + namespace): toplamda ilk 5 seri
// kendi çizgisiyle, kalanı tek "diğer" çizgisinde. Çizgi (bar değil):
// TimeChart bar'ları yığmaz, ÖRTER — 6 grupta öndeki çubuk arkadakini
// tamamen gizlerdi; overlay-okuma numarası (total⊃warn⊃err) yalnız
// altküme serilerde çalışır. Hata-oranı ekseni yok — oran seviye
// türevi, servis serilerinden türetilemez.
const GROUP_COLORS = [
  'var(--accent2)', 'var(--purple)', 'var(--teal)', 'var(--orange)', 'var(--ok)',
];
export function collapseGroups(input: Series[]) {
  const empty = {
    times: [] as number[],
    series: [] as TimeChartSeries[],
    totals: { all: 0, error: 0, ratePct: null as number | null },
  };
  if (input.length === 0) return empty;

  const timeSet = new Set<number>();
  for (const s of input) for (const p of s.points) timeSet.add(p.t);
  const tsNs = Array.from(timeSet).sort((a, b) => a - b);
  if (tsNs.length === 0) return empty;
  const idx = new Map(tsNs.map((t, i) => [t, i]));

  // ES backend'i terms-agg artığını sentetik "OTHER" serisi olarak basar
  // (elasticsearch.go:2076, v0.5.396) — binlerce serviste bu çoğu zaman EN
  // BÜYÜK seridir; sıralamaya sokulsa bir servis gibi renk+lejant kapardı.
  // Adıyla ayıklanıp "diğer"e katlanır. CH tarafı da top_groups LIMIT 20
  // ile keser: kesilen servisler HİÇ gelmez — bu yüzden "diğer" etikete
  // sayı YAZMAZ (iki backend'de de tam sayı bilinemez, v0.9.1220 review).
  // v0.9.1250 — cluster/namespace ekseninde CH, attribute'u OLMAYAN
  // satırları da 'OTHER' adıyla gönderir (chLogsGroupOrOther): eleseydi
  // yığın sessizce eksik sayardı, '' gönderseydi lejantta boş çip
  // olurdu. Aynı ad, aynı katlama — iki backend tek kelime dağarcığı.
  const withTotal = (s: Series) =>
    ({ s, total: s.points.reduce((a, p) => a + p.v, 0) });
  const otherSeries = input.filter(s => s.name === 'OTHER').map(withTotal);
  const ranked = input
    .filter(s => s.name !== 'OTHER')
    .map(withTotal)
    .sort((a, b) => b.total - a.total);
  const top = ranked.slice(0, GROUP_COLORS.length);
  const rest = ranked.slice(GROUP_COLORS.length).concat(otherSeries);

  const toData = (list: { s: Series }[]) => {
    const arr = new Array(tsNs.length).fill(0);
    for (const { s } of list) {
      for (const p of s.points) {
        const i = idx.get(p.t);
        if (i !== undefined) arr[i] += p.v;
      }
    }
    return arr;
  };

  const series: TimeChartSeries[] = top.map(({ s }, i) => ({
    key: `g${i}`, label: s.name, data: toData([{ s }]),
    type: 'line' as const, axis: 'left' as const,
    color: GROUP_COLORS[i], width: 1.6,
  }));
  if (rest.length > 0) {
    series.push({
      key: 'rest', label: 'diğer', data: toData(rest),
      type: 'line', axis: 'left',
      color: 'color-mix(in srgb, var(--text3) 55%, transparent)', width: 1.2,
    });
  }

  const all = ranked.reduce((a, r) => a + r.total, 0)
    + otherSeries.reduce((a, r) => a + r.total, 0);
  return {
    times: tsNs.map(t => Math.round(t / 1e9)),
    series,
    totals: { all, error: 0, ratePct: null as number | null },
  };
}

export function collapse(input: Series[], severityFiltered = false) {
  const empty = {
    times: [] as number[],
    series: [] as TimeChartSeries[],
    totals: { all: 0, error: 0, ratePct: null as number | null },
  };
  if (input.length === 0) return empty;

  const timeSet = new Set<number>();
  for (const s of input) for (const p of s.points) timeSet.add(p.t);
  const tsNs = Array.from(timeSet).sort((a, b) => a - b);
  if (tsNs.length === 0) return empty;

  const all = new Array(tsNs.length).fill(0);
  const err = new Array(tsNs.length).fill(0);
  // v0.9.382 (redesign D6) — WARN kendi bandını kazandı: "by level"
  // alt yazısı v0.9.218'den beri yalnız total+error çiziyordu; olay
  // ÖNCESİ yükselen WARN dalgası gri gövdede görünmezdi. Sıfır ek ES
  // sorgusu: seviye serileri zaten geliyor (çip sayaçları v0.9.358).
  const warnErr = new Array(tsNs.length).fill(0);
  const idx = new Map(tsNs.map((t, i) => [t, i]));

  for (const s of input) {
    const band = severityBandOf(s.name);
    const isError = band === 'ERROR';
    const isWarn = band === 'WARN';
    for (const p of s.points) {
      const i = idx.get(p.t);
      if (i === undefined) continue;
      all[i] += p.v;
      if (isError) err[i] += p.v;
      if (isError || isWarn) warnErr[i] += p.v;
    }
  }

  // Rate is null (a gap, not a zero) in buckets with no logs at all —
  // 0/0 is "we don't know", and drawing it as 0% invents a clean window.
  //
  // v0.9.287 — and null for the WHOLE series when a severity floor is
  // active, because then `all` is not the population the rate is about.
  // err/all was being read off a denominator the filter had already
  // narrowed: at ERROR the line pinned to a flat 100% (every remaining
  // log is an error) and the red bars sat exactly on the grey ones; at
  // WARN it silently became error/(warn+error) — a number that looks
  // perfectly reasonable and is not the error rate. Clicking ERROR is
  // this page's most frequent action, so this was the most-seen wrong
  // number on the surface.
  const rate: (number | null)[] = severityFiltered
    ? all.map(() => null)
    : all.map((n, i) => (n > 0 ? (err[i] / n) * 100 : null));

  const sumAll = all.reduce((a, b) => a + b, 0);
  const sumErr = err.reduce((a, b) => a + b, 0);

  // Draw order = overlay order (baseline'dan): tam çubuk, üstüne
  // warn+err (amber), en üste err (kırmızı) — yığılmış OKUNUR, overlay
  // ÇİZİLİR (öndeki daha kısa çubuk arkadakini kısmen örter). Oran
  // çizgisi ayrışsın diye amber'den turuncuya kaydı.
  const series: TimeChartSeries[] = [
    {
      key: 'total', label: 'logs', data: all, type: 'bar', axis: 'left',
      // Deliberately NOT the accent: the loudest colour should mark the
      // rarest, most actionable data, not the background traffic.
      color: 'color-mix(in srgb, var(--text3) 45%, transparent)',
    },
    { key: 'warn', label: 'warn+', data: warnErr, type: 'bar', axis: 'left', color: 'var(--warn)' },
    { key: 'error', label: 'errors', data: err, type: 'bar', axis: 'left', color: 'var(--err)' },
    {
      key: 'rate', label: 'error rate', data: rate, type: 'line', axis: 'right',
      color: 'var(--orange)', width: 1.8, pointsShow: true,
    },
  ];

  return {
    times: tsNs.map(t => Math.round(t / 1e9)), // ns → unix sec
    series,
    totals: {
      all: sumAll,
      error: sumErr,
      // v0.9.287 — same reasoning as the series: under a severity floor
      // this ratio is not the error rate, so it is withheld, not shown.
      ratePct: severityFiltered || sumAll === 0 ? null : (sumErr / sumAll) * 100,
    },
  };
}
