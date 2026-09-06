import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';

// v0.10.451 (dış skill denetimi D3, kalan siteler) — tıklanabilir <tr>
// yalnız rowActivation ile (klavye eşdeğeri: role=button, tabIndex=0,
// Enter/Space). Kaynak taraması: sayfa/bileşen ağacında `<tr … onClick=`
// KALMAMALI. Tek muafiyet VirtualTable primitifi (onRowClick sözleşmesi;
// Traces gibi tüketiciler kendi rowActivation'ını uygular).
// v0.10.455 (dilim 3): tarama ÇOK SATIRLI açılış etiketlerini de kapsar.
// Olayı okuyan tık işleyicileri (Ctrl/⌘-tık) onClick'i korur ama aynı
// etikette rowKeyboard( ile klavye yarısını taşımak ZORUNDADIR — tarama
// yalnız onu muaf tutar.
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
      const n = (src.match(/<tr[^>]*\sonClick=/g) ?? []).filter(tag => !tag.includes('rowKeyboard(')).length;
      if (n > 0) offenders.push(`${f.slice(root.length + 1)} (${n})`);
    }
    expect(offenders).toEqual([]);
  });
  it('the D3 sites adopted the helper', () => {
    for (const f of ['components/DBQueriesPanel.tsx', 'features/anomalies/AnomaliesPage.tsx', 'pages/AIObservability.tsx', 'pages/AdminElastic.tsx', 'pages/Profiling.tsx', 'pages/service/ServiceClusterPods.tsx', 'pages/Clusters.tsx', 'pages/Inbox.tsx', 'pages/Hosts.tsx', 'pages/Databases.tsx', 'pages/SlowQueries.tsx', 'pages/service/OverviewTables.tsx', 'components/LogPatternsPanel.tsx']) {
      expect(readFileSync(join(root, f), 'utf8')).toContain('{...rowActivation(');
    }
    for (const f of ['components/chart/StatsLegend.tsx', 'components/viz/TimeSeriesPanel.tsx', 'pages/Metrics.tsx']) {
      expect(readFileSync(join(root, f), 'utf8')).toContain('rowKeyboard(');
    }
  });
});
