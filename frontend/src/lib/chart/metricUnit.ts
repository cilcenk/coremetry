// metricUnit.ts — OTLP (UCUM) birimi → @grafana/data birim kimliği.
//
// TEK TANIM. v0.9.801'e kadar bu harita yalnız Service → Detaylar
// metrik panellerinde (detailsMetricPanels.ts) yaşıyordu; Explore'un
// metrik-kaynaklı sorguları katalog birimini HAM geçiriyordu. 's'/'ms'
// iki tarafta da aynı yazıldığı için o dal kazara çalışıyor, 'By' / '1'
// gibi UCUM-özel birimler Grafana'ya bilinmeyen kimlik olarak iniyordu.
// Harita paylaşılınca "birim nasıl gösterilir" sorusunun tek cevabı olur.
//
// Bilinmeyen ve ANNOTATION birimleri ({connection}, {request}) undefined
// döner: eksen ham sayı çizer. Sessizce "ms" varsaymak yanlış sayıya
// güven üretir (v0.9.774 dürüstlük deseni).
//
// '1' (boyutsuz) BİLİNEN bir birimdir ve karşılığı "birim yok"tur — bu
// yüzden haritada AÇIKÇA duruyor, `default` dalına düşmüyor.
const UNIT_MAP: Record<string, string | undefined> = {
  'By': 'bytes',
  'ms': 'ms',
  's': 's',
  'ns': 'ns',
  'us': 'µs',
  'µs': 'µs',
  '%': 'percent',
  '1': undefined,
  '': undefined,
};

// v0.10.288 (chart audit AS-4 / Dilim 1.5) — süre birimlerinin YAZILI
// biçimleri (Prometheus/receiver katalogları 'seconds', 'milliseconds'
// yayar). routeSeries.ts'in kendi yarım haritası buraya katlandı: iki
// harita aynı seriyi iki sayfada farklı yazabiliyordu. Harf-duyarsız
// (katalog 'Seconds' da yazar); UCUM sembolleri (By, µs) ise duyarlı.
const DURATION_WORDS: Record<string, 's' | 'ms'> = {
  s: 's', sec: 's', secs: 's', second: 's', seconds: 's',
  ms: 'ms', millis: 'ms', millisecond: 'ms', milliseconds: 'ms',
};

export function otlpUnitToGrafana(unit: string | undefined): string | undefined {
  const t = (unit ?? '').trim();
  if (Object.prototype.hasOwnProperty.call(UNIT_MAP, t)) return UNIT_MAP[t];
  const w = t.toLowerCase();
  return Object.prototype.hasOwnProperty.call(DURATION_WORDS, w) ? DURATION_WORDS[w] : undefined;
}

// durationUnitToGrafana — yalnız SÜRE birimleri ('s' | 'ms'); başka her
// şey undefined (KPI karosu ms'ye çevirirken 'bytes'ı süre sanmasın).
// routeSeries.metricUnitToGrafana bunun takma adı.
export function durationUnitToGrafana(unit: string | undefined): 's' | 'ms' | undefined {
  const g = otlpUnitToGrafana(unit);
  return g === 's' || g === 'ms' ? g : undefined;
}
