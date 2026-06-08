import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { timeRangeToNs, fmtNum, isMessagingDep, tsShort } from '@/lib/utils';
import type { TimeRange, LogRow, ServiceMap } from '@/lib/types';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { LogsHistogram } from '@/components/LogsHistogram';
import { LogTable } from '@/components/LogTable';
import { TopologyPillGraph, type PillNode, type PillEdge, type PillLevel } from '@/components/TopologyPillGraph';
import { FocusedNeighborhood } from '@/components/topology/FocusedNeighborhood';

// Service-scoped Traces / Logs / Topology tabs — the design's tab strip
// beyond Overview/Operations/Details. All read-only, all reuse the
// app-wide primitives (LogsHistogram / LogTable / TopologyPillGraph) so the
// operator's eye builds the same scan pattern as the standalone surfaces.

// ── Traces: slowest traces for this service ─────────────────────────────
export function ServiceTracesTab({ service, range }: { service: string; range: TimeRange }) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const rangeParam = encodeRange(range);
  const q = useQuery({
    queryKey: ['service-tab-traces', service, from, to],
    queryFn: () => api.traces({ service, from, to, sort: 'duration', order: 'desc', limit: 25, count: 'skip' }),
    enabled: !!service,
    staleTime: 30_000,
  });
  const traces = q.data?.traces ?? [];
  const maxDur = useMemo(() => Math.max(1, ...traces.map(t => t.durationMs)), [traces]);

  return (
    <div className="card" style={{ marginTop: 4 }}>
      <div className="ov-card-h">
        <h3>Slowest traces</h3>
        <span className="ov-right">
          <Link className="ov-sub" to={`/traces?service=${encodeURIComponent(service)}&range=${rangeParam}`}>Open in Traces →</Link>
        </span>
      </div>
      {q.isLoading ? (
        <div className="ov-card-b"><Spinner /></div>
      ) : traces.length === 0 ? (
        <div className="ov-card-b"><Empty icon="⋮" title="No traces in this window" /></div>
      ) : (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ tableLayout: 'fixed', width: '100%' }}>
            <colgroup>
              <col style={{ width: 150 }} /><col style={{ width: 132 }} /><col /><col style={{ width: 70 }} />
              <col style={{ width: 80 }} /><col style={{ width: 160 }} />
            </colgroup>
            <thead><tr>
              <th style={{ textAlign: 'left' }}>Trace</th>
              <th style={{ textAlign: 'left' }}>Started</th>
              <th style={{ textAlign: 'left' }}>Root operation</th>
              <th className="num">Spans</th>
              <th className="num">Status</th>
              <th className="num">Duration</th>
            </tr></thead>
            <tbody>
              {traces.map(t => (
                <tr key={t.traceId} style={{ cursor: 'pointer' }}>
                  <td><Link className="mono" style={{ color: 'var(--accent)' }} to={`/trace?id=${t.traceId}`}>{t.traceId.slice(0, 16)}…</Link></td>
                  <td className="mono" style={{ color: 'var(--text2)', fontSize: 11.5, whiteSpace: 'nowrap' }} title={tsShort(t.startTime)}>{tsShort(t.startTime)}</td>
                  <td><span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'block' }} title={t.rootName}>{t.rootName || '—'}</span></td>
                  <td className="num">{t.spanCount}</td>
                  <td className="num"><span className={`badge ${t.hasError ? 'b-err' : 'b-ok'}`}>{t.hasError ? 'ERROR' : 'OK'}</span></td>
                  <td>
                    <div className="ov-barcell">
                      <span className="mono" style={{ minWidth: 56 }}>{t.durationMs.toFixed(0)} ms</span>
                      <span className="ov-minibar"><i style={{ width: `${(t.durationMs / maxDur) * 100}%`, background: t.hasError ? 'var(--err)' : 'var(--accent)' }} /></span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ── Logs: full scoped log view (search + level facets + volume + table) ──
type Lvl = 'error' | 'warn' | 'info' | 'debug';
const LVL_ORDER: Lvl[] = ['error', 'warn', 'info', 'debug'];

// Normalise a row to one of the four facet buckets — prefer the canonical
// severityText, fall back to the OTel numeric severity (ERROR≥17, WARN≥13,
// INFO≥9, else DEBUG/TRACE).
function levelOf(r: LogRow): Lvl {
  const t = (r.severityText || '').toUpperCase();
  if (t.startsWith('ERR') || t.startsWith('FATAL') || t.startsWith('CRIT')) return 'error';
  if (t.startsWith('WARN')) return 'warn';
  if (t.startsWith('INFO')) return 'info';
  if (t.startsWith('DEBUG') || t.startsWith('TRACE')) return 'debug';
  const s = r.severity;
  if (s >= 17) return 'error';
  if (s >= 13) return 'warn';
  if (s >= 9) return 'info';
  return 'debug';
}

const LVL_BADGE: Record<Lvl, string> = { error: 'b-err', warn: 'b-warn', info: 'b-ok', debug: 'b-mut' };

export function ServiceLogsTab({ service, range }: { service: string; range: TimeRange }) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const rangeParam = encodeRange(range);

  // Search box → debounced into the query key (server-side substring
  // search scales). Level facet filters the fetched page client-side
  // (instant, mirrors the design); the histogram always shows full
  // volume-by-level for the service so the facet doesn't hide the shape.
  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [lvl, setLvl] = useState<'all' | Lvl>('all');
  useEffect(() => { const t = setTimeout(() => setSearch(searchInput), 300); return () => clearTimeout(t); }, [searchInput]);

  const filter = useMemo(
    () => ({ service, search, severity: 0, traceId: '', spanId: '' }),
    [service, search],
  );

  const q = useQuery({
    queryKey: ['service-tab-logs', service, from, to, search],
    queryFn: () => api.logs({ limit: 200, from, to, service, search: search || undefined }),
    enabled: !!service,
    staleTime: 15_000,
    // v0.8.3 (operator-reported ES incident) — /api/logs is uncached and
    // opens an ES Point-in-Time per call; a tab focus/reconnect shouldn't
    // re-open one. The filter/range/search change still refetches via the key.
    refetchOnWindowFocus: false,
  });
  const logs = useMemo(() => q.data?.logs ?? [], [q.data]);

  const counts = useMemo(() => {
    const c: Record<string, number> = { all: logs.length, error: 0, warn: 0, info: 0, debug: 0 };
    for (const r of logs) c[levelOf(r)]++;
    return c;
  }, [logs]);
  const rows = useMemo(() => (lvl === 'all' ? logs : logs.filter(r => levelOf(r) === lvl)), [logs, lvl]);

  return (
    <div style={{ marginTop: 4 }}>
      {/* Filter bar — substring search + level facet chips with counts */}
      <div className="ov-logbar">
        <input className="field" placeholder="Filter logs (message, service)…" value={searchInput}
          onChange={e => setSearchInput(e.target.value)} style={{ flex: '1 1 280px', maxWidth: 360 }} />
        <span className={'ov-facet' + (lvl === 'all' ? ' on' : '')} onClick={() => setLvl('all')}>
          All <span className="n">{counts.all}</span>
        </span>
        {LVL_ORDER.map(l => (
          <span key={l} className={'ov-facet' + (lvl === l ? ' on' : '')} onClick={() => setLvl(l)}>
            <span className={`badge ${LVL_BADGE[l]}`}>{l.toUpperCase()}</span>
            <span className="n">{counts[l]}</span>
          </span>
        ))}
        <Link className="ov-sub" style={{ marginLeft: 'auto' }}
          to={`/logs?service=${encodeURIComponent(service)}&range=${rangeParam}`}>Open in Logs →</Link>
      </div>

      {/* Volume histogram — full service volume, stacked by level */}
      <div className="card ov-mb">
        <div className="ov-card-h"><h3>Log volume</h3><span className="ov-sub">by level</span></div>
        <div className="ov-card-b" style={{ paddingTop: 8 }}>
          <LogsHistogram range={{ from, to }} filter={filter} />
        </div>
      </div>

      {/* Log table */}
      <div className="card">
        <div className="ov-card-h">
          <h3>Logs</h3>
          <span className="ov-sub">{rows.length} lines{lvl !== 'all' ? ` · ${lvl}` : ''}</span>
        </div>
        {q.isLoading ? (
          <TableSkeleton rows={10} cols={4} />
        ) : rows.length === 0 ? (
          <div className="ov-card-b"><Empty icon="≡" title={`No logs for ${service} in this window`} /></div>
        ) : (
          <LogTable logs={rows} />
        )}
      </div>
    </div>
  );
}

// buildPillTiers — project a (2-hop-scoped) ServiceMap onto the shared
// pill renderer. Kafka/broker nodes are dropped (isMessagingDep — the same
// exclusion the circle graph applied) so a broadcast topic can't explode
// the layout; survivors lay out in tiers around the focus:
// upstream callers | focus | direct deps | 2nd-hop backends.
function buildPillTiers(map: ServiceMap, focus: string): {
  nodes: PillNode[]; edges: PillEdge[]; columns: string[][];
} {
  const live = map.nodes.filter(n => !isMessagingDep(n.kind, n.subkind));
  const byId = new Map(live.map(n => [n.service, n] as const));
  const edges = map.edges.filter(e => byId.has(e.caller) && byId.has(e.callee));

  const lvl = (er: number): PillLevel => (er > 0.05 ? 'red' : er > 0.01 ? 'amber' : 'green');
  const nodes: PillNode[] = live.map(n => ({
    id: n.service,
    name: n.subkind || n.service.replace(/^(db|queue|ext):/, ''),
    level: lvl(n.errorRate),
    sub: `${(n.errorRate * 100).toFixed(2)}% · ${fmtNum(n.spanCount)}`,
    title: `${n.service} · ${fmtNum(n.spanCount)} spans · ${(n.errorRate * 100).toFixed(2)}% err`,
  }));
  const pillEdges: PillEdge[] = edges.map(e => {
    const er = Math.max(byId.get(e.caller)?.errorRate ?? 0, byId.get(e.callee)?.errorRate ?? 0);
    return { from: e.caller, to: e.callee, level: er > 0.05 ? 'err' : er > 0.01 ? 'warn' : undefined };
  });

  // Tiers: callers | focus | direct deps | 2nd-hop. Each node placed once.
  const placed = new Set<string>();
  const callers: string[] = [];
  for (const e of edges) if (e.callee === focus && e.caller !== focus && byId.has(e.caller) && !placed.has(e.caller)) { callers.push(e.caller); placed.add(e.caller); }
  placed.add(focus);
  const deps: string[] = [];
  for (const e of edges) if (e.caller === focus && e.callee !== focus && byId.has(e.callee) && !placed.has(e.callee)) { deps.push(e.callee); placed.add(e.callee); }
  const depSet = new Set(deps);
  const second: string[] = [];
  for (const e of edges) if (depSet.has(e.caller) && byId.has(e.callee) && !placed.has(e.callee)) { second.push(e.callee); placed.add(e.callee); }
  for (const n of live) if (!placed.has(n.service)) { second.push(n.service); placed.add(n.service); }
  const columns = [callers, [focus], deps, second].filter(c => c.length > 0);
  return { nodes, edges: pillEdges, columns };
}

// ── Topology: tiered pill-card neighbourhood (shared TopologyPillGraph) ──
export function ServiceTopologyTab({ service, range }: { service: string; range: TimeRange }) {
  const navigate = useNavigate();
  const rangeParam = encodeRange(range);
  // v0.8.93 — render the SAME focused topology as the /topology page
  // (FocusedNeighborhood) so the service tab and the full page show one
  // identical graph (was a different ServiceGraph neighborhood view).
  const [hops, setHops] = useState(1);
  const [errorsOnly, setErrorsOnly] = useState(false);
  return (
    <div className="card" style={{ marginTop: 4 }}>
      <div className="ov-card-h">
        <h3>Topology</h3>
        <span className="ov-sub">{service} neighborhood</span>
        <span className="ov-right">
          <Link className="ov-sub" to={`/topology?focus=${encodeURIComponent(service)}&range=${rangeParam}`}>
            Open full Topology →
          </Link>
        </span>
      </div>
      <div className="ov-card-b">
        <FocusedNeighborhood
          range={range}
          focus={service}
          hops={hops}
          errorsOnly={errorsOnly}
          onHops={setHops}
          onErrorsOnly={setErrorsOnly}
          onRecenter={(s) => navigate(`/service?service=${encodeURIComponent(s)}&range=${rangeParam}`)}
          onClear={() => navigate(`/topology?range=${rangeParam}`)}
        />
      </div>
    </div>
  );
}
