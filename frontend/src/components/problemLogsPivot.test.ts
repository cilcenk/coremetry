// v0.10.419 — log arama denetimi C2: Problem → Logs hata pivotu. Trace
// linki hasError taşıyor, log linki seviye taşımıyordu. Kaynak pini:
// EK link (severity: 17 = ERROR+) var, "tüm loglar" linki dokunulmamış.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

describe('Problem → Logs error pivot (C2)', () => {
  it('ProblemDetail: iki log linki — tümü ve yalnız hatalar', () => {
    const src = read('../features/anomalies/ProblemDetail.tsx');
    expect(src).toContain('logsHref({ window: probWindow, service: problem.service })');
    expect(src).toContain('logsHref({ window: probWindow, service: problem.service, severity: 17 })');
    expect(src).toContain('label="≡ Logs (yalnız hatalar)"');
    // v0.10.449 — üçüncü link: desen paneli açık iner; ilk ikisi aynen.
    expect(src).toContain("logsHref({ window: probWindow, service: problem.service, panel: 'patterns' })");
    expect(src).toContain('label="≡ Loglar (desenler)"');
  });
  it('ProblemDetail: log kanıtı paneli servis kapısının içinde, pencere problemWindowNs (v0.10.452)', () => {
    const src = read('../features/anomalies/ProblemDetail.tsx');
    expect(src).toContain("subjectKind(problem.service, problem.kind) === 'service' && (");
    expect(src).toContain('<ProblemLogEvidence service={problem.service} startedAt={problem.startedAt} resolvedAt={problem.resolvedAt} linkWindow={probWindow} />');
    const panel = read('../features/anomalies/ProblemLogEvidence.tsx');
    expect(panel).toContain('problemWindowNs(startedAt, resolvedAt ?? undefined, Date.now())');
    expect(panel).toContain('useState(false)'); // varsayılan kapalı
    expect(panel).toContain('sample: PROBLEM_LOG_SAMPLE');
    expect(panel).not.toContain('refetchInterval');
  });
  it('InboxTriageDrawer: Logs + Error logs', () => {
    const src = read('./InboxTriageDrawer.tsx');
    expect(src).toContain('logsHref({ window: w, service: item.service })');
    expect(src).toContain('logsHref({ window: w, service: item.service, severity: 17 })');
    expect(src).toContain("logsHref({ window: w, service: item.service, panel: 'patterns' })"); // v0.10.449
  });
});
