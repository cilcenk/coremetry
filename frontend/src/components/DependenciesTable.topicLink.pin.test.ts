// v0.10.586 — /messaging listesinde topic ADI detay sayfasına gider, Traces'e
// değil (operatör 2026-09-10). DB dalı DOKUNULMADI: Explore/Traces pivotu
// kalır. Yorumlar süzülür; iddialar exploreHref'in gövdesine çapalı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const SRC = readFileSync(new URL('./DependenciesTable.tsx', import.meta.url), 'utf8');
const CODE = SRC.replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');
const start = CODE.indexOf('const exploreHref');
const body = CODE.slice(start, CODE.indexOf('\n  };', start));

describe('DependenciesTable topic linki', () => {
  it('queue dalı messagingTopicHref kullanır', () => {
    expect(start).toBeGreaterThan(-1);
    expect(body).toContain('messagingTopicHref(');
  });
  it('queue dalı artık Traces\'e GİTMEZ', () => {
    expect(body).not.toContain('messagingTracesHref(');
  });
  it('cluster kimliğin parçası — (default) yedeğiyle', () => {
    expect(body).toMatch(/cluster:\s*r\.cluster \?\? '\(default\)'/);
  });
  it('DB dalı korunur', () => {
    expect(body).toContain('dbTracesHref(');
  });
});
