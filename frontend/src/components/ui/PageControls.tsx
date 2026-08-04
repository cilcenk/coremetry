import type { CSSProperties, ReactNode } from 'react';

// v0.9.639 — liste sayfalarının filtre barı için tek primitif.
//
// Denetim bulgusu #5 (etki 5/5): "Hiçbir liste sayfasında sticky filtre
// barı yok — filtreler kaydırınca ekrandan çıkıyor." Uzun bir tabloda
// operatör filtreyi değiştirmek için her seferinde başa dönüyor.
//
// SAYFA KABUĞU ZATEN DOĞRU — bunu ölçtükten sonra kapsamı daralttım:
// `#app` 100vh/overflow:hidden, `#topbar` flex-shrink:0, `#content`
// flex:1+overflow:auto. Yani "sayfa başına tek scroll ekseni" ve
// "listeye max-width yok" hedefleri ZATEN sağlanıyor. Eksik olan tek
// şey barın yapışkanlığıydı; genel bir "PageShell" yazmak var olan
// doğru kabuğu gereksiz yere yeniden icat etmek olurdu.
//
// Yapışkanlık OPT-IN (`sticky` prop'u): `.controls` sınıfını 30 dosya
// kullanıyor ve hepsi liste sayfası değil — kimi kart ızgarası, kimi
// detay paneli. Görsel olarak doğrulanmamış bir davranış değişikliği
// 30 sayfaya birden yayılmaz (artımsal rollout).
//
// İÇ ÇERÇEVE KURMUYOR: bar `#content`'in kendi kaydırma eksenine
// yapışıyor, yeni bir kaydırma konteyneri açmıyor. `.table-wrap`'ın
// yaptığı hata tam buydu ve sticky tablo BAŞLIĞINI da o öldürüyor
// (ayrı dilim — yatay kaydırma davranışını değiştiriyor).

export interface PageControlsProps {
  children: ReactNode;
  /** Kaydırırken üstte kalsın mı. Liste sayfalarında true. */
  sticky?: boolean;
  className?: string;
  /** Sayfa-özel ince ayar (tipik: marginBottom). Dönüştürülen
      sayfaların taşıdığı mevcut değerleri kaybetmemek için var —
      yeni kullanımda gerekmiyor. */
  style?: CSSProperties;
}

export function PageControls({ children, sticky = false, className, style }: PageControlsProps) {
  return (
    <div className={[
      'controls',
      sticky ? 'is-sticky' : '',
      className ?? '',
    ].filter(Boolean).join(' ')} style={style}>
      {children}
    </div>
  );
}
