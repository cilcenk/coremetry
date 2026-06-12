import { EXPLORE_VIZ, type ExploreViz } from './model';

// VizRail — the builder's viz mode picker (explore-v2 Phase 2).
// line / area / bars render on TimeSeriesPanel; heatmap keeps the
// LatencyHeatmap path (query A). toplist / stat / table land in Phase 4.

const VIZ_META: Record<ExploreViz, { icon: string; label: string; hint: string }> = {
  line:    { icon: '∿', label: 'Line',    hint: 'One line per series' },
  area:    { icon: '◪', label: 'Area',    hint: 'Filled line — good for rates' },
  bars:    { icon: '▮', label: 'Bars',    hint: 'Per-bucket bars — good for counts' },
  heatmap: { icon: '▦', label: 'Heatmap', hint: 'Latency density (time × log-duration) — uses query A' },
};

export function VizRail({ value, onChange }: {
  value: ExploreViz;
  onChange: (v: ExploreViz) => void;
}) {
  return (
    <div className="segmented">
      {EXPLORE_VIZ.map(v => (
        <button key={v} type="button" title={VIZ_META[v].hint}
          className={value === v ? 'active' : ''}
          onClick={() => onChange(v)}>
          {VIZ_META[v].icon} {VIZ_META[v].label}
        </button>
      ))}
    </div>
  );
}
