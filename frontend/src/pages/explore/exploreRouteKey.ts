// exploreRouteKey.ts (v0.9.805) — /explore'un REMOUNT kararı, saf.
//
// SORUN. ExploreInner URL'i mount başına BİR KEZ çözüyor (builderSeed guard,
// Explore.tsx) ve ondan sonra state tek gerçek. Remount ise yalnız
// `key={meaningful ? 'workspace' : 'entry'}` sınırında oluyordu. Yani
// ÇALIŞAN bir Explore'da:
//
//   SavedViewsBar.apply → navigate('/explore?q=<başka sorgu>')
//   → meaningful hâlâ true → key aynı → REMOUNT YOK → seed okunmaz
//   → ekran ESKİ sorguda kalır.
//
// Üstelik ilk düzenlemede state→URL effect'i çalışıp saved view'in URL'ini
// eziyor: operatör çipe bastı, hiçbir şey olmadı, sonra da linki kayboldu.
// Aynı şey ⌘K derin linkleri ve 1-9 kısayolları için de geçerli. Bu,
// one-way-read hata sınıfının dördüncü vakası (v0.8.253/256/265/267).
//
// ÇÖZÜM. Sayfa anahtarına sorgunun NORMALİZE İMZASINI kat. Ama bunun bir
// tuzağı var ve düzeltmenin asıl zorluğu orada: state→URL yazımları da
// URL'i değiştiriyor. İmzayı düz katarsak operatörün her düzenlemesi
// remount tetikler — yani ekranın kendi kendini sıfırlaması, düzeltmeye
// çalıştığımız hatadan beter.
//
// Ayrım şu: ExploreInner navigate etmeden ÖNCE "state şu an bu URL'i
// kodluyor" diye imzasını bildiriyor (onSelfWrite). Gelen imza o bildirime
// eşitse bu bizim kendi yankımızdır, anahtar SABİT kalır. Eşit değilse URL'i
// dışarıdan biri değiştirmiştir (saved view / derin link / geri tuşu) ve
// remount ŞARTTIR.
//
// Bildirim TEK DEĞERLİ, küme değil, ve bu bilinçli: "en son yazdığımız URL"
// state'in kendisidir. Geçmiş yazımları biriktirseydik A→B→A' düzenlemesinden
// sonra B'yi seçen bir saved view "bunu biz yazmıştık" diye yutulurdu.

// hasMeaningfulParams — `range` DIŞINDA bir param var mı? range her zaman
// yazılır (Topbar), dolayısıyla tek başına "operatör bir şey sordu" demek
// değildir. Giriş ↔ workspace sınırının tanımı (Phase-1'den beri).
export function hasMeaningfulParams(sp: URLSearchParams): boolean {
  for (const k of sp.keys()) {
    if (k !== 'range') return true;
  }
  return false;
}

// exploreQuerySig — "hangi sorgu?" sorusunun kısa, kararlı cevabı.
//
// Kapsam: `range` HARİÇ her param. Brief'in adlandırdığı `q` + `result`
// bunun alt kümesi; traces/repeats konsolunun kendi kanonik paramları
// (filters/dsl/mode/limit/count/cols/groupBy/minRepeats) ve metrics/logs
// panellerinin passthrough'ları (source/viz/service/metric) da SORGUNUN
// parçası — yalnız q'ya bakan bir imza, filtresi değişen bir traces saved
// view'ini yok sayardı. range dışarıda: aralık değişimi sorguyu değiştirmez,
// zaten Topbar üzerinden canlı yönetiliyor ve remount istemez.
//
// Normalizasyon: boş değerler atılır (buildQuery zaten basmıyor ama elle
// yazılmış bir link basabilir), anahtarlar sıralanır — böylece param SIRASI
// imzayı değiştirmez ve aynı sorgunun iki yazımı aynı anahtara düşer.
export function exploreQuerySig(search: string): string {
  const sp = new URLSearchParams(search);
  const parts: string[] = [];
  for (const [k, v] of sp.entries()) {
    if (k === 'range' || v === '') continue;
    parts.push(`${k}=${v}`);
  }
  if (parts.length === 0) return '';
  parts.sort();
  const canon = parts.join('&');
  // FNV-1a 32-bit (seriesColor emsali) + uzunluk. Anahtar kısa kalsın diye
  // hash'liyoruz; çakışmanın tek bedeli kaçırılmış bir remount olurdu ve
  // uzunluk ön eki onu da pratikte imkânsızlaştırıyor.
  let h = 2166136261;
  for (let i = 0; i < canon.length; i++) {
    h ^= canon.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return `${canon.length.toString(36)}.${(h >>> 0).toString(36)}`;
}

export interface ExploreKeyState {
  key: string;   // React key'i — DEĞİŞİRSE ExploreInner remount olur
  sig: string;   // o anda ekranda olan sorgunun imzası
}

export const EXPLORE_ENTRY_KEY = 'entry';

// nextExploreKey — SAF geçiş fonksiyonu.
//
//   prev             bir önceki karar (ilk render'da null)
//   search           window.location.search (router'dan)
//   lastSelfWriteSig ExploreInner'ın en son bildirdiği imza (yoksa null)
export function nextExploreKey(
  prev: ExploreKeyState | null,
  search: string,
  lastSelfWriteSig: string | null,
): ExploreKeyState {
  if (!hasMeaningfulParams(new URLSearchParams(search))) {
    // Workspace → giriş de bir sınır geçişi: anahtar değişir, remount olur.
    return prev && prev.key === EXPLORE_ENTRY_KEY
      ? prev
      : { key: EXPLORE_ENTRY_KEY, sig: '' };
  }
  const sig = exploreQuerySig(search);
  // Aynı sorgu → hiçbir şey değişmedi (range değişimi buraya düşer).
  if (prev && prev.sig === sig) return prev;
  // Giriş → workspace: eskiden beri remount eden sınır; state URL'den
  // tohumlanır, o yüzden taze mount DOĞRU davranış.
  if (!prev || prev.key === EXPLORE_ENTRY_KEY) return { key: `workspace:${sig}`, sig };
  // Kendi yazımımızın yankısı: imzayı benimse, ANAHTARA DOKUNMA.
  if (sig === lastSelfWriteSig) return { key: prev.key, sig };
  // Dışarıdan gelen sorgu (saved view / derin link / geri tuşu) → remount.
  return { key: `workspace:${sig}`, sig };
}
