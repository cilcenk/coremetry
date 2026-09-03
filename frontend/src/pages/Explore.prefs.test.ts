// Explore.prefs.test.ts — v0.10.320 (DataTable audit dilim 8) kaynak pini:
// trace sonuç kolonları için sunucu tercihi, Traces'tan AYRI anahtar; bir
// kez benimseme (deep-link cols= varken asla); kayıt yalnız sunucu
// modelinden farklıysa (benimseme kendini geri yazmaz).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Explore.tsx'), 'utf8');

describe('Explore sunucu sütun tercihi', () => {
  it("useTablePrefs('explore-traces') — Traces anahtarıyla paylaşılmaz", () => {
    expect(src).toContain("useTablePrefs('explore-traces')");
    expect(src).not.toContain("useTablePrefs('traces-list')");
    expect(src).toContain('if (prefs.model === undefined || prefsAdopted.current) return;');
    expect(src).toContain('if (urlHadCols.current || !prefs.model) return;');
  });
  it('kayıt yalnız sunucu modelinden farklıysa; benimseme öncesi asla', () => {
    expect(src.split('prefs.save(').length - 1).toBe(1);
    expect(src).toContain("if (server && server.join(',') === extraCols.join(',')) return;");
    expect(src).toContain('if (!prefsAdopted.current) return;');
  });
});
