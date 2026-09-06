import { UnitSelect } from './UnitSelect'; // v0.10.506 (D9)
import { MetricNamePicker } from '../MetricNamePicker';
import { ActionRow, Button } from '@/components/ui';
import { STEP_OPTIONS } from '@/pages/explore/presets';
import type {
  Panel, PanelType, PanelWidth, PanelHeight,
  MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, GaugePanelConfig, MarkdownPanelConfig,
  HeatmapPanelConfig, PromqlPanelConfig, TopNPanelConfig,
} from '@/lib/types';
// v0.9.781 — ONE limit ceiling. The editor's options and the panel's slice
// read the same clamp, so the dropdown can never offer more rows than the
// server's 50-series trim can actually deliver.
import { TOPN_SERVER_CAP } from './topN';
// v0.9.778 — ONE band palette. This file used to keep its own PALETTE copy of
// the same rgb() literals as PanelRenderer, so the editor swatch and the
// rendered panel were two independent sources that could (and did) drift.
import { THRESHOLD_COLOURS } from './panelChrome';

const TYPE_LABELS: Record<PanelType, string> = {
  metric:     'Metric (line)',
  spanmetric: 'Span aggregation (line)',
  stat:       'Stat (single value)',
  // v0.6.19 — semicircle dial. Best for bounded metrics where
  // the operator wants the at-a-glance "where am I in the safe
  // / warning / breached bands". Same data source pattern as
  // Stat — point either at a metric_points key or a span agg.
  gauge:      'Gauge (semicircle dial)',
  // v0.9.109 (C2) — time×bucket latency density for a histogram metric.
  // Reuses the LatencyHeatmap viz + /api/metrics/histogram (the F3 machine).
  heatmap:    'Heatmap (latency density)',
  // v0.9.117 (F4) — a chart driven by a raw PromQL query.
  // v0.9.1157 — "/ MetricsQL": on a VictoriaMetrics install the query goes
  // to VM VERBATIM (no pre-validating parser), so MetricsQL extensions
  // work. Label text only — the field, the endpoint and the ClickHouse
  // path are unchanged.
  promql:     'PromQL / MetricsQL query',
  // v0.9.781 — ranked bars over the whole window (Datadog "Top List").
  topn:       'Top-N bar',
  markdown:   'Markdown / notes',
  row:        'Row (collapsible group)',
};

const WIDTH_LABELS: Record<PanelWidth, string> = {
  1: 'Quarter (1/4)',
  2: 'Half (2/4)',
  3: 'Three quarters (3/4)',
  4: 'Full row',
};

