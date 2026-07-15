import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';

// Button is the typed shell for the existing globals.css button
// rules. Maps `variant`/`size` props to the same class names the
// raw <button> uses today (`<button>` = primary, `<button
// className="sec">` = secondary, etc.) so existing CSS keeps
// driving theme + hover/focus states. The React component just
// adds typing, an optional loading state, and a forwarded ref so
// callers can imperatively focus.
//
// Why not styled-components / CSS-in-JS? globals.css is already
// the design-token source of truth (--bg, --accent, --err). A
// runtime CSS lib would duplicate the work + add bundle weight
// without giving anything back.

// `accent` (v0.8.540) is the "emphasised but NOT primary" layer: a
// tinted chip, not a solid fill. It exists because a solid-accent
// Share would sit next to the solid-accent `Resolve` in the
// ProblemDetail action bar — two equally loud blues, which is exactly
// the "too prominent" critique Grafana#84110 took. Reach for it when a
// control must out-rank `secondary` without claiming `primary`.
type Variant = 'primary' | 'secondary' | 'danger' | 'ghost' | 'accent';
type Size    = 'sm' | 'md' | 'lg';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  // leftIcon/rightIcon let callers stick a glyph on either side
  // without managing the gap themselves.
  leftIcon?: ReactNode;
  rightIcon?: ReactNode;
}

const variantClass: Record<Variant, string> = {
  primary:   '',
  secondary: 'sec',
  danger:    'danger',
  ghost:     'ghost',
  accent:    'accent',
};
const sizeClass: Record<Size, string> = {
  sm: 'sm',
  md: '',
  lg: 'lg',
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'primary', size = 'md', loading, leftIcon, rightIcon,
    className, disabled, children, type = 'button', ...rest },
  ref,
) {
  const classes = [
    variantClass[variant],
    sizeClass[size],
    className,
  ].filter(Boolean).join(' ');

  return (
    <button
      ref={ref}
      type={type}
      className={classes || undefined}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}>
      {loading ? <span className="row gap-2">
        <span aria-hidden="true">…</span>
        {children}
      </span> : <span className="row gap-2">
        {leftIcon}
        {children}
        {rightIcon}
      </span>}
    </button>
  );
});
