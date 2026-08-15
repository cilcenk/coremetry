import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.1054 (Faz 0.6) — /problems filtreleri URL'de yaşar (sayfa "URL =
// source of truth" kuralının tek ihlaliydi: sev/prio localStorage+state,
// owner/sre/cluster düz state — "şu P1'lere bak" linki karşıda başka
// görünüm açıyordu, SavedViewsBar boş kayıt yazıyordu). Kaynak pini:
// filtre eksenleri searchParams'tan türetilir, Set-state'e geri dönüş
// (URL→state import döngüsü, v0.8.253 sınıfı) sessizce yeniden doğamaz.

describe('/problems URL-state (v0.9.1054)', () => {
  const src = readFileSync(
    resolve(__dirname, 'ProblemsSection.tsx'), 'utf8');

  it('sev/prio/status/owner/sre/cluster URL parametrelerinden okunur', () => {
    for (const param of ["searchParams.get('sev')", "searchParams.get('prio')",
      "searchParams.get('status')", "searchParams.get('owner')",
      "searchParams.get('sre')", "searchParams.get('cluster')"]) {
      expect(src, `${param} yok — filtre URL'den kopmuş`).toContain(param);
    }
  });

  it('filtre eksenleri için Set-state geri dönmedi (bulk-select hariç)', () => {
    expect(src).not.toMatch(/set(Sev|Prio)Set\]\s*=\s*useState/);
    expect(src).not.toMatch(/\[(ownerTeam|sreTeam|cluster),\s*set\w+\]\s*=\s*useState/);
  });

  it('yazımlar tek setParam üzerinden, codec inboxUrl paylaşımlı', () => {
    expect(src).toContain("from '@/lib/inboxUrl'");
    expect(src).toMatch(/const setParam = \(k: string, v: string \| null\)/);
  });
});
