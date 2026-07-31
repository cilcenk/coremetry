// Typed localStorage wrapper (refactor 2026-07). Every ad-hoc
// `localStorage.*` access in src/ routes through here so private-mode
// windows, disabled storage, and quota errors can never crash a page:
// reads fall back, writes are best-effort. The inline boot scripts in
// index.html intentionally do NOT use this module (they run before the
// bundle) — keep them in sync by hand if a boot-critical key changes.
//
// Two API levels, chosen per call site to keep behaviour identical:
//   - getRaw/setRaw/removeRaw — plain strings. Use where the stored
//     value is a bare token ('dark', '1', a name). Existing stored
//     values are NOT valid JSON, so these sites must never migrate to
//     the JSON API (JSON.parse('dark') throws → silent fallback would
//     lose the user's setting).
//   - getItem<T>/setItem<T>  — JSON round-trip with a typed fallback.

/** Known storage keys. Dynamic families (dt.*) get helper builders. */
export const STORAGE_KEYS = {
  theme:            'coremetry-theme',
  density:          'coremetry-density',
  range:            'coremetry-range',
  env:              'coremetry-env',
  lang:             'coremetry.lang',
  rum:              'coremetry-rum',
  recentServices:   'coremetry.recentServices',
  pinnedServices:   'coremetry.pinnedServices',
  recentMetrics:    'coremetry.recentMetrics',
  // v0.9.301 — /traces histogram height. Dynatrace keeps the trace
  // list's overview chart a THIN brush strip so the table gets the
  // page; ours was a 140px headline that pushed rows below the fold.
  // Slim by default, expandable, and the choice sticks.
  tracesChartTall: 'coremetry.tracesChartTall',
  sidebarWidth:     'coremetry-sidebar-w',
  sidebarCollapsed: 'coremetry-sidebar-collapsed',
  sidebarGroups:    'coremetry-sidebar-groups',
  spanPanelWidth:   'coremetry-span-panel-w',
  exploreHistory:   'coremetry-explore-history',
  sqlBackend:       'coremetry-sql-backend',
  // v0.9.225 — son odaklanılan servis. Auto-pick tüm haritanın gelmesini
  // beklediği için ilk boyama gereksiz yere o sorguya bağlanıyordu; hatırlanan
  // odak, komşuluk sorgusunu harita daha yoldayken başlatır.
  topoFocus:        'coremetry-topo-focus',
  announcementDismissed: 'coremetry-announcement-dismissed', // v0.8.486 — kapatılan duyuru revizyonu
  finopsCostPerTbMo: 'coremetry.finops.costPerTbMo',
  inboxPrio:        'inbox.prio',
  inboxKind:        'inbox.kind',
  problemsSev:      'problems.sev',
  problemsPrio:     'problems.prio',
  svcHeatmapCollapsed: 'svc.heatmap.collapsed',
  svcChartsCompare:    'svc.charts.compare',
} as const;

/** DataTable persistence family: sort + column widths per table. */
export const dtSortKey = (storageKey: string) => `dt.${storageKey}.sort`;
export const dtWidthKey = (storageKey: string) => `dt.${storageKey}.widths`;

/** Chart-legend family (v0.9.483): "▶/▼ Series (N)" istatistik tablosunun
 *  açık/kapalı durumu, grafik başına. Görünürlük seçimi ayrı bir ailede
 *  (`cm.legendVis:` — lib/chart/legendVisibility.ts) yaşar; iki kayıt
 *  bilinçli AYRI: seri gizleme ile paneli kapatma farklı niyetler. */
export const legendCollapseKey = (storageKey: string) => `cm.legendCollapsed.${storageKey}`;

export function getRaw(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    // Storage disabled (private mode / iframe policy) — behave as unset.
    return null;
  }
}

export function setRaw(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Quota exceeded / storage disabled — chrome state is best-effort.
  }
}

export function removeRaw(key: string): void {
  try {
    localStorage.removeItem(key);
  } catch {
    // Same best-effort contract as setRaw.
  }
}

/** JSON read with typed fallback: unset, unparseable, or storage-
 *  disabled all yield `fallback` — a corrupt entry never crashes. */
export function getItem<T>(key: string, fallback: T): T {
  const raw = getRaw(key);
  if (raw === null) return fallback;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

/** JSON write, best-effort (see setRaw). */
export function setItem<T>(key: string, value: T): void {
  setRaw(key, JSON.stringify(value));
}
