import type { HTMLAttributes, ReactNode } from 'react';

// Card is the headed-panel container that 30+ pages roll on
// their own with the same bg1+border+8px-radius inline style.
// Two density variants:
//   - default  → bg1 + 14px padding + 8px radius (page-level
//                section like the Service Infra panel)
//   - tight    → bg2 + 10px padding + 6px radius (inline meta
//                callout, sparkline tile, anomaly card)
// `header`/`footer` slots keep the divider lines consistent
// without repeating border/padding in every caller.

type Density = 'default' | 'tight';

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  density?: Density;
  header?: ReactNode;
  footer?: ReactNode;
  children?: ReactNode;
}

export function Card({
  density = 'default', header, footer, className, children, ...rest
}: CardProps) {
  const cls = ['card', density === 'tight' ? 'card-tight' : '', className]
    .filter(Boolean).join(' ');
  return (
    <div className={cls} {...rest}>
      {header && (
        <div style={{
          marginBottom: 'var(--sp-5)',
          paddingBottom: 'var(--sp-4)',
          borderBottom: '1px solid var(--border)',
          fontSize: 'var(--fs-md)', fontWeight: 600,
        }}>
          {header}
        </div>
      )}
      {children}
      {footer && (
        <div style={{
          marginTop: 'var(--sp-5)',
          paddingTop: 'var(--sp-4)',
          borderTop: '1px solid var(--border)',
          fontSize: 'var(--fs-xs)', color: 'var(--text3)',
        }}>
          {footer}
        </div>
      )}
    </div>
  );
}
