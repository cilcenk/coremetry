// endpointHonesty — /endpoints sayfasının DÜRÜSTLÜK yardımcıları (v0.9.818).
//
// Bu dosyadaki her şey SAF: tablo-güdümlü test (endpointHonesty.test.ts)
// bunları canlı bir sayfa kurmadan sabitliyor. Sayfa yalnız çiziyor.
//
// Dört ayrı yalan sınıfı burada kapanıyor:
//
//   1. "YENİ" — prior okuma da top-N. Önceki pencerede VAR OLUP listeye
//      girmemiş bir endpoint, prior alanı boş geldiği için "yeni" diye
//      etiketleniyordu. Doğru cümle "bu LİSTEDE yeni".
//   2. KPI kapsamı — karolar görünen satırların toplamıydı ama etiket
//      ("Total calls") filo iddiasında bulunuyordu.
//   3. URL kapsamı — sparkline modalı ve genişletilmiş satırlar yalnız
//      bileşen state'inde yaşıyordu; kopyalanan link onları taşımıyordu
//      (v0.8.253 sınıfı).
//   4. Kaynak değişimi — env/cluster seçiliyken okuma MV'den HAM spans'e
//      düşüyor ve popülasyon değişiyor; sayfa bunu söylemiyordu.

/** EndpointKey — satır kimliği. MV (service_name, http_route) grenine
 *  birebir oturur; method/kind satırın içinde toplanıyor. */
export interface EndpointKey {
  service: string;
  path: string;
}

// ───────────────────────── 1. "listede yeni" ─────────────────────────

/** LIST_NEW_TITLE — "listede yeni" rozetinin TEK açıklaması. Hem tablo
 *  hücresi hem paylaşılan TrendDelta çipi bunu kullanır ki aynı rozet iki
 *  farklı şey vaat etmesin. */
export const LIST_NEW_TITLE =
  'Önceki pencerenin LİSTESİNDE yoktu. Prior okuma da top-N ile sınırlı: ' +
  'bu endpoint önceki pencerede pekâlâ var olmuş, sadece listeye girmemiş ' +
  'olabilir. Yani "yeni endpoint" değil, "bu listede yeni".';

/** LIST_NEW_LABEL — rozetin metni. "YENİ" iddialıydı; bu değil. */
export const LIST_NEW_LABEL = 'listede yeni';

export type EndpointDelta =
  | { kind: 'listNew' }
  | { kind: 'delta'; pct: number };

/**
 * endpointP99Delta — bir satırın kötüleşme oranı, ya da "listede yeni".
 *
 * prior YOK / 0 / negatif → karşılaştırılacak taban yok. Bu ÖLÇÜM YOKLUĞU,
 * "%0 değişim" DEĞİL: sıfır taban bölmesi Infinity verirdi ve sayfa onu
 * "sonsuz kötüleşme" diye çizerdi.
 */
export function endpointP99Delta(cur: number, prior?: number): EndpointDelta {
  const p = prior ?? 0;
  if (!(p > 0) || !Number.isFinite(p) || !Number.isFinite(cur)) {
    return { kind: 'listNew' };
  }
  return { kind: 'delta', pct: ((cur - p) / p) * 100 };
}

// ───────────────────────── 2. KPI kapsamı ─────────────────────────

export interface ListedTotals {
  rows: number;
  calls: number;
  errors: number;
  errorRate: number; // yüzde
}

/**
 * listedTotals — GÖRÜNEN satırların toplamı. Adı bilerek "listed":
 * "total" demek, filoyu toplamış olmayı gerektirir ve bu okuma top-N.
 * errorRate pencere TOPLAMLARINDAN türer (satır oranlarının ortalaması
 * DEĞİL — düşük trafikli bir satır yüksek trafikli olanı eşit ağırlıklar).
 */
export function listedTotals(
  rows: Array<{ calls: number; errors: number }>,
): ListedTotals {
  let calls = 0;
  let errors = 0;
  for (const r of rows) {
    calls += r.calls;
    errors += r.errors;
  }
  return {
    rows: rows.length,
    calls,
    errors,
    errorRate: calls > 0 ? (errors / calls) * 100 : 0,
  };
}

// ───────────────────────── 3. URL kapsamı ─────────────────────────

/**
 * ENDPOINTS_RESERVED_PARAMS — /endpoints'in ZATEN kullandığı query
 * paramları. Yeni bir param eklemeden önce buraya bakılır; test bu listeyi
 * ?detail= / ?exp= ile çakışmaya karşı sabitliyor.
 *
 * Kaynaklar: sayfa-yerel okumalar (Endpoints.tsx), useUrlRange (range),
 * useUrlEnv (env), useDataTable (s_<storageKey>), SavedViewsBar (search
 * string'in TAMAMINI kaydeder, kendi paramı yok).
 */
export const ENDPOINTS_RESERVED_PARAMS = [
  'range', 'env', 'service', 'search', 'cluster', 'limit',
  'compare', 'shape', 'entry', 'cols', 'endpoint', 's_endpoints',
] as const;

