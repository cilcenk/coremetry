import { FilterBuilder } from './FilterBuilder';
import { Button } from './ui/Button';
import type { FilterExpr, FilterGroup, FilterJoin } from '@/lib/types';

// FilterGroupBuilder — grouped AND/OR query builder (v0.8.x trace-query gap-2).
//
// Additive, default-off upgrade over the flat conjunction-only FilterBuilder.
// It is a thin shell around FilterBuilder: the top-level leaf set is one
// FilterBuilder, and each nested group (v1 cap: ONE level of nesting) is
// another FilterBuilder with its OWN AND/OR join. The top-level join combines
// the top-level leaves with the nested groups.
//
// Why a shell and not a fork of FilterBuilder: FilterBuilder already owns the
// hard part — server-side attribute-key + range-bound value autocomplete,
// chip rendering, the inline draft editor. Reusing it per leaf-set means the
// grouped builder inherits all of that for free and can't drift from the flat
// path's UX.
//
// The consumer keeps the flat FilterBuilder as the DEFAULT render and only
// mounts this when the operator opts into grouped mode, so existing saved
// views / shared URLs (which carry a flat FilterExpr[]) are untouched.

const JOINS: FilterJoin[] = ['AND', 'OR'];

function JoinToggle({ value, onChange, label }: {
  value: FilterJoin;
  onChange: (j: FilterJoin) => void;
  label?: string;
}) {
  return (
    <div className="row gap-2" style={{ alignItems: 'center' }}>
      {label && <span className="field-label" style={{ fontSize: 11, color: 'var(--text3)' }}>{label}</span>}
      <div className="row" role="group" aria-label="Join operator" style={{ gap: 4 }}>
        {JOINS.map(j => (
          <Button
            key={j}
            variant={value === j ? 'primary' : 'secondary'}
            size="sm"
            aria-pressed={value === j}
            onClick={() => onChange(j)}>
            {j}
          </Button>
        ))}
      </div>
    </div>
  );
}

export function FilterGroupBuilder({ value, onChange, suggestedValues }: {
  value: FilterGroup;
  onChange: (next: FilterGroup) => void;
  /** Optional value-suggestions per key (e.g. service names). */
  suggestedValues?: Record<string, string[]>;
}) {
  const join: FilterJoin = value.join ?? 'AND';
  const filters: FilterExpr[] = value.filters ?? [];
  const groups: FilterGroup[] = value.groups ?? [];

  const setJoin = (j: FilterJoin) => onChange({ ...value, join: j });
  const setFilters = (next: FilterExpr[]) => onChange({ ...value, filters: next });

  const setGroupAt = (i: number, next: FilterGroup) => {
    const out = groups.slice();
    out[i] = next;
    onChange({ ...value, groups: out });
  };
  const removeGroupAt = (i: number) =>
    onChange({ ...value, groups: groups.filter((_, j) => j !== i) });
  const addGroup = () =>
    onChange({ ...value, groups: [...groups, { join: 'OR', filters: [] }] });

  // v1 depth cap: exactly one level of nested groups. The "add group" button
  // hides once a nested group exists if we wanted a single group, but we allow
  // multiple sibling groups at depth 1 (still one level of nesting), which
  // covers `(A OR B) AND (C OR D) AND e`.
  return (
    <div className="fgb" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div className="row gap-3" style={{ alignItems: 'center', flexWrap: 'wrap' }}>
        <JoinToggle value={join} onChange={setJoin} label="Match" />
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {join === 'AND' ? 'all of the following' : 'any of the following'}
        </span>
      </div>

      {/* Top-level leaf set — the existing flat builder, unchanged. */}
      <FilterBuilder value={filters} onChange={setFilters} suggestedValues={suggestedValues} />

      {/* Nested groups (one level). Each is its own AND/OR sub-builder. */}
      {groups.map((g, i) => (
        <div key={i} className="fgb-group"
          style={{
            border: '1px solid var(--border)', borderRadius: 8,
            padding: '8px 10px', display: 'flex', flexDirection: 'column', gap: 8,
            background: 'var(--bg1)',
          }}>
          <div className="row gap-3" style={{ alignItems: 'center', justifyContent: 'space-between' }}>
            <JoinToggle
              value={g.join ?? 'OR'}
              onChange={j => setGroupAt(i, { ...g, join: j })}
              label="Group — match" />
            <Button variant="ghost" size="sm" onClick={() => removeGroupAt(i)} aria-label="Remove group">
              ✕ Remove group
            </Button>
          </div>
          <FilterBuilder
            value={g.filters ?? []}
            onChange={next => setGroupAt(i, { ...g, filters: next })}
            suggestedValues={suggestedValues} />
        </div>
      ))}

      <div>
        <Button variant="secondary" size="sm" onClick={addGroup}>+ Add group</Button>
      </div>
    </div>
  );
}
