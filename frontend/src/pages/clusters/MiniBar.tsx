import { pctColor } from './thresholds';

// MiniBar — eşik-renkli ince kullanım çubuğu (v0.9.32, design
// handoff — cluster kartı / node heatmap / tablo hücreleri paylaşır).
// Track var(--border), fill pctColor (override edilebilir). pct null
// → boş track (veri yok). Tema token'ı, hardcoded hex yok.
export function MiniBar({ pct, color, height = 5, width }: {
  pct: number | null;
  color?: string;
  height?: number;
  width?: number | string;
}) {
  const shown = pct == null ? 0 : Math.max(0, Math.min(100, pct));
  return (
    <div style={{
      width: width ?? '100%', height, borderRadius: 3,
      background: 'var(--border)', overflow: 'hidden',
    }}>
      {pct != null && (
        <div style={{
          width: `${shown}%`, height: '100%', borderRadius: 3,
          background: color ?? pctColor(shown),
          transition: 'width .4s ease',
        }} />
      )}
    </div>
  );
}
