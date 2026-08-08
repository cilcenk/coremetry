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

export function otlpUnitToGrafana(unit: string | undefined): string | undefined {
  const t = (unit ?? '').trim();
  return Object.prototype.hasOwnProperty.call(UNIT_MAP, t) ? UNIT_MAP[t] : undefined;
}
