// contextParams.ts — v0.10.250 ContextBar (audit §8/§10). SAF codec'ler;
// React yok. URL şeması (uzlaştırılan):
//   range=      kanonik zaman kanalı (useUrlRange yazar; buradan YAZILMAZ)
//   from= to=   yalnız GİRİŞTE takma ad: now-1h / now / epoch ms|s|ns ->
//               range'e çevrilir, URL'ye asla geri yazılmaz (ikinci pencere
//               kanalı = iki yazım kayması sınıfı)
//   env=        useUrlEnv (oturum yapışkan)
//   cluster=    span cluster değeri
//   namespace=  k8s namespace (ns= DEĞİL — /clusters çekmecesi ns'i kullanıyor;
//               ns yalnız rota izin verirse takma ad, salt-okur)
//   service=    servis adı
//   compare=    'prior' (compare=1 -> prior, takma ad, salt-okur)
// Boyut uygulanmıyorsa (applies dışı) okunmaz ve yazılmaz.

import type { TimeRange } from './types';

export type ContextDim = 'range' | 'env' | 'cluster' | 'namespace' | 'service' | 'compare';
export const CONTEXT_DIMS: ContextDim[] = ['range', 'env', 'cluster', 'namespace', 'service', 'compare'];

export interface ScopeParams {
  cluster: string;
  namespace: string;
  service: string;
  compare: '' | 'prior';
}

export const EMPTY_SCOPE: ScopeParams = { cluster: '', namespace: '', service: '', compare: '' };

/** parseNowExpr — 'now' | 'now-15m' | 'now-2h' | 'now-7d' -> ms offset (<=0); çöp -> null. */
export function parseNowExpr(s: string): number | null {
  const t = s.trim().toLowerCase();
  if (t === 'now') return 0;
  const m = /^now-(\d+)([smhd])$/.exec(t);
  if (!m) return null;
  const n = Number(m[1]);
  const unit = { s: 1_000, m: 60_000, h: 3_600_000, d: 86_400_000 }[m[2] as 's' | 'm' | 'h' | 'd'];
  return -n * unit;
}

/**
 * epochToMs — birim tespiti: ns (>=1e17), us (>=1e14), ms (>=1e11), s.
 * 2001-09-09'dan (1e12 ms / 1e9 s) sonra her birim ayrışır; çöp -> null.
 */
export function epochToMs(raw: string): number | null {
  if (!/^\d{9,20}$/.test(raw.trim())) return null;
  const n = Number(raw);
  if (!Number.isFinite(n) || n <= 0) return null;
  if (n >= 1e17) return Math.round(n / 1e6);
  if (n >= 1e14) return Math.round(n / 1e3);
  if (n >= 1e11) return n;
  return n * 1000;
}

/**
 * rangeFromAliases — from/to takma adlarından TimeRange. Her ikisi de
 * verilmeli; 'now-1h'+'now' -> { preset: '1h' }, epoch/rel çifti -> custom;
 * çöp/eksik/ters -> null (range'e dokunulmaz).
 */
export function rangeFromAliases(from: string | null, to: string | null, nowMs: number): TimeRange | null {
  if (!from || !to) return null;
  const fRel = parseNowExpr(from);
  const tRel = parseNowExpr(to);
  if (fRel !== null && tRel === 0) {
    const m = /^now-(\d+)([smhd])$/.exec(from.trim().toLowerCase());
    if (m) return { preset: `${m[1]}${m[2]}` };
  }
  const fMs = fRel !== null ? nowMs + fRel : epochToMs(from);
  const tMs = tRel !== null ? nowMs + tRel : epochToMs(to);
  if (fMs === null || tMs === null || tMs <= fMs) return null;
  return { preset: 'custom', fromMs: fMs, toMs: tMs };
}

/**
 * readScopeParams — sp'den uygulanan boyutları okur; uygulanmayan boyut ''.
 * allowNsAlias: rota ns='i takma ad olarak kabul ediyorsa (Traces ETMEZ —
 * /clusters çekmecesi ns'i sahipleniyor).
 */
export function readScopeParams(sp: URLSearchParams, applies: ReadonlySet<ContextDim>, allowNsAlias = false): ScopeParams {
  const get = (k: string) => (sp.get(k) ?? '').trim();
  const ns = applies.has('namespace') ? (get('namespace') || (allowNsAlias ? get('ns') : '')) : '';
  const cmpRaw = applies.has('compare') ? get('compare') : '';
  return {
    cluster: applies.has('cluster') ? get('cluster') : '',
    namespace: ns,
    service: applies.has('service') ? get('service') : '',
    compare: cmpRaw === 'prior' || cmpRaw === '1' ? 'prior' : '',
  };
}

/**
 * writeScopeParams — patch'i URL'ye yazar: '' siler, yabancı anahtarlar
 * korunur, takma adlar (from/to/ns/compare=1) ASLA yazılmaz; patch'te
 * olmayan boyut dokunulmaz. Uygulanmayan boyut yazılmaz (sessiz düşer).
 */
export function writeScopeParams(prev: URLSearchParams, patch: Partial<ScopeParams>, applies: ReadonlySet<ContextDim>): URLSearchParams {
  const next = new URLSearchParams(prev);
  const put = (k: ContextDim, v: string | undefined) => {
    if (v === undefined || !applies.has(k)) return;
    if (v) next.set(k, v); else next.delete(k);
  };
  put('cluster', patch.cluster);
  put('namespace', patch.namespace);
  put('service', patch.service);
  put('compare', patch.compare);
  if (patch.namespace !== undefined) next.delete('ns');
  return next;
}

/** contextSig — sahip olunan parametrelerin FNV-1a imzası (URL->state içe aktarım koruması). */
export function contextSig(parts: Array<string | number | undefined | null>): string {
  let h = 0x811c9dc5;
  const s = parts.map(p => (p == null ? '' : String(p))).join('');
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h.toString(16);
}
