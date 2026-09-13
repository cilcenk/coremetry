// depRowFilter.ts — v0.10.704 (operatör denetimi 2026-09-13, Databases →
// "Called from services" serbest metin filtresi).
//
// Predicate bugüne dek DependenciesTable'ın içindeydi ve db adına yalnız
// instance "unknown" ise bakıyordu: instance bilinen bir satırda db adı
// aramaya girmiyordu. Ayrıca sayfa başlığındaki "Called from services (N)"
// tablonun q/msys filtresini görmüyordu (sayı yazarken değişmiyordu).
// Tek üretici: tablo da sayfa sayacı da bu fonksiyonu kullanır.

export interface DepFilterRow {
  system: string;
  cluster?: string;
  instance?: string;
  destination?: string;
  dbName?: string;
  callers: string[];
}

// normalizeDepSearch — predicate'in gördüğü terim: kırpılmış, küçük harf.
// URL'deki ham değer input'ta aynen kalır; yalnız boşluktan oluşan değer
// parametre olarak YAZILMAZ (DependenciesTable setSearch).
export function normalizeDepSearch(raw: string | null | undefined): string {
  return (raw ?? '').trim().toLowerCase();
}

// depRowMatches — system seçicisi (tam eşleşme) VE metin terimi (alt-dize,
// harf duyarsız) — system / cluster / instance|destination / dbName /
// callers. dbName KOŞULSUZ: "COREBANK" araması instance bilinse de bulur.
export function depRowMatches(r: DepFilterRow, term: string, system: string): boolean {
  if (system && r.system !== system) return false;
  if (!term) return true;
  const inst = r.instance ?? r.destination ?? '';
  return r.system.toLowerCase().includes(term)
    || (r.cluster ?? '').toLowerCase().includes(term)
    || inst.toLowerCase().includes(term)
    || (r.dbName ?? '').toLowerCase().includes(term)
    || r.callers.some(c => c.toLowerCase().includes(term));
}
