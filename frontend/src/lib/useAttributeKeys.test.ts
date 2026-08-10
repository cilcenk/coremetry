// useAttributeKeys — Ö14 kapısı (v0.9.933)
//
// Ne çiviliyor: (a) birleştirme sırası — GÖZLENEN önce, statik sonra;
// (b) keşfin TEK yerden geldiği: FilterBuilder ve /traces'in aggregate
// anahtar kutusu aynı listeyi kullanıyor.
//
// (a) neden önemli: ters sıra (v0.5.261 öncesi) operatörün KENDİ custom
// attribute'larını bayat sabit listenin altına gömüyordu — yani keşfin
// tam olarak keşfetmesi gereken şeyi.
//
// (b) neden kapı: iki kopya olsaydı biri bayatlardı ve belirtisi "bazı
// anahtarlar bir kutuda var, diğerinde yok" gibi açıklanamaz bir
// tutarsızlık olurdu.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { mergeKeys, scopedKey, SUGGESTED_KEYS } from './useAttributeKeys';

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .replace(/^\s*\/\/.*$/gm, '')
    .replace(/\{\/\*[\s\S]*?\*\/\}/g, m => m.replace(/[^\n]/g, ' '));
}

describe('mergeKeys — gözlenen önce, statik sonra', () => {
  const cases: Array<{ name: string; observed: string[]; suggested: string[]; want: string[] }> = [
    { name: 'boş gözlem → yalnız statik', observed: [], suggested: ['a', 'b'], want: ['a', 'b'] },
    { name: 'gözlenen LİDER', observed: ['function_code'], suggested: ['name'], want: ['function_code', 'name'] },
    { name: 'gözlenen sıra KORUNUR (sayıya göre gelmiş)', observed: ['z', 'a', 'm'], suggested: [], want: ['z', 'a', 'm'] },
    { name: 'kopya statikten elenir', observed: ['name'], suggested: ['name', 'kind'], want: ['name', 'kind'] },
    { name: 'gözlemin KENDİ kopyası da elenir', observed: ['x', 'x'], suggested: [], want: ['x'] },
    { name: 'ikisi de boş', observed: [], suggested: [], want: [] },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(mergeKeys(c.observed.map(key => ({ key, count: 1 })), c.suggested)).toEqual(c.want);
    });
  }

  it('varsayılan statik liste OTel semconv taşıyor', () => {
    expect(SUGGESTED_KEYS).toContain('http.status_code');
    expect(SUGGESTED_KEYS).toContain('resource.service.name');
  });
});

describe('scopedKey — backend scope → filtre dili', () => {
  it('resource önekleniyor', () => {
    expect(scopedKey({ scope: 'resource', key: 'host.name' })).toBe('resource.host.name');
  });
  it('span önekleNMİYOR (çıplak ad iyi bilinen kolona da düşebilir)', () => {
    expect(scopedKey({ scope: 'span', key: 'http.method' })).toBe('http.method');
  });
});

describe('keşif TEK kaynaktan', () => {
  const read = (p: string[]) => stripComments(readFileSync(resolve(__dirname, '..', ...p), 'utf8'));

  it('FilterBuilder hooku kullanıyor, kendi kopyasını tutmuyor', () => {
    const src = read(['components', 'FilterBuilder.tsx']);
    expect(src).toContain('useAttributeKeys(value)');
    // Kendi fetch'ini geri koyan bir değişiklik iki listeyi ayırırdı.
    expect(src.includes('api.attributeKeys(')).toBe(false);
  });

  it('/traces aggregate anahtar kutusu çıplak input DEĞİL', () => {
    const src = read(['pages', 'Traces.tsx']);
    expect(src).toContain('useAttributeKeys()');
    expect(src).toContain('options={attrKeys}');
    // Eski çıplak hâl geri gelmemeli.
    expect(src.includes('<input placeholder="attribute key')).toBe(false);
  });
});
