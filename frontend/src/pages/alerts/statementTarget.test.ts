// statementTarget.test.ts — v0.10.331 (hedefli DB ifadesi kuralı) kaynak pini +
// saf yardımcılar: Alerts formunda seçici + metrik ailesi + koşul hücresi,
// Statement sayfasında modal, istemci + tipler.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defaultStatementRuleName } from './StatementAlertModal';
import { statementTargetOf } from './StatementPicker';
import { isDbStmtMetric, DB_STMT_METRICS } from './constants';

const root = resolve(__dirname, '..');
const alerts = readFileSync(resolve(root, 'Alerts.tsx'), 'utf8');
const stmt = readFileSync(resolve(root, 'StatementDetail.tsx'), 'utf8');
const api = readFileSync(resolve(root, '../lib/api.ts'), 'utf8');
const types = readFileSync(resolve(root, '../lib/types.ts'), 'utf8');

describe('DB statement hedefli kural', () => {
  it('saf yardımcılar', () => {
    expect(defaultStatementRuleName('  SELECT   *  FROM t ')).toBe('Slow SQL: SELECT * FROM t');
    expect(defaultStatementRuleName('x'.repeat(80))).toMatch(/…$/);
    expect(defaultStatementRuleName('')).toBe('Slow SQL: statement');
    const t = statementTargetOf({ dbSystem: 'oracle', dbName: 'CRD', stmtHash: '42', sample: 'SELECT 1', execs: 3, p95Ms: 1, services: ['a'] });
    expect(t).toEqual({ kind: 'db_statement', dbSystem: 'oracle', dbName: 'CRD', stmtHash: '42', sample: 'SELECT 1' });
    expect(isDbStmtMetric('db_stmt_p95_ms')).toBe(true);
    expect(isDbStmtMetric('p99_ms')).toBe(false);
    expect(DB_STMT_METRICS[0].v).toBe('db_stmt_p95_ms'); // spec: p95 varsayılan
  });
  it('Alerts formu: seçici, metrik ailesi, düz eşik, koşul hücresi, tür etiketi', () => {
    expect(alerts).toContain('<StatementPicker value={draft.target}');
    expect(alerts).toContain('(draft.target ? DB_STMT_METRICS : METRICS)');
    expect(alerts).toContain("r.target ? 'DB STATEMENT'");
    expect(alerts).toContain("isTarget ? '— all callers —'");
  });
  it('Statement sayfası: yazma rolüne Alarm oluştur + modal', () => {
    expect(stmt).toContain('<StatementAlertModal open onClose={() => setAlertOpen(false)}');
    expect(stmt).toContain("canEditRules = user?.role === 'admin' || user?.role === 'editor'");
    expect(stmt).toContain('⚠ Alarm oluştur');
  });
  it('istemci + tip', () => {
    expect(api).toContain('`/api/db/statements/search?${qs({ q, limit })}`');
    expect(types).toMatch(/export interface RuleTarget \{\s*kind: 'db_statement';/);
    expect(types).toMatch(/export interface AlertRule \{[^}]*target\?: RuleTarget;/);
  });
});
