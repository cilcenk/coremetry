import { lazy, Suspense, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowUp, ArrowDown } from 'lucide-react';
import { api } from '@/lib/api';
import { LazyMount } from '@/components/LazyMount';
import { Spinner } from '@/components/Spinner';
import { fmtNum } from '@/lib/utils';
import { worseningCount } from '@/lib/endpointHonesty';
import { splitPartialTail, coveredRangeNote, partialBucketNote } from '@/lib/seriesWindow';
import type { EndpointRow, EndpointsSeries, EndpointsSeriesPoint, SpanMetricSeries } from '@/lib/types';

// EndpointsSummary — /endpoints sayfasının üst şeridi (v0.9.819).
//
// Sayfa bugüne dek SADECE tabloydu. Her satır pencerenin TOPLAMINI
// gösteriyordu, yani "ne zaman bozuldu" sorusu ancak bir satırın
// sparkline'ına tıklayıp modalı açarak — endpoint endpoint —
// cevaplanabiliyordu. Filo genelinde "trafik ne zaman düştü", "gecikme ne
// zaman tırmandı", "hata dalgası nerede" soruları hiç cevaplanamıyordu.
//
// TEK ÇAĞRI (/api/endpoints/series) hem şeridi hem üç grafiği besler ve
// pencere KPI'ları sunucuda serinin KENDİ sorgusundan (WITH ROLLUP)
// türer — şerit ile grafiklerin farklı sayı göstermesi imkânsız.
//
// CorePanel LAZY: @grafana/* statik import edilseydi /endpoints'in vendor
// chunk'ı ~35 KB'den ~1 MB'ye çıkardı ve grafikler görünmeden de ödenirdi
// (Overview.tsx:26-29 ölçümü, MessagingSummary emsali).
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// toSeries — nokta dizisi → CorePanel'in beklediği tek seri.
// time unix NANOSANİYE (SpanMetricSeries sözleşmesi; dataFrame köprüsü
// /1e6 ile ms'ye indiriyor).
function toSeries(
  points: EndpointsSeriesPoint[],
  pick: (p: EndpointsSeriesPoint) => number,
  label: string,
): SpanMetricSeries[] {
  return [{ groupKey: [label], points: points.map(p => ({ time: p.timeS * 1e9, value: pick(p) })) }];
}

// KPI karosu — ev dili (.card.ov-kpi + .ov-kpi-accent + .ov-lab/.ov-val),
// Service Overview KpiTile / MessagingSummary MsgKpi ile AYNI token seti.
function EpKpi({ lab, val, unit, accent, note, delta }: {
  lab: string; val: string; unit?: string; accent: string;
  note?: string;
  // delta: yön + metin. YOKSA hiç çizilmez — "değişmedi" ile "ölçülmedi"
  // aynı şey değil (v0.9.818'in "listede yeni" dersi).
  delta?: { pct: number; title: string };
}) {
  return (
    <div className="card ov-kpi" title={note}>
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">{lab}</div>
      <div className="ov-val">{val}{unit && <span className="ov-unit">{unit}</span>}</div>
      {delta && (
        <div className={`ov-delta ${delta.pct > 0 ? 'up' : delta.pct < 0 ? 'down' : 'flat'}`}
          title={delta.title}>
          {delta.pct > 0 ? <ArrowUp size={11} strokeWidth={2.5} />
            : delta.pct < 0 ? <ArrowDown size={11} strokeWidth={2.5} /> : null}
          {Math.abs(delta.pct) < 10 ? Math.abs(delta.pct).toFixed(1) : Math.abs(delta.pct).toFixed(0)}%
        </div>
      )}
    </div>
  );
}

// fmtRate — sub-10 oranlar bir ondalık tutar ki damlayan bir endpoint
// "0" diye okunmasın (tablodaki fmtRate ile aynı kural).
function fmtRate(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return v < 10 ? v.toFixed(1) : Math.round(v).toLocaleString();
}

// fmtMsVal — "—" ölçüm YOKLUĞU için; 0 ms bir ölçüm değildir.
function fmtMsVal(v?: number): string {
  if (v === undefined || v === null || !(v > 0)) return '—';
  return v < 10 ? v.toFixed(1) : Math.round(v).toLocaleString();
}

