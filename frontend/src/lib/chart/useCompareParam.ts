// useCompareParam.ts — v0.10.315 (chart-layer Dilim 2.3b): ?compare= URL
// sözleşmesinin TEK React sarmalayıcısı (v0.10.311'de ServiceCharts içinde
// doğdu; Pod da kullanınca hook'a çıktı). URL kaynak; seedKey verilirse
// localStorage yalnız TOHUM (URL'de anahtar yokken okunur ve URL'ye yazılır —
// useUrlRange oturum yapışkanlığı emsali) ve her seçim oraya da yazılır.
// Yazım replace:true, yabancı anahtarlar korunur; içe aktarım yalnız
// compare anahtarına bağlı (aralık yazımı state'i silmez).
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { getRaw, setRaw } from '@/lib/storage';
import { type CompareMode, parseCompareParam, encodeCompareParam, parseStoredCompare } from './compareParam';

export function useCompareParam(opts: { seedKey?: string } = {}): [CompareMode, (m: CompareMode) => void] {
  const { seedKey } = opts;
  const [searchParams, setSearchParams] = useSearchParams();
  const urlCompare = parseCompareParam(searchParams.get('compare'));
  const seed = (): CompareMode => (seedKey ? parseStoredCompare(getRaw(seedKey)) : 'off');
  const [compare, setCompare] = useState<CompareMode>(() => urlCompare ?? seed());
  useEffect(() => {
    if (urlCompare === null) {
      const s = seed();
      if (s !== 'off') {
        setSearchParams(prev => {
          const n = new URLSearchParams(prev);
          n.set('compare', encodeCompareParam(s));
          return n;
        }, { replace: true });
      } else {
        setCompare(c => (c === 'off' ? c : 'off'));
      }
      return;
    }
    setCompare(c => (c === urlCompare ? c : urlCompare));
    // seed() yalnız seedKey'e bağlı; effect anahtarı compare paramı.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [urlCompare, seedKey, setSearchParams]);
  const set = (m: CompareMode) => {
    setCompare(m);
    if (seedKey) setRaw(seedKey, m);
    setSearchParams(prev => {
      const n = new URLSearchParams(prev);
      const v = encodeCompareParam(m);
      if (v) n.set('compare', v); else n.delete('compare');
      return n;
    }, { replace: true });
  };
  return [compare, set];
}
