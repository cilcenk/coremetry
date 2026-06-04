import { useEffect, useMemo, useRef, useState } from 'react';
import { useQueries, useQuery } from '@tanstack/react-query';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeFilters } from '@/lib/urlState';
import { timeRangeToNs } from '@/lib/utils';
import { fmtSmart } from '@/lib/chartFmt';
import { evalExpr, exprRefs } from '@/lib/metricFormula';
import { MultiLineChart, type DeployMarker } from '@/components/MultiLineChart';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui';
import { toast } from '@/lib/toast';
import type { MetricInfo, SpanMetricSeries, FilterExpr, TimeRange, Panel, DashboardSummary } from '@/lib/types';

// MetricQueryEditor (v0.7.126 — UX query editor, step 1) — a thin Grafana-
// style builder over the REAL metric query API. Multiple queries (A/B/C…)
// overlay on ONE MultiLineChart. Each query is a metric + aggregation +
// label filters (AND-ed) + group-by (fan-out → one series per group) + step.
// Live preview re-runs via react-query (debounced); the full query model
// serialises to the URL (?mq=) so a built query is a shareable deep link.
// A Builder/Code toggle shows the compiled query DSL and edits it back.
//
// Everything is REAL: api.metricNames (catalog), api.metricQuery (series),
// api.metricLabels (filter-value autocomplete), api.serviceDeploys (deploy
// markers). No fabricated data. The chart is unit-aware (fmtSmart via the
// `unit` prop) and colours group series by a stable label→colour palette
// (seriesColor), so the same group value keeps its colour across re-runs.

type Agg = 'avg' | 'sum' | 'min' | 'max' | 'last' | 'p50' | 'p95' | 'p99';
// Backend-supported aggregations (internal/api metric query). NOTE: p90 /
// count / rate are NOT in the server agg set — offering them would 400, so
// they're intentionally omitted (count is `sum` on a counter; rate needs a
// rate() the backend doesn't expose yet).
const AGGS: Agg[] = ['avg', 'sum', 'min', 'max', 'last', 'p50', 'p95', 'p99'];

const STEPS: { label: string; v: number }[] = [
  { label: 'Auto', v: 0 }, { label: '15s', v: 15 }, { label: '1m', v: 60 }, { label: '5m', v: 300 },
];

// Common metric label keys offered for filter + group-by. Values are
// server-autocompleted per metric (api.metricLabels), so this is just the
// key palette — the operator can still type a custom key.
const LABEL_KEYS = [
  'service.name', 'http.route', 'http.request.method', 'http.response.status_code',
  'status_class', 'rpc.service', 'rpc.method', 'db.system', 'db.operation.name',
  'messaging.system', 'messaging.destination.name', 'host.name', 'deployment.environment',
];

type MGroup = 'http' | 'rpc' | 'db' | 'messaging' | 'runtime' | 'other';
function metricGroup(name: string): MGroup {
  const n = name.toLowerCase();
  if (n.startsWith('http')) return 'http';
  if (n.startsWith('rpc')) return 'rpc';
  if (n.startsWith('db') || n.startsWith('database') || /(redis|oracle|postgres|mysql|mongo)/.test(n)) return 'db';
  if (n.startsWith('messaging') || /(kafka|rabbit|queue|consumer)/.test(n)) return 'messaging';
  if (/^(jvm|process|go\.|system|runtime|dotnet|nodejs|python)/.test(n)) return 'runtime';
  return 'other';
}
const GROUP_FACETS: { key: 'all' | MGroup; label: string }[] = [
  { key: 'all', label: 'All' }, { key: 'http', label: 'HTTP' }, { key: 'rpc', label: 'RPC' },
  { key: 'runtime', label: 'Runtime' }, { key: 'db', label: 'Database' }, { key: 'messaging', label: 'Messaging' },
];

interface MQQuery {
  id: string;            // 'A', 'B', 'C', …
  kind: 'metric' | 'formula'; // formula = derived from other queries (v0.7.128)
  enabled: boolean;
  metric: string;
  unit: string;          // from MetricInfo.unit, for the y-axis + display
  agg: Agg;
  filters: FilterExpr[]; // AND-ed label filters
  groupBy: string[];     // label keys → fan-out
  step: number;          // 0 = auto
  alias: string;         // optional legend alias
  color: string;         // optional per-query colour override ('' = palette)
  expr: string;          // formula expression over other ids, e.g. "A / B * 100"
}
// Panel options (v0.7.128 step 2). logScale + unit feed MultiLineChart props;
// viz toggles the line fill. Heavier viz (bars/stacked) + legend-table are a
// follow-up.
interface MQModel { queries: MQQuery[]; topN: number; logScale: boolean; unit: string; }

