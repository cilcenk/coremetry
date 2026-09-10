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
    expect(call).toContain('search: effectiveTraceSearch(filter)'); // v0.10.523 tek terim
    expect(call).toContain('service: filter.service || undefined');
    expect(call).toContain('filters: advGroupParam ? undefined');
    const deps = src.slice(src.indexOf('}, [view, listRangeNs', i), src.indexOf(']);', src.indexOf('}, [view, listRangeNs', i)));
    expect(deps).toContain('filter.search');
    expect(deps).toContain('filter.traceId'); // v0.10.523 kimlik kutusu da sayımı yeniler
  });
  it('liste bitmişse (hasMore=false) kesin toplam listeden; sayılamıyor metni yalnız devam eden listede', () => {
    expect(src).toContain("{countRes?.reason && !hasMore ? (");
    expect(src).toContain('{(page * 50 + traces.length).toLocaleString()} total');
    expect(src).toContain("showing {traces.length}{hasMore ? '+' : ''} · toplam sayılamıyor");
  });
});

// v0.10.522 — teşhis (explain) linki dolu listede de (admin): operatör dolu
// sayfada explain'e ulaşamadı ("steps rows göremedim"); link yalnız boş
// sonuç bileşenindeydi.
describe('/traces teşhis linki', () => {
  it('Pager extras içinde explainHref varsa "teşhis" linki; boş sonuç bileşeninde de kalır', () => {
    const i = src.indexOf('<Pager mode="offset" count="skip"');
    expect(i).toBeGreaterThan(0);
    const extras = src.slice(i, src.indexOf('</Pager>', i) > 0 ? src.indexOf('</Pager>', i) : i + 4000);
    expect(extras).toContain('{explainHref && (');
    expect(extras).toContain('>teşhis</a>');
    expect(src).toContain('Teşhis (explain) →');
  });
});
