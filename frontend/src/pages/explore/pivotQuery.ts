// pivotQuery.ts — GroupTable satırından builder sorgusuna pivot (v0.9.848).
//
// SAF. React yok, fetch yok: karar bir BuilderQuery alıp bir BuilderQuery
// döndürmekten ibaret, o yüzden tabloyla test edilir ve URL-state'i kendisi
// hiç bilmez (builder zaten her düzenlemede ?q= yazıyor).
//
// NE ÇÖZÜYOR: bir panelde 40 seri var, biri fırlamış. Bugüne dek operatörün
// yolu şuydu — etiketi gözüyle okumak, filtre çipi eklemek, anahtarı elle
// yazmak, değeri elle yazmak. Honeycomb/Datadog'da bu tek tıktır: satırdan
// "yalnız bu değer" (daralt) ya da "bunu hariç tut" (gürültüyü at).
//
// İKİ KİP, İKİ FARKLI SPLIT DAVRANIŞI — ve bu bilinçli:
//
//   only     `k = v` çipi eklenir VE k splitBy'dan ÇIKARILIR. Daralttıktan
//            sonra o boyut artık tek değerlidir; splitte bırakmak paneli tek
//            serilik bir "fan-out"a çevirir ve lejantta okunacak hiçbir şey
//            kalmaz. Honeycomb'un daraltma jesti de aynen böyle davranır.
//
//   exclude  `k != v` çipi eklenir, splitBy AYNEN KALIR. Burada niyet
//            "gürültüyü at, KALANI karşılaştırmaya devam et" — boyutu
//            splitten düşürmek tam tersini yapardı.
//
// EXCLUDE YALNIZ TEK BOYUTLU SATIRDA: splitBy iki anahtar taşıyorsa bir satır
// (k1=v1 VE k2=v2) kombinasyonudur, ve o kombinasyonun DEĞİLİ düz AND'li
// çiplerle yazılamaz — `k1!=v1 AND k2!=v2` satırdan çok daha fazlasını eler.
// Yaklaşık bir filtre üretip doğruymuş gibi göstermektense kipi kapatıyoruz
// (çağıran düğmeyi disabled çizer, sebebi title'da).
//
// GRUPLU FİLTRE AÇIKSA PİVOT YOK: hasGroupedFilter(q) ise fetch'e giden şey
// filterGroup'tur, düz `filters` değil (model.effectiveFilterGroup). Düz bir
// çip eklemek ekranda görünür ama SORGUYA HİÇ GİRMEZDİ — sessiz yalan. OR
// join'li bir gruba leaf eklemek ise daraltma değil GENİŞLETME olurdu.

import type { FilterExpr } from '@/lib/types';
import { type BuilderQuery, type PivotPair, hasGroupedFilter } from './model';

export type PivotMode = 'only' | 'exclude';

// eqChip — bu çip TAM olarak `k <op> v` mi? Çok değerli (IN) çipler asla
// eşleşmez: onların bir tek değeri yoktur, dolayısıyla pivotun ürettiği
// tekil çiple çelişmezler de.
function isChip(f: FilterExpr, k: string, op: FilterExpr['op'], v: string): boolean {
  return f.k === k && f.op === op && f.v.length === 1 && f.v[0] === v;
}

function sameFilters(a: FilterExpr[], b: FilterExpr[]): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

// pivotQuery — pivotlanmış sorgu, ya da null.
//
// null ÜÇ ayrı "olmaz" için döner ve üçü de çağıran için aynı anlama gelir:
// state'e DOKUNMA. (a) uygulanamaz (gruplu filtre / boş çift / çok boyutlu
// exclude), (b) sonuç bugünküyle BİREBİR aynı — yani çip zaten orada.
//
// (b) önemsiz bir incelik değil: aynı çipi silip sonuna yeniden eklemek
// `filters` dizisinin SIRASINI değiştirir, querySignature o diziyi olduğu
// gibi serialize eder (model.querySignature) ve sonuç, veri hiç değişmemişken
// garanti bir cache MISS + yeni bir fan-out olurdu.
export function pivotQuery(
  q: BuilderQuery, pairs: PivotPair[], mode: PivotMode,
): BuilderQuery | null {
  if (hasGroupedFilter(q)) return null;
  if (pairs.length === 0) return null;

  if (mode === 'exclude') {
    if (pairs.length !== 1) return null;
    const { k, v } = pairs[0];
    if (q.filters.some(f => isChip(f, k, '!=', v))) return null;  // zaten hariç
    // Çelişen `k = v` çipi düşer: aynı anda "yalnız bu" ve "bu hariç" hiçbir
    // satır döndürmez, ve boş panelin sebebi hiçbir yerde yazmazdı.
    const filters: FilterExpr[] = [
      ...q.filters.filter(f => !isChip(f, k, '=', v)),
      { k, op: '!=', v: [v] },
    ];
    return { ...q, filters };
  }

  // only — her çift için tekil `=` çipi; aynı anahtardaki eski `=` çipi ve
  // aynı (anahtar,değer) üzerindeki `!=` çipi düşer.
  let filters = q.filters;
  for (const { k, v } of pairs) {
    filters = [
      ...filters.filter(f => !(f.k === k && (f.op === '=' || isChip(f, k, '!=', v)))),
      { k, op: '=' as const, v: [v] },
    ];
  }
  const keys = new Set(pairs.map(p => p.k));
  const splitBy = q.splitBy.filter(k => !keys.has(k));
  if (sameFilters(filters, q.filters) && splitBy.length === q.splitBy.length) return null;
  return { ...q, filters, splitBy };
}
