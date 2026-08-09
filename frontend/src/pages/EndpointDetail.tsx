import { useMemo } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Card } from '@/components/ui';
import { Spinner, Empty } from '@/components/Spinner';
import { TrendDelta } from '@/components/TrendDelta';
import { useEndpoints, useEndpointDetail } from '@/lib/queries';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import type { EndpointDetail, EndpointRow } from '@/lib/types';
import {
  parseEndpointPageRef, endpointSearchHint, type EndpointRef,
} from '@/pages/endpoints/endpointParam';
import { tracesLink, exploreLink } from '@/pages/endpoints/links';
import { MetricTile } from '@/pages/endpoints/MetricTile';
import { bucketsToSeries } from '@/pages/endpoints/series';
import {
  HistogramSection, StatusSection, ExceptionsSection, FailingTracesSection,
  SplitSection, WhereTheTimeGoesSection, CallersSection,
} from '@/pages/endpoints/detailSections';

// /endpoint — the full-page endpoint detail (v0.9.839).
//
// WHY A PAGE. Until now a row click opened a 620px drawer and a
// sparkline click opened a modal, so one endpoint's story was told in
// two overlays that could not be open at once and neither of which had
// room for a table. The operator's call: retire both, the row click
// navigates. Everything the drawer showed is here at full width, plus
// the series the modal owned and the caller panel that had no home.
//
// IDENTITY — the trap this page is built around. An endpoint here is
// (service, http.route|path), NOT a span name. Every read below is
// keyed on that pair: the row lookup goes through /api/endpoints (which
// resolves the path dimension the same way the table does), and the
// section reads go through /api/endpoints/* which match on http_route.
// /api/spans/metric-batch is deliberately NOT used anywhere on this
// page — it keys on span NAME, and an SDK that names its server span
// "HTTP POST" would return a different population that looks correct.
//
// NO EXTRA SERIES FETCH. The three RED charts are drawn from the table
// row's own sparkline arrays (the MV produced them beside the numbers),
// exactly as the retired modal did.
//
// URL = source of truth: ?service=&path=&sig=&range=&env=&cluster=
// &compare=&entry=. Human-readable on purpose — a link pasted into an
// incident channel should be legible there — while the packed
// pre-v0.9.839 `?endpoint=` / `?detail=` codecs still resolve, via the
// one redirect effect on /endpoints.
export default function EndpointDetailPage() {
  const [params, setParams] = useSearchParams();
  const search = params.toString();
  const refObj = useMemo(() => parseEndpointPageRef(search), [search]);
  const env = useUrlEnv()[0];
  const cluster = params.get('cluster') ?? '';
  const compare = params.get('compare') === '1';
  const entry = params.get('entry') === 'rpc' ? 'rpc' : undefined;
  const { range, setRange, handleZoom, handleZoomReset } = usePageZoomRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

  const setCompare = (v: boolean) => setParams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('compare', '1'); else next.delete('compare');
    return next;
  }, { replace: true });

  // The row carries the RED numbers AND the three sparkline arrays, so
  // one list read feeds both the score strip and the charts.
  //
  // The list is narrowed as hard as the identity allows: the service is
  // exact, and `search` is a substring of the raw path dimension —
  // which in signature mode has to be the literal prefix before the
  // first ':' segment, since a collapsed path (/orders/:id) is not a
  // substring of any raw one. endpointSearchHint owns that reasoning.
  const rowsQ = useEndpoints(refObj ? {
    from, to,
    service: refObj.service,
    search: endpointSearchHint(refObj.path, refObj.sig) || undefined,
    cluster: cluster || undefined,
    env: env || undefined,
    limit: 200,
    compare: compare ? 'prior' as const : undefined,
    groupBy: refObj.sig ? 'signature' as const : undefined,
    sort: 'calls',
    dir: 'desc' as const,
    entry,
  } : { from, to, limit: 1 });
  const row: EndpointRow | undefined = useMemo(() => {
    if (!refObj) return undefined;
    return (rowsQ.data?.rows ?? []).find(r =>
      r.service === refObj.service && r.path === refObj.path);
  }, [rowsQ.data, refObj]);

  const detailQ = useEndpointDetail(refObj ? {
    service: refObj.service, path: refObj.path, from, to,
    ...(refObj.sig ? { sig: '1' as const } : {}),
    ...(env ? { env } : {}),
    ...(cluster ? { cluster } : {}),
  } : null);
  const detail: EndpointDetail | null | undefined =
    detailQ.isPending ? undefined : detailQ.isError ? null : detailQ.data;

  const series = useMemo(
    () => (row ? bucketsToSeries(row, range) : { calls: [], errors: [], p99: [] }),
    [row, range]);

  if (!refObj) {
    return (
      <>
        <Topbar title="Endpoint" />
        <div id="content">
          <Empty icon="⚠" title="No endpoint in this link">
            The URL is missing <code>service</code> or <code>path</code>.
            {' '}<Link to="/endpoints">Back to Endpoints →</Link>
          </Empty>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Endpoint" range={range} onRangeChange={setRange} envApplies />
      <div id="content">
        <div style={{ fontSize: 11.5, color: 'var(--text3)', marginBottom: 8 }}>
          <Link to="/endpoints">Endpoints</Link> › endpoint detail
        </div>

        {/* Identity header — method, route, service, the scope it was
            read under, and the outbound pivots. */}
        <div style={{
          display: 'flex', alignItems: 'center', gap: 10,
          flexWrap: 'wrap', marginBottom: 4,
        }}>
          {row?.method && (
            <span className="badge b-info" style={{ fontSize: 10 }}>{row.method}</span>
          )}
          <span className="mono" style={{ fontSize: 15, fontWeight: 600, wordBreak: 'break-all' }}
            title={refObj.path}>
            {refObj.path}
          </span>
          <Link to={`/service?name=${encodeURIComponent(refObj.service)}`}
            className="mono" style={{ fontSize: 12 }}>
            {refObj.service} →
          </Link>
          {refObj.sig && (
            <span className="badge b-info" style={{ fontSize: 10 }}
              title="Grouped by shape — IDs in the path are collapsed to :id; every section below aggregates all matching raw routes.">
              shape
            </span>
          )}
          {/* v0.9.306 — SCOPE chip. The sections narrow to the same
              env/cluster the row was computed under; saying so is the
              other half of the fix, because an unlabelled narrowing is
              the same class of lie in the opposite direction. */}
          {(env || cluster) && (
            <span className="badge b-info" style={{ fontSize: 10 }}
              title="Everything on this page covers only this scope — the same slice the table row's numbers came from.">
              {env && `env=${env}`}{env && cluster && ' · '}{cluster && `cluster=${cluster}`}
            </span>
          )}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: 12, fontSize: 12 }}>
            <Link to={tracesLink(refObj, range, env, cluster)}>Traces →</Link>
            <Link to={exploreLink(refObj, range, 'p99', env, cluster)}
              title="Open this route's p99 in Explore — charted from the metric rollups, where you can add dimensions or compare against another query.">
              Explore →
            </Link>
            <Link to={`/service?name=${encodeURIComponent(refObj.service)}`}>Service →</Link>
          </span>
        </div>

        <REDStrip row={row} pending={rowsQ.isPending} compare={compare}
          onToggleCompare={() => setCompare(!compare)} />

        {/* Three RED series — from the row's own sparklines, no extra
            fetch. Absent row ⇒ honest empty tiles, never fabricated
            series (the v0.9.206 rule the modal carried). */}
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))',
          gap: 12, marginBottom: 12,
        }}>
          <MetricTile label="Calls" storageKey="calls"
            big={row ? fmtNum(row.calls) : '—'}
            sub={row ? `peak ${fmtNum(Math.max(0, ...(row.sparkline ?? [0])))} / bucket` : 'no row in window'}
            series={series.calls} unit="reqps"
            service={refObj.service} range={range}
            emptyLabel={row ? undefined : 'endpoint not in the current window'}
            onZoom={handleZoom} onZoomReset={handleZoomReset} />
          <MetricTile label="Errors" storageKey="errors"
            big={row ? fmtNum(row.errors) : '—'}
            sub={row ? `${row.errorRate.toFixed(2)}% rate` : 'no row in window'}
            subCls={row ? (row.errorRate >= 5 ? 'err' : row.errorRate >= 1 ? 'warn' : '') : ''}
            series={series.errors} unit="percent" role="error"
            service={refObj.service} range={range}
            emptyLabel={row ? undefined : 'endpoint not in the current window'}
            onZoom={handleZoom} onZoomReset={handleZoomReset} />
          <MetricTile label="P99 latency" storageKey="p99"
            big={row ? `${row.p99Ms.toFixed(0)} ms` : '—'}
            sub={row ? `avg ${row.avgMs.toFixed(0)} ms` : 'no row in window'}
            series={series.p99} unit="ms"
            service={refObj.service} range={range}
            emptyLabel={row ? undefined : 'endpoint not in the current window'}
            onZoom={handleZoom} onZoomReset={handleZoomReset} />
        </div>

        {detail === undefined && <Spinner />}
        {detail === null && (
          <Empty icon="⚠" title="Detail query failed">
            The backend /api/endpoints/detail request errored. The score
            strip and charts above read from a different endpoint and are
            still live.
          </Empty>
        )}
        {detail && (
          <>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))',
              gap: 12,
            }}>
              <HistogramSection detail={detail} />
              <StatusSection detail={detail} />
            </div>
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))',
              gap: 12,
            }}>
              <WhereTheTimeGoesSection refObj={refObj} from={from} to={to}
                env={env} cluster={cluster} />
              <SplitSection refObj={refObj} from={from} to={to}
                env={env} cluster={cluster} />
            </div>
            <CallersSection refObj={refObj} from={from} to={to}
              env={env} cluster={cluster} />
            <FailingTracesSection detail={detail} />
            <ExceptionsSection detail={detail} />
          </>
        )}

        <div style={{ marginTop: 10, fontSize: 11, color: 'var(--text3)', lineHeight: 1.6 }}>
          Endpoint identity is <code>http.route</code> (templated) with the
          table's own path fallbacks — never the span name. Server / consumer
          spans only; outbound client spans count under the callee.
          P50/P95/P99 are true window quantiles (tdigest).
        </div>
      </div>
    </>
  );
}

