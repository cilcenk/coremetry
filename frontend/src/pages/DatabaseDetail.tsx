import { useMemo } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { navHref } from '@/lib/navHref';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { api } from '@/lib/api';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { usePageZoomRange } from '@/lib/chart/usePageZoomRange';
import { timeRangeToNs } from '@/lib/utils';
import { stmtDetailHref } from '@/pages/slowqueries/stmtParam';
import { useStmtParamRedirect } from '@/pages/slowqueries/useStmtParamRedirect';
import { parseDatabasePageRef } from '@/pages/databases/databaseParam';
import {
  DatabaseIdentityHeader, DatabaseSignalStrip, DatabaseTrendCards,
  DatabaseCallersSection, DatabaseStatementsSection, DatabaseEnginePanels,
} from '@/pages/databases/detailSections';
import type { DBDetail, DBTrend, SlowQueryRow } from '@/lib/types';
import { PageShell } from '@/components/ui/PageShell';

// /database — the full-page database detail (v0.9.840).
//
// WHY A PAGE. The /databases row used to expand into an inline drawer
// squeezed under the table, which is where the caller table, the
// wait/lock strip, the engine panel AND the statement list all had to
// share one 12px-padded cell. Operator's call, same as /endpoint the
// version before: the row click navigates.
//
// NO NEW BACKEND. Every read here already existed behind the drawer:
// /api/databases/detail (the v0.9.821 identity TRIPLE), /trends, the
// four engine panels and /slow-queries. The trends read is the
// page-wide one the table also issues — same params, so React Query
// serves this page from the same cache entry rather than adding a
// per-row parameter to a shared endpoint.
//
// NO WAITS & LOCKS (v0.9.846, operator's call; v0.9.852'de TAMAMEN
// SÖKÜLDÜ). The wait/lock strip read /api/databases/waitlock, which is
// fed by the OpenTelemetry database receivers' engine metric families —
// and this install does not ingest them. The panel therefore had exactly
// two states, both content-free: an empty strip, or a paragraph
// explaining that the engine has no such metric family. A cell that can
// only ever explain its own emptiness is worth less than the table that
// took its place. v0.9.846'da bileşen, "DetailDrawer hâlâ mount ediyor"
// gerekçesiyle bırakılmıştı; o dal ÇALIŞMA ZAMANINDA ERİŞİLEMEZDİ
// (/databases artık onRowNavigate veriyor, satır tıklaması çekmece
// açmıyor; /messaging ise kind="queue"). v0.9.852: uç + chstore okuyucusu
// + tip + istemci metodu + bileşen gitti; geri dönüş git geçmişinden.
//
// IDENTITY (v0.9.821, the trap): a row is (system, instance, dbName),
// never the printed label. When dbName is empty the page says "every
// database on this instance" out loud, because the drawer's hardest-won
// lesson was that a silent scope reads as a narrow one.
//
// KABUK / GÖVDE AYRIMI (v0.9.1366). Bu dosya artık yalnız KABUK: veriyi
// çeker, `?stmt=` açılış parametresini sahiplenir, bölümleri dizer.
// Gövde `pages/databases/detailSections.tsx`te — `pages/endpoints/`
// ikizinin kardeşi. Ayrımın gerekçesi ve "çekmece + sayfa aynı anda"
// kararı o dosyanın başlığında yazılı; özeti: db tarafında çekmece
// v0.9.840'ta emekli oldu (DetailDrawer'ın `kind='db'` dalı çalışma
// zamanında erişilemez — HEAD'de yeniden ölçüldü), o yüzden ikisi aynı
// anda AÇILAMAZ; bölümler yine de kabuk hakkında hiçbir şey bilmiyor ki
// gerekirse iki ayrı kabuğa sarılabilsinler.
export default function DatabaseDetailPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const search = params.toString();
  const refObj = useMemo(() => parseDatabasePageRef(search), [search]);
  const [env] = useUrlEnv();
  const { range, setRange } = usePageZoomRange('1h');
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);

  const system = refObj?.system ?? '';
  const instance = refObj?.instance ?? '';
  const dbName = refObj?.dbName ?? '';

  const detailQ = useQuery({
    queryKey: ['database-detail', system, instance, dbName, from, to],
    queryFn: () => api.databaseDetail(system, instance, dbName, from, to),
    enabled: !!refObj,
    staleTime: 30_000,
  });

  // The SAME key /databases uses, on purpose: no new parameter on a
  // shared endpoint, and the page inherits the table's warm cache.
  const trendsQ = useQuery({
    queryKey: ['db-trends', from, to],
    queryFn: () => api.dbTrends(from, to),
    enabled: !!refObj,
    staleTime: 30_000,
  });
  const trend: DBTrend | undefined = useMemo(() => {
    if (!refObj) return undefined;
    return (trendsQ.data ?? []).find(t =>
      t.dbSystem === refObj.system
      && t.instance === refObj.instance
      && (t.dbName ?? '') === refObj.dbName);
  }, [trendsQ.data, refObj]);

  const stmtsQ = useQuery({
    queryKey: ['database-statements', system, dbName, from, to],
    queryFn: () => api.slowQueries({
      from, to,
      db_system: system || undefined,
      db_name: dbName || undefined,
      limit: 10,
    }),
    enabled: !!refObj,
    staleTime: 30_000,
  });

  // Eski `?stmt=` linkleri (yer imi / paylaşılmış URL) sayfaya taşınır.
  useStmtParamRedirect(range);
  // v0.9.1374 — çekmece emekli, satır tıklaması sayfaya gidiyor.
  const openStmt = (r: SlowQueryRow) => {
    const href = stmtDetailHref({ hash: r.stmtHash, system: r.dbSystem }, range);
    if (href) navigate(href);
  };

  if (!refObj) {
    return (
      <>
        <Topbar title="Database" />
        <PageShell>
          <Empty icon="⚠" title="No database in this link">
            The URL is missing <code>system</code> or <code>instance</code>.
            {' '}<Link to={navHref('/databases', search)}>Back to Databases →</Link>
          </Empty>
        </PageShell>
      </>
    );
  }

  const d: DBDetail | null | undefined =
    detailQ.isPending ? undefined : detailQ.isError ? null : detailQ.data;

  return (
    <>
      <Topbar title="Database" range={range} onRangeChange={setRange} />
      <PageShell>
        <div style={{ fontSize: 11.5, color: 'var(--text3)', marginBottom: 8 }}>
          {/* v0.9.1320 — geri/kırıntı linkleri pencereyi ve env'i TAŞIR.
              Bare bir `/databases` linki, olay penceresiyle bu sayfaya
              inen operatörü sticky pencereye düşürüyordu; bir geri
              linkinin bağlamı değiştirmesi, geri linki olmaktan çıkması
              demek (Pod.tsx:206-209, v0.9.965). */}
          <Link to={navHref('/databases', search)}>Databases</Link> › database detail
        </div>

        <DatabaseIdentityHeader refObj={refObj} env={env} range={range} />

        {d === undefined && <Spinner />}
        {d === null && (
          <Empty icon="⚠" title="Detail query failed">
            The /api/databases/detail request errored.
            {' '}<Link to={navHref('/databases', search)}>Back to the overview →</Link>
          </Empty>
        )}

        {d && (
          <>
            <DatabaseSignalStrip d={d} />

            {/* Three series, from the SAME /api/databases/trends payload
                the overview grid uses — filtered client-side by the
                identity triple. No per-row parameter added to a shared,
                cached endpoint. */}
            <DatabaseTrendCards trend={trend} pending={trendsQ.isPending} xRange={xRange} />

            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(340px, 1fr))',
              gap: 12,
            }}>
              <DatabaseCallersSection callers={d.callers ?? []} range={range} env={env} />

              {/* TOP STATEMENTS — v0.9.846 moved up into the grid, taking
                  the cell "Waits & locks" used to hold. The operator does
                  not ingest DB engine metrics, so the wait/lock strip had
                  no data to draw and its whole cell was a permanent
                  explanation of why it was empty. The statement list is
                  the answer the page is actually asked for, so it moves
                  next to "Who calls this": who hits this DB, and with
                  what. The ?stmt= click-through is unchanged.

                  The Service column is dropped here and NOT lost: in a
                  340px-min cell it would have starved the statement text,
                  which is the column being read. It rides in the row's
                  title alongside the sample statement. */}
              <DatabaseStatementsSection
                refObj={refObj}
                rows={stmtsQ.data}
                pending={stmtsQ.isPending}
                error={stmtsQ.isError}
                errorMessage={stmtsQ.error instanceof Error ? stmtsQ.error.message : undefined}
                onRetry={() => { void stmtsQ.refetch(); }}
                onOpen={openStmt} />
            </div>

            <DatabaseEnginePanels refObj={refObj} range={range} />
          </>
        )}

      </PageShell>
    </>
  );
}
