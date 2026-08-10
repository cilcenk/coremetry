import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';

// MenuItem — the dropdown-menu row (v0.9.890 dalgası, BB10).
//
// Aynı iş depoda dört ayrı elle kurulumla yaşıyordu: padding, renk ve
// yazı boyu hepsinde farklı, İKİSİNDE hover JS ile
// (`onMouseEnter={e => e.currentTarget.style.background = …}`). JS-hover
// iki şeyi birden kaybettiriyor:
//   • klavye focus'u HİÇ vurgulanmıyor — `onMouseEnter` yalnız fareye
//     bakar, yani menüde ok tuşlarıyla gezen operatör nerede olduğunu
//     göremiyor;
//   • `:hover` bir CSS DURUMU; JS kopyası tema değişiminde, disabled'da
//     ve dokunmatikte ayrışıyor.
// Bu yüzden hover/focus CSS'ten geliyor ve JS-hover bu atomda YASAK.
//
// `role="menuitem"` atomdan basılıyor: dört kopyanın yalnız ikisinde
// vardı, yani iki menü ekran okuyucuya menü OLARAK tanıtılmıyordu.

export interface MenuItemProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon?: ReactNode;
  /** "Delete panel" satırı — yıkıcı olan tek satır menüde ayrışmalı. */
  danger?: boolean;
}

export const MenuItem = forwardRef<HTMLButtonElement, MenuItemProps>(function MenuItem(
  { icon, danger, className, type = 'button', children, ...rest },
  ref,
) {
  // Bir boole için `Record<'yes'|'no', string>` haritası kurmak cazipti;
  // kurulmadı. `primitiveClasses` kapısı `const classes = [...]` dizisindeki
  // literalleri de topluyor ve `map[danger ? 'yes' : 'no']` yazımında ANAHTAR
  // literalleri sınıf sanılıyordu (v0.9.890'da kapı bunu yakaladı). Tek
  // değiştiricili primitiflerde koşul doğrudan dizide durur.
  const classes = [
    'menuitem',
    danger ? 'mi-danger' : '',
    className,
  ].filter(Boolean).join(' ');

  return (
    <button ref={ref} type={type} role="menuitem" className={classes} {...rest}>
      {icon !== undefined && <span className="menuitem-icon" aria-hidden="true">{icon}</span>}
      <span className="menuitem-label">{children}</span>
    </button>
  );
});