export function EndpointsSummary({
  fromNs, toNs, service, search, entry, env, cluster, compare, rows, limit,
  onZoom, onZoomReset,
}: {
  fromNs: number;
  toNs: number;
  /** Tablonun kendi filtreleri — şerit tabloyla aynı kümeyi anlatır. */
  service: string;
  search: string;
  entry: 'http' | 'rpc';
  /** Cevaplanabilirlik kapısı: MV'de bu boyutlar yok (unsupportedScope). */
  env: string;
  cluster: string;
  /** "Filo p95" karosunun Δ'sı için önceki eş pencere okunsun mu? */
  compare: boolean;
  /** Tablo satırları — "Listelenen" ve "Kötüleşen" karoları BUNLARDAN. */
  rows: EndpointRow[];
  limit: number;
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
}) {
  const q = useQuery({
    queryKey: ['endpoints-series', fromNs, toNs, service, search, entry, env, cluster, compare],
    queryFn: () => api.endpointsSeries({
      from: fromNs, to: toNs,
      service: service || undefined,
      search: search || undefined,
      entry: entry === 'rpc' ? 'rpc' : undefined,
      env: env || undefined,
      cluster: cluster || undefined,
      compare: compare ? 'prior' : undefined,
    }),
    // Sunucu TTL'i 30 sn — altına inmek sıcak slotu ıskalayıp CH'ye
    // gereksiz tur attırırdı (ES-maliyet disiplininin CH ikizi).
    staleTime: 30_000,
  });

  const data = q.data as EndpointsSeries | undefined;
  const points = useMemo(() => data?.points ?? [], [data]);
  const xRange = useMemo(() => ({ from: fromNs / 1e9, to: toNs / 1e9 }), [fromNs, toNs]);
  // Kötüleşen sayısı İSTEMCİDE, tablodaki P99 Δ kolonuyla AYNI tanımdan
  // (worseningCount → endpointP99Delta): karonun sayısı ile ▲ işaretli
  // satır sayısı ayrışamaz.
  const worsening = useMemo(() => worseningCount(rows), [rows]);

  // Kapsam notu — grafikler neyi çiziyor? Filtre varsa SÖYLENMELİ,
  // yoksa daraltılmış bir grafik filo geneliymiş gibi okunur.
  const scope = useMemo(() => {
    const bits: string[] = [entry === 'rpc' ? 'RPC & Messaging girişleri' : 'HTTP girişleri'];
    if (service) bits.push(service);
    if (search) bits.push(`"${search}" araması`);
    return bits.join(' · ');
  }, [entry, service, search]);

  const unsupported = data?.unsupportedScope;
  const loading = q.isPending;
  const error = q.isError ? 'Endpoint seri sorgusu başarısız' : undefined;
  const empty = !loading && !error && points.length === 0;

  // Cevaplanamaz kapsam AYRI bir boş-hâl: "veri yok" DEĞİL, "bu soru bu
  // kaynaktan sorulamaz". İkisini aynı mesaja indirmek, sayfanın
  // v0.9.313'te temizlediği sessiz-boş sınıfının geri gelmesi olurdu.
  const emptyReason = unsupported
    ? `${unsupported} filtresiyle çizilemez`
    : empty ? 'Bu pencerede giriş span\'i yok' : undefined;
  const emptyHint = unsupported
    ? `Tablo ${unsupported} filtresinde ham span'lere düşüyor; grafikleri besleyen spanmetrics rollup'ında bu boyut yok. Filtresiz bir filo grafiğini filtreli bir tablonun üstüne koymamak için grafik çizilmiyor. ${scope}.`
    : empty ? `Kapsam: ${scope}.` : undefined;

  // Zarf notları — ikisi de yalnız gerçekten geçerliyken çıkar.
  const covNote = useMemo(() => coveredRangeNote(data, fromNs), [data, fromNs]);
  const partNote = useMemo(() => partialBucketNote(data), [data]);
  const noteLine = [`Kapsam: ${scope}.`, covNote, partNote,
    data?.sourceMV ? `Kaynak: ${data.sourceMV}.` : null]
    .filter(Boolean).join(' ');

  // DOLMAKTA olan son kova KESİKLİ çizilir; nokta atılmaz.
  const split = useMemo(
    () => splitPartialTail(points, data?.partialLastBucket), [points, data]);

  // Filo p95 Δ — prior YOKSA hiç çizilmez.
  const p95Delta = useMemo(() => {
    const prior = data?.priorP95Ms ?? 0;
    const cur = data?.p95Ms ?? 0;
    if (!(prior > 0) || !(cur > 0)) return undefined;
    return {
      pct: ((cur - prior) / prior) * 100,
      title: `Önceki eşit pencere p95: ${prior.toFixed(0)} ms. Filo geneli değil — ${scope}.`,
    };
  }, [data, scope]);

  // '-ms' soneki MOTOR AD ALANI (v0.9.789): CorePanel x'i milisaniye
  // tutar, v1 uPlot gövdesi saniye — karışık grup crosshair'i 1000×
  // yanlış yere koyar. /endpoints'te v1 grafikler var (modal), o yüzden
  // sonek burada gerçek bir koruma.
  const syncKey = 'endpoints-ms';
  const panelCommon = {
    height: 190,
    xRange,
    loading,
    error,
    emptyReason,
    emptyHint,
    syncKey,
    note: noteLine,
    // Sürükle-zoom sayfanın range'ini yazar, çift-tık bir adım geri —
    // usePageZoomRange'in tek sahipli sözleşmesi (v0.9.429). Tablo ve
    // grafikler aynı pencereden besleniyor, yani zoom ikisini birden
    // daraltır; grafiği ayrı bir pencereye kaydırmak iki gerçek üretirdi.
    onZoom,
    onZoomReset,
  };

  // dashedTail — kesikli kuyruk item'ı; kuyruk yoksa hiç eklenmez.
  const tailItem = (pick: (p: EndpointsSeriesPoint) => number, name: string, role: 'data' | 'error') =>
    (split.tail.length > 0
      ? [{ name: `${name} (dolmakta)`, role, dashed: true, series: toSeries(split.tail, pick, `${name} (dolmakta)`) }]
      : []);

  return (
    <>
      <div className="ov-grid ov-kpis-4 ov-mb">
        <EpKpi lab="Listelenen endpoint" accent="var(--accent)"
          val={fmtNum(rows.length)}
          note={`Sunucudan dönen satır sayısı — filo geneli DEĞİL. Bu sayıyı limit (top ${fmtNum(limit)}), servis/yol filtresi ve seçili sekme belirler.`} />
        <EpKpi lab="İstek" accent="var(--accent2)"
          val={unsupported ? '—' : fmtRate(data?.callsPerMin ?? 0)} unit=" /dk"
          note={unsupported
            ? `${unsupported} filtresinde bu ölçüm spanmetrics rollup'ından okunamıyor.`
            : `Pencere boyunca dakikaya normalize edilmiş giriş span'i. Bu karo TABLODAN BAĞIMSIZ: seri, limit'e girmeyen endpoint'leri de sayar — yani ${scope} kapsamının TAMAMI.`} />
        <EpKpi lab="Filo p95" accent="var(--warn)"
          val={unsupported ? '—' : fmtMsVal(data?.p95Ms)} unit=" ms"
          delta={p95Delta}
          note={unsupported
            ? `${unsupported} filtresinde bu ölçüm spanmetrics rollup'ından okunamıyor.`
            : `Pencerenin TAM quantile'ı (tüm kovaların TDigest merge'ü). Kova p95'lerinin ortalaması DEĞİL — o sistematik olarak iyimser olurdu. Kapsam: ${scope}.`} />
        <EpKpi lab="Kötüleşen" accent="var(--err)"
          val={compare ? fmtNum(worsening) : '—'}
          unit={compare ? ` / ${fmtNum(rows.length)}` : undefined}
          note={compare
            ? `Listedeki ${fmtNum(rows.length)} satırdan ${fmtNum(worsening)} tanesinin P99'u önceki eşit pencereye göre %5'ten fazla arttı. Tablodaki P99 Δ kolonuyla AYNI tanım. Önceki pencerenin listesinde olmayan satırlar SAYILMAZ — tabanı olmayan bir satır kötüleşmiş olamaz, sadece bilinmiyordur.`
            : 'Karşılaştırma kapalı. "Compare vs prior" ile önceki eşit pencereye göre kötüleşen satır sayısı gelir.'} />
      </div>

      <div className="ov-grid ov-charts-3 ov-mb">
        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              title="İstek hızı"
              storageKey="endpoints-rps"
              unit="reqps"
              items={[
                { name: 'İstek', role: 'data' as const, series: toSeries(split.solid, p => p.rps, 'İstek') },
                ...tailItem(p => p.rps, 'İstek', 'data'),
              ]} />
          </Suspense>
        </LazyMount>

        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              // BAŞLIK NE ÇİZDİĞİNİ SÖYLER (v0.9.774 dersi): bunlar
              // KOVA quantile'ları, yani şeritteki "Filo p95" karosuyla
              // aynı sayı DEĞİL (o pencerenin tam merge'ü). İkisi de
              // doğru, farklı sorulara cevap veriyor.
              title="Giriş gecikmesi · kova p50 / p95"
              storageKey="endpoints-latency"
              unit="ms"
              items={[
                { name: 'p50', role: 'data' as const, series: toSeries(split.solid, p => p.p50Ms, 'p50') },
                { name: 'p95', role: 'data' as const, series: toSeries(split.solid, p => p.p95Ms, 'p95') },
                ...tailItem(p => p.p95Ms, 'p95', 'data'),
              ]} />
          </Suspense>
        </LazyMount>

        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              title="Hata oranı"
              storageKey="endpoints-error-rate"
              unit="percent"
              items={[
                { name: 'Hata oranı', role: 'error' as const, series: toSeries(split.solid, p => p.errorRate, 'Hata oranı') },
                ...tailItem(p => p.errorRate, 'Hata oranı', 'error'),
              ]} />
          </Suspense>
        </LazyMount>
      </div>
    </>
  );
}
