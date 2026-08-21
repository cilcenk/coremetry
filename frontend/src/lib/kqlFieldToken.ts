// kqlFieldToken — KQL kutusunda ALAN ADI tamamlamasının saf çekirdeği
// (v0.9.955, UX denetimi F4 / Ö16).
//
// SORUN: /logs'ta alan ADI keşfi yoktu. KQL kutusu `field:` yazıldıktan
// SONRA değerleri tamamlıyordu ama hangi alanların VAR OLDUĞUNU
// göstermiyordu. Operatör CHANNEL_CODE'u aramak için ES'teki tam yazımı
// (verbatim mi `attributes.` altında mı, büyük mü küçük harf mi)
// DIŞARIDAN bilmek zorundaydı; yanlış yazımın bedeli sessizdi — sorgu
// koşuyor, sıfır satır dönüyor ve bu "böyle log yok" diye okunuyordu,
// "alanın adı farklı" diye değil.
//
// AĞ MALİYETİ SIFIR: alan listesi ZATEN çekiliyor (/api/logs/fields,
// LogFieldsPanel'in "Available fields" bölümü). Bu modül yalnız o listeyi
// kutunun kendisine bağlıyor. Operatörün "elastic api kullanımını çok
// artırma" şartı (v0.8.270 disiplini) yeni bir tur eklemeden karşılanıyor.
//
// SAF — tablo testleri kqlFieldToken.test.ts.

export interface FieldNameToken {
  /** Şu ana dek yazılmış alan-adı öneki (boş olabilir: ` ` sonrası). */
  prefix: string;
  /** Değiştirilecek aralık — [start, end). */
  start: number;
  end: number;
}

/**
 * detectFieldNameToken — imleç bir ALAN ADI yazılabilecek konumda mı?
 *
 * null döndüğü hâller ve NEDENLERİ:
 *  • Token'da zaten ':' var → orası DEĞER konumu; alan adı önermek
 *    operatörün yazdığı değeri ezerdi (detectFieldToken'ın işi).
 *  • İmleç tırnak içinde → serbest metin arıyor, alan adı değil.
 *  • Token boş VE öncesinde metin yok → boş kutuda liste açmak, henüz
 *    soru sormamış operatöre 200 satırlık bir mapping dökümü göstermek
 *    olurdu.
 */
export function detectFieldNameToken(text: string, cursor: number): FieldNameToken | null {
  if (cursor < 0 || cursor > text.length) return null;

  // Tırnak dengesi: imlece kadar TEK sayıda tırnak varsa içerideyiz.
  let quotes = 0;
  for (let i = 0; i < cursor; i++) {
    if (text[i] === '"' && text[i - 1] !== '\\') quotes++;
  }
  if (quotes % 2 === 1) return null;

  // Token sınırları: geriye doğru ilk ayraca kadar.
  let start = cursor;
  for (let i = cursor - 1; i >= 0; i--) {
    const c = text[i];
    if (c === ' ' || c === '\t' || c === '(' || c === ')' || c === '"') break;
    start = i;
  }
  const prefix = text.slice(start, cursor);
  // ':' varsa burası değer konumu — alan adı önerisi YANLIŞ olurdu.
  if (prefix.includes(':')) return null;
  // Boş önek yalnız operatör bir şeyler yazmaya BAŞLADIYSA anlamlı.
  if (prefix.length === 0) return null;
  return { prefix, start, end: cursor };
}

/**
 * rankFieldNames — öneke göre sıralı alan adları.
 *
 * SIRALAMA SÖZLEŞMESİ (Kibana davranışı):
 *   1. Önekle BAŞLAYANLAR (büyük/küçük harf duyarsız),
 *   2. sonra öneki İÇERENLER — CHANNEL_CODE'u `channel` yazarak bulmak
 *      tam olarak bu dalın işi, çünkü ES alanı `attributes.CHANNEL_CODE`
 *      gibi bir yol olabilir ve operatör önekini bilmiyor;
 *   3. her grup kendi içinde alfabetik (kararlı: aynı önek her zaman
 *      aynı listeyi verir, poll'de sıra oynamaz).
 */
export function rankFieldNames(fields: string[], prefix: string, limit = 12): string[] {
  const p = prefix.toLowerCase();
  const starts: string[] = [];
  const contains: string[] = [];
  for (const f of fields) {
    const lf = f.toLowerCase();
    if (lf.startsWith(p)) starts.push(f);
    else if (p && lf.includes(p)) contains.push(f);
  }
  starts.sort((a, b) => a.localeCompare(b));
  contains.sort((a, b) => a.localeCompare(b));
  return [...starts, ...contains].slice(0, Math.max(0, limit));
}

