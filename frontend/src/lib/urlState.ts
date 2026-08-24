import type { FilterExpr, FilterGroup, TimeRange } from './types';

// ─────────────────────────────────────────────────────────────────────────────
// Helpers for serialising Explore-style page state to/from the URL query
// string. Stable, human-readable where possible.
// ─────────────────────────────────────────────────────────────────────────────

/** Encode a TimeRange. Preset → `1h`. Custom → `custom:<fromMs>-<toMs>`. */
export function encodeRange(r: TimeRange): string {
  if (r.preset === 'custom' && r.fromMs && r.toMs) {
    return `custom:${r.fromMs}-${r.toMs}`;
  }
  return r.preset;
}

// windowRangeParam — v0.9.963. The one place a window becomes a `range=`
// value, for every cross-page link builder (serviceHref, stmtDetailHref, …).
//
// Two rules the builders kept re-deriving:
//   • ns → ms floors `from` and CEILS `to`, so rounding never NARROWS the
//     window. A truncated `to` drops the newest bucket, which on an event
//     window is the part the operator came to see.
//   • A window decodeRange would REJECT emits '' rather than a token. A
//     rejected `custom:` renders a confident window in the address bar while
//     the page silently draws the sticky one — the hardest failure to notice.
//     Reachable in practice: an event window's lead-in can push `from` below
//     the epoch on a degenerate timestamp.
export function windowRangeParam(w: TimeRange | { fromNs: number; toNs: number }): string {
  if ('preset' in w) {
    // v0.9.1355 — sınırsız `{preset:'custom'}` DELİĞİ. encodeRange bu şekle
    // çıplak `'custom'` literalini basıyor (yukarıdaki satır 11 dalına
    // giremez), decodeRange onu `{preset:'custom'}` olarak GERİ veriyordu ve
    // timeRangeToNs tanımadığı token'ı 86400 s'ye çözüyor (utils.ts:17-25).
    // Sonuç: adres çubuğunda kendinden emin bir `?range=custom` dururken
    // sayfa sessizce 24 SAAT çiziyor — bu fonksiyonun şerhinde tarif edilen
    // arızanın tam kendisi, ama şerhi yazan dal onu yakalamıyordu.
    // Reddedilen pencere '' döner ve çağıran paramı hiç yazmaz.
    if (w.preset === 'custom' && !(w.fromMs && w.toMs)) return '';
    return encodeRange(w);
  }
  const fromMs = Math.floor(w.fromNs / 1e6);
  const toMs = Math.ceil(w.toNs / 1e6);
  if (!(fromMs > 0) || !(toMs > fromMs)) return '';
  return encodeRange({ preset: 'custom', fromMs, toMs });
}

export function decodeRange(s: string | null | undefined, fallback: TimeRange): TimeRange {
  if (!s) return fallback;
  if (s.startsWith('custom:')) {
    const [from, to] = s.slice('custom:'.length).split('-').map(n => parseInt(n, 10));
    if (from > 0 && to > from) return { preset: 'custom', fromMs: from, toMs: to };
    return fallback;
  }
  // v0.9.1355 — çıplak `custom` (sınırsız). Kodlama tarafını kapatmak YETMEZ:
  // elle yazılmış / paylaşılmış bir `?range=custom` de aynı sessiz 24 saati
  // üretiyordu. Ve bu token'ın kaynağı kullanıcı hatası DEĞİL, uygulamanın
  // kendisi — encodeRange sınırsız bir custom'ı tam olarak buna çeviriyor.
  // Çözümlenebilir bir pencere olmadığı için fallback'e düşer; okuyucunun
  // sözleşmesi "çözümlenebilir bir aralık döndür", "token'ı aynen taşı" değil.
  //
  // KAPSAM BİLİNÇLİ DAR: tanınmayan DİĞER token'lar (`?range=90m`) aynen
  // geçmeye devam ediyor — shareUrl.test.ts:74 o toleransı sınanmış davranış
  // olarak çiviliyor ve ayrı bir karar. Fark şu: `90m` görünür biçimde
  // yabancı, `custom` ise uygulamanın kendi ürettiği ve geri okuyabildiği
  // için GEÇERLİ görünen bir token — sessizliği yapan da bu tutarlılık.
  if (s === 'custom') return fallback;
  return { preset: s };
}

/** Encode FilterExpr[] as compact JSON. */
export function encodeFilters(f: FilterExpr[]): string {
  return f.length ? JSON.stringify(f) : '';
}

export function decodeFilters(s: string | null | undefined): FilterExpr[] {
  if (!s) return [];
  try {
    const v = JSON.parse(s);
    return Array.isArray(v) ? (v as FilterExpr[]) : [];
  } catch {
    return [];
  }
}

// ── Grouped AND/OR builder codec (v0.8.x trace-query gap-2) ───────────────────
// FilterGroup is the additive, default-off upgrade. A group is "flat-AND" —
// indistinguishable from the legacy FilterExpr[] path — when its join is AND
// and it has no nested groups. encodeFilterGroup returns '' for a flat-AND
// group so the URL keeps using the legacy `filters=` param (back-compat:
// existing saved views / shared URLs are byte-identical); only a genuine OR /
// nested group serialises to the `filterGroup=` param.

