import { MiniBar } from './MiniBar';
import type { ClusterNodeRow } from '@/lib/types';

// NodeHeatmap — node kullanım ısı-ızgarası (v0.9.32, design handoff
// "Node utilization heatmap"). Full-width, auto-fill minmax(150px).
// Her tile: role-dot (master --purple, worker --teal; role yoksa
// nötr — B4'te doldurulacak) + node adı (mono 11) + CPU/Mem
// mini-bar. clusterNodes verisini (mevcut) tüketir.
export function NodeHeatmap({ nodes }: { nodes: ClusterNodeRow[] }) {
  if (nodes.length === 0) return null;
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))',
      gap: 8,
    }}>
      {nodes.map(n => {
        const roleColor = n.role === 'master' || n.role === 'control-plane'
          ? 'var(--purple)'
          : n.role === 'worker'
            ? 'var(--teal)'
            : 'var(--text3)';
        return (
          <div key={n.node} style={{
            border: '1px solid var(--border)', borderRadius: 6,
            padding: '8px 10px', background: 'var(--bg1)',
            display: 'grid', gap: 6,
          }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span style={{ width: 6, height: 6, borderRadius: 2, background: roleColor, flexShrink: 0 }}
                title={n.role || 'node'} />
              <span style={{
                fontFamily: 'ui-monospace, monospace', fontSize: 11,
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={n.node}>{n.node}</span>
            </div>
            <div style={{ display: 'grid', gap: 4 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 10, color: 'var(--text3)' }}>
                <span style={{ width: 26 }}>CPU</span>
                <MiniBar pct={n.cpuPct ?? null} />
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 10, color: 'var(--text3)' }}>
                <span style={{ width: 26 }}>Mem</span>
                <MiniBar pct={n.memPct ?? null} />
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}
