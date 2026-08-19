// cosreChartSpec — sohbete gömülen ```chart``` çitinin SAF yarısı
// (v0.9.1186, AI Faz 4.4). Testi cosreChartSpec.test.ts.
//
// Ayrı dosya, çünkü buradaki üç karar da tamamen saf ve tamamen kenar
// durumdan ibaret: DSL'in kaçışı, kırılımlı yanıtın seri→item eşlemesi ve
// seri tavanı. React içine gömülü kalsalardı hiçbiri koşturularak
// denenemezdi — oysa DSL kaçışı yanlışsa sorgu sessizce servis-geneline
// düşer (v0.9.187'nin dersi) ve tavan yanlışsa operatör eksik bir kırılıma
// "evren bu" diye bakar.

import type { SpanMetricSeries } from '@/lib/types';
import type { CorePanelMultiItem } from '@/components/chart/corePanelEntry';

export interface CosreChartSpec {
  title?: string;
  service: string;
  // operation (v0.9.184) — verilirse grafik tek span-name'e daralır
  // (DSL: name = "..."). Boşsa servis-geneli.
  operation?: string;
  agg: 'rate' | 'error_rate' | 'p50' | 'p95' | 'p99';
  unit?: string;
  rangeS?: number; // pencere (saniye); default 1800 (30 dk)
  // v0.9.1186 — TEK anahtar kırılım: her farklı değer için bir çizgi.
  // Tek, çünkü sohbet balonundaki ~560px kartta iki anahtarlı kırılım
  // seri sayısını çarpar ve kırılım tam da okunabilirlik için var.
  groupBy?: string;
  // Mutlak pencere (unix ns). Doluysa rangeS'i EZER — guided/insight
  // yolları olayın penceresini zaten biliyor, "son 30dk" cevabı kaydırırdı.
  fromNs?: number;
  toNs?: number;
}

// COSRE_SERIES_CAP — kırılımda çizilecek en fazla seri.
//
// 8: sohbet balonundaki lejant bundan sonrası için kartı yükseltmeye
// başlıyor ve 20 çizgili bir spagetti, kırılımsız tek çizgiden DAHA az
// bilgi taşıyor. Tavan ısırdığında SÖYLENİR (bkz. CosreChart) — sessizce
// ilk N'i çizmek operatöre "evren bu" dedirtirdi.
export const COSRE_SERIES_CAP = 8;

const AGG_UNIT: Record<CosreChartSpec['agg'], string> = {
  rate: 'req/s', error_rate: '%', p50: 'ms', p95: 'ms', p99: 'ms',
};

/** dslQuote — DSL string literali için kaçış. */
function dslQuote(v: string): string {
  return v.replace(/"/g, '\\"');
}

/**
 * cosreChartDSL — spec'ten span seçim DSL'i.
 *
 * operation yalnız TEMİZ bir string ise eklenir (v0.9.187): ParseDSL
 * satırları \n ile böldüğü için kontrol karakteri taşıyan bir değer
 * sorguyu bozar. Bozuksa servis-geneli çizeriz — yanlış bir daraltmayla
 * boş grafik göstermektense geniş ama doğru bir grafik.
 *
 * Alan `name`, http.route DEĞİL (dsl_test.go:61 ile doğrulanmıştı).
 */
export function cosreChartDSL(spec: CosreChartSpec): string {
  let dsl = `service.name = "${dslQuote(spec.service)}"`;
  const op = spec.operation;
  const clean =
    typeof op === 'string' && op !== '' &&
    ![...op].some(c => c.charCodeAt(0) < 32);
  if (clean) {
    dsl += ` AND name = "${dslQuote(op as string)}"`;
  }
  return dsl;
}

/**
 * cosreChartItems — spanMetricBatch yanıtı → CorePanel item'ları.
 *
 * Kırılımsız yanıt tek seri taşır; kırılımlı yanıt her değer için bir
 * seri. Sıralama BÜYÜKLÜĞE göre (en yüksek toplam önce), çünkü tavan
 * ısırdığında kesilenler EN AZ ilgi çekenler olmalı — alfabetik kesmek
 * "z" ile başlayan patlayan endpoint'i düşürürdü.
 */
export function cosreChartItems(
  spec: CosreChartSpec,
  series: SpanMetricSeries[] | null | undefined,
): { items: CorePanelMultiItem[]; unit: string; truncated: boolean; total: number } {
  const unit = AGG_UNIT[spec.agg] ?? '';
  const all = series ?? [];
  if (all.length === 0) {
    return { items: [], unit, truncated: false, total: 0 };
  }
  const withMag = all.map(s => ({
    s,
    // Toplam büyüklük: null noktalar atlanır (oran serilerinde istek
    // olmayan kovalar boş gelir; onları 0 saymak seriyi haksız yere aşağı
    // çekerdi).
    mag: (s.points ?? []).reduce((acc, p) => acc + (typeof p?.value === 'number' ? p.value : 0), 0),
  }));
  withMag.sort((a, b) => b.mag - a.mag);
  const total = withMag.length;
  const kept = withMag.slice(0, COSRE_SERIES_CAP);
  return {
    items: kept.map(({ s }, i) => ({
      series: [s],
      name: seriesLabel(spec, s, i, total),
      // error_rate tek seriyken hata rengini hak eder; kırılımda rol
      // vermeyiz — 8 kırmızı çizgi rolün taşıdığı anlamı yok eder.
      role: !spec.groupBy && spec.agg === 'error_rate' ? 'error' : 'data',
    })),
    unit,
    truncated: total > COSRE_SERIES_CAP,
    total,
  };
}

/**
 * seriesLabel — lejant adı.
 *
 * Kırılımsızken agg'ın kendisi (tek çizgi, "rate" yeter). Kırılımda
 * grup değeri; boş değer "(boş)" olur — Prometheus/CH boş etiketi gerçek
 * bir kova ve adsız bir çizgi lejantta okunamaz.
 */
function seriesLabel(spec: CosreChartSpec, s: SpanMetricSeries, i: number, total: number): string {
  if (!spec.groupBy) return spec.agg;
  const key = (s.groupKey ?? []).filter(Boolean).join(' · ').trim();
  if (key) return key;
  // groupKey boş geldiyse (sunucu kırılımı uygulayamadı) indeksle ayır:
  // aynı adlı iki çizgi lejantta birbirinin üstüne biner.
  return total > 1 ? `(boş) ${i + 1}` : '(boş)';
}
