import { useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { MultiLineChart } from '@/components/MultiLineChart';
import { IconLock } from '@/components/icons';
import { timeRangeToNs } from '@/lib/utils';
import type { TimeRange, SpanMetricSeries } from '@/lib/types';

// AdminQuery — v0.5.265. Coremetry's unified query language
// (DQL-lite) playground. Operator types a pipe-shaped query;
// backend compiles + runs + returns the series + the actual
// CH SQL it executed so the operator can see what hit the
// database.
//
// Admin-only — same posture as /admin/sql.

const SAMPLE_QUERIES = [
  {
    label: 'Spans count by minute',
    text: `spans | summarize count() by bin(time, 1m)`,
  },
  {
    label: 'P99 latency for one service',
    text: `spans | filter service.name == "api-gateway" | summarize p99(duration_ms) by bin(time, 1m)`,
  },
  {
    label: 'Error rate per minute',
    text: `spans | summarize error_rate() by bin(time, 1m)`,
  },
  {
    label: 'P99 split by service',
    text: `spans | summarize p99(duration_ms) by service.name, bin(time, 1m)`,
  },
  {
    label: 'HTTP 5xx counts on 5s buckets',
    text: `spans | filter http.status_code >= "500" | summarize count() by bin(time, 5s)`,
  },
  {
    label: 'Metric: HTTP server duration p99 by service',
    text: `metrics http.server.duration | summarize p99(value) by service.name, bin(time, 1m)`,
  },
  {
    label: 'Logs: count by service',
    text: `logs | summarize count() by service.name, bin(time, 1m)`,
  },
  {
    label: 'Logs: count by severity',
    text: `logs | summarize count() by severity, bin(time, 1m)`,
  },
  {
    label: 'Cross-signal: logs in errored traces',
    text: `spans | filter status_code == "error" | join logs on trace.id | summarize count() by bin(time, 1m)`,
  },
  {
    label: 'Cross-signal: spans in slow checkouts',
    text: `spans | filter http.route == "/checkout" | filter duration_ms > "1000" | join spans on trace.id | summarize p99(duration_ms) by service.name, bin(time, 1m)`,
  },
];

interface QueryResult {
  plan: unknown;
  sql: string;
  series: SpanMetricSeries[];
  window: { fromNs: number; toNs: number };
}

export default function AdminQueryPage() {
  const { user } = useAuth();
  const [query, setQuery] = useState(SAMPLE_QUERIES[1].text);
  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [result, setResult] = useState<QueryResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (user && user.role !== 'admin') {
    return (
      <>
        <Topbar title="Query" />
        <div id="content">
          <Empty icon={<IconLock size={28} />} title="Admin access required">
            The unified query playground is only available to administrators.
          </Empty>
        </div>
      </>
    );
  }

  const run = async () => {
    setBusy(true);
    setError(null);
    setResult(null);
    const { from, to } = timeRangeToNs(range);
    try {
      const r = await fetch('/api/query/run', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ query, from, to }),
      });
      if (!r.ok) {
        const body = await r.json().catch(() => ({}));
        throw new Error(body.error ?? `HTTP ${r.status}`);
      }
      const data = (await r.json()) as QueryResult;
      setResult(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Topbar title="Query" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 10, fontSize: 12, color: 'var(--text2)' }}>
          Coremetry's <b>unified query language</b> — Kusto-flavoured pipe shape.
          One syntax across spans + metrics + logs. Tables: <code>spans</code>,{' '}
          <code>metrics &lt;name&gt;</code>, <code>logs</code>. Operators:{' '}
          <code>filter</code>, <code>summarize</code>, <code>join &lt;target&gt; on trace.id</code>.{' '}
          Aggregations: <code>count()</code>, <code>rate()</code>,{' '}
          <code>error_rate()</code>, <code>p50</code>/<code>p95</code>/
          <code>p99</code>/<code>avg</code>/<code>max</code>/<code>min(field)</code>.
          Time bucket: <code>by bin(time, 1m)</code> (s/m/h/d). Cross-signal join
          resolves up to 1000 trace_ids from the source, then runs the target
          aggregation scoped to that set.
        </div>

        <div className="controls" style={{ marginBottom: 10, gap: 6, flexWrap: 'wrap' }}>
          <span style={{ fontSize: 11, color: 'var(--text3)' }}>Samples:</span>
          {SAMPLE_QUERIES.map(s => (
            <button key={s.label} className="sec"
              onClick={() => setQuery(s.text)}
              style={{ fontSize: 11, padding: '3px 8px' }}
              title={s.text}>
              {s.label}
            </button>
          ))}
        </div>

        <textarea
          value={query}
          onChange={e => setQuery(e.target.value)}
          rows={4}
          style={{
            width: '100%', fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            fontSize: 13, padding: 10, marginBottom: 8,
            background: 'var(--bg1)', border: '1px solid var(--border)',
            borderRadius: 6, color: 'var(--text)', resize: 'vertical',
          }}
          onKeyDown={e => {
            if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') run();
          }} />
        <div style={{ display: 'flex', gap: 8, marginBottom: 14 }}>
          <button onClick={run} disabled={busy}>
            {busy ? 'Running…' : 'Run (⌘+Enter)'}
          </button>
          <span style={{ flex: 1 }} />
          <span style={{ color: 'var(--text3)', fontSize: 11 }}>
            Range applies to every query · admin-only · same auth as /admin/sql
          </span>
        </div>

        {error && (
          <div style={{
            color: 'var(--err)', fontSize: 13,
            padding: '8px 12px', marginBottom: 10,
            background: 'rgba(220,38,38,0.08)',
            border: '1px solid rgba(220,38,38,0.3)', borderRadius: 4,
            fontFamily: 'ui-monospace, monospace',
          }}>
            ✗ {error}
          </div>
        )}

        {busy && <Spinner />}

        {result && (
          <>
            {/* Chart */}
            <div style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 8, padding: 14, marginBottom: 12,
            }}>
              {result.series.length === 0 ? (
                <Empty icon="◎" title="Query ran but produced no data" />
              ) : (
                <MultiLineChart series={result.series} unit="" height={260} />
              )}
            </div>

            {/* SQL preview — operator transparency */}
            <details open>
              <summary style={{
                cursor: 'pointer', fontSize: 12, color: 'var(--text2)',
                marginBottom: 6, userSelect: 'none',
              }}>
                ▾ Generated ClickHouse SQL
              </summary>
              <pre style={{
                fontSize: 11, padding: 12, borderRadius: 6,
                background: 'var(--bg0)', border: '1px solid var(--border)',
                fontFamily: 'ui-monospace, monospace',
                whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                color: 'var(--text2)',
                overflow: 'auto', maxHeight: 280,
              }}>{result.sql}</pre>
            </details>

            <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)' }}>
              {result.series.length} series · window{' '}
              {new Date(result.window.fromNs / 1e6).toLocaleString()} →{' '}
              {new Date(result.window.toNs / 1e6).toLocaleString()}
            </div>
          </>
        )}
      </div>
    </>
  );
}
