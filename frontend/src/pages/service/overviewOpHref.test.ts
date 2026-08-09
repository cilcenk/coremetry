import { describe, it, expect } from 'vitest';
import { overviewOpHref } from './OverviewTables';

// v0.9.861 — UX denetimi Ö6: Service Overview'un "Operations" kartı hâlâ
// serbest-metin `search=` ile pivotluyordu. `search` trace'in HERHANGİ bir
// span'ında eşleşir ve kart satırı KÖK operasyonu gösterdiğinden, satırdan
// /traces'e geçen operatörün listesine başka operasyonların trace'leri de
// düşüyordu — "bu operasyonun trace'leri" sanılıp yanlış teşhis kuruluyordu.
//
// Aynı hata v0.8.488'de OPERATÖR RAPORUYLA Operations tablosunda
// düzeltilmişti (`filters=[{k:'name',op:'=',…}]`); bu kart kalan yarısıydı.
// İki yüzey "aynı hedefe gider" derken farklı sorular soruyordu.

const params = (href: string) => new URLSearchParams(href.slice(href.indexOf('?') + 1));

describe('overviewOpHref — Ö6 substring search pivotu', () => {
  const href = overviewOpHref('checkout', { preset: '6h' }, 'GET /cart');
  const p = params(href);

  it('serbest metin `search=` ASLA yazılmaz', () => {
    // Bug'ın tam kendisi: eşleşme span değil TRACE düzeyinde oluyordu.
    expect(p.has('search')).toBe(false);
  });

  it('kapsam kesin isim filtresi — v0.8.488 ile aynı biçim', () => {
    expect(JSON.parse(p.get('filters') ?? '[]'))
      .toEqual([{ k: 'name', op: '=', v: ['GET /cart'] }]);
  });

  it('servis ve pencere taşınır', () => {
    expect(p.get('service')).toBe('checkout');
    expect(p.get('range')).toBe('6h');
  });

  it('custom pencere de taşınır — kartın zoom\'lu hâli', () => {
    const c = params(overviewOpHref('checkout', { preset: 'custom', fromMs: 1000, toMs: 2000 }, 'GET /cart'));
    expect(c.get('range')).toBe('custom:1000-2000');
  });

  it('rootOnly=false — operasyon span\'i kök olmak zorunda değil', () => {
    expect(p.get('rootOnly')).toBe('false');
  });

  it('sorgu-benzeri karakterli operasyon adı filtreye AYNEN girer', () => {
    // `search=` yolunda bu adlar hem kaçış hem eşleşme tarafında sorunluydu.
    const weird = 'GET /a b?c=1&d';
    const back = JSON.parse(params(overviewOpHref('s', { preset: '1h' }, weird)).get('filters') ?? '[]');
    expect(back).toEqual([{ k: 'name', op: '=', v: [weird] }]);
  });
});
