// pageContext.ts — v0.10.538 (CoSRE v2 Faz 3.1): sayfa bağlamı SAF serileştirici.
//
// Sohbet bugün yalnız {service, operation, rangeS, toMs, trace, env} gönderiyor
// ve servisi elle tutulan bir rota listesinden çözüyor (chatContext.
// serviceFromRoute — iki rota bir sürüm boyunca sessizce ıskalandı). Audit
// docs/audit/cosre-agent-v2.md §2.2: 11 alanın 9'u URL'den saf
// (pathname, search) fonksiyonuyla türetilebilir. Bu modül o fonksiyondur:
// tek rota tablosu, tek çıktı şekli, rota kapsama TESTİ (App.tsx'teki her
// rota tabloda; tablodaki her rota App.tsx'te) — eksik rota artık sessiz
// bağlamsızlık değil, kırmızı test.
//
// URL-dışı alanlar (görünür kolonlar, seçili chart serisi) burada YOK:
// onları sayfa yayınlar (Faz 3.x usePageContextPublisher). Filtreler üç
// kodekten (FilterExpr JSON, LogFilter tuple, skaler param) tek şekle
// {k, op, v[]} indirgenir — modele giden bağlam kodek bilmez.
import type { FilterExpr, FilterGroup, TimeRange } from './types';
import { decodeRange, decodeFilters, decodeFilterGroup } from './urlState';
import { parseFiltersParam } from './logFilters';

export type PageId =
  | 'home' | 'trace' | 'trace-compare' | 'traces' | 'service' | 'service-backtrace' | 'services'
  | 'problems' | 'anomalies' | 'inbox' | 'exceptions' | 'errors' | 'logs' | 'explore' | 'metrics'
  | 'clusters' | 'pod' | 'entity' | 'hosts' | 'rollouts' | 'events' | 'deployment-report'
  | 'endpoints' | 'endpoint' | 'databases' | 'database' | 'slow-queries' | 'statement'
  | 'dashboards' | 'dashboard' | 'service-map' | 'topology' | 'messaging' | 'external' | 'profiling'
  | 'slos' | 'alerts' | 'monitors' | 'watchers' | 'incidents' | 'incident' | 'runbooks' | 'runbook'
  | 'runbook-exec' | 'shift' | 'status' | 'system' | 'admin' | 'ai' | 'settings' | 'users' | 'profile'
  | 'login' | 'public-trace' | 'public-status' | 'unknown';

/** Normalleştirilmiş filtre: kodek bağımsız. op FilterOp ya da EXISTS / NOT EXISTS. */
export interface PageFilter { k: string; op: string; v: string[] }

export interface PageContext {
  page: PageId;
  path: string;
  env?: string;
  cluster?: string;
  namespace?: string;
  service?: string;
  workload?: string;
  pod?: string;
  operation?: string;
  traceId?: string;
  spanId?: string;
  problemId?: string;
  exceptionId?: string;
  /** Yalnız ?range= varsa (sessionStorage yedeği burada OKUNMAZ — saf). */
  timeRange?: TimeRange;
  activeFilters?: PageFilter[];
  search?: string;
}

