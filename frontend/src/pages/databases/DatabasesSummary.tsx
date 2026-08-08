import { lazy, Suspense, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowUp, ArrowDown } from 'lucide-react';
import { api } from '@/lib/api';
import { LazyMount } from '@/components/LazyMount';
import { Spinner } from '@/components/Spinner';
import { foldTopN } from '@/lib/chart/foldTopN';
import { splitPartialTail, coveredRangeNote, partialBucketNote } from '@/lib/seriesWindow';
import type { DatabasesSeries, DBSeriesPoint, SpanMetricSeries } from '@/lib/types';

// DatabasesSummary — /databases sayfasının üst şeridi (v0.9.820).
//
// Sayfa iki tablo + bir ifade listesiydi: her satır pencerenin TOPLAMINI
// gösteriyordu, yani "veritabanı ne zaman yavaşladı" sorusu ancak bir
// satır açılıp drawer'a bakılarak — instance instance — cevaplanıyordu.
// Filo genelinde "hangi motor ne zaman tırmandı", "hata dalgası nerede"
// soruları hiç cevaplanamıyordu.
//
// TEK ÇAĞRI (/api/databases/series) hem şeridi hem üç grafiği besler;
// pencere KPI'ları sunucuda serinin KENDİ sorgusundan (iki seviyeli WITH
// ROLLUP) türer — şerit ile grafiklerin farklı sayı göstermesi imkânsız.
//
// CorePanel LAZY: @grafana/* statik import edilseydi /databases'in vendor
// chunk'ı ~35 KB'den ~1 MB'ye çıkardı (Overview.tsx:26-29 ölçümü).
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

// toSeries — kova dizisi → CorePanel'in beklediği tek seri.
// time unix NANOSANİYE (SpanMetricSeries sözleşmesi).
function toSeries<T extends { timeS: number }>(
  points: T[], pick: (p: T) => number, label: string,
): SpanMetricSeries[] {
  return [{
    groupKey: [label],
    points: points.map(p => ({ time: p.timeS * 1e9, value: pick(p) })),
  }];
}

