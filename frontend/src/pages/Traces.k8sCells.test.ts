// Traces.k8sCells.test.ts — v0.10.330 (operatör, prod): K8s kolon hücreleri
// çip/link DEĞİL, tek satır düz metin (üç nokta + title); satır linkine sarılır.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Traces.tsx'), 'utf8');

describe('Traces K8s hücreleri', () => {
  it('çip/link yok, cell-ellipsis var, ownLink kapalı', () => {
    expect(src).not.toContain('className="mono sec"');
    expect(src).not.toMatch(/entity sayfası \(trace anı\)/);
    expect(src).toContain('<span className="mono cell-ellipsis" style={{ color: \'var(--text2)\' }} title={v}>{v}</span>');
    expect(src).toContain('const ownLink = false;');
    expect(src).not.toMatch(/import \{[^}]*traceK8sHref[^}]*\} from '@\/lib\/traceK8sLinks'/);
  });
  it('K8s başlangıç genişlikleri daraltıldı (1440 px bütçesi)', () => {
    expect(src).toContain("'k8s.pod.name': 220, 'k8s.namespace.name': 130, 'k8s.node.name': 170, cluster: 90,");
  });
});
