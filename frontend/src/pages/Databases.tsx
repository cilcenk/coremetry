import { useMemo } from 'react';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Turtle } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { TableSkeleton } from '@/components/Skeleton';
import { Button } from '@/components/ui/Button';
import { DependenciesTable } from '@/components/DependenciesTable';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs } from '@/lib/utils';
import type { DBInstance } from '@/lib/types';

// /databases — two distinct panels driven by data origin:
//
//   Panel 1: "Called from services" — Dynatrace-style overview
//     of every (db_system, instance) the platform's services
//     have called. Rows derived from spans with a populated
//     db.system attribute. This is the "what depends on what"
//     view for application-side SREs.
//
//   Panel 2: "DB receiver instances" — every database
//     instance discovered via an OpenTelemetry database
//     receiver (oracledb / postgresql / mysql / redis)
//     regardless of whether the application traced it. The
//     DBA-team view: surface every monitored DB even when
//     no app-side SDK is yet in place.
//
// Splitting them prevents the two data origins (span-driven
// vs receiver-driven) from colliding in one list; each
// audience scans the panel that matches their question.
export default function DatabasesPage() {
  const [range, setRange] = useUrlRange('1h');
  // v0.9.86 (operatör talebi) — db tipi (?dbsys=) + db.name (?dbname=)
  // filtreleri. URL source-of-truth (replace:true, yabancı paramlar
  // korunur); seçenekler zaten çekilmiş satırlardan türetilir — ekstra
  // sorgu/katalog fetch'i YOK (satır sayısı sınırlı, client-side yeterli).
  const [sp, setSp] = useSearchParams();
  // v0.9.399 (desen paritesi) — satır drawer'ı URL-first (?row=,
  // controlled mod DependenciesTable'da zaten hazırdı): kopyalanan
  // link artık drawer'ı da açıyor. İki tablo aynı param'ı paylaşır
  // (anahtar system|cluster|name — çakışma yok).
  const [dbRowParams, setDbRowParams] = useSearchParams();
  const openRow = dbRowParams.get('row');
  const setOpenRow = (row: { system: string; cluster?: string; name?: string; dbName?: string } | null) => setDbRowParams(prev => {
    const next = new URLSearchParams(prev);
    // DependenciesTable'ın iç anahtar şekli: system|cluster|name
    const k = row ? `${row.system}|${row.cluster ?? ''}|${row.name ?? row.dbName ?? ''}` : null;
    if (k) next.set('row', k); else next.delete('row');
    return next;
  }, { replace: true });

  const dbsys = sp.get('dbsys') ?? '';
  const dbname = sp.get('dbname') ?? '';
  // v0.9.433 (desen paritesi, kuyruk #3a) — compare URL'de
  // (?compare=prior, Messaging v0.9.399 birebiri): copy-link ve saved
  // view compare durumunu da taşır. Opt-in — backend maliyeti ikiye katlar.
  const compare = sp.get('compare') === 'prior';
  const setCompare = (v: boolean) => setSp(prev => {
    const next = new URLSearchParams(prev);
    if (v) next.set('compare', 'prior'); else next.delete('compare');
    return next;
  }, { replace: true });
  const setFilter = (key: 'dbsys' | 'dbname', value: string) => {
    setSp(prev => {
      const next = new URLSearchParams(prev);
      if (value) next.set(key, value); else next.delete(key);
      // Tip değişince önceki tipe ait db.name seçimi anlamsız kalır.
      if (key === 'dbsys') next.delete('dbname');
      return next;
    }, { replace: true });
  };
  // Memoize on range identity — without this, a relative range
  // resolved fresh every render reshuffles the useQuery key
  // and the table refetches on every paint.
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery({
    queryKey: ['databases', from, to, compare],
    queryFn: () => api.databases(from, to, compare ? 'prior' : undefined).then(r => r ?? []),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });

  // Split rows by origin. Span-derived rows go to the top
  // panel; receiver-discovered rows go to the bottom. Either
  // panel can be empty — we render the heading + an empty
  // state so the operator sees that we did look.
  const { spanRows, receiverRows, systems, dbNames } = useMemo(() => {
    const all = (q.data ?? []) as DBInstance[];
    const sysSet = new Set<string>();
    const nameSet = new Set<string>();
    for (const d of all) {
      if (d.system) sysSet.add(d.system);
      // db.name seçenekleri seçili tipe göre daralır (bağımlı liste).
      if (d.dbName && (!dbsys || d.system === dbsys)) nameSet.add(d.dbName);
    }
    const span: DBInstance[] = [];
    const recv: DBInstance[] = [];
    for (const d of all) {
      if (dbsys && d.system !== dbsys) continue;
      if (dbname && d.dbName !== dbname) continue;
      if (d.source === 'receiver') recv.push(d);
      else span.push(d);
    }
    return {
      spanRows: span, receiverRows: recv,
      systems: [...sysSet].sort(), dbNames: [...nameSet].sort(),
    };
  }, [q.data, dbsys, dbname]);

  const toRow = (d: DBInstance) => ({
    system: d.system,
    instance: d.instance,
    dbName: d.dbName,
    spanCount: d.spanCount,
    errorCount: d.errorCount,
    errorRate: d.errorRate,
    avgDurationMs: d.avgDurationMs,
    // toRow copies EXPLICITLY — a field missing here never reaches the table,
    // however well-populated the payload is (v0.9.259's dead-P95 lesson on
    // /messaging was exactly this).
    p50DurationMs: d.p50DurationMs,
    p95DurationMs: d.p95DurationMs,
    p99DurationMs: d.p99DurationMs,
    // v0.9.433 — prior ikizi eşleşen satırların delta rozetleri
    // (DepRow bunları zaten biliyor; Messaging sözleşmesinin aynısı:
    // prior yoksa alanlar undefined kalır, rozet gizli).
    priorSpanCount: d.priorSpanCount,
    priorErrorCount: d.priorSpanCount !== undefined ? (d.priorErrorCount ?? 0) : undefined,
    priorAvgMs: d.priorAvgMs,
    priorP50Ms: d.priorP50Ms,
    priorP99Ms: d.priorP99Ms,
    callers: d.callers ?? [],
    source: d.source,
  });

  return (
    <>
      <Topbar title="Databases" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 8, marginBottom: 12, alignItems: 'center' }}>
          <select value={dbsys} onChange={e => setFilter('dbsys', e.target.value)}
            style={{ fontSize: 12, padding: '3px 8px' }}
            title="db.system'e göre filtrele">
            <option value="">All types</option>
            {systems.map(x => <option key={x} value={x}>{x}</option>)}
          </select>
          <select value={dbname} onChange={e => setFilter('dbname', e.target.value)}
            style={{ fontSize: 12, padding: '3px 8px' }}
            title="db.name'e göre filtrele">
            <option value="">All db names</option>
            {dbNames.map(x => <option key={x} value={x}>{x}</option>)}
          </select>
          {(dbsys || dbname) && (
            <Button variant="secondary" size="sm"
              onClick={() => setSp(prev => {
                const next = new URLSearchParams(prev);
                next.delete('dbsys'); next.delete('dbname');
                return next;
              }, { replace: true })}>Clear</Button>
          )}
          {/* v0.9.433 — Messaging'in compare toggle'ının birebiri. */}
          <label style={{ fontSize: 11, display: 'flex', alignItems: 'center', gap: 4, cursor: 'pointer' }}
            title="Compare current window against the immediately-preceding equal-length window. Adds a second backend scan; off by default.">
            <input type="checkbox" checked={compare}
              onChange={e => setCompare(e.target.checked)} />
            Compare vs prior
          </label>
          <Link to="/databases/slow-queries" className="sec"
            style={{
              fontSize: 12, padding: '5px 12px', borderRadius: 6,
              border: '1px solid var(--border)', background: 'var(--bg3)',
              color: 'var(--accent2)', textDecoration: 'none',
              display: 'inline-flex', alignItems: 'center', gap: 6,
            }}
            title="Cross-service slow-query catalog — what's burning the most DB time globally">
            <Turtle size={14} strokeWidth={1.75} /> Slow queries →
          </Link>
        </div>
        {/* v0.9.405 (desen paritesi) — Spinner yerine tablo iskeleti:
            gerçek tablo ~11 kolon; iskelet aynı kalıpta gelince veri
            indiğinde düzen zıplamıyor (Endpoints deseni). */}
        {/* v0.9.405 — URL-state taşıyan sayfa görünüm kaydedebilmeli
            (saved_views şeması hazır; Endpoints emsali). */}
        <SavedViewsBar page="databases" />
        {q.isPending && <TableSkeleton rows={8} cols={11} wideFirst />}
        {q.isError && (
          <div style={{ color: 'var(--err)', fontSize: 12 }}>
            Failed to load databases overview.
          </div>
        )}
        {q.data && (
          <>
            <SectionHeader
              title={`Called from services (${spanRows.length})`}
              subtitle={`Derived from spans with a populated `}
              code="db.system"
              tail=" attribute. Click a row to drill into matching traces." />
            {spanRows.length === 0 ? (
              <EmptyHint>
                {dbsys || dbname
                  ? 'No service-called databases match the current filter.'
                  : 'No service-emitted database spans in this window. Wire an OTel SDK into one of the application services to see this section populate.'}
              </EmptyHint>
            ) : (
              <DependenciesTable rows={spanRows.map(toRow)} kind="db" range={range} openRowKey={openRow} onOpenRowChange={setOpenRow} />
            )}

            <div style={{ height: 24 }} />

            <SectionHeader
              title={`DB receiver instances (${receiverRows.length})`}
              subtitle="OpenTelemetry database-receiver instances — discovered from "
              code="oracledb.* / postgresql.* / mysql.* / redis.*"
              tail=" metric_points. Expand a row to see receiver-specific drill-downs (sessions, wait classes, buffer pool, keyspaces…)." />
            {receiverRows.length === 0 ? (
              <EmptyHint>
                {dbsys || dbname
                  ? 'No receiver instances match the current filter.'
                  : 'No receiver-detected instances in this window. Point an OpenTelemetry database receiver (oracledb / postgresql / mysql / redis) at one of your databases and the discovered instance will appear here.'}
              </EmptyHint>
            ) : (
              <DependenciesTable rows={receiverRows.map(toRow)} kind="db" range={range} openRowKey={openRow} onOpenRowChange={setOpenRow} />
            )}
          </>
        )}
      </div>
    </>
  );
}

function SectionHeader({ title, subtitle, code, tail }: {
  title: string;
  subtitle: string;
  code: string;
  tail: string;
}) {
  return (
    <>
      <div style={{
        fontSize: 13, fontWeight: 700, marginBottom: 4,
        color: 'var(--text)',
      }}>{title}</div>
      <div style={{ marginBottom: 12, fontSize: 12, color: 'var(--text2)' }}>
        {subtitle}<code>{code}</code>{tail}
      </div>
    </>
  );
}

function EmptyHint({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      padding: 14, borderRadius: 6, marginBottom: 8,
      background: 'var(--bg2)', border: '1px dashed var(--border)',
      fontSize: 12, color: 'var(--text3)',
    }}>{children}</div>
  );
}
