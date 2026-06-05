import { useState } from 'react';
import { usePerfStats } from '@/lib/perf/useFpsMeter';

// PerfMeter — DEV-ONLY floating perf HUD (v0.8.6 Phase 0). Live FPS + long-task
// count + Core Web Vitals (LCP/CLS/INP) measured against the perf budget
// (TTI<2s, interaction<100ms, 60fps scroll). Returns null in production builds
// (import.meta.env.DEV) so it's tree-shaken out. Click to collapse to a dot.
// Tokens only — no raw hex.
export function PerfMeter() {
  const dev = import.meta.env.DEV;
  const s = usePerfStats(dev);
  const [open, setOpen] = useState(false);
  if (!dev) return null;

  const fpsColor = s.fps >= 55 ? 'var(--ok)' : s.fps >= 30 ? 'var(--warn)' : 'var(--err)';
  const box: React.CSSProperties = {
    position: 'fixed', bottom: 8, left: 8, zIndex: 99999,
    font: '11px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace',
    background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 6,
    color: 'var(--text2)', boxShadow: '0 1px 2px rgba(20,30,45,.12)',
    cursor: 'pointer', userSelect: 'none',
  };

  if (!open) {
    return (
      <div style={{ ...box, padding: '3px 7px' }} onClick={() => setOpen(true)} title="Perf meter (dev)">
        <span style={{ color: fpsColor, fontWeight: 700 }}>{s.fps}</span>
        <span style={{ color: 'var(--text3)' }}> fps</span>
      </div>
    );
  }

  const row = (label: string, value: string, color?: string) => (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 14 }}>
      <span style={{ color: 'var(--text3)' }}>{label}</span>
      <span style={{ color: color ?? 'var(--text)', fontWeight: 600, fontVariantNumeric: 'tabular-nums' }}>{value}</span>
    </div>
  );

  return (
    <div style={{ ...box, padding: '7px 9px', minWidth: 134 }} onClick={() => setOpen(false)} title="Click to collapse">
      <div style={{ color: 'var(--text)', fontWeight: 700, marginBottom: 4, letterSpacing: '.04em' }}>PERF · dev</div>
      {row('FPS', String(s.fps), fpsColor)}
      {row('Long tasks', String(s.longTasks), s.longTasks > 0 ? 'var(--warn)' : undefined)}
      {row('LCP', s.lcp != null ? `${s.lcp} ms` : '—', s.lcp != null && s.lcp > 2000 ? 'var(--err)' : 'var(--ok)')}
      {row('CLS', s.cls != null ? s.cls.toFixed(3) : '—', s.cls != null && s.cls > 0.1 ? 'var(--warn)' : 'var(--ok)')}
      {row('INP', s.inp != null ? `${s.inp} ms` : '—', s.inp != null && s.inp > 100 ? 'var(--err)' : 'var(--ok)')}
    </div>
  );
}
