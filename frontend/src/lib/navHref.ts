// navHref — v0.9.932 (UX denetimi K2). Sinyaller arası GEZİNTİDE pencereyi
// (ve env'i) taşıyan tek yapıcı.
//
// BUG'IN ANATOMİSİ. Operatör bir olayı incelemek için custom bir pencere
// kuruyor ("şu 20 dakika"), sonra Traces → Logs → Metrics → Explore gezinip
// aynı soruyu farklı sinyallerde soruyor. Her geçişte pencere DÜŞÜYOR:
//
//   • Sidebar, ⌘K ve `g x` kısayolları çıplak href/`navigate(to)` kullanıyor.
//   • Hedefte `useUrlRange` zinciri çalışıyor: `?range=` → sticky → varsayılan.
//   • Custom pencere sticky kanalına YAZILMIYOR (v0.8.409 bilinçli karar:
//     fırçalanmış mutlak bir pencereyi kalıcılaştırmak sonraki her sayfa
//     yüklemesini donduruyordu).
//
// Yani zincir sticky'ye ya da varsayılana düşüyor. Belirtisi sinsi: hedef
// sayfa ÖNCE yanlış pencerenin verisini çekiyor (israf fetch), gelen boş
// liste "veri yok" diye okunuyor. Fark eden operatör Recents'ten iki tıkla
// pencereyi geri kuruyor (beş geçiş = on fazladan tık); fark etmeyen YANLIŞ
// PENCEREDE karar veriyor.
//
// NEDEN YALNIZ `custom:`. Göreli bir preset (30m, 6h) zaten sticky kanalıyla
// akıyor — onu URL'e çivilemek operatörün bir sonraki değişikliğini eskitir:
// paylaşılan/yer imlenmiş bağlantılar "6h" diye donar ve sticky'yi ezer.
// Kaybolan tek şey mutlak pencere, taşınan da yalnız o olmalı.
//
// HEDEF HER ZAMAN KAZANIR. `to` kendi `range`/`env`ini taşıyorsa dokunulmaz:
// bu yapıcı bir VARSAYILAN dolduruyor, bir niyeti ezmiyor.
//
// Aile: serviceHref (v0.9.860), tracesPivotHref, podDetailPath (v0.9.152).
// Onlar PIVOT'un penceresini taşıyor; bu, GEZİNTİ'ninkini.

/** URL'de mutlak (fırçalanmış) pencereyi işaretleyen ön ek. */
const ABSOLUTE_PREFIX = 'custom:';

/**
 * Hedef yola geçerken mevcut URL'den taşınması gereken paramları ekler.
 *
 * @param to      hedef yol; kendi query string'i olabilir (`/explore?source=logs`)
 * @param search  şu anki `location.search`
 */
export function navHref(to: string, search: string): string {
  const cur = new URLSearchParams(search);
  const range = cur.get('range');
  const env = cur.get('env');
  // Taşınacak bir şey yoksa girdiyi AYNEN döndür — gereksiz bir yeniden
  // kodlama (sıra değişimi, kaçış farkı) hedefin kendi param sözleşmesini
  // sessizce değiştirebilir.
  const carryRange = range && range.startsWith(ABSOLUTE_PREFIX) ? range : null;
  if (!carryRange && !env) return to;

  const hashAt = to.indexOf('#');
  const hash = hashAt >= 0 ? to.slice(hashAt) : '';
  const path = hashAt >= 0 ? to.slice(0, hashAt) : to;
  const qAt = path.indexOf('?');
  const base = qAt >= 0 ? path.slice(0, qAt) : path;
  const dest = new URLSearchParams(qAt >= 0 ? path.slice(qAt + 1) : '');

  if (carryRange && !dest.has('range')) dest.set('range', carryRange);
  if (env && !dest.has('env')) dest.set('env', env);

  const qs = dest.toString();
  return qs ? `${base}?${qs}${hash}` : `${base}${hash}`;
}
