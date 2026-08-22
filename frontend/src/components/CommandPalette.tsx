import { useEffect, useMemo, useRef, useState } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import { scorePaletteEntry } from '@/lib/paletteScore';
import { useT } from '@/lib/i18n';
import { SETTINGS_TAB_INDEX } from '@/pages/settings/tabIndex';
import { useLocation, useNavigate } from 'react-router-dom';
import { navHref } from '@/lib/navHref';
import { serviceHref } from '@/lib/serviceHref';
import { api } from '@/lib/api';
import { operationTracesHref } from '@/lib/pivotHref';
import { currentRange } from '@/lib/useUrlRange';
import { getRecentServices, getPinnedServices } from '@/lib/recentServices';
import { useShortcuts } from '@/lib/keyboard';
import { useAuth } from '@/components/AuthProvider';
import {
  filterActions, DEFAULT_DURATIONS,
  type Action, type ParamValues, type SuggestItem,
} from '@/lib/actions';
import { toast } from '@/lib/toast';
import { Button, LinkButton } from '@/components/ui';

// CommandPalette — global Cmd-K / Ctrl-K spotlight (v0.5.162).
// Mounted once at AppShell level; listens for the hotkey and pops
// the modal. Three result kinds in v1:
//   • Pages — hardcoded route catalog (every internal SPA page)
//   • Services — server-searched per keystroke (v0.8.518), pinned/recent
//                in-memory for the session
//   • Trace — when the query looks like a trace id (32 hex chars)
//             a "Go to trace" suggestion appears
//
// Designed to feel like Linear / Raycast: opens in 16ms, results
// re-rank as the user types, arrows + enter to select, Esc to
// close. No search-index dep — substring scoring is fine at our
// catalog size (~30 pages + N services).

type Result = {
  kind: 'page' | 'service' | 'trace' | 'action' | 'endpoint';
  label: string;
  // v0.9.1270 (B5#1) — sayfa adı sidebar'la TEK kaynaktan: navKey
  // varsa etiket t(navKey) ile çözülür (i18n + TR paritesi bedava);
  // aliases eski/alternatif adları aranabilir tutar (paletteScore).
  navKey?: string;
  aliases?: string[];
  // v0.9.1272 (#3/Q3) — admin-kapılı sayfalar palette rol-süzgeçli:
  // viewer'a gidemeyeceği Settings/System girişlerini önermek boş vaat.
  adminOnly?: boolean;
  hint?: string;
  // navigate target — set for page/service/trace; absent for actions
  // (selecting an action enters param-prompt mode, doesn't navigate).
  to?: string;
  // action payload — set when kind === 'action'.
  action?: Action;
  score?: number;
};

