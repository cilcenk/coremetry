import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { cosreChartDSL, cosreChartItems, cosreEmptyNoteTR, COSRE_SERIES_CAP } from './cosreChartSpec';
import type { SpanMetricSeries } from '@/lib/types';

// v0.9.1186 (AI Faz 4.4) — sohbet grafiğinin saf yarısı.
//
// Üç şey ölçülüyor ve üçü de sessiz-yanlış üretebilecek cinsten:
//   (1) DSL kaçışı. Yanlışsa sorgu servis-geneline DÜŞER ve operatör
//       daraltılmış sandığı bir grafiğe bakar (v0.9.187'nin dersi);
//   (2) seri sıralaması. Tavan ısırdığında kesilenler EN AZ ilgi çekenler
//       olmalı — alfabetik kesmek "z" ile başlayan patlayan endpoint'i
//       düşürürdü, ki kırılımın tek amacı onu bulmak;
//   (3) tavanın İLAN edilebilir olması (truncated/total).

const pt = (v: number | null) => ({ time: 0, value: v as number });
const s = (groupKey: string[] | undefined, values: (number | null)[]): SpanMetricSeries =>
  ({ groupKey, points: values.map(pt) } as unknown as SpanMetricSeries);

describe('cosreChartDSL', () => {
  it('servis-geneli', () => {
    expect(cosreChartDSL({ service: 'payments-api', agg: 'rate' }))
      .toBe('service.name = "payments-api"');
  });

  it('operasyon daraltması name alanına gider (http.route DEĞİL)', () => {
    expect(cosreChartDSL({ service: 'payments-api', operation: 'GET /orders/:id', agg: 'p99' }))
      .toBe('service.name = "payments-api" AND name = "GET /orders/:id"');
  });

  it('tırnak kaçırılır', () => {
    expect(cosreChartDSL({ service: 'a"b', operation: 'c"d', agg: 'rate' }))
      .toBe('service.name = "a\\"b" AND name = "c\\"d"');
  });

  it('kontrol karakterli operasyon DÜŞER, servis-geneline inilir', () => {
    // ParseDSL satırları \n ile bölüyor: böyle bir değer sorguyu bozardı.
    // Yanlış daraltmayla boş grafik göstermektense geniş ama doğru grafik.
    expect(cosreChartDSL({ service: 'api', operation: 'GET /x\nDROP', agg: 'rate' }))
      .toBe('service.name = "api"');
    expect(cosreChartDSL({ service: 'api', operation: 'a\tb', agg: 'rate' }))
      .toBe('service.name = "api"');
  });

  it('boş operasyon daraltmaz', () => {
    expect(cosreChartDSL({ service: 'api', operation: '', agg: 'rate' }))
      .toBe('service.name = "api"');
  });
});

