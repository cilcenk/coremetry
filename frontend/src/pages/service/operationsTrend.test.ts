import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.697 — operatör: "buradaki sparkline'lar kullanışsız" (prod ekran
// görüntüsü). Trend hücresi üç mikro grafik yerine TEK geniş TrendSpark;
// diğer kolonlar ve sıralama aynen (klasik tablo kuralı, mockup onaylı).
const src = readFileSync(resolve(__dirname, 'OperationsTable.tsx'), 'utf8');

describe('Operations trend hücresi (v0.10.697)', () => {
  it('tek TrendSpark; üç ayrı Sparkline düğmesi yok', () => {
    expect(src).toContain('<TrendSpark calls={op.sparkline ?? []} errors={op.errorsSparkline ?? []} p99={op.p99Sparkline ?? []}');
    expect(src).not.toContain('color={TREND_C.errors} width='); // eski errors mikro grafiği
    expect(src).not.toContain('SPARK_W');
    expect(src).not.toContain("setOpFocus('errors'); setOpDetail(op);");
  });
  it('kolon genişliği tek grafiğe göre (200)', () => {
    expect(src).toContain("{ id: 'trend',     label: 'Trend',     width: 200 }");
  });
});
