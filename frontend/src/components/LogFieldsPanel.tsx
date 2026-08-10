import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import { IconButton } from '@/components/ui/IconButton';
import { getRaw, setRaw } from '@/lib/storage';
import type { CSSProperties } from 'react';

// LogFieldsPanel — Kibana Discover-style left rail on /logs
// (revamp step 2, v0.8.255). Two groups: "Selected fields" (the
// table's active dynamic columns) and "Available fields" (the
// backend mapping via /api/logs/fields), with a client-side filter
// box. Clicking a field expands an accordion showing its top-5
// values with % bars; each value carries ⊕/⊖ (adds a filter pill)
// and the accordion footer toggles the field as a table column.
//
// ES-usage contract (operator: "elastic api kullanımını çok
// artırma"): the ONLY network call this panel makes is one
// fieldstats fetch when a field is expanded — no prefetch across
// the field list, no polling, staleTime 60s to match the server
// cache. Collapsing/re-expanding within a minute is free.

const OPEN_KEY = 'logs.fieldsPanel.open';

// Params for the stats fetch — mirrors the /logs slice so the
// accordion reflects what the table shows.
export interface FieldStatsScope {
  from?: number;
  to?: number;
  service?: string;
  cluster?: string;
  env?: string; // v0.8.400 — global ?env= deployment-environment filter
  search?: string;
  severity?: number;
  traceId?: string;
  spanId?: string;
}

function FieldAccordion({ field, scope, isColumn, onToggleColumn, onPillAdd, onPillExclude }: {
  field: string;
  scope: FieldStatsScope;
  isColumn: boolean;
  onToggleColumn: (id: string) => void;
  onPillAdd: (key: string, value: string) => void;
  onPillExclude: (key: string, value: string) => void;
}) {
  const q = useQuery({
    queryKey: ['logs', 'fieldstats', field, scope],
    queryFn: () => api.logsFieldStats({ field, ...scope }),
    staleTime: 60_000, // matches the server-side cache TTL
    retry: 1,
  });
  const d = q.data; // const-narrowed so the map callbacks see non-undefined
  return (
    <div style={{
      padding: '6px 8px 8px', margin: '2px 0 4px',
      background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6,
    }}>
      {q.isLoading && <div style={{ fontSize: 11, color: 'var(--text3)' }}>Loading top values…</div>}
      {q.isError && <div style={{ fontSize: 11, color: 'var(--err)' }}>Top values unavailable</div>}
      {d && d.values.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>No values in this window</div>
      )}
      {d && d.values.map(v => {
        const pct = d.total > 0 ? (v.count / d.total) * 100 : 0;
        return (
          <div key={v.value} style={{ marginBottom: 6 }}>
            <div style={{
              display: 'flex', alignItems: 'center', gap: 6, fontSize: 11,
            }}>
              <span title={v.value} style={{
                flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              }}>{v.value}</span>
              <span style={{ color: 'var(--text3)' }}>{pct.toFixed(pct >= 10 ? 0 : 1)}%</span>
              <IconButton variant="bare" size="xs" className="ib-add"
                onClick={() => onPillAdd(field, v.value)}
                title={`Filter for ${field}: ${v.value}`}
                aria-label={`Filter for ${field}: ${v.value}`}
                icon="⊕" />
              <IconButton variant="bare" size="xs" className="ib-not"
                onClick={() => onPillExclude(field, v.value)}
                title={`Filter out ${field}: ${v.value}`}
                aria-label={`Filter out ${field}: ${v.value}`}
                icon="⊖" />
            </div>
            <div style={{ height: 3, background: 'var(--bg3)', borderRadius: 2, marginTop: 2 }}>
              <div style={{
                height: 3, width: `${Math.max(2, pct)}%`,
                background: 'var(--accent)', borderRadius: 2,
              }} />
            </div>
          </div>
        );
      })}
      <Button variant="secondary" size="sm"
        style={{ marginTop: 2 }}
        onClick={() => onToggleColumn(field)}>
        {isColumn ? '− Remove table column' : '+ Add table column'}
      </Button>
    </div>
  );
}

// v0.9.452 — backend'in conventionalLogFields listesiyle AYNI adlar
// (internal/logstore/elasticsearch.go): backend cap'ten korur, burada
// öne çıkar. Sıra operatörün Kibana "Popular fields" ekranını izler.
const POPULAR_FIELDS: readonly string[] = [
  'kubernetes.container_name',
  'openshift.labels.cluster',
  'message',
  'kubernetes.namespace_name',
  'kubernetes.pod_name',
  'backendUrl',
  'service_name',
  'lastError.message',
  'responseCode',
  'throwable',
];