/** DETAIL_PARAM / EXP_PARAM — v0.9.818'de eklenen iki param. */
export const DETAIL_PARAM = 'detail';
export const EXP_PARAM = 'exp';

/** isEndpointsParamFree — bu ad /endpoints'te boşta mı? */
export function isEndpointsParamFree(name: string): boolean {
  return !(ENDPOINTS_RESERVED_PARAMS as readonly string[]).includes(name);
}

/**
 * EXP_MAX — ?exp= içinde taşınacak EN ÇOK satır. URL uzunluğu sınırsız
 * büyüyemez (proxy'ler ~8 KB'de keser) ve 20 açık bağımlılık şeridi zaten
 * okunabilirliğin çok ötesinde. Kesme SESSİZ DEĞİL: encode ilk 20'yi
 * yazar, decode aynı tavanı uygular, yani iki taraf aynı kümede kalır.
 */
export const EXP_MAX = 20;

/**
 * endpointRowKey — satırın URL-güvenli kimliği: `enc(service)|enc(path)`.
 * Alanlar ÖNCE URI-encode edilir, yani yolun içindeki bir `|` sahte alan
 * sınırı uyduramaz (encodeEndpointParam'ın aynı sözleşmesi). Ayraç `,`
 * ile birleştirmek de güvenli: encodeURIComponent virgülü %2C yapar.
 */
export function endpointRowKey(service: string, path: string): string {
  return encodeURIComponent(service) + '|' + encodeURIComponent(path);
}

/** decodeEndpointRowKey — endpointRowKey'in tersi. Bozuk girdi → null. */
export function decodeEndpointRowKey(key: string): EndpointKey | null {
  const parts = key.split('|');
  if (parts.length !== 2 || !parts[0] || !parts[1]) return null;
  try {
    return { service: decodeURIComponent(parts[0]), path: decodeURIComponent(parts[1]) };
  } catch {
    return null; // bozuk %-escape
  }
}

/** encodeExpandedParam — açık satır kümesi → ?exp= değeri. Boş küme ''. */
export function encodeExpandedParam(keys: Iterable<string>): string {
  return [...keys].slice(0, EXP_MAX).join(',');
}

/**
 * decodeExpandedParam — ?exp= → açık satır kümesi. Çözülemeyen parça
 * ATILIR (sayfa bozuk bir derin-linkte çökmez, o satır sadece kapalı
 * açılır); tavan burada da uygulanır.
 */
export function decodeExpandedParam(raw: string | null | undefined): Set<string> {
  const out = new Set<string>();
  if (!raw) return out;
  for (const part of raw.split(',')) {
    if (!part) continue;
    if (!decodeEndpointRowKey(part)) continue;
    out.add(part);
    if (out.size >= EXP_MAX) break;
  }
  return out;
}

/** toggleExpanded — SAF küme geçişi (React state'i mutasyona uğratmadan). */
export function toggleExpanded(cur: Set<string>, key: string): Set<string> {
  const next = new Set(cur);
  if (next.has(key)) next.delete(key);
  else if (next.size < EXP_MAX) next.add(key);
  return next;
}

// ───────────────────────── 4. Kaynak değişimi ─────────────────────────

/**
 * endpointsSourceNote — okuma MV'den HAM spans'e düştüğünde basılacak
 * satır; düşmediğinde null (yani not yalnız gerçekten farklı bir şey
 * okunuyorken çıkar, her sayfada duran bir uyarı gürültüsü değil).
 *
 * NEDEN ÖNEMLİ: spanmetrics_1m'de ne cluster ne deploy_env boyutu var
 * (chstore.EndpointsQuery.forcesRaw), o yüzden bu iki filtre okumayı ham
 * span'lere indiriyor. Ham yol AYNI listeyi vermiyor:
 *   · pathExpr'in url.path / http.target katmanları devreye giriyor ve
 *     rotasız client span'leri (endpointKindPred'in v0.8.560 carve-out'u)
 *     listeye KATILIYOR — MV yolunda bu satırlar yok;
 *   · MV'nin argMax exemplar state'leri ham yolda YOK, yani ⚡/✖ trace
 *     kısayolları hiç gelmiyor ("bu pencerede yavaş trace yok" diye
 *     okunuyordu, oysa cevap "bu yolda exemplar tutulmuyor").
 */
export function endpointsSourceNote(cluster?: string, env?: string): string | null {
  const dims: string[] = [];
  if (env) dims.push('env');
  if (cluster) dims.push('cluster');
  if (dims.length === 0) return null;
  const which = dims.join(' + ');
  return (
    `Kaynak: HAM spans — ${which} filtresi spanmetrics rollup'ında olmayan ` +
    'bir boyut, okuma ham span\'lere düşüyor. Bu liste MV yolunun birebir ' +
    'aynısı DEĞİL: rotasız client span\'leri (url.path / http.target) buraya ' +
    'dahil olur ve exemplar kısayolları (⚡ / ✖) hiç gelmez — argMax ' +
    'exemplar state\'leri yalnız MV\'de yaşıyor.'
  );
}
