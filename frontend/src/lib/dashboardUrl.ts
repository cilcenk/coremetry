// dashboardUrl (v0.9.779) — /dashboard adres çubuğunun SAF kuralları.
//
// Neden ayrı dosya: Dashboard.tsx'teki değişken aynası "rezerve
// OLMAYAN her paramı sil" mantığıyla çalışıyor (bir panonun Grafana
// tarzı değişkenleri ?<ad>=<değer> olarak yazılıyor, dolayısıyla
// beyaz liste değil KARA liste tutulabiliyor). Bu, yeni bir sayfa
// paramı ekleyen herkes için bir tuzak: listeye yazmayı unutursan
// paramın her render'da sessizce siliniyor ve özellik "çalışmıyor"
// görünüyor. Kural burada, testli ve tek yerde.
//
// İkinci parça: auto-refresh aralığının ayrıştırılması. Adres
// çubuğundan gelen değer operatör tarafından elle düzenlenebilir
// (paylaşılan link, yapıştırılan URL) — bilinmeyen/bozuk her değer
// KAPALI'ya düşer, asla varsayılan bir aralığa değil: kimse
// istemediği bir yenileme döngüsünü miras almamalı.

// DASHBOARD_RESERVED_PARAMS — /dashboard'un KENDİ paramları. Bunların
// dışındaki her ?key=value bir pano değişkeni değeri sayılır.
export const DASHBOARD_RESERVED_PARAMS: ReadonlySet<string> = new Set([
  'id',      // hangi pano
  'edit',    // düzenleme modunda aç
  'range',   // zaman penceresi (useUrlRange)
  'refresh', // auto-refresh aralığı, saniye (v0.9.779)
  'kiosk',   // TV/kiosk modu (v0.9.779)
]);

// isDashboardVariableParam — bu param bir pano değişkeninin değeri mi?
export function isDashboardVariableParam(key: string): boolean {
  return !DASHBOARD_RESERVED_PARAMS.has(key);
}

// REFRESH_CHOICES — seçilebilir aralıklar (saniye). 0 = kapalı.
//
// 10s BİLİNÇLİ olarak YOK. Bir dashboard bundle isteği 50 panele
// kadar sorgu taşıyabiliyor; 10s'lik bir döngü büyük bir panoda
// ClickHouse'a sürekli baskı demek. Ev kuralı zaten ≥10s diyor, biz
// tabanı 30s'e çekiyoruz — panel verisinin efektif tazeliği sunucu
// cache TTL'ine bağlı olduğundan daha sık sormak yalnızca yük üretir.
export const REFRESH_CHOICES: readonly number[] = [0, 30, 60, 300];

// parseRefreshParam — ?refresh=<sn> → geçerli seçim ya da 0 (kapalı).
// Listede olmayan / sayı olmayan / negatif her şey KAPALI.
export function parseRefreshParam(raw: string | null | undefined): number {
  if (!raw) return 0;
  const n = Number(raw);
  if (!Number.isFinite(n)) return 0;
  return REFRESH_CHOICES.includes(n) ? n : 0;
}

// refreshLabel — seçim → operatöre gösterilen etiket.
export function refreshLabel(sec: number): string {
  if (!sec) return 'Kapalı';
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${sec / 60}m`;
  return `${sec / 3600}h`;
}
