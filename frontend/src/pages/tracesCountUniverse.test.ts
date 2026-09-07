// v0.10.520 — operatör (prod): "2.4M span diyor ama 5 trace getirdi; 10,000+
// demesine rağmen diğer trace'ler gelmiyor." Sayım isteği `search`i
// taşımıyordu: sayım servisin tüm evrenini sayıp "10,000+" basarken liste
// aramayla 5 trace buluyordu (v0.9.638 "sayım listeyle AYNI evreni sayar"
// sözleşmesinin ihlali). Kaynak pinleri: sayım aramayı taşır, effect
// bağımlılığında; liste son sayfadaysa kesin toplam listeden.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, './Traces.tsx'), 'utf8');

describe('/traces sayımı listeyle aynı evren', () => {
  it('sayım isteği search taşır ve effect search değişince yeniden koşar', () => {
    const i = src.indexOf('api.tracesCount({');
    expect(i).toBeGreaterThan(0);
    const call = src.slice(i, src.indexOf('}, ctl.signal)', i));
    expect(call).toContain('search: filter.search || undefined');
    expect(call).toContain('service: filter.service || undefined');
    expect(call).toContain('filters: advGroupParam ? undefined');
    const deps = src.slice(src.indexOf('}, [showTotal, view, listRangeNs', i), src.indexOf(']);', src.indexOf('}, [showTotal, view, listRangeNs', i)));
    expect(deps).toContain('filter.search');
  });
  it('liste bitmişse (hasMore=false) kesin toplam listeden; sayılamıyor metni yalnız devam eden listede', () => {
    expect(src).toContain("{countRes?.reason && !hasMore ? (");
    expect(src).toContain('{(page * 50 + traces.length).toLocaleString()} total');
    expect(src).toContain("showing {traces.length}{hasMore ? '+' : ''} · toplam sayılamıyor");
  });
});
