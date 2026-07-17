// PhaseDonut — pod fazı halkası (v0.9.31, design handoff "Pod phase
// card"). Üç yay (Running --ok, Pending --warn, Failed --err),
// stroke 13, yuvarlak uçlar + küçük boşluklar, merkez = toplam.
// Toplam 0 → "—" (veri yok). Yaylar SVG dasharray ile; -90°'den
// saat yönünde dizilir.

const SEGMENTS = [
  { key: 'running', label: 'Running', color: 'var(--ok)' },
  { key: 'pending', label: 'Pending', color: 'var(--warn)' },
  { key: 'failed', label: 'Failed', color: 'var(--err)' },
] as const;

export function PhaseDonut({ running, pending, failed, size = 130, stroke = 13 }: {
  running: number;
  pending: number;
  failed: number;
  size?: number;
  stroke?: number;
}) {
  const counts = { running, pending, failed };
  const total = running + pending + failed;
  const r = (size - stroke) / 2;
  const circ = 2 * Math.PI * r;
  const gap = total > 1 ? 2 : 0; // segmentler arası küçük boşluk (px)

  let acc = 0;
  const arcs = SEGMENTS.map(seg => {
    const v = counts[seg.key];
    const frac = total > 0 ? v / total : 0;
    const len = Math.max(0, frac * circ - gap);
    const dash = `${len} ${circ - len}`;
    const offset = -acc; // dönüş yönü
    acc += frac * circ;
    return { seg, v, dash, offset, show: v > 0 };
  });

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
      <div style={{ position: 'relative', width: size, height: size, flexShrink: 0 }}>
        <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}
          style={{ transform: 'rotate(-90deg)' }}>
          <circle cx={size / 2} cy={size / 2} r={r} fill="none"
            stroke="var(--border)" strokeWidth={stroke} />
          {total > 0 && arcs.filter(a => a.show).map(a => (
            <circle key={a.seg.key} cx={size / 2} cy={size / 2} r={r} fill="none"
              stroke={a.seg.color} strokeWidth={stroke} strokeLinecap="round"
              strokeDasharray={a.dash} strokeDashoffset={a.offset}
              style={{ transition: 'stroke-dasharray .6s ease' }} />
          ))}
        </svg>
        <div style={{
          position: 'absolute', inset: 0, display: 'flex', flexDirection: 'column',
          alignItems: 'center', justifyContent: 'center',
        }}>
          <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 22, fontWeight: 700 }}>
            {total > 0 ? total : '—'}
          </span>
          <span style={{ fontSize: 10, color: 'var(--text3)' }}>pods</span>
        </div>
      </div>
      <div style={{ display: 'grid', gap: 5 }}>
        {SEGMENTS.map(seg => (
          <div key={seg.key} style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12 }}>
            <span style={{ width: 9, height: 9, borderRadius: 2, background: seg.color, flexShrink: 0 }} />
            <span style={{ color: 'var(--text2)', minWidth: 58 }}>{seg.label}</span>
            <span className="mono" style={{ fontWeight: 600 }}>{counts[seg.key]}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
