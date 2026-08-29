// v0.10.151 (operatör) — Service Details bölüm sırası: Properties → Clusters →
// Database → Performance → Latency → Runtime & rollouts; sağ ray (DetailsToc)
// sayfayla aynı sırada. Kaynak taraması: sıra kayarsa kırmızı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const order = (src: string, ids: string[]) => ids.map(id => src.indexOf(id));
const ascending = (xs: number[]) => xs.every((x, i) => x > -1 && (i === 0 || x > xs[i - 1]));

describe('Service Details section order (v0.10.151)', () => {
  it('page: clusters and database come before performance', () => {
    const src = readFileSync(join(__dirname, 'Service.tsx'), 'utf8');
    expect(ascending(order(src, ['id="dtl-props"', 'id="dtl-clusters"', 'id="dtl-db"', 'id="dtl-perf"', 'id="dtl-latency"', 'id="dtl-runtime"']))).toBe(true);
    expect(src).toMatch(/<ServiceClusterBreakdown service=\{svc\} range=\{range\} \/>/);
  });
  it('toc follows the page order', () => {
    const src = readFileSync(join(__dirname, 'service/DetailsToc.tsx'), 'utf8');
    expect(ascending(order(src, ["'dtl-props'", "'dtl-clusters'", "'dtl-db'", "'dtl-perf'", "'dtl-latency'", "'dtl-runtime'"]))).toBe(true);
  });
});