describe('cosreChartItems', () => {
  const base = { service: 'api', agg: 'rate' as const };

  it('veri yoksa boş', () => {
    const r = cosreChartItems(base, []);
    expect(r.items).toEqual([]);
    expect(r.total).toBe(0);
    expect(r.truncated).toBe(false);
  });

  it('kırılımsız tek seri agg adıyla etiketlenir', () => {
    const r = cosreChartItems(base, [s(undefined, [1, 2])]);
    expect(r.items).toHaveLength(1);
    expect(r.items[0].name).toBe('rate');
    expect(r.unit).toBe('req/s');
  });

  it('kırılımsız error_rate hata rolünü alır', () => {
    const r = cosreChartItems({ ...base, agg: 'error_rate' }, [s(undefined, [1])]);
    expect(r.items[0].role).toBe('error');
    expect(r.unit).toBe('%');
  });

  it('KIRILIMDA rol verilmez — 8 kırmızı çizgi rolün anlamını yok eder', () => {
    const r = cosreChartItems({ ...base, agg: 'error_rate', groupBy: 'http.route' },
      [s(['/a'], [1]), s(['/b'], [2])]);
    expect(r.items.every(i => i.role === 'data')).toBe(true);
  });

  it('kırılımda grup değeri lejant adı olur', () => {
    const r = cosreChartItems({ ...base, groupBy: 'http.route' },
      [s(['/orders'], [5]), s(['/health'], [1])]);
    expect(r.items.map(i => i.name)).toEqual(['/orders', '/health']);
  });

  it('BÜYÜKLÜĞE göre sıralanır — alfabetik değil', () => {
    // "z" ile başlayan patlayan endpoint önde olmalı.
    const r = cosreChartItems({ ...base, groupBy: 'http.route' },
      [s(['/a'], [1, 1]), s(['/z'], [50, 50]), s(['/m'], [10])]);
    expect(r.items.map(i => i.name)).toEqual(['/z', '/m', '/a']);
  });

  it('null noktalar toplamı aşağı ÇEKMEZ', () => {
    // Oran serilerinde istek olmayan kova boş gelir; 0 saymak seriyi
    // haksız yere sıralamanın dibine atardı.
    const r = cosreChartItems({ ...base, groupBy: 'http.route' },
      [s(['/sparse'], [null, null, 100]), s(['/steady'], [5, 5, 5])]);
    expect(r.items[0].name).toBe('/sparse');
  });

  it('tavan ısırır ve İLAN EDİLİR', () => {
    const many = Array.from({ length: COSRE_SERIES_CAP + 5 }, (_, i) => s([`/r${i}`], [i]));
    const r = cosreChartItems({ ...base, groupBy: 'http.route' }, many);
    expect(r.items).toHaveLength(COSRE_SERIES_CAP);
    expect(r.truncated).toBe(true);
    expect(r.total).toBe(COSRE_SERIES_CAP + 5);
  });

  it('tavana TAM oturan küme kırpılmış SAYILMAZ', () => {
    const exact = Array.from({ length: COSRE_SERIES_CAP }, (_, i) => s([`/r${i}`], [i]));
    const r = cosreChartItems({ ...base, groupBy: 'http.route' }, exact);
    expect(r.truncated).toBe(false);
  });

  it('boş grup değeri adsız bırakılmaz', () => {
    const r = cosreChartItems({ ...base, groupBy: 'peer' }, [s([''], [1]), s([], [2])]);
    // İkisi de "(boş)" olurdu; indeks ekiyle lejantta üst üste binmezler.
    expect(new Set(r.items.map(i => i.name)).size).toBe(2);
    expect(r.items.every(i => i.name.startsWith('(boş)'))).toBe(true);
  });
});

// ── v0.10.43 — GRAFİK BİRİMİ MODEL KONTROLÜNDE OLAMAZ ───────────────────
//
// Copilot denetiminin bulgusu: `ChatBubble` cevap metnindeki HERHANGİ bir
// ```chart``` çitini canlı panele çeviriyor ve doğrulama
// `typeof … === 'string'` ile bitiyordu. `CosreChart` de
// `unit={spec.unit ?? unit}` diyordu — yani MODELİN yazdığı birim,
// agg'den türetileni EZİYORDU.
//
// Sonuç: model bir p99 grafiğine "%" etiketi yazabiliyor ve grafik
// GERÇEK gecikme verisiyle çiziliyor. Doğru veri + yanlış birim,
// düzyazıdan daha ikna edici bir hata — grafik daha yüksek güven taşır.
//
// ⚠ DÜZELTMEYİ RİSKSİZ KILAN ÖLÇÜM: sunucunun render_chart aracı spec'i
// TAM ÜÇ anahtarla kuruyor — {service, agg, rangeS}. `unit` ve `title`
// HİÇ üretilmiyor (internal/mcptools/tools.go). Yani bu iki alan arayüze
// ulaşıyorsa kaynağı YALNIZCA modelin kendi yazdığı çit olabilir;
// onları yok saymak meşru hiçbir grafiği etkilemiyor.
describe('grafik birimi/başlığı spec\'ten alınmıyor', () => {
  const src = readFileSync(new URL('./CosreChart.tsx', import.meta.url), 'utf8');

  it('birim YALNIZ agg\'den türetiliyor', () => {
    expect(src).toContain('unit={unit}');
    expect(src).not.toContain('spec.unit ?? unit');
  });

  it('başlık YALNIZ defaultTitle\'dan', () => {
    expect(src).toContain('title={defaultTitle(spec)}');
    expect(src).not.toContain('spec.title ?? defaultTitle');
  });

  it('birim kaynağı AGG_UNIT tablosu', () => {
    const spec = readFileSync(new URL('./cosreChartSpec.ts', import.meta.url), 'utf8');
    expect(spec).toContain("p99: 'ms'");
    expect(spec).toContain("error_rate: '%'");
  });
});

