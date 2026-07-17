import { pctColor } from './thresholds';

// Gauge — radial kullanım göstergesi (v0.9.31, design handoff
// "Utilization card / 3 radial gauges"). Prototype ölçüleri: size
// 110, stroke 9, track var(--border), arc pctColor eşiğiyle,
// stroke-linecap round, -90° döndürülmüş, dashoffset .6s geçiş.
// Renk override edilebilir (pod-health gauge'u pctColor değil kendi
// eşiğini kullanır). pct null → "—" (veri yok, gauge boş track).
export function Gauge({ pct, label, sub, color, size = 110, stroke = 9 }: {
  pct: number | null;
  label: string;
  sub?: string;
  color?: string;
  size?: number;
  stroke?: number;
}) {
  const r = (size - stroke) / 2;
  const circ = 2 * Math.PI * r;
  const shown = pct == null ? 0 : pct;
  const offset = circ * (1 - shown / 100);
  const arcColor = color ?? pctColor(shown);
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }}>
      <div style={{ position: 'relative', width: size, height: size }}>
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}
          style={{ transform: 'rotate(-90deg)' }}>
          <circle cx={size / 2} cy={size / 2} r={r} fill="none"
            stroke="var(--border)" strokeWidth={stroke} />
          {pct != null && (
            <circle cx={size / 2} cy={size / 2} r={r} fill="none"
              stroke={arcColor} strokeWidth={stroke} strokeLinecap="round"
              strokeDasharray={circ} strokeDashoffset={offset}
              style={{ transition: 'stroke-dashoffset .6s ease' }} />
          )}
        </svg>
        <div style={{
          position: 'absolute', inset: 0, display: 'flex',
          alignItems: 'center', justifyContent: 'center',
          fontFamily: 'ui-monospace, monospace', fontSize: 24, fontWeight: 700,
        }}>
          {pct == null ? '—' : `${Math.round(pct)}%`}
        </div>
      </div>
      <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--text2)' }}>{label}</div>
      {sub && <div style={{ fontSize: 10, color: 'var(--text3)' }}>{sub}</div>}
    </div>
  );
}
