import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';

// v0.10.451 (dış skill denetimi D3, kalan siteler) — tıklanabilir <tr>
// yalnız rowActivation ile (klavye eşdeğeri: role=button, tabIndex=0,
// Enter/Space). Kaynak taraması: sayfa/bileşen ağacında `<tr … onClick=`
// KALMAMALI. Tek muafiyet VirtualTable primitifi (onRowClick sözleşmesi;
// Traces gibi tüketiciler kendi rowActivation'ını uygular).
// KAPSAM (v0.10.451): tek satırlık `<tr … onClick=` şekli. Çok satırlı
// açılış etiketindeki 20 site (Clusters, Inbox, Hosts, Databases, …) bir
// sonraki D3 dilimi — tarama `[^>\n]*` ile bilinçli tek satır.
function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (/\.tsx$/.test(e) && !/\.test\.tsx$/.test(e)) out.push(p);
  }
  return out;
}
describe('clickable rows use rowActivation (D3)', () => {
  const root = resolve(__dirname, '..');
  it('no raw <tr … onClick= outside the VirtualTable primitive', () => {
    const offenders: string[] = [];
    for (const f of walk(root)) {
      if (f.endsWith('components/ui/DataTable/VirtualTable.tsx')) continue;
      const src = readFileSync(f, 'utf8');
      const n = (src.match(/<tr[^>\n]*\sonClick=/g) ?? []).length;
      if (n > 0) offenders.push(`${f.slice(root.length + 1)} (${n})`);
    }
    expect(offenders).toEqual([]);
  });
  it('the six D3-remaining sites adopted the helper', () => {
    for (const f of ['components/DBQueriesPanel.tsx', 'features/anomalies/AnomaliesPage.tsx', 'pages/AIObservability.tsx', 'pages/AdminElastic.tsx', 'pages/Profiling.tsx', 'pages/service/ServiceClusterPods.tsx']) {
      expect(readFileSync(join(root, f), 'utf8')).toContain('{...rowActivation(');
    }
  });
});