/**
 * FieldNameHint — alan-adı listesinin GÜVENİLİRLİĞİ hakkında söylenmesi
 * gereken şey (v0.9.970, Ö16 ikinci tur).
 *
 * `/api/logs/fields` katalogu 500 yolda KIRPIYOR (ES tarafında
 * `listFieldsMax`; kırpma alfabetik). Kırpıldığında `total` gerçek yol
 * sayısını dürüstçe söylüyor ve yan paneldeki "Available fields" bunu
 * "first N of M" diye gösteriyor — ama KUTUNUN açılır listesi
 * göstermiyordu. Sonuç, Ö16'nın kapatmak için var olduğu yanlış okumanın
 * ta kendisi: dinamik mapping'de dört haneli alan sayısı rutin
 * (ListFieldsBounded'ın kendi yorumu), yani `attributes.CHANNEL_CODE`
 * pekâlâ 500'ün DIŞINDA kalır; operatör `CHANNEL` yazar, hiçbir şey
 * açılmaz ve bunu "böyle bir alan yok" diye okur. Oysa doğrusu
 * "katalog o alana kadar uzanmıyor".
 */
export type FieldNameHint =
  | { kind: 'none' }
  /** Eşleşme VAR ama katalog kırpık — liste tam olmayabilir. */
  | { kind: 'clipped'; shown: number; total: number }
  /** Eşleşme YOK ve katalog kırpık — sessizlik yanıltıcı, söylemek ŞART. */
  | { kind: 'clipped-no-match'; shown: number; total: number };

/**
 * fieldNameHint — saf karar: açılır liste kataloğun kırpıklığını
 * söylemeli mi?
 *
 * `total` bilinmiyorsa (undefined) hiçbir şey söylenmez — "bilmiyorum"u
 * "tamam" diye sunmak da, "kırpık" diye sunmak da uydurma olurdu.
 * `total <= shown` ise katalog TAM; kırpılmamış bir listede uyarı
 * göstermek operatörü boşuna şüpheye düşürür.
 */
export function fieldNameHint(
  matches: number, shown: number, total?: number,
): FieldNameHint {
  if (typeof total !== 'number' || !(total > shown)) return { kind: 'none' };
  return { kind: matches === 0 ? 'clipped-no-match' : 'clipped', shown, total };
}

/**
 * applyFieldName — seçilen alanı metne yazar ve ':' EKLER.
 *
 * ':' eklemek süs değil: alan adı tek başına KQL'de serbest metindir,
 * yani tamamlama operatörü tam da kaçındığı hâle (yanlış sorgu, sıfır
 * satır) bırakırdı. İki nokta, değer tamamlamasının da tetikleyicisi —
 * yani tek seçimle zincirin ikinci halkası açılıyor.
 */
export function applyFieldName(
  text: string, token: FieldNameToken, field: string,
): { text: string; cursor: number } {
  const before = text.slice(0, token.start);
  const after = text.slice(token.end);
  const inserted = `${field}:`;
  return { text: before + inserted + after, cursor: before.length + inserted.length };
}

/**
 * KQL_OPERATORS + mergeOperatorSuggestions — v0.9.1216 (dilim 4).
 *
 * query_string'de boolean operatörler YALNIZ BÜYÜK HARF tanınır:
 * küçük harfle yazılan `and` sessizce sıradan bir arama terimi olur ve
 * sorgu "çalışır ama yanlış" sınıfına düşer — bu yüzden tamamlama
 * gerçek bir düzeltmedir, süs değil. Operatörler alan-adı listesinin
 * BAŞINA girer (önek büyük/küçük harf duyarsız eşleşince), böylece
 * ayrı bir öneri kaynağı/klavye yolu doğmaz.
 */
export const KQL_OPERATORS = ['AND', 'OR', 'NOT'] as const;

export function mergeOperatorSuggestions(ranked: string[], prefix: string): string[] {
  const p = prefix.toLowerCase();
  if (!p) return ranked;
  const ops = KQL_OPERATORS.filter(op => op.toLowerCase().startsWith(p));
  if (ops.length === 0) return ranked;
  return [...ops, ...ranked.filter(f => !(KQL_OPERATORS as readonly string[]).includes(f))];
}
