// Pure decisions shared by DependenciesTable's three trend-column sites.
//
// v0.9.258. The Trend column is fed by /api/databases/trends →
// chstore.GetDBTrends → `FROM db_summary_5m`, and that MV is defined
// `WHERE db_system != ''` (internal/chstore/store.go:2513). A messaging
// span carries messaging.system and leaves db_system empty, so a queue
// row's join key (`kafka|orders-topic|`) can never appear in that table:
// on /messaging the column rendered the muted '—' 100% of the time while
// the page paid a LIMIT 200000 scan for it on every range change.
//
// This lives as one exported predicate rather than three inline
// `kind === 'db'` checks because the column definition, the fetch effect
// and the body <td> must agree EXACTLY. If they drift, the header and the
// row cells desync by one column — a silent layout corruption that no
// type error catches.
export type DepKind = 'db' | 'queue';

export function trendsEnabled(kind: DepKind): boolean {
  // v0.9.434 — queue yeniden AÇIK: v0.9.258'in kapattığı kusur "yanlış
  // MV (db_summary_5m) + boşa LIMIT 200000 taraması"ydı; artık kind
  // başına doğru endpoint var (/api/messaging/trends,
  // messaging_summary_5m). Fetch effect'i endpoint'i BU kind'a göre
  // seçer — tek taraflı yeniden-etkinleştirme sınıfı yine bu
  // fonksiyondan geçer.
  return kind === 'db' || kind === 'queue';
}

// latencyMissing — is there genuinely no latency measurement for this cell?
//
// v0.9.262. Two different absences, one honest answer ('—', never 0.0ms):
//
//  · 'receiver' rows come from discoverReceiverInstances, which reads
//    metric_points and carries no duration data whatsoever. The Go struct
//    fields are plain float64, so they marshal as 0 rather than being
//    omitted — the cell printed "0.0ms" and a database with zero application
//    traffic sorted to the top as the fastest thing on the page, flatly
//    contradicting the "receiver" badge rendered beside its own name.
//  · undefined means a warm cached payload from a backend older than the
//    field (rolling deploy).
//
// Zero is a legitimate measurement in principle, so this deliberately does
// NOT treat v === 0 as missing on span-derived rows — only the source tells
// us the value was never measured.
//
// Written as a type predicate (`v is number`) rather than a boolean
// `latencyMissing`, so the caller's else-branch narrows and can call
// `v.toFixed()` without a non-null assertion.
export function latencyPresent(
  source: 'spans' | 'receiver' | undefined,
  v: number | undefined,
): v is number {
  return source !== 'receiver' && v !== undefined;
}

// depRowKey — satırın KİMLİĞİ (v0.9.821).
//
// BUG: anahtar `system|cluster|name` idi ve DB tarafında `name`, satırın
// GÖRÜNEN etiketiydi (instance). Bir host üzerinde N veritabanı olan her
// kurulumda — canlı doğrulandı: oracle/oracle üzerinde 6, postgres
// üzerinde 9 — bütün o satırlar AYNI anahtarı üretiyordu. Sonuç iki
// katmanlı: bir satıra tıklamak altı çekmeceyi birden açıyordu ve
// kopyalanan ?row= linki hangi veritabanını kastettiğini taşımıyordu.
//
// db.name kimliğin ÜÇÜNCÜ alanı; messaging tarafında yok, o yüzden
// opsiyonel ve boş geçilince anahtar bayt-bayt eski hâlinde kalır
// (mevcut ?row= linkleri bozulmaz).
//
// TEK NÜSHA olması şart: anahtarı ÜRETEN (DependenciesTable satırı) ile
// URL'e YAZAN (Databases sayfası) ayrışırsa drawer hiç açılmaz — sessiz,
// tip hatası vermeyen bir kırılma.
export function depRowKey(r: {
  system: string; cluster?: string; instance?: string;
  destination?: string; dbName?: string;
}): string {
  const name = r.instance ?? r.destination ?? '';
  return `${r.system}|${r.cluster ?? ''}|${name}|${r.dbName ?? ''}`;
}
