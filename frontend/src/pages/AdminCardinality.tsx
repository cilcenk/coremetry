import { useMemo, useState } from 'react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { useAuth } from '@/components/AuthProvider';
import { Card, Badge, Stack, Row } from '@/components/ui';
import { useCardinality, useSystemStats, keys } from '@/lib/queries';
import { useQueryClient } from '@tanstack/react-query';
import { fmtBytes, fmtNum } from '@/lib/utils';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// Row shapes for the cardinality data tables. Kept local — these
// mirror the cardinality report response and aren't shared.
type AttrKeyRow = { key: string; distinctValues: number; occurrences: number; source: string };
type ColumnRow = { table: string; column: string; compressedBytes: number; uncompressedBytes: number; compressionRatio: number };
type FinOpsServiceRow = { name: string; rows: number };

// Sortable + resizable column defs for the attribute-key panel.
const ATTR_KEY_COLS: DataTableColumn<AttrKeyRow>[] = [
  { id: 'key',         label: 'Key',                 sortValue: r => r.key,            naturalDir: 'asc',  width: 280 },
  { id: 'distinct',    label: 'Distinct values',     sortValue: r => r.distinctValues, numeric: true, naturalDir: 'desc', width: 150 },
  { id: 'occurrences', label: 'Sampled occurrences', sortValue: r => r.occurrences,    numeric: true, naturalDir: 'desc', width: 170 },
  { id: 'source',      label: 'Source',              sortValue: r => r.source,         naturalDir: 'asc',  width: 160 },
];

// Sortable + resizable column defs for the top-columns panel.
const COLUMN_COLS: DataTableColumn<ColumnRow>[] = [
  { id: 'table',        label: 'Table',               sortValue: r => r.table,            naturalDir: 'asc',  width: 180 },
  { id: 'column',       label: 'Column',              sortValue: r => r.column,           naturalDir: 'asc',  width: 200 },
  { id: 'compressed',   label: 'On disk (compressed)', sortValue: r => r.compressedBytes,  numeric: true, naturalDir: 'desc', width: 170 },
  { id: 'uncompressed', label: 'Uncompressed',        sortValue: r => r.uncompressedBytes, numeric: true, naturalDir: 'desc', width: 150 },
  { id: 'ratio',        label: 'Ratio',               sortValue: r => r.compressionRatio,  numeric: true, naturalDir: 'desc', width: 90 },
];

