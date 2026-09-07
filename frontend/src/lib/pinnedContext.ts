// pinnedContext.ts — v0.10.540 (CoSRE v2 Faz 3.2b): sabitlenmiş sayfa bağlamı
// (pin) saf yardımcıları. Operatör Problem sayfasında sohbeti başlatıp kanıt
// için /traces'a geçtiğinde özne kaybolmasın: bağlam sohbete sabitlenir, sunucu
// önsözde ÖNCE ve tam yazar (agentctx.PreambleTR), eski düz alanlar
// (service/operation/env/rangeS/toMs/trace) de pinden türetilir ki guided /
// drawer kademeleri sunucu değişikliği olmadan pini izlesin.
//
// Pin thread'e (oturum) bağlıdır, Redis çalışma setine DEĞİL (v0.10.487
// kullanıcılar arası sızıntı dersi); sessionStorage yalnız sayfa yenilemede
// hayatta kalması için, try/catch'li.
import type { PageContext } from './types';
import { timeRangeToNs } from './utils';

const NO_PIN: ReadonlySet<string> = new Set(['login', 'settings', 'admin', 'users', 'profile', 'ai', 'system', 'status', 'public-status', 'public-trace', 'dashboards', 'unknown']);

/** Sabitlenebilir mi: bağlamsız sayfa değil ve en az bir boyut taşıyor. */
export function hasPinnableContext(ctx: PageContext | null | undefined): boolean {
  if (!ctx || NO_PIN.has(ctx.page)) return false;
  return !!(ctx.cluster || ctx.namespace || ctx.service || ctx.workload || ctx.pod || ctx.operation
    || ctx.traceId || ctx.problemId || ctx.exceptionId || ctx.timeRange || ctx.activeFilters?.length || ctx.search);
}

/** Çip etiketi: "traces · api · c1/payments · 6h" — boş boyut yazılmaz. */
export function pinLabelTR(ctx: PageContext): string {
  const parts: string[] = [ctx.page];
  if (ctx.problemId) parts.push(`problem ${ctx.problemId}`);
  if (ctx.traceId) parts.push(`trace ${ctx.traceId.slice(0, 8)}…`);
  if (ctx.service) parts.push(ctx.service);
  if (ctx.operation) parts.push(ctx.operation);
  if (ctx.cluster || ctx.namespace) parts.push([ctx.cluster, ctx.namespace].filter(Boolean).join('/'));
  if (ctx.workload) parts.push(ctx.workload);
  if (ctx.pod) parts.push(ctx.pod);
  if (ctx.timeRange) parts.push(ctx.timeRange.preset === 'custom' ? 'özel aralık' : ctx.timeRange.preset);
  if (ctx.activeFilters?.length) parts.push(`${ctx.activeFilters.length} filtre`);
  return parts.join(' · ');
}

export interface LegacyContext {
  service?: string; operation?: string; trace?: string; env?: string; rangeS?: number; toMs?: number;
}

/** Eski düz alanlar pinden: guided/drawer kademeleri de pini izlesin. */
export function legacyFromPinned(ctx: PageContext): LegacyContext {
  const out: LegacyContext = {};
  if (ctx.service) out.service = ctx.service;
  if (ctx.operation) out.operation = ctx.operation;
  if (ctx.traceId) out.trace = ctx.traceId;
  if (ctx.env) out.env = ctx.env;
  if (ctx.timeRange) {
    const { from, to } = timeRangeToNs(ctx.timeRange);
    const s = Math.round((to - from) / 1e9);
    if (s > 0) out.rangeS = s;
    if (ctx.timeRange.preset === 'custom' && ctx.timeRange.toMs) out.toMs = ctx.timeRange.toMs;
  }
  return out;
}

const KEY = 'cm.aiPin';
export function readPin(): PageContext | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as PageContext;
    return v && typeof v.page === 'string' ? v : null;
  } catch { return null; }
}
export function writePin(ctx: PageContext | null): void {
  try {
    if (ctx) sessionStorage.setItem(KEY, JSON.stringify(ctx)); else sessionStorage.removeItem(KEY);
  } catch { /* storage kapalı: pin yalnız bellekte yaşar */ }
}
