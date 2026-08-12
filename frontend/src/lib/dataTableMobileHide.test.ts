// dataTableMobileHide — dar ekran denetimi D6.1 kapısı (v0.9.988)
//
// Ne çiviliyor: `visibleColumns(cols, narrow)` iki süzgeci de doğru
// birleştiriyor mu — `headerHidden` HER genişlikte düşer, `mobileHide`
// YALNIZ dar ekranda düşer, sıra korunur.
//
// Neden kapıya değer: `mobileHide` yanlış yönde uygulanırsa (ör. `narrow`
// bayrağı ters okunursa) hata SESSİZDİR — masaüstünde kolonlar kaybolur,
// tip sistemi bunu göremez ve hiçbir mount testi 68 tablonun kolon
// setini yoklamıyor. `DataTableColgroup` ve `DataTableHead` aynı listeyi
// kullandığı için tek yönlü bir hata `<col>` ile `<th>` sayısını da
// AYNI ölçüde bozar, yani hizalama testi de yakalayamazdı.
//
// Bu sürümde HİÇBİR kolon `mobileHide` taşımıyor (mekanizma kuruldu,
// tüketici sonra gelecek) — o yüzden son iddia depo genelinde bunu
// doğruluyor: bir kolon işaretlendiği anda bu satır kırmızıya döner ve
// işaretleyen sayfanın GÖVDE hücresini de atlaması gerektiği hatırlatılır.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { visibleColumns, type DataTableColumn } from './dataTable';

type Row = { a: number };

const col = (id: string, extra: Partial<DataTableColumn<Row>> = {}): DataTableColumn<Row> =>
  ({ id, label: id, ...extra });

describe('D6.1 — visibleColumns', () => {
  const CASES: {
    name: string;
    cols: DataTableColumn<Row>[];
    narrow: boolean;
    want: string[];
  }[] = [
    {
      name: 'işaretsiz kolonlar — geniş ekran',
      cols: [col('a'), col('b'), col('c')],
      narrow: false,
      want: ['a', 'b', 'c'],
    },
    {
      name: 'işaretsiz kolonlar — dar ekran (BUGÜNKÜ davranış, hiçbir şey düşmez)',
      cols: [col('a'), col('b'), col('c')],
      narrow: true,
      want: ['a', 'b', 'c'],
    },
    {
      name: 'headerHidden HER genişlikte düşer — geniş',
      cols: [col('a'), col('impact', { headerHidden: true }), col('c')],
      narrow: false,
      want: ['a', 'c'],
    },
    {
      name: 'headerHidden HER genişlikte düşer — dar',
      cols: [col('a'), col('impact', { headerHidden: true }), col('c')],
      narrow: true,
      want: ['a', 'c'],
    },
    {
      name: 'mobileHide geniş ekranda DÜŞMEZ',
      cols: [col('a'), col('b', { mobileHide: true }), col('c')],
      narrow: false,
      want: ['a', 'b', 'c'],
    },
    {
      name: 'mobileHide dar ekranda düşer',
      cols: [col('a'), col('b', { mobileHide: true }), col('c')],
      narrow: true,
      want: ['a', 'c'],
    },
    {
      name: 'iki süzgeç birlikte — dar',
      cols: [
        col('a'),
        col('b', { mobileHide: true }),
        col('impact', { headerHidden: true }),
        col('d', { mobileHide: true }),
        col('e'),
      ],
      narrow: true,
      want: ['a', 'e'],
    },
    {
      name: 'iki süzgeç birlikte — geniş',
      cols: [
        col('a'),
        col('b', { mobileHide: true }),
        col('impact', { headerHidden: true }),
        col('d', { mobileHide: true }),
        col('e'),
      ],
      narrow: false,
      want: ['a', 'b', 'd', 'e'],
    },
    {
      name: 'her kolon dar ekranda düşerse boş liste (çökmez)',
      cols: [col('a', { mobileHide: true }), col('b', { mobileHide: true })],
      narrow: true,
      want: [],
    },
  ];

  CASES.forEach(c => {
    it(c.name, () => {
      expect(visibleColumns(c.cols, c.narrow).map(x => x.id)).toEqual(c.want);
    });
  });

  it('girdiyi MUTASYONA UĞRATMAZ (kolon dizisi useMemo bağımlılığı)', () => {
    const cols = [col('a'), col('b', { mobileHide: true })];
    const before = cols.map(c => c.id);
    visibleColumns(cols, true);
    expect(cols.map(c => c.id)).toEqual(before);
    expect(cols).toHaveLength(2);
  });
});

// ---------------------------------------------------------------------------
// Kaynak kapısı: `mobileHide` işaretleyen İLK sayfa geldiğinde bu satır
// kırmızıya döner. Amaç yasaklamak DEĞİL — hatırlatmak: `<colgroup>` ve
// `<thead>` otomatik daralıyor, GÖVDE hücresini sayfa basıyor. İkisi
// ayrışırsa hücreler bir kolon kayar ve bunu hiçbir tip kontrolü görmez.
// İşaretleyen sürüm bu listeyi gerekçesiyle günceller (kapı sözleşmesi
// değişince kapıyı GÜNCELLE, susturma).
const SRC = resolve(__dirname, '..');
const MOBILE_HIDE_ALLOWLIST = new Set<string>([
  // dosya yolu → gövde hücresini de `dt.visibleColumns`/`dt.narrow` ile
  // süzdüğü DOĞRULANMIŞ sayfalar. Şu an boş.
]);

function tsxFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules') continue;
    const p = join(dir, e);
    if (statSync(p).isDirectory()) tsxFiles(p, out);
    else if (/\.tsx?$/.test(e) && !/\.test\.tsx?$/.test(e)) out.push(p);
  }
  return out;
}

describe('D6.1 — mobileHide tüketicileri kayıtlı', () => {
  it('işaretleyen her dosya izin listesinde (gövde hücresi de süzülüyor mu?)', () => {
    // `lib/dataTable.ts` tanımın kendisi — arama imzası ÇALIŞMA ANINDA
    // kuruluyor ki bu testin kendi düzyazısı eşleşmesin (depoda yedi kez
    // ısıran tuzak).
    const NEEDLE = 'mobile' + 'Hide:';
    const offenders = tsxFiles(SRC)
      .filter(p => !p.endsWith(join('lib', 'dataTable.ts')))
      .filter(p => readFileSync(p, 'utf8').includes(NEEDLE))
      .map(p => p.slice(SRC.length + 1))
      .filter(p => !MOBILE_HIDE_ALLOWLIST.has(p));
    expect(
      offenders,
      'mobileHide işaretlendi: gövde <td> render\'ı da dt.visibleColumns ile süzülmeli, sonra bu izin listesine ekle',
    ).toEqual([]);
  });
});
