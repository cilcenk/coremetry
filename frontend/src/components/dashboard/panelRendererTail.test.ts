// v0.10.147 — kuyruk ön-toplamı DashChart'a ULAŞIR (kaynak taraması):
// SpanMetricPanel tail + totalSeries'i iki yoldan da (bundle override, kendi
// fetch'i) state'e alıp DashChart'a geçirir; DashChart foldTopN'e tail'i,
// foldNote'a gerçek toplamı verir. "Test edilmiş ama ulaşılamaz" sınıfına
// karşı (saf çekirdek yeşil, çağrı yolu kopuk).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

describe('DashChart tail plumbing (v0.10.147)', () => {
  // Yorumlar soyulur: bu dosyada şerhler tam bu adları tarih anlatmak için
  // kullanıyor; soymazsan gate kendi dokümantasyonuna ısırır.
  const src = readFileSync(join(__dirname, 'PanelRenderer.tsx'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '');
  it('DashChart folds with the server tail and notes the true total', () => {
    expect(src).toMatch(/foldTopN\(series, unit, undefined, tail\)/);
    expect(src).toMatch(/foldNote\(totalSeries \?\? series\.length\)/);
  });
  it('SpanMetricPanel forwards tail/totalSeries from both data paths', () => {
    const i = src.indexOf('function SpanMetricPanel(');
    expect(i).toBeGreaterThan(-1);
    // Bir sonraki üst-düzey fonksiyona kadar: başka bir panelin kopyası
    // SpanMetricPanel'i sessizce geri alırken gate'i yeşil tutamasın.
    const next = src.indexOf('\nfunction ', i + 1);
    const body = src.slice(i, next > 0 ? next : undefined);
    expect(body).toMatch(/setTail\(dataOverride!\.tail\)/);
    expect(body).toMatch(/setTotal\(dataOverride!\.totalSeries\)/);
    expect(body).toMatch(/setTail\(r\?\.tail\)/);
    expect(body).toMatch(/setTotal\(r\?\.totalSeries\)/);
    expect(body).toMatch(/<DashChart[^>]*series=\{series\} tail=\{tail\} totalSeries=\{total\}/s);
  });
});
