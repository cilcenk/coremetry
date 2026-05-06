'use client';
import { useEffect, useMemo, useState } from 'react';
import { Combobox } from './Combobox';
import { api } from '@/lib/api';
import type { FilterExpr, FilterOp } from '@/lib/types';

// Suggested attribute keys for the autocomplete. Users can type anything else
// (custom span/resource attributes) — backend looks them up in the matching
// array. Tempo-style scope prefixes choose where to look:
//   resource.X  → resource attribute (env, host, etc.)
//   span.X      → span attribute
//   X (bare)    → well-known column if any, else span attribute
const SUGGESTED_KEYS = [
  // Span — well-known
  'name', 'operation', 'kind', 'status', 'duration_ms',
  'http.method', 'http.route', 'http.status_code',
  'db.system', 'db.statement',
  'rpc.system', 'rpc.method',
  'peer.service', 'messaging.system',
  // Span — explicit scope
  'span.http.method', 'span.http.route', 'span.http.status_code',
  'span.db.system', 'span.db.statement',
  'span.peer.service',
  // Resource (process / host / deployment)
  'resource.service.name',
  'resource.host.name',
  'resource.deployment.environment',
  'resource.service.version',
  'resource.telemetry.sdk.name',
  'resource.telemetry.sdk.language',
];

const OPS: FilterOp[] = ['=', '!=', 'LIKE', 'NOT LIKE', 'IN', 'NOT IN', '>', '>=', '<', '<=', 'EXISTS', 'NOT EXISTS'];

const NEEDS_VALUE: Record<FilterOp, boolean> = {
  '=': true, '!=': true,
  'LIKE': true, 'NOT LIKE': true,
  'IN': true, 'NOT IN': true,
  '>': true, '>=': true, '<': true, '<=': true,
  'EXISTS': false, 'NOT EXISTS': false,
};

export function FilterBuilder({ value, onChange, suggestedValues }: {
  value: FilterExpr[];
  onChange: (next: FilterExpr[]) => void;
  /** Optional value-suggestions per key (e.g. service names). */
  suggestedValues?: Record<string, string[]>;
}) {
  const [draft, setDraft] = useState<FilterExpr | null>(null);

  // Live-load attribute keys actually observed in the last hour and
  // merge with the static suggestion list. Custom attrs (function_code,
  // channel_code, etc.) emitted by the operator's collector now surface
  // as picker suggestions instead of relying on the operator typing the
  // exact key from memory. Resource-scoped keys are prefixed with
  // "resource." so they slot into the right backend lookup.
  const [observedKeys, setObservedKeys] = useState<string[]>([]);
  useEffect(() => {
    api.attributeKeys('1h', 500)
      .then(rows => setObservedKeys(
        (rows ?? []).map(r => r.scope === 'resource' ? `resource.${r.key}` : r.key)
      ))
      .catch(() => setObservedKeys([]));
  }, []);
  const allKeys = useMemo(() => {
    // Union + dedupe; preserve static keys first so the most-common
    // OTel semconv ones lead the dropdown, then live keys (which the
    // browser's substring filter narrows down as the operator types).
    const seen = new Set<string>();
    const out: string[] = [];
    for (const k of [...SUGGESTED_KEYS, ...observedKeys]) {
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(k);
    }
    return out;
  }, [observedKeys]);

  const addOrUpdate = (next: FilterExpr) => {
    if (!next.k.trim()) return;
    const out = [...value];
    const i = out.findIndex(f => f.k === next.k && f.op === next.op);
    if (i >= 0) out[i] = next;
    else out.push(next);
    onChange(out);
    setDraft(null);
  };

  const removeAt = (i: number) => onChange(value.filter((_, j) => j !== i));

  return (
    <div className="fb">
      <div className="fb-chips">
        {value.map((f, i) => (
          <span key={i} className="fb-chip"
            title="Click ✕ to remove"
            onClick={() => setDraft({ ...f })}>
            <b>{f.k}</b>
            <span className="fb-chip-op"> {f.op} </span>
            {NEEDS_VALUE[f.op] && (
              <span className="fb-chip-val">{formatValues(f.v, f.op)}</span>
            )}
            <button className="fb-chip-x" type="button"
              onClick={e => { e.stopPropagation(); removeAt(i); }}
              aria-label="Remove filter">✕</button>
          </span>
        ))}
        {!draft && (
          <button className="fb-add" type="button"
            onClick={() => setDraft({ k: '', op: '=', v: [''] })}>
            + Add filter
          </button>
        )}
      </div>
      {draft && (
        <DraftEditor
          draft={draft}
          onSave={addOrUpdate}
          onCancel={() => setDraft(null)}
          suggestedValues={suggestedValues}
          keyOptions={allKeys}
        />
      )}
    </div>
  );
}

function DraftEditor({ draft, onSave, onCancel, suggestedValues, keyOptions }: {
  draft: FilterExpr;
  onSave: (f: FilterExpr) => void;
  onCancel: () => void;
  suggestedValues?: Record<string, string[]>;
  keyOptions: string[];
}) {
  const [local, setLocal] = useState<FilterExpr>(draft);
  const valueOptions = suggestedValues?.[local.k] ?? [];
  const needsValue = NEEDS_VALUE[local.op];
  const isList = local.op === 'IN' || local.op === 'NOT IN';

  const submit = () => {
    const v = needsValue
      ? (isList
          ? local.v.flatMap(x => x.split(',').map(s => s.trim()).filter(Boolean))
          : local.v.map(x => x.trim()).filter(Boolean))
      : [];
    if (needsValue && v.length === 0) return;
    onSave({ k: local.k.trim(), op: local.op, v });
  };

  return (
    <div className="fb-form">
      <div className="fb-form-grid">
        <label>
          <span>Attribute</span>
          <Combobox value={local.k} onChange={k => setLocal({ ...local, k })}
            options={keyOptions} placeholder="e.g. http.status_code or function_code" width={260}
            onEnter={submit} />
        </label>
        <label>
          <span>Op</span>
          <select value={local.op}
            onChange={e => setLocal({ ...local, op: e.target.value as FilterOp })}>
            {OPS.map(o => <option key={o} value={o}>{o}</option>)}
          </select>
        </label>
        {needsValue && (
          <label style={{ flex: 1 }}>
            <span>{isList ? 'Values (comma-sep)' : 'Value'}</span>
            <Combobox value={local.v[0] ?? ''}
              onChange={v => setLocal({ ...local, v: [v] })}
              options={valueOptions}
              placeholder={isList ? 'a, b, c' : 'value'}
              width={'100%'} onEnter={submit} />
          </label>
        )}
      </div>
      <div className="fb-form-actions">
        <button onClick={submit}>Add</button>
        <button className="sec" onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}

function formatValues(v: string[], op: string): string {
  if (op === 'IN' || op === 'NOT IN') return v.join(', ');
  return v[0] ?? '';
}