// REDStrip — the single-entity score strip (the drawer header's
// promotion). NOT the list-page KPI block that was removed in v0.9.834:
// these are one endpoint's own numbers over the selected window, which
// is the thing the page is about.
function REDStrip({ row, pending, compare, onToggleCompare }: {
  row: EndpointRow | undefined; pending: boolean;
  compare: boolean; onToggleCompare: () => void;
}) {
  if (pending && !row) {
    return <Card density="tight" style={{ marginBottom: 12 }}><Spinner /></Card>;
  }
  if (!row) {
    // Honest, and specific about WHY — the deep-link case the modal
    // learned to state in v0.9.818: the numbers come from the list read,
    // so an endpoint outside the window has none to show.
    return (
      <Card density="tight" style={{ marginBottom: 12 }}>
        <div style={{ fontSize: 11.5, color: 'var(--text2)', lineHeight: 1.6 }}>
          This endpoint has <b>no row in the selected window</b>, so its RED
          numbers and series cannot be drawn — they come from the endpoint
          list read. Widen the time range, or clear the env / cluster
          filter. The identity is intact and the sections below still load.
        </div>
      </Card>
    );
  }
  const tone = row.errorRate >= 5 ? 'err' : row.errorRate >= 1 ? 'warn' : undefined;
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(104px, 1fr))',
        gap: 8,
      }}>
        <Stat label="Calls">
          {fmtNum(row.calls)}
          {compare && <TrendDelta cur={row.calls} prior={row.priorCalls} kind="neutral" />}
        </Stat>
        <Stat label="Errors" tone={tone}>
          {fmtNum(row.errors)}
          {compare && <TrendDelta cur={row.errors} prior={row.priorErrors} kind="lowerBetter" />}
        </Stat>
        <Stat label="Err rate" tone={tone}>{row.errorRate.toFixed(2)}%</Stat>
        {row.reqPerMin != null && <Stat label="Req/min">{row.reqPerMin.toFixed(1)}</Stat>}
        <Stat label="Avg">
          {row.avgMs.toFixed(1)} ms
          {compare && <TrendDelta cur={row.avgMs} prior={row.priorAvgMs} kind="lowerBetter" />}
        </Stat>
        {row.p50Ms != null && <Stat label="P50">{row.p50Ms.toFixed(0)} ms</Stat>}
        {row.p95Ms != null && <Stat label="P95">{row.p95Ms.toFixed(0)} ms</Stat>}
        <Stat label="P99">
          {row.p99Ms.toFixed(0)} ms
          {compare && <TrendDelta cur={row.p99Ms} prior={row.priorP99Ms} kind="lowerBetter" />}
        </Stat>
      </div>
      <button type="button" onClick={onToggleCompare}
        title="Compare each number against the previous window of the same length"
        style={{
          all: 'unset', cursor: 'pointer', fontSize: 11,
          color: 'var(--accent2)', marginTop: 6,
        }}>
        {compare ? '✓ comparing to prior window' : 'compare to prior window'}
      </button>
    </div>
  );
}

function Stat({ label, tone, children }: {
  label: string; tone?: 'err' | 'warn'; children: React.ReactNode;
}) {
  return (
    <div style={{
      padding: '8px 10px', border: '1px solid var(--border)',
      borderRadius: 6, background: 'var(--bg1)', minWidth: 0,
    }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', marginBottom: 2,
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>{label}</div>
      <div className="mono" style={{
        fontSize: 15, fontWeight: 600,
        color: tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text)',
      }}>{children}</div>
    </div>
  );
}

export type { EndpointRef };
