// bandsParam.ts — v0.10.170 (operatör: "Neden bantlar var" → "2 de olsun"):
// anomali bantları grafiklerde VARSAYILAN KAPALI; `?bands=1` açar. URL kaynak-
// doğru (paylaşılan link aynı görünümü açar; replace:true ile yazılır), foreign
// param'lar korunur, kapalı = param yok (temiz URL). Yalnız '1' açar — '0',
// 'true', 'yes' kapalıdır (tek yazım; gate-single-spelling dersi). Deploy ▼ ve
// Problem bantları bu anahtara BAĞLI DEĞİL — yalnız anomali bantları.
// Tüketiciler: Service Overview (chartRegions) ve /pod (podAnomalyRegions).
//
// v0.10.171 (inceleme #6): react-router'ın `prev`'i BAYAT olabilir — aynı
// sayfada ham history.replaceState yazan var (CopilotExplain ?aicode=,
// aiSubject.ts); yalnız prev'den kopyalamak o param'ı sessizce düşürürdü.
// Ev deseni (useAiSubject.ts / insightRow.tsx): canlı location.search'ten
// tohumla, prev'de olup canlıda olmayanı ekle. `live` argümanı test için;
// çağıran window.location.search geçer. Adlandırma podPage.ts'in
// parse*/write* (yerinde mutasyon) çiftinden bilinçli ayrı: burada KOPYA döner
// çünkü setSearchParams(prev => …) prev'i mutasyona uğratmamalı.
export const BANDS_PARAM = 'bands';

export function readBandsParam(sp: URLSearchParams): boolean {
  return sp.get(BANDS_PARAM) === '1';
}

export function writeBandsParam(prev: URLSearchParams, on: boolean, live?: string): URLSearchParams {
  const next = new URLSearchParams(live || prev.toString());
  prev.forEach((v, k) => { if (!next.has(k)) next.append(k, v); });
  if (on) next.set(BANDS_PARAM, '1'); else next.delete(BANDS_PARAM);
  return next;
}
