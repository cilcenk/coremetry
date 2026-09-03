// VolumeChart.legend.test.ts — v0.10.321 (operatör): /traces şeridinin
// Series lejantı VARSAYILAN KAPALI. Kaynak pini: v0.10.268 "açık" kararına
// sessiz dönüş kırmızı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('VolumeChart lejant varsayılanı', () => {
  it('legendCollapsed={true}', () => {
    const src = readFileSync(resolve(__dirname, 'VolumeChart.tsx'), 'utf8');
    expect(src).toContain('legendCollapsed={true}');
    expect(src).not.toContain('legendCollapsed={false}');
  });
});
