// navScope — j/k arbitrajı (v0.9.928).
//
// PROBLEM. v0.9.926 kısayol defterini YIĞIN yaptı, yani iki gezinilebilir
// tablo aynı anda mount olduğunda ikisinin de kaydı ayakta kalıyor ve
// unmount alttakini öldürmüyor. Ama "hangisi sahip?" sorusunun cevabı
// hâlâ **yığının tepesi** = EN SON MOUNT OLAN idi. Bu bir varsayılan,
// tasarım değil: operatör ekranın altındaki tabloya tıklayıp j'ye bassa
// seçim yukarıdaki (ya da rastgele geç mount olan) tabloda yürüyordu.
//
// KURAL. Sahip = **son etkileşim** alan tablo. Etkileşim üç yoldan gelir:
// tık (pointerdown), odak (focusin), klavye (kapsam İÇİNDEKİ bir elemana
// gelen keydown). Hiç etkileşim olmamışsa — sayfa yeni açıldı — davranış
// eskisi gibi kalır: yığının tepesi.
//
// YAPIŞKANLIK bilinçli. Kapsam DIŞINDA bir yere tıklamak (filtre çipi,
// zaman aralığı, boşluk) sahibi DEĞİŞTİRMEZ. Aksi hâlde operatör tabloya
// tıklayıp sonra üstteki filtreye dokununca j/k sessizce başka tabloya
// atlardı — "her tık bir karar" en kötü tür kararsızlıktır.
//
// KENDİ KENDİNİ ONARIR. Aktif kapsam unmount olursa yığında o `scope`a
// sahip binding kalmaz; `pickOwner` tepeye düşer. Ayrı bir temizlik
// kancası (ve onun unutulma riski) yok.
//
// Kimlik taşıyıcısı `data-table-id` — v0.9.926'da oto-kaydırma için
// zaten basılıyordu. Arbitraj aynı damgayı okur; iki ayrı kimlik sistemi
// er geç birbirinden ayrı düşerdi.

/** Tablo kimliğini taşıyan DOM özniteliği (useTableNav `pageId`). */
export const NAV_SCOPE_ATTR = 'data-table-id';

/** Bir kısayolun sahiplenme kapsamı — bağlıysa `data-table-id` ile aynı. */
export interface ScopedBinding {
  scope?: string;
}

// pickOwner — SAF arbitraj. Kayıt yığını + aktif kapsam → kazanan binding.
//
//   1. Aktif kapsamı olan bir kayıt varsa O kazanır (birden fazlaysa en
//      son kaydolan — aynı tablonun re-register'ı).
//   2. Yoksa yığının tepesi (son mount) — v0.9.926 davranışı.
//
// (2) hem "hiç etkileşim yok" hem "aktif kapsam artık ekranda değil"
// hâllerini kapsıyor; ikisi de aynı doğru cevaba gidiyor.
export function pickOwner<T extends ScopedBinding>(
  stack: readonly T[],
  activeScope: string | null,
): T | undefined {
  if (stack.length === 0) return undefined;
  if (activeScope) {
    for (let i = stack.length - 1; i >= 0; i--) {
      if (stack[i].scope === activeScope) return stack[i];
    }
  }
  return stack[stack.length - 1];
}

// scopeFromTarget — bir olay hedefinden en yakın tablo kimliği.
// `closest` kullanıyor: tık satırın İÇİNDEKİ bir hücreye/rozete gelir,
// kimlik `<tr>`de (satır) ya da `<thead>`de (başlık tıkı) durur.
export function scopeFromTarget(t: EventTarget | null): string | null {
  if (!t || typeof (t as Element).closest !== 'function') return null;
  const el = (t as Element).closest(`[${NAV_SCOPE_ATTR}]`);
  if (!el) return null;
  return el.getAttribute(NAV_SCOPE_ATTR) || null;
}

let activeScope: string | null = null;

export function getActiveNavScope(): string | null {
  return activeScope;
}

/** Test/kurtarma kancası — normal akışta `noteInteraction` çağrılır. */
export function setActiveNavScope(id: string | null): void {
  activeScope = id;
}

// noteInteraction — bir etkileşimi kaydet. Hedef bir tablo kapsamının
// içinde DEĞİLSE hiçbir şey yapmaz (yapışkanlık kuralı). Değişiklik
// olduysa true döner.
export function noteInteraction(t: EventTarget | null): boolean {
  const scope = scopeFromTarget(t);
  if (!scope || scope === activeScope) return false;
  activeScope = scope;
  return true;
}
