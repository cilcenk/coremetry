// v0.10.146 — düzenleme modunda bundle override'ı paneli bypass eder (kaynak
// taraması). Grid taslak panelleri çizerken kayıtlı doc'tan üretilen bundle
// slot'unu geçirirse taslak konfig ekrana yansımaz: dolu slot bayat kalır,
// boş slot ("series": []) "No data"da donar. PanelEditor'ün ayrı önizlemesi
// olmadığı için grid paneli önizlemenin kendisidir → editing iken
// dataOverride undefined olmalı (panel kendi fetch'ini taslakla yapar).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

describe('dashboard edit preview (v0.10.146)', () => {
  it('DashboardGrid passes no bundle override while editing', () => {
    const src = readFileSync(join(__dirname, 'Dashboard.tsx'), 'utf8');
    const i = src.indexOf('function DashboardGrid(');
    expect(i).toBeGreaterThan(-1);
    const grid = src.slice(i);
    expect(grid).toMatch(/dataOverride=\{editing \? undefined : bundlePanelData\[p\.id\]\}/);
    // Şekil: bir başka koşulsuz geçiş kalmamalı.
    expect(grid).not.toMatch(/dataOverride=\{bundlePanelData\[p\.id\]\}/);
  });
});
