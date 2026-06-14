import { useEffect, useMemo, useState } from 'react';
import { Combobox } from './Combobox';
import { api } from '@/lib/api';
import type { FilterExpr, FilterOp } from '@/lib/types';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs } from '@/lib/utils';

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
  //
  // v0.5.261 — context-aware. When the operator already has filters
  // active, the suggester scans for keys WITH data under those
  // filters (not the global top-N), so the dropdown matches the
  // slice they're already looking at. Re-fetches when the filter
  // chip set changes.
  const [observedKeys, setObservedKeys] = useState<{ key: string; count: number }[]>([]);
  useEffect(() => {
    const filterCtx = value.length > 0 ? JSON.stringify(value) : undefined;
    api.attributeKeys('1h', 500, filterCtx)
      .then(rows => setObservedKeys(
        (rows ?? []).map(r => ({
          key: r.scope === 'resource' ? `resource.${r.key}` : r.key,
          count: r.count,
        }))
      ))
      .catch(() => setObservedKeys([]));
  }, [JSON.stringify(value)]);
  const allKeys = useMemo(() => {
    // v0.5.261 — observed-by-count first (real data leads), then
    // static OTel semconv suggestions for anything still missing.
    // Previous order (static-first) buried real custom attributes
    // below stale hardcoded keys; the new ordering matches the
    // operator's mental model "what's in MY data right now".
    const seen = new Set<string>();
    const out: string[] = [];
    for (const o of observedKeys) {
      if (seen.has(o.key)) continue;
      seen.add(o.key);
      out.push(o.key);
    }
    for (const k of SUGGESTED_KEYS) {
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(k);
    }
    return out;
  }, [observedKeys]);
  // Top-5 hint surfaced under the picker so the operator sees
  // "what's heavy right now" without scrolling the dropdown.
  const topHints = observedKeys.slice(0, 5);

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
          topHints={topHints}
        />
      )}
    </div>
  );
}