const TOPN_DEFAULT = 12;
const ID_LETTERS = 'ABCDEFGHIJ';
function nextId(queries: MQQuery[]): string {
  const used = new Set(queries.map(q => q.id));
  for (const l of ID_LETTERS) if (!used.has(l)) return l;
  return `Q${queries.length + 1}`;
}
function blankQuery(id: string): MQQuery {
  return { id, kind: 'metric', enabled: true, metric: '', unit: '', agg: 'avg', filters: [], groupBy: [], step: 0, alias: '', color: '', expr: '' };
}
function blankFormula(id: string): MQQuery {
  return { ...blankQuery(id), kind: 'formula', expr: '', alias: '' };
}
const EMPTY_MODEL = (): MQModel => ({ queries: [blankQuery('A')], topN: TOPN_DEFAULT, logScale: false, unit: '' });

// ── URL (de)serialisation — the whole model rides one ?mq= param ──────────
function encodeModel(m: MQModel): string {
  return JSON.stringify({
    n: m.topN, ls: m.logScale ? 1 : 0, un: m.unit,
    q: m.queries.map(q => ({
      i: q.id, k: q.kind === 'formula' ? 'f' : 'm', e: q.enabled ? 1 : 0, m: q.metric, u: q.unit, a: q.agg,
      f: q.filters, g: q.groupBy, s: q.step, l: q.alias, c: q.color, x: q.expr,
    })),
  });
}
function decodeModel(s: string | null): MQModel | null {
  if (!s) return null;
  try {
    const o = JSON.parse(s);
    if (!o || !Array.isArray(o.q)) return null;
    const queries: MQQuery[] = o.q.map((q: Record<string, unknown>) => ({
      id: String(q.i ?? 'A'),
      kind: q.k === 'f' ? 'formula' : 'metric',
      enabled: q.e !== 0,
      metric: String(q.m ?? ''),
      unit: String(q.u ?? ''),
      agg: (AGGS.includes(q.a as Agg) ? q.a : 'avg') as Agg,
      filters: Array.isArray(q.f) ? (q.f as FilterExpr[]) : [],
      groupBy: Array.isArray(q.g) ? (q.g as string[]) : [],
      step: typeof q.s === 'number' ? q.s : 0,
      alias: String(q.l ?? ''),
      color: String(q.c ?? ''),
      expr: String(q.x ?? ''),
    }));
    if (!queries.length) return null;
    return { queries, topN: typeof o.n === 'number' ? o.n : TOPN_DEFAULT, logScale: o.ls === 1, unit: String(o.un ?? '') };
  } catch { return null; }
}

// Formula evaluator + ref-extraction live in lib/metricFormula (pure, tested).

