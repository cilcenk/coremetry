// repeatsPivot.test.ts — v0.9.1277.
//
// `repeatsExploreHref` bir SÖZLEŞME testi: ürettiği dizeyi Explore.tsx
// okuyor ve iki taraf ayrı dosyalarda. Üç şey sessizce kırılabilir —
//
//  1. ANAHTAR ADI. Explore repeats modunu `result=repeats` + `groupBy` +
//     `minRepeats` + `filters` ile okur. Yanlış bir anahtar param'ı ölü
//     yapmaz, DAHA KÖTÜSÜNÜ yapar: Explore'un state→URL efekti kendi
//     beyaz listesinden yeniden kuruyor, yani bilinmeyen anahtar ilk
//     yazımda SİLİNİYOR (v0.9.855'te `?operation=` tam böyle öldü).
//  2. PENCERE. `range=` düşerse Explore operatörün yapışkan aralığını
//     kullanır ve boş sonuç "desen yok" diye okunur (v0.9.208/213).
//  3. NORMALİZE-vs-HAM İFADE. Drawer'daki SQL normalize; span'lerdeki
//     `db.statement` ham. İfadeyi FİLTREYE koyan bir "iyileştirme"
//     sonucu garantili boşaltır. Bu testin en sert iddiası bu.
import { describe, it, expect } from 'vitest';
import { repeatsExploreHref } from './pivotHref';

const WIN = { fromNs: 1_700_000_000_000_000_000, toNs: 1_700_003_600_000_000_000 };
const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?')));

describe('repeatsExploreHref', () => {
  it('Explore repeats modunu açıyor ve pencereyi TAŞIYOR', () => {
    const p = params(repeatsExploreHref({ window: WIN }));
    expect(p.get('result')).toBe('repeats');
    expect(p.get('groupBy')).toBe('db.statement');
    expect(p.get('minRepeats')).toBe('5');
    expect(p.get('range')).toBe('custom:1700000000000-1700003600000');
  });

  it('preset aralık aynen geçiyor', () => {
    const p = params(repeatsExploreHref({ window: { preset: '6h' } }));
    expect(p.get('range')).toBe('6h');
  });

  it('tek servis `=`, çok servis `IN` — backend `=` için tek değer ister', () => {
    const one = JSON.parse(params(repeatsExploreHref({
      window: WIN, services: ['orders'],
    })).get('filters')!);
    expect(one).toEqual([{ k: 'service.name', op: '=', v: ['orders'] }]);

    const many = JSON.parse(params(repeatsExploreHref({
      window: WIN, services: ['orders', 'payments'],
    })).get('filters')!);
    expect(many).toEqual([{ k: 'service.name', op: 'IN', v: ['orders', 'payments'] }]);
  });

  it('servis yoksa filtre param\'ı HİÇ yazılmıyor (boş JSON dizisi değil)', () => {
    // '[]' yazmak Explore'un decodeFilters'ında zararsız ama URL'i
    // "filtreli" gösterir ve paylaşılan link yanıltıcı olur.
    expect(params(repeatsExploreHref({ window: WIN })).has('filters')).toBe(false);
    expect(params(repeatsExploreHref({ window: WIN, services: [] })).has('filters')).toBe(false);
  });

  it('db.system kapsamı ekleniyor', () => {
    const f = JSON.parse(params(repeatsExploreHref({
      window: WIN, services: ['orders'], dbSystem: 'postgresql',
    })).get('filters')!);
    expect(f).toContainEqual({ k: 'db.system', op: '=', v: ['postgresql'] });
  });

  it('İFADE METNİ FİLTREYE GİRMİYOR — normalize/ham uyuşmazlığı', () => {
    // Sözleşme: bu fonksiyonun imzasında ifade metni ALMAK için bir
    // yer YOK ve üretilen filtre yalnız servis + db.system taşır.
    // İfade kapsamı `groupBy=db.statement` üzerinden kurulur.
    const href = repeatsExploreHref({
      window: WIN, services: ['orders'], dbSystem: 'postgresql',
    });
    expect(href).not.toContain('db.statement%22%2C%22op');
    const f = JSON.parse(params(href).get('filters')!) as Array<{ k: string }>;
    expect(f.map(x => x.k)).toEqual(['service.name', 'db.system']);
    expect(params(href).get('groupBy')).toBe('db.statement');
  });

  it('groupBy ve minRepeats ezilebiliyor', () => {
    const p = params(repeatsExploreHref({
      window: WIN, groupBy: ['name', 'peer.service'], minRepeats: 10,
    }));
    expect(p.get('groupBy')).toBe('name,peer.service');
    expect(p.get('minRepeats')).toBe('10');
  });
});