// v0.8.525 — güncel rotalara hizalandı. Önceki katalogda ölü hedefler
// vardı (Home /, Topology /topology, Errors /errors, Status /status —
// hepsi redirect/retired) ve tüm 'Admin · …' girişleri /admin/* idi;
// admin sayfaları v0.8.9'da /system/:tab altına taşındı. Eksik canlı
// sayfalar da eklendi (Inbox, Endpoints, Service Map, External, Hosts,
// Trace compare, Events, AI). Sidebar'dan gizli sayfalar (Monitors,
// Profiling, External, Hosts, Events) ⌘K'da BİLEREK kalır — keşif
// yüzeyi burasıdır.
const PAGES: Result[] = [
  // Triage
  { kind: 'page', label: 'Inbox',       navKey: 'nav.inbox', aliases: ['inbox'], hint: 'Unified triage queue', to: '/inbox' },
  { kind: 'page', label: 'Incidents',   navKey: 'nav.incidents', hint: 'Manual incident log', to: '/incidents' },
  { kind: 'page', label: 'Problems',    navKey: 'nav.problems', aliases: ['problems'], hint: 'Open alert + exception inbox', to: '/problems' },
  { kind: 'page', label: 'Anomalies',   navKey: 'nav.anomalies', hint: 'Log + trace anomaly streams', to: '/anomalies' },
  // Services
  { kind: 'page', label: 'Services',    navKey: 'nav.services', hint: 'Per-service RED + latency', to: '/services' },
  { kind: 'page', label: 'Endpoints',   navKey: 'nav.endpoints', hint: 'Per-route RED', to: '/endpoints' },
  { kind: 'page', label: 'Service Map', navKey: 'nav.topology', aliases: ['service map', 'topology'], hint: 'Topology / flows', to: '/service-map' },
  { kind: 'page', label: 'Databases',   navKey: 'nav.databases', hint: 'DBM-style query catalog', to: '/databases' },
  { kind: 'page', label: 'Messaging',   navKey: 'nav.messaging', hint: 'Kafka / RabbitMQ / SQS', to: '/messaging' },
  { kind: 'page', label: 'External',    navKey: 'nav.external', hint: 'Third-party dependencies', to: '/external' },
  { kind: 'page', label: 'Hosts',       navKey: 'nav.hosts', hint: 'Infrastructure inventory', to: '/hosts' },
  // Signals
  { kind: 'page', label: 'Traces',      navKey: 'nav.traces', hint: 'Search raw traces', to: '/traces' },
  { kind: 'page', label: 'Metrics',     navKey: 'nav.metrics', hint: 'Time-series explorer', to: '/metrics' },
  { kind: 'page', label: 'Logs',        navKey: 'nav.logs', hint: 'Elasticsearch logs', to: '/logs' },
  { kind: 'page', label: 'Profiling',   navKey: 'nav.profiling', hint: 'Continuous profiling', to: '/profiling' },
  { kind: 'page', label: 'Trace compare', hint: 'Diff two traces', to: '/trace/compare' },
  // Workspaces
  { kind: 'page', label: 'Explore',     navKey: 'nav.explore', hint: 'Cross-signal ad-hoc query', to: '/explore' },
  { kind: 'page', label: 'Runbooks',    navKey: 'nav.runbooks', hint: 'Operational procedures', to: '/runbooks' },
  { kind: 'page', label: 'Dashboards',  navKey: 'nav.dashboards', hint: 'Operator-curated', to: '/dashboards' },
  // Alerting
  { kind: 'page', label: 'Alerts',      navKey: 'nav.alerts', hint: 'Alert rules + noisy report', to: '/alerts' },
  { kind: 'page', label: 'SLOs',        hint: 'Service level objectives', to: '/slos' },
  { kind: 'page', label: 'Monitors',    hint: 'Synthetic probes', to: '/monitors' },
  { kind: 'page', label: 'Events',      hint: 'Deploy / config markers', to: '/events' },
  // v0.9.952 (UX denetimi F1 / Ö17) — üç CANLI rota katalogda YOKTU.
  //
  // /deploys en ağırı: UI'dan TAMAMEN ulaşılmazdı — sidebar'da yok,
  // ⌘K'da yok, grafiklerdeki ▼ deploy işaretleri sayfaya link vermiyor.
  // Deploy geçmişine tek yol adres çubuğuydu. /clusters ve /watchers ise
  // sidebar'da GÖRÜNÜR ama palette'te aranamıyordu; klavyeyle gezinen
  // operatör için "yok" demekle aynı şey.
  //
  // Katalogun kendi yorumu "sidebar'dan gizli sayfalar ⌘K'da BİLEREK
  // kalır — keşif yüzeyi burasıdır" diyordu; üçü de o kuralın kapsamına
  // giriyordu ama listeye hiç girmemişti. Kural doğruydu, uygulaması
  // eksikti.
  { kind: 'page', label: 'Deploys',     navKey: 'nav.deploys', hint: 'Deploy history + change events', to: '/deploys' },
  { kind: 'page', label: 'Clusters',    navKey: 'nav.clusters', hint: 'Kubernetes / OpenShift inventory', to: '/clusters' },
  { kind: 'page', label: 'Watchers',    hint: 'Log pattern watchers', to: '/watchers' },
  // System (v0.8.9 — /admin/* → /system/:tab)
  { kind: 'page', label: 'System · Overview', adminOnly: true,      hint: 'Internal CH + cache stats', to: '/system/stats' },
  { kind: 'page', label: 'System · ClickHouse', adminOnly: true,    hint: 'CH health + mutations', to: '/system/clickhouse' },
  { kind: 'page', label: 'System · Elasticsearch', adminOnly: true, hint: 'ES backend health', to: '/system/elastic' },
  { kind: 'page', label: 'System · Cluster', adminOnly: true,       hint: 'Cluster topology', to: '/system/cluster' },
  { kind: 'page', label: 'System · Cardinality', adminOnly: true,   hint: 'Attribute cardinality watch', to: '/system/cardinality' },
  { kind: 'page', label: 'System · Catalog', adminOnly: true,       hint: 'Owner / runbook / oncall metadata', to: '/system/catalog' },
  { kind: 'page', label: 'System · Audit log', adminOnly: true,     hint: 'Operator action history', to: '/system/audit' },
  { kind: 'page', label: 'System · SQL', adminOnly: true,           hint: 'Raw CH query console', to: '/system/sql' },
  { kind: 'page', label: 'System · Query', adminOnly: true,         hint: 'Query console', to: '/system/query' },
  { kind: 'page', label: 'System · Status page', adminOnly: true,   hint: 'Components + subscribers', to: '/system/status-page' },
  { kind: 'page', label: 'AI observability', navKey: 'nav.ai', aliases: ['cosre', 'ai observability'], hint: 'CoSRE usage + cost', to: '/ai' },
  // Meta
  { kind: 'page', label: 'Settings',    adminOnly: true, hint: 'AI / SMTP / retention / theme', to: '/settings' },
  { kind: 'page', label: 'Users',       adminOnly: true, hint: 'Role + team management', to: '/users' },
  { kind: 'page', label: 'Public status',  hint: 'Public incident page', to: '/public-status' },
];

