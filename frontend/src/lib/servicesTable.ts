import type { Service } from './types';
import type { DataTableColumn, SortState } from './dataTable';

// servicesTable — pure column defs + URL-sort helpers for pages/Services.tsx
// (v0.8.251). /services was the primitive's resize-only adopter (v0.7.54)
// with a hand-rolled SortKey/NATURAL_DIR/SortTh header on the side; the page
// now runs useDataTable in serverSort mode instead, and everything the node
// vitest harness should pin — column ids that double as the backend's ?sort=
// keys, the old-link bridge, the ORDER BY sanitizer — lives here, out of the
// page file.

// SERVICE_COLS — column defs for the shared DataTable primitive. The page
// runs serverSort mode (clicking a header re-fetches a sorted page off
// service_summary_5m — CH does the ORDER BY before LIMIT/OFFSET, so the page
// reflects the GLOBAL rank), so each `sortValue` accessor is never invoked
// for ordering: it marks the column click-sortable and carries the naturalDir
// click semantics. Ids mirror the backend's ?sort= keys 1:1 — the same keys
// the pre-v0.8.251 SortKey union enumerated.
//
// Natural directions (unchanged from the old NATURAL_DIR map): name is
// alphabetical so 'asc'; apdex is a satisfaction score so 'asc' surfaces the
// WORST services first; the volume/latency columns keep the default 'desc'
// (biggest first).
export const SERVICE_COLS: DataTableColumn<Service>[] = [
  { id: 'name',      label: 'Service',    width: 280, sortValue: s => s.name, naturalDir: 'asc' },
  { id: 'spanCount', label: 'Spans',      width: 130, align: 'right', sortValue: s => s.spanCount },
  { id: 'errorRate', label: 'Error rate', width: 130, align: 'right', sortValue: s => s.errorRate },
  { id: 'avg',       label: 'Avg',        width: 120, align: 'right', sortValue: s => s.avgDurationMs },
  { id: 'p99',       label: 'P99',        width: 120, align: 'right', sortValue: s => s.p99DurationMs },
  // v0.9.1111 (Faz 5 "en çok kötüleşenler") — önceki pencereye göre göreli
  // p99 değişimi. Sıralama SUNUCUDA (sort=p99Delta aday-havuzu deseni;
  // backend compare'i zorlar); sortValue yalnız kolonu tıklanabilir kılar.
  // naturalDir desc = en çok kötüleşen önce.
  { id: 'p99Delta',  label: 'P99 Δ',      width: 110, align: 'right',
    sortValue: s => (s.priorP99Ms ?? 0) > 0 ? (s.p99DurationMs - (s.priorP99Ms as number)) / (s.priorP99Ms as number) : -Infinity },
  { id: 'apdex',     label: 'Apdex',      width: 100, align: 'right', sortValue: s => s.apdex, naturalDir: 'asc' },
  // v0.9.1317 (entity-model A2) — service_seen MV'sinden "Son görülme".
  //
  // sortValue YOK, ve bu bilinçli. Sayfa serverSort kipinde: bir başlık
  // tıklaması ?sort=<id> ile backend'e gider ve ORDER BY'ı CH,
  // LIMIT/OFFSET'ten ÖNCE service_summary_5m üzerinde uygular. lastSeen o
  // MV'de yok — ayrı bir tablodan, sayfalama BİTTİKTEN sonra Go tarafında
  // damgalanıyor. Kolonu tıklanabilir yapsaydık servicesAggSortExpr
  // bilinmeyen anahtarı sessizce `spans DESC`'e düşürürdü (whitelist'in
  // varsayılan dalı): başlıkta "Son görülme"ye göre sıralı görünen ama
  // aslında hacme göre sıralı bir tablo. Sıralanamayan kolon, yalan
  // söyleyen sıralamadan iyidir. (lib/dataTable.ts:12 — sortValue'suz
  // kolon zaten tıklanamaz; Endpoints 'trend'/'traces', Clusters, LogTable
  // 'time' aynı sınıf.)
  { id: 'lastSeen',  label: 'Son görülme', width: 130, align: 'right' },
];

