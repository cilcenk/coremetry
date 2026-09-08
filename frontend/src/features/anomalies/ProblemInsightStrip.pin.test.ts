// ProblemInsightStrip.pin.test.ts — v0.10.562: şerit ilk ekranın (.pd-cols) üstünde;
// hata sayfayı bozmaz (null döner).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const detail = readFileSync(resolve(__dirname, 'ProblemDetail.tsx'), 'utf8');
const strip = readFileSync(resolve(__dirname, 'ProblemInsightStrip.tsx'), 'utf8');

describe('ProblemInsightStrip', () => {
  it('yerleşim: pd-cols öncesi', () => {
    const s = detail.indexOf('<ProblemInsightStrip problemId={problem.id} />');
    const cols = detail.indexOf('<div className="pd-cols pd-cols-15">');
    expect(s).toBeGreaterThan(0);
    expect(s).toBeLessThan(cols);
  });
  it('hata → null, yükleniyor → soluk satır', () => {
    expect(strip).toContain('if (q.isError || !q.data) return null;');
    expect(strip).toContain('ins-strip--pending');
  });
});
