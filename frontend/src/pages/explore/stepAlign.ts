// pages/explore/stepAlign.ts — "hangi harf hangi çözünürlükte?" (v0.9.809)
//
// Explore'un formül satırı (ƒ: A / B) harflerin per-bucket TOPLAMLARINI
// birleştirir ve formulaSeries buckets'ları KESİŞİMLE eşler. Kesişim
// zaman damgalarını hizalar ama ÇÖZÜNÜRLÜĞÜ hizalamaz: A 15 saniyelik
// bucket'larda, B 60 saniyelik bucket'larda geliyorsa A/B "15 saniyede
// sayılan" bir değeri "60 saniyede sayılan" bir değere bölüyor demektir.
// Sayı çıkar, grafik çizilir, ve dörtte bir büyüklüğünde bir orandır.
//
// Bu neden CANLI bir olasılık: ÜÇ okuma yolu step'i AYRI kelepçeliyor —
//   • resolver     → clampMetricStep (10s tabanı + ≤720 nokta bütçesi)
//   • span-metric  → clampSpanMetricStep (nokta bütçesi, farklı tavan)
//   • metric-query → clampStepToExport (metriğin GÖZLENEN export
//                    kadansı; 60 saniyede bir scrape edilen bir metrik
//                    15 saniyelik step istense de 60'ta kalır)
// Frontend üçüne de AYNI effStep'i gönderiyor, yani "aynı step istedim,
// aynı step geldi" varsayımı sessizce yanlış olabiliyor.
//
// Kelepçeleri TEKLEŞTİRMİYORUZ (ayrı bir iş). Burada yalnız GÖRÜNÜRLÜK
// ve DÜRÜST DÜŞÜRME var: her harfin gerçek çözünürlüğünü öğren, uyuşmayan
// harfleri formülden düşür, sebebini yaz.
//
// İki kaynak, bu öncelikle:
//   1. SUNUCUNUN SÖZÜ — /api/metrics/resolve zarfı `stepSeconds` döndürür
//      (chstore.MetricResolveResult). Kelepçeyi uygulayan taraf o.
//   2. ÖLÇÜM — öteki iki yol efektif step'i zarfta söylemiyor (step
//      QueryMetric/QuerySpanMetric içinde beş ayrı dönüş dalında ayrı ayrı
//      hesaplanıyor; dışarı çıkarmak kelepçe birleştirmesinin ta kendisi
//      olurdu). O yüzden GELEN BUCKET IZGARASINDAN ölçüyoruz. Bu bir
//      tahmin değil: sunucunun ÜRETTİĞİ bucket aralığının kendisi.
//
// Saf modül — tablo-testli (stepAlign.test.ts).

import type { SpanMetricSeries } from '@/lib/types';
import { exprRefs } from '@/lib/metricFormula';

// measureStepSec — dönen serilerin bucket ızgarasından efektif step
// (saniye). Ardışık nokta farklarının EN KÜÇÜĞÜ alınır: veri boşluğu olan
// bir seride farklar step'in katlarıdır (2×, 3×…), en küçüğü ise ızgaranın
// kendisidir. 0 = ölçülemedi (tek nokta / seri yok) — çağıran "bilinmiyor"
// diye ele alır, "sıfır" diye değil.
export function measureStepSec(series: SpanMetricSeries[] | undefined): number {
  if (!series?.length) return 0;
  let minDeltaNs = Infinity;
  for (const s of series) {
    const pts = s.points;
    for (let i = 1; i < pts.length; i++) {
      const d = pts[i].time - pts[i - 1].time;
      if (d > 0 && d < minDeltaNs) minDeltaNs = d;
    }
  }
  if (!isFinite(minDeltaNs)) return 0;
  return Math.round(minDeltaNs / 1e9);
}

// resolveStepSec — sunucunun sözü varsa o, yoksa ölçüm. Sunucu 0/eksik
// gönderirse (eski pod, rolling deploy) ölçüme düşer — yani bu kablo
// sürüm karışık bir kümede de sessizce yalan söylemez.
export function resolveStepSec(
  serverStep: number | undefined,
  series: SpanMetricSeries[] | undefined,
): number {
  if (serverStep && serverStep > 0) return serverStep;
  return measureStepSec(series);
}

export interface FormulaStepAlignment {
  // Formül GÜVENLE hesaplanabilir mi? false ise formulaSeries hiç
  // çağrılmaz ve panel notu sebebi söyler.
  aligned: boolean;
  // Referans (çoğunluk) step — hizanın ölçüldüğü taban, saniye.
  baseStepSec: number;
  // Tabana uymayan harfler, alfabetik.
  droppedLetters: string[];
  // Operatöre gösterilecek cümle; aligned iken null.
  note: string | null;
}

// alignFormulaLetters — formülün referans verdiği harflerin çözünürlüğünü
// karşılaştırır.
//
// Taban: EN SIK görülen step (eşitlikte en küçük olan — daha ince ızgara
// daha çok harfin ortak paydası olma şansına sahiptir ve keyfî bir "ilk
// harf" seçimi yapmaktan daha az sürprizlidir). Step'i BİLİNMEYEN harf
// (ölçüm de sunucu da veremedi) düşürülmez: bilmemek, uyuşmamak değildir —
// aksi hâlde tek noktalı bir sorgu formülü sebepsiz kapatırdı.
export function alignFormulaLetters(
  expr: string,
  stepByLetter: Record<string, number | undefined>,
): FormulaStepAlignment {
  const refs = exprRefs(expr);
  const known = refs
    .map(id => ({ id, step: stepByLetter[id] ?? 0 }))
    .filter(x => x.step > 0);
  if (known.length < 2) {
    return { aligned: true, baseStepSec: known[0]?.step ?? 0, droppedLetters: [], note: null };
  }
  const freq = new Map<number, number>();
  for (const k of known) freq.set(k.step, (freq.get(k.step) ?? 0) + 1);
  let baseStepSec = known[0].step;
  for (const [step, n] of freq) {
    const bestN = freq.get(baseStepSec) ?? 0;
    if (n > bestN || (n === bestN && step < baseStepSec)) baseStepSec = step;
  }
  const dropped = known.filter(k => k.step !== baseStepSec).map(k => k.id).sort();
  if (dropped.length === 0) {
    return { aligned: true, baseStepSec, droppedLetters: [], note: null };
  }
  const detail = dropped
    .map(id => `${id} ${fmtStep(stepByLetter[id] ?? 0)}`)
    .join(', ');
  return {
    aligned: false,
    baseStepSec,
    droppedLetters: dropped,
    note: `${detail} — taban ${fmtStep(baseStepSec)}. Farklı çözünürlükteki `
      + `harfler formül dışı: aynı bucket'ta farklı uzunlukta pencereler `
      + `birleştirilirse sonuç sessizce ölçek hatası taşır. Adımı elle sabitle `
      + `(step) ya da harfleri aynı kaynağa taşı.`,
  };
}

// fmtStep — saniyeyi kısa okunur adım metnine çevirir ("15s", "5dk", "1sa").
export function fmtStep(sec: number): string {
  if (!(sec > 0)) return '?';
  if (sec % 3600 === 0) return `${sec / 3600}sa`;
  if (sec % 60 === 0) return `${sec / 60}dk`;
  return `${sec}s`;
}
