import { ServicePicker } from '../ServicePicker';
import { Button } from '@/components/ui';
import type { DashboardVariable } from '@/lib/types';

// VariablesBar renders one picker per dashboard variable above the
// dashboard's panel grid. Selected values are URL-persisted by the
// parent (so reloads + share links keep the choice). Empty value =
// "all" — the renderer drops the predicate line entirely.
//
// Each variable's UI:
//   - type=service  → ServicePicker (auto-loads from /api/service-names)
//   - type=custom   → <select> over the variable's options list
//
// Looks like Grafana's variable bar: small label + picker, sits above
// the dashboard toolbar, no border-heavy chrome.
export function VariablesBar({ variables, values, onChange }: {
  variables: DashboardVariable[];
  values: Record<string, string>;
  onChange: (name: string, value: string) => void;
}) {
  if (variables.length === 0) return null;
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap',
      padding: '6px 0', marginBottom: 10,
    }}>
      {variables.map(v => (
        <div key={v.name} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>
            {v.label || v.name}:
          </span>
          {v.type === 'service' ? (
            <ServicePicker
              value={values[v.name] ?? ''}
              onChange={x => onChange(v.name, x)}
              placeholder="(all)"
              width={220}
            />
          ) : (
            <select
              value={values[v.name] ?? ''}
              onChange={e => onChange(v.name, e.target.value)}
              style={{ minWidth: 160 }}
            >
              <option value="">(all)</option>
              {(v.options ?? []).map(o => (
                <option key={o} value={o}>{o}</option>
              ))}
            </select>
          )}
          {values[v.name] && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onChange(v.name, '')}
              title="Clear"
            >✕</Button>
          )}
        </div>
      ))}
    </div>
  );
}
