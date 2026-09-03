// dbSlowQuery.test.ts — v0.10.325 kaynak pini: Anomaly sekmesinde yavaş SQL
// bölümü, EscalationSection'dan sonra; istemci iki uç; tip alanları spec ile.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('DB yavaş sorgu ayarları', () => {
  const tab = readFileSync(resolve(__dirname, 'AnomalyTab.tsx'), 'utf8');
  const api = readFileSync(resolve(__dirname, '../../lib/api.ts'), 'utf8');
  const types = readFileSync(resolve(__dirname, '../../lib/types.ts'), 'utf8');
  it('bölüm monte, istemci ve tip mevcut', () => {
    expect(tab.indexOf('<DBSlowQuerySection />')).toBeGreaterThan(tab.indexOf('<EscalationSection />'));
    expect(tab).toContain('function DBSlowQuerySection()');
    expect(api).toContain('`/api/settings/db-slow-query`');
    for (const f of ['thresholdMs', 'criticalMs', 'minExecutions', 'forBuckets', 'cooldownSec']) {
      expect(types).toMatch(new RegExp(`export interface DBSlowQueryConfig \\{[^}]*${f}: number`));
    }
  });
});
