import { useEffect, useRef, useState, useMemo } from 'react';
import { Topbar } from '@/components/Topbar';
import { Empty, Spinner } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { VirtualList } from '@/components/ui';
import { useDataTable } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import { api } from '@/lib/api';
import type { SQLResult, SchemaTable } from '@/lib/types';
import { getRaw, setRaw, getItem, setItem, STORAGE_KEYS } from '@/lib/storage';

// Embedded SQL playground — admin-only ad-hoc CH query interface.
// Three layers of defence (server allow-list, server readonly=2,
// 60s execution-time cap) plus an audit-log row per query, so
// banking-grade ops can give an SRE this surface without losing
// the audit trail.
//
// Layout: schema browser left, editor + results right.

type Backend = 'clickhouse' | 'elasticsearch';

// ES-flavoured sample queries — shown only when the operator
// switches the playground to the Elasticsearch tab. Quoted
// index pattern + SQL the ES plugin supports (no joins, no
// nested aggregates past one level).
const ES_SAMPLES: { label: string; sql: string }[] = [
  { label: 'Top error messages (last 1h)', sql:
    `SELECT message, COUNT(*) AS errs
FROM "logs-*"
WHERE "log.level" = 'ERROR' AND "@timestamp" > NOW() - INTERVAL 1 HOURS
GROUP BY message
ORDER BY errs DESC
LIMIT 50` },
  { label: 'Error rate per service (last 1h)', sql:
    `SELECT "service.name", COUNT(*) AS total,
       SUM(CASE WHEN "log.level" = 'ERROR' THEN 1 ELSE 0 END) AS errs
FROM "logs-*"
WHERE "@timestamp" > NOW() - INTERVAL 1 HOURS
GROUP BY "service.name"
ORDER BY errs DESC` },
  { label: 'Recent logs for a trace', sql:
    `SELECT "@timestamp", "service.name", "log.level", message
FROM "logs-*"
WHERE "trace.id" = 'PASTE_TRACE_ID_HERE'
ORDER BY "@timestamp" DESC
LIMIT 100` },
  { label: 'Show columns of the index', sql:
    `DESCRIBE "logs-*"` },
];

const SAMPLES: { label: string; sql: string }[] = [
  { label: 'Top services by spans (last 1h)', sql:
    `SELECT service_name, count() AS spans, countIf(status_code='error') AS errors
FROM spans WHERE time >= now() - INTERVAL 1 HOUR
GROUP BY service_name ORDER BY spans DESC LIMIT 50` },
  { label: 'Slowest endpoints (last 1h, p99)', sql:
    `SELECT service_name, name,
       quantile(0.99)(duration) / 1e6 AS p99_ms, count() AS spans
FROM spans WHERE time >= now() - INTERVAL 1 HOUR
GROUP BY service_name, name HAVING spans >= 100
ORDER BY p99_ms DESC LIMIT 30` },
  { label: 'Storage by table', sql:
    `SELECT table,
       formatReadableSize(sum(bytes_on_disk)) AS size,
       sum(rows) AS rows,
       round(sum(data_compressed_bytes) / nullIf(sum(data_uncompressed_bytes), 0) * 100, 1) AS compress_pct
FROM system.parts WHERE database = currentDatabase() AND active = 1
GROUP BY table ORDER BY sum(bytes_on_disk) DESC` },
  { label: 'Trace IDs with most spans (last 10m)', sql:
    `SELECT trace_id, count() AS span_count
FROM spans WHERE time >= now() - INTERVAL 10 MINUTE
GROUP BY trace_id ORDER BY span_count DESC LIMIT 20` },
  { label: 'Recent slow ClickHouse queries', sql:
    `SELECT event_time, query_duration_ms,
       formatReadableSize(read_bytes) AS read_bytes,
       substring(query, 1, 80) AS q
FROM system.query_log WHERE event_time >= now() - INTERVAL 30 MINUTE
  AND type = 'QueryFinish'
ORDER BY query_duration_ms DESC LIMIT 30` },
];

