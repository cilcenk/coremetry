// nodeLinkLabel — komşuluk panelindeki düğüm linkinin ETİKETİ, SAF.
//
// v0.9.1337. Karar `FocusedNeighborhood.tsx` içinde JSX'e gömülüydü ve
// HİÇBİR testi yoktu — v0.9.1326'da mutasyonla ölçüldü: `/databases`
// dalını TERS ÇEVİRMEK 301 dosya / 4500+ testin hepsini yeşil bırakıyordu.
// Yani iki ayrı sürümde bilinçle alınmış bir DÜRÜSTLÜK kararını hiçbir şey
// tutmuyordu.
//
// KARARIN KENDİSİ (v0.9.1026 kuyruk, v0.9.1326 db — aynı ilke): etiket
// LİNKİN NE YAPTIĞINI söyler, ne yapmasını istediğimizi değil.
//   · db: v0.9.1318 öncesi yazılmış düz `db:<system>` düğümleri instance'ı
//     BİLMİYOR; hedefleri detay sayfası değil, motora daraltılmış katalog.
//     Orada "Open instance" demek yalan olur.
//   · queue: üçlü (msys+q+destination) tamamsa çekmece doğrudan açılıyor;
//     cluster yoksa v0.9.972'nin daraltılmış kataloğuna düşüyoruz ve etiket
//     bunu SÖYLÜYOR (v0.9.973 dürüstlük kararı, yönü ters çevrilmiş hâli).
//
// ⚠ DİL: buradaki dört dize bileşenden AYNEN taşındı, çevrilmedi.
// `FocusedNeighborhood` bugün karışık ("Open service →", "Recenter",
// `title="Back to the service picker"` ile `title="Topoloji yüklenemedi"`,
// `"Sabitlendi — ✕ ile bırak"` yan yana). Normalizasyon bileşenin
// tamamını ilgilendiren ayrı bir karar ve operatöre bırakıldı; bu dosya
// yalnız kararı TEST EDİLEBİLİR hale getiriyor. Dizeleri burada
// değiştirmek, ölçülmemiş bir dil kararını sessizce vermek olurdu.

export type NodeLinkKind = 'database' | 'queue';

/**
 * nodeLinkLabel — hedef href'e bakarak etiketi seçer.
 *
 * `href` = `nodeDetailHref(...)` çıktısı. Ayrım kaynağı HEDEFİN KENDİSİ,
 * düğümün alanları değil: link nereye gidiyorsa etiket onu söylemeli, ve
 * hedefi üreten mantık (`nodeDetailHref`) tek sahiptir.
 */
export function nodeLinkLabel(kind: NodeLinkKind, href: string): string {
  if (kind === 'database') {
    // Katalog hedefi = instance ÇÖZÜLEMEDİ.
    return href.startsWith('/databases')
      ? 'Veritabanı kataloğunda göster →'
      : 'Open instance →';
  }
  // queue: `destination=` varsa çekmece açılabilir, yoksa katalog.
  return href.includes('destination=')
    ? 'Topiği aç →'
    : 'Messaging kataloğunda göster →';
}
