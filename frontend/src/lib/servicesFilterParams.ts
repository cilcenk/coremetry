import { rebuildPreserving } from './urlState';

// servicesFilterParams — /services filtrelerinin URL karşılığı, SAF.
//
// v0.9.1336 (denetim K5). Sayfadan ayrı duruyor ki testlenebilsin: efektin
// kendisi (useEffect + setSearchParams + window.location) test edilemez, ama
// "hangi filtre hangi parametreye gider ve boş olan silinir mi" sorusu saf.
//
// SAHİPLİK = burada adı geçmek. rebuildPreserving'in sözleşmesi
// (lib/urlState.ts:154) bunu böyle tanımlıyor: listede adı geçen anahtar bu
// yazıcıya aittir ve boş değer o anahtarın SİLİNMESİ demektir; listede
// olmayan her şey olduğu gibi taşınır.
//
// TAŞINMASI HAYATİ olan yabancılar — hiçbiri burada YOK ve olmamalı:
//   · `page`   — setPage onu ham window.location.search üzerinden yazıyor
//                (Services.tsx:126). Sahiplenirsek her filtre yazımı sayfayı
//                sıfırlar; sahiplenmezsek dokunmayız.
//   · `s_*`    — DataTable primitifinin sıralama parametresi. v0.9.878'de
//                (K9) tam bu sınıf yüzünden sessizce siliniyordu: paylaşılan
//                sıralama linki kayboluyor, alıcı BAŞLIKTA p99 görüp
//                sunucuda count sıralaması alıyordu.
//   · `ai`     — AI çekmecesi (v0.9.477). Filtre düzenlemek çekmeceyi
//                kapatırdı.
//   · `env`, `range` — Topbar'ın global eksenleri (v0.8.383 / K4 vakası).
export interface ServicesFilterState {
  committedFilter: string;
  errorsOnly: boolean;
  minSpans: string;
  minP99: string;
  ownerTeam: string;
  sreTeam: string;
  cluster: string;
  namespace: string;
}

export function servicesFilterParams(
  f: ServicesFilterState,
): Array<[string, string]> {
  return [
    ['q',         f.committedFilter],
    ['err',       f.errorsOnly ? '1' : ''],
    ['minSpans',  f.minSpans],
    ['minP99',    f.minP99],
    // ownerTeam/sreTeam/cluster/namespace: bu dördü v0.9.1336'dan ÖNCE de
    // URL'den okunuyordu ama geri YAZILMIYORDU (tek-yön init). Ad değişmiyor
    // — mevcut derin linkler aynen çalışmaya devam ediyor.
    ['ownerTeam', f.ownerTeam],
    ['sreTeam',   f.sreTeam],
    ['cluster',   f.cluster],
    ['namespace', f.namespace],
  ];
}

/** servicesFilterSearch — mevcut sorgu dizesi + filtreler → yeni sorgu dizesi. */
export function servicesFilterSearch(
  prevSearch: string,
  f: ServicesFilterState,
): string {
  return rebuildPreserving(prevSearch, servicesFilterParams(f));
}