// ── v0.10.46 — SESSİZ BOŞ TUVAL ────────────────────────────────────────
//
// Denetimin kapatılmamış son maddesi. Çitin KÖKENİ arayüzde ayırt
// edilmiyor: `render_chart` aracının kurduğu meşru bir çit ile modelin
// kendi uydurduğu bir çit AYNI görünüyor. Model olmayan bir servis adı
// yazarsa sorgu geçerli koşar, sıfır seri döner ve operatör boş bir
// tuval görür — okunuşu "bu servis sessiz", yani ölçülmemiş bir sağlık
// beyanı.
//
// Boş grafik yanlış sayıdan tehlikeli: yanlış sayı sorgulanır, boşluk
// onaylanır.
describe('boş grafik ne demek olduğunu SÖYLER', () => {
  const spec = { service: 'checkout-service', agg: 'p99' } as const;

  it('kapsamı adıyla taşır', () => {
    expect(cosreEmptyNoteTR(spec)).toContain('checkout-service');
    expect(cosreEmptyNoteTR({ ...spec, operation: 'GET /pay' }))
      .toContain('checkout-service · GET /pay');
  });

  // ⚠ ASIL İDDİA. Metin "sessiz" okumasını ANMALI ve aynı cümlede
  // REDDETMELİ. Anmadan geçmek operatörün zaten yaptığı çıkarımı
  // düzeltmez; reddetmeden anmak onu pekiştirir.
  it('"servis sessiz" okumasını anar ve reddeder', () => {
    const t = cosreEmptyNoteTR(spec);
    expect(t).toContain('sessiz');
    expect(t).toContain('DEMEK DEĞİL');
  });

  it('doğrulanacak eylemi verir — belirsizliği ilan etmek yetmez', () => {
    expect(cosreEmptyNoteTR(spec)).toMatch(/doğrula/i);
  });

  // Kapsamsız çit AYRI bir arıza: sorgu hiç koşmuyor (enabled:false),
  // yani "veri yok" demek yalan olurdu — ölçülmedi.
  it('servis adı yoksa bunu VERİ YOKLUĞU diye sunmaz', () => {
    const t = cosreEmptyNoteTR({ service: '', agg: 'rate' });
    expect(t).toContain('çit hatası');
    expect(t).toContain('hiçbir sorgu çalıştırılmadı');
  });
});

// Muhafız: boş dal ÇİZİM YOLUNDAN ÖNCE gelmeli. Panel yine koşulsuz
// render edilirse yardımcı yeşil kalır ama operatör gene boş tuval
// görür — "test edilmiş ama ulaşılamaz" sınıfı.
describe('boş dal çizim yolundan önce', () => {
  const src = readFileSync(new URL('./CosreChart.tsx', import.meta.url), 'utf8');

  it('CosreChart boş durumu erken döner', () => {
    expect(src).toContain('cosreEmptyNoteTR');
    expect(src).toContain('q.isSuccess && items.length === 0');
    // Erken dönüş, CorePanelMulti render'ından ÖNCE olmalı.
    expect(src.indexOf('if (emptied)')).toBeGreaterThan(-1);
    expect(src.indexOf('if (emptied)')).toBeLessThan(src.indexOf('<CorePanelMulti'));
  });

  // Kapsamsız çit sorguyu hiç koşturmuyor (enabled:!!spec.service), yani
  // isSuccess ASLA true olmuyor. Yalnız isSuccess'e bakan bir muhafız o
  // yolu açıkta bırakırdı — sonsuz boş panel.
  it('kapsamsız çit de yakalanıyor', () => {
    expect(src).toContain('const noScope = !spec.service');
    expect(src).toContain('noScope ||');
  });
});
