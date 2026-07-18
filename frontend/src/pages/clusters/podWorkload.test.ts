import { describe, it, expect } from 'vitest';
import { podWorkloadName, workloadMatchesService } from './podWorkload';

// v0.9.56 — servis-adı↔pod-adı yedek eşleşmesinin çekirdeği; backend
// stripPodSuffixes ile aynı davranış (operatör vakası:
// shop-adkservices-login-prep-<rs>-<rand> → shop-adkservices-login-prep).
describe('podWorkloadName', () => {
  it('strips deployment rs-hash + random suffix', () => {
    expect(podWorkloadName('shop-adkservices-login-prep-6bd9df6c4d-x2b1z'))
      .toBe('shop-adkservices-login-prep');
  });
  it('strips daemonset random suffix only', () => {
    expect(podWorkloadName('node-exporter-x2b1z')).toBe('node-exporter');
  });
  it('strips statefulset ordinal', () => {
    expect(podWorkloadName('kafka-2')).toBe('kafka');
  });
  it('sibling prefix stays distinct (equality contract)', () => {
    // "shop-login" servisi bu pod'a eşleşmemeli — soyulmuş ad
    // "shop-login-prep"tir, prefix değil eşitlik karşılaştırılır.
    expect(podWorkloadName('shop-login-prep-6bd9df6c4d-x2b1z')).toBe('shop-login-prep');
  });
  it('leaves non-conforming names untouched', () => {
    expect(podWorkloadName('gateway')).toBe('gateway');
    expect(podWorkloadName('my-app-canary')).toBe('my-app-canary'); // 'canary' 6 harf + sesli
  });
  it('rand5 with vowels is not a suffix', () => {
    expect(podWorkloadName('svc-audio')).toBe('svc-audio'); // 'audio' sesli içerir
  });
});

// v0.9.56 — operatör ekran görüntüsündeki GERÇEK filo adları (OpenShift
// ocp-cluster, callcenter ns): oneagent varyantı servise eşlenir, -batch /
// -uat kardeş iş yükleri eşlenmez.
describe('workloadMatchesService (gerçek filo adları)', () => {
  const cases: [string, string, boolean][] = [
    ['shop-callcenter-core-prep-oneagent-665867d649-7qmqp', 'shop-callcenter-core-prep', true],
    ['shop-callcenter-core-prep-6f8744665f-k2tcj', 'shop-callcenter-core-prep', true],
    ['shop-callcenter-login-prep-oneagent-6479476b7d-9wcrp', 'shop-callcenter-login-prep', true],
    ['shop-callcenter-channelparameters-prep-oneagent-dc565bf68-h7bnf', 'shop-callcenter-channelparameters-prep', true], // 9-hex rs
    // Kardeş iş yükleri: AYRI servis — eşleşmemeli.
    ['shop-callcenter-core-prep-batch-7f5c96cd4b-gtg2f', 'shop-callcenter-core-prep', false],
    ['shop-callcenter-integration-uat-784c99c57d-dmpvz', 'shop-callcenter-integration', false],
    // Alakasız pod hiç eşleşmez.
    ['httpd-test-595cbb999d-2p4dn', 'shop-callcenter-core-prep', false],
  ];
  for (const [pod, svc, want] of cases) {
    it(`${pod} ↔ ${svc} → ${want}`, () => {
      expect(workloadMatchesService(podWorkloadName(pod), svc)).toBe(want);
    });
  }
});
