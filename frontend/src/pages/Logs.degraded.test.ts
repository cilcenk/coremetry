// v0.10.415 — log arama denetimi B1: üç yüzey de sunucunun 200 {degraded}
// sözleşmesini OKUR. Kaynak pini (Logs.prefs.test deseni): bayrağı okuyan
// satır kaybolursa yavaş backend gene "No logs found" gibi görünür.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

describe('degraded log backend surfaces (B1)', () => {
  it('Logs.tsx: rozet + degraded boş durum + normal boş durum kapısı', () => {
    const src = read('./Logs.tsx');
    expect(src).toContain("!live && !!staticQ.data?.degraded && (");
    expect(src).toContain('title="Log backend yavaş — bu liste eksik"');
    expect(src).toContain("(live || !staticQ.data?.degraded) && (");
  });
  it('LogFieldsPanel: degraded ve partial "No values" ile karışmaz', () => {
    const src = read('../components/LogFieldsPanel.tsx');
    expect(src).toContain('d?.degraded &&');
    expect(src).toContain('d?.partial &&');
    expect(src).toContain('d && !d.degraded && d.values.length === 0');
  });
  it('LogContextModal: degraded state okunur, sıfırlanır, çizilir', () => {
    const src = read('../components/LogContextModal.tsx');
    expect(src).toContain("setDegraded(r?.degraded ?");
    expect((src.match(/setDegraded\(null\)/g) ?? []).length).toBeGreaterThanOrEqual(3);
    expect(src).toContain('{degraded && (');
  });
});