// SORTABLE_SERVICE_COL_IDS — backend'in ?sort= olarak KABUL ETTİĞİ id'ler,
// yani sortValue taşıyan kolonlar. sanitizeServicesSort bunun üzerinden
// doğrular; salt SERVICE_COLS üyeliği v0.9.1317'den beri yeterli DEĞİL,
// çünkü artık listede backend'in ORDER BY'a çeviremeyeceği bir kolon var.
const SORTABLE_SERVICE_COL_IDS = new Set(
  SERVICE_COLS.filter(c => !!c.sortValue).map(c => c.id),
);

// The landing sort — span volume first (operator request, v0.8.259;
// error-rate-first had been the default since v0.3.0). Busiest
// services top the list; the error-rate column stays one click away.
export const DEFAULT_SERVICES_SORT: SortState = { id: 'spanCount', dir: 'desc' };

// sanitizeServicesSort — dt.sort → the ?sort=/&dir= pair /api/services
// accepts. The hook's state survives in localStorage + the URL, so a stale
// entry (an id from an older column schema, or a hand-edited link) can carry
// an id that isn't a Services column; falling back to the default pair keeps
// an unknown ORDER BY key from ever reaching the backend. Falls back as a
// PAIR — an unknown id's dir is just as untrusted as the id.
// v0.9.1317 — kapı ÜYELİK'ten SIRALANABİLİRLİK'e daraltıldı. Önceden
// "SERVICE_COLS'ta var mı" diye bakıyordu; o gün her kolon sıralanabilir
// olduğu için ikisi aynı kümeydi. 'lastSeen' bunu bozdu: listede var ama
// backend'in whitelist'inde yok, ve oradaki eşleşmeyen dal sessizce
// `spans DESC` üretiyor. Elle düzenlenmiş bir ?sort=lastSeen linki ya da
// eski bir localStorage kaydı tam olarak o sessiz yanlış sıralamayı
// verirdi.
export function sanitizeServicesSort(sort: SortState): { sort: string; dir: 'asc' | 'desc' } {
  if (sort.id && SORTABLE_SERVICE_COL_IDS.has(sort.id)) {
    return { sort: sort.id, dir: sort.dir };
  }
  return { sort: DEFAULT_SERVICES_SORT.id as string, dir: DEFAULT_SERVICES_SORT.dir };
}

// decodeLegacyServicesSort — back-compat bridge for pre-v0.8.251 links.
// Before the page adopted the primitive's `s_services` URL param, the
// deep-link shape for a sorted /services view was the backend's own
// `?sort=<col>&dir=<asc|desc>` pair. Decode that into a SortState the hook
// seeds from via urlSortFallback: it ranks BELOW `s_services` (new schema
// wins when both are present) but ABOVE the viewer's localStorage — a shared
// link's intent beats the recipient's personal default. Returns null when
// the legacy param is absent or names an unknown column; a missing /
// malformed dir falls back to the column's natural direction, matching what
// a header click on that column would have produced. READ-only: new writes
// always use `s_services`, so the old params age out of circulating links.
//
// v0.9.1317 — "unknown column" burada da SIRALANAMAYAN'ı kapsar. Bir
// legacy link zaten 'lastSeen' taşıyamaz (kolon o sürümden önce yoktu),
// ama elle yazılmış bir ?sort=lastSeen'i hook'un sort state'ine
// tohumlamanın hiçbir faydası yok: sanitizeServicesSort onu nasılsa
// varsayılana düşürür, arada UI sıralanamayan bir kolonu aktif sıralama
// sanmış olur. İki kapı aynı kümeyi kullansın.
export function decodeLegacyServicesSort(search: string): SortState | null {
  const p = new URLSearchParams(search);
  const id = p.get('sort');
  if (!id) return null;
  const col = SERVICE_COLS.find(c => c.id === id && !!c.sortValue);
  if (!col) return null;
  const dir = p.get('dir');
  return { id, dir: dir === 'asc' || dir === 'desc' ? dir : (col.naturalDir ?? 'desc') };
}
