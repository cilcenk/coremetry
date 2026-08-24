import { useEffect, useMemo, useRef, useState } from 'react';
import { Pager } from '@/components/Pager';
import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '@/lib/api';
import { encodeRange } from '@/lib/urlState';
import { logsHref } from '@/lib/logsUrl';
import { timeRangeToNs, fmtNum, isMessagingDep } from '@/lib/utils';
import type { TimeRange, LogRow, ServiceMap } from '@/lib/types';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { LogsHistogram } from '@/components/LogsHistogram';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { LogTable } from '@/components/LogTable';
import { TopologyPillGraph, type PillNode, type PillEdge, type PillLevel } from '@/components/TopologyPillGraph';
import { FocusedNeighborhood } from '@/components/topology/FocusedNeighborhood';
import { parseTopologyHops, topologyHopsUrlValue } from './topologyHops';
import { serviceHref } from '@/lib/serviceHref';

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

export function ServiceLogsTab({ service, range, windowNs, onZoom, onZoomReset }: {
  service: string; range: TimeRange;
  // v0.9.373 — histogram brush'ı sayfanın zoom yoluna bağlar (Service.tsx
  // handleZoom: unix-SANİYE alır, geri-yığını korur; diğer grafiklerle aynı).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  onZoomReset?: () => void;
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
  // v0.9.382 (redesign D6) — arama + seviye facet'i URL'de (?lq/?llvl,
  // replace:true): sekme değişince kaybolmuyor, paylaşılabiliyor.
  // Yalnız SAHİP OLDUĞUMUZ paramlar yazılır (prev kopyalanır); debounce
  // input state'te yaşar, URL'e ödemeli (settled) değer düşer.
  // v0.9.453 (operatör onayı: "sekme ile /logs farklı") — global ortam
  // filtresi bu sekmede de uygulanır: /logs env geçiriyordu, sekme
  // geçirmiyordu; env seçiliyken iki yüzey farklı kayıt kümesi
  // gösteriyordu (v0.9.219 drift sınıfı). Liste + sayfalama + histogram
  // aynı env'i taşır; env yokken davranış değişmez.
  const [env] = useUrlEnv();
  const envParam = env || undefined;
  const [lparams, setLparams] = useSearchParams();
  const urlLq = lparams.get('lq') ?? '';
  const urlLvl = (lparams.get('llvl') as 'all' | Lvl | null) ?? 'all';
  const [searchInput, setSearchInput] = useState(urlLq);
  const [search, setSearch] = useState(urlLq);
  const lvl: 'all' | Lvl = urlLvl;
  const setLvl = (v: 'all' | Lvl) => setLparams(prev => {
    const next = new URLSearchParams(prev);
    if (v !== 'all') next.set('llvl', v); else next.delete('llvl');
    return next;
  }, { replace: true });
  useEffect(() => {
    const t = setTimeout(() => {
      setSearch(searchInput);
      setLparams(prev => {
        const next = new URLSearchParams(prev);
        if (searchInput) next.set('lq', searchInput); else next.delete('lq');
        return next;
      }, { replace: true });
    }, 300);
    return () => clearTimeout(t);
  }, [searchInput, setLparams]);

  const filter = useMemo(
    () => ({ service, search, severity: 0, traceId: '', spanId: '', env: envParam }),
    [service, search, envParam],
  );

  // v0.9.358 — seçili bant SUNUCUYA gider: eskiden 200 satır her zaman
  // "her seviyeden en yeni 200"dü ve ERROR çipi o sayfada sıfır sayarken
  // histogram kıpkırmızı olabiliyordu (dakikada 10k satır basan bir servis
  // 30 dakikalık pencerenin ilk ~1.2 saniyesiyle 200'ü dolduruyor).
  const minSev = lvl === 'all' ? undefined : (LVL_MIN_SEV[lvl] || undefined);
  const q = useQuery({
    queryKey: ['service-tab-logs', service, from, to, search, minSev ?? 0, envParam ?? ''],
    queryFn: () => api.logs({ limit: 200, from, to, service, search: search || undefined, severity: minSev, env: envParam }),
    enabled: !!service,
    staleTime: 15_000,
    // v0.8.3 (operator-reported ES incident) — /api/logs is uncached and
    // opens an ES Point-in-Time per call; a tab focus/reconnect shouldn't
    // re-open one. The filter/range/search change still refetches via the key.
    refetchOnWindowFocus: false,
  });
  // v0.9.406 (redesign açık-dilimi "200 satır daha") — kullanıcı-tetiklemeli
  // keyset sayfalama. İlk sayfa CURSOR'SUZ kalır (PIT açılmaz — v0.9.361
  // maliyet kapısı aynen); butona ilk basışta bir kez wantCursor'lu sayfa-1
  // ile cursor alınır (tek ek ES sorgusu, kullanıcı tetikli), sonrası
  // after-zinciri. Filtre/pencere değişince birikim sıfırlanır.
  const [extraRows, setExtraRows] = useState<import('@/lib/types').LogRow[]>([]);
  const [pagingBusy, setPagingBusy] = useState(false);
  const [pagingDone, setPagingDone] = useState(false);
  const cursorRef = useRef<string>('');
  useEffect(() => {
    setExtraRows([]); setPagingDone(false); cursorRef.current = '';
  }, [service, from, to, search, minSev, envParam]);
  const loadMore = async () => {
    if (pagingBusy || pagingDone) return;
    setPagingBusy(true);
    try {
      const base = { limit: 200, from, to, service, search: search || undefined, severity: minSev, env: envParam } as const;
      if (!cursorRef.current) {
        const first = await api.logs({ ...base, paging: true });
        cursorRef.current = first?.nextCursor ?? '';
        if (!cursorRef.current) { setPagingDone(true); return; }
      }
      const next = await api.logs({ ...base, after: cursorRef.current, paging: true });
      setExtraRows(r => [...r, ...(next?.logs ?? [])]);
      cursorRef.current = next?.nextCursor ?? '';
      if (!cursorRef.current || (next?.logs ?? []).length === 0) setPagingDone(true);
    } catch {
      setPagingDone(true); // hata = sessiz sonsuz-retry yok; sayaç zaten dürüst
    } finally {
      setPagingBusy(false);
    }
  };
  const logs = useMemo(() => [...(q.data?.logs ?? []), ...extraRows], [q.data, extraRows]);

  // v0.9.358 — çip sayıları histogramın ZATEN çektiği severity serisinden
  // (pencerenin tamamı), sayfadan değil. Aynı ekrandaki iki sayı aynı
  // popülasyonu saymalı; eskiden çip en yeni 200 satırı sayıyordu.
  // Histogram verisi gelmeden sayfa-türevi sayılara düşer (yüklenme anı).
  const [bandTotals, setBandTotals] = useState<Record<string, number> | null>(null);
  const onHistSeries = useMemo(() => (series: { name: string; total: number }[] | null) => {
    // v0.9.1220 — histogram fetch hatası null iletir: bandTotals'ı sıfırla
    // ki aşağıdaki memo sayfa-türevi sayılara (dürüst geri-düşüş) dönsün.
    if (series === null) { setBandTotals(null); return; }
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
          to={logsHref({ window: rangeParam, service })}>Open in Logs →</Link>
      </div>

      {/* Volume histogram — full service volume, stacked by level */}
      <div className="card ov-mb">
        <div className="ov-card-h"><h3>Log volume</h3><span className="ov-sub">by level</span></div>
        <div className="ov-card-b" style={{ paddingTop: 8 }}>
          <LogsHistogram range={{ from, to }} filter={filter} onSeries={onHistSeries}
            onRangeSelect={onZoom ? (fromNs, toNs) => onZoom(fromNs / 1e9, toNs / 1e9) : undefined}
            onZoomReset={onZoomReset} />
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
          <>
          <LogTable logs={rows} />
          {/* v0.9.406 — "200 satır daha": kullanıcı-tetiklemeli keyset
              sayfa (otomatik prefetch YOK — ES disiplini). Sayaç üstteki
              başlıkta; burada yalnız eylem + dürüst son. */}
          {/* v0.9.1016 — paylaşılan sözleşme (v0.9.1014), cursor kipi.
              v0.9.406'nın kararı korunuyor: kullanıcı-tetiklemeli keyset
              sayfa, otomatik prefetch YOK (ES disiplini). Dürüst son
              cümlesi zaten atomun varsayılanı — bu yüzeyden geldi.
              (v0.9.1078: `stickyBottom` prop'u atomdan tamamen söküldü —
              yüzen şeritler operatör kararıyla kalktı; buradaki eski
              "kart içinde kapalı" istisnası artık genel kural.) */}
          <Pager mode="cursor" count="skip"
            hasMore={!pagingDone} onMore={loadMore} loading={pagingBusy}
            loaded={logs.length} moreLabel="200 satır daha" />
          </>
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

// DEFAULT_TOPOLOGY_HOPS — servis Topology sekmesinin varsayılan komşuluk
// derinliği (v0.9.558, operatör talebi).
//
// Tek yerde durur çünkü İKİ yerde kullanılıyor: URL'den okurken
// geri-düşüş değeri ve URL'e yazarken "varsayılansa yazma" kararı.
// İkisi ayrışırsa kullanıcının seçimi sessizce yutulur — sabiti
// bölmek, tam da o hatanın davetiyesidir.
const DEFAULT_TOPOLOGY_HOPS = 2;

// ── Topology: tiered pill-card neighbourhood (shared TopologyPillGraph) ──
export function ServiceTopologyTab({ service, range }: { service: string; range: TimeRange }) {
  const navigate = useNavigate();
  const rangeParam = encodeRange(range);
  // v0.8.93 — render the SAME focused topology as the /topology page
  // (FocusedNeighborhood) so the service tab and the full page show one
  // identical graph (was a different ServiceGraph neighborhood view).
  // v0.9.381 (redesign D5) — hops + errorsOnly URL'e taşındı (?hops=2&
  // eonly=1): sekme değişiminde/paylaşımda kaybolmuyordu iddiası artık
  // doğru. replace:true; yabancı paramlar korunur (prev kopyalanır).
  // v0.9.558 — varsayılan 2 hop (operatör: "topology sekmesinde
  // servicelerin direkt 2 hop gelsin").
  //
  // Gerekçe: 1 hop yalnız doğrudan komşuları gösterir ve bir servisin
  // sorunu çoğu zaman komşusunun komşusundan gelir — operatörün her
  // seferinde elle 2'ye çıkarması gerekiyordu.
  //
  // Maliyet sınırlı: ReadServiceTopologyAggForFocus hop-farkında ve
  // hop başına BİR sınırlı MV sorgusu yapıyor (api katmanında 3'e
  // kırpılı), yani 2 hop = 2 sorgu. Kullanıcı kontrolden 1'e
  // düşürebilir.
  const [tparams, setTparams] = useSearchParams();
  const hops = Math.min(3, Math.max(1,
    parseInt(tparams.get('hops') ?? String(DEFAULT_TOPOLOGY_HOPS), 10) || DEFAULT_TOPOLOGY_HOPS));
  const errorsOnly = tparams.get('eonly') === '1';
  const setHops = (h: number) => setTparams(prev => {
    const next = new URLSearchParams(prev);
    // Karşılaştırma VARSAYILANLA yapılır, sabit 1'le değil. Eskiden
    // `h > 1` yazıyordu; varsayılan 2 olunca bu, kullanıcının 1 hop
    // seçimini URL'den siler ve okuma yine 2 döndürürdü — seçim
    // sessizce yutulurdu. Bu repoda üç kez tekrarlamış tek-yön-okuma
    // hata sınıfı (v0.8.256/265/267).
    if (h !== DEFAULT_TOPOLOGY_HOPS) next.set('hops', String(h)); else next.delete('hops');
    return next;
  }, { replace: true });
  const setErrorsOnly = (v: boolean) => setTparams(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('eonly', '1'); else next.delete('eonly');
    return next;
  }, { replace: true });
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
          onRecenter={(s) => navigate(serviceHref(s, { range, tab: 'topology' }))}
          onClear={() => navigate(`/topology?range=${rangeParam}`)}
        />
      </div>
    </div>
  );
}