// ── rota tablosu ─────────────────────────────────────────────────────────
// Anahtar = App.tsx path'i (param'lı rotalar prefix'iyle). pageContext.test.ts
// iki yönlü kapsama sorar; yeni rota buraya girmeden yeşil olmaz.
export const ROUTE_PAGES: Record<string, PageId> = {
  '/': 'home',
  '/trace': 'trace', '/trace/compare': 'trace-compare', '/traces': 'traces',
  '/service': 'service', '/service/backtrace': 'service-backtrace', '/services': 'services',
  '/problems': 'problems', '/anomalies': 'anomalies', '/inbox': 'inbox',
  '/exceptions': 'exceptions', '/errors': 'errors',
  '/logs': 'logs', '/explore': 'explore', '/metrics': 'metrics',
  '/clusters': 'clusters', '/pod': 'pod', '/entity': 'entity', '/hosts': 'hosts',
  '/rollouts': 'rollouts', '/events': 'events', '/deployment-report': 'deployment-report', '/deploys': 'rollouts',
  '/endpoints': 'endpoints', '/endpoint': 'endpoint',
  '/databases': 'databases', '/database': 'database', '/databases/slow-queries': 'slow-queries', '/databases/statement': 'statement',
  '/dashboards': 'dashboards', '/dashboard': 'dashboard',
  '/service-map': 'service-map', '/topology': 'topology', '/messaging': 'messaging', '/external': 'external', '/profiling': 'profiling',
  '/slos': 'slos', '/alerts': 'alerts', '/monitors': 'monitors', '/watchers': 'watchers',
  '/incidents': 'incidents', '/incident': 'incident',
  '/runbooks': 'runbooks', '/runbook': 'runbook', '/runbook-exec': 'runbook-exec',
  '/shift': 'shift', '/status': 'status', '/system': 'system', '/system/:tab': 'system',
  '/admin/:tab': 'admin', '/ai': 'ai', '/settings': 'settings', '/settings/:section': 'settings',
  '/users': 'users', '/profile': 'profile', '/login': 'login',
  '/public/trace': 'public-trace', '/public-status': 'public-status',
};

/** Bağlam TAŞIMAYAN sayfalar: yalnız page + path (kimlik/ayar/kabuk yüzeyleri). */
export const NO_CONTEXT_PAGES: ReadonlySet<PageId> = new Set<PageId>([
  'login', 'settings', 'admin', 'users', 'profile', 'ai', 'system', 'status', 'public-status', 'public-trace', 'dashboards',
]);

/** Servisi ?service= yerine başka param'dan okuyan sayfalar. */
const SERVICE_PARAM: Partial<Record<PageId, string[]>> = {
  'service': ['name', 'service'], 'service-backtrace': ['name', 'service'], 'service-map': ['focus'],
};
/** ?cluster= anlamı DEĞİŞEN sayfa: dashboard'da bir değişken adı olabilir, kapsam değil. */
const CLUSTER_IS_VARIABLE: ReadonlySet<PageId> = new Set<PageId>(['dashboard']);
/** Problem çekmecesi taşıyan sayfalar (?problem=, ?exc=). */
const PROBLEM_PAGES: ReadonlySet<PageId> = new Set<PageId>(['problems', 'anomalies', 'inbox', 'exceptions', 'errors', 'incident']);

export function pageIdFor(pathname: string): PageId {
  const p = pathname.replace(/\/+$/, '') || '/';
  if (ROUTE_PAGES[p]) return ROUTE_PAGES[p];
  for (const [route, id] of Object.entries(ROUTE_PAGES)) {
    if (route.includes('/:') && p.startsWith(route.slice(0, route.indexOf('/:')) + '/')) return id;
  }
  return 'unknown';
}

const get = (sp: URLSearchParams, k: string) => (sp.get(k) ?? '').trim();
type StringKey = 'env' | 'cluster' | 'namespace' | 'service' | 'workload' | 'pod' | 'operation' | 'traceId' | 'spanId' | 'problemId' | 'exceptionId' | 'search';
const put = (ctx: PageContext, k: StringKey, v: string) => { if (v) ctx[k] = v; };

function flatGroupFilters(g: FilterGroup | null): FilterExpr[] {
  if (!g) return [];
  const out: FilterExpr[] = [...(g.filters ?? [])];
  for (const sub of g.groups ?? []) out.push(...flatGroupFilters(sub));
  return out;
}

function exprFilters(sp: URLSearchParams): PageFilter[] {
  const out: PageFilter[] = decodeFilters(sp.get('filters')).map(f => ({ k: f.k, op: f.op, v: [...f.v] }));
  for (const f of flatGroupFilters(decodeFilterGroup(sp.get('filterGroup')))) out.push({ k: f.k, op: f.op, v: [...f.v] });
  return out;
}

function logFilters(sp: URLSearchParams): PageFilter[] {
  return parseFiltersParam(sp.get('filters')).filter(f => !f.disabled).map(f => {
    if (f.exists) return { k: f.key, op: f.negated ? 'NOT EXISTS' : 'EXISTS', v: [] };
    const vals = f.values && f.values.length ? f.values : [f.value];
    const op = f.op ?? (vals.length > 1 ? (f.negated ? 'NOT IN' : 'IN') : (f.negated ? '!=' : '='));
    return { k: f.key, op, v: vals };
  });
}

