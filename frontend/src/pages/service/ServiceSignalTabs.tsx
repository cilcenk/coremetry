import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { timeRangeToNs, fmtNum, isMessagingDep } from '@/lib/utils';
import type { TimeRange, LogRow, ServiceMap } from '@/lib/types';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { LogsHistogram } from '@/components/LogsHistogram';
import { LogTable } from '@/components/LogTable';
import { TopologyPillGraph, type PillNode, type PillEdge, type PillLevel } from '@/components/TopologyPillGraph';
import { FocusedNeighborhood } from '@/components/topology/FocusedNeighborhood';

// Service-scoped Logs / Topology tabs — the design's tab strip beyond
// Overview/Operations/Details. All read-only, all reuse the app-wide
// primitives (LogsHistogram / LogTable / TopologyPillGraph) so the
// operator's eye builds the same scan pattern as the standalone surfaces.
//
// v0.9.212 — the Traces tab that used to live here is gone: it was a 25-row
// "slowest traces" table plus an "Open in Traces →" link, and the service
// header's ⋮ Traces chip already lands on that same page.

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

// v0.9.358 — histogram serisi adını levelOf ile AYNI kurallarla banda indirger;
// iki ayrı sınıflandırıcı sürüklenirse çip ile çubuk yine ayrışırdı.
function bandOfName(name: string): Lvl {
  const t = name.toUpperCase();
  if (t.startsWith('ERR') || t.startsWith('FATAL') || t.startsWith('CRIT')) return 'error';
  if (t.startsWith('WARN')) return 'warn';
  if (t.startsWith('INFO')) return 'info';
  return 'debug';
}
// Sunucu severity paramı MINIMUM'dur (OTel numarası). error bandı için kesin
// (>=17 zaten bandın kendisi); warn/info için taban — sayfa o taban ÜSTÜNDEN
// gelir, istemci bant kesimi görünümü tamamlar.
const LVL_MIN_SEV: Record<Lvl, number> = { error: 17, warn: 13, info: 9, debug: 0 };

export function ServiceLogsTab({ service, range, windowNs }: {
  service: string; range: TimeRange;
  // v0.9.361 — sayfanın ZATEN memo'lu penceresi. Sekme kendi
  // timeRangeToNs'ini üretiyordu; {tab==='logs' && …} her sekme dönüşünde
  // unmount/remount ettiği için her dönüş taze bir nanosaniye basıyor,
  // React Query anahtarı VE sunucu cache anahtarları (list + histogram)
  // ıskalıyordu — her Overview↔Logs gidiş-gelişi iki gerçek ES sorgusuydu.
  windowNs?: { from: number; to: number };
}) {
  const local = useMemo(() => timeRangeToNs(range), [range]);
  const { from, to } = windowNs ?? local;
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

  // v0.9.358 — seçili bant SUNUCUYA gider: eskiden 200 satır her zaman
  // "her seviyeden en yeni 200"dü ve ERROR çipi o sayfada sıfır sayarken
  // histogram kıpkırmızı olabiliyordu (dakikada 10k satır basan bir servis
  // 30 dakikalık pencerenin ilk ~1.2 saniyesiyle 200'ü dolduruyor).
  const minSev = lvl === 'all' ? undefined : (LVL_MIN_SEV[lvl] || undefined);
  const q = useQuery({
    queryKey: ['service-tab-logs', service, from, to, search, minSev ?? 0],
    queryFn: () => api.logs({ limit: 200, from, to, service, search: search || undefined, severity: minSev }),
    enabled: !!service,
    staleTime: 15_000,
    // v0.8.3 (operator-reported ES incident) — /api/logs is uncached and
    // opens an ES Point-in-Time per call; a tab focus/reconnect shouldn't
    // re-open one. The filter/range/search change still refetches via the key.
    refetchOnWindowFocus: false,
  });
  const logs = useMemo(() => q.data?.logs ?? [], [q.data]);

  // v0.9.358 — çip sayıları histogramın ZATEN çektiği severity serisinden
  // (pencerenin tamamı), sayfadan değil. Aynı ekrandaki iki sayı aynı
  // popülasyonu saymalı; eskiden çip en yeni 200 satırı sayıyordu.
  // Histogram verisi gelmeden sayfa-türevi sayılara düşer (yüklenme anı).
  const [bandTotals, setBandTotals] = useState<Record<string, number> | null>(null);
  const onHistSeries = useMemo(() => (series: { name: string; total: number }[]) => {
    const c: Record<string, number> = { all: 0, error: 0, warn: 0, info: 0, debug: 0 };
    for (const sr of series) { c[bandOfName(sr.name)] += sr.total; c.all += sr.total; }
    setBandTotals(c);
  }, []);

  const counts = useMemo(() => {
    if (bandTotals) return bandTotals;
    const c: Record<string, number> = { all: logs.length, error: 0, warn: 0, info: 0, debug: 0 };
    for (const r of logs) c[levelOf(r)]++;
    return c;
  }, [bandTotals, logs]);
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
          <LogsHistogram range={{ from, to }} filter={filter} onSeries={onHistSeries} />
        </div>
      </div>

      {/* Log table */}
      <div className="card">
        <div className="ov-card-h">
          <h3>Logs</h3>
          <span className="ov-sub">
            {rows.length} lines{lvl !== 'all' ? ` · ${lvl}` : ''}
            {/* v0.9.358 — sınır varsa görünür olur: 200'lük sayfa penceredeki
                her şey değilse gerçek toplamı söyle (total zaten telde). */}
            {(q.data?.total ?? 0) > logs.length &&
              ` · pencerede ${q.data!.total}${q.data?.totalIsLowerBound ? '+' : ''} satır, en yeni ${logs.length} gösteriliyor`}
          </span>
        </div>
        {q.isLoading ? (
          <TableSkeleton rows={10} cols={4} />
        ) : q.data?.degraded ? (
          /* v0.9.363 — ES brownout "log yok" DEĞİLDİR. Backend v0.8.350'den
             beri 200 + degraded/reason gönderiyor; bu sekme onu atıyordu. */
          <div className="ov-card-b">
            <Empty icon="⚠" title="Log backend'i yanıt veremedi">
              {q.data.reason || 'Elasticsearch yavaş ya da erişilemez — pencerede log olmadığı anlamına gelmez.'}
            </Empty>
          </div>
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
          onRecenter={(s) => navigate(`/service?name=${encodeURIComponent(s)}&tab=topology&range=${rangeParam}`)}
          onClear={() => navigate(`/topology?range=${rangeParam}`)}
        />
      </div>
    </div>
  );
}
