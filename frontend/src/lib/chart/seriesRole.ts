// seriesRole — seri renginin TEK karar noktası (FAZ 2, CorePanel sözleşmesi).
//
// Spec şartı: "Deterministik renk: aynı servis/endpoint her chart'ta aynı
// renkte. Rastgele palet döngüsü yok. Hata kırmızı, başarı yeşil sabit."
//
// Deterministik yarı ZATEN VARDI: chartFmt.seriesColor (FNV-1a → sabit
// palet, v0.8.x'ten beri her chart'ta aynı etiket aynı renk). Burada onu
// YENİDEN YAZMIYORUZ — eksik olan yalnız SEMANTİK rol katmanı.
//
// Rol ETİKETTEN TAHMİN EDİLMEZ, çağıran açıkça söyler. "error" içeren her
// etiketi kırmızıya boyamak tuzak: "error-budget-service" diye bir servis
// pekâlâ olur ve o bir VERİ serisidir, hata serisi değil. Hangi serinin
// hata/başarı olduğunu yalnız sorguyu kuran bilir.
//
// Semantik roller TOKEN döndürür (var(--err)/var(--ok)) — tema-canlı;
// draw anında resolveVar çözer (canvas CSS var okuyamaz). Palet hex'i
// bilinçli tema-bağımsız (bugünkü davranış; seriesColor sözleşmesi).

import { seriesColor } from '@/lib/chartFmt';

export type SeriesRole = 'data' | 'error' | 'success' | 'muted';

export function seriesRoleColor(label: string, role: SeriesRole = 'data'): string {
  switch (role) {
    case 'error':   return 'var(--err)';
    case 'success': return 'var(--ok)';
    case 'muted':   return 'var(--text3)';
    case 'data':    return seriesColor(label);
  }
}
