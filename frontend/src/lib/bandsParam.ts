// bandsParam.ts — v0.10.170 (operatör: "Neden bantlar var" → "2 de olsun"):
// anomali bantları grafiklerde VARSAYILAN KAPALI; `?bands=1` açar. URL kaynak-
// doğru (paylaşılan link aynı görünümü açar; replace:true ile yazılır), foreign
// param'lar korunur, kapalı = param yok (temiz URL). Yalnız '1' açar — '0',
// 'true', 'yes' kapalıdır (tek yazım; gate-single-spelling dersi). Deploy ▼ ve
// Problem bantları bu anahtara BAĞLI DEĞİL — yalnız anomali bantları.
// Tüketiciler: Service Overview (chartRegions) ve /pod (podAnomalyRegions).
export const BANDS_PARAM = 'bands';

export function readBandsParam(sp: URLSearchParams): boolean {
  return sp.get(BANDS_PARAM) === '1';
}

export function writeBandsParam(prev: URLSearchParams, on: boolean): URLSearchParams {
  const next = new URLSearchParams(prev);
  if (on) next.set(BANDS_PARAM, '1'); else next.delete(BANDS_PARAM);
  return next;
}
