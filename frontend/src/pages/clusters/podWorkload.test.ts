import { describe, it, expect } from 'vitest';
import { podWorkloadName, workloadMatchesService, podMatchesService, stripEnvSuffix, dominantWorkload, servicePodRegex } from './podWorkload';

// v0.9.56 — servis-adı↔pod-adı yedek eşleşmesinin çekirdeği; backend
// stripPodSuffixes ile aynı davranış (operatör vakası:
// bsa-adkservices-login-prep-<rs>-<rand> → bsa-adkservices-login-prep).
describe('podWorkloadName', () => {
  it('strips deployment rs-hash + random suffix', () => {
    expect(podWorkloadName('bsa-adkservices-login-prep-6bd9df6c4d-x2b1z'))
      .toBe('bsa-adkservices-login-prep');
  });
  it('strips daemonset random suffix only', () => {
    expect(podWorkloadName('node-exporter-x2b1z')).toBe('node-exporter');
  });
  it('strips statefulset ordinal', () => {
    expect(podWorkloadName('kafka-2')).toBe('kafka');
  });
  it('sibling prefix stays distinct (equality contract)', () => {
    // "bsa-login" servisi bu pod'a eşleşmemeli — soyulmuş ad
    // "bsa-login-prep"tir, prefix değil eşitlik karşılaştırılır.
    expect(podWorkloadName('bsa-login-prep-6bd9df6c4d-x2b1z')).toBe('bsa-login-prep');
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
// ocptest3, callcenter ns): oneagent varyantı servise eşlenir, -batch /
// -uat kardeş iş yükleri eşlenmez.
describe('workloadMatchesService (gerçek filo adları)', () => {
  const cases: [string, string, boolean][] = [
    ['bsa-callcenter-core-prep-oneagent-665867d649-7qmqp', 'bsa-callcenter-core-prep', true],
    ['bsa-callcenter-core-prep-6f8744665f-k2tcj', 'bsa-callcenter-core-prep', true],
    ['bsa-callcenter-login-prep-oneagent-6479476b7d-9wcrp', 'bsa-callcenter-login-prep', true],
    ['bsa-callcenter-channelparameters-prep-oneagent-dc565bf68-h7bnf', 'bsa-callcenter-channelparameters-prep', true], // 9-hex rs
    // Kardeş iş yükleri: AYRI servis — eşleşmemeli.
    ['bsa-callcenter-core-prep-batch-7f5c96cd4b-gtg2f', 'bsa-callcenter-core-prep', false],
    ['bsa-callcenter-integration-uat-784c99c57d-dmpvz', 'bsa-callcenter-integration', false],
    // Alakasız pod hiç eşleşmez.
    ['httpd-test-595cbb999d-2p4dn', 'bsa-callcenter-core-prep', false],
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
    ({ service: 'bsa-core-prep', deploy: '', ns: '', podNames: null, ...o });

  it('REGRESYON: depRow var ama podNames prefix-eşleşen pod\'u kaçırıyor → yine eşleşir', () => {
    // Eski kilitli zincirde bu FALSE dönerdi (podSet.has=false, prefix
    // yedeğine düşmezdi) — cluster boş görünürdü. Şimdi prefix yakalar.
    const pod = P('bsa-core-prep-6f8744665f-k2tcj');
    const podNames = new Set(['bsa-core-prep-aaaa111111-zzzzz']); // farklı pod
    expect(podMatchesService(pod, opts({ deploy: 'bsa-core-prep', ns: 'callcenter', podNames })))
      .toBe(true);
  });

  it('REGRESYON: applyDeployKSM zero-serisi → podNames boş Set → prefix yine eşleşir', () => {
    const pod = P('bsa-core-prep-6f8744665f-k2tcj');
    expect(podMatchesService(pod, opts({ deploy: 'bsa-core-prep', ns: 'callcenter', podNames: new Set() })))
      .toBe(true);
  });

  it('podSet, prefix\'in kaçırdığı özel-adlı pod\'u yakalar (union geniş)', () => {
    const pod = P('legacy-worker-xyz'); // "<deploy>-" öneki taşımıyor
    const podNames = new Set(['legacy-worker-xyz']);
    expect(podMatchesService(pod, opts({ deploy: 'bsa-core-prep', ns: 'callcenter', podNames })))
      .toBe(true);
  });

  it('ns süzgeci: farklı namespace\'in pod\'u dışlanır (ns türetildiyse)', () => {
    const pod = P('bsa-core-prep-6f8744665f-k2tcj', 'other-ns');
    expect(podMatchesService(pod, opts({ deploy: 'bsa-core-prep', ns: 'callcenter', podNames: new Set() })))
      .toBe(false);
  });

  it('deploy var, ne üyelik ne prefix → eşleşmez (daraltma korunur)', () => {
    const pod = P('unrelated-app-6f8744665f-k2tcj');
    expect(podMatchesService(pod, opts({ deploy: 'bsa-core-prep', ns: 'callcenter', podNames: new Set() })))
      .toBe(false);
  });

  it('yedek mod (deploy yok): isim-eşitliği eşleşir, kardeş eşleşmez', () => {
    const hit = P('bsa-core-prep-6f8744665f-k2tcj');
    expect(podMatchesService(hit, opts({ service: 'bsa-core-prep' }))).toBe(true);
    const sibling = P('bsa-core-prep-batch-7f5c96cd4b-gtg2f');
    expect(podMatchesService(sibling, opts({ service: 'bsa-core-prep' }))).toBe(false);
  });

  it('yedek mod: enrichment service alanı eşleşir', () => {
    const pod = P('renamed-pod-abc', 'callcenter', 'bsa-core-prep');
    expect(podMatchesService(pod, opts({ service: 'bsa-core-prep' }))).toBe(true);
  });
});

// v0.9.535 — operatör direktifi: "mobile*bff-prod sonunda prod olmadan
// bul" + somut örnek: servis mobile-overview-prod, pod
// mobile-overview-bff-<hash>-<rand>. İki adlandırma boşluğu birden:
// (a) k8s deployment adı servis adındaki env ekini taşımıyor,
// (b) pod'da servis adında olmayan bir -bff kuyruğu olabiliyor.
describe('stripEnvSuffix', () => {
  it('bilinen env ekleri kuyruktayken soyulur', () => {
    expect(stripEnvSuffix('mobile-loans-bff-prod')).toBe('mobile-loans-bff');
    expect(stripEnvSuffix('mobile-overview-prod')).toBe('mobile-overview');
    expect(stripEnvSuffix('bsa-login-int')).toBe('bsa-login');
    expect(stripEnvSuffix('svc-uat')).toBe('svc');
    expect(stripEnvSuffix('svc-prep')).toBe('svc');
  });
  it('ad ortasındaki env sözcüğü DOKUNULMAZ', () => {
    expect(stripEnvSuffix('bsa-digital-limitcore-prod-oneagent')).toBe('bsa-digital-limitcore-prod-oneagent');
    expect(stripEnvSuffix('prod-gateway')).toBe('prod-gateway');
  });
  it('bilinmeyen ek / eksiz ad aynen kalır', () => {
    expect(stripEnvSuffix('mobile-loans-bff')).toBe('mobile-loans-bff');
    expect(stripEnvSuffix('svc-production')).toBe('svc-production');
  });
  it('yalnız ekten ibaret ad soyulmaz (boş ad üretme)', () => {
    expect(stripEnvSuffix('-prod')).toBe('-prod');
  });
});

describe('podMatchesService — env eki soyulmuş aday (v0.9.535)', () => {
  const noOpts = { deploy: '', ns: '', podNames: null };
  it('BFF şekli 1: servis env ekli, pod eksiz', () => {
    expect(podMatchesService(
      { pod: 'mobile-loans-bff-6b8f49b9d5-8hrtj', namespace: 'mobile-bff-prod' },
      { service: 'mobile-loans-bff-prod', ...noOpts },
    )).toBe(true);
  });
  it('BFF şekli 2 (operatör örneği): servis mobile-overview-prod, pod mobile-overview-bff-*', () => {
    expect(podMatchesService(
      { pod: 'mobile-overview-bff-c747d59bc-s66gr', namespace: 'mobile-bff-prod' },
      { service: 'mobile-overview-prod', ...noOpts },
    )).toBe(true);
  });
  it('kardeş disiplini: bsa-login-prod, bsa-login-prep podunu ALMAZ', () => {
    expect(podMatchesService(
      { pod: 'bsa-login-prep-6bd9df6c4d-x2b1z', namespace: 'x' },
      { service: 'bsa-login-prod', ...noOpts },
    )).toBe(false);
  });
  it('bilinmeyen kuyruk eşleşmez: mobile-overview-web pod, overview-prod servis', () => {
    expect(podMatchesService(
      { pod: 'mobile-overview-web-c747d59bc-s66gr', namespace: 'x' },
      { service: 'mobile-overview-prod', ...noOpts },
    )).toBe(false);
  });
  it('eski davranış bozulmadı: BSA tam eşitlik hâlâ tutar', () => {
    expect(podMatchesService(
      { pod: 'bsa-digital-limitcore-prod-864cd95d87-q9dt9', namespace: 'x' },
      { service: 'bsa-digital-limitcore-prod', ...noOpts },
    )).toBe(true);
  });
});

describe('dominantWorkload (v0.9.535 — effDeploy yedeği)', () => {
  it('en sık iş yükü kazanır; oneagent azınlığı ezemez', () => {
    expect(dominantWorkload([
      'mobile-loans-bff-6b8f49b9d5-8hrtj',
      'mobile-loans-bff-6b8f49b9d5-vdp54',
      'mobile-loans-bff-oneagent-7d98d8b99d-m6r8f',
    ])).toBe('mobile-loans-bff');
  });
  it('soyulamayan adlar (hostname vb.) önek kanıtı sayılmaz', () => {
    expect(dominantWorkload(['gateway', 'my-app-canary'])).toBe('');
  });
  it('boş liste boş döner (çağıran servis adına düşer)', () => {
    expect(dominantWorkload([])).toBe('');
  });
  it('eşitlikte deterministik (alfabetik ilk)', () => {
    expect(dominantWorkload([
      'bbb-6b8f49b9d5-8hrtj',
      'aaa-6b8f49b9d5-8hrtj',
    ])).toBe('aaa');
  });
});

// v0.9.536 — hedefli envanter seçicisi: sunucu bu regex'i pod=~ olarak
// PromQL'e gömer. Operator-reported kök sebep: cluster-geneli topk(500)
// düşük trafikli BFF pod'larını hiç döndürmüyordu — istemci gelmeyen
// pod'u eşleştiremez.
describe('servicePodRegex', () => {
  it('katalog deploy + servis + soyulmuş ad, sıralı ve tekilleşmiş', () => {
    expect(servicePodRegex('mobile-overview-bff-prod', ''))
      .toBe('(mobile-overview-bff-prod|mobile-overview-bff)-.*');
  });
  it('deploy doluysa başa girer', () => {
    expect(servicePodRegex('mobile-overview-prod', 'mobile-overview-bff'))
      .toBe('(mobile-overview-bff|mobile-overview-prod|mobile-overview)-.*');
  });
  it('eksiz ad tek aday üretir (çift üretme)', () => {
    expect(servicePodRegex('coremetry-monolithic', ''))
      .toBe('(coremetry-monolithic)-.*');
  });
  it('regex metaları kaçışlanır — servis adı PromQL regex bozamaz', () => {
    expect(servicePodRegex('svc.v2', '')).toBe('(svc\\.v2)-.*');
  });
});
