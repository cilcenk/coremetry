import { Combobox } from '@/components/Combobox';
import { ServicePicker } from '@/components/ServicePicker';
import { FilterBuilder } from '@/components/FilterBuilder';
import { GroupedMetricPicker } from '@/components/viz/GroupedMetricPicker';
import { AGG_OPTIONS } from './presets';
import { SplitByPicker } from './SplitByPicker';
import {
  type BuilderQuery, type QuerySource,
  METRIC_CATALOG_AGGS, spanNeedsField, blankQuery,
} from './model';

// QueryRow — one builder query (explore-v2 Phase 2):
//   [A◉] [Spans|Metric] [agg of field | metric-picker agg] [scope] [filters] [split] [×]
// The letter badge toggles enabled (MQE precedent). Source flip resets the
// source-specific slots (metric/agg/unit) but keeps scope/filters/splitBy —
// the operator's narrowing intent survives the flip.

export function QueryRow({ q, canRemove, onChange, onRemove }: {
  q: BuilderQuery;
  canRemove: boolean;
  onChange: (q: BuilderQuery) => void;
  onRemove: () => void;
}) {
  const setSource = (source: QuerySource) => {
    if (source === q.source) return;
    const base = blankQuery(q.letter, source);
    onChange({ ...base, enabled: q.enabled, scope: q.scope, filters: q.filters, splitBy: q.splitBy });
  };

  return (
    <div style={{
      display: 'flex', alignItems: 'flex-start', gap: 8, flexWrap: 'wrap',
      padding: '8px 12px', borderTop: '1px solid var(--border)',
      opacity: q.enabled ? 1 : 0.5,
    }}>
      {/* Letter badge — click toggles the query on/off */}
      <button type="button"
        onClick={() => onChange({ ...q, enabled: !q.enabled })}
        title={q.enabled ? 'Sorguyu kapat' : 'Sorguyu aç'}
        style={{
          all: 'unset', cursor: 'pointer', display: 'inline-flex',
          alignItems: 'center', justifyContent: 'center',
          width: 22, height: 22, borderRadius: 4, flexShrink: 0, marginTop: 2,
          background: q.enabled ? 'var(--accent2)' : 'var(--bg3)',
          color: q.enabled ? 'var(--bg)' : 'var(--text3)',
          fontSize: 12, fontWeight: 700,
          border: '1px solid ' + (q.enabled ? 'var(--accent2)' : 'var(--border)'),
        }}>
        {q.letter}
      </button>

      <div className="segmented" style={{ marginTop: 1 }}>
        <button type="button" className={q.source === 'span' ? 'active' : ''}
          onClick={() => setSource('span')} title="Span sinyalleri (rate / error_rate / persentiller)">
          Spans
        </button>
        <button type="button" className={q.source === 'metric' ? 'active' : ''}
          onClick={() => setSource('metric')} title="Katalog metriği (OTel metric_points)">
          Metric
        </button>
      </div>

      {q.source === 'span' ? (
        <>
          <select value={q.agg} aria-label="Aggregation"
            onChange={e => onChange({ ...q, agg: e.target.value })}>
            {AGG_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          {spanNeedsField(q.agg) && (
            <>
              <span style={{ color: 'var(--text2)', fontSize: 12, alignSelf: 'center' }}>of</span>
              <Combobox value={q.metric} onChange={m => onChange({ ...q, metric: m })}
                options={['duration_ms', 'duration_s', 'http.status_code', '1']}
                placeholder="duration_ms" width={150} />
            </>
          )}
        </>
      ) : (
        <>
          <GroupedMetricPicker value={q.metric} unit={q.unit}
            onPick={m => onChange({ ...q, metric: m.name, unit: m.unit })} />
          <select value={q.agg} aria-label="Aggregation"
            onChange={e => onChange({ ...q, agg: e.target.value })}>
            {METRIC_CATALOG_AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
        </>
      )}

      <ServicePicker value={q.scope} onChange={s => onChange({ ...q, scope: s })}
        placeholder="tüm servisler" width={170} />

      <div style={{ flex: 1, minWidth: 220 }}>
        <FilterBuilder value={q.filters} onChange={f => onChange({ ...q, filters: f })} />
        {q.dsl.trim() !== '' && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 4 }}>
            <span style={{ fontSize: 10.5, color: 'var(--text3)', textTransform: 'uppercase',
                           letterSpacing: '.4px', fontWeight: 700 }}>DSL</span>
            <input value={q.dsl} spellCheck={false}
              onChange={e => onChange({ ...q, dsl: e.target.value })}
              style={{ flex: 1, fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 11.5 }}
              title="Gelişmiş DSL — chip filtreleriyle AND'lenir (eski derin linklerden gelir)" />
            <button className="fb-chip-x" type="button" aria-label="DSL'i kaldır"
              onClick={() => onChange({ ...q, dsl: '' })}>✕</button>
          </div>
        )}
      </div>

      <span style={{ color: 'var(--text2)', fontSize: 12, alignSelf: 'center' }}>Split:</span>
      <SplitByPicker value={q.splitBy} onChange={by => onChange({ ...q, splitBy: by })} />

      <button type="button" onClick={onRemove} disabled={!canRemove}
        aria-label="Sorguyu sil" title={canRemove ? 'Sorguyu sil' : 'Son sorgu silinemez'}
        style={{
          all: 'unset', cursor: canRemove ? 'pointer' : 'not-allowed',
          color: 'var(--text3)', fontSize: 14, padding: '2px 6px', marginTop: 1,
          opacity: canRemove ? 1 : 0.4,
        }}>×</button>
    </div>
  );
}