// Module-level cache so re-opening the palette in the same tab
// doesn't re-fetch services every time.

const TRACE_ID_RE = /^[a-f0-9]{16,32}$/i;

// ——— Görünür kapı kanalı (v0.9.1019, G1) ————————————————————————
//
// Palet ⌘K ile açılıyordu ve bu, bilmeyene GÖRÜNMEZ bir özellikti.
// Topbar'a eklenen görünür arama kutusu aynı motoru açıyor: tek
// davranış, iki tetik. Kanal modül düzeyinde çünkü tetik (Topbar) ile
// palet (App kökü) kardeş — aralarında prop geçirmek App'ten aşağı
// gereksiz bir kablo çekerdi.
type PaletteOpener = () => void;
const paletteOpeners = new Set<PaletteOpener>();

/** Görünür kapıdan (Topbar arama kutusu) paleti açar. */
export function openCommandPalette() {
  // Birden fazla palet mount edilmiş olsaydı hepsi açılırdı; uygulama
  // tek mount ediyor (App kökü) ve `Set` bunu zaten idempotent tutuyor.
  paletteOpeners.forEach(fn => fn());
}

function subscribeOpenPalette(fn: PaletteOpener): () => void {
  paletteOpeners.add(fn);
  return () => { paletteOpeners.delete(fn); };
}

