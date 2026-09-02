// useContextParams.ts — v0.10.250 ContextBar (audit §10). Operatörün
// istediği konum (src/hooks/); depo konvansiyonu src/lib/ — tek dosya,
// yeniden konumlandırma tek satır import değişikliği.
//
// BİLEŞİM, KOPYA DEĞİL: range ← useUrlRange(defaultPreset) (sahiplik +
// oturum yapışkanlığı), env ← useUrlEnv() (oturum yapışkanlığı),
// cluster/namespace/service/compare ← useSearchParams + lib/contextParams
// saf codec'leri. set() tek setSearchParams(prev => …, { replace: true })
// (aralık setRange, env setEnv üzerinden — oturum/LS yazımı korunur).
// sig = sahip olunan parametrelerin FNV'si (Logs urlSig emsali) — sayfalar
// URL→state içe aktarımını bununla korur. windowNs useMemo([range])
// (v0.5.184: bare timeRangeToNs JSX'te sonsuz refetch). Global store YOK.
//
// from/to takma adları: yalnız GİRİŞTE, range yokken, BİR KEZ range'e
// çevrilir (setRange → range= yazılır, from/to silinir). from= /pod
// drillFrom'da, ns= /clusters çekmecesinde başka anlam taşıyor → takma
// adlar yalnız rota izin verirse (allowAliases) okunur.

import { useCallback, useEffect, useMemo, useRef } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useUrlRange } from '@/lib/useUrlRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { timeRangeToNs } from '@/lib/utils';
import { encodeRange } from '@/lib/urlState';
import type { TimeRange } from '@/lib/types';
import {
  readScopeParams, writeScopeParams, rangeFromAliases, contextSig,
  type ContextDim, type ScopeParams,
} from '@/lib/contextParams';

export interface ContextParams extends ScopeParams {
  range: TimeRange;
  env: string;
}

export interface ContextPatch extends Partial<ScopeParams> {
  range?: TimeRange;
  env?: string;
}

export interface UseContextParamsOptions {
  /** Sayfanın aralık varsayılanı (sahiplik). */
  defaultPreset: string;
  /** Sayfanın uyguladığı boyutlar; dışındakiler okunmaz/yazılmaz, çubukta devre dışı. */
  applies: ContextDim[];
  /** from/to (ve ns) takma adlarını girişte kabul et — rota bu anahtarları başka anlamda kullanmıyorsa. */
  allowAliases?: boolean;
}

export interface ContextParamsResult {
  params: ContextParams;
  applies: ReadonlySet<ContextDim>;
  set: (patch: ContextPatch) => void;
  /** Sahip olunan parametrelerin imzası — değişince sayfa URL→state içe aktarır. */
  sig: string;
  /** Pencere ns (useMemo([range])). */
  windowNs: { from: number; to: number };
}

export function useContextParams({ defaultPreset, applies, allowAliases = false }: UseContextParamsOptions): ContextParamsResult {
  const appliesSet = useMemo(() => new Set<ContextDim>(applies), [applies]);
  const [range, setRange] = useUrlRange(defaultPreset);
  const [env, setEnv] = useUrlEnv();
  const [sp, setSearchParams] = useSearchParams();

  // Giriş takma adları: bir kez, range yokken.
  const aliasDone = useRef(false);
  useEffect(() => {
    if (!allowAliases || aliasDone.current) return;
    if (sp.get('range')) return;
    const r = rangeFromAliases(sp.get('from'), sp.get('to'), Date.now());
    if (!r) return;
    aliasDone.current = true;
    setRange(r);
    setSearchParams(prev => {
      const next = new URLSearchParams(prev);
      next.delete('from');
      next.delete('to');
      next.set('range', encodeRange(r));
      return next;
    }, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [allowAliases]);

  const scope = useMemo(() => readScopeParams(sp, appliesSet, allowAliases), [sp, appliesSet, allowAliases]);
  const windowNs = useMemo(() => timeRangeToNs(range), [range]);

  const set = useCallback((patch: ContextPatch) => {
    if (patch.range !== undefined && appliesSet.has('range')) setRange(patch.range);
    if (patch.env !== undefined && appliesSet.has('env')) setEnv(patch.env);
    const { range: _r, env: _e, ...rest } = patch;
    if (Object.keys(rest).length === 0) return;
    setSearchParams(prev => writeScopeParams(prev, rest, appliesSet), { replace: true });
  }, [appliesSet, setRange, setEnv, setSearchParams]);

  const sig = useMemo(() => contextSig([
    appliesSet.has('range') ? encodeRange(range) : '',
    appliesSet.has('env') ? env : '',
    scope.cluster, scope.namespace, scope.service, scope.compare,
  ]), [appliesSet, range, env, scope]);

  const params = useMemo<ContextParams>(() => ({ ...scope, range, env }), [scope, range, env]);
  return useMemo(() => ({ params, applies: appliesSet, set, sig, windowNs }), [params, appliesSet, set, sig, windowNs]);
}
