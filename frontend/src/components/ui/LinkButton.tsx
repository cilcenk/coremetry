import { forwardRef, type ButtonHTMLAttributes } from 'react';

// LinkButton — "looks like a link, IS a button" (v0.9.889 dalgası, MB5).
//
// Depoda 8 site aynı affordansı beş farklı görünümde kuruyordu ve
// hiçbirinde hover ya da focus tanımlı DEĞİLDİ: hepsi `all: 'unset'` ile
// element-seviyesi buton kuralından kaçıyor, `all: unset` de
// `outline-style`ı sıfırladığı için `:focus-visible` halkasını yiyordu.
// Yani bunlar klavyeyle ulaşılabilir ama GÖRÜLEBİLİR değildi.
//
// NEDEN `<a>` DEĞİL: bu tetiklerin hiçbiri bir URL'e gitmiyor — filtre
// temizliyor, sohbet açıyor, karşılaştırma açıp kapıyor. `<a href="#">`
// yazmak orta tıkla yeni sekme vaadi verir ve o vaat tutulamaz. Gerçek
// gezinme yapan yerler `<Link>` kullanmaya devam ediyor.
//
// `tone`: accent (varsayılan, "burada bir eylem var") · muted (yoğun
// tablolarda satırın kendi metniyle yarışmaması gereken tetikler).
// `underline`: hover (varsayılan) · dotted (AdminAudit'in "bu değere göre
// filtrele" varyantı — nokta çizgi "bu bir kısayol" demenin ev deseni) ·
// none.

type Tone      = 'accent' | 'muted';
type Underline = 'hover' | 'dotted' | 'none';

export interface LinkButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  tone?: Tone;
  underline?: Underline;
}

const toneClass: Record<Tone, string> = {
  accent: '',
  muted:  'lb-muted',
};
const underlineClass: Record<Underline, string> = {
  hover:  '',
  dotted: 'lb-dotted',
  none:   'lb-plain',
};

export const LinkButton = forwardRef<HTMLButtonElement, LinkButtonProps>(function LinkButton(
  { tone = 'accent', underline = 'hover', className, type = 'button', children, ...rest },
  ref,
) {
  const classes = [
    'btn-link',
    toneClass[tone],
    underlineClass[underline],
    className,
  ].filter(Boolean).join(' ');

  return (
    <button ref={ref} type={type} className={classes} {...rest}>
      {children}
    </button>
  );
});
