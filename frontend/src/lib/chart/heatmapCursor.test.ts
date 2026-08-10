// heatmapCursor — UX denetimi D5 / Ö26 regresyon testleri (v0.9.948).
//
// Orijinal belirti: RED grafiklerinde gezerken LatencyHeatmap'te imleç izi
// ÇIKMIYORDU — bileşenin kendi dosya-başı yorumu ("Same time axis as the
// metric line chart so an operator can flip between the two views and read
// the same window") paylaşılan bir zaman ekseni VAAT EDİYORDU.
//
// Kök neden: heatmap bir CANVAS, uPlot.sync yalnız uPlot örneklerini
// senkronlar. Vaadin taşıyıcısı olamayacak bir mekanizmaya bel bağlanmıştı.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { heatmapCursorCol, heatmapCursorX } from './heatmapCursor';

// 5 dakikalık kovalar, unix NANOS (LatencyHeatmap veri şekli).
const T0 = 1_700_000_000e9;
const STEP = 300e9;
const times = [T0, T0 + STEP, T0 + 2 * STEP, T0 + 3 * STEP];
const sec = (ns: number) => ns / 1e9;

describe('heatmapCursorCol — kova eşlemesi', () => {
  it('tam kova başlangıcı kendi kovasına düşer', () => {
    expect(heatmapCursorCol(times, sec(T0))).toBe(0);
    expect(heatmapCursorCol(times, sec(T0 + 2 * STEP))).toBe(2);
  });

  it('kova ORTASINDAKİ zaman o kovaya düşer', () => {
    expect(heatmapCursorCol(times, sec(T0 + STEP + STEP * 0.4))).toBe(1);
  });

  it('EN YAKIN kovaya yuvarlar, aşağı kesmez', () => {
    // Kova 1'in %90'ında olan imleç, kova 2'ye daha yakındır.
    expect(heatmapCursorCol(times, sec(T0 + STEP * 1.9))).toBe(2);
  });

  it('boş ızgara null', () => {
    expect(heatmapCursorCol([], sec(T0))).toBeNull();
  });

  it('sonlu olmayan imleç null', () => {
    expect(heatmapCursorCol(times, NaN)).toBeNull();
    expect(heatmapCursorCol(times, Infinity)).toBeNull();
  });
});

describe('heatmapCursorCol — PENCERE DIŞI kelepçelenmez', () => {
  // Kelepçe, kardeş grafik daha geniş bir aralık çizdiğinde (heatmap 6h,
  // çizgi 24h) izi kenara YAPIŞTIRIRDI: operatör var olmayan bir anı
  // işaretlenmiş görürdü. Yarım kova tolerans ızgaranın çözünürlüğü.
  it('sol kenarın yarım kova ötesi null', () => {
    expect(heatmapCursorCol(times, sec(T0 - STEP * 0.6))).toBeNull();
  });

  it('sağ kenarın yarım kova ötesi null', () => {
    expect(heatmapCursorCol(times, sec(T0 + 3 * STEP + STEP * 0.6))).toBeNull();
  });

  it('yarım kova İÇİNDE hâlâ eşleşir (kenar hücresi kaybolmaz)', () => {
    expect(heatmapCursorCol(times, sec(T0 - STEP * 0.4))).toBe(0);
    expect(heatmapCursorCol(times, sec(T0 + 3 * STEP + STEP * 0.4))).toBe(3);
  });

  it('tek kovalı ızgarada 1 dk tolerans kullanılır', () => {
    expect(heatmapCursorCol([T0], sec(T0 + 20e9))).toBe(0);
    expect(heatmapCursorCol([T0], sec(T0 + 120e9))).toBeNull();
  });
});

describe('heatmapCursorX — sütun MERKEZİ', () => {
  it('izi kovanın ortasına koyar, kenarına değil', () => {
    // Kenara koymak, operatörün bir kova yandaki hücreyi okuduğunu
    // sanmasına yol açardı.
    expect(heatmapCursorX(0, 56, 10)).toBe(61);
    expect(heatmapCursorX(3, 56, 10)).toBe(91);
  });
});

describe('kablolama (v0.9.948)', () => {
  const SRC = join(__dirname, '..', '..');
  const heat = readFileSync(join(SRC, 'components/LatencyHeatmap.tsx'), 'utf8');

  it('heatmap paylaşılan kanala ABONE', () => {
    expect(heat).toMatch(/useCursorTime\(\)/);
    expect(heat).toMatch(/from '@\/lib\/chart\/cursorBus'/);
  });

  it('iz KENDİ hover’ımız varken çizilmez — iki dikey çizgi yanıltır', () => {
    expect(heat).toMatch(/sharedCursorSec != null && hover == null/);
  });

  it('canvas YENİDEN BOYANMAZ — iz mutlak konumlu bir katman', () => {
    // 60fps mousemove yolunda canvas repaint'i bu bileşenin tüm
    // performans gerekçesini (dosya başı yorumu) çöpe atardı.
    const block = heat.slice(heat.indexOf('sharedCursorSec != null'));
    expect(block.slice(0, 600)).toMatch(/position: 'absolute'/);
  });

  it('kardeş uPlot panelleri YAYIN yapıyor — yarım kablo yok', () => {
    for (const rel of ['pages/service/DetailsMetricsSection.tsx', 'components/ServiceCharts.tsx']) {
      const src = readFileSync(join(SRC, rel), 'utf8');
      expect(src, `${rel} yayın yapmıyor — abone sessiz kalır`).toMatch(/onCursorTime=\{publishCursor\}/);
    }
  });

  it('MLC kanalı İLETİYOR (yutmuyor)', () => {
    const mlc = readFileSync(join(SRC, 'components/MultiLineChart.tsx'), 'utf8');
    expect(mlc).toMatch(/onCursorTime=\{onCursorTime\}/);
  });
});
