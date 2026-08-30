// podSectionOrder.test.ts — v0.10.177 (operatör, prod pod görüntüsü): Thanos
// CPU/Bellek trend bölümü KPI şeridinin hemen altında, servis (RED)
// metriklerinin ÜSTÜNDE — pod'un kendi sinyali önce; span'siz pod'da RED boş.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('pod sayfası bölüm sırası (v0.10.177)', () => {
  const src = readFileSync(resolve(__dirname, '../Pod.tsx'), 'utf8');
  it('KPI → CPU / Bellek · Thanos → Servis metrikleri', () => {
    const kpi = src.indexOf('<PodKpiStrip');
    const infra = src.indexOf('CPU / Bellek · Thanos');
    const trend = src.indexOf('<ThanosTrendPanel');
    const red = src.indexOf('Servis metrikleri · bu pod');
    expect(kpi).toBeGreaterThan(0);
    expect(infra).toBeGreaterThan(kpi);
    expect(trend).toBeGreaterThan(infra);
    expect(red).toBeGreaterThan(trend);
  });
});
