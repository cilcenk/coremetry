// panelMenu — panel ⋯ menüsünün TEK satır sözleşmesi (v0.9.1163).
//
// OPERATÖR-RAPORLU KUSUR: servis Overview'ında her grafik panelinde İKİ ⋯
// vardı. Dış sarmalayıcı (MetricPanel, compact modda sağ-üste MUTLAK
// konumlanır) kapı eylemlerini taşıyordu — ⤢ Explore / ✎ Edit / ⟨⟩ View
// query / ⧉ Copy link; içindeki CorePanel kendi başlık satırında panel
// eylemlerini taşıyordu — tam ekran / CSV / sorguyu göster / log ölçek.
// İki menü, iki tetik, aynı köşe. v0.9.890 glifleri birleştirince (⋮ → ⋯)
// kopya GÖRÜNÜR hâle geldi: aynı ikon iki kez, farklı içerikle.
//
// ÇÖZÜM: menü SAHİBİ tektir. Dış sarmalayıcı kendi kebabını bastırır ve
// eylemlerini bu şekille içteki panele DEVREDER; panel onları kendi
// listesinin başına, ayraçla, kendi satır atomuyla basar.
//
// Şekil PRİMİTİF alanlardan kurulu (ReactNode DEĞİL) ve bu bilinçli — üç
// sebep, hepsi ölçülebilir:
//
//   • SATIR DİLİ panele ait. ReactNode devri, çağıranın kendi dropdown
//     satırını (MetricPanel'in MenuItem'ı) panelin listesine sokması
//     demekti; panelin yerli satırları başka bir atomsa tek listede İKİ
//     dialekt okunur (v0.9.890'ın BB10 dalgasının kapattığı hastalığın
//     kendisi, bir seviye yukarıda).
//   • MENÜYÜ panel kapatır. ReactNode ile çağıran, panelin `menuOpen`
//     state'ine erişemez — devredilen her satır tıklandıktan sonra menüyü
//     AÇIK bırakırdı.
//   • AYRAÇ ve SIRA kararı tek yerde kalır (devredilenler üstte).
//
// keepOpen: bazı satırlar menüyü bilerek açık bırakır. CorePanel'in "Log
// ölçek"i zaten öyle davranıyor (bir toggle'ın sonucu menüde okunur) ve
// "Copy link" geri bildirimi ("⧉ Copied") aynı sebeple satırın KENDİ
// etiketinde yaşar — v0.8.550 dersi (menü "Copied" derken hiçbir şey
// kopyalanmamış olmasın) bu kanalda da geçerli: etiket yalnız gerçek bir
// kopyada değişir.

export interface PanelMenuAction {
  // Kararlı kimlik (React key). `label` anahtar OLAMAZ: geçici durum
  // taşıyor — '⧉ Copy link' → '⧉ Copied' geçişinde satır remount olur ve
  // klavye fokusunu kaybederdi.
  key: string;
  // Satır etiketi. Glif etikete GÖMÜLÜ ('⤢ Explore'), MenuItem'ın ikon
  // yuvasına DEĞİL: yuva sabit genişliktedir ve yalnız ikon verilen
  // satırlarda basılır — bir kısmı ikonlu bir kısmı ikonsuz liste ragged
  // hizalanır. Devredilen aile glifli, panelin yerlileri glifsiz; ikisi de
  // aynı x'ten başlar.
  label: string;
  onClick: () => void;
  disabled?: boolean;
  // true ise tık menüyü KAPATMAZ (toggle / yerinde geri bildirim).
  keepOpen?: boolean;
}