function DraftEditor({ draft, onSave, onCancel, suggestedValues, keyOptions, topHints }: {
  draft: FilterExpr;
  onSave: (f: FilterExpr) => void;
  onCancel: () => void;
  suggestedValues?: Record<string, string[]>;
  keyOptions: string[];
  // v0.5.261 — top-5 observed-by-count attribute keys, used as a
  // one-click hint row above the picker so the operator doesn't
  // have to scroll the dropdown to find what's heavy.
  topHints: { key: string; count: number }[];
}) {
  const [local, setLocal] = useState<FilterExpr>(draft);
  // v0.8.x (trace-query gap-1) — bind value autocomplete to the page's active
  // window (the URL range source of truth), not a hard-coded 1h, so a value
  // picked on a 24h/7d view matches the rows actually shown.
  const [range] = useUrlRange();
  const rangeNs = useMemo(() => timeRangeToNs(range), [range]);
  const needsValue = NEEDS_VALUE[local.op];
  const isList = local.op === 'IN' || local.op === 'NOT IN';

  // Esc cancels the inline editor — matches Modal.tsx / TimeRangePicker
  // muscle memory. Registered while the editor is mounted; the parent
  // controls mounting via `draft && <DraftEditor>` so this listener
  // only lives during an active edit.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCancel();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onCancel]);

  // Live value autocomplete. As soon as the operator picks an
  // attribute key, fetch the top-N observed values (server-
  // cached 60s) and merge with anything the parent already
  // pre-supplied via `suggestedValues`.
  //
  // v0.5.182 — additionally re-queries with a `q` substring as
  // the operator types in the value field so a long-tail value
  // (a specific http.url, db.statement fragment, etc.) becomes
  // pickable at high cardinality. Empty typed value falls back
  // to the top-N-by-count default. Debounced 200ms so a fast
  // typist doesn't fan out N requests per keystroke.
  const [liveValues, setLiveValues] = useState<string[]>([]);
  const [liveLoading, setLiveLoading] = useState(false);
  // Substring the operator is currently typing in the value
  // field. We don't use `local.v[0]` directly because that
  // double-fires on every keystroke; the debounced effect
  // mirrors it into a separate state.
  const typedValue = (local.v[0] ?? '').trim();
  useEffect(() => {
    const k = local.k.trim();
    if (!k) { setLiveValues([]); return; }
    let cancelled = false;
    setLiveLoading(true);
    const handle = setTimeout(() => {
      api.attributeValues(k, '1h', 200, typedValue || undefined, rangeNs)
        .then(rows => {
          if (cancelled) return;
          setLiveValues((rows ?? []).map(r => r.value));
        })
        .catch(() => { if (!cancelled) setLiveValues([]); })
        .finally(() => { if (!cancelled) setLiveLoading(false); });
    }, 200);
    return () => { cancelled = true; clearTimeout(handle); };
  }, [local.k, typedValue, rangeNs]);

  // Combine: parent-provided suggestions first (services /
  // operations tend to be richer than the 1h fast-cache),
  // then live observed values, deduped. When the operator
  // typed a substring filter the live side already narrowed,
  // so dedup just merges in whatever seed entries also match.
  const valueOptions = useMemo(() => {
    const seed = suggestedValues?.[local.k] ?? [];
    if (seed.length === 0) return liveValues;
    if (liveValues.length === 0) return seed;
    const seen = new Set<string>();
    const out: string[] = [];
    for (const v of [...seed, ...liveValues]) {
      if (!seen.has(v)) { seen.add(v); out.push(v); }
    }
    return out;
  }, [local.k, suggestedValues, liveValues]);

  const submit = () => {
    const v = needsValue
      ? (isList
          ? local.v.flatMap(x => x.split(',').map(s => s.trim()).filter(Boolean))
          : local.v.map(x => x.trim()).filter(Boolean))
      : [];
    if (needsValue && v.length === 0) return;
    onSave({ k: local.k.trim(), op: local.op, v });
  };

  // fmtCount — compact human-readable count for the hint chips.
  const fmtCount = (n: number) => {
    if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'k';
    return String(n);
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
        {/* v0.5.261 — context-aware top-5 hint row. Click → instant
            key pick. Counts are scoped to the filter set currently
            on the chips, so the operator sees "what's heaviest in
            THIS slice" not the global top-N. */}
        {topHints.length > 0 && !local.k && (
          <div style={{
            gridColumn: '1 / -1', display: 'flex', flexWrap: 'wrap',
            gap: 6, alignItems: 'center', marginTop: -2, marginBottom: 2,
          }}>
            <span style={{ fontSize: 10, color: 'var(--text3)' }}>top in this slice:</span>
            {topHints.map(h => (
              <button key={h.key} type="button"
                onClick={() => setLocal({ ...local, k: h.key })}
                style={{
                  fontSize: 10, padding: '1px 6px', borderRadius: 8,
                  background: 'var(--bg1)', border: '1px solid var(--border)',
                  color: 'var(--text)', cursor: 'pointer',
                  fontFamily: 'ui-monospace, SFMono-Regular, monospace',
                }}
                title={`${h.count.toLocaleString()} spans carry this attribute under the active filter set`}>
                {h.key}
                <span style={{ marginLeft: 4, color: 'var(--text3)' }}>
                  {fmtCount(h.count)}
                </span>
              </button>
            ))}
          </div>
        )}
        <label>
          <span>Op</span>
          <select value={local.op}
            onChange={e => setLocal({ ...local, op: e.target.value as FilterOp })}>
            {OPS.map(o => <option key={o} value={o}>{o}</option>)}
          </select>
        </label>
        {needsValue && (
          <label style={{ flex: 1 }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              {isList ? 'Values (comma-sep)' : 'Value'}
              {liveLoading && (
                <span style={{ fontSize: 10, color: 'var(--text3)', fontStyle: 'italic' }}>
                  loading values…
                </span>
              )}
              {!liveLoading && local.k.trim() && valueOptions.length > 0 && (
                <span style={{ fontSize: 10, color: 'var(--text3)' }}>
                  {valueOptions.length} observed
                </span>
              )}
            </span>
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
