import { Search } from 'lucide-react';
import { openCommandPalette } from './CommandPalette';

// TopbarSearch — komut paletinin GÖRÜNÜR kapısı (v0.9.1019, G1/G2).
//
// Ölçülen sorun: uygulamanın global araması ⌘K'nın arkasındaydı ve
// başka hiçbir yerde görünmüyordu. Kısayolu bilen için mükemmel, ama
// bilmeyen için özellik YOK demekti — keşfedilebilirliği sıfır bir
// yetenek. Datadog, Honeycomb, Linear ve Grafana'nın hepsi aynı
// cevabı veriyor: kutu görünür durur, kısayol ipucu kutunun İÇİNDE
// soluk yazar. Kısayolu öğreten şey kutunun kendisi oluyor.
//
// ——— Neden <button>, <input> DEĞİL —————————————————————————————
//
// Bu bir arama alanı gibi GÖRÜNÜYOR ama bir arama alanı değil: tık →
// palet açılır, yazma paletin kendi girdisinde olur. Buraya gerçek bir
// `<input>` koymak iki arama kutusu, iki durum ve iki sonuç listesi
// yaratırdı — hangisinin kazandığı belirsiz. Tek motor, iki tetik.
//
// Anlamı `<button>` taşıyor: kabuk tetikleri bu depoda `<button>`
// olmak zorunda (clickableAffordances kapısı, v0.9.923) — klavyeyle
// ulaşılabilir, Enter/Space ile çalışır, ekran okuyucuya "düğme" der.
// Bir `<div onClick>` bunların hiçbirini vermez.
//
// Mockup: artifact 665d2658 (operatör onayı 2026-08-14).
export function TopbarSearch() {
  return (
    <button type="button" className="tb-search" onClick={() => openCommandPalette()}
      // Erişilebilir ad görünen metinden DAHA fazlasını söylüyor: görme
      // engelli bir kullanıcı için "Servis, trace ID… ara" bir yer
      // tutucu değil, düğmenin ne yaptığı.
      aria-label="Ara — komut paletini açar (⌘K)"
      aria-keyshortcuts="Meta+K Control+K">
      <Search size={13} strokeWidth={2.2} className="tb-search-icon" aria-hidden="true" />
      <span className="tb-search-ph">Servis, trace ID, sayfa veya ayar ara…</span>
      {/* İki ipucu da soluk ve KUTUNUN İÇİNDE — mockup'taki gibi.
          `/` sayfanın asıl arama kutusuna odaklanıyor (M1, v0.9.1003),
          ⌘K paleti açıyor; ikisi de zaten çalışan kısayollar, burada
          yalnız GÖRÜNÜR oluyorlar. Dar ekranda ipuçları CSS ile
          düşüyor (yer yok) — davranış değişmiyor. */}
      <span className="tb-search-kbd" aria-hidden="true">/</span>
      <span className="tb-search-kbd" aria-hidden="true">⌘K</span>
    </button>
  );
}
