// TraceMinimap — v0.10.278 (trace view Dilim 1d, docs/audit/trace-view.md §3.6).
// Uzun şelalenin tamamı tek bakışta: her satır bir ince çizgi (x = başlangıç/
// süre, y = satır sırası), renk servis (svcColor ile aynı hex paleti — canvas
// CSS var okuyamaz), görünür pencere accent çerçeve, tıkla → o satıra kaydır.
// Canvas: 5000+ satırda SVG düğümü yerine tek çizim. Yapışkan DEĞİL (ev
// kuralı: sayfa düzeyi floating şerit yok) — başlığın altında durur.
import { useEffect, useRef } from 'react';
import type { SpanRow } from '@/lib/types';

export function TraceMinimap({ spans, minT, totalNs, colorFor, range, onSeek, height = 40 }: {
  spans: SpanRow[];            // satır sırasında
  minT: number;
  totalNs: number;
  colorFor: (s: SpanRow) => string;
  /** görünür satır aralığı [ilk, son] (sanal modda); null = çerçeve yok */
  range: [number, number] | null;
  onSeek: (rowIndex: number) => void;
  height?: number;
}) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const cv = ref.current;
    if (!cv) return;
    const draw = () => {
      const w = cv.clientWidth || 600;
      const dpr = (typeof window !== 'undefined' && window.devicePixelRatio) || 1;
      cv.width = Math.round(w * dpr); cv.height = Math.round(height * dpr);
      const ctx = cv.getContext?.('2d');
      if (!ctx) return; // jsdom: canvas yok
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, height);
      const n = spans.length || 1;
      const rowH = height / n;
      for (let i = 0; i < spans.length; i++) {
        const s = spans[i];
        const x = ((s.startTime - minT) / totalNs) * w;
        const bw = Math.max(1, ((s.endTime - s.startTime) / totalNs) * w);
        ctx.fillStyle = colorFor(s);
        ctx.globalAlpha = 0.85;
        ctx.fillRect(x, i * rowH, bw, Math.max(1, rowH));
      }
      ctx.globalAlpha = 1;
      if (range) {
        const accent = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#388bfd';
        const y0 = (range[0] / n) * height, y1 = ((range[1] + 1) / n) * height;
        ctx.strokeStyle = accent; ctx.lineWidth = 1.5;
        ctx.strokeRect(0.75, y0 + 0.75, w - 1.5, Math.max(2, y1 - y0 - 1.5));
      }
    };
    draw();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(draw);
    ro.observe(cv);
    return () => ro.disconnect();
  }, [spans, minT, totalNs, colorFor, range, height]);

  return (
    <div className="tm-wrap" title="Trace haritası — tıkla: o satıra git" style={{ height }}
      onClick={e => {
        const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
        const y = e.clientY - r.top;
        onSeek(Math.max(0, Math.min(spans.length - 1, Math.floor((y / Math.max(1, r.height)) * spans.length))));
      }}>
      <canvas ref={ref} />
    </div>
  );
}
