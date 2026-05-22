import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { Sparkline } from '@/components/Sparkline';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import type { TimeRange, SpanMetricsServicesResponse, SpanMetricServiceRow } from '@/lib/types';

// /span-metrics — per-service RED derived from the
// spanmetrics processor (or any otelcol pipeline emitting the
// traces.spanmetrics.calls.total counter + duration histogram).
// Mirrors the /services list but the source is metric_points
// rather than the span-derived MVs, so operators whose data
// path is "Collector spanmetrics → OTLP /v1/metrics" see a
// first-class APM surface even when there's no application-side
// tracing.
//
// Backend self-detects the metric name (handles
// traces.spanmetrics.calls.total / spanmetrics.calls / calls
// and dotted-vs-underscored variants) so the page renders
// without any operator-side config.

type SortKey = 'service' | 'calls' | 'errors' | 'errorRate' | 'avgMs' | 'p50Ms' | 'p99Ms' | 'maxMs';

// Top-N choices for the cap selector. v0.5.355 — at 10k+
// services the full window query took 10s+; defaulting to 200
// lets the page paint sub-second. Operator-overridable.
const TOP_CHOICES = [50, 100, 200, 500, 1000];

export default function SpanMetricsPage() {
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [data, setData] = useState<SpanMetricsServicesResponse | null | undefined>(undefined);
  const [search, setSearch] = useState('');
  const [sortKey, setSortKey] = useState<SortKey>('calls');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');
  // Local UI prefs, persisted in localStorage so the operator
  // doesn't have to re-pick on every visit. Top defaults to
  // 200 (the backend default); sparkline ON by default.
  const [top, setTop] = useState<number>(() => {
    const v = parseInt(localStorage.getItem('coremetry.spanmetrics.top') ?? '200', 10);
    return TOP_CHOICES.includes(v) ? v : 200;
  });
  const [showSpark, setShowSpark] = useState<boolean>(() =>
    (localStorage.getItem('coremetry.spanmetrics.spark') ?? '1') !== '0');

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  useEffect(() => {
    setData(undefined);
    api.spanmetricsServices(from, to, { top, spark: showSpark })
      .then(d => setData(d ?? null))
      .catch(() => setData(null));
  }, [from, to, top, showSpark]);

  const persistTop = (v: number) => {
    setTop(v);
    try { localStorage.setItem('coremetry.spanmetrics.top', String(v)); } catch { /* private mode */ }
  };
  const persistShowSpark = (v: boolean) => {
    setShowSpark(v);
    try { localStorage.setItem('coremetry.spanmetrics.spark', v ? '1' : '0'); } catch { /* private mode */ }
  };

  const rows: SpanMetricServiceRow[] = data?.rows ?? [];
  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    let list = q ? rows.filter(r => r.service.toLowerCase().includes(q)) : rows;
    list = [...list].sort((a, b) => {
      const dir = sortDir === 'asc' ? 1 : -1;
      switch (sortKey) {
        case 'service':   return dir * a.service.localeCompare(b.service);
        case 'calls':     return dir * (a.calls - b.calls);
        case 'errors':    return dir * (a.errors - b.errors);
        case 'errorRate': return dir * (a.errorRate - b.errorRate);
        case 'avgMs':     return dir * ((a.avgMs ?? 0) - (b.avgMs ?? 0));
        case 'p50Ms':     return dir * ((a.p50Ms ?? 0) - (b.p50Ms ?? 0));
        case 'p99Ms':     return dir * ((a.p99Ms ?? 0) - (b.p99Ms ?? 0));
        case 'maxMs':     return dir * ((a.maxMs ?? 0) - (b.maxMs ?? 0));
      }
    });
    return list;
  }, [rows, search, sortKey, sortDir]);

  // Window seconds for rate calc — surfaces per-second call
  // rate alongside the absolute count so an operator can
  // compare across windows without doing the math.
  const windowSec = Math.max(1, Math.round((to - from) / 1e9));
  const totalCalls = rows.reduce((s, r) => s + r.calls, 0);
  const totalErrors = rows.reduce((s, r) => s + r.errors, 0);
  const totalErrorRate = totalCalls > 0 ? (totalErrors / totalCalls) * 100 : 0;
  const totalRate = totalCalls / windowSec;
  // Cluster-wide avg latency — call-weighted across services
  // so a 1k-call service with 200ms avg outweighs a 10-call
  // service with 50ms avg (sums avg×calls / total calls).
  const weightedAvgMs = (() => {
    let num = 0, den = 0;
    for (const r of rows) {
      if (r.avgMs && r.calls) { num += r.avgMs * r.calls; den += r.calls; }
    }
    return den > 0 ? num / den : 0;
  })();

  const setSort = (k: SortKey) => {
    if (k === sortKey) setSortDir(d => d === 'asc' ? 'desc' : 'asc');
    else { setSortKey(k); setSortDir(k === 'service' ? 'asc' : 'desc'); }
  };

  return (
    <>
      <Topbar title="Span metrics" range={range} onRangeChange={setRange} />
      <div id="content">
        {data === undefined && <Spinner />}
        {data === null && (
          <Empty icon="⚠" title="Failed to load span metrics">
            The /api/spanmetrics/services endpoint errored. Check the server log.
          </Empty>
        )}
        {data && (!data.callsMetric || rows.length === 0) && (
          <Empty icon="⌗" title="No span metrics detected in this window">
            <div style={{ fontSize: 12, color: 'var(--text2)', marginTop: 8, maxWidth: 640, lineHeight: 1.55 }}>
              Coremetry didn't find any metric_points matching the
              spanmetrics naming conventions (
              <code>traces.spanmetrics.calls.total</code>{' / '}
              <code>spanmetrics.calls</code>{' / '}
              <code>calls</code> and underscored variants) in the
              selected window. Wire the
              {' '}<code>spanmetrics</code> processor in your OTel
              Collector (or the equivalent Grafana Alloy component)
              to emit them, and they'll appear here automatically.
              <br /><br />
              Reference snippet (otelcol-contrib):
              <pre style={{
                marginTop: 8, padding: 10, background: 'var(--bg1)',
                border: '1px solid var(--border)', borderRadius: 4,
                fontSize: 11, overflowX: 'auto',
              }}>{`processors:
  spanmetrics:
    histogram:
      explicit:
        buckets: [10ms, 50ms, 100ms, 500ms, 1s, 5s]

exporters:
  otlphttp/coremetry:
    endpoint: http://coremetry:8088
    tls:
      insecure: true

service:
  pipelines:
    traces:
      processors: [spanmetrics]
      exporters: [otlphttp/coremetry, …]
    metrics/spanmetrics:
      receivers: [spanmetrics]
      exporters: [otlphttp/coremetry]`}</pre>
            </div>
          </Empty>
        )}
        {data && data.callsMetric && rows.length > 0 && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
              gap: 12, marginBottom: 16,
            }}>
              <KPI label="Services" value={fmtNum(rows.length)} />
              <KPI label="Total calls" value={fmtNum(totalCalls)}
                   sub={`${totalRate.toFixed(1)}/s`} />
              <KPI label="Errors" value={fmtNum(totalErrors)}
                   sub={`${totalErrorRate.toFixed(2)}%`}
                   cls={totalErrorRate >= 1 ? (totalErrorRate >= 5 ? 'err' : 'warn') : ''} />
              <KPI label="Avg latency"
                   value={weightedAvgMs > 0 ? `${weightedAvgMs.toFixed(1)} ms` : '—'}
                   sub="call-weighted" />
              <KPI label="Source metric"
                   value={shortMetric(data.callsMetric)}
                   sub={data.durationMetric ? shortMetric(data.durationMetric) : 'no duration metric'} />
            </div>
            <div className="controls" style={{ marginBottom: 12, flexWrap: 'wrap' }}>
              <input value={search} onChange={e => setSearch(e.target.value)}
                placeholder="Filter by service…"
                style={{ width: 240, padding: '5px 10px', fontSize: 12,
                         background: 'var(--bg)', color: 'var(--text)',
                         border: '1px solid var(--border)', borderRadius: 4 }} />
              <label style={{ fontSize: 11, color: 'var(--text2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                Top
                <select value={top} onChange={e => persistTop(parseInt(e.target.value, 10))}
                  title="Cap on services returned — keeps the page fast at high cardinality">
                  {TOP_CHOICES.map(n => <option key={n} value={n}>{n}</option>)}
                </select>
              </label>
              <label style={{ fontSize: 11, color: 'var(--text2)', display: 'inline-flex', alignItems: 'center', gap: 6 }}
                title="Disable the per-row sparkline aggregation for the fastest possible load">
                <input type="checkbox" checked={showSpark}
                  onChange={e => persistShowSpark(e.target.checked)} />
                Sparkline
              </label>
              <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
                {search.trim()
                  ? `${visible.length} / ${rows.length} services`
                  : `${rows.length} services`}
                {data?.truncated && (
                  <span style={{ marginLeft: 8, color: 'var(--warn)' }}
                    title="Result hit the top-N cap. Increase Top to see more.">
                    · capped at top {data.top}
                  </span>
                )}
              </span>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <SortHeader k="service"   label="Service"     cur={sortKey} dir={sortDir} onSort={setSort} />
                    <SortHeader k="calls"     label="Calls"       cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="errors"    label="Errors"      cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="errorRate" label="Error rate"  cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <th className="num">Rate / s</th>
                    <SortHeader k="avgMs"     label="Avg"         cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="p50Ms"     label="p50"         cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="p99Ms"     label="p99"         cur={sortKey} dir={sortDir} onSort={setSort} num />
                    <SortHeader k="maxMs"     label="Max"         cur={sortKey} dir={sortDir} onSort={setSort} num />
                    {showSpark && <th>Calls / time</th>}
                    <th>Explore</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.map(r => {
                    const rateSec = r.calls / windowSec;
                    const errCls = r.errorRate >= 5 ? 'b-err' : r.errorRate >= 1 ? 'b-warn' : 'b-ok';
                    return (
                      <tr key={r.service}
                          style={{ contentVisibility: 'auto', containIntrinsicSize: 'auto 32px' }}>
                        <td>
                          <Link to={`/services?name=${encodeURIComponent(r.service)}`}
                                style={{ fontFamily: 'monospace', fontSize: 12, color: 'var(--text)' }}
                                title="Open the span-derived service detail page">
                            {r.service}
                          </Link>
                        </td>
                        <td className="num mono">{fmtNum(r.calls)}</td>
                        <td className="num mono">{fmtNum(r.errors)}</td>
                        <td className="num mono">
                          <span className={`badge ${errCls}`}>{r.errorRate.toFixed(2)}%</span>
                        </td>
                        <td className="num mono">{rateSec.toFixed(1)}</td>
                        <td className="num mono">
                          {r.avgMs != null && r.avgMs > 0 ? `${r.avgMs.toFixed(1)} ms` : '—'}
                        </td>
                        <td className="num mono">
                          {r.p50Ms != null && r.p50Ms > 0 ? `${r.p50Ms.toFixed(1)} ms` : '—'}
                        </td>
                        <td className="num mono">
                          {r.p99Ms != null && r.p99Ms > 0 ? `${r.p99Ms.toFixed(0)} ms` : '—'}
                        </td>
                        <td className="num mono" style={{ color: 'var(--text3)' }}>
                          {r.maxMs != null && r.maxMs > 0 ? `${r.maxMs.toFixed(0)} ms` : '—'}
                        </td>
                        {showSpark && (
                          <td>
                            <Sparkline values={r.sparkline ?? []}
                              width={100} height={22}
                              title={`${fmtNum(r.calls)} calls across ${windowSec >= 60 ? `${Math.round(windowSec / 60)} min` : `${windowSec} s`}`} />
                          </td>
                        )}
                        <td>
                          <Link to={`/metrics?service=${encodeURIComponent(r.service)}&metric=${encodeURIComponent(data.callsMetric)}`}
                                style={{ fontSize: 11, color: 'var(--accent2)' }}>
                            chart →
                          </Link>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)' }}>
              Source: metric_points · <code>{data.callsMetric}</code>
              {data.durationMetric ? <> + <code>{data.durationMetric}</code></> : null}
              {'. '}Avg = Σsum/Σcount; p50/p99 estimated from histogram
              bucket counts (linear-interpolated within the matched
              bucket); max is the observed upper bound per service.
              Quantiles are blank when the source histogram emitted
              count/sum only without explicit bucket bounds.
            </div>
          </>
        )}
      </div>
    </>
  );
}

