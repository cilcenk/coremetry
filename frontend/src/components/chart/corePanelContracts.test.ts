import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { seriesRoleColor } from '@/lib/chart/seriesRole';
import { visibleRangeStats } from '@/lib/chart/visibleStats';
import { seriesColor } from '@/lib/chartFmt';

// CorePanel sözleşme testleri (FAZ 2B). Bileşenin kendisi jsdom+canvas
// ister (uPlot 2D context) — o katman FAZ 2 self-review'da bilinçli borç.
// Burada korunanlar: saf çekirdekler + TEK-SARMALAYICI tekeli.

describe('seriesRoleColor', () => {
  it('semantik roller token döndürür — tema-canlı', () => {
    expect(seriesRoleColor('errors', 'error')).toBe('var(--err)');
    expect(seriesRoleColor('ok', 'success')).toBe('var(--ok)');
    expect(seriesRoleColor('other', 'muted')).toBe('var(--text3)');
  });

  it('data rolü mevcut deterministik palete düşer — YENİDEN yazılmadı', () => {
    expect(seriesRoleColor('payments', 'data')).toBe(seriesColor('payments'));
    expect(seriesRoleColor('payments')).toBe(seriesRoleColor('payments'));
  });

  it('rol etiketten TAHMİN EDİLMEZ: "error" adlı seri data rolünde palet alır', () => {
    // "error-budget-service" bir VERİ serisidir; kırmızıya zorlamak yanlış.
    expect(seriesRoleColor('error', 'data')).toBe(seriesColor('error'));
    expect(seriesRoleColor('error', 'data')).not.toBe('var(--err)');
  });
});

describe('visibleRangeStats', () => {
  const t = [10, 20, 30, 40, 50];
  const v = [1, null, 3, 4, 5];

  it('tam pencere = tüm dizi', () => {
    const s = visibleRangeStats(t, v, 0, 100);
    expect(s.count).toBe(4);
    expect(s.sum).toBe(13);
  });

  it('zoom penceresi kırpar — sınırlar DAHİL', () => {
    const s = visibleRangeStats(t, v, 20, 40);
    expect(s.count).toBe(2); // 20→null atlanır, 30 ve 40 girer
    expect(s.min).toBe(3);
    expect(s.max).toBe(4);
    expect(s.last).toBe(4);
  });

  it('null istatistiğe girmez — 0 sayılmaz', () => {
    const s = visibleRangeStats(t, [null, null, 2, null, null], 0, 100);
    expect(s.count).toBe(1);
    expect(s.mean).toBe(2); // null'lar 0 sayılsaydı mean 0.4 olurdu
  });

  it('pencere dışı → boş istatistik', () => {
    const s = visibleRangeStats(t, v, 60, 90);
    expect(s.count).toBe(0);
    expect(s.last).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// TEK-SARMALAYICI TEKELİ. dataFrame.test.ts'teki köprü tekelinin kardeşi:
// UPlotChart/UPlotConfigBuilder importu YALNIZ CorePanel.tsx'te yasal.
// Bir sayfa doğrudan import ederse dört-kopya-chart hastalığı @grafana/ui
// katmanında yeniden başlar (v0.9.97 engine.ts'in kapattığı borç).
// ---------------------------------------------------------------------------
describe('CorePanel tekeli', () => {
  const SRC = resolve(__dirname, '../..');

  function* walk(dir: string): Generator<string> {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = resolve(dir, e.name);
      if (e.isDirectory()) yield* walk(p);
      else if (/\.(ts|tsx)$/.test(e.name) && !/\.test\./.test(e.name)) yield p;
    }
  }

  it('UPlotChart/UPlotConfigBuilder yalnız CorePanel.tsx içinden', () => {
    const offenders: string[] = [];
    for (const f of walk(SRC)) {
      if (f.endsWith('components/chart/CorePanel.tsx')) continue;
      const src = readFileSync(f, 'utf8');
      if (/from '@grafana\/ui'/.test(src) && /UPlotChart|UPlotConfigBuilder/.test(src)) {
        offenders.push(f);
      }
    }
    expect(offenders, 'CorePanel dışında @grafana/ui chart primitifi').toEqual([]);
  });
});
