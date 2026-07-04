export { fmtBytes } from '@/lib/utils';

// Shared atoms for the /admin/stats surface (split out of the
// 867-line AdminStats.tsx — frontend refactor batch item 2,
// v0.8.269). Pure presentation + formatters; no data fetching.

export function SectionHeader({ title, sub }: { title: string; sub?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, marginBottom: 10 }}>
      <h2 style={{ margin: 0, fontSize: 16, color: 'var(--text)' }}>{title}</h2>
      {sub && <span style={{ fontSize: 11, color: 'var(--text3)' }}>{sub}</span>}
    </div>
  );
}

export function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: 12, border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg2)',
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
      }}>{label}</div>
      <div className={cls} style={{ fontSize: 20, fontWeight: 700, marginTop: 4 }}>{value}</div>
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
          {sub}
        </div>
      )}
    </div>
  );
}

export function fmtUptime(sec: number): string {
  if (!sec || sec < 0) return '—';
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}


export function fmtRate(perSec: number): string {
  if (!perSec || perSec < 0) return '0 /s';
  if (perSec >= 1000) return `${(perSec / 1000).toFixed(1)}k /s`;
  if (perSec >= 1) return `${perSec.toFixed(0)} /s`;
  return `${perSec.toFixed(2)} /s`;
}
