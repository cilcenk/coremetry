import { lazy, Suspense } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Spinner } from '@/components/Spinner';
import type { CorePanelMultiItem } from '@/components/chart/corePanelEntry';
import { cosreChartDSL, cosreChartItems, COSRE_SERIES_CAP } from './cosreChartSpec';

// CosreChart — sohbete gömülen CANLI grafik (```chart``` çiti).
//
// v0.9.1186 (AI Faz 4.4) — iki değişiklik, biri diğerini gerektiriyor:
//
//   1. SPEC genişledi: `groupBy` (tek anahtar kırılım) + mutlak pencere
//      (fromNs/toNs). Kırılım N seri demek.
//   2. MOTOR değişti: ChartCard (tek çizgi) → CorePanel. Kırılım tek-çizgi
//      bir kartta çizilemezdi; ve geçişle birlikte zoom, lejant, imleç
//      senkronu ve exemplar altyapısı BEDAVA geldi — uygulamanın geri
//      kalanı zaten o motorda (v0.9.743+).
//
// Lazy import zorunlu: CorePanel @grafana/data'ya bağlı ve statik bağlamak
// vendor'ı 35 KB'dan 1 MB'a çıkarıyor (corePanelEntry.tsx'in ölçümü).
// Sohbet balonu her sayfada mount olabildiği için burada bedeli ödemek
// bütün uygulamayı şişirirdi.
const CorePanelMulti = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));

export type { CosreChartSpec } from './cosreChartSpec';
import type { CosreChartSpec } from './cosreChartSpec';

export function CosreChart({ spec }: { spec: CosreChartSpec }) {
  const rangeS = spec.rangeS && spec.rangeS > 0 ? spec.rangeS : 1800;
  // Mutlak pencere doluysa RangeS'i EZER. Guided/insight yolları olayın
  // penceresini zaten biliyor; "son 30dk" onların cevabını kaydırırdı.
  const absolute = !!(spec.fromNs && spec.toNs && spec.toNs > spec.fromNs);
  const groupBy = spec.groupBy ?? '';

  const q = useQuery({
    // Anahtar HER girdiyi taşır (v0.5.187 sınıfı): kırılım ya da mutlak
    // pencere anahtara girmezse iki farklı kart aynı cache satırını okur.
    queryKey: ['cosre-chart', spec.service, spec.operation ?? '', spec.agg,
      rangeS, groupBy, absolute ? spec.fromNs : 0, absolute ? spec.toNs : 0],
    queryFn: () => {
      const to = absolute ? spec.toNs! : Date.now() * 1e6;
      const from = absolute ? spec.fromNs! : to - rangeS * 1e9;
      return api.spanMetricBatch({
        from, to,
        dsl: cosreChartDSL(spec),
        ...(groupBy ? { groupBy: [groupBy] } : {}),
        // v0.9.391 — sohbet içi ~560px kart; sabit küçük bütçe yeterli.
        maxDataPoints: 300,
        aggs: [{ name: 'v', agg: spec.agg, field: AGG_FIELD[spec.agg] }],
      });
    },
    select: d => d.series,
    enabled: !!spec.service,
    staleTime: 30_000,
  });

  const { items, unit, truncated, total } = cosreChartItems(spec, q.data?.v ?? []);

  return (
    <div style={{ margin: '10px 0', maxWidth: 560 }}>
      <Suspense fallback={<Spinner />}>
        <CorePanelMulti
          // v0.10.43 — BAŞLIK SPEC'TEN DEĞİL, agg'DEN. Sunucunun
          // render_chart aracı spec'i tam üç anahtarla kuruyor
          // (service, agg, rangeS) — title'ı HİÇ üretmiyor. Yani
          // spec.title'a saygı duymak yalnız MODELİN kendi yazdığı çiti
          // onurlandırırdı; meşru grafik zaten defaultTitle'a düşüyordu,
          // dolayısıyla bu değişiklik hiçbir gerçek grafiği etkilemiyor.
          title={defaultTitle(spec)}
          height={180}
          // storageKey lejant katlanma durumunun kimliği. Spec'in
          // KAPSAMINDAN türetiliyor, sohbet turundan değil: aynı grafiği
          // ikinci kez soran operatör lejantı yeniden katlamak zorunda
          // kalmasın, farklı bir kırılım ise kendi durumunu taşısın.
          storageKey={`cosre-chart:${spec.service}:${spec.operation ?? ''}:${spec.agg}:${groupBy}`}
          loading={q.isLoading}
          error={q.isError ? 'Grafik verisi alınamadı' : undefined}
          items={items}
          // ⚠ BİRİM MODEL KONTROLÜNDE OLAMAZ. Eskiden spec.unit,
          // agg'den türetilen birimi EZİYORDU: model bir p99 grafiğine
          // "%" yazabiliyor ve grafik GERÇEK gecikme verisiyle
          // çiziliyordu. Doğru veri + yanlış birim, düzyazıdan daha ikna
          // edici bir hata — grafik daha yüksek güven taşır.
          // Birim artık YALNIZ AGG_UNIT[agg]'den.
          unit={unit}
        />
      </Suspense>
      {/* Kırpma İLAN EDİLİR. Sessizce ilk N'i çizmek, operatöre "evren bu"
          dedirtir — kırılımın amacı tam da hangi değerin farklı olduğunu
          görmekken. (Aynı dürüstlük kuralı: RowsCapped, v0.9.809.) */}
      {truncated && (
        <div style={{ fontSize: 10, color: 'var(--text3)', marginTop: 2 }}>
          {total} seriden ilk {COSRE_SERIES_CAP}'i çizildi (en yüksek değerliler)
        </div>
      )}
    </div>
  );
}

// AGG_FIELD — gecikme yüzdelikleri bir ALAN üstünde hesaplanır; sayım
// sınıfı aggler alansız. cosreChartSpec.ts'teki AGG_META'nın alan yarısı.
const AGG_FIELD: Record<string, string | undefined> = {
  rate: undefined, error_rate: undefined,
  p50: 'duration_ms', p95: 'duration_ms', p99: 'duration_ms',
};

function defaultTitle(spec: CosreChartSpec): string {
  const base = spec.operation || spec.service;
  return spec.groupBy ? `${base} · ${spec.agg} · ${spec.groupBy}` : `${base} · ${spec.agg}`;
}

export type { CorePanelMultiItem };
