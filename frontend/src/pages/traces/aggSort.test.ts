// @vitest-environment jsdom
// (resolveInitialSort localStorage'a bakıyor; öncelik sözleşmesi ancak
//  gerçek bir depo varken sınanabilir.)
import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { AGG_SORT_IDS, toAggSort, decodeLegacyAggSort } from './aggSort';
import { resolveInitialSort } from '@/components/ui/DataTable';
import { removeRaw, dtSortKey } from '@/lib/storage';

// aggSort.test.ts — v0.9.878.
//
// Kaynak: tutarlılık denetimi (docs/audit/frontend-consistency-audit.md) BT6,
// icra denetiminin R1 riski — dalganın TEK yüksek riskli parçası.
//
// SEMPTOM (dönüşümün AÇACAĞI kusur, sessiz): /traces'in kendi URL yazıcısı
// sorgu dizesini her state yazımında SIFIRDAN kuruyor. Primitif kendi
// `s_traces-agg` parametresini yazdıktan bir render sonra, o yazıcının
// listesinde bulunmayan her parametre SİLİNİYOR. Yani paylaşılan bir
// sıralama linki alıcıda kayboluyor: alıcının kendi localStorage'ındaki sıra
// devreye giriyor, BAŞLIK p99 diyor ama sunucu count'a göre sıralıyor.
// Ekranda hiçbir şey bozulmuyor — sayılar yanlış sırada duruyor.
//
// Aggregate sıralaması bir görüntüleme tercihi DEĞİL: `sort` API'ye gidiyor
// ve backend LIMIT 200 uyguluyor, yani sıralama HANGİ 200 SATIRIN geldiğini
// belirliyor. Yanlış sıra = eksik veri, yanlış cila değil.

describe('toAggSort — sunucuya giden değerin daraltılması', () => {
  it.each([...AGG_SORT_IDS])('%s geçerli', id => {
    expect(toAggSort(id)).toBe(id);
  });

  it.each([
    ['bilinmeyen kolon', 'traceCount'],
    ['boş dize', ''],
    ['büyük harf', 'P99'],
    ['enjeksiyon denemesi', 'count; DROP'],
  ])('%s reddediliyor', (_n, v) => {
    expect(toAggSort(v)).toBeNull();
  });

  it('null/undefined güvenli', () => {
    expect(toAggSort(null)).toBeNull();
    expect(toAggSort(undefined)).toBeNull();
  });

  // Sunucuya tanınmayan bir `sort` göndermek 400 döndürür. Daraltmanın
  // sessizce bir varsayılana düşmemesi, çağıranın fallback'lemeyi
  // unutamaması için.
  it('geçersizde varsayılana DÜŞMÜYOR — null dönüyor', () => {
    expect(toAggSort('nope')).not.toBe('count');
  });
});

describe('decodeLegacyAggSort — eski link köprüsü', () => {
  it('?aggSort=p99&aggOrder=asc → {p99, asc}', () => {
    expect(decodeLegacyAggSort('p99', 'asc')).toEqual({ id: 'p99', dir: 'asc' });
  });

  it('aggOrder yoksa desc', () => {
    expect(decodeLegacyAggSort('errorRate', null)).toEqual({ id: 'errorRate', dir: 'desc' });
  });

  it('aggOrder çöpse desc (asc DIŞINDA her şey)', () => {
    expect(decodeLegacyAggSort('p95', 'sideways')).toEqual({ id: 'p95', dir: 'desc' });
  });

  it('aggSort geçersizse köprü YOK', () => {
    expect(decodeLegacyAggSort('bogus', 'asc')).toBeNull();
    expect(decodeLegacyAggSort(null, 'asc')).toBeNull();
  });
});

// ── R1'İN ÖZÜ: ÖNCELİK ────────────────────────────────────────────────────
//
// Üç kanal aynı anda dolu olabilir. Sözleşme:
//   s_traces-agg  >  eski ?aggSort=  >  localStorage  >  initialSort
//
// Gerekçe: paylaşılan bir linkin niyeti alıcının KİŞİSEL varsayılanını
// yenmeli — kusurun operatöre pahalıya patlayan yarısı tam olarak buydu
// (link gönderildi, alıcı başka bir sıra gördü, ikisi de fark etmedi).
describe('resolveInitialSort önceliği (R1)', () => {
  const KEY = 'traces-agg';
  // Üretim koduyla AYNI kapıdan: ortamın localStorage'ı kısmî bir stub
  // (`.clear` yok), doğrudan dokunmak testi ortama bağımlı kılıyordu.
  beforeEach(() => removeRaw(dtSortKey(KEY)));

  it('s_ parametresi eski aggSort köprüsünü YENİYOR', () => {
    const got = resolveInitialSort(KEY, 'p99.asc', { id: 'count', dir: 'desc' },
      { id: 'errorRate', dir: 'desc' });
    expect(got).toEqual({ id: 'p99', dir: 'asc' });
  });

  it('s_ yokken eski köprü devreye giriyor', () => {
    const got = resolveInitialSort(KEY, null, { id: 'count', dir: 'desc' },
      { id: 'errorRate', dir: 'desc' });
    expect(got).toEqual({ id: 'errorRate', dir: 'desc' });
  });

  // NOT: localStorage rungu (link kanalları > kişisel varsayılan) burada
  // SINANMIYOR. Bu ortamda global `localStorage` kısmî bir stub ve yazma
  // kalıcı değil; sınamak testi ortam kurulumuna bağlar, davranışa değil.
  // Zaten BT6'nın değiştirdiği şey o rung değil — iki LİNK kanalının
  // birbirine göre sırası. Onlar yukarıda/aşağıda çivili.

  it('hiçbiri yoksa initialSort', () => {
    const got = resolveInitialSort(KEY, null, { id: 'count', dir: 'desc' }, null);
    expect(got).toEqual({ id: 'count', dir: 'desc' });
  });
});

// ── KANALIN KENDİSİ ───────────────────────────────────────────────────────
describe('Traces URL yazıcısı s_traces-agg\'i ÜRETİYOR', () => {
  const src = readFileSync(resolve(__dirname, '../Traces.tsx'), 'utf8');

  // ASIL KAPI. Bu satır silinirse primitifin yazdığı sıralama parametresi
  // bir render sonra buildQuery tarafından süpürülür ve kusur SESSİZCE döner.
  it('buildQuery listesinde s_traces-agg var', () => {
    expect(src).toContain("'s_traces-agg'");
    expect(src).toMatch(/formatSortParam\(\{ id: aggSort, dir: aggOrder \}\)/);
  });

  it('eski aggSort/aggOrder parametreleri artık ÜRETİLMİYOR', () => {
    // Okumak serbest (köprü), yazmak değil: ikisini birden üretmek URL'de
    // iki sıralama kaynağı demek ve hangisinin kazandığı okuyana kapalı.
    expect(src).not.toMatch(/\['aggSort',\s+view === 'aggregate'/);
    expect(src).not.toMatch(/\['aggOrder',\s+view === 'aggregate'/);
  });

  it('tablo serverSort kipinde — satırlar yeniden sıralanmıyor', () => {
    // Client-side sıralamaya düşerse ekranda sıralı GÖRÜNÜR ama sunucudan
    // gelen LIMIT 200'lük küme yanlış eksende seçilmiş olur: tablo doğru
    // sırada YANLIŞ satırları gösterir.
    expect(src).toMatch(/storageKey: 'traces-agg'[\s\S]{0,400}serverSort: true/);
  });

  it('elle AggHeader geri gelmedi', () => {
    expect(src).not.toMatch(/function AggHeader\(/);
  });
});
