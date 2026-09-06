import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { rowActivation } from '@/lib/a11y'; // v0.10.451 (dış denetim D3 kalan)
import { Link, useLocation, useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { TriageCrumb } from '@/components/TriageCrumb';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { useAuth } from '@/components/AuthProvider';
import { ChevronRight, ChevronDown } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { PriorityBadge } from '@/components/ui/PriorityBadge';
import { Pager } from '@/components/Pager';
import { useServicesMetadata, keys } from '@/lib/queries';
import { useQueryClient } from '@tanstack/react-query';
import { api, type UserRow } from '@/lib/api';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { fmtNum, tsLong } from '@/lib/utils';
import { teamOptionsCI } from '@/lib/teamOptions';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/ui/DataTable';
import type { DataTableColumn } from '@/lib/dataTable';
import type {
  ExceptionGroup, ExceptionGroupState, ExceptionSample,
} from '@/lib/types';
import { SearchField } from '@/components/ui/SearchField';
import { ProblemDetail } from './ProblemDetail';
import { withProblemParam, withExcParam, excDetailHref } from './problemLink';
import { emptySamplesNote, type SampleScanEnvelope } from './exceptionSamples';
import { PageControls } from '@/components/ui/PageControls';
// v0.9.837 — the "Alert rules" section moved to /inbox ("Problems").
// The ?problem= deep-link host stays HERE too: notification e-mails
// point at /problems?problem=<id> and that contract is test-locked.
import { AlertProblemHost } from './ProblemsSection';
import { serviceHref } from '@/lib/serviceHref';
import { traceHref } from '@/lib/traceHref';
import { QueryError, QueryErrorInline } from '@/components/QueryError';
import { PageShell } from '@/components/ui/PageShell';
import { stripMarkdown } from '@/components/Markdown';
import { IconSparkles } from '@/components/icons';

// State buckets shown as tabs along the top of the page.
const TABS: { key: string; label: string; hint: string }[] = [
  { key: 'open',         label: 'Inbox',        hint: 'New + acknowledged + regressed' },
  { key: 'new',          label: 'Open',         hint: 'Untouched since first occurrence' }, // v0.8.382: NEW is the first-seen badge
  { key: 'acknowledged', label: 'Acknowledged', hint: 'Someone is on it' },
  { key: 'regressed',    label: 'Regressed',    hint: 'Resolved but happening again' },
  { key: 'resolved',     label: 'Resolved',     hint: 'Closed out' },
  { key: 'ignored',      label: 'Ignored',      hint: 'Permanently silenced' },
];

// Exception-inbox sort keys the SERVER understands (v0.8.318 — the
// ORDER BY runs in ClickHouse across the whole paginated set). The
// DataTable column ids below are exactly these values, so dt.sort.id
// forwards straight to the fetch after a sanitize.
type SortKey = 'state' | 'type' | 'service' | 'occurrences' | 'firstSeen' | 'lastSeen' | 'assignee';
const SORT_KEYS: readonly SortKey[] =
  ['state', 'type', 'service', 'occurrences', 'firstSeen', 'lastSeen', 'assignee'];
const DEFAULT_EXC_SORT = { id: 'lastSeen' as SortKey, dir: 'desc' as const };

// Exception inbox columns — shared DataTable primitive in serverSort
// mode (Services template, v0.8.251): the hook owns the sort UX, the
// `s_exception-inbox` URL param and the persisted widths; the backend
// owns the ordering, so `sortValue` is never invoked here (it is the
// sortable marker + naturalDir carrier). State's severity-style
// ordering (worst at top) stays server-side — see
// exceptionGroupsOrderBy's multiIf.
const EXC_COLS: DataTableColumn<ExceptionGroup>[] = [
  { id: 'state',       label: 'State',       sortValue: g => g.state,       naturalDir: 'desc', width: 132 },
  { id: 'type',        label: 'Exception',   sortValue: g => g.type,        naturalDir: 'asc', flex: true },
  { id: 'service',     label: 'Service',     sortValue: g => g.service,     naturalDir: 'asc',  width: 150 },
  { id: 'occurrences', label: 'Occurrences', sortValue: g => g.occurrences, numeric: true,      width: 100 },
  { id: 'firstSeen',   label: 'First seen',  sortValue: g => g.firstSeen,   width: 124 },
  { id: 'lastSeen',    label: 'Last seen',   sortValue: g => g.lastSeen,    width: 124 },
  { id: 'assignee',    label: 'Assignee',    sortValue: g => g.assignee,    naturalDir: 'asc',  width: 120 },
];

export default function ProblemsPage() {
  const { user } = useAuth();
  const isAdmin = user?.role === 'admin' || user?.role === 'editor';
  // URL is the source of truth for tab + service + page so a
  // pasted link reproduces the exact triage view. ?service= was
  // already URL-driven (driven by the service-detail "Errors"
  // pill); v0.5.98 adds ?tab=…&page=N so the inbox is fully
  // shareable. Filter changes always reset page back to 0 so a
  // teammate's link can't land them on "page 4 of 2".
  const [searchParams, setSearchParams] = useSearchParams();
  const location = useLocation(); // v0.10.221 — exception satır linkleri
  const tab     = searchParams.get('tab') || 'open';
  const service = searchParams.get('service') || '';
  // Owner (ug-team) / SRE (sy-team) team filter — URL-backed so a
  // triage link reproduces the exact team slice, mirroring /inbox's
  // ?owner=/?sre= (v0.8.310). Resolved server-side to member services
  // so the narrowing is correct across the whole paginated set.
  // v0.9.315 (operatör) — occurrence eşiği. Prod'da liste tek seferlik
  // exception'larla doluyordu: bir kez düşen Java socket timeout'u,
  // sürmekte olan bir kesintiyle aynı görünen bir satır üretiyordu.
  // Operatör: "gerçek sorun 5-10 adetten fazla occurrence olan
  // problemler". Varsayılan 5; ?minOcc= ile URL'de, yani kaydedilen
  // görünüm ve paylaşılan link eşiği de taşıyor.
  //
  // Süzgeç GİZLİ DEĞİL: şerit hangi eşiğin uygulandığını yazıyor ve
  // "show all" tek tık. Bir kez düşen nadir bir exception gerçekten
  // önemli olan olabilir; onu söylemeden saklamak bu deponun tekrar
  // eden hata sınıfı.
  // v0.9.417 (operatör kararı 2026-07-30, eski "1-2-3'lük grupları
  // gösterme" direktifini geri alır): varsayılan taban 0 — az sayıda
  // occurrence'lı gruplar da görünür (P3 olarak dibe sıralanır).
  const minOcc = (() => {
    const raw = searchParams.get('minOcc');
    if (raw === null) return 0;
    const n = Number(raw);
    return Number.isFinite(n) && n >= 0 ? n : 0;
  })();
  const setMinOcc = (v: number) => setSearchParams(prev => {
    const next = new URLSearchParams(prev);
    if (v === 0) next.delete('minOcc'); else next.set('minOcc', String(v));
    next.delete('page');
    return next;
  }, { replace: true });
  const ownerTeam = searchParams.get('owner') || '';
  const sreTeam   = searchParams.get('sre')   || '';
  const page    = Math.max(0, parseInt(searchParams.get('page') || '0', 10) || 0);
  const setTab = (v: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('tab', v); p.delete('page');
    return p;
  }, { replace: true });
  const setService = (v: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('service', v); else p.delete('service');
    p.delete('page');
    return p;
  }, { replace: true });
  const setTeam = (key: 'owner' | 'sre', v: string) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set(key, v); else p.delete(key);
    p.delete('page'); // a filter change can't leave you on a now-missing page
    return p;
  }, { replace: true });
  const setPage = (next: number | ((p: number) => number)) =>
    setSearchParams(prev => {
      const p = new URLSearchParams(prev);
      const v = typeof next === 'function' ? next(page) : next;
      if (v > 0) p.set('page', String(v)); else p.delete('page');
      return p;
    }, { replace: true });
  const [search, setSearch] = useState('');
  // v0.8.318 — search commits debounced and runs SERVER-side (q=): the old
  // client filter only searched the loaded 50-row page, so matches on
  // other pages read as "no results".
  const [committedSearch, setCommittedSearch] = useState('');
  useEffect(() => {
    const t = window.setTimeout(() => {
      setCommittedSearch(prev => {
        const next = search.trim();
        if (next !== prev) setPage(0);
        return next;
      });
    }, 300);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search]);
  const [users, setUsers] = useState<UserRow[]>([]);
  const [data, setData] = useState<ExceptionGroup[] | null | undefined>(undefined);
  // v0.8.485 (sadeleştirme #3) — refetch'te tablo boşalmaz: önceki sayfa
  // solgunlaşarak kalır, skeleton yalnız ilk yüklemede (Traces/Services/
  // Service-detay ile aynı keep-data dili).
  const [refreshing, setRefreshing] = useState(false);
  // v0.9.858 — sunucunun hata metni; hata dalı bunu aynen gösterir.
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const dataRef = useRef<ExceptionGroup[] | null | undefined>(undefined);
  dataRef.current = data;
  const [total, setTotal] = useState(0);
  const PAGE_SIZE = 50;
  // Expanded fingerprint(s) — multiple groups can be open at once for compare-and-contrast.
  // Seed with any ?exception=<fingerprint> the URL carries so a
  // deep-link (e.g. from /inbox) lands with the right row open.
  const [expanded, setExpanded] = useState<Set<string>>(() => {
    const fp = searchParams.get('exception');
    return new Set(fp ? [fp] : []);
  });
  // Selected group for the full in-page detail view (null = list).
  const [detail, setDetail] = useState<ExceptionGroup | null>(null);
  // v0.8.443 (operator-reported): a specific exception-group problem
  // couldn't be shared as a link — this detail view only ever lived in
  // `detail` local state, never the URL, unlike the alert-rule problem
  // detail's ?problem=<id> (v0.8.256/426/428). ?exc=<fingerprint> gives
  // it the same both-ways contract: open/close write it, and it's
  // resolved back on mount/refresh — checking the loaded page first,
  // then falling back to a direct fetch so a shared link still works
  // even when the group isn't on the requester's current tab/filter/page.
  const excParam = searchParams.get('exc');
  const [excNotFound, setExcNotFound] = useState(false);
  useEffect(() => {
    if (!excParam) { setDetail(null); setExcNotFound(false); return; }
    if (detail?.fingerprint === excParam) return;
    const hit = (data ?? []).find(g => g.fingerprint === excParam);
    if (hit) { setDetail(hit); setExcNotFound(false); return; }
    let cancelled = false;
    api.getExceptionGroup(excParam)
      .then(g => { if (!cancelled) { setDetail(g); setExcNotFound(false); } })
      .catch(() => { if (!cancelled) { setDetail(null); setExcNotFound(true); } });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [excParam, data]);
  // v0.9.1149 — "▸ Ne oldu?" çipi + satır-altı insight yuvası bu
  // listeden KALDIRILDI (operatör kararı 2026-08-17: detaya girince
  // Explain zaten var, satırdaki buton kötü görünüyordu; v0.9.1133'te
  // gelmişti). Worker-yazımı pasif aiSummary satırı KALIYOR — o buton
  // değil, sıfır-fetch metin. Problems/desen yuvalarına dokunulmadı.
  const openExcDetail = (g: ExceptionGroup) => {
    setDetail(g);
    setExcNotFound(false);
    setSearchParams(prev => withExcParam(prev, g.fingerprint), { replace: true });
  };
  const closeExcDetail = () => {
    setDetail(null);
    setExcNotFound(false);
    setSearchParams(prev => withExcParam(prev, null), { replace: true });
  };

  // Sort lives in the shared DataTable hook, serverSort mode
  // (v0.8.251 Services template): the hook owns the header UX, the
  // `s_exception-inbox` URL param and localStorage persistence; the
  // fetch below forwards the sanitized pair so a stale persisted id
  // (old column schema, hand-edited URL) never reaches the backend
  // ORDER BY. Must precede the fetch effect that consumes it.
  const dt = useDataTable<ExceptionGroup>({
    storageKey: 'exception-inbox',
    columns: EXC_COLS,
    rows: data ?? [],
    serverSort: true,
    initialSort: DEFAULT_EXC_SORT,
  });
  const sortOk = (SORT_KEYS as readonly string[]).includes(dt.sort.id ?? '');
  const sortBy: SortKey = sortOk ? dt.sort.id as SortKey : DEFAULT_EXC_SORT.id;
  const sortDir = sortOk ? dt.sort.dir : DEFAULT_EXC_SORT.dir;

  // Exception groups inbox — separate query because it depends
  // on tab + service filter; couldn't be folded into the shared
  // anomaly hooks above.
  const qc = useQueryClient();
  // v0.9.941 (UX denetimi B1/K8) — ORTAM SÜZGECİ. Topbar seçicisi bu
  // sayfada GÖRÜNÜYOR ama uygulanmıyordu (bayrak açıkça kapalıydı):
  // operatör prod'u seçip int/uat exception'larını listede görmeye devam
  // ediyordu — seçicinin varlığı yalan söylüyordu. Sunucu env'i üye
  // servislere çözüp `service IN (…)` ile daraltıyor, yani daraltma
  // limit/offset'ten ÖNCE ısırıyor (filter-after-LIMIT sınıfına
  // düşmüyoruz).
  const [env] = useUrlEnv();
  const refreshExceptionGroups = () => {
    if (dataRef.current && dataRef.current.length) {
      setRefreshing(true);
    } else {
      setData(undefined);
    }
    api.exceptionGroups({
      state: tab, service: service || undefined,
      ownerTeam: ownerTeam || undefined, sreTeam: sreTeam || undefined,
      env: env || undefined,
      // v0.8.318 — sort + search are server-side across the whole set;
      // the client-side sort of one server page mis-prioritized ("top by
      // occurrences" was really "most-recent 50, reordered").
      sort: sortBy, dir: sortDir,
      q: committedSearch || undefined,
      limit: PAGE_SIZE, offset: page * PAGE_SIZE,
      minOccurrences: minOcc > 0 ? minOcc : undefined, // v0.9.315
    })
      .then(d => { setData(d.items ?? []); setTotal(d.total ?? 0); setLoadErr(null); setRefreshing(false); })
      // v0.9.858 (UX denetimi K6) — null hiçbir render dalına girmiyordu:
      // exception listesinin sorgu hatası BOŞ EKRAN demekti ve "exception
      // yok, temiziz" diye okunuyordu. Sinyal kaybının en pahalı yeri.
      .catch(e => {
        setData(null); setRefreshing(false);
        setLoadErr(e instanceof Error ? e.message : String(e));
      });
  };
  // Page reset on filter change is owned by setTab/setService/setTeam
  // (they delete ?page=); search resets it itself. A SORT change also
  // invalidates the page offset — that reset lives INSIDE the fetch
  // effect, guarded by a sig ref, so (a) a deep-linked ?page= survives
  // mount, and (b) sorting while on page N doesn't fire a wasted fetch
  // for the stale offset first (refreshExceptionGroups has no
  // cancellation — two in-flight fetches would race). It can't ride
  // the hook's onSortChange either: the hook's URL write and setPage
  // are separate same-tick setSearchParams calls, and the second would
  // clobber the first (the v0.8.253 URL-overwrite class).
  const sortSig = `${sortBy}.${sortDir}`;
  const lastSortSigRef = useRef(sortSig);
  useEffect(() => {
    if (lastSortSigRef.current !== sortSig) {
      lastSortSigRef.current = sortSig;
      if (page !== 0) { setPage(0); return; } // effect re-runs with page 0
    }
    refreshExceptionGroups();
    // eslint-disable-next-line react-hooks/exhaustive-deps
    // env (v0.9.941) dep listesinde: Topbar'dan ortam değiştirildiğinde
    // liste yeniden okunmalı, yoksa süzgeç bir sonraki başka değişikliğe
    // kadar uygulanmamış görünür.
  }, [tab, service, ownerTeam, sreTeam, env, page, sortSig, committedSearch, minOcc]);


  useEffect(() => {
    if (!isAdmin) return;
    api.listUsers().then(u => setUsers(u ?? [])).catch(() => {});
  }, [isAdmin]);

  // Team dropdown options come from the service catalog (not the loaded
  // page), so a pick never collapses the list of teams to choose from —
  // same source the alert-rules section + Services page use.
  const catalogQ = useServicesMetadata();
  // v0.8.330 — case-insensitive dedup: mixed-casing team attrs
  // ("avengerSY"/"Avengersy") listed the same team twice.
  const ownerTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.ownerTeam)),
    [catalogQ.data]);
  const sreTeamOptions = useMemo(
    () => teamOptionsCI(Object.values(catalogQ.data ?? {}).map(m => m.sreTeam)),
    [catalogQ.data]);

  // v0.8.318 — the server owns filtering AND ordering now (q=/sort=/dir=
  // across the whole paginated set); the page renders rows verbatim. The
  // old client-side sort of one 50-row server page mis-prioritized, and
  // the client search read as "no results" for matches on other pages.
  const filtered = data ?? [];

  const userById = useMemo(() => {
    const m = new Map<string, UserRow>();
    users.forEach(u => m.set(u.id, u));
    return m;
  }, [users]);

  const setState = async (g: ExceptionGroup, next: ExceptionGroupState) => {
    try {
      await api.setExceptionGroupState(g.fingerprint, next);
      // Refresh the exception inbox + every anomaly feed so a
      // state change percolates everywhere it might appear
      // (the /anomalies page consumes the same cache).
      refreshExceptionGroups();
      qc.invalidateQueries({ queryKey: keys.anomalies.all });
    } catch (err) { alert(humanize(err)); }
  };
  const setAssignee = async (g: ExceptionGroup, userId: string) => {
    try {
      await api.assignExceptionGroup(g.fingerprint, userId);
      refreshExceptionGroups();
    } catch (err) { alert(humanize(err)); }
  };
  const toggleExpand = (fp: string) => {
    setExpanded(prev => {
      const next = new Set(prev);
      if (next.has(fp)) next.delete(fp);
      else next.add(fp);
      return next;
    });
  };

  // Full in-page exception-group detail (prototype design-parity). Clicking a
  // row opens it; back returns to this list. Caret still toggles the inline
  // quick-peek (SamplesPanel) so both affordances coexist. Shareable via
  // ?exc=<fingerprint> (openExcDetail/closeExcDetail keep the URL in sync).
  if (detail) {
    return (
      <>
        <Topbar title="Exceptions" showEnv envApplies />
        <ProblemDetail
          group={detail}
          isAdmin={isAdmin}
          onBack={closeExcDetail}
          onChanged={() => { refreshExceptionGroups(); qc.invalidateQueries({ queryKey: keys.anomalies.all }); }}
        />
      </>
    );
  }
  // A shared ?exc=<fingerprint> link that doesn't resolve — the group
  // was purged, or the id is stale/malformed. Honest empty state
  // instead of silently falling through to the list.
  if (excParam && excNotFound) {
    return (
      <>
        <Topbar title="Exceptions" showEnv envApplies />
        <PageShell>
          <Empty icon="❓" title="Exception not found">
            Bu exception grubu artık mevcut değil.{' '}
            <Button variant="secondary" size="sm" onClick={closeExcDetail}>← Exceptions</Button>
          </Empty>
        </PageShell>
      </>
    );
  }
  if (excParam && !excNotFound) {
    return (
      <>
        <Topbar title="Exceptions" showEnv envApplies />
        <PageShell><Spinner /></PageShell>
      </>
    );
  }

  // v0.8.426/428 — a firing alert problem opens as a Variant-B
  // full-page detail on the same route (?problem=<id>). The classic
  // table list stays MOUNTED underneath (display:none) so facet /
  // team / bulk-selection state survives "← Exceptions" / Esc.
  const problemParam = searchParams.get('problem');

  return (
    <>
      {/* v0.9.323 — this list is exception GROUPS; "Problems" now names the
          merged triage queue at /inbox. Same page, same route, honest label. */}
      {!problemParam && <Topbar title="Exceptions" showEnv envApplies />}
      {/* Hidden (NOT unmounted) while the full-page detail is open — facet /
          team / bulk-selection state survives "← Exceptions" / Esc.
          v0.9.981 (D3.1) — kap artık `id="content"` YERİNE `.page-body`.
          Eski yorum "duplicate #content id burada inert" diyerek kendi
          gerekçesini yazıyordu: doğruydu AMA yalnız `useContentWidth`in
          bugünkü çağrı listesi (Dashboard / ServiceCharts / Explore)
          sayesinde. Bu sayfaya bir sparkline eklendiği gün
          `getElementById('content')` GİZLİ düğümü döndürür (clientWidth 0)
          ve grafik adımı sessizce yanlış hesaplanır. Sınıfa geçmek
          gerekçeyi çağrı listesine bağımlı olmaktan çıkarıyor. */}
      <div className="page-body" style={problemParam ? { display: 'none' } : undefined}>
        <TriageCrumb label="Problems" />
        <SavedViewsBar page="problems" />

        {/* ── 1. Exception inbox (top of page) ─────────────────
            Per-group state machine the operator triages: New →
            Ack → Resolved/Ignored. Sits at the very top because
            it's the most actionable signal in the product —
            this is the assignable queue an SRE works through. */}
        <div className="tab-strip">
          {TABS.map(t => (
            <button key={t.key} onClick={() => setTab(t.key)} title={t.hint}
              className={tab === t.key ? 'active' : ''}>
              {t.label}
            </button>
          ))}
        </div>

        <PageControls sticky>
          <ServicePicker value={service} onChange={setService}
            placeholder="Service…" width={170} />
          {/* v0.9.1003 (etkileşim denetimi O4) — `/` sayfanın ASIL
              aramasına iner. İşaretsizken GlobalShortcuts fallback'i
              bardaki ilk metin kutusunu (ServicePicker) seçiyordu. */}
          {/* v0.9.1004 (M2/O5) — dar ekranda yüzeyde kalan kontrol. */}
          <SearchField value={search} onChange={setSearch}
            data-shortcut-search data-pc-lead
            aria-label="Search anomaly type or message"
            hint="/"
            placeholder="Search type/message…" width={260} />
          {/* Owner (ug-team) / SRE (sy-team) team filter — plain <select>
              for these small catalog-derived sets (frontend-conventions
              §3), resolved server-side so the narrowing is correct across
              the whole paginated inbox, not just the loaded page. */}
          <select value={ownerTeam} onChange={e => setTeam('owner', e.target.value)}
            aria-label="Filter by owner team" style={{ minWidth: 130 }}>
            <option value="">All owner teams</option>
            {ownerTeamOptions.map(t => <option key={t} value={t}>{t}</option>)}
          </select>
          <select value={sreTeam} onChange={e => setTeam('sre', e.target.value)}
            aria-label="Filter by SRE team" style={{ minWidth: 130 }}>
            <option value="">All SRE teams</option>
            {sreTeamOptions.map(t => <option key={t} value={t}>{t}</option>)}
          </select>
          <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto' }}>
            {total > 0 && (
              <>
                {page * PAGE_SIZE + 1}–{Math.min((page + 1) * PAGE_SIZE, total)} of {fmtNum(total)} groups
                {search.trim() && <> · {filtered.length} on this page match</>}
              </>
            )}
          </span>
        </PageControls>

        {data === undefined && <Spinner />}
        {data === null && (
          <QueryError message={loadErr} onRetry={refreshExceptionGroups}>
            The exception list could not be loaded. This is a failed read — an
            empty page here would have read as "no exceptions".
          </QueryError>
        )}
        {data && filtered.length === 0 && (
          <Empty icon="✓" title={tab === 'open'
            ? 'Inbox is clear — no untriaged exceptions'
            : `No groups in "${tab}"`}>
            {/* v0.6.24 — explain why each tab might legitimately
                be empty so operators don't think the page broke. */}
            {tab === 'resolved' && (
              <>Groups land here when you click <b>Resolve</b> on a row in the Inbox,
              or automatically after 14 days without a new occurrence.</>
            )}
            {tab === 'ignored' && (
              <>Groups land here when you click <b>Ignore</b> on a row. Ignored groups
              stay silent even if they fire again.</>
            )}
            {tab === 'acknowledged' && (
              <>Groups land here when you <b>Ack</b> a row — you've seen it but haven't
              fixed it yet. Click <b>Resolve</b> to move out of ack.</>
            )}
            {tab !== 'resolved' && tab !== 'ignored' && tab !== 'acknowledged' && (
              <>Click a row to inspect recent occurrences. Use Ack / Resolve / Ignore to manage state.</>
            )}
          </Empty>
        )}
        {/* v0.9.315 (operatör) — occurrence eşiği ŞERİDİ. Süzgecin
            kendisi kadar önemli: bir kez düşen nadir bir exception
            gerçekten önemli olan olabilir, ve onu söylemeden saklamak
            bu deponun tekrar eden hata sınıfı. Eşik yazılı, kaldırmak
            tek tık. */}
        {data && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            fontSize: 11.5, color: 'var(--text3)', marginBottom: 8,
          }}>
            {minOcc > 0 ? (
              <>
                <span title="Bir kez düşen bir exception bu pencere hakkında bir olgudur, triage edilecek bir problem değil. Eşiğin altındakiler gizli — hepsini görmek için tıkla.">
                  Showing groups with <b style={{ color: 'var(--text2)' }}>{minOcc}+</b> occurrences
                </span>
                <Button variant="ghost" size="sm" onClick={() => setMinOcc(0)}>show all</Button>
                {minOcc !== 10 && (
                  <Button variant="ghost" size="sm" onClick={() => setMinOcc(10)}>10+</Button>
                )}
              </>
            ) : (
              <>
                <span>Showing <b style={{ color: 'var(--text2)' }}>every</b> group, including one-off exceptions</span>
                <Button variant="ghost" size="sm" onClick={() => setMinOcc(5)}>5+ only</Button>
              </>
            )}
          </div>
        )}
        {data && filtered.length > 0 && (
          <div className="table-wrap"
            style={{ opacity: refreshing ? 0.55 : 1, transition: 'opacity 120ms' }}
            aria-busy={refreshing}>
            <table style={{ tableLayout: 'fixed', width: '100%' }}>
              <DataTableColgroup dt={dt} leading={[24]} trailing={isAdmin ? [208] : undefined} />
              <DataTableHead dt={dt}
                leading={<th style={{ width: 24 }}></th>}
                trailing={isAdmin ? <th style={{ width: 208 }}>Actions</th> : undefined} />
              <tbody>
                {filtered.map(g => {
                  const open = expanded.has(g.fingerprint);
                  // v0.10.221 — düz hücreler gerçek <Link> (orta tık yeni
                  // sekme); caret/servis/eylem hücreleri kendi tıklarını
                  // taşır, satır onClick'i kalan boşluk için kalıyor.
                  const excHref = excDetailHref(location.pathname, searchParams, g.fingerprint);
                  return (
                    <Fragment key={g.fingerprint}>
                      <tr {...rowActivation(() => openExcDetail(g))}
                        onKeyDown={(e) => {
                          // Enter/Space opens the full detail (keyboard parity
                          // with the click). The caret cell handles the inline
                          // quick-peek separately.
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            openExcDetail(g);
                          }
                        }}
                        tabIndex={0}
                        role="button"
                        aria-expanded={open}
                        style={{ cursor: 'pointer' }}>
                        <td style={{ color: 'var(--text3)', textAlign: 'center', cursor: 'pointer' }}
                          title={open ? 'Hide occurrences' : 'Peek occurrences'}
                          onClick={e => { e.stopPropagation(); toggleExpand(g.fingerprint); }}>
                          {open
                            ? <ChevronDown size={13} strokeWidth={1.75} style={{ verticalAlign: 'middle' }} />
                            : <ChevronRight size={13} strokeWidth={1.75} style={{ verticalAlign: 'middle' }} />}
                        </td>
                        <td className="row-cell"><Link to={excHref} replace className="row-link" onClick={e => e.stopPropagation()}><StateBadge s={g.state} />{g.priority && <>{' '}<PriorityBadge p={g.priority} reason={g.priorityReason} /></>}</Link></td>
                        <td className="row-cell">
                          <Link to={excHref} replace className="row-link" onClick={e => e.stopPropagation()}>
                          <div className="mono" style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 600, fontSize: 11.5, color: 'var(--err)' }}>
                            {g.type}
                            {/* First observed within the last hour —
                                the highest-signal marker for an SRE
                                scanning the list in the morning: these
                                did not exist yesterday.

                                v0.9.314 — was labelled "NEW", which now
                                belongs to the STATE column. "<1h" says
                                literally what it means and cannot be
                                read as a triage state. */}
                            {Date.now() - g.firstSeen / 1e6 < 60 * 60 * 1000 && (
                              <span className="badge b-warn" style={{ fontSize: 9, padding: '0 5px' }}
                                title="First seen within the last hour — this exception did not exist before that.">
                                &lt;1h
                              </span>
                            )}
                          </div>
                          <div className="mono" style={{ fontSize: 10.5, color: 'var(--text3)',
                                        maxWidth: 480, overflow: 'hidden',
                                        textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                               title={g.message}>
                            {g.message || '—'}
                          </div>
                          {/* v0.9.1133 (AI Faz 2.3) — PASİF özet satırı.
                              ExceptionExplainer'ın arka planda yazdığı
                              `aiSummary` (v0.9.415) bu listenin payload'ında
                              ZATEN geliyordu (ListExceptionGroups ai_summary'yi
                              seçiyor) ama hiçbir yerde çizilmiyordu — yalnız
                              detay sayfası gösteriyordu. Sıfır fetch, sıfır
                              LLM: satır ne yazılmışsa onu okur. Özet YOKSA
                              hiçbir şey çizilmez (uydurma bir cümle yerine
                              sessizlik); yalnız çip durur.
                              stripMarkdown ŞART: model `**kalın**` üretiyor ve
                              tek satırlık kırpılmış bir yüzeyde yıldızlar
                              ekrana dökülür (v0.9.641/696 sınıfı). */}
                          {g.aiSummary && (
                            <div style={{
                              display: 'flex', alignItems: 'baseline', gap: 5, marginTop: 3,
                              fontSize: 10.5, color: 'var(--text2)', maxWidth: 480, minWidth: 0,
                            }} title={stripMarkdown(g.aiSummary)}>
                              <IconSparkles size={10} />
                              <span style={{
                                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                              }}>{stripMarkdown(g.aiSummary)}</span>
                            </div>
                          )}
                          </Link>
                        </td>
                        <td>
                          {/* v0.9.966 — grubun KENDİ ömrü (firstSeen→
                              lastSeen). ProblemDetail aynı satır tipi için
                              zaten bu pencereyi kullanıyor; listeden ve
                              detaydan açılan servis sayfası aynı zamana
                              bakmalı. */}
                          <Link to={serviceHref(g.service, { range: { fromNs: g.firstSeen, toNs: g.lastSeen } })}
                            onClick={e => e.stopPropagation()}
                            style={{ fontFamily: 'monospace', fontSize: 11 }}>
                            {g.service}
                          </Link>
                        </td>
                        <td className="mono row-cell" style={{ textAlign: 'right', fontWeight: 600, color: 'var(--err)' }}>
                          <Link to={excHref} replace className="row-link" onClick={e => e.stopPropagation()}>{fmtNum(Number(g.occurrences))}</Link>
                        </td>
                        <td className="mono row-cell" style={{ fontSize: 11, color: 'var(--text3)' }}><Link to={excHref} replace className="row-link" onClick={e => e.stopPropagation()}>{tsLong(g.firstSeen)}</Link></td>
                        <td className="mono row-cell" style={{ fontSize: 11, color: 'var(--text3)' }}><Link to={excHref} replace className="row-link" onClick={e => e.stopPropagation()}>{tsLong(g.lastSeen)}</Link></td>
                        <td onClick={e => e.stopPropagation()}>
                          {isAdmin ? (
                            <select value={g.assignee} onChange={e => setAssignee(g, e.target.value)}
                              style={{ fontSize: 11, maxWidth: 160 }}>
                              <option value="">— unassigned —</option>
                              {users.map(u => (
                                <option key={u.id} value={u.id}>{u.email}</option>
                              ))}
                            </select>
                          ) : (
                            <span style={{ fontSize: 11, color: 'var(--text2)' }}>
                              {g.assignee ? (userById.get(g.assignee)?.email ?? g.assignee) : '—'}
                            </span>
                          )}
                        </td>
                        {isAdmin && (
                          <td onClick={e => e.stopPropagation()}>
                            <ActionButtons g={g} onSet={setState} />
                          </td>
                        )}
                      </tr>
                      {open && (
                        <tr>
                          <td colSpan={isAdmin ? 9 : 8} style={{
                            background: 'var(--bg1)', padding: '10px 16px',
                            borderTop: '1px solid var(--border)',
                          }}>
                            <SamplesPanel fingerprint={g.fingerprint} occurrences={Number(g.occurrences)} />
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {/* v0.9.1015 — paylaşılan sözleşme (v0.9.1014). Elle çizilmiş
            şerit üç yerde ayrışıyordu: sağa yaslıydı (Gutenberg'e göre
            "ileri" sağda olmalı ama şeridin KENDİSİ ortada/solda
            başlamalı), yapışkan değildi (50 satırlık bir tabloda Next
            ekranın dışında kalıyordu) ve İKİ butonu da ikincildi — yani
            baskın eylem yoktu. Sayı burada KESİN: /api/anomalies grup
            sayısını tam döndürüyor, tavan yok, dolayısıyla son sayfa hem
            türetiliyor hem ulaşılabiliyor. */}
        {data && total > PAGE_SIZE && (
          <Pager mode="offset" count="exact" total={total}
            page={page} pageSize={PAGE_SIZE} onPage={setPage}
            lastReachablePage={Math.max(0, Math.ceil(total / PAGE_SIZE) - 1)} />
        )}
      </div>
      {problemParam && (
        <AlertProblemHost
          id={problemParam}
          isAdmin={isAdmin}
          onBack={() => setSearchParams(prev => withProblemParam(prev, null), { replace: true })}
        />
      )}
    </>
  );
}

// SamplesPanel — fetches recent occurrences for the group and lists them
// as collapsible cards. Stacktraces are folded by default; trace/span IDs
// link out to the waterfall.
function SamplesPanel({ fingerprint, occurrences }: { fingerprint: string; occurrences?: number }) {
  const [samples, setSamples] = useState<ExceptionSample[] | null | undefined>(undefined);
  // v0.9.795 — boş listenin nedenini zarf söyler (tavan / pencere bitti /
  // gerçekten yok); tek bir "capped" bayrağı üçünü aynı cümleye sıkıştırıyordu.
  const [scan, setScan] = useState<SampleScanEnvelope>(null);
  const [limit, setLimit] = useState(10);
  const [err, setErr] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);
  // v0.9.314 — the total the table no longer shows. Kept here rather
  // than dropped: how often a group fires is real context ONCE you are
  // looking at the group, it was only misleading as a scan-time column.

  useEffect(() => {
    setSamples(undefined);
    api.exceptionGroupSamples(fingerprint, limit)
      .then(r => { setSamples(r?.samples ?? []); setScan(r ?? null); setErr(null); })
      .catch(e => { setSamples(null); setErr(e instanceof Error ? e.message : String(e)); });
  }, [fingerprint, limit, retry]);

  if (samples === undefined) return <Spinner />;
  // v0.9.858 (UX denetimi K6) — hata "No sample occurrences found." olarak
  // sunuluyordu: taranmış-ama-boş ile sorgu-düştü ayırt edilemiyordu.
  if (samples === null) {
    return <QueryErrorInline text={err ? `Samples could not be loaded — ${err}` : 'Samples could not be loaded.'}
      onRetry={() => setRetry(n => n + 1)} />;
  }
  // v0.9.867 — `!samples ||` buradan SİLİNDİ: null dalı yukarıda kendi
  // dönüşüne kavuştuğu için (v0.9.858) artık ölü, ama null-yutan guard'ın
  // birebir yazım şekli olduğu için okuyanı yanıltıyor ve hata dalı bir gün
  // yer değiştirirse sınıfı sessizce geri getirir.
  if (samples.length === 0) {
    const note = emptySamplesNote(scan, 'No sample occurrences found.');
    return (
      <div style={{ color: note.warn ? 'var(--warn)' : 'var(--text3)', fontSize: 12 }}>
        {note.text}
      </div>
    );
  }

  const distinct = new Set(samples.map(s => s.message)).size;

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 8 }}>
        <span style={{ fontSize: 12, color: 'var(--text2)', fontWeight: 600 }}>
          {occurrences != null && occurrences > 0 && (
            <span style={{ color: 'var(--err)', marginRight: 6 }}
              title="Total times this exception group has fired in the window. Shown here rather than as a table column: as a column it read like a priority signal, and it isn't — a single OOM outranks a noisy retry loop.">
              {fmtNum(occurrences)}×
            </span>
          )}
          Recent occurrences
        </span>
        {distinct > 1 && (
          <span style={{ marginLeft: 8, fontSize: 11, color: 'var(--text3)' }}>
            · {distinct} distinct messages observed in this group
          </span>
        )}
        <span style={{ flex: 1 }} />
        <label style={{ fontSize: 11, color: 'var(--text3)', marginRight: 6 }}>Show</label>
        <select value={limit} onChange={e => setLimit(parseInt(e.target.value))}
          style={{ fontSize: 11 }}>
          <option value={5}>5</option>
          <option value={10}>10</option>
          <option value={25}>25</option>
          <option value={50}>50</option>
        </select>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {samples.map((s, i) => <SampleCard key={`${s.spanId}-${i}`} sample={s} index={i + 1} />)}
      </div>
    </div>
  );
}

function SampleCard({ sample, index }: { sample: ExceptionSample; index: number }) {
  const [showTrace, setShowTrace] = useState(false);
  const hasTrace = sample.stacktrace.trim().length > 0;
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 10,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, fontSize: 12 }}>
        <span style={{ color: 'var(--text3)', fontFamily: 'monospace' }}>#{index}</span>
        <Link to={traceHref(sample.traceId)} style={{ fontFamily: 'monospace' }}>
          {sample.traceId.slice(0, 12)}…
        </Link>
        <span style={{ color: 'var(--text2)', fontFamily: 'monospace', fontSize: 11 }}>
          span <code>{sample.spanId.slice(0, 8)}</code>
          {sample.spanName && <> · <b>{sample.spanName}</b></>}
        </span>
        <span style={{ flex: 1 }} />
        <span style={{ color: 'var(--text3)', fontSize: 11 }}>{tsLong(sample.time)}</span>
      </div>
      {sample.message && (
        <div style={{ fontSize: 12, color: 'var(--text)', marginTop: 6,
                      fontFamily: 'monospace', wordBreak: 'break-word' }}>
          {sample.message}
        </div>
      )}
      {sample.statusMsg && sample.statusMsg !== sample.message && (
        <div style={{ fontSize: 11, color: 'var(--text2)', marginTop: 4, fontFamily: 'monospace' }}>
          status: {sample.statusMsg}
        </div>
      )}
      {hasTrace && (
        <>
          <Button variant="secondary" size="sm" style={{ marginTop: 8 }}
            onClick={() => setShowTrace(t => !t)}>
            {showTrace
              ? <><ChevronDown size={12} strokeWidth={1.75} /> Hide stacktrace</>
              : <><ChevronRight size={12} strokeWidth={1.75} /> Show stacktrace</>}
          </Button>
          {showTrace && (
            <pre style={{
              marginTop: 6, padding: 10, background: 'var(--bg)',
              border: '1px solid var(--border)', borderRadius: 4,
              fontSize: 11, lineHeight: 1.45, overflow: 'auto', maxHeight: 280,
              whiteSpace: 'pre', fontFamily: 'monospace',
            }}>{sample.stacktrace}</pre>
          )}
        </>
      )}
    </div>
  );
}

