import type { ChatTurn } from '@/lib/types';

// chatAbort — kullanıcının durdurduğu bir akışı ARIZADAN ayırır
// (v0.10.23, Copilot denetimi bulgusu: "iptal düğmesi yok").
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// `useChatThread` bir AbortController KURUYOR ama yalnız unmount'ta
// ateşliyordu ve `stop` fonksiyonunu dışa vermiyordu. CopilotChat ise
// AppShell'de KALICI monte, yani çekmeceyi kapatmak bile akışı
// durdurmuyordu.
//
// Sonuç: operatör yanlış soruyu sorduğunda ya da model saçmalamaya
// başladığında yapabileceği tek şey beklemekti — üstelik input
// `disabled={busy}` olduğu için yeni soru da yazamıyordu. Yerel gemma4
// tek GPU'da koşuyor: istenmeyen bir 5-turlu döngü, operatörün SIRADAKİ
// meşru sorusunun önünü dakikalarca tıkıyor.
//
// ── NEDEN AYRI BİR MODÜL ────────────────────────────────────────────────
//
// İptal edilen bir fetch AbortError FIRLATIR ve `send`in catch dalına
// düşer. Hiçbir şey yapılmazsa kasıtlı bir kullanıcı eylemi, kırmızı bir
// "⚠ signal is aborted without reason" balonuna dönüşür — yani düzeltme
// yeni bir kusur üretir. Ayrım saf ve testli olmalı.

/**
 * isAbortError — bu hata kullanıcının durdurmasından mı geliyor?
 *
 * Tarayıcılar ve fetch polyfill'leri farklı şekiller üretiyor:
 * DOMException(name='AbortError'), "signal is aborted without reason",
 * ya da düz "AbortError". Üçünü de tanıyoruz; yanlış negatif, kullanıcıya
 * sahte bir arıza göstermek demek.
 */
export function isAbortError(err: unknown): boolean {
  if (!err) return false;
  if (typeof err === 'object' && 'name' in err && (err as { name?: string }).name === 'AbortError') {
    return true;
  }
  const msg = err instanceof Error ? err.message : String(err);
  // ⚠ `(ed|error)?` — ilk yazımda `(ed)?` yazmıştım ve `Error('AbortError')`
  // şekli KAÇIYORDU: "abort" ile "Error" arasında sözcük sınırı yok, yani
  // `abort\b` tutmuyor. Test onu yakaladı. Kaçan her şekil, kullanıcının
  // kasıtlı durdurmasını SAHTE BİR ARIZAYA çeviriyor.
  return /\babort(ed|error)?\b/i.test(msg) || /signal is aborted/i.test(msg);
}

/**
 * settleStoppedTurn — durdurulan turu kapatır.
 *
 * ⚠ AKAN METİN KORUNUYOR. Operatör çoğu zaman "yeterince gördüm" ya da
 * "yanlış yola gitti" diye durduruyor; o ana kadarki metni silmek,
 * durdurma sebebinin kendisini yok etmek olurdu. Metin varsa tur normal
 * bir cevap gibi kalıyor, yalnız durdurulduğu İLAN ediliyor.
 *
 * Metin hiç gelmediyse (henüz ilk token yok) balon boş kalamaz — o zaman
 * kısa bir bilgi metni konuyor. Boş bir balon, "cevap geldi ama boş"
 * izlenimi verir.
 */
export function settleStoppedTurn(t: ChatTurn): ChatTurn {
  const hasText = !!(t.text && t.text.trim());
  return {
    ...t,
    pending: false,
    // error DEĞİL: bu bir arıza değil, kullanıcının kararı. Kırmızı bir
    // hata balonu operatöre "bir şey bozuldu" der ve yanlış olur.
    error: undefined,
    text: hasText ? t.text : 'Durduruldu.',
    stopped: true,
  };
}
