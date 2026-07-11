import { MetricNamePicker } from '../MetricNamePicker';
import { Button } from '@/components/ui';
import { STEP_OPTIONS } from '@/pages/explore/presets';
import type {
  Panel, PanelType, PanelWidth,
  MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, GaugePanelConfig, MarkdownPanelConfig,
} from '@/lib/types';

const TYPE_LABELS: Record<PanelType, string> = {
  metric:     'Metric (line)',
  spanmetric: 'Span aggregation (line)',
  stat:       'Stat (single value)',
  // v0.6.19 — semicircle dial. Best for bounded metrics where
  // the operator wants the at-a-glance "where am I in the safe
  // / warning / breached bands". Same data source pattern as
  // Stat — point either at a metric_points key or a span agg.
  gauge:      'Gauge (semicircle dial)',
  markdown:   'Markdown / notes',
  row:        'Row (collapsible group)',
};

const WIDTH_LABELS: Record<PanelWidth, string> = {
  1: 'Quarter (1/4)',
  2: 'Half (2/4)',
  3: 'Three quarters (3/4)',
  4: 'Full row',
};

const SPAN_AGGS = ['count', 'rate', 'errors', 'error_rate', 'avg', 'sum', 'min', 'max',
                   'p50', 'p90', 'p95', 'p99', 'p999'];
const METRIC_AGGS = ['avg', 'sum', 'min', 'max', 'last', 'p50', 'p95', 'p99'];

// GRAN-C (v0.8.248) — shared step <select> for the metric / spanmetric forms.
// Same option list as Explore's Step picker (STEP_OPTIONS); the 0 entry is
// relabelled because dashboard "auto" now means width-aware (panel-pixel
// budget, PanelRenderer), not the backend's ~120-point ladder. Auto stores
// `undefined` (never 0) so a saved auto panel's JSON stays byte-identical to
// a pre-GRAN-C document — the backward-compat contract for old dashboards.
function StepSelect({ value, onChange }: {
  value: number | undefined;
  onChange: (v: number | undefined) => void;
}) {
  return (
    <select value={value ?? 0}
      onChange={e => {
        const v = Number(e.target.value);
        onChange(v > 0 ? v : undefined);
      }}>
      {STEP_OPTIONS.map(o => (
        <option key={o.v} value={o.v}>{o.v === 0 ? 'Auto (fit width)' : o.label}</option>
      ))}
    </select>
  );
}

