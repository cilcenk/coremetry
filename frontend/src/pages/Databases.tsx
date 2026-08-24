import { useEffect, useMemo } from 'react';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { Turtle } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { TableSkeleton } from '@/components/Skeleton';
import { Button } from '@/components/ui/Button';
import { DependenciesTable, type DepRow } from '@/components/DependenciesTable';
import { databaseDetailHref, legacyDatabaseRowTarget } from '@/pages/databases/databaseParam';
import { api } from '@/lib/api';
import { useUrlRange } from '@/lib/useUrlRange';
import { encodeRange } from '@/lib/urlState';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { timeRangeToNs } from '@/lib/utils';
import { stmtDetailHref } from '@/pages/slowqueries/stmtParam';
import { useStmtParamRedirect } from '@/pages/slowqueries/useStmtParamRedirect';
import type { SlowQueryRow } from '@/lib/types';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';

// v0.9.874 (tutarlılık denetimi MT12) — "En pahalı ifadeler" tablosu.
// Kardeşi /slow-queries çoktan primitifte; bu tablo hem sıralanamıyordu
// hem de tanımsız `.tbl` sınıfıyla, kapsız çiziliyordu.
const DB_STMT_COLS: DataTableColumn<SlowQueryRow>[] = [
  { id: 'statement', label: 'İfade',  sortValue: r => r.statement, naturalDir: 'asc', flex: true },
  { id: 'service',   label: 'Service', sortValue: r => r.service,  naturalDir: 'asc', width: 170 },
  { id: 'count',     label: 'Çağrı',  sortValue: r => r.count,     numeric: true, width: 95 },
  { id: 'p95',       label: 'P95',    sortValue: r => r.p95Ms,     numeric: true, width: 100 },
  { id: 'total',     label: 'Toplam', sortValue: r => r.totalMs,   numeric: true, width: 105 },
  { id: 'db',        label: 'DB',     sortValue: r => r.dbName || r.dbSystem, naturalDir: 'asc', width: 130 },
];
import type { DBInstance, DatabasesOverview } from '@/lib/types';
import { PageShell } from '@/components/ui/PageShell';

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
  // v0.9.840 — SATIR-ALTI ÇEKMECE EMEKLİ. Satır tıkı artık /database
  // TAM SAYFASINA gidiyor (operatör kararı 2026-08-09, /endpoint ile
  // aynı gerekçe): çekmece caller tablosunu, bekleme/kilit şeridini,
  // motor panelini ve ifade listesini 12px dolgulu TEK bir hücreye
  // sıkıştırıyordu.
  //
  // DEĞİŞİKLİK DAR: DependenciesTable PAYLAŞILAN bir bileşen ve
  // /messaging onu satır-altı çekmecesiyle kullanmaya DEVAM ediyor.
  // Emeklilik bir prop (onRowNavigate) — geçen tek tüketici bu sayfa.
  const navigate = useNavigate();
  // Eski ?row= derin linkleri (saved_views satırları, postmortem'lere
  // yapıştırılmış bağlantılar) yeni sayfaya iner. Şim değil: arkasında
  // eski yüzey yok ve bu paramı artık bu sayfa YAZMIYOR. Anahtarın
  // cluster alanı doluysa o satır /messaging'e ait — dokunulmaz.
  useEffect(() => {
    const target = legacyDatabaseRowTarget(sp.toString());
    if (target) navigate(target, { replace: true });
  }, [sp, navigate]);

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

  // v0.9.763 (mockup dilim 2) — EN PAHALI İFADELER sayfanın İÇİNDE:
  // "slow queries için ayrı sayfaya gidiyorum" akışı bitiyor. Mevcut
  // dbsys/dbname filtreleri kapsamı daraltır; satır tıkı URL-first
  // ifade çekmecesini (?stmt=, v0.8.378 D2) BURADA açar. Tam katalog
  // linki duruyor. limit=10: özet görünüm; devamı katalogda.
  const stmtsQ = useQuery({
    queryKey: ['db-top-statements', from, to, dbsys, dbname],
    queryFn: () => api.slowQueries({
      from, to,
      db_system: dbsys || undefined, db_name: dbname || undefined,
      limit: 10,
    }),
    staleTime: 30_000,
  });
  const stmtDt = useDataTable<SlowQueryRow>({
    storageKey: 'databases-top-statements', columns: DB_STMT_COLS,
    rows: stmtsQ.data ?? [], initialSort: { id: 'total', dir: 'desc' },
  });

  // Eski `?stmt=` linkleri (yer imi / paylaşılmış URL) sayfaya taşınır.
  useStmtParamRedirect(range);
  // v0.9.1374 — satır tıklaması ÇEKMECE değil SAYFA açıyor
  // (operatör bildirimi: "top statements çekmece açıyor bir satır
  // üzerine basınca"). Kimlik grameri aynı: stmtDetailHref pencereyi
  // de taşır, yoksa hedef kendi sticky penceresine düşer.
  const openStmt = (r: SlowQueryRow) => {
    const href = stmtDetailHref({ hash: r.stmtHash, system: r.dbSystem }, range);
    if (href) navigate(href);
  };
  // v0.9.821 — GLOBAL ENV FİLTRESİ. Bu sayfa Topbar'ın env seçimini
  // SESSİZCE YOK SAYIYORDU: operatör env=uat seçince /endpoints ve
  // /services daralıyor, /databases ise tüm ortamların veritabanlarını
  // göstermeye devam ediyordu ve hiçbir şey bunu söylemiyordu. Sessizce
  // filtrelenmemiş bir liste, filtrelenmiş gibi okunur.
  //
  // Ayrı bir sayfa-yerel select AÇILMADI: env tek bir kavram ve tek bir
  // doğruluk kaynağı olmalı (Topbar). İki seçici, iki gerçek demekti.
  const [env] = useUrlEnv();
  const q = useQuery({
    queryKey: ['databases', from, to, compare, env],
    queryFn: () => api.databases(from, to, compare ? 'prior' : undefined, env || undefined),
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  });
  const ov = q.data as DatabasesOverview | null | undefined;

  // Split rows by origin. Span-derived rows go to the top
  // panel; receiver-discovered rows go to the bottom. Either
  // panel can be empty — we render the heading + an empty
  // state so the operator sees that we did look.
  const { spanRows, receiverRows, systems, dbNames } = useMemo(() => {
    const all = (ov?.rows ?? []) as DBInstance[];
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
      // v0.9.821 — db.name filtresi RECEIVER satırlarına UYGULANMAZ.
      // Receiver satırları metric_points'ten geliyor ve db.name TAŞIMAZ
      // (kimlikleri instance seviyesinde), yani her db.name seçimi
      // receiver panelini sessizce boşaltıyor ve "bu filtreye uyan
      // receiver yok" gibi okunuyordu — oysa doğru cümle "receiver
      // satırları bu boyutu hiç taşımaz". Panel artık FİLTRELENMEZ,
      // GİZLENİR ve sebebi yazılır (aşağıdaki render).
      if (dbname && d.source !== 'receiver' && d.dbName !== dbname) continue;
      if (d.source === 'receiver') recv.push(d);
      else span.push(d);
    }
    return {
      spanRows: span, receiverRows: recv,
      systems: [...sysSet].sort(), dbNames: [...nameSet].sort(),
    };
  }, [ov, dbsys, dbname]);

  // openDatabasePage — satır tıkının ve klavye Enter/o'sunun TEK
  // hedefi. Kimlik ÜÇLÜ (system, instance, dbName) ve ETİKETTEN
  // bağımsız (v0.9.821): nameOf() instance 'unknown' iken db.name'i
  // etiket olarak basıyor, onu kimlik sanmak boş bir sayfa demekti.
  const openDatabasePage = (r: DepRow) => navigate(databaseDetailHref({
    system: r.system,
    instance: r.instance ?? '',
    dbName: r.dbName ?? '',
    source: r.source === 'receiver' ? 'receiver' : 'spans',
  }, { range: encodeRange(range), env: env || undefined }));

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
      <Topbar title="Databases" range={range} onRangeChange={setRange} envApplies />
      <PageShell>
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
        {/* v0.9.834 — v0.9.820'nin KPI şeridi + üç grafiği ve v0.9.822'nin
            doygunluk karosu KALDIRILDI (operatör: bu metrikler gereksiz).
            Doygunluk VERİSİ kaybolmadı: receiver satırının çekmecesinde
            (oturumlar, wait class'ları, tablespace) yaşamaya devam
            ediyor — giden yalnız karo ve karodan çekmeceye giden tık
            kısayolu. /api/databases/series ile /api/databases/saturation
            uçları ve okuyucuları da silindi. */}
        {/* v0.9.821 — DÜRÜSTLÜK ŞERİTLERİ. Üçü de yalnız gerçekten
            geçerliyken çıkar; her sayfada duran bir uyarı, hiçbir sayfada
            okunmayan bir uyarıdır. */}
        {ov?.source === 'raw' && (
          <HonestyStrip tone="var(--accent)">
            Kaynak: <b>ham spans</b> — <code>env={env}</code> filtresi
            db_summary_5m rollup&#39;ında olmayan bir boyut, okuma ham
            span&#39;lere düşüyor. Sayılar rollup yolununkiyle birebir aynı
            OLMAYABİLİR (quantile&#39;lar burada ham span&#39;lerden hesaplanıyor)
            ve bu okuma prod ölçeğinde belirgin biçimde daha pahalı.
            {ov?.receiversSkipped === 'env' && <> Receiver paneli bu modda
            {' '}<b>doldurulmuyor</b>: metric_points <code>deploy_env</code>
            {' '}taşımıyor, yani receiver satırları env&#39;e göre daraltılamaz
            ve daraltılmamış satırları daraltılmış bir listenin yanına
            koymak aynı yalan olurdu.</>}
          </HonestyStrip>
        )}
        {ov?.rowsCapped && (
          <HonestyStrip tone="var(--warn)">
            Satır okuması <b>{ov.rowLimit?.toLocaleString()}</b> tavanına
            dayandı — liste EKSİK olabilir. Tip / db.name filtresiyle
            daraltın; aşağıdaki panel sayıları yalnız dönen satırları
            anlatır.
          </HonestyStrip>
        )}
        {ov?.receiversCapped && (
          <HonestyStrip tone="var(--warn)">
            Receiver keşfi motor başına{' '}
            <b>{ov.receiverLimit?.toLocaleString()}</b> instance tavanına
            dayandı — en az veri yayanlar listeden düşmüş olabilir
            (sıralama datapoint hacmine göre, alfabetik değil).
          </HonestyStrip>
        )}
        {q.isPending && <TableSkeleton rows={8} cols={11} wideFirst />}
        {q.isError && (
          <div style={{ color: 'var(--err)', fontSize: 12 }}>
            Failed to load databases overview.
          </div>
        )}
        {ov && (
          <>
            <SectionHeader
              title={`Called from services (${spanRows.length})`}
              subtitle={`Derived from spans with a populated `}
              code="db.system"
              tail=" attribute. Satır tıkı veritabanı detay SAYFASINI açar." />
            {spanRows.length === 0 ? (
              <EmptyHint>
                {dbsys || dbname
                  ? 'No service-called databases match the current filter.'
                  : 'No service-emitted database spans in this window. Wire an OTel SDK into one of the application services to see this section populate.'}
              </EmptyHint>
            ) : (
              <DependenciesTable rows={spanRows.map(toRow)} kind="db" range={range} onRowNavigate={openDatabasePage} />
            )}

            <div style={{ height: 24 }} />

            {/* v0.9.821 — db.name filtresi altında bu panel GİZLENİR.
                Receiver satırları metric_points'ten geliyor ve db.name
                TAŞIMAZ; filtre uygulandığında panel sessizce boşalıyor ve
                "bu filtreye uyan receiver yok" diye okunuyordu. Doğru
                cümle "receiver satırları bu boyutu hiç taşımaz" ve bunu
                boş bir tablo söyleyemez. */}
            {dbname ? (
              <>
                <SectionHeader
                  title="DB receiver instances (gizli)"
                  subtitle="Receiver satırları "
                  code="db.name"
                  tail=" taşımaz — kimlikleri instance seviyesindedir (metric_points'te böyle bir boyut yok)." />
                <EmptyHint>
                  <b>db.name = {dbname}</b> filtresi açıkken bu panel gizleniyor.
                  Filtreyi uygulasaydık panel boşalır ve &quot;bu veritabanına ait
                  receiver yok&quot; gibi okunurdu; oysa receiver satırları o
                  boyutu hiç taşımıyor. Filtreyi temizleyin ({receiverRows.length}
                  {' '}instance bekliyor).
                </EmptyHint>
              </>
            ) : (
              <>
                <SectionHeader
                  title={`DB receiver instances (${receiverRows.length})`}
                  subtitle="OpenTelemetry database-receiver instances — discovered from "
                  code="oracledb.* / postgresql.* / mysql.* / redis.*"
                  tail=" metric_points. Satır tıkı detay sayfasını açar; motor paneli (oturumlar, wait class'ları, buffer pool, keyspace'ler…) orada." />
                {receiverRows.length === 0 ? (
                  <EmptyHint>
                    {ov?.receiversSkipped === 'env'
                      ? `env=${env} filtresi açıkken receiver keşfi hiç çalışmıyor (metric_points deploy_env taşımıyor) — bu panel "boş" değil, SORULMADI.`
                      : dbsys
                        ? 'No receiver instances match the current filter.'
                        : 'No receiver-detected instances in this window. Point an OpenTelemetry database receiver (oracledb / postgresql / mysql / redis) at one of your databases and the discovered instance will appear here.'}
                  </EmptyHint>
                ) : (
                  <DependenciesTable rows={receiverRows.map(toRow)} kind="db" range={range} onRowNavigate={openDatabasePage} />
                )}
              </>
            )}

            {/* v0.9.763 (mockup dilim 2) — EN PAHALI İFADELER sayfanın
                içinde; dbsys/dbname filtreleri kapsamı daraltır, satır
                tıkı ifade çekmecesini burada açar. Tam katalog linki
                üstte duruyor. */}
            <div style={{ marginTop: 18 }}>
              <SectionHeader
                title={`En pahalı ifadeler${dbname ? ` · ${dbname}` : dbsys ? ` · ${dbsys}` : ''} (ilk 10)`}
                subtitle="Toplam süreye göre; satır tıkı ifade detayını açar ("
                code="?stmt="
                tail=" derin-linklenebilir). Tam katalog: Slow queries →" />
              {stmtsQ.isPending && <TableSkeleton rows={5} cols={6} wideFirst />}
              {stmtsQ.data && stmtsQ.data.length === 0 && (
                <EmptyHint>Bu pencerede eşleşen ifade yok.</EmptyHint>
              )}
              {(stmtsQ.data ?? []).length > 0 && (
                // v0.9.874 (tutarlılık denetimi MT12) — ÖNCE KAP, SONRA
                // PRİMİTİF. Bu tablo `.tbl` sınıfıyla çiziliyordu; o sınıf
                // globals.css'te TANIMSIZ (ölü sınıf, okuyanı yanıltıyordu)
                // ve `.table-wrap` kabı da YOKTU. Sabit düzen + kap yokluğu
                // = dar ekranda sayfanın yatay taşması (v0.9.640 sızıntısı).
                <div className="table-wrap">
                  <table style={{ width: '100%', fontSize: 12, tableLayout: 'fixed' }}>
                    <DataTableColgroup dt={stmtDt} />
                    <DataTableHead dt={stmtDt} />
                  <tbody>
                    {stmtDt.sortedRows.map((r, i) => (
                      <tr key={r.stmtHash ?? i}
                        onClick={() => openStmt(r)}
                        title={r.sampleStatement || r.statement}
                        style={{ cursor: r.stmtHash ? 'pointer' : 'default' }}>
                        <td className="mono" style={{ maxWidth: 480, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                          {r.statement}
                        </td>
                        <td>{r.service}</td>
                        <td className="num mono">{r.count}</td>
                        <td className="num mono">{r.p95Ms.toFixed(1)} ms</td>
                        <td className="num mono"><b>{(r.totalMs / 1000).toFixed(1)} s</b></td>
                        {/* v0.9.821 — ÇOK-DB BELİRSİZLİK İŞARETİ geri
                            geldi. SlowQueries sayfası bunu v0.9.272'den
                            beri çiziyordu ama bu KARDEŞ tablo yalnız adı
                            basıyordu: aynı ifadenin pencerede birden çok
                            veritabanına gittiği durumda burada tek bir ad
                            görünüyor ve o veritabanına aitmiş gibi
                            okunuyordu. Gruplama db_name'i katlıyor, yani
                            gösterilen ad onlardan BİRİ. */}
                        <td className="num">
                          {r.dbName
                            ? <>
                                {r.dbName}
                                {r.dbNameCount > 1 && (
                                  <span style={{ color: 'var(--text3)', marginLeft: 4 }}
                                    title={`Bu ifade pencerede ${r.dbNameCount} farklı veritabanına gitti; gösterilen onlardan biri.`}>
                                    +{r.dbNameCount - 1}
                                  </span>
                                )}
                              </>
                            : r.dbSystem}
                        </td>
                      </tr>
                    ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          </>
        )}
      </PageShell>
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

// HonestyStrip — kaynak / tavan şeritlerinin tek görsel dili
// (v0.9.821). Endpoints'in havuz şeridiyle aynı anatomi: sol kenarda
// renkli çubuk, yumuşak zemin, tek paragraf.
function HonestyStrip({ tone, children }: { tone: string; children: React.ReactNode }) {
  return (
    <div style={{
      padding: '8px 12px', marginBottom: 10, fontSize: 11.5,
      border: '1px solid var(--border)', borderLeft: `3px solid ${tone}`,
      borderRadius: 6, background: 'var(--bg2)', color: 'var(--text2)',
      lineHeight: 1.5,
    }}>{children}</div>
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
