import { describe, it, expect } from 'vitest';
import { podWorkloadName, workloadMatchesService, podMatchesService } from './podWorkload';

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

// v0.9.130 — operatör raporu: "infrastructure tabında bazı cluster'ları
// buluyor bazılarını bulamıyor". Zincir eskiden depRow bulununca podSet'e
// KİLİTLENİP "<deploy>-" prefix yedeğine düşmüyordu; KSM'i kısmi/boş olan
// cluster'da pod'lar podSet'te olmadığından hiç eşleşmiyordu. Düzeltme:
// deploy varken podSet ADDİTİF (üyelik ⋃ prefix).
describe('podMatchesService', () => {
  const P = (pod: string, namespace = 'callcenter', service?: string) =>
    ({ pod, namespace, service });
  const opts = (o: Partial<{ service: string; deploy: string; ns: string; podNames: Set<string> | null }>) =>
    ({ service: 'shop-core-prep', deploy: '', ns: '', podNames: null, ...o });

  it('REGRESYON: depRow var ama podNames prefix-eşleşen pod\'u kaçırıyor → yine eşleşir', () => {
    // Eski kilitli zincirde bu FALSE dönerdi (podSet.has=false, prefix
    // yedeğine düşmezdi) — cluster boş görünürdü. Şimdi prefix yakalar.
    const pod = P('shop-core-prep-6f8744665f-k2tcj');
    const podNames = new Set(['shop-core-prep-aaaa111111-zzzzz']); // farklı pod
    expect(podMatchesService(pod, opts({ deploy: 'shop-core-prep', ns: 'callcenter', podNames })))
      .toBe(true);
  });

  it('REGRESYON: applyDeployKSM zero-serisi → podNames boş Set → prefix yine eşleşir', () => {
    const pod = P('shop-core-prep-6f8744665f-k2tcj');
    expect(podMatchesService(pod, opts({ deploy: 'shop-core-prep', ns: 'callcenter', podNames: new Set() })))
      .toBe(true);
  });

  it('podSet, prefix\'in kaçırdığı özel-adlı pod\'u yakalar (union geniş)', () => {
    const pod = P('legacy-worker-xyz'); // "<deploy>-" öneki taşımıyor
    const podNames = new Set(['legacy-worker-xyz']);
    expect(podMatchesService(pod, opts({ deploy: 'shop-core-prep', ns: 'callcenter', podNames })))
      .toBe(true);
  });

  it('ns süzgeci: farklı namespace\'in pod\'u dışlanır (ns türetildiyse)', () => {
    const pod = P('shop-core-prep-6f8744665f-k2tcj', 'other-ns');
    expect(podMatchesService(pod, opts({ deploy: 'shop-core-prep', ns: 'callcenter', podNames: new Set() })))
      .toBe(false);
  });

  it('deploy var, ne üyelik ne prefix → eşleşmez (daraltma korunur)', () => {
    const pod = P('unrelated-app-6f8744665f-k2tcj');
    expect(podMatchesService(pod, opts({ deploy: 'shop-core-prep', ns: 'callcenter', podNames: new Set() })))
      .toBe(false);
  });

  it('yedek mod (deploy yok): isim-eşitliği eşleşir, kardeş eşleşmez', () => {
    const hit = P('shop-core-prep-6f8744665f-k2tcj');
    expect(podMatchesService(hit, opts({ service: 'shop-core-prep' }))).toBe(true);
    const sibling = P('shop-core-prep-batch-7f5c96cd4b-gtg2f');
    expect(podMatchesService(sibling, opts({ service: 'shop-core-prep' }))).toBe(false);
  });

  it('yedek mod: enrichment service alanı eşleşir', () => {
    const pod = P('renamed-pod-abc', 'callcenter', 'shop-core-prep');
    expect(podMatchesService(pod, opts({ service: 'shop-core-prep' }))).toBe(true);
  });
});