// v0.9.778 — panel body height. Grafana lets you drag any pixel value; three
// rungs keep a dashboard reading as a grid instead of a ragged wall, and the
// grid's row-stretch does the rest (a tall panel lifts its whole row).
const HEIGHT_LABELS: Record<PanelHeight, string> = {
  s: 'Short (S)',
  m: 'Regular (M)',
  l: 'Tall (L)',
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
  // Height is meaningless for the two types that have no fixed body: a row is
  // a layout marker, markdown flows with its text.
  const showHeight = panel.type !== 'row' && panel.type !== 'markdown';

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'grid', placeItems: 'center', zIndex: 'var(--z-modal)',
    }}>
      {/* v0.9.988 (D6.2) — görüntü alanına kelepçeli genişlik; masaüstü
          (520 < 100vw-32) değişmiyor. */}
      <div onClick={e => e.stopPropagation()} style={{
        width: 'min(520px, calc(100vw - 32px))', maxHeight: '90vh', overflow: 'auto',
        padding: 24, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 14 }}>Edit panel</div>

        <Field label="Title">
          <input value={panel.title}
            onChange={e => update('title', e.target.value)}
            style={{ width: '100%' }} />
        </Field>

        {/* v0.9.773 — optional note surfaced as a hoverable ⓘ next to the
            panel title on the dashboard. Blank string is normalized back to
            undefined so clearing the box removes the field rather than
            saving an empty one (which would render a dot with no tooltip). */}
        <Field label="Description (optional — shown as ⓘ on the panel)">
          <input value={panel.description ?? ''}
            placeholder="What this panel measures, and when it matters"
            onChange={e => update('description', e.target.value.trim() ? e.target.value : undefined)}
            style={{ width: '100%' }} />
        </Field>

        {/* v0.9.989 (D8) — koşullu kolon sayısı satır içi değerden
            KOŞULLU SINIFA döndü; ikisi de eşit kesirli olduğu için
            paylaşılan ızgara sınıfları birebir karşılıyor ve dar ekran
            daraltmasını da beraberinde getiriyor. */}
        <div className={showHeight ? 'grid-3' : 'grid-2'} style={{ display: 'grid', gap: 12 }}>
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
          {showHeight && (
            <Field label="Height">
              {/* 'm' stores UNDEFINED, not the literal — the StepSelect
                  contract (v0.8.248): the default must never write a field
                  into the saved document, so a dashboard the operator only
                  opened and closed round-trips byte-identical. */}
              <select value={panel.height ?? 'm'}
                onChange={e => {
                  const h = e.target.value as PanelHeight;
                  update('height', h === 'm' ? undefined : h);
                }}>
                {(Object.keys(HEIGHT_LABELS) as PanelHeight[]).map(h =>
                  <option key={h} value={h}>{HEIGHT_LABELS[h]}</option>)}
              </select>
            </Field>
          )}
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
        {panel.type === 'heatmap' && (
          <HeatmapFields cfg={panel.config as HeatmapPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'promql' && (
          <PromqlFields cfg={panel.config as PromqlPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'topn' && (
          <TopNFields cfg={panel.config as TopNPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'markdown' && (
          <Field label="Markdown text">
            <textarea
              value={(panel.config as MarkdownPanelConfig).text ?? ''}
              onChange={e => updateConfig({ text: e.target.value })}
              style={{ width: '100%', minHeight: 140, fontFamily: 'monospace', fontSize: 12 }} />
          </Field>
        )}

        {/* v0.9.1007 (M5) — elle `space-between` yerine ActionRow'un
            `destructive` + `confirm` yuvaları; aynı görsel sonuç, ama
            sıra artık sözleşme. "Done" çıplak bir <button>du (atom
            baypası): varyantsız ham element `button {}` element-
            seviyesi kuralına düşüyor, yani DOLU MAVİ oluyordu. */}
        <ActionRow
          destructive={<Button variant="ghost-danger" onClick={onDelete}>Delete panel</Button>}
          confirm={<Button variant="primary" onClick={onClose}>Done</Button>} />
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
  // v0.9.121 (F4) — Builder ↔ PromQL toggle. PromQL mode when cfg.promql is
  // defined (even ''); switching to Builder clears it.
  const promqlMode = cfg.promql !== undefined;
  return (
    <>
      <div className="segmented" style={{ marginBottom: 8 }}>
        <button type="button" className={!promqlMode ? 'active' : ''}
          onClick={() => update('promql', undefined)}>Builder</button>
        <button type="button" className={promqlMode ? 'active' : ''}
          onClick={() => { if (cfg.promql === undefined) update('promql', ''); }}>PromQL / MetricsQL</button>
      </div>
      {promqlMode ? (
        <Field label="PromQL / MetricsQL query">
          <textarea value={cfg.promql ?? ''} spellCheck={false}
            onChange={e => update('promql', e.target.value)}
            rows={3}
            style={{ width: '100%', fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 12 }}
            placeholder={'sum by (service.name) (rate(http.server.duration[5m]))'} />
        </Field>
      ) : (
        <>
          <Field label="Metric name">
            <MetricNamePicker service="" value={cfg.metricName ?? ''}
              onChange={v => update('metricName', v)}
              placeholder="search metrics…" width="100%" />
          </Field>
          <div className="grid-3" style={{ display: 'grid', gap: 12 }}>
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
      )}
      {/* v0.9.790 — mark seçimi (SpanMetricFields'in ikizi). Builder/PromQL
          koşulunun DIŞINDA duruyor çünkü MetricPanel'in çizim dalı iki modda
          da aynı: viz'i yalnız builder'a koymak, PromQL'e geçen operatörün
          config'inde okunan ama düzenlenemeyen bir alan bırakırdı. */}
      <div className="grid-3" style={{ display: 'grid', gap: 12 }}>
        <Field label="Visualization">
          <select value={cfg.viz ?? 'line'}
            onChange={e => update('viz', e.target.value as MetricPanelConfig['viz'])}>
            <option value="line">Line</option>
            <option value="bar">Bar</option>
            <option value="stacked-bar">Stacked bar</option>
            <option value="area">Area</option>
            <option value="stacked-area">Stacked area</option>
          </select>
        </Field>
      </div>
      {/* v0.10.314 (chart-layer Dilim 2.2) — yatay eşik çizgileri; stat/gauge
          ile AYNI editör ve config şekli. Çizim tek kapıdan (thresholdLines):
          amber = warn, red = err çizgi; green taban, çizgisiz. */}
      <Field label="Threshold lines (amber = warn, red = err; green = base, no line)">
        <ThresholdEditor
          thresholds={cfg.thresholds ?? []}
          onChange={t => update('thresholds', t)} />
      </Field>
    </>
  );
}

// v0.9.109 (C2) — Heatmap panel editor. A histogram metric + optional
// service/unit/step. No agg/groupBy: a heatmap renders the whole bucket
// distribution over time (global), not a reduced series.
function HeatmapFields({ cfg, onChange }: {
  cfg: HeatmapPanelConfig; onChange: (c: HeatmapPanelConfig) => void;
}) {
  const update = <K extends keyof HeatmapPanelConfig>(k: K, v: HeatmapPanelConfig[K]) =>
    onChange({ ...cfg, [k]: v });
  return (
    <>
      <Field label="Histogram metric name">
        <MetricNamePicker service="" value={cfg.metricName ?? ''}
          onChange={v => update('metricName', v)}
          placeholder="search histogram metrics…" width="100%" />
      </Field>
      <div className="grid-3" style={{ display: 'grid', gap: 12 }}>
        <Field label="Service (optional)">
          <input value={cfg.service ?? ''}
            onChange={e => update('service', e.target.value)} />
        </Field>
        <Field label="Bounds unit (y-axis)">
          <select value={cfg.unit ?? 'ms'} onChange={e => update('unit', e.target.value)}>
            <option value="ms">ms (bounds already ms)</option>
            <option value="s">s (bounds in seconds → ms)</option>
          </select>
        </Field>
        <Field label="Step">
          <StepSelect value={cfg.step} onChange={v => update('step', v)} />
        </Field>
      </div>
    </>
  );
}

// v0.9.117 (F4) — PromQL panel editor: a query textarea + optional unit/step.
function PromqlFields({ cfg, onChange }: {
  cfg: PromqlPanelConfig; onChange: (c: PromqlPanelConfig) => void;
}) {
  const update = <K extends keyof PromqlPanelConfig>(k: K, v: PromqlPanelConfig[K]) =>
    onChange({ ...cfg, [k]: v });
  return (
    <>
      <Field label="PromQL / MetricsQL query">
        <textarea value={cfg.query ?? ''} spellCheck={false}
          onChange={e => update('query', e.target.value)}
          rows={3}
          style={{ width: '100%', fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 12 }}
          placeholder={'sum by (service.name) (rate(http.server.duration[5m]))'} />
      </Field>
      <div className="grid-3" style={{ display: 'grid', gap: 12 }}>
        <Field label="Viz">
          <select value={cfg.viz ?? 'line'}
            onChange={e => update('viz', e.target.value as PromqlPanelConfig['viz'])}>
            <option value="line">Line</option>
            <option value="area">Area</option>
            <option value="bar">Bar</option>
            <option value="stacked-bar">Stacked bar</option>
            <option value="stacked-area">Stacked area</option>
          </select>
        </Field>
        <Field label="Unit (optional)">
          <UnitSelect value={cfg.unit} onChange={v => update('unit', v)} />
        </Field>
        <Field label="Step">
          <StepSelect value={cfg.step} onChange={v => update('step', v)} />
        </Field>
      </div>
      {/* v0.10.314 (chart-layer Dilim 2.2) — yatay eşik çizgileri; stat/gauge
          ile AYNI editör ve config şekli. Çizim tek kapıdan (thresholdLines):
          amber = warn, red = err çizgi; green taban, çizgisiz. */}
      <Field label="Threshold lines (amber = warn, red = err; green = base, no line)">
        <ThresholdEditor
          thresholds={cfg.thresholds ?? []}
          onChange={t => update('thresholds', t)} />
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
      <div className="grid-3" style={{ display: 'grid', gap: 12 }}>
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
      {/* v0.10.314 (chart-layer Dilim 2.2) — yatay eşik çizgileri; stat/gauge
          ile AYNI editör ve config şekli. Çizim tek kapıdan (thresholdLines):
          amber = warn, red = err çizgi; green taban, çizgisiz. */}
      <Field label="Threshold lines (amber = warn, red = err; green = base, no line)">
        <ThresholdEditor
          thresholds={cfg.thresholds ?? []}
          onChange={t => update('thresholds', t)} />
      </Field>
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

// v0.9.781 — Top-N bar fields. Same span-query vocabulary as SpanMetricFields
// (agg / field / group-by / DSL) minus everything time-series-shaped: no step
// (the panel derives a single-bucket step from the window itself — topN.ts)
// and no viz (a Top-N panel IS the bar list).
function TopNFields({ cfg, onChange }: {
  cfg: TopNPanelConfig; onChange: (c: TopNPanelConfig) => void;
}) {
  const update = <K extends keyof TopNPanelConfig>(k: K, v: TopNPanelConfig[K]) =>
    onChange({ ...cfg, [k]: v });
  return (
    <>
      <div className="grid-3" style={{ display: 'grid', gap: 12 }}>
        <Field label="Aggregation">
          <select value={cfg.agg ?? 'p99'} onChange={e => update('agg', e.target.value)}>
            {SPAN_AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </Field>
        <Field label="Field (for percentiles)">
          <input value={cfg.field ?? 'duration_ms'} placeholder="duration_ms"
            onChange={e => update('field', e.target.value)} />
        </Field>
        {/* Capped at the server's own trim — offering 100 would render a
            "top 100" that is really a top 50 with a missing tail. */}
        <Field label="Rows">
          <select value={cfg.limit ?? 10}
            onChange={e => update('limit', Number(e.target.value))}>
            {[5, 10, 20, TOPN_SERVER_CAP].map(n =>
              <option key={n} value={n}>{`Top ${n}`}</option>)}
          </select>
        </Field>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr', gap: 12 }}>
        {/* Dotted semconv spelling: `http.route` resolves to the indexed
            http_route column, while the underscore form falls through to the
            attribute-array lookup and returns ONE empty group (verified
            against live ClickHouse, v0.9.781). */}
        <Field label="Group by (comma-sep keys — these ARE the bars)">
          <input value={cfg.groupBy ?? ''} placeholder="service.name, http.route"
            onChange={e => update('groupBy', e.target.value)} style={{ width: '100%' }} />
        </Field>
        <Field label="Unit">
          <UnitSelect value={cfg.unit} onChange={v => update('unit', v)} />
        </Field>
      </div>
      <Field label="Row click">
        <select value={cfg.linkTo ?? 'none'}
          onChange={e => update('linkTo', e.target.value as TopNPanelConfig['linkTo'])}>
          <option value="none">Not clickable</option>
          <option value="service">Service detail (first group-by key must be a service)</option>
          <option value="traces">Traces, filtered to this row</option>
        </select>
      </Field>
      <Field label="DSL filter (optional)">
        <textarea value={cfg.dsl ?? ''}
          placeholder='service.name = "checkout"\nduration > 100ms'
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
      <div className="grid-2" style={{ display: 'grid', gap: 12 }}>
        <Field label="Unit suffix">
          <UnitSelect value={cfg.unit} onChange={v => onChange({ ...cfg, unit: v })} />
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
      <div className="grid-4" style={{ display: 'grid', gap: 12 }}>
        <Field label="Min">
          <input type="number" value={cfg.min ?? 0}
            onChange={e => onChange({ ...cfg, min: parseFloat(e.target.value || '0') })} />
        </Field>
        <Field label="Max">
          <input type="number" value={cfg.max ?? 100}
            onChange={e => onChange({ ...cfg, max: parseFloat(e.target.value || '0') })} />
        </Field>
        <Field label="Unit suffix">
          <UnitSelect value={cfg.unit} onChange={v => onChange({ ...cfg, unit: v })} />
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
            background: THRESHOLD_COLOURS[t.color], flexShrink: 0,
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
    // v0.9.109 (C2) — empty metricName → HeatmapPanel shows the "configure a
    // metric" prompt (same as MetricPanel), never a blank panel. unit 'ms' is
    // the common latency-histogram default; operator flips to 's' for
    // seconds-valued bounds (http.server.request.duration).
    case 'heatmap':    return { metricName: '', unit: 'ms' };
    // v0.9.117 (F4) — empty query → PromqlPanel shows the "type a query"
    // prompt (never a blank panel).
    case 'promql':     return { query: '', viz: 'line' };
    // v0.9.781 — renderable with ZERO further input: "slowest services in the
    // window" is both a real answer and the panel's own demo. groupBy uses the
    // DOTTED semconv key — `service_name` also resolves, but the underscore
    // form of http.route does NOT, so the dotted spelling is the one to teach.
    case 'topn':       return {
      agg: 'p99', field: 'duration_ms', groupBy: 'service.name',
      unit: 'ms', limit: 10, linkTo: 'none',
    };
    case 'markdown':   return { text: '## Notes\n\nDescribe what this dashboard shows.' };
    // Row panels carry no config of their own — title is on the panel
    // itself, default-collapsed is opt-in.
    case 'row':        return { collapsed: false };
  }
}
