// zoomHistory (Grafana-parite M1) — sayfa katmanının drag-zoom GERİ-YIĞINI
// saf çekirdeği. Çift-tık = bir adım geri (Grafana davranışı).
//
// Yığın EFEMER state'tir, URL'e YAZILMAZ: range yazımları replace:true
// olduğundan browser history zoom adımlarını zaten biriktirmiyor; yığını
// URL'e taşımak copy-link / SavedViews sözleşmesini zoom-geçmişiyle
// kirletirdi. Sayfa yenilenince yığın sıfırdan başlar — kabul edilen
// davranış (Grafana da öyle).
//
// Generic T: Service/Dashboard TimeRange iter; Explore lokal zoomWindow'u
// ({from,to} | null — ilk zoom'un öncesi "zoom yok"tur) iter.

const MAX_DEPTH = 32;

// pushZoom — zoom ÖNCESİ görünümü yığına iter (immutable). MAX_DEPTH
// aşımında en eski adım düşer — uzun zoom oturumunda sınırsız büyüme yok.
export function pushZoom<T>(stack: readonly T[], view: T): T[] {
  const next = [...stack, view];
  return next.length > MAX_DEPTH ? next.slice(next.length - MAX_DEPTH) : next;
}

// popZoom — son itilen görünümü çıkarır (immutable). Boş yığında view null:
// çağıran ya preset-default'a döner (zoom'luyken) ya da hiçbir şey yapmaz.
export function popZoom<T>(stack: readonly T[]): { stack: T[]; view: T | null } {
  if (stack.length === 0) return { stack: [], view: null };
  return { stack: stack.slice(0, -1), view: stack[stack.length - 1] };
}
