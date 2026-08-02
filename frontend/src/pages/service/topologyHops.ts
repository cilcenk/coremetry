// v0.9.558 — servis Topology sekmesinin hop derinliği: URL kodeki.
//
// Operatör: "topology sekmesinde servicelerin direkt 2 hop gelsin."
// 1 hop yalnız doğrudan komşuları gösterir; bir servisin sorunu çoğu
// zaman komşusunun komşusundan gelir ve operatörün her seferinde elle
// 2'ye çıkarması gerekiyordu.
//
// Okuma ve yazma AYNI dosyada, çünkü aralarında sessiz bir sözleşme
// var: yazma "varsayılansa URL'e yazma" der, okuma "URL'de yoksa
// varsayılanı kullan" der. İkisi farklı bir varsayılan bilirse
// kullanıcının seçimi kaybolur — ve bu, bu repoda üç kez tekrarlamış
// bir hata sınıfı (tek-yön-okuma: v0.8.256 problems, v0.8.265
// service-map, v0.8.267 anomalies).
//
// Somut tuzak: eski kod `if (h > 1) set else delete` yazıyordu, yani
// "1 varsayılandır" bilgisi koda GÖMÜLÜYDÜ. Varsayılan 2'ye çıkınca bu
// satır, kullanıcının 1 hop seçimini URL'den siler ve okuma yine 2
// döndürürdü. Seçim hiçbir hata vermeden yutulurdu.

/** Sekmenin varsayılan komşuluk derinliği. */
export const DEFAULT_TOPOLOGY_HOPS = 2;

/** Sunucunun kabul ettiği aralık (api katmanı da kırpar). */
export const MIN_TOPOLOGY_HOPS = 1;
export const MAX_TOPOLOGY_HOPS = 3;

/**
 * URL'deki `hops` parametresini derinliğe çevirir.
 *
 * Bozuk/eksik/aralık dışı her girdi varsayılana ya da en yakın geçerli
 * sınıra düşer — bir URL'i elle düzenleyen operatör boş bir grafikle
 * karşılaşmamalı.
 */
export function parseTopologyHops(raw: string | null | undefined): number {
  if (raw == null || raw === '') return DEFAULT_TOPOLOGY_HOPS;
  const n = parseInt(raw, 10);
  if (!Number.isFinite(n) || n === 0) return DEFAULT_TOPOLOGY_HOPS;
  return Math.min(MAX_TOPOLOGY_HOPS, Math.max(MIN_TOPOLOGY_HOPS, n));
}

/**
 * Derinliği URL değerine çevirir. `null` = parametreyi SİL.
 *
 * Varsayılan URL'e yazılmaz (temiz link), ama karşılaştırma sabitle
 * yapılır — asla elle yazılmış bir sayıyla.
 */
export function topologyHopsUrlValue(hops: number): string | null {
  const clamped = Math.min(MAX_TOPOLOGY_HOPS, Math.max(MIN_TOPOLOGY_HOPS, hops));
  return clamped === DEFAULT_TOPOLOGY_HOPS ? null : String(clamped);
}
