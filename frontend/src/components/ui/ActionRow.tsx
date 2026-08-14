import type { ReactNode } from 'react';

// ActionRow (v0.9.1007, etkileşim denetimi M5 / KN-1) — form ve kart
// aksiyon satırlarının TEK primitifi.
//
// NEDEN VAR: bu depoda kural, bir ATOMA yazıldığı her yerde tutuyor,
// yorumda kaldığı her yerde tutmuyor. Ölçüm ikili ve aynı depoda:
// `.modal-footer`ın `justify-content: flex-end`i olduğu için 14 modal
// footer'ının 14'ü de K5 uyumlu ve elle yazılmış tek istisna yok.
// Form/kart satırlarının atomu OLMADIĞI için aynı depoda 4 satır TERS
// (onay solda, iptal sağda) ve 115 çok-butonlu kapsayıcının 47'si
// satır-içi `style={{display:'flex',gap:8}}` — adı, hizası ve sırası
// her çağıranın kendi kararı.
//
// SÖZLEŞMEYİ YAPI TAŞIYOR, DİSİPLİN DEĞİL:
//   destructive → en solda (kas hafızasının "onay" beklediği yerden
//                 mümkün olduğunca uzakta)
//   secondary   → sağ kümenin solunda (iptal + yardımcı eylemler)
//   confirm     → EN SAĞDA ve TEK YUVA
//
// `confirm` tek yuva olduğu için yan yana iki dolu buton YAZILAMAZ —
// O6'nın 8 ihlali bu şekli alamaz. Aynı sebeple sıra da tartışma
// konusu değil: çağıran çocukların sırasını değil, ROLLERİNİ beyan
// ediyor.
//
// DÜRÜST NOT: bu atom bugünkü modal footer'larından SIFIR bug
// kazandırmaz — oralar zaten 14/14 doğru ve `.modal-footer` kendi
// hizasını basıyor, dolayısıyla modaller bu atoma GEÇMİYOR. Kazanç
// 4 ters satır + 47 sözleşmesiz flex satırı + henüz yazılmamış 15.
// çağıran. "Bugünkü hatayı düzeltiyor" diye değil, "sapmayı
// imkânsızlaştırıyor" diye var.
export interface ActionRowProps {
  /** Yıkıcı eylem — satırın EN SOLUNDA durur. */
  destructive?: ReactNode;
  /** İptal + yardımcı eylemler. Birden fazlaysa Fragment ile geç. */
  secondary?: ReactNode;
  /** Birincil eylem. TEK yuva: iki dolu buton yazılamaz. */
  confirm?: ReactNode;
  /**
   * Satır-içi bağlamlar (tablo hücresi, düzenleme satırı) için
   * `inline-flex`. Yalnız DISPLAY değişir — sıra ve hiza aynı kalır,
   * yani sözleşme delinmiyor.
   */
  inline?: boolean;
}

export function ActionRow({ destructive, secondary, confirm, inline }: ActionRowProps) {
  return (
    <div className={inline ? 'action-row is-inline' : 'action-row'}>
      {destructive}
      {/* Ayırıcı HER ZAMAN basılıyor (yıkıcı yuva boş olsa bile):
          `justify-content: flex-end` tek başına yıkıcı olanı sağ kümeye
          yapıştırırdı. Boşken `flex: 1` bir sıfır-genişlik esneme
          öğesidir, görsel etkisi yok. */}
      <span className="action-row-gap" />
      {secondary}
      {confirm}
    </div>
  );
}
