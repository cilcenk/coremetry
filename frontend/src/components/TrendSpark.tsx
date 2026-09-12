import { useMemo, useState } from 'react';
import { downsampleBuckets, maxBarsForWidth, barIndexAt, sparkRenderMode } from '@/lib/sparkline';

// TrendSpark — v0.10.697 (operatör: Operations tablosundaki üç mikro sparkline
// "kullanışsız"; mockup B onaylı). Tek geniş trend grafiği: çubuklar çağrı
// hacmi, çubuğun kırmızı kısmı hata payı (aynı ölçek), üstte p99 çizgisi
// (kendi ölçeği), sağ uçta son değer noktası; hover'da kova değerleri.
// Sparkline.tsx'in saf yardımcıları (downsample / genişlik bütçesi / kova
// indeksi) — ikinci geometri yazımı yok. Chart kütüphanesi yok: tablo içi
// mini grafik, Sparkline emsali.
export function TrendSpark({ calls, errors, p99, width = 160, height = 30, className }: {
  calls: number[];
  errors?: number[];
  p99?: number[];
  width?: number;
  height?: number;
  className?: string;
}) {
  const n = Math.min(maxBarsForWidth(width, 3), Math.max(calls.length, 1));
  const c = useMemo(() => downsampleBuckets(calls, n, 'sum'), [calls, n]);
  const e = useMemo(() => downsampleBuckets(errors ?? [], n, 'sum'), [errors, n]);
  const p = useMemo(() => downsampleBuckets(p99 ?? [], n, 'max'), [p99, n]);
  const [hover, setHover] = useState<number | null>(null);
  if (sparkRenderMode(calls) === 'nodata') {
    return <span className={className} style={{ display: 'inline-block', width, height, lineHeight: `${height}px`, textAlign: 'center', color: 'var(--text3)', fontSize: 11 }}>—</span>;
  }
  const N = c.length;
  const bw = width / N;
  const maxC = Math.max(1, ...c.map(v => v ?? 0));
  const maxP = Math.max(1, ...p.map(v => v ?? 0));
  const inner = height - 4;
  const pts: string[] = [];
  let last: [number, number] | null = null;
  p.forEach((v, i) => {
    if (v == null) return;
    const x = i * bw + bw / 2;
    const y = height - 2 - (v / maxP) * (inner - 2);
    pts.push(`${x.toFixed(1)},${y.toFixed(1)}`);
    last = [x, y];
  });
  const hv = hover != null && hover < N ? { calls: c[hover] ?? 0, errors: e[hover] ?? 0, p99: p[hover] } : null;
  return (
    <span className={`trend-spark${className ? ' ' + className : ''}`} style={{ width, height }}>
      <svg width={width} height={height} role="img" aria-label="çağrı · hata · p99 trendi"
        onMouseMove={ev => {
          const r = ev.currentTarget.getBoundingClientRect();
          setHover(barIndexAt(ev.clientX - r.left, r.width, N));
        }}
        onMouseLeave={() => setHover(null)}>
        {c.map((v, i) => {
          const h = v ? Math.max(1, (v / maxC) * inner) : 0;
          const ev = e[i] ?? 0;
          const eh = ev > 0 ? Math.max(1, (ev / maxC) * inner) : 0;
          const x = i * bw + 0.5;
          return (
            <g key={i}>
              {h > 0 && <rect className="ts-bar" x={x} y={height - h} width={Math.max(0.5, bw - 1)} height={h} />}
              {eh > 0 && <rect className="ts-err" x={x} y={height - eh} width={Math.max(0.5, bw - 1)} height={eh} />}
            </g>
          );
        })}
        {pts.length > 1 && <polyline className="ts-p99" points={pts.join(' ')} />}
        {last && <circle className="ts-cur" cx={last[0]} cy={last[1]} r={2} />}
      </svg>
      {hv && (
        <span className="trend-spark__tt mono" role="status">
          kova {hover! + 1}/{N} · calls {Math.round(hv.calls).toLocaleString()} · errors {Math.round(hv.errors)}{hv.p99 != null ? ` · p99 ${Math.round(hv.p99)} ms` : ''}
        </span>
      )}
    </span>
  );
}
