// slowTracesHref — v0.9.961 (UX denetimi G4 / Ö7). Gecikme alarmından
// YAVAŞ trace'e köprü.
//
// ─── Hangi çıkmazı kapatıyor ─────────────────────────────────────────
// Problem detayının tek trace pivotu `hasError=true` sabitliydi. Bir
// p99-eşik alarmında yavaşlık çoğu zaman HATASIZ span'lerdedir: operatör
// "Error traces"a tıklıyor, boş ya da alakasız bir liste görüyor ve
// yavaş trace'i bulmak için servis → endpoint → traces turuna çıkıyor
// (3+ tık). Alarmın sorduğu soru "ne yavaşladı", verilen cevap "ne hata
// verdi"ydi.
//
// ─── BİRİM: ad kanıtlamıyorsa link YOK (v0.6.36'nın dersi) ──────────
// Eşik `problem.threshold` alanında METRİĞİN kendi biriminde duruyor;
// /traces ise `minMs`i MİLİSANİYE okur. İkisini birim kanıtı olmadan
// birbirine bağlamak bu deponun en pahalı hata sınıfı (değer+birim
// şablonları: v0.6.36 toDate/INTERVAL, retention_test.go).
//
// Bu yüzden kapı ADIN kendisinde: yalnız `_ms` ile BİTEN metrikler
// gecikme sayılır. CANLI DOĞRULAMA (2026-08-11, lokal): evaluator'ın
// ürettiği gecikme metrikleri `http_p99_ms`, `db_p99_ms`,
// `mq_consume_p99_ms` ve problem gövdesi eşiği "threshold > 3000.00ms"
// diye yazıyor — yani `_ms` eki birimin BEYANI, tahmini değil.
// `latency`/`duration` gibi birimi söylemeyen adlar BİLEREK dışarıda:
// saniye cinsinden bir eşiği milisaniye sanmak listeyi sessizce
// 1000× yanlış kıstırırdı.

// ─── v0.9.1331 — BU DOSYA KENDİ DERSİNİ İHLAL EDİYORDU ───────────────
// Yukarıdaki şerh birim disiplini üzerine bir manifesto, ama imza
// `window: { fromNs, toNs }` diyip değeri `custom:` yuvasına DÖNÜŞTÜRMEDEN
// basıyordu. `custom:` ise milisaniye (`lib/urlState.ts:8` — açıkça
// `custom:<fromMs>-<toMs>`). Yani ad nanosaniye vaat ediyor, kod
// milisaniye bekliyordu.
//
// Çalışma zamanı BUGÜN doğruydu: tek çağıran zaten bölünmüş `logsFrom/
// logsTo`yu geçiyordu. Yalan yalnız adlardaydı — ve testi (`:52`,
// `{fromNs: 111}`) o yalanı ÇİVİLİYORDU, yani doğru bir yeniden yazım
// kırmızı verip regresyon gibi görünürdü.
//
// Tehlike ölçülebilir: doğal olanı yapan (`probWindow`'u doğrudan geçen)
// biri pencereyi ~55 milyon yıl ileriye atardı ve /traces boş dönerdi —
// "yavaş trace yok" diye okunur. Tam olarak bu dosyanın §BİRİM şerhinin
// kaçınmak için yazıldığı arıza şekli.
//
// Çare ad değiştirmek DEĞİL: `windowRangeParam` (v0.9.963) bir pencerenin
// `range=`e dönüştüğü TEK yer ve bu dosya onu hiç kullanmıyordu. Ona
// geçmek üç şeyi birlikte getiriyor: (1) `fromNs` artık gerçekten
// nanosaniye, (2) `from` floor / `to` CEIL — yuvarlama pencereyi asla
// DARALTMIYOR (kesilen `to` en yeni kovayı düşürür, operatörün görmeye
// geldiği yarıyı), (3) `decodeRange`'in reddedeceği pencerede token
// yerine '' — adres çubuğunda kendinden emin ama yanlış bir pencere
// yazmıyor. Üçü de elle string kurarken kaybediliyordu.
import { windowRangeParam } from '@/lib/urlState';
import type { Problem, TimeRange } from '@/lib/types';

/**
 * latencyThresholdMs — problemin eşiği milisaniye olarak, ya da null
 * ("bu problem bir gecikme problemi değil / birimi kanıtlanamıyor").
 *
 * null bir CEVAP: çağıran linki hiç çizmez. Yanlış birimle çizilmiş bir
 * "Slow traces" linki, hiç link olmamasından daha pahalıdır — boş liste
 * "yavaş trace yok" diye okunur.
 */
export function latencyThresholdMs(p: Pick<Problem, 'metric' | 'threshold'>): number | null {
  const metric = (p.metric ?? '').trim();
  if (!metric.endsWith('_ms')) return null;
  const t = p.threshold;
  if (typeof t !== 'number' || !Number.isFinite(t) || t <= 0) return null;
  return t;
}

/**
 * slowTracesHref — servis + minMs + problem penceresi.
 *
 * rootOnly=false: yavaş span kök olmak zorunda değil (bir DB çağrısı ya
 * da downstream RPC olabilir) ve /traces'in varsayılanı kök-only. Hata
 * pivotu v0.8.585'te operatör raporuyla tam bu yüzden düzeltilmişti;
 * gecikme tarafında aynı tuzak birebir geçerli.
 */
export function slowTracesHref(
  service: string, minMs: number,
  window: TimeRange | { fromNs: number; toNs: number },
): string {
  const p = new URLSearchParams();
  p.set('service', service);
  // Tam sayı: /traces minMs'i metin olarak taşıyor ve 3573.4759511999923
  // gibi bir eşik URL'de gürültüden başka bir şey değil.
  p.set('minMs', String(Math.round(minMs)));
  p.set('rootOnly', 'false');
  // '' = windowRangeParam pencereyi reddetti. Parametreyi ATLIYORUZ, boş
  // yazmıyoruz — serviceHref'in kuralı (serviceHref.ts:67). Sayfa o
  // durumda sticky pencereyi çizer; yanlış bir pencere yazmaktan iyidir.
  const range = windowRangeParam(window);
  if (range) p.set('range', range);
  return `/traces?${p.toString()}`;
}