export function CommandPalette() {
  const navigate = useNavigate();
  const t = useT();
  // v0.9.855 — pivot href'leri operatörün BAKTIĞI pencereyi taşısın diye
  // sayfanın aralığı (useUrlRange önceliğiyle) buradan çözülür.
  const { search: locationSearch } = useLocation();
  const { user } = useAuth();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [services, setServices] = useState<Result[]>([]);
  // Endpoint (operation) matches — server-debounced search, NOT an eager
  // catalogue (picker = server-side search; 10k+ operations can't ride a
  // client list). Refreshed per keystroke; cleared when the query is too
  // short or looks like a trace id. (UX pass #1.)
  const [endpoints, setEndpoints] = useState<Result[]>([]);
  // v0.7.89 — pinned + recently-viewed services, refreshed each open,
  // shown in the empty-query state as the pivot rotation.
  const [pivotSvcs, setPivotSvcs] = useState<Result[]>([]);
  const [selected, setSelected] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  // Param-prompt sub-mode (v0.5.457). When activeAction is set,
  // the palette stops showing the search results and starts
  // collecting per-param input for the chosen action. paramIdx
  // tracks which param we're on; paramValues accumulates answers.
  const [activeAction, setActiveAction] = useState<Action | null>(null);
  const [paramIdx, setParamIdx] = useState(0);
  const [paramValues, setParamValues] = useState<ParamValues>({});
  const [running, setRunning] = useState(false);
  // id-suggest sub-state (v0.5.459). Per-step typed query, the
  // current debounced result list, and which row is highlighted
  // for keyboard pick. Cleared between params + on reset.
  const [suggestQuery, setSuggestQuery] = useState('');
  const [suggestResults, setSuggestResults] = useState<SuggestItem[]>([]);
  const [suggestSelected, setSuggestSelected] = useState(0);
  const [suggestLoading, setSuggestLoading] = useState(false);

  // Global hotkey via the existing shortcut registry — Cmd-K on
  // Mac, Ctrl-K elsewhere. Registering through useShortcuts means
  // the binding shows up in the "?" help modal automatically and
  // the editable-target guard is handled centrally. `evenInInputs`
  // so an operator typing in a filter field can still pop the
  // palette without blurring first — Cmd-K is universally
  // expected to override.
  // Reset all transient state on every open so re-opening doesn't
  // resume mid-action from a previous session.
  const resetState = () => {
    setQuery('');
    setSelected(0);
    setActiveAction(null);
    setParamIdx(0);
    setParamValues({});
    setRunning(false);
    setSuggestQuery('');
    setSuggestResults([]);
    setSuggestSelected(0);
    setSuggestLoading(false);
  };

  // Debounced id-suggest fetch. When the current param is an
  // id-suggest, every keystroke on suggestQuery triggers a 200ms
  // debounced call to its suggest(q) function. Cancellation flag
  // protects against late responses racing a newer query.
  useEffect(() => {
    if (!activeAction) return;
    const cur = activeAction.params[paramIdx];
    if (!cur || cur.kind !== 'id-suggest' || !cur.suggest) return;
    const q = suggestQuery;
    let cancelled = false;
    setSuggestLoading(true);
    const t = window.setTimeout(async () => {
      try {
        const rows = await cur.suggest!(q);
        if (cancelled) return;
        setSuggestResults(rows);
        setSuggestSelected(0);
      } catch {
        if (!cancelled) setSuggestResults([]);
      } finally {
        if (!cancelled) setSuggestLoading(false);
      }
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [activeAction, paramIdx, suggestQuery]);

  useShortcuts([{
    keys: 'mod+k',
    label: 'Open command palette',
    group: 'Navigation',
    evenInInputs: true,
    handler: () => {
      setOpen(true);
      resetState();
    },
  }], []);

  // v0.9.1019 (G1) — DIŞARIDAN açma kanalı. Topbar'daki görünür arama
  // kutusu bu paleti açıyor; palet `open` durumunu kendi içinde tuttuğu
  // için dışarıya tek bir abonelik dikişi gerekiyordu.
  //
  // Neden sentetik ⌘K klavye olayı DEĞİL: o, kayıt defterinin
  // `evenInInputs` / kapsam / Esc-katmanı arbitrajını taklit etmeye
  // çalışmak olurdu ve taklit ilk kenar vakada ayrışır. Bu dikiş
  // paletin KENDİ açma yolunu çağırıyor — tek davranış, iki tetik.
  useEffect(() => subscribeOpenPalette(() => {
    setOpen(true);
    resetState();
  }), []);

  // v0.9.950 (E2/Ö28) — Esc KATMAN yığınında.
  //
  // Eski yorum "global dinleyici düzenlenebilir hedeflerde duraklar, bizim
  // input'umuz da düzenlenebilir" diyordu ve bu yüzden kendi document
  // dinleyicisini kuruyordu. Sonuç: bir çekmece açıkken paleti Esc'le
  // kapatmak ÇEKMECEYİ DE kapatıyordu (iki dinleyici, aynı olay).
  //
  // Katman kuralı odak tipine değil NİYETE bakıyor (keyboard.ts:
  // defaultPrevented), yani palet input'u artık istisna gerektirmiyor ve
  // yığın "en son açılan en üstte" diyor.
  useEscLayer(open, () => setOpen(false));

  // Focus the input + refresh the pivot rotation on open.
  useEffect(() => {
    if (!open) return;
    setTimeout(() => inputRef.current?.focus(), 10);
    // v0.7.89 — refresh the pivot rotation each open: pinned (★) first,
    // then recently-viewed (newest first), deduped. Each is a
    // one-keystroke jump back to a service the operator is working.
    const pinned = getPinnedServices();
    const recents = getRecentServices().filter(n => !pinned.includes(n));
    // v0.9.967/968 — PENCERESİZ, bilerek. Palet sonuçları navigate()'e
    // navHref üzerinden gidiyor (L477/L670) ve pencere KATMANI orada:
    // navHref yalnız `custom:` (fırçalanmış) aralığı taşır, göreli
    // preset'i taşımaz — çünkü preset'i URL'e çivilemek paylaşılan linki
    // "6h" diye dondurur ve alıcının sticky'sini ezerdi (v0.9.932'nin iki
    // bilinçli sınırı). Burada range yazmak o kararı geri alırdı: navHref
    // hedefte range varsa DOKUNMUYOR.
    const mkSvc = (name: string, hint: string): Result => ({
      kind: 'service', label: name, hint, to: serviceHref(name),
    });
    setPivotSvcs([
      ...pinned.map(n => mkSvc(n, '★ Pinned')),
      ...recents.map(n => mkSvc(n, 'Recent')),
    ]);
  }, [open]);

  // v0.8.518 (perf raporu #11) — servis araması SUNUCUDA. Eski eager
  // /api/services fetch'i 200 ile kesiyordu: 1400+ servisli prod'da
  // katalogun çoğu ⌘K'da hiç bulunamıyordu (fiilen ölü) ve her ilk
  // açılış tam liste indiriyordu. Endpoint aramasının deseniyle
  // (200ms debounce + stale-guard) autocomplete ucuna gider;
  // pinned/recent boş-sorgu rotasyonu aynen.
  useEffect(() => {
    const q = query.trim();
    if (!open || q.length < 2 || TRACE_ID_RE.test(q)) { setServices([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      api.serviceNames(q, 20)
        .then(r => {
          if (cancelled) return;
          setServices((r?.names ?? []).map(name => ({
            kind: 'service' as const,
            label: name,
            hint: 'Service',
            // Penceresiz — yukarıdaki mkSvc yorumu: katman navHref'te.
            to: serviceHref(name),
          })));
        })
        .catch(() => { if (!cancelled) setServices([]); });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [query, open]);

  // Endpoint search — server-debounced operation lookup so the palette can
  // jump to an endpoint, not just pages/services, WITHOUT eager-loading the
  // operation catalogue (10k+ ops; picker = server-side search constraint).
  // Fires only for a real ≥2-char query that isn't a trace id; each hit
  // jumps to that operation's traces. 200ms debounce + stale-guard mirror
  // the OperationPicker. (UX pass #1.)
  useEffect(() => {
    const q = query.trim();
    if (!open || q.length < 2 || TRACE_ID_RE.test(q)) { setEndpoints([]); return; }
    let cancelled = false;
    const t = window.setTimeout(() => {
      api.operationNames(undefined, q, 6)
        .then(r => {
          if (cancelled) return;
          // v0.9.855 (UX denetimi K4) — link `?operation=` taşıyordu:
          // /traces'in okuyucusu bu adı BİLMEZ ve State→URL efekti query
          // string'i beyaz-listeden sıfırdan kurduğu için param anında
          // SİLİNİYORDU. Sonuç: endpoint adını yazan operatör filtresiz,
          // alakasız bir liste görüyor ("arama bozuk"). Artık kesin
          // isim filtresi + pencere (pivotHref ailesi).
          setEndpoints((r?.names ?? []).map(name => ({
            kind: 'endpoint' as const,
            label: name,
            hint: 'Endpoint',
            to: operationTracesHref({ window: currentRange(locationSearch), operation: name }),
          })));
        })
        .catch(() => { if (!cancelled) setEndpoints([]); });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [query, open, locationSearch]);

  // Score: pages with the query as a prefix beat substring beat
  // fuzzy. Exact matches sort to the top. Hand-rolled rather than
  // pulling in fuzzysort — at this catalog size the diff is in
  // microseconds and the bundle stays lean.
  // v0.9.1270 (B5#1) — sayfa adları sidebar'la TEK kaynaktan: navKey'li
  // girişlerin etiketi t(navKey). Sidebar "Problems" derken paletin
  // "Inbox" demesi bitti; TR kataloğu da bedavaya geldi. Alias'lar eski
  // adları aranabilir tutar (skor saf çekirdekte: lib/paletteScore).
  const isAdmin = user?.role === 'admin';
  const localizedPages = useMemo(() => {
    const base = PAGES
      .filter(pg => !pg.adminOnly || isAdmin)
      .map(pg => (pg.navKey ? { ...pg, label: t(pg.navKey) } : pg));
    if (!isAdmin) return base;
    // v0.9.1272 (#3) — 23 Settings sekmesi aranabilir: "ayar ara…"
    // vaadi artık boş değil. Kaynak yaprak dizin (settings/tabIndex) —
    // bileşen import'u yok, bundle şişmez; alias = slug.
    const settings = SETTINGS_TAB_INDEX.map<Result>(tb => ({
      kind: 'page', label: `Settings · ${tb.label}`, aliases: [tb.slug],
      hint: 'Settings', to: `/settings/${tb.slug}`, adminOnly: true,
    }));
    return [...base, ...settings];
  }, [t, isAdmin]);
  const results = useMemo(() => {
    const q = query.trim().toLowerCase();
    const all = [...localizedPages, ...services];
    let scored: Result[];
    if (!q) {
      // empty query → pivot rotation (pinned ★ + recent services) first,
      // then the page catalog (v0.7.89).
      scored = [...pivotSvcs, ...localizedPages];
    } else {
      scored = all
        .map(r => ({ ...r, score: scorePaletteEntry(q, r.label, r.aliases) }))
        .filter(r => r.score && r.score > 0)
        .sort((a, b) => (b.score ?? 0) - (a.score ?? 0))
        .slice(0, 50);
    }
    // Action launcher results (v0.5.457). Verb-driven matches like
    // "ack" → Acknowledge problem float ABOVE navigation results
    // because the operator's intent when typing a verb is action,
    // not page nav. filterActions handles role-gating + ranking.
    const actionMatches = filterActions(user?.role, q).map<Result>(a => ({
      kind: 'action',
      label: a.label,
      hint: a.hint,
      action: a,
      score: 800,
    }));
    if (actionMatches.length > 0) {
      scored = [...actionMatches, ...scored];
    }
    // Trace-id shortcut — looks like 16-32 hex chars → offer a
    // direct jump. Trace IDs are commonly pasted from logs and
    // emails into this kind of search box.
    if (q && TRACE_ID_RE.test(q)) {
      scored = [
        { kind: 'trace', label: q, hint: 'Open trace', to: `/trace?id=${encodeURIComponent(q)}`, score: 999 },
        ...scored,
      ];
    }
    // Endpoint matches (already server-filtered by the query) appended after
    // the page/service hits so they're visible without crowding out an exact
    // page/service name match. (UX pass #1.)
    if (q && endpoints.length > 0) {
      scored = [...scored, ...endpoints];
    }
    return scored;
  }, [query, services, endpoints, pivotSvcs, user?.role, localizedPages]);

  // Reset cursor when results shrink/grow — otherwise the cursor
  // can point past the last row and Enter does nothing.
  useEffect(() => {
    if (selected >= results.length) setSelected(0);
  }, [results.length, selected]);

  // advanceParam — store the picked value, then either move to
  // the next param or fire run(). Shared by Enter (text input,
  // id-suggest pick) and chip clicks (duration).
  const advanceParam = (paramName: string, value: string | SuggestItem | number) => {
    if (!activeAction) return;
    const next = { ...paramValues, [paramName]: value };
    setParamValues(next);
    if (paramIdx + 1 < activeAction.params.length) {
      setParamIdx(paramIdx + 1);
      // Clear suggest sub-state for the next param.
      setSuggestQuery('');
      setSuggestResults([]);
      setSuggestSelected(0);
      // Re-focus the input (which becomes the next param's input
      // after re-render).
      setTimeout(() => inputRef.current?.focus(), 0);
    } else {
      // Last param — fire run with the final paramValues. We
      // pass `next` directly because the setParamValues from
      // above hasn't flushed by the time we'd read state.
      void runActionWithValues(next);
    }
  };

  // runActionWithValues — same as runActiveAction but reads the
  // values param explicitly so we don't depend on a state flush.
  const runActionWithValues = async (values: ParamValues) => {
    if (!activeAction || running) return;
    setRunning(true);
    try {
      const msg = await activeAction.run(values);
      toast.success(msg);
      setOpen(false);
      resetState();
    } catch (e) {
      const m = e instanceof Error ? e.message : String(e);
      toast.error(`${activeAction.label} failed: ${m}`);
      setRunning(false);
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (activeAction) {
      const cur = activeAction.params[paramIdx];
      if (!cur) return;
      // ArrowUp/Down navigates the suggest dropdown when id-suggest.
      if (cur.kind === 'id-suggest') {
        if (e.key === 'ArrowDown') {
          e.preventDefault();
          setSuggestSelected(s => Math.min(suggestResults.length - 1, s + 1));
          return;
        }
        if (e.key === 'ArrowUp') {
          e.preventDefault();
          setSuggestSelected(s => Math.max(0, s - 1));
          return;
        }
        if (e.key === 'Enter') {
          e.preventDefault();
          const picked = suggestResults[suggestSelected];
          if (!picked) return;
          advanceParam(cur.name, picked);
          return;
        }
        return;
      }
      // Text: Enter advances if the value passes the required
      // check. Empty + required → ignored.
      if (cur.kind === 'text' && e.key === 'Enter') {
        e.preventDefault();
        const raw = paramValues[cur.name];
        const val = typeof raw === 'string' ? raw.trim() : '';
        if (cur.required && !val) return;
        advanceParam(cur.name, val);
        return;
      }
      // Duration: chips are click-driven; Enter has no semantics
      // (operator should click a chip). Esc bubbles to the global
      // close handler.
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSelected(s => Math.min(results.length - 1, s + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSelected(s => Math.max(0, s - 1));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const r = results[selected];
      if (!r) return;
      if (r.kind === 'action' && r.action) {
        setActiveAction(r.action);
        setParamIdx(0);
        setParamValues({});
        // Defer focus to the param input — the existing inputRef
        // points at the query input; once activeAction flips, we
        // re-render and the same input becomes the param input,
        // so re-focusing keeps the cursor where the operator
        // expects.
        setTimeout(() => inputRef.current?.focus(), 0);
        return;
      }
      if (r.to) {
        navigate(navHref(r.to, locationSearch));
        setOpen(false);
      }
    }
  };

  if (!open) return null;
  return (
    <div onClick={() => setOpen(false)}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)',
        display: 'flex', justifyContent: 'center',
        alignItems: 'flex-start', paddingTop: '12vh',
        zIndex: 'var(--z-modal)',
      }}>
      <div onClick={e => e.stopPropagation()}
        onKeyDown={onKeyDown}
        style={{
          width: 'min(640px, 92vw)',
          maxHeight: '70vh',
          display: 'flex', flexDirection: 'column',
          background: 'var(--bg)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 10,
          boxShadow: '0 12px 48px rgba(0,0,0,0.5)',
        }}>
        {activeAction ? (
          // Param-prompt sub-mode (v0.5.457). Header shows the
          // action label + step pip (N/M params). Single input is
          // the current param; Enter advances or runs.
          <>
            <div style={{
              padding: '14px 16px',
              borderBottom: '1px solid var(--border)',
              display: 'flex', alignItems: 'center', gap: 10,
              background: 'var(--bg2)',
            }}>
              <span style={{
                fontSize: 10, padding: '2px 6px', borderRadius: 3,
                background: 'color-mix(in srgb, var(--accent) 18%, transparent)', color: 'rgb(56,139,253)',
                fontFamily: 'ui-monospace, monospace', fontWeight: 600,
              }}>action</span>
              <span style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>
                {activeAction.label}
              </span>
              {activeAction.params.length > 1 && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  {paramIdx + 1} / {activeAction.params.length}
                </span>
              )}
            </div>
            {(() => {
              const cur = activeAction.params[paramIdx];
              if (cur.kind === 'duration') {
                const opts = cur.durations ?? DEFAULT_DURATIONS;
                return (
                  <div style={{
                    padding: '14px 16px', display: 'flex', gap: 8,
                    alignItems: 'center', flexWrap: 'wrap',
                  }}>
                    <span style={{ fontSize: 12, color: 'var(--text2)', marginRight: 6 }}>
                      {cur.label}:
                    </span>
                    {opts.map(o => (
                      <Button key={o.label} variant="secondary" size="sm"
                        onClick={() => advanceParam(cur.name, o.seconds)}
                        disabled={running}>
                        {o.label}
                      </Button>
                    ))}
                  </div>
                );
              }
              if (cur.kind === 'id-suggest') {
                return (
                  <>
                    <input ref={inputRef}
                      value={suggestQuery}
                      onChange={e => setSuggestQuery(e.target.value)}
                      placeholder={cur.placeholder || cur.label}
                      disabled={running}
                      style={{
                        border: 'none', outline: 'none',
                        background: 'transparent', color: 'var(--text)',
                        padding: '14px 16px', fontSize: 14,
                      }} />
                    <div style={{
                      maxHeight: 240, overflowY: 'auto',
                      borderTop: '1px solid var(--border)',
                    }}>
                      {suggestLoading && suggestResults.length === 0 && (
                        <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
                          Searching…
                        </div>
                      )}
                      {!suggestLoading && suggestResults.length === 0 && (
                        <div style={{ padding: 12, fontSize: 11, color: 'var(--text3)' }}>
                          No matches. Type to search.
                        </div>
                      )}
                      {suggestResults.map((r, i) => (
                        <div key={r.id}
                          onMouseEnter={() => setSuggestSelected(i)}
                          onClick={() => advanceParam(cur.name, r)}
                          style={{
                            padding: '8px 16px', cursor: 'pointer',
                            display: 'flex', alignItems: 'center', gap: 10,
                            background: i === suggestSelected ? 'var(--bg2)' : 'transparent',
                            borderLeft: i === suggestSelected
                              ? '2px solid var(--accent2)' : '2px solid transparent',
                          }}>
                          <span style={{ fontSize: 13, flex: 1 }}>{r.label}</span>
                          {r.hint && (
                            <span style={{ fontSize: 11, color: 'var(--text3)' }}>{r.hint}</span>
                          )}
                        </div>
                      ))}
                    </div>
                  </>
                );
              }
              // 'text'
              const v = paramValues[cur.name];
              const text = typeof v === 'string' ? v : '';
              return (
                <input ref={inputRef}
                  value={text}
                  onChange={e => setParamValues({ ...paramValues, [cur.name]: e.target.value })}
                  placeholder={cur.placeholder || cur.label}
                  disabled={running}
                  style={{
                    border: 'none', outline: 'none',
                    background: 'transparent', color: 'var(--text)',
                    padding: '14px 16px', fontSize: 14,
                  }} />
              );
            })()}
            <div style={{
              padding: '10px 16px',
              fontSize: 11, color: 'var(--text3)',
              borderTop: '1px solid var(--border)',
              display: 'flex', justifyContent: 'space-between',
            }}>
              <span>
                {(() => {
                  if (running) return 'Running…';
                  const cur = activeAction.params[paramIdx];
                  const isLast = paramIdx + 1 >= activeAction.params.length;
                  if (cur.kind === 'duration') {
                    return isLast ? 'Click a duration · Esc cancel' : 'Click a duration to continue';
                  }
                  if (cur.kind === 'id-suggest') {
                    return isLast ? '↑↓ ↵ pick · Esc cancel' : '↑↓ ↵ pick to continue';
                  }
                  return isLast ? '↵ run · Esc cancel' : '↵ next';
                })()}
              </span>
              <LinkButton tone="muted"
                onClick={() => { resetState(); }}
                disabled={running}
                style={{ fontSize: 11 }}>
                ← back to search
              </LinkButton>
            </div>
          </>
        ) : (
        <>
        <input ref={inputRef}
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="Search or paste a trace id…"
          style={{
            border: 'none', outline: 'none',
            background: 'transparent', color: 'var(--text)',
            padding: '14px 16px', fontSize: 14,
            borderBottom: '1px solid var(--border)',
          }} />
        <div style={{ overflowY: 'auto', flex: 1 }}>
          {results.length === 0 && (
            <div style={{ padding: 16, color: 'var(--text3)', fontSize: 13 }}>
              No matches.
            </div>
          )}
          {results.map((r, i) => (
            <div key={`${r.kind}:${r.to ?? r.action?.id ?? i}`}
              onMouseEnter={() => setSelected(i)}
              onClick={() => {
                if (r.kind === 'action' && r.action) {
                  setActiveAction(r.action);
                  setParamIdx(0);
                  setParamValues({});
                  setTimeout(() => inputRef.current?.focus(), 0);
                  return;
                }
                if (r.to) { navigate(navHref(r.to, locationSearch)); setOpen(false); }
              }}
              style={{
                padding: '8px 16px',
                cursor: 'pointer',
                display: 'flex', alignItems: 'center', gap: 10,
                background: i === selected ? 'var(--bg2)' : 'transparent',
                borderLeft: i === selected
                  ? '2px solid var(--accent2)'
                  : '2px solid transparent',
              }}>
              <span style={{
                fontSize: 10, padding: '2px 6px', borderRadius: 3,
                background: r.kind === 'action' ? 'color-mix(in srgb, var(--accent) 18%, transparent)' : 'var(--bg3)',
                color: r.kind === 'action' ? 'rgb(56,139,253)' : 'var(--text2)',
                fontFamily: 'ui-monospace, monospace',
                minWidth: 56, textAlign: 'center',
                fontWeight: r.kind === 'action' ? 600 : 400,
              }}>
                {r.kind === 'trace' ? 'trace'
                 : r.kind === 'service' ? 'service'
                 : r.kind === 'endpoint' ? 'endpoint'
                 : r.kind === 'action' ? 'action'
                 : 'page'}
              </span>
              <span style={{ fontSize: 13, fontWeight: 500, flex: 1 }}>
                {r.label}
              </span>
              {r.hint && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>{r.hint}</span>
              )}
            </div>
          ))}
        </div>
        <div style={{
          padding: '6px 12px', borderTop: '1px solid var(--border)',
          fontSize: 11, color: 'var(--text3)',
          display: 'flex', gap: 16,
        }}>
          <span>↑↓ navigate</span>
          <span>↵ select</span>
          <span>esc close</span>
          <span style={{ marginLeft: 'auto' }}>{results.length} result{results.length === 1 ? '' : 's'}</span>
        </div>
        </>
        )}
      </div>
    </div>
  );
}