// ── Code DSL (Builder/Code toggle) ───────────────────────────────────────
// One line per query: "A: <metric> | agg=p99 | by=a,b | where=k=v;k2=v2 |
// step=1m | alias=foo". Round-trips with the builder model.
function fmtStep(v: number): string { return v === 0 ? 'auto' : STEPS.find(s => s.v === v)?.label ?? `${v}s`; }
function parseStep(s: string): number {
  const t = s.trim().toLowerCase();
  if (t === 'auto' || t === '0' || t === '') return 0;
  return STEPS.find(x => x.label.toLowerCase() === t)?.v ?? (parseInt(t, 10) || 0);
}
// Operators longest-first so ">=" wins over ">"/"=", "NOT IN" over "IN", etc.
const FILTER_OPS: FilterExpr['op'][] = ['NOT EXISTS', 'EXISTS', 'NOT LIKE', 'LIKE', 'NOT IN', 'IN', '>=', '<=', '!=', '=', '>', '<'];
function fmtFilter(f: FilterExpr): string {
  const word = /[A-Z]/.test(f.op);
  if (f.op === 'EXISTS' || f.op === 'NOT EXISTS') return `${f.k} ${f.op}`;
  // Multi-values joined with ',' (NOT '|', which separates DSL segments).
  return word ? `${f.k} ${f.op} ${f.v.join(',')}` : `${f.k}${f.op}${f.v.join(',')}`;
}
function parseFilterTok(tok: string): FilterExpr | null {
  const t = tok.trim();
  for (const op of FILTER_OPS) {
    const word = /[A-Z]/.test(op);
    const needle = word ? ` ${op}` : op;
    const idx = t.indexOf(needle);
    if (idx > 0) {
      const k = t.slice(0, idx).trim();
      if (op === 'EXISTS' || op === 'NOT EXISTS') return k ? { k, op, v: [] } : null;
      const v = t.slice(idx + needle.length).trim().split(',').map(x => x.trim()).filter(Boolean);
      return k && v.length ? { k, op, v } : null;
    }
  }
  return null;
}
function generateDSL(m: MQModel): string {
  return m.queries.map(q => {
    const dis = q.enabled ? '' : '#';
    const alias = q.alias ? ` | alias=${q.alias.replace(/[|\n]/g, ' ')}` : ''; // | / newline break segment parsing
    if (q.kind === 'formula') return `${q.id}${dis}: =${q.expr || '<expr>'}${alias}`;
    const parts = [`agg=${q.agg}`];
    if (q.groupBy.length) parts.push(`by=${q.groupBy.join(',')}`);
    if (q.filters.length) parts.push(`where=${q.filters.map(fmtFilter).join(';')}`);
    if (q.step) parts.push(`step=${fmtStep(q.step)}`);
    return `${q.id}${dis}: ${q.metric || '<metric>'} | ${parts.join(' | ')}${alias}`;
  }).join('\n');
}
function parseDSL(text: string, prev: MQModel): { model?: MQModel; error?: string } {
  const lines = text.split('\n').map(l => l.trim()).filter(Boolean);
  if (!lines.length) return { error: 'No queries' };
  const queries: MQQuery[] = [];
  const warn: string[] = [];
  for (const line of lines) {
    const m = line.match(/^([A-Za-z0-9]+)(#?):\s*(.*)$/);
    if (!m) return { error: `Bad line: ${line}` };
    const id = m[1], enabled = m[2] !== '#';
    const segs = m[3].split('|').map(s => s.trim());
    const metric = segs[0] ?? '';
    // Formula line — first segment is "=<expression>".
    if (metric.startsWith('=')) {
      const fq = blankFormula(id);
      fq.enabled = enabled;
      fq.expr = metric.slice(1).replace('<expr>', '').trim();
      for (const seg of segs.slice(1)) {
        const eq = seg.indexOf('='); if (eq < 0) continue;
        if (seg.slice(0, eq).trim() === 'alias') fq.alias = seg.slice(eq + 1).trim();
      }
      queries.push(fq);
      continue;
    }
    const q = blankQuery(id);
    q.enabled = enabled;
    q.metric = metric === '<metric>' ? '' : metric;
    q.unit = prev.queries.find(p => p.metric === q.metric)?.unit ?? '';
    for (const seg of segs.slice(1)) {
      const eq = seg.indexOf('=');
      if (eq < 0) continue;
      const k = seg.slice(0, eq).trim(), v = seg.slice(eq + 1).trim();
      if (k === 'agg') { if (AGGS.includes(v as Agg)) q.agg = v as Agg; else warn.push(`unknown agg "${v}" on ${id}`); }
      else if (k === 'by') q.groupBy = v ? v.split(',').map(s => s.trim()).filter(Boolean) : [];
      else if (k === 'step') q.step = parseStep(v);
      else if (k === 'alias') q.alias = v;
      else if (k === 'where') {
        const fs: FilterExpr[] = [];
        if (v) for (const tok of v.split(';')) {
          const f = parseFilterTok(tok);
          if (f) fs.push(f);
          else if (tok.trim()) warn.push(`bad filter "${tok.trim()}" on ${id}`);
        }
        q.filters = fs;
      }
    }
    queries.push(q);
  }
  return { model: { queries, topN: prev.topN, logScale: prev.logScale, unit: prev.unit }, error: warn.length ? warn.join('; ') : undefined };
}

// ── Grouped metric picker (searchable + faceted, unit shown) ──────────────
function GroupedMetricPicker({ value, unit, onPick }: {
  value: string; unit: string; onPick: (m: MetricInfo) => void;
}) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState('');
  const [facet, setFacet] = useState<'all' | MGroup>('all');
  const ref = useRef<HTMLDivElement>(null);
  const catalogQ = useQuery({ queryKey: ['metric-catalog'], queryFn: () => api.metricNames(''), staleTime: 60_000 });
  const catalog = catalogQ.data ?? [];

  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const filtered = useMemo(() => {
    const ql = q.trim().toLowerCase();
    return catalog.filter(m =>
      (facet === 'all' || metricGroup(m.name) === facet) &&
      (!ql || m.name.toLowerCase().includes(ql)));
  }, [catalog, q, facet]);

  return (
    <div className="mqe-picker" ref={ref}>
      <button type="button" className="mqe-pickbtn" onClick={() => setOpen(o => !o)}
        aria-label={value ? `Metric: ${value}` : 'Pick a metric'} aria-expanded={open} title={value || 'Pick a metric'}>
        <span className="mqe-pickname">{value || 'Select metric…'}</span>
        {unit && <span className="mqe-unit">{unit}</span>}
        <span className="mqe-caret">▾</span>
      </button>
      {open && (
        <div className="mqe-pop">
          <input autoFocus className="mqe-search" placeholder="Search metrics…" value={q}
            onChange={e => setQ(e.target.value)} />
          <div className="mqe-facets">
            {GROUP_FACETS.map(f => (
              <button key={f.key} type="button" className={'mqe-facet' + (facet === f.key ? ' on' : '')}
                onClick={() => setFacet(f.key)}>{f.label}</button>
            ))}
          </div>
          <div className="mqe-list">
            {catalogQ.isLoading ? <div className="mqe-hint"><Spinner /></div>
              : filtered.length === 0 ? <div className="mqe-hint">No metrics match.</div>
              : filtered.slice(0, 300).map(m => (
                <button key={m.name} type="button" className={'mqe-opt' + (m.name === value ? ' on' : '')}
                  onClick={() => { onPick(m); setOpen(false); }}>
                  <span className="mqe-optname">{m.name}</span>
                  {m.unit && <span className="mqe-unit">{m.unit}</span>}
                </button>
              ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ── Filter chips with metric-label-value autocomplete ─────────────────────
function FilterEditor({ metric, filters, onChange }: {
  metric: string; filters: FilterExpr[]; onChange: (f: FilterExpr[]) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [k, setK] = useState(LABEL_KEYS[0]);
  const [v, setV] = useState('');
  const [vals, setVals] = useState<string[]>([]);

  // Server autocomplete of label values for (metric, k). Debounced.
  useEffect(() => {
    if (!adding || !metric || !k) { setVals([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      api.metricLabels(metric, k, '24h').then(r => { if (!cancelled) setVals(r ?? []); }).catch(() => { if (!cancelled) setVals([]); });
    }, 150);
    return () => { cancelled = true; clearTimeout(t); };
  }, [adding, metric, k]);

  const add = () => {
    if (!v.trim()) return;
    onChange([...filters, { k, op: '=', v: [v.trim()] }]);
    setV(''); setAdding(false);
  };

  return (
    <div className="mqe-filters">
      {filters.map((f, i) => (
        <span key={i} className="mqe-chip" title={`${f.k} ${f.op} ${f.v.join(', ')}`}>
          <span className="mqe-chip-k">{f.k}</span>
          <span className="mqe-chip-op">{f.op}</span>
          <span className="mqe-chip-v">{f.v.join(', ')}</span>
          <button type="button" className="mqe-chip-x" aria-label="Remove filter"
            onClick={() => onChange(filters.filter((_, j) => j !== i))}>×</button>
        </span>
      ))}
      {adding ? (
        <span className="mqe-chip mqe-chip-edit">
          <select value={k} onChange={e => setK(e.target.value)} aria-label="Filter key">
            {LABEL_KEYS.map(key => <option key={key} value={key}>{key}</option>)}
          </select>
          <span className="mqe-chip-op">=</span>
          <input list={`mqe-vals-${metric}-${k}`} value={v} autoFocus placeholder="value"
            onChange={e => setV(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') add(); if (e.key === 'Escape') setAdding(false); }} />
          <datalist id={`mqe-vals-${metric}-${k}`}>{vals.slice(0, 100).map(x => <option key={x} value={x} />)}</datalist>
          <button type="button" className="mqe-chip-add" onClick={add}>Add</button>
        </span>
      ) : (
        <button type="button" className="mqe-addfilter" onClick={() => setAdding(true)} disabled={!metric}
          title={metric ? 'Add a label filter' : 'Pick a metric first'}>+ filter</button>
      )}
    </div>
  );
}

// ── Group-by toggle chips ─────────────────────────────────────────────────
function GroupByEditor({ value, onChange }: { value: string[]; onChange: (g: string[]) => void }) {
  const toggle = (key: string) => onChange(value.includes(key) ? value.filter(x => x !== key) : [...value, key]);
  return (
    <div className="mqe-groupby">
      <span className="mqe-lbl">by</span>
      {LABEL_KEYS.slice(0, 8).map(key => (
        <button key={key} type="button" className={'mqe-gchip' + (value.includes(key) ? ' on' : '')}
          onClick={() => toggle(key)}>{key.replace(/^.*\./, '')}</button>
      ))}
    </div>
  );
}

// ── One query row ─────────────────────────────────────────────────────────
function QueryRow({ q, canRemove, onChange, onDuplicate, onRemove }: {
  q: MQQuery; canRemove: boolean;
  onChange: (q: MQQuery) => void; onDuplicate: () => void; onRemove: () => void;
}) {
  const isFormula = q.kind === 'formula';
  return (
    <div className={'mqe-row' + (q.enabled ? '' : ' off') + (isFormula ? ' formula' : '')}>
      <button type="button" className="mqe-id" title={q.enabled ? 'Disable query' : 'Enable query'}
        onClick={() => onChange({ ...q, enabled: !q.enabled })}>
        <span className="mqe-id-letter">{q.id}</span>
        <span className="mqe-eye">{q.enabled ? '◉' : '○'}</span>
      </button>
      {isFormula ? (
        <input className="mqe-expr" value={q.expr} aria-label="Formula expression"
          placeholder="formula over other queries, e.g.  A / B * 100"
          onChange={e => onChange({ ...q, expr: e.target.value })} />
      ) : (
        <>
          <GroupedMetricPicker value={q.metric} unit={q.unit}
            onPick={m => onChange({ ...q, metric: m.name, unit: m.unit })} />
          <select className="mqe-agg" value={q.agg} onChange={e => onChange({ ...q, agg: e.target.value as Agg })} aria-label="Aggregation">
            {AGGS.map(a => <option key={a} value={a}>{a}</option>)}
          </select>
          <FilterEditor metric={q.metric} filters={q.filters} onChange={f => onChange({ ...q, filters: f })} />
          <GroupByEditor value={q.groupBy} onChange={g => onChange({ ...q, groupBy: g })} />
          <select className="mqe-step" value={q.step} onChange={e => onChange({ ...q, step: Number(e.target.value) })} aria-label="Step">
            {STEPS.map(s => <option key={s.v} value={s.v}>{s.label}</option>)}
          </select>
        </>
      )}
      <input className="mqe-alias" placeholder={isFormula ? 'alias' : 'alias'} value={q.alias}
        onChange={e => onChange({ ...q, alias: e.target.value })} title="Legend alias (optional)" />
      <label className={'mqe-color' + (q.color ? ' set' : '')} title="Series colour override (blank = auto palette)">
        <input type="color" value={q.color || '#7d8590'} aria-label="Series colour"
          onChange={e => onChange({ ...q, color: e.target.value })} />
        {q.color && <button type="button" className="mqe-color-x" aria-label="Clear colour"
          onClick={e => { e.preventDefault(); onChange({ ...q, color: '' }); }}>×</button>}
      </label>
      <div className="mqe-rowact">
        <button type="button" title="Duplicate" aria-label="Duplicate query" onClick={onDuplicate}>⧉</button>
        <button type="button" title="Remove" aria-label="Remove query" onClick={onRemove} disabled={!canRemove}>×</button>
      </div>
    </div>
  );
}

// ── Main editor ───────────────────────────────────────────────────────────
export function MetricQueryEditor({ range }: { range: TimeRange }) {
  const [searchParams, setSearchParams] = useSearchParams();
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const navigate = useNavigate();
  const [model, setModel] = useState<MQModel>(() =>
    decodeModel(searchParams.get('mq')) ?? EMPTY_MODEL());
  const [view, setView] = useState<'builder' | 'code'>('builder');
  const [codeText, setCodeText] = useState('');
  const [codeErr, setCodeErr] = useState<string | null>(null);
  // "Add to dashboard" (step 3) — picker modal state.
  const [dashOpen, setDashOpen] = useState(false);
  const [dashList, setDashList] = useState<DashboardSummary[] | null>(null);
  const [dashTarget, setDashTarget] = useState<string>('new'); // dashboard id | 'new'
  const [newDashName, setNewDashName] = useState('');
  const [savingDash, setSavingDash] = useState(false);

  // Debounced copy of the model that actually drives the fetch + URL write,
  // so typing/clicking doesn't fire a query per keystroke (v0.5.184 posture).
  const [debounced, setDebounced] = useState(model);
  useEffect(() => {
    const t = window.setTimeout(() => setDebounced(model), 250);
    return () => clearTimeout(t);
  }, [model]);

  // Serialise the model to ?mq= (replace — refining a query shouldn't spam
  // history). Coexists with the Metrics page's other params untouched.
  useEffect(() => {
    const enc = encodeModel(debounced);
    setSearchParams(prev => {
      const next = new URLSearchParams(prev);
      next.set('mq', enc);
      return next;
    }, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);

  // Live preview — one fetch per enabled query (react-query, keyed on every
  // input so the cache is correct + shared).
  const results = useQueries({
    queries: debounced.queries.map(q => ({
      queryKey: ['mqe', from, to, q.metric, q.agg, q.groupBy.join(','), encodeFilters(q.filters), q.step] as const,
      queryFn: () => api.metricQuery({
        name: q.metric, agg: q.agg,
        groupBy: q.groupBy.length ? q.groupBy.join(',') : undefined,
        filters: q.filters.length ? encodeFilters(q.filters) : undefined,
        from, to, step: q.step || undefined,
      }),
      enabled: q.enabled && !!q.metric && from > 0,
      staleTime: 30_000,
    })),
  });

  // Combine all enabled queries' series into one overlay: relabel each
  // series to "<query>: key=value, key=value", cap to top-N BY AREA (biggest
  // series win) with a "+N more" note, and let MultiLineChart's stable
  // seriesColor(label) keep each group value the same colour across re-runs.
  // Stabilise the combine on a DATA signature, not the `results` array
  // identity. useQueries returns a fresh array every render; depending on it
  // directly re-ran this memo (and thus rebuilt the non-memoised
  // MultiLineChart) on every keystroke/hover. dataUpdatedAt only changes when
  // a query's data actually changes, so the series array stays referentially
  // stable between unrelated renders. (review-confirmed perf fix)
  const dataSig = results.map(r => (r.data ? r.dataUpdatedAt : 0)).join('|');
  const { series, hidden, unit, colorOverride } = useMemo(() => {
    const metricQs = debounced.queries.filter(q => q.enabled && q.kind === 'metric' && q.metric);
    const producing = debounced.queries.filter(q => q.enabled && (q.kind === 'metric' ? !!q.metric : !!q.expr.trim()));
    const multi = producing.length > 1;
    const colorOverride: Record<string, string> = {};
    const all: SpanMetricSeries[] = [];
    // First series of each metric query — what a formula references by id.
    const repById: Record<string, SpanMetricSeries> = {};
    debounced.queries.forEach((q, qi) => {
      if (!q.enabled || q.kind !== 'metric' || !q.metric) return;
      const data = results[qi]?.data;
      if (!data || !data.length) return;
      repById[q.id] = data[0];
      for (const s of data) {
        const labeled = s.groupKey.map((val, gi) => `${(q.groupBy[gi] ?? 'g').replace(/^.*\./, '')}=${val}`);
        const grp = labeled.join(', ');
        const base = grp || q.alias || q.metric;
        const label = q.alias ? (grp ? `${q.alias} · ${grp}` : q.alias) : (multi ? `${q.id}: ${base}` : base);
        if (q.color) colorOverride[label] = q.color;
        all.push({ groupKey: [label], points: s.points });
      }
    });
    // Formula queries — evaluate the expression per shared time bucket over the
    // referenced metric queries' representative series. Buckets missing any
    // referenced value (or a non-finite result, e.g. /0) become a gap.
    for (const q of debounced.queries) {
      if (!q.enabled || q.kind !== 'formula' || !q.expr.trim()) continue;
      const refs = exprRefs(q.expr).filter(id => id in repById);
      if (!refs.length) continue;
      const valAt: Record<string, Map<number, number>> = {};
      const times = new Set<number>();
      for (const id of refs) { valAt[id] = new Map(repById[id].points.map(p => [p.time, p.value])); for (const p of repById[id].points) times.add(p.time); }
      const pts: { time: number; value: number }[] = [];
      for (const t of [...times].sort((a, b) => a - b)) {
        const vars: Record<string, number> = {};
        let ok = true;
        for (const id of refs) { const v = valAt[id].get(t); if (v === undefined) { ok = false; break; } vars[id] = v; }
        if (!ok) continue;
        const r = evalExpr(q.expr, vars);
        if (r !== null) pts.push({ time: t, value: r });
      }
      if (!pts.length) continue;
      const label = q.alias || `${q.id}: ${q.expr}`;
      if (q.color) colorOverride[label] = q.color;
      all.push({ groupKey: [label], points: pts });
    }
    const ranked = all
      .map(s => ({ s, area: s.points.reduce((a, p) => a + Math.abs(p.value), 0) }))
      .sort((a, b) => b.area - a.area);
    const top = ranked.slice(0, debounced.topN).map(x => x.s);
    // y-unit: an explicit panel override wins; else the shared metric unit
    // (dropped when overlaid metrics disagree, so ms + % don't both read ms).
    const units = new Set(metricQs.map(q => q.unit).filter(Boolean));
    const u = debounced.unit || (units.size === 1 ? [...units][0] : '');
    return { series: top, hidden: Math.max(0, ranked.length - top.length), unit: u, colorOverride };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dataSig, debounced]);
  // Stable per-series colour resolver for MultiLineChart (per-query overrides).
  const colorFn = useMemo(() => (label: string): string | undefined => colorOverride[label], [colorOverride]);

  // Deploy markers — when a single service is pinned via a service.name="x"
  // filter on any enabled query, paint its deploys on the chart (the same
  // affordance the Metrics focused chart uses). (constraint #3)
  const deployService = useMemo(() => {
    for (const q of debounced.queries) {
      if (!q.enabled) continue;
      const f = q.filters.find(x => x.k === 'service.name' && x.op === '=' && x.v.length === 1);
      if (f) return f.v[0];
    }
    return '';
  }, [debounced]);
  const deploysQ = useQuery({
    queryKey: ['mqe-deploys', deployService, from, to],
    queryFn: () => api.serviceDeploys(deployService, { from, to }),
    enabled: !!deployService && from > 0,
    staleTime: 60_000,
  });
  const deploys: DeployMarker[] | undefined = useMemo(() => {
    const d = deploysQ.data;
    return d && d.length
      ? d.map(x => ({ timeUnixNs: x.timeUnixNs, label: x.version, description: `${x.spanCount} spans since` }))
      : undefined;
  }, [deploysQ.data]);

  const anyLoading = results.some((r, i) => debounced.queries[i]?.enabled && debounced.queries[i]?.metric && r.isLoading);
  const anyError = results.find((r, i) => debounced.queries[i]?.enabled && debounced.queries[i]?.metric && r.isError);
  const noMetric = debounced.queries.every(q => !q.enabled || (q.kind === 'metric' ? !q.metric : !q.expr.trim()));

  // ── mutators ──
  const setQuery = (i: number, q: MQQuery) => setModel(m => ({ ...m, queries: m.queries.map((x, j) => j === i ? q : x) }));
  const addQuery = () => setModel(m => ({ ...m, queries: [...m.queries, blankQuery(nextId(m.queries))] }));
  const addFormula = () => setModel(m => ({ ...m, queries: [...m.queries, blankFormula(nextId(m.queries))] }));
  const dupQuery = (i: number) => setModel(m => {
    const src = m.queries[i];
    const copy = { ...src, id: nextId(m.queries), filters: src.filters.map(f => ({ ...f })), groupBy: [...src.groupBy] };
    const next = [...m.queries]; next.splice(i + 1, 0, copy);
    return { ...m, queries: next };
  });
  const removeQuery = (i: number) => setModel(m => ({ ...m, queries: m.queries.filter((_, j) => j !== i) }));

  const openCode = () => { setCodeText(generateDSL(model)); setCodeErr(null); setView('code'); };
  const applyCode = (text: string) => {
    setCodeText(text);
    const { model: parsed, error } = parseDSL(text, model);
    setCodeErr(error ?? null);          // soft warnings (unknown agg / bad filter) still apply
    if (parsed) setModel(parsed);       // only a hard bad-line returns no model
  };

  const retry = () => results.forEach(r => r.refetch());

  // ── Add to dashboard (step 3) — one metric Panel per enabled metric query,
  // saved to a chosen (or new) dashboard via the real updateDashboard model.
  // Formula queries have no panel type yet, so they're skipped (flagged below).
  const buildPanels = (): Panel[] => model.queries
    .filter(q => q.enabled && q.kind === 'metric' && q.metric)
    .map((q): Panel => ({
      id: Math.random().toString(36).slice(2, 10),
      type: 'metric',
      title: q.alias || q.metric,
      width: 2,
      config: {
        metricName: q.metric,
        agg: q.agg,
        groupBy: q.groupBy.length ? q.groupBy.join(',') : undefined,
        step: q.step || undefined,
        filters: q.filters.length ? encodeFilters(q.filters) : undefined,
      },
    }));
  const openDash = () => {
    setDashTarget('new'); setNewDashName(''); setDashOpen(true);
    api.listDashboards().then(d => setDashList(d ?? [])).catch(() => setDashList([]));
  };
  const saveToDash = async () => {
    const panels = buildPanels();
    if (!panels.length) { toast.error('Add at least one metric query — formulas can’t be saved as a panel yet.'); return; }
    setSavingDash(true);
    try {
      let id: string;
      if (dashTarget === 'new') {
        id = (await api.createDashboard({ name: newDashName.trim() || 'New dashboard', description: '', panels, variables: [] })).id;
      } else {
        const dash = await api.getDashboard(dashTarget);
        await api.updateDashboard(dashTarget, { name: dash.name, description: dash.description, panels: [...(dash.panels ?? []), ...panels], variables: dash.variables ?? [] });
        id = dashTarget;
      }
      toast.success(`Added ${panels.length} panel${panels.length === 1 ? '' : 's'} to the dashboard`);
      setDashOpen(false);
      navigate(`/dashboard?id=${encodeURIComponent(id)}`);
    } catch (e) {
      toast.error(`Couldn’t save: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setSavingDash(false);
    }
  };

  return (
    <div className="mqe">
      <div className="mqe-toolbar">
        <div className="segmented">
          <button className={view === 'builder' ? 'active' : ''} onClick={() => setView('builder')}>Builder</button>
          <button className={view === 'code' ? 'active' : ''} onClick={openCode}>Code</button>
        </div>
        <span className="mqe-spacer" />
        <label className="mqe-topn" title="Override the y-axis unit (e.g. % for a ratio formula)">
          unit
          <input className="mqe-unitin" value={model.unit} placeholder="auto"
            onChange={e => setModel(m => ({ ...m, unit: e.target.value }))} />
        </label>
        <label className="mqe-topn" title="Log-scale the y-axis (multi-order-of-magnitude metrics)">
          <input type="checkbox" checked={model.logScale} onChange={e => setModel(m => ({ ...m, logScale: e.target.checked }))} />
          log
        </label>
        <label className="mqe-topn" title="Cap the overlay to the top-N series by area">
          top
          <select value={model.topN} onChange={e => setModel(m => ({ ...m, topN: Number(e.target.value) }))}>
            {[5, 8, 12, 20, 50].map(n => <option key={n} value={n}>{n}</option>)}
          </select>
        </label>
        <Button variant="secondary" size="sm" onClick={openDash} title="Save these queries as panels on a dashboard">
          + Add to dashboard
        </Button>
      </div>

      {view === 'builder' ? (
        <div className="mqe-rows">
          {model.queries.map((q, i) => (
            <QueryRow key={q.id + i} q={q} canRemove={model.queries.length > 1}
              onChange={nq => setQuery(i, nq)} onDuplicate={() => dupQuery(i)} onRemove={() => removeQuery(i)} />
          ))}
          <div className="row" style={{ gap: 8 }}>
            <button type="button" className="mqe-addq" onClick={addQuery}>+ Add query</button>
            <button type="button" className="mqe-addq" onClick={addFormula}>+ Add formula</button>
          </div>
        </div>
      ) : (
        <div className="mqe-code">
          <textarea spellCheck={false} value={codeText} onChange={e => applyCode(e.target.value)}
            rows={Math.max(3, model.queries.length + 1)}
            placeholder={'A: http.server.request.duration | agg=p99 | by=service.name | where=service.name=checkout | step=1m'} />
          <div className="mqe-code-foot">
            {codeErr
              ? <span className="mqe-code-err">⚠ {codeErr}</span>
              : <span className="mqe-hint">Compiled query — edits sync back to the builder. One line per query; prefix the id with # to disable.</span>}
          </div>
        </div>
      )}

      <div className="card mqe-chart">
        <div className="row-between" style={{ marginBottom: 8 }}>
          <h3 style={{ margin: 0, fontSize: 13 }}>Preview</h3>
          <span className="ov-sub">
            {series.length} series{hidden > 0 ? ` · +${hidden} more (capped by area)` : ''}
            {unit ? ` · ${unit}` : ''}
          </span>
        </div>
        {noMetric ? (
          <Empty icon="📈" title="Build a query to preview">
            <p>Pick a metric on query A — add filters, group by a label to fan out into series, and overlay more queries.</p>
          </Empty>
        ) : anyError ? (
          <Empty icon="⚠" title="Metric query failed">
            <p>One of the queries errored or timed out. Try a narrower window or fewer group keys, then retry.</p>
            <p className="mono" style={{ fontSize: 12, color: 'var(--text2)', margin: '8px 0', wordBreak: 'break-word' }}>
              {anyError.error instanceof Error ? anyError.error.message : String(anyError.error)}
            </p>
            <Button variant="secondary" size="sm" onClick={retry}>↻ Retry</Button>
          </Empty>
        ) : anyLoading && series.length === 0 ? (
          <div style={{ height: 320, display: 'grid', placeItems: 'center' }}><Spinner label="Running metric queries…" /></div>
        ) : series.length === 0 ? (
          <Empty icon="∅" title="No data in this window">
            <p>The query returned no series. Widen the time range or relax the filters.</p>
          </Empty>
        ) : (
          <MultiLineChart series={series} unit={unit} height={340} deploys={deploys}
            logScale={model.logScale} colorOf={colorFn} syncKey="mqe-preview" />
        )}
      </div>

      <Modal open={dashOpen} onClose={() => setDashOpen(false)} title="Add to dashboard" size="sm"
        footer={
          <div className="row row-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => setDashOpen(false)}>Cancel</Button>
            <Button variant="primary" size="sm" loading={savingDash} onClick={saveToDash}>Add</Button>
          </div>
        }>
        <div className="stack gap-2" style={{ fontSize: 13 }}>
          <p className="ov-sub" style={{ margin: 0 }}>
            Each enabled <b>metric</b> query becomes a panel. Formula queries are skipped (no panel type yet).
          </p>
          <label className="mqe-dash-opt">
            <input type="radio" name="mqe-dash" checked={dashTarget === 'new'} onChange={() => setDashTarget('new')} />
            <span>New dashboard</span>
          </label>
          {dashTarget === 'new' && (
            <input autoFocus value={newDashName} placeholder="Dashboard name"
              onChange={e => setNewDashName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') saveToDash(); }} style={{ marginLeft: 22 }} />
          )}
          {dashList === null
            ? <div className="ov-sub"><Spinner /></div>
            : dashList.map(d => (
              <label key={d.id} className="mqe-dash-opt">
                <input type="radio" name="mqe-dash" checked={dashTarget === d.id} onChange={() => setDashTarget(d.id)} />
                <span>{d.name}</span>
              </label>
            ))}
        </div>
      </Modal>
    </div>
  );
}
