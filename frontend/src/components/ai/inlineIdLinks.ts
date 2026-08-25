// inlineIdLinks — cevap metnindeki KİMLİKLERİ satır içi linke böler
// (v0.10.35, operatör isteği: "id'nin kendisinde link").
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// CoSRE cevabı `request_id : INVM0130602037…` yazıyor ve operatör o
// kimliği log arayüzünde aramak için ELLE KOPYALIYORDU. Köprü mekanizması
// (answerRequestIDLinks) beş sohbet yüzeyinde vardı; ✨ Explain
// yüzeylerinin hiçbirinde yoktu ve arayüz de `links` alanını çizmiyordu.
//
// ── NEDEN İSTEMCİDE REGEX YOK ───────────────────────────────────────────
//
// ⚠ Hangi kimliğin linklenebilir olduğuna SUNUCU karar veriyor: cevapta
// "request id" anahtar kelimesi geçiyor mu, şablon geçerli mi (http/https
// + {value}), hangi ortamın şablonu, kimliğin içindeki damgadan zaman
// penceresi çıkıyor mu. Bu kararı istemcide naif bir regex'le yeniden
// vermek, aynı mantığı iki yere koymak ve ikisinin sessizce ayrışmasına
// izin vermek olurdu (v0.10.34'te aynı gerekçeyle route'u ön yüzde
// türetmemiştim).
//
// Buradaki iş yalnız DEKORASYON: sunucunun verdiği HAM kimliği metinde
// bul, o parçayı linke çevir.
//
// ── NEDEN DESEN, BÖLÜCÜ DEĞİL ───────────────────────────────────────────
//
// İlk yazımda metni parçalara BÖLEN bir fonksiyon yazmıştım. Renderer'a
// bakınca kullanılamaz olduğu çıktı: `renderInline` eşleşmeyen metni
// KARAKTER KARAKTER basıyor (`out.push(rest[0])`), yani çıktı üzerinde
// dizge eşleştirmek mümkün değil. Mimarisi ise zaten "konumda eşleş →
// düğüm çiz" kalıbında, o yüzden doğru şekil bir DESEN.
//
// ── NEDEN HTML ENJEKTE ETMİYORUZ ────────────────────────────────────────
//
// Metin MODEL çıktısı. `<a href=…>` dizgesi üretip innerHTML'e vermek,
// escape edilmiş bir boru hattına ham HTML sokmak demekti — bu depoda
// kapısı olan bir kusur sınıfı (tooltipEscapeGate). Fonksiyon bu yüzden
// PARÇA döndürüyor; React'in kendisi metni ve <Link>'i güvenle çiziyor.

export interface IdLink {
  id?: string;
  href: string;
  label: string;
}

/** Kimlik deseni + href araması. Link yoksa null. */
export interface IdLinkPattern {
  /** `renderInline`ın diğer desenleri gibi: dizgenin BAŞINDA eşleşir. */
  re: RegExp;
  /** Eşleşen ham kimlik → link. */
  byId: Map<string, IdLink>;
}

/** Regex meta karakterlerini kaçırır — kimlikler nokta/iki nokta içerebiliyor. */
function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * idLinkPattern — sunucunun verdiği kimliklerden bir eşleşme deseni kurar.
 *
 * null = linklenecek kimlik yok; çağıran hiçbir şey değiştirmez.
 */
export function idLinkPattern(links: IdLink[] | undefined): IdLinkPattern | null {
  if (!links || links.length === 0) return null;
  // Yalnız HAM kimliği olan linkler satır içi olabilir; rota çipleri
  // (id taşımayan) metinde aranacak bir şey taşımıyor.
  const usable = links.filter(l => l.id && l.id.trim() && l.href);
  if (usable.length === 0) return null;

  // UZUN kimlik ÖNCE: kısa bir kimlik uzun olanın önekiyse, alternation
  // kısa olanı eşleştirip uzun kimliği ikiye bölerdi — operatör YANLIŞ
  // kimliğin linkine giderdi.
  const sorted = [...usable].sort((a, b) => b.id!.trim().length - a.id!.trim().length);
  const byId = new Map<string, IdLink>();
  for (const l of sorted) byId.set(l.id!.trim(), l);

  const alt = sorted.map(l => escapeRe(l.id!.trim())).join('|');
  return { re: new RegExp(`^(${alt})`), byId };
}
