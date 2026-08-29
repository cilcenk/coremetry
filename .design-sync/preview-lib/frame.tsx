// _frame — önizleme çerçevesi (bileşen DEĞİL; converter yalnız bileşen adlı
// dosyaları derler, bu yardımcı esbuild ile her önizlemeye gömülür).
// Coremetry dark-first: globals.css `:root` koyu paleti tanımlar, ama kart
// sayfası beyaz zeminli — çerçeve DS zeminini (--bg) ve metin rengini kurar.
import type { CSSProperties, ReactNode } from 'react';

export function Frame({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <div style={{
      background: 'var(--bg)', color: 'var(--text)', padding: 16, borderRadius: 8,
      fontFamily: 'var(--font, -apple-system, "Segoe UI", sans-serif)', fontSize: 13, ...style,
    }}>{children}</div>
  );
}
