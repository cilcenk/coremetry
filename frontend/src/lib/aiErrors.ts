// aiErrors (v0.9.200) — AI sağlayıcı hata metinlerini operatör-dostu
// Türkçe ipuçlarına eşler (operator-reported: analiz hatası ham Gemini
// 429 JSON blob'u olarak görünüyordu). null = bilinen sınıf değil,
// çağıran ham metni göstersin.
export function aiErrorHint(msg: string): string | null {
  if (/429|quota|rate.?limit|resource_exhausted|too many requests/i.test(msg)) {
    return 'AI sağlayıcı kotası/hız limiti aşıldı (429). Arka plan AI işleri 1 saat duraklatıldı — birazdan tekrar deneyin; free-tier kotaları genellikle günlük sıfırlanır.';
  }
  if (/deadline exceeded|timed? ?out|context canceled/i.test(msg)) {
    return 'AI modeli zaman aşımına uğradı — model yavaş yanıt veriyor ya da erişilemiyor. Tekrar deneyin; sürerse Settings → AI Copilot uç noktasını kontrol edin.';
  }
  if (/connection refused|no such host|EOF|dial tcp|certificate/i.test(msg)) {
    return 'AI uç noktasına ulaşılamıyor (ağ/TLS). Settings → AI Copilot adresini ve erişimi kontrol edin.';
  }
  return null;
}
