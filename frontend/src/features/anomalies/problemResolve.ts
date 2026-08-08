// v0.9.825 — bildirim derin linkinin cache-first basamağı.
//
// Kendi modülünde çünkü tam olarak burada SESSİZ bir regresyon oldu ve
// hiçbir test onu tutmuyordu (problemLink.ts / problemLink.test.ts ile
// aynı gerekçe: küçük ve saf olan, ayrı dosyada ve testli durur).
//
// OLAN ŞUYDU: tarama `Problem[]` arıyordu. v0.9.455 liste yanıtını
// çıplak diziden {items,total,truncated} ZARFINA çevirdi. Array.isArray
// bir zarf için false döner — yani cache-first yolu o gün sessizce öldü.
// Derleme geçti, tipler geçti, ekran "çalışıyor" göründü; yalnız her
// derin link tek bir 200-satırlık listeye kaldı ve "görünen bir satır
// asla not-found'a düşemez" garantisi kâğıt üstünde kaldı.
import type { Problem } from '@/lib/types';

// problemRowsFrom — bir React Query cache girdisinden problem satırlarını
// çıkarır; girdide satır yoksa null.
//
// ŞEKLE TOLERANSLI olmak zorunda: 'problems' anahtar ağacının altında
// zarf ({items,…}) dışında sayaç ({count}), evaluator kalp atışı ve
// (v0.9.825'ten beri) tekil kayıt da yaşıyor. Tanımadığı her şeyi
// atlıyor — yeni bir şekil eklendiğinde patlamak yerine görmezden gelmek
// doğru davranış, çünkü bu yol bir OPTİMİZASYON; kaçırdığında bir
// basamak aşağısı (liste, sonra by-id) kaydı zaten buluyor.
export function problemRowsFrom(data: unknown): Problem[] | null {
  if (Array.isArray(data)) return data as Problem[];
  const items = (data as { items?: unknown } | null | undefined)?.items;
  if (Array.isArray(items)) return items as Problem[];
  return null;
}

// findProblemInCaches — yüklü cache girdilerinde kimliğe göre satır arar.
// Girdi listesi qc.getQueriesData()'nın döndürdüğü [key, data] çiftleri;
// React Query'ye bağımlılık taşımaması test edilebilir kalması için.
export function findProblemInCaches(
  entries: readonly (readonly [unknown, unknown])[],
  id: string,
): Problem | undefined {
  if (!id) return undefined;
  for (const [, data] of entries) {
    const rows = problemRowsFrom(data);
    if (!rows) continue;
    const hit = rows.find(x => x?.id === id);
    if (hit) return hit;
  }
  return undefined;
}
