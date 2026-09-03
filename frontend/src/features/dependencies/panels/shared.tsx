import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Empty, Spinner } from '@/components/Spinner';
import { MultiLineChart } from '@/components/MultiLineChart';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs } from '@/lib/utils';
import { encodeFilters } from '@/lib/urlState';
import type { FilterExpr } from '@/lib/types';
import { serviceHref } from '@/lib/serviceHref';
import { statementTracesHref } from '@/lib/pivotHref';
import { metricCatalogueHref } from '@/pages/explore/urlCodec';
import { Button } from '@/components/ui/Button';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type { TimeRange, SpanMetricSeries } from '@/lib/types';

// v0.9.873 (tutarlılık denetimi BT9) — tek dokunuş ÜÇ paneli düzeltiyor:
// TopSQLTable Oracle / Postgres / MySQL tarafından paylaşılıyor.
export type TopSQLRow = { sql: string; elapsedSec: number; executions: number; avgElapsedMs: number };

const TOPSQL_COLS: DataTableColumn<TopSQLRow>[] = [
  { id: 'sql',     label: 'SQL',     sortValue: r => r.sql,          naturalDir: 'asc', flex: true },
  { id: 'elapsed', label: 'Elapsed', sortValue: r => r.elapsedSec,   numeric: true, width: 110 },
  { id: 'execs',   label: 'Execs',   sortValue: r => r.executions,   numeric: true, width: 110 },
  { id: 'avg',     label: 'Avg',     sortValue: r => r.avgElapsedMs, numeric: true, width: 110 },
];

// panels/shared — the chrome + drill plumbing every DB-receiver
// engine panel (Oracle / Postgres / MySQL / Redis) composes:
// Stat/GaugeStat KPI tiles, the metric-chart drill modal, the
// engine-authoritative Top-SQL tables, panel header/error atoms
// and the byte/duration formatters. Split out of the
// DependenciesTable monolith (v0.8.252 refactor) verbatim.

// OracleDrill — what the user clicked on. Carries enough state
// to build a metricQuery against /api/metrics/query and label
// the drill-down modal.
export type OracleDrill = {
  metric: string;                    // e.g. 'oracledb.sessions.usage'
  label: string;                     // human-readable for the modal title
  unit?: string;                     // ms / % / bytes — feeds the chart's fmtSmart
  // v0.9.269 — MUST be the shared FilterExpr shape ({k, op, v[]}). It used
  // to be a lookalike `{key, op, value}`, which the Go side
  // (internal/chstore/filterexpr.go:11-15) binds by JSON TAG: `key` and
  // `value` matched nothing, Key came through empty, SQLForMetricPoints
  // returned "missing key", and ApplyMetricFilters swallowed the error with
  // `continue`. The chart drew UNFILTERED while this modal printed the
  // filter chip above it — the UI asserting a filter that was never applied.
  // Typing it as FilterExpr makes the drift a compile error.
  filters?: FilterExpr[];        // e.g. tablespace_name = SYSTEM
};