// /admin/cardinality — meta-observability dashboard answering
// "which service / metric / label is eating my ClickHouse?".
// Four panels: top services by 24h spans, top metrics by 24h
// points, top attribute keys by distinct cardinality (sampled
// from the last 100k spans), and top columns by compressed
// disk bytes.
//
// The attribute-key panel is the actual operational lever —
// when a label transitions from controlled (e.g. http.method
// with 5 values) to unbounded (e.g. user.id with 50k values),
// it's invisible until storage starts to bleed. Surfacing it
// here lets the admin drop the offending label before it costs
// an order of magnitude in storage.
export default function AdminCardinalityPage() {
  const { user } = useAuth();
  const qc = useQueryClient();
  // useQuery enabled-gated on admin role so a viewer never
  // triggers the report (the API also enforces it, but skipping
  // the request keeps the network tab clean for non-admins).
  const cardinalityQ = useCardinality();
  const data = cardinalityQ.isLoading
    ? undefined
    : cardinalityQ.isError
      ? null
      : cardinalityQ.data;

  if (!user) return null;
  if (user.role !== 'admin') {
    return (
      <>
        <Topbar title="Cardinality" />
        <div id="content">
          <Empty icon="◇" title="Admin access required">
            The cardinality report is only available to administrators —
            it surfaces every service / metric / label name in the cluster.
          </Empty>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Cardinality" />
      <div id="content">
        <Row gap={3} style={{ marginBottom: 14, alignItems: 'center' }}>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>
            What is eating ClickHouse — top emitters across services, metrics, labels, and stored columns. 5-min server cache.
          </span>
          <span style={{ flex: 1 }} />
          <button className="sec"
                  onClick={() => qc.invalidateQueries({ queryKey: keys.admin.cardinality })}>
            Refresh
          </button>
        </Row>

        {data === undefined && <Spinner />}
        {data === null && (
          <Empty icon="!" title="Failed to load cardinality report">
            Check that ClickHouse is reachable and that you have admin access.
          </Empty>
        )}
        {data && (
          <Stack gap={4}>
            {/* FinOps tile — surfaces the "how much is this
                deployment costing us this month" question that
                bank finance teams ask before signoff. Sits
                above the cardinality drill-down so an operator
                triaging cost lands on the projection first,
                then drills into top emitters / labels /
                columns to find what to trim. */}
            <FinOpsPanel services={data.services ?? []} />

            <Row gap={4} wrap>
              <Card style={{ flex: '1 1 380px', minWidth: 0 }}
                    header={<>Top services by 24h spans</>}>
                {/* Defensive `?? []` everywhere — Go marshals
                    a nil slice as JSON null, so when one of
                    the four sub-queries silently fails (e.g.
                    the uniqExact attribute scan times out
                    on a billion-span tenant), the field on
                    the response is `null` rather than `[]`,
                    and `.length` on that crashes the page. */}
                <TopRowList rows={data.services ?? []} unit="spans" />
              </Card>

              <Card style={{ flex: '1 1 380px', minWidth: 0 }}
                    header={<>Top metrics by 24h points</>}>
                <TopRowList rows={data.metrics ?? []} unit="points" />
              </Card>
            </Row>

            <Card header={<>
              Top attribute keys by distinct cardinality
              <span style={{ fontSize: 11, color: 'var(--text3)', fontWeight: 400, marginLeft: 8 }}>
                — sampled from the last 100k spans of the most recent hour. High counts here = unbounded labels (user IDs, raw URLs, request IDs); the worst storage offenders.
              </span>
            </>}>
              <AttrKeyTable rows={data.attrKeys ?? []} />
            </Card>

            <Card header={<>Top columns by compressed bytes</>}>
              <ColumnTable rows={data.columns ?? []} />
            </Card>
          </Stack>
        )}
      </div>
    </>
  );
}

function TopRowList({ rows, unit }: { rows: { name: string; rows: number }[]; unit: string }) {
  if (rows.length === 0) {
    return <div style={{ fontSize: 12, color: 'var(--text3)' }}>No data in the last 24h.</div>;
  }
  const max = Math.max(...rows.map(r => r.rows));
  return (
    <div className="stack gap-1">
      {rows.map((r, i) => (
        <Row key={i} gap={2} style={{ fontSize: 12 }}>
          <span style={{ width: 22, color: 'var(--text3)', fontFamily: 'monospace', textAlign: 'right' }}>
            {i + 1}.
          </span>
          <span style={{ flex: 1, fontFamily: 'monospace', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                title={r.name}>
            {r.name}
          </span>
          {/* Inline horizontal bar — width proportional to top
              row, gives a Datadog-like top-N glance without a
              heavy chart dependency. */}
          <span style={{
            display: 'inline-block',
            width: 100,
            height: 4,
            background: 'var(--bg3)',
            borderRadius: 2,
            position: 'relative',
            overflow: 'hidden',
          }}>
            <span style={{
              position: 'absolute', left: 0, top: 0, bottom: 0,
              width: `${Math.max(2, (r.rows / max) * 100)}%`,
              background: 'var(--accent)',
            }} />
          </span>
          <span style={{ width: 80, textAlign: 'right', fontFamily: 'monospace', color: 'var(--text2)' }}>
            {fmtNum(r.rows)} {unit}
          </span>
        </Row>
      ))}
    </div>
  );
}

function AttrKeyTable({ rows }: { rows: AttrKeyRow[] }) {
  // Sortable + resizable attribute-key table. Default sort matches the
  // panel's intent — biggest distinct-value count first (the worst
  // unbounded-label offenders) — which is also the server's order.
  const dt = useDataTable<AttrKeyRow>({
    storageKey: 'admincardinality-attrkeys',
    columns: ATTR_KEY_COLS,
    rows,
    initialSort: { id: 'distinct', dir: 'desc' },
  });
  if (rows.length === 0) {
    return <div style={{ fontSize: 12, color: 'var(--text3)' }}>No attributes sampled.</div>;
  }
  return (
    <div className="table-wrap">
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>
          {dt.sortedRows.map((r, i) => {
            // Heuristic: > 1000 distinct values in a 100k-span sample
            // is the unbounded-label red flag. Yellow at > 200.
            const tone = r.distinctValues > 1000 ? 'danger'
                       : r.distinctValues > 200 ? 'warning' : 'neutral';
            return (
              // content-visibility: auto lets the browser skip
              // off-screen rows on initial paint — at high-
              // cardinality installs this table can hit a few
              // thousand attribute keys and lock the page
              // without it.
              <tr key={i} style={{
                contentVisibility: 'auto',
                containIntrinsicSize: 'auto 32px',
              }}>
                <td className="mono">{r.key}</td>
                <td className="num mono">
                  <Badge tone={tone}>{fmtNum(r.distinctValues)}</Badge>
                </td>
                <td className="num mono" style={{ color: 'var(--text2)' }}>
                  {fmtNum(r.occurrences)}
                </td>
                <td style={{ fontSize: 11, color: 'var(--text3)' }}>{r.source}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// FinOpsPanel — surfaces ingest + storage + projected monthly
// cost at the top of the cardinality page. Bank finance teams
// ask "what will this cost next month?" before signing off on
// retention bumps or new high-cardinality labels; this is the
// single screen that answers it.
//
// Inputs:
//   • systemStats (24h spans + total disk bytes + per-table
//     storage) — already fetched by the admin pages.
//   • services list from cardinality data — per-service share
//     of 24h ingest.
//   • $/TB-month rate — operator-tunable. Default $50 reads as
//     "ClickHouse on commodity NVMe at hyperscaler list".
//     Persisted to localStorage so the rate sticks across
//     sessions and admins on the same install converge.
function FinOpsPanel({ services }: {
  services: { name: string; rows: number }[];
}) {
  const sysQ = useSystemStats();
  const [costPerTbMo, setCostPerTbMo] = useState<number>(() => {
    const v = parseFloat(getRaw(STORAGE_KEYS.finopsCostPerTbMo) ?? '');
    if (isFinite(v) && v > 0) return v;
    return 50;
  });
  const updateRate = (n: number) => {
    setCostPerTbMo(n);
    setRaw(STORAGE_KEYS.finopsCostPerTbMo, String(n));
  };

  const stats = sysQ.data;
  if (!stats) {
    return (
      <Card header={<>Cost forecast</>}>
        <div style={{ fontSize: 12, color: 'var(--text3)' }}>
          Loading ingest + storage stats…
        </div>
      </Card>
    );
  }

  const diskBytes = stats.snapshot.totalDiskBytes;
  const diskTB = diskBytes / 1e12;
  const monthlyStorageCost = diskTB * costPerTbMo;

  // Daily ingest rate from the spans 24h count. We don't have
  // per-day-bytes directly, so we approximate from
  // (totalDiskBytes / spansAllTime) × spans24h — gives a
  // reasonable bytes-per-day estimate that's close enough for
  // a forecast tile. Fall back to "—" when spansAllTime is 0
  // (cold deployments).
  const bytesPerSpan = stats.snapshot.spansAllTime > 0
    ? diskBytes / stats.snapshot.spansAllTime
    : 0;
  const dailyIngestBytes = bytesPerSpan * stats.snapshot.spans24h;
  const monthlyIngestTB = (dailyIngestBytes * 30) / 1e12;
  const projectedMonthlyGrowthCost = monthlyIngestTB * costPerTbMo;

  // Per-service share of 24h spans — multiplied by the
  // bytes-per-span estimate to give a "this service contributes
  // ~$X this month" projection. Top 10, with shares ≥ 0.1%.
  const totalSpans24h = services.reduce((a, s) => a + s.rows, 0) || 1;
  const top = services
    .slice(0, 10)
    .filter(s => s.rows / totalSpans24h >= 0.001);

  return (
    <Card header={<>
      Cost forecast
      <span style={{ fontSize: 11, color: 'var(--text3)', fontWeight: 400, marginLeft: 8 }}>
        — estimated monthly ClickHouse cost at the rate you set below
      </span>
    </>}>
      <Row gap={3} wrap style={{ marginBottom: 14 }}>
        <KPI label="Current on disk" value={fmtBytes(diskBytes)}
          sub={diskTB > 0 ? `${diskTB.toFixed(2)} TB` : undefined} />
        <KPI label="Storage cost / mo" value={`$${monthlyStorageCost.toFixed(2)}`}
          sub={`@ $${costPerTbMo}/TB-month`} />
        <KPI label="24h spans" value={fmtNum(stats.snapshot.spans24h)}
          sub={bytesPerSpan > 0 ? `~${(bytesPerSpan).toFixed(0)} B/span` : undefined} />
        <KPI label="Projected monthly growth"
          value={dailyIngestBytes > 0
            ? `+${fmtBytes(dailyIngestBytes * 30)}`
            : '—'}
          sub={projectedMonthlyGrowthCost > 0
            ? `+$${projectedMonthlyGrowthCost.toFixed(2)}/mo`
            : undefined}
          tone={projectedMonthlyGrowthCost > monthlyStorageCost * 0.5 ? 'warn' : undefined} />
      </Row>

      <Row gap={2} style={{ alignItems: 'center', marginBottom: 14, fontSize: 12 }}>
        <span style={{ color: 'var(--text2)' }}>Rate ($/TB-month):</span>
        <input type="number" min={0} step={1} value={costPerTbMo}
          onChange={e => updateRate(parseFloat(e.target.value) || 0)}
          style={{ width: 80, fontSize: 12 }} />
        <span style={{ color: 'var(--text3)', fontSize: 11 }}>
          Default $50/TB-mo is rough order-of-magnitude for ClickHouse on commodity
          NVMe; adjust to your cloud / on-prem unit cost. Saved per browser.
        </span>
      </Row>

      {top.length > 0 && bytesPerSpan > 0 && (
        <div>
          <div style={{
            fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.4, marginBottom: 8,
          }}>
            Top contributors (extrapolated monthly cost)
          </div>
          <FinOpsContributorsTable
            top={top}
            bytesPerSpan={bytesPerSpan}
            totalSpans24h={totalSpans24h}
            costPerTbMo={costPerTbMo}
          />
        </div>
      )}
    </Card>
  );
}

// FinOpsContributorsTable — extrapolated per-service monthly cost.
// Split into its own component so the shared DataTable hook is called
// unconditionally (FinOpsPanel early-returns while stats load, which
// would make a hook inside it conditional). Share + est. cost are
// derived from props, so the column accessors are memoised here.
function FinOpsContributorsTable({ top, bytesPerSpan, totalSpans24h, costPerTbMo }: {
  top: FinOpsServiceRow[];
  bytesPerSpan: number;
  totalSpans24h: number;
  costPerTbMo: number;
}) {
  const cols = useMemo<DataTableColumn<FinOpsServiceRow>[]>(() => [
    { id: 'service', label: 'Service',           sortValue: s => s.name,                                       naturalDir: 'asc',  width: 220 },
    { id: 'spans',   label: '24h spans',         sortValue: s => s.rows,                          numeric: true, naturalDir: 'desc', width: 130 },
    { id: 'share',   label: 'Share',             sortValue: s => s.rows / totalSpans24h,          numeric: true, naturalDir: 'desc', width: 100 },
    { id: 'cost',    label: 'Est. monthly cost', sortValue: s => s.rows * bytesPerSpan * 30 * costPerTbMo, numeric: true, naturalDir: 'desc', width: 150 },
  ], [totalSpans24h, bytesPerSpan, costPerTbMo]);
  const dt = useDataTable<FinOpsServiceRow>({
    storageKey: 'admincardinality-finops',
    columns: cols,
    rows: top,
    initialSort: { id: 'spans', dir: 'desc' },
  });
  return (
    <div className="table-wrap">
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>
          {dt.sortedRows.map(s => {
            const share = s.rows / totalSpans24h;
            const cost = (s.rows * bytesPerSpan * 30 / 1e12) * costPerTbMo;
            return (
              <tr key={s.name}>
                <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{s.name}</td>
                <td className="num mono">{fmtNum(s.rows)}</td>
                <td className="num mono">{(share * 100).toFixed(1)}%</td>
                <td className="num mono">${cost.toFixed(2)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function KPI({ label, value, sub, tone }: {
  label: string; value: string; sub?: string; tone?: 'warn' | 'err';
}) {
  const color = tone === 'err' ? 'var(--err)'
              : tone === 'warn' ? 'var(--warn)' : 'var(--text)';
  return (
    <div style={{
      padding: '8px 12px', borderRadius: 6,
      background: 'var(--bg2)', border: '1px solid var(--border)',
      minWidth: 160,
    }}>
      <div style={{
        fontSize: 9, color: 'var(--text3)', fontWeight: 700,
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>{label}</div>
      <div style={{
        fontSize: 18, fontWeight: 700, color,
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      }}>{value}</div>
      {sub && (
        <div style={{
          fontSize: 10, color: 'var(--text3)', marginTop: 2,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>{sub}</div>
      )}
    </div>
  );
}

function ColumnTable({ rows }: { rows: ColumnRow[] }) {
  // Sortable + resizable top-columns table. Default sort matches the
  // panel's intent (and the server order) — largest compressed bytes
  // first, the columns actually eating disk.
  const dt = useDataTable<ColumnRow>({
    storageKey: 'admincardinality-columns',
    columns: COLUMN_COLS,
    rows,
    initialSort: { id: 'compressed', dir: 'desc' },
  });
  if (rows.length === 0) {
    return <div style={{ fontSize: 12, color: 'var(--text3)' }}>system.columns empty.</div>;
  }
  return (
    <div className="table-wrap">
      <table style={{ tableLayout: 'fixed', width: '100%' }}>
        <DataTableColgroup dt={dt} />
        <DataTableHead dt={dt} />
        <tbody>
          {dt.sortedRows.map((r, i) => (
            <tr key={i} style={{
              contentVisibility: 'auto',
              containIntrinsicSize: 'auto 32px',
            }}>
              <td className="mono" style={{ color: 'var(--text2)' }}>{r.table}</td>
              <td className="mono">{r.column}</td>
              <td className="num mono">{fmtBytes(r.compressedBytes)}</td>
              <td className="num mono" style={{ color: 'var(--text3)' }}>{fmtBytes(r.uncompressedBytes)}</td>
              <td className="num mono" style={{ color: 'var(--text2)' }}>
                {r.compressionRatio.toFixed(1)}×
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