function KPI({ label, value, sub, cls }: { label: string; value: string; sub?: string; cls?: string }) {
  return (
    <div style={{
      padding: '8px 14px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 2 }}>{label}</div>
      <div style={{
        fontSize: 22, fontWeight: 600,
        color: cls === 'err' ? 'var(--err)' : cls === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{value}</div>
      {sub && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>{sub}</div>}
    </div>
  );
}

function SortHeader({ k, label, cur, dir, onSort, num }: {
  k: SortKey; label: string; cur: SortKey; dir: 'asc' | 'desc';
  onSort: (k: SortKey) => void; num?: boolean;
}) {
  const active = cur === k;
  return (
    <th onClick={() => onSort(k)}
      className={num ? 'num' : ''}
      style={{ cursor: 'pointer', userSelect: 'none' }}>
      <span style={{ color: active ? 'var(--text)' : 'var(--text2)' }}>
        {label}{active ? (dir === 'asc' ? ' ▲' : ' ▼') : ''}
      </span>
    </th>
  );
}

// shortMetric trims the canonical "traces.spanmetrics.foo"
// prefix so the KPI tile renders the meaningful suffix
// (calls.total / duration) rather than wrapping the long
// fully-qualified name.
function shortMetric(m: string): string {
  if (!m) return '—';
  const trimmed = m.replace(/^traces\.spanmetrics\./, '').replace(/^traces_spanmetrics_/, '');
  return trimmed || m;
}