function ActionButtons({ g, onSet }: {
  g: ExceptionGroup; onSet: (g: ExceptionGroup, s: ExceptionGroupState) => void;
}) {
  // Compact secondary actions in a flex row — uniform gap instead of
  // per-button margins, all on the canonical secondary/sm Button.
  const row = (...kids: React.ReactNode[]) => (
    <span style={{ display: 'inline-flex', gap: 4 }}>{kids}</span>
  );
  switch (g.state) {
    case 'new':
    case 'regressed':
      return row(
        <Button key="ack" variant="secondary" size="sm" onClick={() => onSet(g, 'acknowledged')}>Ack</Button>,
        <Button key="res" variant="secondary" size="sm" onClick={() => onSet(g, 'resolved')}>Resolve</Button>,
        <Button key="ign" variant="secondary" size="sm" onClick={() => onSet(g, 'ignored')}>Ignore</Button>,
      );
    case 'acknowledged':
      return row(
        <Button key="res" variant="secondary" size="sm" onClick={() => onSet(g, 'resolved')}>Resolve</Button>,
        <Button key="reo" variant="secondary" size="sm" onClick={() => onSet(g, 'new')}>Reopen</Button>,
        <Button key="ign" variant="secondary" size="sm" onClick={() => onSet(g, 'ignored')}>Ignore</Button>,
      );
    case 'resolved':
      return <Button variant="secondary" size="sm" onClick={() => onSet(g, 'new')}>Reopen</Button>;
    case 'ignored':
      return <Button variant="secondary" size="sm" onClick={() => onSet(g, 'new')}>Unignore</Button>;
  }
  return null;
}

function StateBadge({ s }: { s: ExceptionGroupState }) {
  const cls =
    s === 'new'          ? 'b-err'  :
    s === 'regressed'    ? 'b-warn' :
    s === 'acknowledged' ? 'b-info' :
    s === 'resolved'     ? 'b-ok'   :
                           'b-gray';
  // v0.9.314 (operatör) — untriaged reads NEW again, which is what the
  // column actually stores (exception_groups.state DEFAULT 'new').
  //
  // v0.8.382 had renamed it to OPEN for a real reason: the word
  // collided with the yellow first-seen-recently chip beside the
  // exception type, so a weeks-old untriaged group looked identical to
  // something that arrived this morning. Flipping the label back alone
  // would restore exactly that confusion — so the CHIP was renamed to
  // "<1h" in the same change. One word, one meaning: NEW = nobody has
  // triaged this; <1h = this did not exist an hour ago.
  const label = s.toUpperCase();
  return <span className={`badge ${cls}`} title={s === 'new' ? 'Untriaged — nobody has acknowledged, resolved or ignored this yet' : undefined}>{label}</span>;
}

function humanize(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') return j.error;
  } catch {}
  return body || msg;
}

