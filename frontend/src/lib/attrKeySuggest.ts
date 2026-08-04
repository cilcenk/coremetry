// v0.9.637 — /traces "Aggregated" sekmesinde yanlış yazılmış bir
// attribute anahtarı SESSİZCE boş tablo veriyordu.
//
// Anahtar serbest metin (Traces.tsx: `attribute key (e.g. user.id)`) ve
// sorgu HARF DUYARLI — bilinçli olarak: OTel'de attribute anahtarları
// harf duyarlıdır ve operatörün yazdığı anahtarı sessizce başkasına
// eşlemek sürpriz olur (v0.9.624/634'ün ayrımı: kod içi sabit liste bir
// KAVRAMI, kullanıcı girdisi bir ANAHTARI ifade eder).
//
// Ama harf duyarlı KALMAK ile SESSİZ kalmak aynı şey değil. Operatör
// `CHANNEL_CODE` yazıyor, prod `channel_code` yazıyor, tablo boş
// dönüyor ve ekranda "bu attribute yok" ile "yazımı yanlış" ayırt
// edilemiyor. v0.9.621-635'in tamamı tam bu uyuşmazlığın kurbanlarıydı;
// operatörün onu kendi gözüyle görebilmesi gerekiyor.
//
// Çözüm: sorguyu DEĞİŞTİRME, boş sonucu AÇIKLA. Yakın-ıska varsa göster.

/** Bir öneri: gerçekte var olan anahtar + neden önerildiği. */
export type AttrKeySuggestion = {
  key: string;
  /** 'case' = yalnız harf düzeni farklı; 'similar' = yazım yakın. */
  reason: 'case' | 'similar';
};

/**
 * levenshtein — küçük dizgiler için yeterli, iteratif iki-satır.
 * Attribute anahtarları kısa (≤ ~40 karakter), maliyet önemsiz.
 */
function levenshtein(a: string, b: string): number {
  if (a === b) return 0;
  if (!a.length) return b.length;
  if (!b.length) return a.length;
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    for (let j = 1; j <= b.length; j++) {
      cur[j] = Math.min(
        prev[j] + 1,
        cur[j - 1] + 1,
        prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1),
      );
    }
    prev = cur;
  }
  return prev[b.length];
}

/**
 * suggestAttrKey — yazılan anahtar hiçbir grup üretmediyse, VERİDE
 * GERÇEKTEN VAR OLAN anahtarlar arasından en olası kastı döndürür.
 *
 * Sıra bilinçli:
 *  1. Harf-düzeni ıskası ('case') — bu oturumun tüm hata sınıfı buydu,
 *     en olası kast ve en net açıklama.
 *  2. Yazım yakınlığı ('similar') — düzenleme mesafesi, anahtar
 *     uzunluğuna göre ölçeklenen bir eşikle. Sabit bir eşik kısa
 *     anahtarlarda alakasız şeyler önerirdi ("id" → "kind").
 *
 * Eşleşme yoksa null: uydurma öneri, öneri yokluğundan kötüdür.
 * SAF — tablo testli.
 */
export function suggestAttrKey(typed: string, known: readonly string[]): AttrKeySuggestion | null {
  const t = typed.trim();
  if (!t || !known.length) return null;

  const lower = t.toLowerCase();
  for (const k of known) {
    if (k !== t && k.toLowerCase() === lower) return { key: k, reason: 'case' };
  }

  // Uzunluğa göre eşik: 1/4 karakter, en az 1, en çok 3. "id" gibi
  // kısa girdilerde her şeyi yakın saymamak için.
  const budget = Math.max(1, Math.min(3, Math.floor(t.length / 4)));
  let best: { key: string; d: number } | null = null;
  for (const k of known) {
    if (k === t) return null; // anahtar VAR — boşluk yazımdan değil
    const d = levenshtein(lower, k.toLowerCase());
    if (d <= budget && (!best || d < best.d)) best = { key: k, d };
  }
  return best ? { key: best.key, reason: 'similar' } : null;
}