// PanelEditor renders a form whose fields depend on panel.type. Pure
// controlled component — the parent owns the panel state and the save
// flow.
export function PanelEditor({ panel, onChange, onClose, onDelete }: {
  panel: Panel;
  onChange: (next: Panel) => void;
  onClose: () => void;
  onDelete: () => void;
}) {
  const update = <K extends keyof Panel>(k: K, v: Panel[K]) =>
    onChange({ ...panel, [k]: v });
  const updateConfig = (cfg: Panel['config']) =>
    onChange({ ...panel, config: cfg });

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'grid', placeItems: 'center', zIndex: 100,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 520, maxHeight: '90vh', overflow: 'auto',
        padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>Edit panel</div>

        <Field label="Title">
          <input value={panel.title}
            onChange={e => update('title', e.target.value)}
            style={{ width: '100%' }} />
        </Field>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <Field label="Type">
            <select value={panel.type}
              onChange={e => {
                const t = e.target.value as PanelType;
                onChange({ ...panel, type: t, config: defaultConfig(t) });
              }}>
              {(Object.keys(TYPE_LABELS) as PanelType[]).map(t =>
                <option key={t} value={t}>{TYPE_LABELS[t]}</option>)}
            </select>
          </Field>
          <Field label="Width">
            <select value={panel.width}
              onChange={e => update('width', Number(e.target.value) as PanelWidth)}>
              {([1, 2, 3, 4] as PanelWidth[]).map(w =>
                <option key={w} value={w}>{WIDTH_LABELS[w]}</option>)}
            </select>
          </Field>
        </div>

        {/* v0.6.20 — optional time-range override. "Default" =
            inherit from the dashboard's Topbar range. Any other
            preset locks this panel to its own window — Grafana-
            parity for "60-day baseline tile next to a 15min
            incident chart". Custom-range overrides are out of
            scope here; the seven canonical presets cover every
            real use case we've seen. */}
        <Field label="Time range override">
          <select
            value={panel.rangeOverride?.preset ?? ''}
            onChange={e => {
              const v = e.target.value;
              if (v === '') update('rangeOverride', undefined);
              else update('rangeOverride', { preset: v });
            }}>
            <option value="">Default (inherit dashboard range)</option>
            <option value="5m">Last 5 min</option>
            <option value="15m">Last 15 min</option>
            <option value="1h">Last 1 hour</option>
            <option value="6h">Last 6 hours</option>
            <option value="24h">Last 24 hours</option>
            <option value="7d">Last 7 days</option>
            <option value="30d">Last 30 days</option>
          </select>
        </Field>

        {panel.type === 'metric' && (
          <MetricFields cfg={panel.config as MetricPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'spanmetric' && (
          <SpanMetricFields cfg={panel.config as SpanMetricPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'stat' && (
          <StatFields cfg={panel.config as StatPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'gauge' && (
          <GaugeFields cfg={panel.config as GaugePanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'markdown' && (
          <Field label="Markdown text">
            <textarea
              value={(panel.config as MarkdownPanelConfig).text ?? ''}
              onChange={e => updateConfig({ text: e.target.value })}
              style={{ width: '100%', minHeight: 140, fontFamily: 'monospace', fontSize: 12 }} />
          </Field>
        )}

        <div style={{ display: 'flex', gap: 8, justifyContent: 'space-between', marginTop: 18 }}>
          <button type="button" className="sec" onClick={onDelete}
            style={{ color: 'var(--err)' }}>
            Delete panel
          </button>
          <button type="button" onClick={onClose}>Done</button>
        </div>
      </div>
    </div>
  );
}

function MetricFields({ cfg, onChange }: {
  cfg: MetricPanelConfig; onChange: (c: MetricPanelConfig) => void;
}) {
  // v0.5.198 — swapped the eager api.metricNames('') load for the
  // server-side MetricNamePicker. At 10k+ metric installs the full
  // catalogue blew up panel-editor TTFI; debounced search keeps
  // the picker usable.
  const update = <K extends keyof MetricPanelConfig>(k: K, v: MetricPanelConfig[K]) =>
    onChange({ ...cfg, [k]: v });
  return (
    <>
      <Field label="Metric name">
        <MetricNamePicker service="" value={cfg.metricName ?? ''}
          onChange={v => update('metricName', v)}
          placeholder="search metrics…" width="100%" />
      </Field>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12 }}>
        <Field label="Aggregation">
          <select value={cfg.agg ?? 'avg'} onChange={e => update('agg', e.target.value)}>
            {METRIC_AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </Field>
        <Field label="Service (optional)">
          <input value={cfg.service ?? ''}
            onChange={e => update('service', e.target.value)} />
        </Field>
        <Field label="Step">
          <StepSelect value={cfg.step} onChange={v => update('step', v)} />
        </Field>
      </div>
      <Field label="Group by (comma-sep keys, optional)">
        <input value={cfg.groupBy ?? ''}
          onChange={e => update('groupBy', e.target.value)} style={{ width: '100%' }} />
      </Field>
    </>
  );
}

function SpanMetricFields({ cfg, onChange }: {
  cfg: SpanMetricPanelConfig; onChange: (c: SpanMetricPanelConfig) => void;
}) {
  const update = <K extends keyof SpanMetricPanelConfig>(k: K, v: SpanMetricPanelConfig[K]) =>
    onChange({ ...cfg, [k]: v });
  return (
    <>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12 }}>
        <Field label="Aggregation">
          <select value={cfg.agg ?? 'count'} onChange={e => update('agg', e.target.value)}>
            {SPAN_AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </Field>
        <Field label="Field (for percentiles)">
          <input value={cfg.field ?? 'duration_ms'} placeholder="duration_ms"
            onChange={e => update('field', e.target.value)} />
        </Field>
        <Field label="Visualization">
          <select value={cfg.viz ?? 'line'}
            onChange={e => update('viz', e.target.value as SpanMetricPanelConfig['viz'])}>
            <option value="line">Line</option>
            <option value="bar">Bar</option>
            <option value="stacked-bar">Stacked bar</option>
            <option value="area">Area</option>
            <option value="stacked-area">Stacked area</option>
          </select>
        </Field>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: 12 }}>
        <Field label="Group by (comma-sep keys)">
          <input value={cfg.groupBy ?? ''} placeholder="service_name, http_route"
            onChange={e => update('groupBy', e.target.value)} style={{ width: '100%' }} />
        </Field>
        <Field label="Step">
          <StepSelect value={cfg.step} onChange={v => update('step', v)} />
        </Field>
      </div>
      <Field label="DSL filter (optional)">
        <textarea value={cfg.dsl ?? ''}
          placeholder='service_name = "checkout"\nduration > 100ms'
          onChange={e => update('dsl', e.target.value)}
          style={{ width: '100%', minHeight: 70, fontFamily: 'monospace', fontSize: 12 }} />
      </Field>
    </>
  );
}

function StatFields({ cfg, onChange }: {
  cfg: StatPanelConfig; onChange: (c: StatPanelConfig) => void;
}) {
  return (
    <>
      <Field label="Source">
        <select value={cfg.source ?? 'spanmetric'}
          onChange={e => onChange({ ...cfg, source: e.target.value as 'metric' | 'spanmetric' })}>
          <option value="spanmetric">Span aggregation</option>
          <option value="metric">Metric query</option>
        </select>
      </Field>
      {cfg.source === 'spanmetric' && (
        <SpanMetricFields cfg={cfg.span ?? { agg: 'count' }}
          onChange={c => onChange({ ...cfg, span: c })} />
      )}
      {cfg.source === 'metric' && (
        <MetricFields cfg={cfg.metric ?? { metricName: '' }}
          onChange={c => onChange({ ...cfg, metric: c })} />
      )}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
        <Field label="Unit suffix">
          <input value={cfg.unit ?? ''} placeholder="ms / % / rps"
            onChange={e => onChange({ ...cfg, unit: e.target.value })} />
        </Field>
        <Field label="Decimals">
          <input type="number" min={0} max={6} value={cfg.decimals ?? 2}
            onChange={e => onChange({ ...cfg, decimals: parseInt(e.target.value || '0') })} />
        </Field>
      </div>
      {/* v0.5.486 — threshold colouring controls. */}
      <Field label="Threshold colour mode">
        <select value={cfg.colorMode ?? 'none'}
          onChange={e => onChange({ ...cfg, colorMode: e.target.value as 'none' | 'value' | 'background' })}>
          <option value="none">None (delta direction only)</option>
          <option value="value">Tint the number</option>
          <option value="background">Tint the panel background</option>
        </select>
      </Field>
      {(cfg.colorMode === 'value' || cfg.colorMode === 'background') && (
        <Field label="Threshold bands">
          <ThresholdEditor
            thresholds={cfg.thresholds ?? []}
            onChange={t => onChange({ ...cfg, thresholds: t })} />
        </Field>
      )}
    </>
  );
}

// v0.6.19 — Gauge panel editor. Shares the source/span/metric
// fields with Stat (they read the same data); adds min/max
// bounds + a required threshold list. Always renders the
// threshold editor (no colorMode toggle — the gauge IS its
// threshold visualisation).
function GaugeFields({ cfg, onChange }: {
  cfg: GaugePanelConfig; onChange: (c: GaugePanelConfig) => void;
}) {
  return (
    <>
      <Field label="Source">
        <select value={cfg.source ?? 'spanmetric'}
          onChange={e => onChange({ ...cfg, source: e.target.value as 'metric' | 'spanmetric' })}>
          <option value="spanmetric">Span aggregation</option>
          <option value="metric">Metric query</option>
        </select>
      </Field>
      {cfg.source === 'spanmetric' && (
        <SpanMetricFields cfg={cfg.span ?? { agg: 'count' }}
          onChange={c => onChange({ ...cfg, span: c })} />
      )}
      {cfg.source === 'metric' && (
        <MetricFields cfg={cfg.metric ?? { metricName: '' }}
          onChange={c => onChange({ ...cfg, metric: c })} />
      )}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12 }}>
        <Field label="Min">
          <input type="number" value={cfg.min ?? 0}
            onChange={e => onChange({ ...cfg, min: parseFloat(e.target.value || '0') })} />
        </Field>
        <Field label="Max">
          <input type="number" value={cfg.max ?? 100}
            onChange={e => onChange({ ...cfg, max: parseFloat(e.target.value || '0') })} />
        </Field>
        <Field label="Unit suffix">
          <input value={cfg.unit ?? ''} placeholder="% / ms / rps"
            onChange={e => onChange({ ...cfg, unit: e.target.value })} />
        </Field>
        <Field label="Decimals">
          <input type="number" min={0} max={6} value={cfg.decimals ?? 1}
            onChange={e => onChange({ ...cfg, decimals: parseInt(e.target.value || '0') })} />
        </Field>
      </div>
      <Field label="Threshold zones">
        <ThresholdEditor
          thresholds={cfg.thresholds ?? []}
          onChange={t => onChange({ ...cfg, thresholds: t })} />
      </Field>
    </>
  );
}

// v0.5.486 — small inline editor for the threshold steps. Three
// fixed colour bands (green / amber / red) cover the Grafana
// shape; operators tweak the value floors and Coremetry picks
// the highest band ≤ current value at render time.
function ThresholdEditor({ thresholds, onChange }: {
  thresholds: { value: number; color: 'green' | 'amber' | 'red' }[];
  onChange: (t: { value: number; color: 'green' | 'amber' | 'red' }[]) => void;
}) {
  const sorted = [...thresholds].sort((a, b) => a.value - b.value);
  const setRow = (i: number, value: number) => {
    const next = sorted.slice();
    next[i] = { ...next[i], value };
    onChange(next);
  };
  const removeRow = (i: number) => {
    const next = sorted.slice();
    next.splice(i, 1);
    onChange(next);
  };
  const addRow = (color: 'green' | 'amber' | 'red') => {
    const last = sorted[sorted.length - 1];
    const value = last ? last.value + 10 : 0;
    onChange([...sorted, { value, color }]);
  };
  const PALETTE: Record<'green' | 'amber' | 'red', string> = {
    green: 'rgb(46,160,67)', amber: 'rgb(217,119,6)', red: 'rgb(220,38,38)',
  };
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      {sorted.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          No thresholds set — add at least one to colour the panel.
          Convention: green &lt; amber &lt; red.
        </div>
      )}
      {sorted.map((t, i) => (
        <div key={i} style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 12 }}>
          <span style={{
            width: 14, height: 14, borderRadius: 3,
            background: PALETTE[t.color], flexShrink: 0,
          }} />
          <span style={{ color: 'var(--text2)', minWidth: 50 }}>{t.color}</span>
          <span style={{ color: 'var(--text3)' }}>≥</span>
          <input type="number" value={t.value}
            onChange={e => setRow(i, parseFloat(e.target.value || '0'))}
            style={{ width: 100, fontSize: 12 }} />
          <Button variant="secondary" size="sm"
            onClick={() => removeRow(i)}>×</Button>
        </div>
      ))}
      <div style={{ display: 'flex', gap: 6, marginTop: 4 }}>
        <Button variant="secondary" size="sm" onClick={() => addRow('green')}>+ green</Button>
        <Button variant="secondary" size="sm" onClick={() => addRow('amber')}>+ amber</Button>
        <Button variant="secondary" size="sm" onClick={() => addRow('red')}>+ red</Button>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 12 }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

export function defaultConfig(t: PanelType): Panel['config'] {
  switch (t) {
    case 'metric':     return { metricName: '', agg: 'avg' };
    case 'spanmetric': return { agg: 'count' };
    case 'stat':       return { source: 'spanmetric', span: { agg: 'count' }, decimals: 0 };
    // v0.6.19 — gauge defaults to span-source 'error_rate' from
    // 0–100% with a sensible 80% amber / 95% red band. Operator
    // tweaks via PanelEditor.
    case 'gauge':      return {
      source: 'spanmetric',
      span: { agg: 'error_rate' },
      unit: '%',
      decimals: 1,
      min: 0,
      max: 100,
      thresholds: [
        { value: 80, color: 'amber' },
        { value: 95, color: 'red' },
      ],
    };
    case 'markdown':   return { text: '## Notes\n\nDescribe what this dashboard shows.' };
    // Row panels carry no config of their own — title is on the panel
    // itself, default-collapsed is opt-in.
    case 'row':        return { collapsed: false };
  }
}
