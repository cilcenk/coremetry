// RelationBuilder — the structural / span-relationship query builder (Gap 3).
//
// Two FilterBuilder instances (parent predicates / child predicates) plus a
// kind selector (child-of / descendant-of / sequence) and a "direct only"
// checkbox. Drives GET /api/traces/relations, which runs a BOUNDED self-join
// over raw spans and returns the matching trace rows for the existing list
// table to render.
//
// Controlled: owns no query state itself — value + onChange come from the
// Traces page so the relation query is URL-reflected like every other view.

import { FilterBuilder } from './FilterBuilder';
import { Button } from './ui/Button';
import type { RelationFilter, RelationKind, FilterExpr } from '@/lib/types';

const KIND_OPTIONS: { value: RelationKind; label: string; hint: string }[] = [
  { value: 'child-of',      label: 'child of',      hint: 'Child span is a DIRECT child of the parent span (one hop).' },
  { value: 'descendant-of', label: 'descendant of', hint: 'Child span is a child or grandchild of the parent span (depth ≤ 2).' },
  { value: 'sequence',      label: 'then (sequence)', hint: 'Parent span happens-before the child span in the same trace (A ends ≤ B starts).' },
];

export function RelationBuilder({
  value,
  onChange,
  onRun,
  running,
}: {
  value: RelationFilter;
  onChange: (next: RelationFilter) => void;
  onRun: () => void;
  running?: boolean;
}) {
  const kindMeta = KIND_OPTIONS.find(k => k.value === value.kind) ?? KIND_OPTIONS[0];
  // "direct only" is meaningful only for descendant-of (it collapses the
  // depth-2 frontier back to a single hop). child-of is already direct;
  // sequence has no edge so the flag is hidden for both.
  const showDirect = value.kind === 'descendant-of';

  const setParent = (parent: FilterExpr[]) => onChange({ ...value, parent });
  const setChild = (child: FilterExpr[]) => onChange({ ...value, child });

  // Parent / child are the two roles. For sequence the labels read "earlier" /
  // "later" since there's no parent/child edge.
  const parentLabel = value.kind === 'sequence' ? 'Earlier span (A)' : 'Parent span';
  const childLabel = value.kind === 'sequence' ? 'Later span (B)' : 'Child span';

  return (
    <div style={{
      border: '1px solid var(--border)', borderRadius: 8, padding: 12,
      background: 'var(--bg2)', marginBottom: 10, display: 'flex',
      flexDirection: 'column', gap: 10,
    }}>
      {/* Relation kind + direct-only + run */}
      <div className="controls" style={{ alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span className="field-label" style={{ color: 'var(--text2)' }}>Relation:</span>
        <select
          className="field"
          value={value.kind}
          onChange={e => onChange({ ...value, kind: e.target.value as RelationKind })}
          title={kindMeta.hint}
          style={{ width: 180 }}
        >
          {KIND_OPTIONS.map(o => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
        {showDirect && (
          <label style={{ display: 'flex', alignItems: 'center', gap: 5, color: 'var(--text2)', cursor: 'pointer', fontSize: 12 }}
            title="Match only the direct child edge (depth 1) instead of descendants up to depth 2.">
            <input type="checkbox" checked={value.direct}
              onChange={e => onChange({ ...value, direct: e.target.checked })} />
            direct only
          </label>
        )}
        <span style={{ color: 'var(--text3)', fontSize: 11, marginLeft: 4 }}>{kindMeta.hint}</span>
        <Button variant="primary" size="sm" onClick={onRun} loading={running}
          style={{ marginLeft: 'auto' }}>
          Run relation query ▶
        </Button>
      </div>

      {/* Parent / child predicate builders */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
        <div>
          <div className="field-label" style={{ color: 'var(--text2)', marginBottom: 4 }}>
            {parentLabel} — predicates
          </div>
          <FilterBuilder value={value.parent} onChange={setParent} />
        </div>
        <div>
          <div className="field-label" style={{ color: 'var(--text2)', marginBottom: 4 }}>
            {childLabel} — predicates
          </div>
          <FilterBuilder value={value.child} onChange={setChild} />
        </div>
      </div>
    </div>
  );
}
