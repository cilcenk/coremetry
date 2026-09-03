// Endpoints.prefs.test.ts — v0.10.319 (DataTable audit dilim 8) kaynak pini:
// sunucu sütun tercihi; benimseme yalnız URL'de cols yokken ve bir kez;
// kaydetme yalnız ColumnToggle değişikliğinde (deep-link sunucuyu ezmez).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Endpoints.tsx'), 'utf8');

describe('Endpoints sunucu sütun tercihi', () => {
  it("useTablePrefs('endpoints') + bir kez benimseme + URL öncelik kapısı", () => {
    expect(src).toContain("useTablePrefs('endpoints')");
    expect(src).toContain('if (prefs.model === undefined || prefsAdopted.current) return;');
    expect(src).toContain('if (urlHadCols.current || !prefs.model) return;');
  });
  it('prefs.save tek noktada (changeVisibleCols) ve ColumnToggle ona bağlı', () => {
    expect(src.split('prefs.save(').length - 1).toBe(1);
    const i = src.indexOf('prefs.save(');
    expect(i).toBeGreaterThan(src.indexOf('const changeVisibleCols = '));
    expect(src).toContain('onChange={changeVisibleCols} />');
    expect(src).not.toContain('onChange={setVisibleCols} />');
  });
});