/** True when the group adds nothing beyond a legacy flat-AND chip row. */
export function isFlatAndGroup(g: FilterGroup | null | undefined): boolean {
  if (!g) return true;
  if (g.groups && g.groups.length > 0) return false;
  return (g.join ?? 'AND') === 'AND';
}

/**
 * Encode a FilterGroup for the `filterGroup=` URL param. Returns '' when the
 * group is flat-AND (the flat `filters=` param carries it instead) or empty,
 * so the grouped param only appears for real OR / nested queries.
 */
export function encodeFilterGroup(g: FilterGroup | null | undefined): string {
  if (!g) return '';
  if (isFlatAndGroup(g)) return '';
  // Strip empty leaf/group noise so the URL stays compact + stable.
  const filters = (g.filters ?? []).filter(f => f.k && f.k.trim());
  const groups = (g.groups ?? [])
    .map(sub => ({ join: sub.join ?? 'AND', filters: (sub.filters ?? []).filter(f => f.k && f.k.trim()) }))
    .filter(sub => sub.filters.length > 0);
  if (filters.length === 0 && groups.length === 0) return '';
  const out: FilterGroup = { join: g.join ?? 'AND', filters };
  if (groups.length > 0) out.groups = groups;
  return JSON.stringify(out);
}

/** Decode the `filterGroup=` URL param; null when absent / malformed. */
export function decodeFilterGroup(s: string | null | undefined): FilterGroup | null {
  if (!s) return null;
  try {
    const v = JSON.parse(s);
    if (!v || typeof v !== 'object' || !Array.isArray(v.filters)) return null;
    const g: FilterGroup = {
      join: v.join === 'OR' ? 'OR' : 'AND',
      filters: v.filters as FilterExpr[],
    };
    if (Array.isArray(v.groups) && v.groups.length > 0) {
      g.groups = (v.groups as unknown[])
        .map(sub => {
          const o = sub as { join?: string; filters?: unknown };
          return {
            join: o.join === 'OR' ? 'OR' : 'AND',
            filters: Array.isArray(o.filters) ? (o.filters as FilterExpr[]) : [],
          } as FilterGroup;
        })
        .filter(sub => sub.filters.length > 0);
    }
    return g;
  } catch {
    return null;
  }
}

/** Build a URLSearchParams, omitting empty/default values. */
export function buildQuery(entries: Array<[string, string | number | undefined | null | false]>): string {
  const u = new URLSearchParams();
  for (const [k, v] of entries) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    u.set(k, String(v));
  }
  return u.toString();
}

// rebuildPreserving — v0.9.940 (UX denetimi A7). buildQuery'nin, sorgu
// dizesini SIFIRDAN kuran efektler için doğru olan biçimi.
//
// SORUN: Traces ve Explore URL'i her state yazımında baştan kuruyor. Bu,
// listede OLMAYAN her parametreyi bir render sonra SİLMEK demek — kim
// yazmış olursa olsun. Sınıf üç kez doğdu ve her seferinde tek tek
// yamandı:
//
//   • v0.8.383 (K4) — Topbar'ın `?env=`i Traces'te her yerel yazımda
//     siliniyordu; çözüm: env'i Traces'in listesine EKLEMEK.
//   • v0.9.878 (K9) — DataTable primitifinin yazdığı `?s_traces-agg`
//     yazıldığı an siliniyordu; paylaşılan sıralama linki sessizce
//     kayboluyor, alıcı BAŞLIKTA p99 görüp sunucuda count sıralaması
//     alıyordu. Çözüm: onu da listeye eklemek.
//   • Üçüncüsü henüz doğmamıştı ama adayı belliydi: `?ai=` (AI çekmecesi,
//     v0.9.477). Explore'da bir filtre düzenlemek çekmeceyi KAPATIRDI.
//
// Her seferinde çözüm "bir tane daha ekle" oldu; oysa kusur listede
// değil, listenin TEK OTORİTE sayılmasındaydı. Bu fonksiyon varsayımı
// tersine çeviriyor: efekt yalnız KENDİ parametrelerine sahiptir,
// tanımadıklarını olduğu gibi taşır.
//
// SAHİPLİK = `entries`te ADI GEÇMEK. Boş değerli bir girdi, o anahtarın
// SİLİNMESİ demektir (buildQuery semantiği aynen korunuyor) — sahip
// olduğun bir parametreyi temizleyebilmek şart.
//
// SIRA KARARLI ve bu ZORUNLU: iki çağıran da sonucu
// `window.location.search` ile karşılaştırıp öyle yazıyor. Sahip olunan
// parametreler `entries` sırasında ÖNCE gelir (yabancı param yokken çıktı
// buildQuery ile BAYT BAYT aynı — mevcut linkler değişmez), yabancılar
// prev'deki göreli sıralarıyla sonra. Böylece fonksiyon idempotent: ikinci
// çağrı aynı dizeyi üretir, iki yazıcı birbirini yeniden sıralamaz.
export function rebuildPreserving(
  prev: string,
  entries: Array<[string, string | number | undefined | null | false]>,
): string {
  const owned = new Set(entries.map(([k]) => k));
  const u = new URLSearchParams();
  for (const [k, v] of entries) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    u.set(k, String(v));
  }
  // append (set değil): tekrarlanan yabancı parametreler korunur.
  for (const [k, v] of new URLSearchParams(prev).entries()) {
    if (owned.has(k)) continue;
    u.append(k, v);
  }
  return u.toString();
}
