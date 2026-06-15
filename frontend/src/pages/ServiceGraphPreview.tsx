import { useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useUrlRange } from '@/lib/useUrlRange';
import { ServiceGraph } from '@/components/ServiceGraph';
import type { NodeSizeMode, NodeSizeMetric } from '@/lib/topologyNodes';

// ServiceGraphPreview — the v0.8.12 Stage-2 scratch route (/servicegraph-preview)
// for comparing the new canonical OTel-native ServiceGraph against the old
// AggregateTopology/Topology views side by side. Removed in Stage 4. Nothing
// production points here yet (Stage 3 swaps the real surfaces).

const PRESETS = ['30m', '1h', '6h', '24h'] as const;

export default function ServiceGraphPreview() {
  const [range, setRange] = useUrlRange('1h');
  const [scope, setScope] = useState<'global' | 'neighborhood'>('global');
  const [focusInput, setFocusInput] = useState('');
  const [focus, setFocus] = useState('');
  const nav = useNavigate();

  // Node-size encoding (v0.8.x — Uptrace adapt, slice 3) lifted to the URL so a
  // global service-graph view is shareable: ?size=incoming|outgoing (default
  // outgoing) and ?metric=rate|duration (default rate). The toggles re-roll the
  // SAME fetched payload client-side — writing these params does NOT change
  // ServiceGraph's react-query key, so flipping them never refetches.
  const [params, setParams] = useSearchParams();
  const nodeSizeMode: NodeSizeMode = params.get('size') === 'incoming' ? 'incoming' : 'outgoing';
  const nodeSizeMetric: NodeSizeMetric = params.get('metric') === 'duration' ? 'duration' : 'rate';
  const onNodeSizeChange = (mode: NodeSizeMode, metric: NodeSizeMetric) => {
    const next = new URLSearchParams(params);
    // Defaults stay out of the URL so a shared link is clean; non-defaults persist.
    if (mode === 'outgoing') next.delete('size'); else next.set('size', mode);
    if (metric === 'rate') next.delete('metric'); else next.set('metric', metric);
    setParams(next, { replace: true });
  };

  const preset = range.preset ?? '';

  return (
    <div style={{ padding: 22 }}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginBottom: 14 }}>
        <h2 style={{ margin: 0, fontSize: 18 }}>Service Graph <span style={{ color: 'var(--text3)', fontWeight: 400, fontSize: 13 }}>· OTel-native preview (Stage 2)</span></h2>
      </div>

      <div className="controls" style={{ marginBottom: 12, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
        <div className="segmented">
          <button type="button" className={scope === 'global' ? 'active' : ''} onClick={() => setScope('global')}>Global</button>
          <button type="button" className={scope === 'neighborhood' ? 'active' : ''} onClick={() => setScope('neighborhood')}>Neighborhood</button>
        </div>
        {scope === 'neighborhood' && (
          <form onSubmit={e => { e.preventDefault(); setFocus(focusInput.trim()); }} style={{ display: 'flex', gap: 6 }}>
            <input className="field" placeholder="Focus service…" value={focusInput}
              onChange={e => setFocusInput(e.target.value)} style={{ width: 200 }} />
            <button type="submit" className="sec" style={{ fontSize: 12, padding: '4px 10px' }}>Focus</button>
          </form>
        )}
        <div className="segmented" style={{ marginLeft: 'auto' }}>
          {PRESETS.map(p => (
            <button key={p} type="button" className={preset === p ? 'active' : ''} onClick={() => setRange({ preset: p })}>{p}</button>
          ))}
        </div>
      </div>

      <ServiceGraph
        scope={scope}
        focus={scope === 'neighborhood' ? (focus || undefined) : undefined}
        range={range}
        height={640}
        onSelectService={svc => nav(`/service?service=${encodeURIComponent(svc)}`)}
        nodeSizeMode={nodeSizeMode}
        nodeSizeMetric={nodeSizeMetric}
        onNodeSizeChange={onNodeSizeChange}
      />
    </div>
  );
}
