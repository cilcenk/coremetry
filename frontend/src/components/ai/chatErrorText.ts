import { aiErrorHint } from '@/lib/aiErrors';

// chatErrorText — sohbet balonundaki hata metni (v0.10.22, Copilot
// denetimi bulgu: "ham hata metni").
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Sohbet hatası ChatBubble'da HAM basılıyordu. Sunucu `err.Error()`'ı
// olduğu gibi yayınlıyor ve sağlayıcı katmanı `url.Error` sarmalayıcısını
// koruduğu için operatörün gördüğü şey şuydu:
//
//     ⚠ openai-compat call: Post "http://<host>:8000/v1/chat/completions":
//       dial tcp 10.x.x.x:8000: connect: connection refused
//
// Operatör bundan ne yapacağını çıkaramıyor. Oysa aynı depoda ona
// "AI uç noktasına ulaşılamıyor (ağ/TLS). Settings → AI Copilot adresini
// kontrol edin." diyen `aiErrorHint` v0.9.200'den beri duruyor —
// AIAnalysisPanel kullanıyor, sohbet kullanmıyordu.
//
// ── NEDEN REDAKTE ETMİYORUZ ─────────────────────────────────────────────
//
// Ham metin iç host ve port taşıyor ve sohbet her kimliği doğrulanmış
// kullanıcıya açık (viewer dâhil). Bunu SİLMEK akla geliyor ve BİLİNÇLİ
// OLARAK yapılmıyor: operatörün duruşu tam-sadakat yönünde (admin export
// düz metin kararı, kabul edilen güvenlik duruşu) ve host'u silmek
// teşhis gücünü azaltır — "ulaşılamıyor" diyen bir mesaj, HANGİ adrese
// ulaşılamadığını söylemezse yarım kalır.
//
// Onun yerine sıralama değişiyor: eyleme dönük cümle ÖNE, ham metin
// erişilebilir ama ikincil. Bilinmeyen bir hata sınıfında ham metin
// yine tek başına gösteriliyor — bir ipucu uyduramayız.

export interface ChatErrorView {
  /** Balonda görünen metin. */
  text: string;
  /**
   * İkincil ham metin (tooltip). null = zaten ham metin gösteriliyor,
   * tekrarlamanın anlamı yok.
   */
  raw: string | null;
}

/**
 * chatErrorText — ham sohbet hatasını gösterilecek hâle çevirir.
 *
 * Saf: hata metinleri sağlayıcıya göre değişiyor, o yüzden eşleme
 * deterministik ve tablo-testli olmalı.
 */
export function chatErrorText(err: string): ChatErrorView {
  const msg = (err ?? '').trim();
  if (!msg) {
    return { text: 'AI isteği bilinmeyen bir hatayla düştü.', raw: null };
  }
  // "yapılandırılmamış" ayrı bir sınıf: bu bir arıza değil, eksik kurulum.
  // AIAnalysisPanel'in aynı ayrımı — operatöre ne YAPACAĞINI söylüyor.
  if (/AI copilot not configured/i.test(msg)) {
    return {
      text: 'Bir AI modeli yapılandırılmamış. Settings → AI bölümünden kendi modelinizi tanımlayın.',
      raw: msg,
    };
  }
  const hint = aiErrorHint(msg);
  // Bilinmeyen sınıf → ham metin. Uyduramayız; yarım bir tahmin,
  // ham metinden kötüdür.
  return hint ? { text: hint, raw: msg } : { text: msg, raw: null };
}
