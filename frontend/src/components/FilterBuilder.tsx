import { useEffect, useMemo, useState } from 'react';
import { attrKeyWindowParams } from '@/lib/attrKeyWindow';
import { useEscLayer } from '@/lib/escLayer';
import { Combobox } from './Combobox';
import { ActionRow, Button, Chip } from '@/components/ui';
import { api } from '@/lib/api';
import type { FilterExpr, FilterOp } from '@/lib/types';
import { useAttributeKeys } from '@/lib/useAttributeKeys';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs } from '@/lib/utils';

const OPS: FilterOp[] = ['=', '!=', 'LIKE', 'NOT LIKE', 'IN', 'NOT IN', '>', '>=', '<', '<=', 'EXISTS', 'NOT EXISTS'];

const NEEDS_VALUE: Record<FilterOp, boolean> = {
  '=': true, '!=': true,
  'LIKE': true, 'NOT LIKE': true,
  'IN': true, 'NOT IN': true,
  '>': true, '>=': true, '<': true, '<=': true,
  'EXISTS': false, 'NOT EXISTS': false,
};

export function FilterBuilder({ value, onChange, suggestedValues, metricName, metricService }: {
  value: FilterExpr[];
  onChange: (next: FilterExpr[]) => void;
  /** Optional value-suggestions per key (e.g. service names). */
  suggestedValues?: Record<string, string[]>;
  /**
   * v0.9.1200 — metrik-kaynak modu. Verilince anahtar/değer önerileri span
   * attribute TARAMASINDAN değil, o metrikte fiilen GÖRÜLMÜŞ etiketlerden
   * gelir (metricAttrKeys/metricLabels — ikisi de metricsource seam'inde,
   * yani VM backend'inde VM'in /api/v1/labels'ına gider). Span anahtarı
   * (`http.route` gibi noktalı) VM'de etiket olarak var olmayabilir ve
   * sessizce hiç eşleşmezdi — önerinin kaynağı sorgunun kaynağıyla aynı
   * olmak zorunda.
   */
  metricName?: string;
  metricService?: string;
}) {
  const [draft, setDraft] = useState<FilterExpr | null>(null);
  const metricMode = !!metricName?.trim();

  // Metrik modunda anahtarlar metrikte görülmüş etiketler. Sunucu 60 sn
  // cache'li; fetch metrik/servis değişince tazelenir.
  const [metricKeys, setMetricKeys] = useState<string[]>([]);
  useEffect(() => {
    if (!metricMode) { setMetricKeys([]); return; }
    let cancelled = false;
    api.metricAttrKeys(metricName!.trim(), metricService ?? '')
      .then(keys => { if (!cancelled) setMetricKeys(keys ?? []); })
      .catch(() => { if (!cancelled) setMetricKeys([]); });
    return () => { cancelled = true; };
  }, [metricMode, metricName, metricService]);

  // v0.9.933 — keşif `useAttributeKeys`e taşındı: /traces'in aggregate
  // "group by attribute" kutusu da AYNI listeyi kullanabilsin diye (Ö14).
  // İki kopya olsaydı biri er geç bayatlardı — ve bayatlayanın belirtisi
  // "bazı anahtarlar bir kutuda var, diğerinde yok" gibi açıklanamaz bir
  // tutarsızlık olurdu.
  // v0.9.953 (F3/Ö14c) — ANAHTAR keşfi de sayfanın penceresinden.
  //
  // DEĞER autocomplete'i bunu v0.8.x'te (trace-query gap-1) zaten
  // yapıyordu — aşağıdaki DraftEditor'ın useUrlRange çağrısı ve yorumu
  // ("not a hard-coded 1h, so a value picked on a 24h/7d view matches
  // the rows actually shown") birebir bu gerekçeyi yazıyor. ANAHTAR
  // tarafı geride kalmıştı: aynı kutuda değerler doğru pencereden,
  // anahtarlar sabit 1 saatten geliyordu.
  //
  // Pencere BASAMAKLI (attrKeyWindow): ham geçirmek sunucunun 60 sn'lik
  // cache anahtarını her dokunuşta ıskalatırdı (v0.8.270).
  const [keyRange] = useUrlRange();
  const attrWindow = useMemo(() => attrKeyWindowParams(keyRange), [keyRange]);
  const { keys: spanKeys, observed: observedKeys } = useAttributeKeys(value, attrWindow, !metricMode);
  const allKeys = metricMode ? metricKeys : spanKeys;
  // Top-5 hint surfaced under the picker so the operator sees
  // "what's heavy right now" without scrolling the dropdown.
  // (Metrik etiket ucu sayı taşımaz — metrik modunda ipucu satırı yok.)
  const topHints = metricMode ? [] : observedKeys.slice(0, 5);

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
          metricName={metricMode ? metricName!.trim() : undefined}
        />
      )}
    </div>
  );
}