// KPI karosu — ev dili (.card.ov-kpi), Service Overview KpiTile /
// MessagingSummary MsgKpi / EndpointsSummary EpKpi ile AYNI token seti.
function DbKpi({ lab, val, unit, accent, note, delta }: {
  lab: string; val: string; unit?: string; accent: string; note?: string;
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

function fmtRate(v: number): string {
  if (!Number.isFinite(v)) return '—';
  return v < 10 ? v.toFixed(1) : Math.round(v).toLocaleString();
}

// '—' ölçüm YOKLUĞU için; 0 ms bir ölçüm değildir (v0.9.262 dersi).
function fmtMsVal(v?: number): string {
  if (v === undefined || v === null || !(v > 0)) return '—';
  return v < 10 ? v.toFixed(1) : Math.round(v).toLocaleString();
}

export function DatabasesSummary({
  fromNs, toNs, dbsys, dbname, compare, extraKpi,
}: {
  fromNs: number;
  toNs: number;
  /** Sayfanın kendi filtreleri — şerit tabloyla aynı kümeyi anlatır. */
  dbsys: string;
  dbname: string;
  compare: boolean;
  /**
   * KOŞULLU ek karo (v0.9.822 havuz doygunluğu). Verilmezse şerit üç
   * karo kalır — boş bir yer tutucu çizilmez, çünkü "ölçüm yok" ile
   * "ölçüm sıfır" aynı şey değil.
   */
  extraKpi?: React.ReactNode;
}) {
  const q = useQuery({
    queryKey: ['databases-series', fromNs, toNs, dbsys, dbname, compare],
    queryFn: () => api.databasesSeries({
      from: fromNs, to: toNs,
      dbsys: dbsys || undefined,
      dbname: dbname || undefined,
      compare: compare ? 'prior' : undefined,
    }),
    // Sunucu TTL'i 30 sn — altına inmek sıcak slotu ıskalayıp CH'ye
    // gereksiz tur attırırdı.
    staleTime: 30_000,
  });

  const data = q.data as DatabasesSeries | undefined;
  const points = useMemo(() => data?.points ?? [], [data]);
  const xRange = useMemo(() => ({ from: fromNs / 1e9, to: toNs / 1e9 }), [fromNs, toNs]);

  const scope = useMemo(() => {
    const bits: string[] = [];
    if (dbsys) bits.push(dbsys);
    if (dbname) bits.push(`db.name = ${dbname}`);
    return bits.length ? bits.join(' · ') : 'tüm veritabanı trafiği';
  }, [dbsys, dbname]);

  const loading = q.isPending;
  const error = q.isError ? 'Veritabanı seri sorgusu başarısız' : undefined;
  const empty = !loading && !error && points.length === 0;
  const emptyReason = empty ? 'Bu pencerede veritabanı çağrısı yok' : undefined;
  const emptyHint = empty ? `Kapsam: ${scope}.` : undefined;

  const covNote = useMemo(() => coveredRangeNote(data, fromNs), [data, fromNs]);
  const partNote = useMemo(() => partialBucketNote(data), [data]);
  const noteLine = [`Kapsam: ${scope}.`, covNote, partNote].filter(Boolean).join(' ');

  // DOLMAKTA olan son kova KESİKLİ çizilir; nokta atılmaz.
  const split = useMemo(
    () => splitPartialTail<DBSeriesPoint>(points, data?.partialLastBucket), [points, data]);

  // Motor kırılımı — foldTopN uzun kuyruğu "others"a katlıyor (SUNUCUDA
  // kesilseydi katlanan pay GÖRÜNMEZ olurdu). Birim 'reqps' toplanabilir,
  // yani kuyruk TOPLANIR (foldTopN'in oran-birimi ortalaması devreye
  // girmez) — hacim için doğru olan bu.
  const engineItems = useMemo(() => {
    const raw: SpanMetricSeries[] = (data?.engines ?? []).map(e => ({
      groupKey: [e.system],
      points: e.points.map(p => ({ time: p.timeS * 1e9, value: p.queriesPerMin })),
    }));
    return foldTopN(raw, 'reqps').map(s => ({
      name: s.groupKey[0],
      role: (s.groupKey[0] === 'others' ? 'muted' : 'data') as 'muted' | 'data',
      series: [s],
    }));
  }, [data]);

  const p95Delta = useMemo(() => {
    const prior = data?.priorP95Ms ?? 0;
    const cur = data?.p95Ms ?? 0;
    if (!(prior > 0) || !(cur > 0)) return undefined;
    return {
      pct: ((cur - prior) / prior) * 100,
      title: `Önceki eşit pencere p95: ${prior.toFixed(0)} ms. Kapsam: ${scope}.`,
    };
  }, [data, scope]);

  // '-ms' soneki MOTOR AD ALANI (v0.9.789) — v1 gövdesi x'i saniye,
  // CorePanel milisaniye tutar; karışık grup imleci 1000× kaydırır.
  const syncKey = 'databases-ms';
  const panelCommon = {
    height: 190,
    xRange,
    loading,
    error,
    emptyReason,
    emptyHint,
    syncKey,
    note: noteLine,
  };

  const tailItem = (pick: (p: DBSeriesPoint) => number, name: string, role: 'data' | 'error') =>
    (split.tail.length > 0
      ? [{ name: `${name} (dolmakta)`, role, dashed: true, series: toSeries(split.tail, pick, `${name} (dolmakta)`) }]
      : []);

  return (
    <>
      <div className={`ov-grid ${extraKpi ? 'ov-kpis-4' : 'ov-kpis'} ov-mb`}>
        <DbKpi lab="Sorgu" accent="var(--accent)"
          val={fmtRate(data?.queriesPerMin ?? 0)} unit=" /dk"
          note={`Pencere boyunca dakikaya normalize edilmiş db çağrısı. Kapsam: ${scope}. Tablodaki satır sayısıyla ilgisi yok — bu, kapsamın TAMAMI.`} />
        <DbKpi lab="Filo p95" accent="var(--warn)"
          val={fmtMsVal(data?.p95Ms)} unit=" ms"
          delta={p95Delta}
          note={`Pencerenin TAM quantile'ı (tüm kovaların ve tüm motorların TDigest merge'ü). Motor p95'lerinin ortalaması DEĞİL — motorlar arası fark 10 katı bulabiliyor, ortalama da maksimum da yanlış cevap verirdi. Kapsam: ${scope}.`} />
        <DbKpi lab="Hata oranı" accent="var(--err)"
          val={`${(data?.errorRate ?? 0).toFixed(2)}%`}
          note={`Pencere TOPLAMLARINDAN: ${(data?.errors ?? 0).toLocaleString()} hatalı / ${(data?.queries ?? 0).toLocaleString()} db çağrısı. Kova oranlarının ortalaması DEĞİL. Kapsam: ${scope}.`} />
        {extraKpi}
      </div>

      <div className="ov-grid ov-charts-3 ov-mb">
        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              title="Sorgu hacmi · motor kırılımı"
              storageKey="db-volume"
              unit="reqps"
              items={engineItems.length > 0 ? engineItems : [
                { name: 'Sorgu', role: 'data' as const, series: toSeries(split.solid, p => p.queriesPerMin, 'Sorgu') },
              ]} />
          </Suspense>
        </LazyMount>

        <LazyMount minHeight={220}>
          <Suspense fallback={<div style={{ height: 220, display: 'grid', placeItems: 'center' }}><Spinner /></div>}>
            <CorePanelMultiLazy
              {...panelCommon}
              // BAŞLIK NE ÇİZDİĞİNİ SÖYLER (v0.9.774 dersi): bunlar KOVA
              // quantile'ları, şeritteki "Filo p95" karosuyla aynı sayı
              // DEĞİL (o pencerenin tam merge'ü). İkisi de doğru, farklı
              // sorulara cevap veriyor.
              title="Sorgu gecikmesi · kova p50 / p95"
              storageKey="db-latency"
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
              storageKey="db-error-rate"
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
