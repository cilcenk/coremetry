'use client';
import { useState, useEffect } from 'react';
import { api } from '@/lib/api';
import type {
  Panel, PanelType, PanelWidth, MetricInfo,
  MetricPanelConfig, SpanMetricPanelConfig, StatPanelConfig, MarkdownPanelConfig,
} from '@/lib/types';

const TYPE_LABELS: Record<PanelType, string> = {
  metric:     'Metric (line)',
  spanmetric: 'Span aggregation (line)',
  stat:       'Stat (single value)',
  markdown:   'Markdown / notes',
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

        {panel.type === 'metric' && (
          <MetricFields cfg={panel.config as MetricPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'spanmetric' && (
          <SpanMetricFields cfg={panel.config as SpanMetricPanelConfig} onChange={updateConfig} />
        )}
        {panel.type === 'stat' && (
          <StatFields cfg={panel.config as StatPanelConfig} onChange={updateConfig} />
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
  const [names, setNames] = useState<MetricInfo[]>([]);
  useEffect(() => { api.metricNames('').then(n => setNames(n ?? [])); }, []);
  const update = <K extends keyof MetricPanelConfig>(k: K, v: MetricPanelConfig[K]) =>
    onChange({ ...cfg, [k]: v });
  return (
    <>
      <Field label="Metric name">
        <input list="metric-names" value={cfg.metricName ?? ''}
          onChange={e => update('metricName', e.target.value)} style={{ width: '100%' }} />
        <datalist id="metric-names">
          {names.map(n => <option key={n.name} value={n.name} />)}
        </datalist>
      </Field>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
        <Field label="Aggregation">
          <select value={cfg.agg ?? 'avg'} onChange={e => update('agg', e.target.value)}>
            {METRIC_AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </Field>
        <Field label="Service (optional)">
          <input value={cfg.service ?? ''}
            onChange={e => update('service', e.target.value)} />
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
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
        <Field label="Aggregation">
          <select value={cfg.agg ?? 'count'} onChange={e => update('agg', e.target.value)}>
            {SPAN_AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </Field>
        <Field label="Field (for percentiles)">
          <input value={cfg.field ?? 'duration_ms'} placeholder="duration_ms"
            onChange={e => update('field', e.target.value)} />
        </Field>
      </div>
      <Field label="Group by (comma-sep keys)">
        <input value={cfg.groupBy ?? ''} placeholder="service_name, http_route"
          onChange={e => update('groupBy', e.target.value)} style={{ width: '100%' }} />
      </Field>
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
    </>
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
    case 'markdown':   return { text: '## Notes\n\nDescribe what this dashboard shows.' };
  }
}