function DraftEditor({ draft, onSave, onCancel, suggestedValues, keyOptions, topHints, metricName }: {
  draft: FilterExpr;
  onSave: (f: FilterExpr) => void;
  onCancel: () => void;
  suggestedValues?: Record<string, string[]>;
  keyOptions: string[];
  // v0.5.261 — top-5 observed-by-count attribute keys, used as a
  // one-click hint row above the picker so the operator doesn't
  // have to scroll the dropdown to find what's heavy.
  topHints: { key: string; count: number }[];
  // v0.9.1200 — verilince değer autocomplete'i metricLabels'tan.
  metricName?: string;
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
  // v0.9.950 (E2/Ö28) — KATMAN. Editör yalnız aktif düzenleme sırasında
  // mount, yani `true` doğru koşul; üstünde açılan bir öneri popover'ı
  // ilk Esc'i alır.
  useEscLayer(true, onCancel);

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
      // v0.9.1200 — metrik modunda değerler metriğin ETİKET uzayından
      // (metricLabels; VM'de /api/v1/label/<k>/values). Uçta substring
      // paramı yok (tavan 200) — daraltma istemcide.
      const fetchValues: Promise<string[]> = metricName
        ? api.metricLabels(metricName, k).then(vals => {
            const t = typedValue.toLowerCase();
            const all = vals ?? [];
            return t ? all.filter(v => v.toLowerCase().includes(t)) : all;
          })
        : api.attributeValues(k, '1h', 200, typedValue || undefined, rangeNs)
            .then(rows => (rows ?? []).map(r => r.value));
      fetchValues
        .then(vals => { if (!cancelled) setLiveValues(vals); })
        .catch(() => { if (!cancelled) setLiveValues([]); })
        .finally(() => { if (!cancelled) setLiveLoading(false); });
    }, 200);
    return () => { cancelled = true; clearTimeout(handle); };
  }, [local.k, typedValue, rangeNs, metricName]);

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
              <Chip key={h.key} size="xs" className="mono"
                onClick={() => setLocal({ ...local, k: h.key })}
                title={`${h.count.toLocaleString()} spans carry this attribute under the active filter set`}>
                {h.key}
                <span style={{ color: 'var(--text3)' }}>{fmtCount(h.count)}</span>
              </Chip>
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
      {/* v0.9.1007 (M5/O8) — sıra TERSTİ (Add solda, Cancel sağda) ve
          kapsayıcı `justify-content` bile tanımlamadığı için satır
          ayrıca sola yaslıydı. ActionRow ikisini de yapıyor. */}
      <ActionRow
        secondary={<Button variant="secondary" onClick={onCancel}>Cancel</Button>}
        confirm={<Button variant="primary" onClick={submit}>Add</Button>} />
    </div>
  );
}

function formatValues(v: string[], op: string): string {
  if (op === 'IN' || op === 'NOT IN') return v.join(', ');
  return v[0] ?? '';
}
