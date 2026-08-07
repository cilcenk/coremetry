import { ServicePicker } from '../ServicePicker';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// v0.9.759 — database değişkeni: /api/databases kataloğundan ad listesi
// (dbName ?? instance; tekilleştirilmiş). DB sayısı küçük küme → düz
// select (ev kuralı: ≤~10 sabit küme picker istemez). Son 24h penceresi
// katalog için yeterli; 60s staleTime sunucu cache'iyle uyumlu.
function useDbNames(): string[] {
  const q = useQuery({
    queryKey: ['dash-var-db-names'],
    queryFn: () => {
      const to = Date.now() * 1e6;
      return api.databases(to - 24 * 3600 * 1e9, to);
    },
    staleTime: 60_000,
  });
  const names = new Set<string>();
  for (const r of q.data ?? []) names.add(r.dbName || r.instance);
  return [...names].sort();
}

function DatabaseSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const names = useDbNames();
  return (
    <select value={value} onChange={e => onChange(e.target.value)} style={{ minWidth: 160 }}>
      <option value="">(all)</option>
      {names.map(n => <option key={n} value={n}>{n}</option>)}
    </select>
  );
}
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
          ) : v.type === 'database' ? (
            <DatabaseSelect
              value={values[v.name] ?? ''}
              onChange={x => onChange(v.name, x)}
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
