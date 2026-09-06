import { describe, it, expect } from 'vitest';
import { depInstanceLabel, depPillLines } from './topoLabels';

// v0.8.297 (operator-reported: db pill'inde "sadece oracle yazıyor") — the
// dependency pill's sub-line prefers the concrete identity over the generic
// kind label. Contract of depInstanceLabel:
//   1. db.name wins when present ("COREBANK");
//   2. else the @instance suffix of the node id ("db:oracle@oracle-prod" →
//      "oracle-prod") — UNLESS it just repeats the system name
//      ("db:redis@redis" adds nothing);
//   3. else null — caller falls back to the generic kind label.
describe('depInstanceLabel', () => {
  it('prefers db.name when present', () => {
    expect(depInstanceLabel({ service: 'db:oracle@oracle', subkind: 'oracle', dbName: 'COREBANK' })).toBe('COREBANK');
  });

  it('falls back to the @instance suffix', () => {
    expect(depInstanceLabel({ service: 'db:postgresql@pg-payments', subkind: 'postgresql' })).toBe('pg-payments');
  });

  it('suppresses an @instance that just repeats the system', () => {
    expect(depInstanceLabel({ service: 'db:redis@redis', subkind: 'redis' })).toBeNull();
  });

  it('bare node id without @ or db.name yields null', () => {
    expect(depInstanceLabel({ service: 'db:clickhouse', subkind: 'clickhouse' })).toBeNull();
  });

  it('empty db.name is treated as absent', () => {
    expect(depInstanceLabel({ service: 'db:mysql@my-1', subkind: 'mysql', dbName: '' })).toBe('my-1');
  });
});

// v0.10.517 (operatör: "oracle altta, db name üstte") — somut kimlik başlığa,
// sistem alt satıra; kimlik yoksa eski düzen.
describe('depPillLines', () => {
  it('db.name başlık, sistem alt satır', () => {
    expect(depPillLines({ service: 'db:oracle@orablog-prd', subkind: 'oracle', dbName: 'pgts02_prd' }, 'database'))
      .toEqual({ title: 'pgts02_prd', sub: 'oracle' });
  });
  it('@instance başlık, sistem alt satır', () => {
    expect(depPillLines({ service: 'db:postgresql@pg-payments', subkind: 'postgresql' }, 'database'))
      .toEqual({ title: 'pg-payments', sub: 'postgresql' });
  });
  it('kimlik yoksa sistem başlık, tür etiketi alt satır (eski düzen)', () => {
    expect(depPillLines({ service: 'db:redis@redis', subkind: 'redis' }, 'cache'))
      .toEqual({ title: 'redis', sub: 'cache' });
    expect(depPillLines({ service: 'queue:kafka' }, 'queue')).toEqual({ title: 'kafka', sub: 'queue' });
  });
  it('kaynak pini: TopologyFlowGraph bağımlılık pilini depPillLines ile çizer', () => {
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const fs = require('node:fs') as typeof import('node:fs');
    const src = fs.readFileSync(new URL('../components/TopologyFlowGraph.tsx', import.meta.url), 'utf8');
    expect(src).toContain('depPillLines(n, depLabel(n.kind!))');
    expect(src).not.toContain('depInstanceLabel(n) ?? depLabel(n.kind!)');
  });
});
