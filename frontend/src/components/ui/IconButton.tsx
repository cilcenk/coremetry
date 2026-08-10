import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';

// IconButton — the square, glyph-only affordance (v0.9.884 dalgası, MB4).
//
// Depoda 10+ site bunu dosya başına elle kuruyordu: `all: 'unset'` +
// satır-içi padding. `all: unset` bir buton için sessiz bir a11y kaybıdır —
// yalnız rengi değil, `:focus-visible` HALKASINI da siler. Klavyeyle gezen
// operatör bu butonların üzerinden geçerken hiçbir şey görmüyordu. İki
// kopya (Dashboard ⋯ ve MetricPanel ⋮) aynı 26×24 tetikti ve GLİFLERİ bile
// farklıydı.
//
// `aria-label` TİP DÜZEYİNDE ZORUNLU. Bilinçli sürtünme: glif-only bir
// butonun erişilebilir adı yoksa ekran okuyucu yalnızca "buton" der. Göç
// sırasında ~17 sitede `tsc` kırılacak — her etiketin elle yazılması
// isteniyor, çünkü doğru etiket ancak bağlama bakılarak bulunur.
//
// Boyutlar kare: xs 20 / sm 24 / md 28 px. `sm`, depodaki iki 26×24
// tetiğin de oturduğu rung.
//
// Sınıf adları `ib-*` ön ekli — `sm`/`sec` gibi paylaşılan adlar
// kullanılsaydı element-seviyesi `button.sm { padding: 3px 9px }` kuralı
// karenin padding:0'ını EZERDİ (özgüllük 0,1,1 > 0,1,0) ve buton
// dikdörtgene dönerdi.

type Variant = 'secondary' | 'ghost' | 'bare';
type Size    = 'xs' | 'sm' | 'md';

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Zorunlu: glif tek başına erişilebilir ad taşımaz. */
  'aria-label': string;
  icon: ReactNode;
  variant?: Variant;
  size?: Size;
  /** Yıldız / pin / negate gibi açık-kapalı durumlar — `aria-pressed` de basar. */
  active?: boolean;
}

const variantClass: Record<Variant, string> = {
  secondary: 'ib-sec',
  ghost:     'ib-ghost',
  bare:      'ib-bare',
};
const sizeClass: Record<Size, string> = {
  xs: 'ib-xs',
  sm: 'ib-sm',
  md: 'ib-md',
};

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { icon, variant = 'ghost', size = 'sm', active, className,
    type = 'button', ...rest },
  ref,
) {
  const classes = [
    'btn-icon',
    variantClass[variant],
    sizeClass[size],
    active ? 'active' : '',
    className,
  ].filter(Boolean).join(' ');

  return (
    <button
      ref={ref}
      type={type}
      className={classes}
      aria-pressed={active === undefined ? undefined : active}
      {...rest}>
      <span className="btn-icon-glyph" aria-hidden="true">{icon}</span>
    </button>
  );
});