export function LogFieldsPanel({
  fields, fieldsTotal, columns, scope, onToggleColumn, onPillAdd, onPillExclude,
}: {
  fields: string[];          // available fields from the backend mapping
  // v0.9.292 — how many paths the mapping actually had. The backend
  // caps the list (dynamic mapping at 10B docs/day routinely produces
  // four-digit field counts); a clipped list rendered without saying so
  // would read as "these are the fields".
  fieldsTotal?: number;
  columns: string[];         // active dynamic table columns
  scope: FieldStatsScope;
  onToggleColumn: (id: string) => void;
  onPillAdd: (key: string, value: string) => void;
  onPillExclude: (key: string, value: string) => void;
}) {
  // v0.8.286 (operator-reported "sürekli durmasın") — the fields rail is now
  // CLOSED by default so /logs opens with a full-width table; the thin "ƒ Fields"
  // affordance summons it, and the choice persists ('1' = keep open).
  const [open, setOpen] = useState<boolean>(() => getRaw(OPEN_KEY) === '1');
  const [needle, setNeedle] = useState('');
  const [expandedField, setExpandedField] = useState<string | null>(null);
  const toggleOpen = () => {
    setOpen(v => { setRaw(OPEN_KEY, v ? '0' : '1'); return !v; });
  };

  const selectedSet = useMemo(() => new Set(columns), [columns]);
  // v0.9.452 (operatör isteği, Kibana emsali) — popüler alanlar:
  // OpenShift cluster-logging + yaygın uygulama şeması adayları,
  // YALNIZ mapping'de gerçekten varsa (fields listede-varlık = gerçek;
  // backend cap'e kurban gidenleri geri ekliyor). CH backend'de fields
  // boş → grup hiç görünmez.
  const popular = useMemo(() => {
    const n = needle.trim().toLowerCase();
    const set = new Set(fields);
    return POPULAR_FIELDS
      .filter(f => set.has(f) && !selectedSet.has(f))
      .filter(f => !n || f.toLowerCase().includes(n));
  }, [fields, selectedSet, needle]);
  const available = useMemo(() => {
    const n = needle.trim().toLowerCase();
    const pop = new Set(popular);
    return fields
      .filter(f => !selectedSet.has(f) && !pop.has(f))
      .filter(f => !n || f.toLowerCase().includes(n));
  }, [fields, selectedSet, needle, popular]);
  const selected = useMemo(() => {
    const n = needle.trim().toLowerCase();
    return columns.filter(f => !n || f.toLowerCase().includes(n));
  }, [columns, needle]);

  if (!open) {
    // Deliberately NOT the shared <Button> atom: this is the collapsed
    // rail affordance (vertical writing-mode, stretches the panel edge),
    // a structural anatomy the atom's variants can't express (U1 batch 2).
    return (
      <button type="button" onClick={toggleOpen}
        title="Show the fields panel"
        style={{
          alignSelf: 'stretch', width: 24, border: '1px solid var(--border)',
          borderRadius: 6, background: 'var(--bg1)', cursor: 'pointer',
          color: 'var(--text3)', fontSize: 11, padding: 0,
          writingMode: 'vertical-rl',
        }}>
        ƒ Fields
      </button>
    );
  }

  const groupTitle: CSSProperties = {
    fontSize: 10, fontWeight: 700, letterSpacing: '.06em',
    textTransform: 'uppercase', color: 'var(--text3)', margin: '8px 0 4px',
  };
  const fieldRow = (f: string, removable: boolean) => (
    <div key={f}>
      <div
        role="button" tabIndex={0}
        onClick={() => setExpandedField(cur => (cur === f ? null : f))}
        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setExpandedField(cur => (cur === f ? null : f)); } }}
        title={`${f} — click for top values`}
        style={{
          display: 'flex', alignItems: 'center', gap: 4,
          padding: '2px 4px', borderRadius: 4, cursor: 'pointer',
          fontSize: 11.5,
          // v0.9.292 — the rail is a >100-row list inside a 70vh
          // scroller and was rendering flat: no virtualisation, no
          // content-visibility, no cap. It was the ONE place on /logs
          // that broke the rule the log table itself follows. Rows are
          // uniform, so content-visibility skips layout+paint for the
          // off-screen ones at zero structural cost.
          contentVisibility: 'auto',
          containIntrinsicSize: '0 22px',
          fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
          background: expandedField === f ? 'var(--accent-soft)' : 'transparent',
          color: removable ? 'var(--accent2)' : 'var(--text2)',
        }}>
        <span style={{
          flex: 1, minWidth: 0, overflow: 'hidden',
          textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}>{f}</span>
        <span style={{ color: 'var(--text3)', fontSize: 10 }}>{expandedField === f ? '▾' : '▸'}</span>
      </div>
      {expandedField === f && (
        <FieldAccordion field={f} scope={scope}
          isColumn={selectedSet.has(f)}
          onToggleColumn={onToggleColumn}
          onPillAdd={onPillAdd} onPillExclude={onPillExclude} />
      )}
    </div>
  );

  return (
    <div style={{
      width: 250, flex: '0 0 250px', alignSelf: 'flex-start',
      border: '1px solid var(--border)', borderRadius: 8,
      background: 'var(--bg1)', padding: 8,
      maxHeight: '70vh', overflowY: 'auto',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
        <input
          placeholder="Filter fields…"
          aria-label="Filter field names"
          value={needle}
          onChange={e => setNeedle(e.target.value)}
          style={{ flex: 1, minWidth: 0, fontSize: 11.5, padding: '3px 6px' }} />
        <Button variant="secondary" size="sm" onClick={toggleOpen}
          title="Hide the fields panel">«</Button>
      </div>

      <div style={groupTitle}>Selected fields</div>
      {selected.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '0 4px' }}>—</div>
      )}
      {selected.map(f => fieldRow(f, true))}

      {popular.length > 0 && (
        <>
          <div style={groupTitle}>Popular fields</div>
          {popular.map(f => fieldRow(f, false))}
        </>
      )}
      <div style={groupTitle}>Available fields</div>
      {fields.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '0 4px' }}>
          No mapping discovery on this backend
        </div>
      )}
      {available.map(f => fieldRow(f, false))}
      {/* v0.9.292 — say it when the mapping was clipped. Silence here
          would read as "this is every field", which is the same
          wrong-because-unstated class as the ES honesty envelope. */}
      {typeof fieldsTotal === 'number' && fieldsTotal > fields.length && (
        <div style={{ fontSize: 10.5, color: 'var(--text3)', padding: '6px 4px 0' }}
          title={`This index mapping exposes ${fieldsTotal.toLocaleString()} field paths. The list is capped so the rail stays responsive — type above to find one that isn't shown.`}>
          first {fields.length.toLocaleString()} of {fieldsTotal.toLocaleString()} fields · search to narrow
        </div>
      )}
    </div>
  );
}