const HISTORY_KEY = 'coremetry-sql-history';
const HISTORY_MAX = 30;

export default function SQLPlaygroundPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin';

  const [schema, setSchema] = useState<SchemaTable[] | undefined>(undefined);
  const [openTables, setOpenTables] = useState<Set<string>>(new Set());

  const [backend, setBackend] = useState<Backend>(() => {
    const v = getRaw(STORAGE_KEYS.sqlBackend) as Backend | null;
    return v === 'elasticsearch' ? 'elasticsearch' : 'clickhouse';
  });
  const [query, setQuery] = useState<string>(
    'SELECT count() FROM spans WHERE time >= now() - INTERVAL 5 MINUTE');
  const [result, setResult] = useState<SQLResult | null | undefined>(undefined);
  const [running, setRunning] = useState(false);

  const switchBackend = (next: Backend) => {
    setBackend(next);
    setRaw(STORAGE_KEYS.sqlBackend, next);
    setResult(undefined);
    // Swap the starter query so the operator doesn't try to run
    // CH SQL against ES (or vice-versa) and hit a parse error.
    if (next === 'elasticsearch') {
      setQuery(ES_SAMPLES[0].sql);
    } else {
      setQuery('SELECT count() FROM spans WHERE time >= now() - INTERVAL 5 MINUTE');
    }
  };

  const editorRef = useRef<HTMLTextAreaElement>(null);
  const [history, setHistory] = useState<string[]>([]);

  useEffect(() => {
    if (!isAdmin) return;
    api.sqlSchema().then(setSchema).catch(() => setSchema([]));
    const h = getItem<string[] | null>(HISTORY_KEY, null);
    if (h) setHistory(h);
  }, [isAdmin]);

  const run = async () => {
    if (running) return;
    const q = query.trim();
    if (!q) return;
    setRunning(true);
    setResult(undefined);
    try {
      const res = backend === 'elasticsearch'
        ? await api.elasticSqlQuery(q)
        : await api.sqlQuery(q);
      setResult(res);
      // Push to history (dedupe + cap)
      const next = [q, ...history.filter(h => h !== q)].slice(0, HISTORY_MAX);
      setHistory(next);
      setItem(HISTORY_KEY, next);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      setResult({ columns: [], rows: [], rowCount: 0, tookMs: 0, error: msg });
    } finally {
      setRunning(false);
    }
  };

  // Cmd/Ctrl+Enter triggers run from inside the editor.
  const onEditorKey = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault();
      run();
    }
  };

  const insertAtCursor = (text: string) => {
    const ta = editorRef.current;
    if (!ta) { setQuery(q => q + text); return; }
    const start = ta.selectionStart, end = ta.selectionEnd;
    const next = query.slice(0, start) + text + query.slice(end);
    setQuery(next);
    setTimeout(() => {
      ta.focus();
      const pos = start + text.length;
      ta.setSelectionRange(pos, pos);
    }, 0);
  };

  if (!isAdmin) {
    return (
      <>
        <Topbar title="SQL playground" />
        <div id="content"><Empty icon="◇" title="Admin only">
          The SQL playground gives ad-hoc read-only access to the
          underlying ClickHouse instance. Restricted to admin role.
        </Empty></div>
      </>
    );
  }

  return (
    <>
      <Topbar title="SQL playground" />
      <div id="content" style={{ display: 'flex', gap: 12, height: 'calc(100vh - 80px)' }}>
        {/* Schema sidebar — CH only. The Elasticsearch backend's
            queryable fields are surfaced via /logs page's
            ƒ Fields button; the playground sidebar would just
            duplicate that and confuse the operator with two
            different schema sources. */}
        {backend === 'clickhouse' && (
        <div style={{
          width: 240, flexShrink: 0,
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 6, padding: 8, overflow: 'auto',
        }}>
          <div style={{
            fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase',
            letterSpacing: 0.4, marginBottom: 6,
          }}>
            Schema
          </div>
          {schema === undefined && <Spinner />}
          {schema && schema.length === 0 && (
            <div style={{ fontSize: 11, color: 'var(--text3)' }}>
              No tables visible to this user.
            </div>
          )}
          {schema && schema.map(t => {
            const open = openTables.has(t.table);
            return (
              <div key={t.table} style={{ marginBottom: 2 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                  <button type="button"
                    onClick={() => setOpenTables(s => {
                      const next = new Set(s);
                      if (open) next.delete(t.table); else next.add(t.table);
                      return next;
                    })}
                    style={{
                      background: 'transparent', border: 'none', cursor: 'pointer',
                      color: 'var(--text2)', fontSize: 10, padding: 0, width: 14,
                    }}>
                    {open ? '▾' : '▸'}
                  </button>
                  <button type="button"
                    onClick={() => setQuery(`SELECT * FROM ${t.table} LIMIT 100`)}
                    title={`Replace editor with: SELECT * FROM ${t.table} LIMIT 100`}
                    style={{
                      flex: 1, textAlign: 'left',
                      background: 'transparent', border: 'none', cursor: 'pointer',
                      color: 'var(--text)', fontSize: 11, padding: '2px 0',
                      fontFamily: 'monospace',
                    }}>
                    {t.table}
                  </button>
                </div>
                {open && (
                  <div style={{ paddingLeft: 18 }}>
                    {t.columns.map(c => (
                      <button key={c.name} type="button"
                        onClick={() => insertAtCursor(c.name)}
                        style={{
                          display: 'block', width: '100%',
                          background: 'transparent', border: 'none', cursor: 'pointer',
                          textAlign: 'left', padding: '1px 0',
                          fontSize: 10, color: 'var(--text2)',
                          fontFamily: 'monospace',
                        }}
                        title={c.type}>
                        {c.name} <span style={{ color: 'var(--text3)' }}>{c.type}</span>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
        )}

        {/* ── Editor + results ─────────────────────────────────── */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 8, minWidth: 0 }}>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            {/* Backend toggle (v0.5.138). Persists to local-
                storage so the operator's last choice survives
                reload. Swaps sample queries + run path; the
                starter query is replaced to avoid a parse error
                on first run. */}
            <div style={{
              display: 'flex', border: '1px solid var(--border)',
              borderRadius: 6, overflow: 'hidden',
            }}>
              <button type="button"
                onClick={() => switchBackend('clickhouse')}
                className={backend === 'clickhouse' ? '' : 'sec'}
                style={{ borderRadius: 0, padding: '5px 10px', fontSize: 11 }}>
                ClickHouse
              </button>
              <button type="button"
                onClick={() => switchBackend('elasticsearch')}
                className={backend === 'elasticsearch' ? '' : 'sec'}
                style={{ borderRadius: 0, padding: '5px 10px', fontSize: 11, borderLeft: '1px solid var(--border)' }}
                title="Forwards the SQL to Elasticsearch's _sql endpoint. Requires the logs backend to be Elasticsearch.">
                Elasticsearch
              </button>
            </div>
            <button type="button" onClick={run} disabled={running}
              style={{ padding: '6px 14px', fontSize: 12, fontWeight: 600 }}>
              {running ? 'Running…' : 'Run'} <span style={{ color: 'var(--text3)' }}>⌘↵</span>
            </button>
            <select onChange={e => {
              const list = backend === 'elasticsearch' ? ES_SAMPLES : SAMPLES;
              const s = list.find(x => x.label === e.target.value);
              if (s) setQuery(s.sql);
              e.target.value = '';
            }}
              defaultValue=""
              style={{ fontSize: 11 }}>
              <option value="">Sample queries…</option>
              {(backend === 'elasticsearch' ? ES_SAMPLES : SAMPLES)
                .map(s => <option key={s.label} value={s.label}>{s.label}</option>)}
            </select>
            <select onChange={e => {
              if (e.target.value) setQuery(e.target.value);
              e.target.value = '';
            }} defaultValue="" style={{ fontSize: 11 }}>
              <option value="">History…</option>
              {history.map((q, i) => (
                <option key={i} value={q}>
                  {q.length > 80 ? q.slice(0, 77) + '…' : q}
                </option>
              ))}
            </select>
            <span style={{ flex: 1 }} />
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              read-only · 60s timeout · 10k row cap
            </span>
          </div>

          <textarea ref={editorRef}
            data-shortcut-search
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={onEditorKey}
            spellCheck={false}
            style={{
              flexShrink: 0, height: 220,
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              fontSize: 12, lineHeight: 1.5,
              padding: 10, background: 'var(--bg1)',
              color: 'var(--text)',
              border: '1px solid var(--border)', borderRadius: 6,
              resize: 'vertical', outline: 'none',
            }} />

          {result === undefined && !running && (
            <div style={{ color: 'var(--text3)', fontSize: 12 }}>
              Run a query to see results.
            </div>
          )}
          {running && <Spinner />}
          {result && result.error && (
            <div style={{
              fontSize: 12, color: 'var(--err)', padding: '8px 12px',
              background: 'rgba(220,38,38,0.08)',
              border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
              fontFamily: 'monospace', whiteSpace: 'pre-wrap',
            }}>
              {result.error}
            </div>
          )}
          {result && !result.error && (
            <ResultTable result={result} />
          )}
        </div>
      </div>
    </>
  );
}

// One result row is a positional tuple (rows[i][colIndex]); we
// keep that shape and let the column accessors index by position.
type ResultRow = unknown[];

const RESULT_COL_W = 160; // default column width (px) for the fixed grid layout

function ResultTable({ result }: { result: SQLResult }) {
  // Virtualised result grid. The previous implementation
  // rendered every result row to the DOM, which froze the
  // browser at the 10k cap. With VirtualList only the visible
  // ~30 rows touch the DOM; scrolling stays buttery even on a
  // worst-case query.
  //
  // Layout: explicit CSS Grid with one column per result column
  // so headers and body rows share the same template — matches
  // the visual `<table>` look without needing table semantics.
  //
  // Shared-primitive adoption (v0.7.x): the body MUST stay
  // virtualised (10k-row cap), so we don't render a real
  // <table>/<tbody>. Instead we drive sort + per-column resize
  // from the useDataTable hook and reflect its state onto this
  // grid header + VirtualList body. Columns are dynamic (one per
  // result column), so the column defs are built per-result with
  // a positional accessor.
  const dt = useDataTable<ResultRow>({
    storageKey: 'adminsql-result',
    columns: useMemo<DataTableColumn<ResultRow>[]>(() =>
      result.columns.map((name, idx): DataTableColumn<ResultRow> => ({
        id: `c${idx}`,
        label: name,
        width: RESULT_COL_W,
        // Positional accessor: index into the row tuple. Coerce to a
        // number when the cell parses cleanly as one (so numeric
        // columns sort numerically), else fall back to a string;
        // null/object cells sink last via the primitive's null rule.
        sortValue: (row) => {
          const v = row[idx];
          if (v === null || v === undefined) return null;
          if (typeof v === 'number') return v;
          if (typeof v === 'object') return null;
          const s = String(v);
          const n = Number(s);
          return s !== '' && Number.isFinite(n) ? n : s;
        },
        naturalDir: 'asc',
      })),
      [result.columns]),
    rows: result.rows,
  });

  // Grid template driven by the primitive's per-column widths so a
  // resize drag is reflected in both header + body. Fixed px widths
  // (not 1fr) keep the columns stable while resizing.
  const gridTemplate = dt.columns
    .map(c => `${dt.colWidths[c.id] ?? c.width ?? RESULT_COL_W}px`)
    .join(' ');
  const ROW_HEIGHT = 22; // matches the previous td vertical padding × 2 + line height

  return (
    <div style={{
      flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg1)',
      overflow: 'hidden',
    }}>
      {/* Meta strip — query stats, sticky cap warning. */}
      <div style={{
        background: 'var(--bg2)',
        padding: '6px 12px', borderBottom: '1px solid var(--border)',
        fontSize: 11, color: 'var(--text3)', display: 'flex', gap: 12,
        flexShrink: 0,
      }}>
        <span><b style={{ color: 'var(--text)' }}>{result.rowCount}</b> rows</span>
        <span>·</span>
        <span><b style={{ color: 'var(--text)' }}>{result.tookMs}</b> ms</span>
        <span>·</span>
        <span><b style={{ color: 'var(--text)' }}>{result.columns.length}</b> columns</span>
        {result.rowCount === 10000 && (
          <span style={{ color: 'var(--warn)' }}>
            (capped at 10k rows — narrow your query for the full set)
          </span>
        )}
      </div>

      {/* Scroll container: header + virtualised body share one
          horizontal scroll so wide results pan together. The grid
          is at least as wide as the sum of the column widths. */}
      <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', overflowX: 'auto' }}>
        {/* Header row — same gridTemplate as data rows so columns
            line up. Click a header to sort (client-side, via the
            shared primitive); drag the right edge to resize. */}
        <div style={{
          display: 'grid',
          gridTemplateColumns: gridTemplate,
          minWidth: 'max-content',
          background: 'var(--bg2)',
          borderBottom: '1px solid var(--border)',
          fontFamily: 'monospace', fontSize: 11, fontWeight: 600,
          flexShrink: 0,
        }}>
          {dt.columns.map(c => {
            const active = dt.sort.id === c.id;
            return (
              <div key={c.id}
                onClick={() => dt.toggleSort(c.id)}
                title={`${c.label} — click to sort, drag right edge to resize`}
                style={{
                  position: 'relative', padding: '4px 8px',
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  cursor: 'pointer', userSelect: 'none',
                  color: active ? 'var(--accent2)' : undefined,
                }}>
                {c.label}
                <span style={{ marginLeft: 4, opacity: 0.7 }}>
                  {active ? (dt.sort.dir === 'desc' ? '▼' : '▲') : '↕'}
                </span>
                {/* Resize hot-zone on the right edge. stopPropagation
                    on click so a resize drag never triggers a sort. */}
                <span
                  onMouseDown={e => dt.startResize(c.id, e)}
                  onClick={e => e.stopPropagation()}
                  title="Drag to resize"
                  style={{
                    position: 'absolute', top: 0, right: 0, width: 6, height: '100%',
                    cursor: 'col-resize', userSelect: 'none',
                  }} />
              </div>
            );
          })}
        </div>

        {/* Virtualised body. height: '100%' inside flex container
            fills the remaining space; the inner scroll lives on
            the VirtualList element. */}
        <VirtualList
          items={dt.sortedRows}
          rowHeight={ROW_HEIGHT}
          height="100%"
          overscan={12}
          renderRow={(row, i) => (
            <div style={{
              display: 'grid',
              gridTemplateColumns: gridTemplate,
              minWidth: 'max-content',
              borderBottom: '1px solid var(--border)',
              fontSize: 11, fontFamily: 'monospace',
              background: i % 2 === 0 ? 'transparent' : 'var(--bg0)',
            }}>
              {row.map((v, j) => (
                <div key={j} style={{
                  padding: '3px 8px', whiteSpace: 'nowrap',
                  overflow: 'hidden', textOverflow: 'ellipsis',
                }} title={String(v)}>
                  {v === null ? <span style={{ color: 'var(--text3)' }}>NULL</span>
                              : typeof v === 'object' ? JSON.stringify(v)
                              : String(v)}
                </div>
              ))}
            </div>
          )}
        />
      </div>
    </div>
  );
}