export function Stat({ label, value, tone, onClick, sub }: {
  label: string; value: string; tone?: 'ok' | 'warn' | 'err';
  onClick?: () => void;
  sub?: string;
}) {
  const color = tone === 'err' ? 'var(--err)'
              : tone === 'warn' ? 'var(--warn)'
              : tone === 'ok'  ? 'var(--ok)'
              : 'var(--text)';
  // When clickable we render the tile as a button so the
  // operator gets keyboard + screen-reader treatment for free,
  // a subtle hover state, and an arrow affordance in the
  // corner to telegraph the drill-down.
  const inner = (
    <>
      <div style={{
        fontSize: 9, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
        display: 'flex', alignItems: 'center', gap: 4,
      }}>
        {label}
        {onClick && (
          <span aria-hidden style={{ marginLeft: 'auto', opacity: 0.5 }}>↗</span>
        )}
      </div>
      <div style={{ fontSize: 16, fontWeight: 700, color,
                     fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
        {value}
      </div>
      {sub && (
        <div style={{
          fontSize: 10, color: 'var(--text3)', marginTop: 2,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>{sub}</div>
      )}
    </>
  );
  if (onClick) {
    return (
      <button type="button" onClick={onClick}
        title="Open metric chart"
        style={{
          all: 'unset', display: 'block', cursor: 'pointer',
          padding: '8px 10px', borderRadius: 4,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          transition: 'border-color 0.12s, background 0.12s',
        }}
        onMouseEnter={e => {
          e.currentTarget.style.borderColor = 'var(--accent2)';
          e.currentTarget.style.background = 'var(--bg3)';
        }}
        onMouseLeave={e => {
          e.currentTarget.style.borderColor = 'var(--border)';
          e.currentTarget.style.background = 'var(--bg2)';
        }}>
        {inner}
      </button>
    );
  }
  return (
    <div style={{
      padding: '8px 10px', borderRadius: 4,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      {inner}
    </div>
  );
}

export function GaugeStat({ label, usage, limit, sub, onClick }: {
  label: string; usage: number; limit: number; sub?: string;
  onClick?: () => void;
}) {
  const pct = limit > 0 ? (usage / limit) * 100 : 0;
  const tone: 'ok' | 'warn' | 'err' =
    pct >= 90 ? 'err' : pct >= 75 ? 'warn' : 'ok';
  const fill = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--ok)';
  const inner = (
    <>
      <div style={{
        fontSize: 9, color: 'var(--text3)',
        textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
        display: 'flex', alignItems: 'center',
      }}>
        {label}
        {onClick && (
          <span aria-hidden style={{ marginLeft: 'auto', opacity: 0.5 }}>↗</span>
        )}
      </div>
      <div style={{
        fontSize: 14, fontWeight: 700,
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        marginBottom: 4,
      }}>
        {fmtNum(usage)} <span style={{ color: 'var(--text3)', fontWeight: 400 }}>/ {fmtNum(limit)}</span>
      </div>
      <div style={{
        height: 4, background: 'var(--bg3)', borderRadius: 2, overflow: 'hidden',
      }}>
        <div style={{
          width: `${Math.min(100, pct)}%`, height: '100%', background: fill,
          transition: 'width 0.2s',
        }} />
      </div>
      {sub && (
        <div style={{
          fontSize: 10, color: 'var(--text3)', marginTop: 4,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>{sub}</div>
      )}
    </>
  );
  if (onClick) {
    return (
      <button type="button" onClick={onClick}
        title="Open metric chart"
        style={{
          all: 'unset', display: 'block', cursor: 'pointer',
          padding: '8px 10px', borderRadius: 4,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          transition: 'border-color 0.12s, background 0.12s',
        }}
        onMouseEnter={e => {
          e.currentTarget.style.borderColor = 'var(--accent2)';
          e.currentTarget.style.background = 'var(--bg3)';
        }}
        onMouseLeave={e => {
          e.currentTarget.style.borderColor = 'var(--border)';
          e.currentTarget.style.background = 'var(--bg2)';
        }}>
        {inner}
      </button>
    );
  }
  return (
    <div style={{
      padding: '8px 10px', borderRadius: 4,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      {inner}
    </div>
  );
}

// OracleMetricDrillModal renders a time-series chart for one
// metric over the panel's current window. Same MultiLineChart
// the services / dashboards use, so an operator who already
// reads our other charts gets identical mechanics (hover
// crosshair, axis formatting, legend). Filters ride through
// to /api/metrics/query so a tablespace row chart only shows
// that tablespace's usage, not every tablespace's blended.
export function OracleMetricDrillModal({ drill, range, instance, engine, onClose }: {
  drill: OracleDrill;
  // v0.9.279 — the panel already knows both; passing them here rather than
  // stamping them onto every DrillSpec keeps four call sites instead of the
  // dozen-odd places a drill is constructed.
  //
  // Without them the chart blended every instance of the engine: an install
  // with two Oracle databases drew one line that belonged to neither. It is
  // NOT expressible as an ordinary filter — service_name names the RECEIVER,
  // so both instances share it (measured: oracledb-receiver for both), and the
  // discriminating key differs per receiver family. The backend compiles it to
  // a per-engine OR; see dbInstanceScopeClause.
  instance: string;
  engine: 'oracle' | 'postgresql' | 'mysql' | 'redis';
  range: TimeRange;
  onClose: () => void;
}) {
  const [series, setSeries] = useState<SpanMetricSeries[] | null | undefined>(undefined);
  useEffect(() => {
    setSeries(undefined);
    const { from, to } = timeRangeToNs(range);
    const filterArg = drill.filters && drill.filters.length > 0
      ? JSON.stringify(drill.filters)
      : undefined;
    api.metricQuery({
      name: drill.metric,
      filters: filterArg,
      instance,
      engine,
      agg: 'avg',
      from, to,
    })
      .then(r => setSeries(r ?? []))
      .catch(() => setSeries(null));
  }, [drill, range, instance, engine]);

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
      display: 'grid', placeItems: 'center', zIndex: 'var(--z-modal)',
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 880, maxWidth: '94vw', maxHeight: '88vh', overflow: 'auto',
        padding: 20, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{
          display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 14,
        }}>
          <div style={{ fontSize: 14, fontWeight: 700 }}>{drill.label}</div>
          <code style={{
            fontSize: 11, color: 'var(--text3)',
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          }}>{drill.metric}</code>
          {drill.filters && drill.filters.length > 0 && (
            <span style={{ fontSize: 10, color: 'var(--text3)' }}>
              {drill.filters.map(f => `${f.k} ${f.op} "${f.v.join(', ')}"`).join(' · ')}
            </span>
          )}
          <span style={{ marginLeft: 'auto' }}>
            <Link to={metricCatalogueHref(drill.metric)}
                  style={{ fontSize: 11, marginRight: 12 }}>
              Open in Explore →
            </Link>
            <Button variant="secondary" size="sm" onClick={onClose}>Close</Button>
          </span>
        </div>
        {series === undefined && <Spinner />}
        {series === null && (
          <div style={{ fontSize: 12, color: 'var(--err)' }}>
            Failed to load metric series.
          </div>
        )}
        {series && series.length === 0 && (
          <Empty icon="◯" title="No data points">
            No metric_points found for <code>{drill.metric}</code> in this window.
            Wire the OracleDB receiver against this instance to populate.
          </Empty>
        )}
        {series && series.length > 0 && (
          <MultiLineChart series={series} unit={drill.unit} height={360} />
        )}
      </div>
    </div>
  );
}

// TopSQLTable lists the heaviest SQL statements by accumulated
// elapsed time over the window — Oracle's authoritative
// "which statement is the DB working hardest on" view.
// Complementary to the span-derived "Top statements" further
// down: V$SQL sees everything the DB executes, traces only see
// what the application emits.
export function TopSQLTable({ rows, instance, range }: {
  rows: TopSQLRow[];
  instance: string;
  // v0.9.1324 (§3.1 K2) — the statement→/traces exemplar link was the last
  // windowless pivot in this file; HostLink next to it has carried the
  // window since v0.9.968.
  range: TimeRange;
}) {
  const dt = useDataTable<TopSQLRow>({
    storageKey: 'deps-topsql', columns: TOPSQL_COLS, rows,
    initialSort: { id: 'elapsed', dir: 'desc' },
  });
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Top SQL by elapsed time
      </div>
      {/* is-scroll: iç kaydırmalı kapta yapışkan başlık. `is-fit` DEĞİL
          (top: var(--controls-h) burada yanlış referans — R7/v0.9.697). */}
      <div className="table-wrap is-scroll" style={{ maxHeight: 240, overflowY: 'auto' }}>
        <table style={{ tableLayout: 'fixed', width: '100%' }}>
          <DataTableColgroup dt={dt} />
          <DataTableHead dt={dt} />
          <tbody>
            {dt.sortedRows.map((r, i) => (
              <tr key={i}>
                <td style={{
                  fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 11,
                  maxWidth: 600, wordBreak: 'break-word',
                }}>
                  {r.sql
                    ? (
                      <>
                        {r.sql}
                        {/* Trace exemplars — V$SQL text is normalised
                            so the LIKE-prefix is best-effort. Scopes
                            by the receiver instance as service +
                            rootOnly=false (db.statement lives on the
                            child DB span). */}
                        <Link to={statementTracesHref({ window: range, statement: r.sql, service: instance })}
                          onClick={e => e.stopPropagation()}
                          title="Find traces running this statement (LIKE-prefix, best-effort)"
                          style={{
                            marginLeft: 8, fontSize: 10, whiteSpace: 'nowrap',
                            color: 'var(--accent2)', fontWeight: 500,
                          }}>
                          → traces
                        </Link>
                      </>
                    )
                    : <span style={{ color: 'var(--text3)' }}>(unknown)</span>}
                </td>
                <td className="num mono">{r.elapsedSec.toFixed(1)}s</td>
                <td className="num mono">{fmtNum(r.executions)}</td>
                <td className="num mono">{r.avgElapsedMs.toFixed(1)}ms</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// TopSQLSection wraps TopSQLTable with the section header + an
// explicit EMPTY state. Postgres / MySQL receivers only emit
// engine-authoritative statement stats when the operator has
// enabled pg_stat_statements / performance_schema scraping — the
// common case (and the bundled demo, which only emits Oracle) is
// zero rows. We render an Empty with a hint pointing at the fix
// rather than a blank gap, mirroring the no-fake-data policy of
// the rest of the panel. When rows exist we delegate to the same
// TopSQLTable the Oracle panel uses, so the v0.7.67 statement→
// /traces exemplar link is shared across all three engines.
export function TopSQLSection({ rows, instance, hint, range }: {
  rows: { sql: string; elapsedSec: number; executions: number; avgElapsedMs: number }[];
  instance: string;
  hint: string;
  range: TimeRange;
}) {
  if (rows.length > 0) {
    return <TopSQLTable rows={rows} instance={instance} range={range} />;
  }
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        Top SQL by elapsed time
      </div>
      <Empty icon="◯" title="No engine-authoritative statement metrics">
        {hint}
      </Empty>
    </div>
  );
}

export function fmtBytes(v: number): string {
  if (!isFinite(v) || v <= 0) return '0 B';
  if (v >= 1e12) return (v / 1e12).toFixed(2) + ' TB';
  if (v >= 1e9)  return (v / 1e9).toFixed(2)  + ' GB';
  if (v >= 1e6)  return (v / 1e6).toFixed(1)  + ' MB';
  if (v >= 1e3)  return (v / 1e3).toFixed(1)  + ' kB';
  return v.toFixed(0) + ' B';
}

// fmtDuration — compact seconds → "Nd Nh" / "Nh Nm" / "Nm" /
// "Ns" for the Redis uptime tile. Sub-day TTLs and uptimes read
// better than a raw second count.
export function fmtDuration(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return '0s';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${Math.floor(sec)}s`;
}

// PanelHeader is the engine-tile chrome shared by Postgres /
// MySQL / Redis (and now Oracle by copy). status badge +
// optional secondary chip (Redis role) + instance label on
// the right. Centralised so all three panels read identically.
export function PanelHeader({ engineLabel, instance, status, color, extraBadge, range }: {
  engineLabel: string;
  instance: string;
  status: 'up' | 'down' | undefined;
  color: string;
  extraBadge?: string;
  // v0.9.968 — the panel's window, threaded purely for HostLink.
  range?: TimeRange;
}) {
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10,
      fontSize: 12, fontWeight: 700, color,
    }}>
      <span style={{ fontSize: 13 }}>⛁</span>
      {engineLabel}
      {status && (
        <span title={status === 'up'
          ? 'receiver metric_points present in window'
          : 'No receiver metric_points seen — receiver may be down or not yet wired'}
          style={{
            fontSize: 9, padding: '1px 6px', borderRadius: 3,
            background: status === 'up'
              ? 'color-mix(in srgb, var(--ok) 15%, transparent)'
              : 'color-mix(in srgb, var(--err) 15%, transparent)',
            color: status === 'up' ? 'var(--ok)' : 'var(--err)',
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            textTransform: 'uppercase', letterSpacing: '.5px',
          }}>{status}</span>
      )}
      {extraBadge && (
        <span style={{
          fontSize: 9, padding: '1px 6px', borderRadius: 3,
          background: 'rgba(120,120,120,0.15)', color: 'var(--text2)',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          textTransform: 'uppercase', letterSpacing: '.5px',
        }}>{extraBadge}</span>
      )}
      <span style={{
        marginLeft: 'auto', fontSize: 10, color: 'var(--text3)',
        fontWeight: 400, fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      }}>
        instance: {instance || '(unknown)'}
      </span>
      {instance && <HostLink instance={instance} range={range} />}
    </div>
  );
}

// HostLink — unobtrusive cross-link from a receiver instance to
// the host/service infra view (/service?name=…). Degrades
// gracefully when the instance doesn't resolve to a known
// service (the Service page shows its own empty state).
export function HostLink({ instance, range }: { instance: string; range?: TimeRange }) {
  return (
    // v0.9.968 — the receiver panel's own window. Its metrics were read
    // over exactly this range; a host link that opens on the sticky one
    // shows a different machine-hour than the gauges beside it.
    <Link to={serviceHref(instance, { range })}
      onClick={e => e.stopPropagation()}
      title="Open this host / service in the infra view"
      style={{
        fontSize: 10, fontWeight: 500, color: 'var(--accent2)',
        fontFamily: 'ui-monospace, SFMono-Regular, monospace',
      }}>
      host ↗
    </Link>
  );
}

export function PanelErr() {
  return (
    <div style={{ fontSize: 12, color: 'var(--err)' }}>Receiver metrics query failed.</div>
  );
}

export function SubHeader({ label }: { label: string }) {
  return (
    <div style={{
      fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
      textTransform: 'uppercase', letterSpacing: 0.4,
    }}>{label}</div>
  );
}

// WaitClassesBar renders a wait-class distribution as a single
// stacked horizontal bar — at-a-glance "where is the DB spending
// its time". Mirrors the System Wait Classes panel in Oracle's
// reference Grafana dashboard. Sum of perSec across classes is the
// total wait pressure: a 1.0 result means one concurrent client
// fully blocked on the DB. Moved here from OraclePanel (v0.8.391,
// Stage-2 D3) so the cross-engine wait/lock strip could reuse it;
// o strip v0.9.852'de söküldü ve tek tüketici yeniden OraclePanel —
// dosya değiştirilmedi, taşımayı geri almak sebepsiz bir diff olurdu.
export function WaitClassesBar({ waits, onClickClass }: {
  waits: { name: string; perSec: number }[];
  onClickClass?: (cls: string) => void;
}) {
  const total = waits.reduce((a, w) => a + w.perSec, 0);
  // Stable, semantic colour-per-class. user_io is the heaviest
  // typical class so we give it the most-visible blue; commit
  // gets green (success-coded); concurrency red (where row
  // locks live).
  const CLASS_COLOR: Record<string, string> = {
    user_io:       '#388bfd',
    system_io:     '#5b8fb9',
    commit:        '#3fb950',
    network:       '#a371f7',
    concurrency:   '#f0703f',
    application:   '#f5b343',
    configuration: '#39c5cf',
    scheduler:     '#db61a2',
    cluster:       '#7d8590',
    other:         '#6dbf5b',
  };
  const colorOf = (n: string) => CLASS_COLOR[n.toLowerCase()] ?? '#7d8590';
  if (total <= 0) return null;
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8,
        fontSize: 11, fontWeight: 700, marginBottom: 6, color: 'var(--text2)',
        textTransform: 'uppercase', letterSpacing: 0.4,
      }}>
        System wait classes
        <span style={{
          fontWeight: 400, color: 'var(--text3)',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
          textTransform: 'none', letterSpacing: 0,
        }}>
          total {total.toFixed(2)} s/s
        </span>
      </div>
      <div style={{
        display: 'flex', height: 18, borderRadius: 3, overflow: 'hidden',
        border: '1px solid var(--border)',
      }}>
        {waits.map(w => {
          const pct = (w.perSec / total) * 100;
          if (pct < 0.5) return null; // suppress sub-pixel slivers
          const handleClick = onClickClass ? () => onClickClass(w.name) : undefined;
          return (
            <div key={w.name}
              onClick={handleClick}
              title={`${w.name}: ${w.perSec.toFixed(3)} s/s (${pct.toFixed(1)}%)${handleClick ? ' · click to chart' : ''}`}
              style={{
                width: `${pct}%`, background: colorOf(w.name),
                cursor: handleClick ? 'pointer' : 'help',
              }} />
          );
        })}
      </div>
      <div style={{
        display: 'flex', flexWrap: 'wrap', gap: 10, marginTop: 6, fontSize: 10,
      }}>
        {waits
          .filter(w => w.perSec > 0)
          .slice(0, 8)
          .map(w => {
            const handleClick = onClickClass ? () => onClickClass(w.name) : undefined;
            const labelInner = (
              <>
                <span style={{
                  width: 8, height: 8, borderRadius: 2,
                  background: colorOf(w.name),
                }} />
                <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
                  {w.name}
                </span>
                <span style={{ color: 'var(--text3)' }}>
                  {w.perSec.toFixed(2)}
                </span>
              </>
            );
            if (handleClick) {
              return (
                <button key={w.name} type="button" onClick={handleClick}
                  title={`Chart wait time · ${w.name}`}
                  style={{
                    all: 'unset', cursor: 'pointer',
                    display: 'inline-flex', alignItems: 'center', gap: 4,
                    color: 'var(--text2)',
                  }}>
                  {labelInner}
                </button>
              );
            }
            return (
              <span key={w.name} style={{
                display: 'inline-flex', alignItems: 'center', gap: 4,
                color: 'var(--text2)',
              }}>
                {labelInner}
              </span>
            );
          })}
      </div>
    </div>
  );
}

/**
 * DegradedBand — "bu sayılar EKSİK" şeridi (v0.10.11).
 *
 * Neden ayrı bir durum: dört motor okuyucusu alt-sorgu hatalarını
 * yutuyor ve KOŞULSUZ başarı döndürüyordu. Bir ClickHouse hatasında
 * panel eksik değil, SIFIRLARLA DOLU çiziliyordu — yani hata, sakin
 * boştaki bir veritabanından ayırt edilemiyordu. Operatörün paged
 * olduğunda baktığı ilk gösterge yanlış YÖNDE yalan söylüyordu.
 *
 * Şerit ızgaranın ÜSTÜNDE ve ızgara yine çiziliyor: kısmi veri hâlâ
 * işe yarar, yeter ki eksik olduğu söylensin. Izgarayı gizlemek,
 * bugünkü sessizliği gürültülü bir boşlukla değiştirmek olurdu.
 *
 * `reason` backend'den geliyor ve SQL/host ayrıntısı taşımıyor
 * (chstore/engine_degraded.go sözleşmesi).
 */
export function DegradedBand({ reason }: { reason?: string }) {
  if (!reason) return null;
  return (
    <div style={{
      border: '1px solid var(--warn)',
      background: 'color-mix(in srgb, var(--warn) 10%, transparent)',
      borderRadius: 6, padding: '7px 11px', marginBottom: 10,
      fontSize: 12, color: 'var(--text)', lineHeight: 1.45,
    }}>
      <strong>⚠ Bu paneldeki sayılar eksik.</strong>{' '}{reason}.{' '}
      Sıfır görünen alanlar “veri yok” anlamına GELMEZ — okunamadılar.
    </div>
  );
}
