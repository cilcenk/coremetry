import { useEffect, useRef } from 'react';
import type { CSSProperties, HTMLAttributes, ReactNode } from 'react';

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

// v0.9.669 — kalan div ÖZNİTELİKLERİ geçirgen.
//
// AIObservability'nin barı `data-shortcut-search` taşıyordu ve
// GlobalShortcuts o özniteliği arama kısayolunun AÇIK işareti olarak
// arıyor (querySelectorAll('[data-shortcut-search]')). PageControls'a
// dönüştürürken öznitelik sessizce düşmüştü — sayfa çalışmaya devam
// eder, yalnız kısayol ölür; fark edilmesi zor bir kayıp.
//
// Genel geçirgenlik tercih edildi çünkü sorun tek bir öznitelikte
// değil: `.controls`u kullanan 20+ sayfanın taşıdığı her data-*/aria-*
// aynı şekilde düşerdi.
type PageControlsAllProps = PageControlsProps &
  Omit<HTMLAttributes<HTMLDivElement>, 'style' | 'className' | 'children'>;

export function PageControls({ children, sticky = false, className, style, ...rest }: PageControlsAllProps) {
  const ref = useRef<HTMLDivElement>(null);

  // v0.9.644 — barın YÜKSEKLİĞİNİ `--controls-h` olarak yayınla.
  //
  // Yapışkan tablo BAŞLIĞI da `#content`'e yapışıyor; ikisi de top:0
  // olsaydı üst üste binerlerdi. Başlık `top: var(--controls-h)` ile
  // barın ALTINA yapışıyor.
  //
  // Sabit bir sayı yazmak kırılgan olurdu: bar sarınca (dar pencere,
  // çok filtre) yüksekliği değişiyor. ResizeObserver bunu izliyor.
  //
  // Değişken `#content`'e yazılıyor — sayfa başına tek scrollport o, ve
  // aynı ağaçtaki tablolar oradan miras alıyor. Bar yoksa değişken hiç
  // tanımlanmıyor ve başlık `top: 0` fallback'ine düşüyor.
  useEffect(() => {
    const el = ref.current;
    if (!el || !sticky) return;
    // v0.9.981 — `.page-body` de geçerli host: `/problems` ve `/inbox`
    // detay açıkken liste kabı id yerine sınıf taşıyor (çift `#content`
    // düzeltmesi). Yalnız `#content` aransaydı o iki sayfada
    // `--controls-h` HİÇ yayınlanmaz, yapışkan tablo başlığı `top: 0`
    // fallback'ine düşüp filtre barının ALTINA gizlenirdi.
    const host = el.closest('#content, .page-body') as HTMLElement | null;
    if (!host) return;
    const apply = () => host.style.setProperty('--controls-h', `${el.offsetHeight}px`);
    apply();
    const ro = new ResizeObserver(apply);
    ro.observe(el);
    return () => {
      ro.disconnect();
      host.style.removeProperty('--controls-h');
    };
  }, [sticky]);

  return (
    // rest ÖNCE: className/style/ref açıkça sonra yazılıyor ki bir
    // çağıran onları kazara ezemesin.
    <div {...rest} ref={ref} className={[
      'controls',
      sticky ? 'is-sticky' : '',
      className ?? '',
    ].filter(Boolean).join(' ')} style={style}>
      {children}
    </div>
  );
}