/** /entity?id=<type>:<cluster>/<namespace>/<name> — sunucu-sahipli opak kimlik; okunabilen kısım. */
function entityParts(id: string): { type: string; cluster: string; namespace: string; name: string } | null {
  const m = /^([a-z]+):([^/]+)\/([^/]*)\/(.+)$/.exec(id);
  return m ? { type: m[1], cluster: m[2], namespace: m[3], name: m[4] } : null;
}

// pageContext — (pathname, search) → bağlam. SAF: now(), storage, DOM okumaz.
export function pageContext(pathname: string, search: string): PageContext {
  const page = pageIdFor(pathname);
  const ctx: PageContext = { page, path: pathname };
  if (NO_CONTEXT_PAGES.has(page) || page === 'unknown') return ctx;
  const sp = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search);

  put(ctx, 'env', get(sp, 'env'));
  if (sp.has('range')) {
    const r = decodeRange(sp.get('range'), { preset: '' });
    if (r.preset) ctx.timeRange = r;
  }
  if (!CLUSTER_IS_VARIABLE.has(page)) {
    put(ctx, 'cluster', get(sp, 'cluster'));
    put(ctx, 'namespace', get(sp, 'namespace') || (page === 'clusters' ? get(sp, 'ns') : ''));
    for (const k of SERVICE_PARAM[page] ?? ['service']) { if (get(sp, k)) { ctx.service = get(sp, k); break; } }
  }

  switch (page) {
    case 'trace':
      put(ctx, 'traceId', get(sp, 'id')); put(ctx, 'spanId', get(sp, 'span'));
      break;
    case 'traces':
      put(ctx, 'traceId', get(sp, 'traceId')); put(ctx, 'search', get(sp, 'search'));
      ctx.activeFilters = exprFilters(sp);
      if (get(sp, 'hasError') === '1' || get(sp, 'hasError') === 'true') ctx.activeFilters.push({ k: 'status', op: '=', v: ['error'] });
      if (get(sp, 'rootOnly') && get(sp, 'rootOnly') !== 'false') ctx.activeFilters.push({ k: 'root', op: '=', v: ['true'] });
      break;
    case 'explore':
      ctx.activeFilters = exprFilters(sp);
      break;
    case 'logs':
      put(ctx, 'traceId', get(sp, 'traceId')); put(ctx, 'spanId', get(sp, 'spanId')); put(ctx, 'search', get(sp, 'q'));
      ctx.activeFilters = logFilters(sp);
      if (Number(get(sp, 'severity')) > 0) ctx.activeFilters.push({ k: 'severity', op: '>=', v: [get(sp, 'severity')] });
      break;
    case 'service': case 'service-backtrace':
      put(ctx, 'operation', get(sp, 'op'));
      break;
    case 'clusters':
      put(ctx, 'workload', get(sp, 'deployment')); put(ctx, 'search', get(sp, 'q'));
      break;
    case 'pod':
      put(ctx, 'pod', get(sp, 'pod')); put(ctx, 'workload', get(sp, 'deploy'));
      break;
    case 'entity': {
      const e = entityParts(get(sp, 'id'));
      if (e) {
        put(ctx, 'cluster', e.cluster); put(ctx, 'namespace', e.namespace);
        if (e.type === 'pod') ctx.pod = e.name; else if (e.type === 'workload' || e.type === 'deployment' || e.type === 'statefulset') ctx.workload = e.name;
      }
      break;
    }
    case 'endpoints': case 'inbox':
      put(ctx, 'search', get(sp, page === 'inbox' ? 'q' : 'search'));
      break;
    default:
      break;
  }
  if (PROBLEM_PAGES.has(page)) {
    put(ctx, 'problemId', get(sp, 'problem')); put(ctx, 'exceptionId', get(sp, 'exc') || get(sp, 'exception'));
  }
  if (ctx.activeFilters && ctx.activeFilters.length === 0) delete ctx.activeFilters;
  return ctx;
}
