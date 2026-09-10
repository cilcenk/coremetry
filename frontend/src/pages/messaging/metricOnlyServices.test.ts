import { describe, expect, it } from 'vitest';
import { metricOnlyServices } from './metricOnlyServices';

// v0.10.610 — keşfedilen (metrik) servislerden span'de olmayanlar: başlık
// sayaçlarının "+N" çipi ve sekme altındaki liste bunu sayar.
describe('metricOnlyServices', () => {
  it('span satırlarında olmayan keşfedilenleri sıralı ve tekil verir', () => {
    const rows = [{ service: 'shop' }, { service: 'shop-payment' }, { service: '' }];
    expect(metricOnlyServices(['zeta', 'shop-payment', 'alpha', 'alpha', ' zeta '], rows)).toEqual(['alpha', 'zeta']);
  });
  it('boş/undefined keşif → boş; boş adlar atılır', () => {
    expect(metricOnlyServices(undefined, [{ service: 'a' }])).toEqual([]);
    expect(metricOnlyServices([], [])).toEqual([]);
    expect(metricOnlyServices(['', '  '], [])).toEqual([]);
  });
  it('tam eşleşme — kısmi ad farklı servistir', () => {
    expect(metricOnlyServices(['shop-payment-prod'], [{ service: 'shop-payment' }])).toEqual(['shop-payment-prod']);
  });
  it('hepsi span\'de varsa boş (başlıkta çip çizilmez)', () => {
    expect(metricOnlyServices(['a', 'b'], [{ service: 'b' }, { service: 'a' }])).toEqual([]);
  });
});
