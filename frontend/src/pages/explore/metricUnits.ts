// pages/explore/metricUnits.ts — metrik-kaynaklı sorgulara KATALOG
// birimini geç doldurma (v0.9.801).
//
// OPERATÖR RAPORU: Explore'da `avg(http.server.request.duration) by
// http.route` tooltip/lejant/eksende ÇIPLAK sayı basıyordu (0.348),
// olması gereken "348 ms".
//
// KÖK HALKA — birim zincirinin TEK dolduran ucu vardı:
//   • katalog: unit DOLU (ölçüldü — http.server.duration → 'ms')
//   • picker:  QueryRow.tsx onPick → { unit: m.unit }        ✅ dolduruyor
//   • codec:   urlCodec encode 'u' / decode q.u              ✅ taşıyor
//   • queryUnit → PanelData.unit → FieldConfig.unit          ✅ akıtıyor
//   • TOHUM YOLLARI: metricCatalogueHref (urlCodec.ts) ve
//     seedFromLegacyParams'ın ?metric= dalı ile panelToExplore'un metrik
//     dalı `blankQuery(letter,'metric')` kuruyor — unit: '' — ve bir daha
//     ASLA doldurulmuyordu.                                  ❌ KOPUK
//
// Yani operatör metriği picker'dan seçtiyse birim geliyor, /metrics
// kataloğundan · Overview route panelinden · Dependencies drill'inden ·
// dashboard panelinden · v0.9.801 ÖNCESİ paylaşılmış bir ?q= linkinden
// geldiyse gelmiyordu. Aradaki fark görünmez, sonuç sessizce yanlış.
//
// ONARIM: tohum yollarını tek tek yamamak yerine halkayı BİR yerde kapat —
// metriği olup birimi olmayan her metrik-kaynaklı sorgu için katalog
// birimini çöz ve state'e yaz. State'e yazmak URL'i de iyileştirir: bir
// sonraki ?q= yazımı 'u' alanını taşır, yani paylaşılan link artık
// birimli. Katalogda birim yoksa alan BOŞ kalır — uydurma yok.

import { useMemo } from 'react';
import { useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { keys } from '@/lib/queries/keys';
import type { MetricInfo } from '@/lib/types';
import type { BuilderState } from './model';

// Arama TAM ADLA yapılır; sunucu substring eşlemesi yaptığı için aynı adı
// ÖNEK kabul eden kardeşler de dönebilir (http.server.duration.sum gibi).
// 20 satır o kuyruğu fazlasıyla kapsar ve picker'ın 200'lük tavanının çok
// altında kalır (hard-constraint: picker = sunucu-taraflı arama).
const UNIT_LOOKUP_LIMIT = 20;

// metricsNeedingUnit — birimi eksik metrik-kaynaklı sorguların DISTINCT +
// SIRALI metrik adları. Sıralı: hook'un useQueries dizisi render'lar
// arasında kaymasın (react-query sıra-duyarlıdır).
export function metricsNeedingUnit(state: BuilderState): string[] {
  const out = new Set<string>();
  for (const q of state.queries) {
    if (q.source !== 'metric') continue;
    if (!q.metric || q.unit) continue;
    out.add(q.metric);
  }
  return Array.from(out).sort();
}

// unitFromCatalog — arama sonucundan TAM AD eşleşmesinin birimi.
// Substring eşleşmesi yeter sanmak yanlış olurdu: 'http.server.duration'
// araması 'http.server.duration.sum' da döndürür ve o kardeşin birimi
// başka olabilir.
export function unitFromCatalog(name: string, infos: ReadonlyArray<MetricInfo>): string {
  for (const m of infos) {
    if (m.name === name) return (m.unit ?? '').trim();
  }
  return '';
}

// withMetricUnits — çözülen birimleri state'e uygular. HİÇBİR sorgu
// değişmediyse AYNI referansı döndürür: setBuilder(b => withMetricUnits(b,
// u)) o durumda React'in bail-out'una düşer, yani boş çözüm sonsuz
// render/URL-yazım döngüsü kuramaz.
export function withMetricUnits(
  state: BuilderState, units: Readonly<Record<string, string>>,
): BuilderState {
  let changed = false;
  const queries = state.queries.map(q => {
    if (q.source !== 'metric' || !q.metric || q.unit) return q;
    const u = units[q.metric];
    if (!u) return q;
    changed = true;
    return { ...q, unit: u };
  });
  return changed ? { ...state, queries } : state;
}

// useMetricUnits — ad → HAM katalog birimi. Adı olmayan hiçbir istek
// atılmaz (boş dizi = sıfır fetch). Picker'ın kullandığı ENDPOINT'in
// aynısı (sunucu 30 sn cache'li), staleTime 5 dk: aynı metriği ikinci kez
// açmak ağa çıkmaz.
export function useMetricUnits(names: ReadonlyArray<string>): Record<string, string> {
  const results = useQueries({
    queries: names.map(name => ({
      queryKey: keys.explore.metricUnit(name),
      queryFn: () => api.metricNamesSearch('', name, UNIT_LOOKUP_LIMIT, 0),
      staleTime: 5 * 60_000,
    })),
  });
  // Çözülen çiftler önce DÜZ bir imzaya indirilir; useQueries her render'da
  // taze bir dizi döndürdüğü için sonucu doğrudan dep yapmak tüketiciyi
  // her hover'da yeniden çalıştırırdı (useExploreQueries'in aynı deseni).
  const pairs: [string, string][] = [];
  for (let i = 0; i < names.length; i++) {
    const d = results[i]?.data;
    if (!d) continue;
    const u = unitFromCatalog(names[i], d.names);
    if (u) pairs.push([names[i], u]);
  }
  const sig = pairs.map(([n, u]) => `${n}=${u}`).join('|');
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => Object.fromEntries(pairs), [sig]);
}
