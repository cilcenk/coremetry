import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  fieldState, fieldPct, fleetSummary, fieldSeen, COVERAGE_FIELDS,
  podSeenWindow, podStabilityWarning,
} from './coverageRows';
import type { K8sCoverageRow, PodRow } from '@/lib/types';

// v0.10.36 — K8s entity katmanı Faz 0: bağlam kapsama kartı.
//
// Kartın var oluş sebebi: entity katmanının asıl adımı (k8sattributes +
// RBAC) prod'da collector restart'ı istiyor ve collector pod bounce'ta
// wedge oluyor. O riski ÖLÇÜLMEMİŞ gerekçeyle almak yanlış sıra — bugün
// elimizde yalnız TEK bir prod span'inin resource seti var.
//
// ⚠ Kart aynı zamanda sonraki fazın KABUL TESTİ: değişiklikten önce ve
// sonra aynı tablo. Bu yüzden kartın kendi yargısı yanlışsa, düzeltmek
// için var olduğu şeyi bozar.

const row = (o: Partial<K8sCoverageRow> = {}): K8sCoverageRow => ({
  service: 'svc-a', sampled: 100,
  namespace: 0, deployment: 0, pod: 0, podUid: 0, node: 0, container: 0, cluster: 0,
  ...o,
});

describe('fieldState', () => {
  it('tam kapsama', () => expect(fieldState(100, 100)).toBe('full'));
  it('fazlası da tam', () => expect(fieldState(120, 100)).toBe('full'));
  it('kısmi', () => expect(fieldState(40, 100)).toBe('partial'));
  it('hiç yok', () => expect(fieldState(0, 100)).toBe('none'));

  // ⚠ EN ÖNEMLİ SÖZLEŞME. Örneklemde o servisten hiç satır görülmediyse
  // "alan yok" demek YANLIŞ olur — ölçüm yapılmadı. İkisini karıştırmak
  // kabul testini çürütür: collector düzgün kurulmuşken bile kart
  // "yaymıyor" der.
  describe('ölçüm yoksa UNKNOWN, none DEĞİL', () => {
    for (const s of [0, -1]) {
      it(`sampled=${s}`, () => {
        expect(fieldState(0, s)).toBe('unknown');
        expect(fieldState(5, s)).toBe('unknown');
      });
    }
  });
});

describe('fieldPct', () => {
  it('yüzde', () => expect(fieldPct(40, 100)).toBe(40));
  it('ondalık bir basamak', () => expect(fieldPct(1, 3)).toBe(33.3));
  it('tam', () => expect(fieldPct(100, 100)).toBe(100));
  // Sıfıra bölme değil, BİLGİ yok.
  it('ölçüm yoksa null', () => {
    expect(fieldPct(0, 0)).toBeNull();
    expect(fieldPct(5, -1)).toBeNull();
  });
});

describe('fieldSeen — her alan doğru sayıya bağlı', () => {
  // Alanlar tek tek çiviliyor: bir eşleme kayarsa kart YANLIŞ alanı
  // "yayılıyor" gösterir ve hata sessiz kalır (sayılar makul görünür).
  const r = row({
    cluster: 1, namespace: 2, deployment: 3, pod: 4, podUid: 5, node: 6, container: 7,
  });
  it.each([
    ['cluster', 1], ['namespace', 2], ['deployment', 3],
    ['pod', 4], ['podUid', 5], ['node', 6], ['container', 7],
  ] as const)('%s → %i', (k, want) => expect(fieldSeen(r, k)).toBe(want));

  it('kartta yedi alan var ve sıra hiyerarşiyi izliyor', () => {
    expect(COVERAGE_FIELDS.map(f => f.key)).toEqual([
      'cluster', 'namespace', 'deployment', 'pod', 'podUid', 'node', 'container',
    ]);
  });
});

describe('fleetSummary', () => {
  it('PROD DURUMU — node var, namespace ve uid yok', () => {
    // Operatörün prod ekranından: k8s.node.name VAR, k8s.namespace.name
    // ve k8s.pod.uid YOK. Kartın bunu böyle göstermesi gerek.
    const rows = [
      row({ service: 'a', sampled: 100, node: 100, pod: 100, cluster: 100 }),
      row({ service: 'b', sampled: 100, node: 100, pod: 100, cluster: 100 }),
    ];
    const s = fleetSummary(rows);
    const by = (k: string) => s.find(x => x.field === k)!;
    expect(by('node').full).toBe(2);
    expect(by('namespace').none).toBe(2);
    expect(by('podUid').none).toBe(2);
  });

  it('kısmi yayan servis kendi kovasında', () => {
    const s = fleetSummary([row({ sampled: 100, pod: 50 })]);
    const pod = s.find(x => x.field === 'pod')!;
    expect(pod).toMatchObject({ full: 0, partial: 1, none: 0 });
  });

  // ⚠ Ölçülmemiş servis HİÇBİR kovaya girmiyor. "Yaymıyor" saymak,
  // kabul testini çürüten aynı hata.
  it('ölçülmemiş servis hiçbir kovaya girmez', () => {
    const s = fleetSummary([row({ sampled: 0 })]);
    for (const f of s) {
      expect(f.full + f.partial + f.none).toBe(0);
    }
  });

  it('boş girdi çökmez', () => {
    expect(fleetSummary([])).toHaveLength(COVERAGE_FIELDS.length);
    expect(fleetSummary(undefined)).toHaveLength(COVERAGE_FIELDS.length);
  });

  it('karışık filo doğru bölünür', () => {
    const s = fleetSummary([
      row({ service: 'a', sampled: 100, node: 100 }),   // full
      row({ service: 'b', sampled: 100, node: 30 }),    // partial
      row({ service: 'c', sampled: 100, node: 0 }),     // none
      row({ service: 'd', sampled: 0, node: 0 }),       // unknown → sayılmaz
    ]);
    expect(s.find(x => x.field === 'node')).toMatchObject({ full: 1, partial: 1, none: 1 });
  });
});

// KABLOLAMA PİNİ — saf çekirdek yeşil ama sayfa onu kullanmıyorsa kart
// yanlış yargı gösterebilir ve KABUL TESTİ çürür.
describe('kablolama', () => {
  const page = readFileSync(new URL('../AdminK8sCoverage.tsx', import.meta.url), 'utf8');
  const sys = readFileSync(new URL('../System.tsx', import.meta.url), 'utf8');

  it('sayfa saf çekirdeği kullanıyor', () => {
    for (const f of ['fleetSummary(rows)', 'fieldState(seen, r.sampled)', 'fieldSeen(r, f.key)']) {
      expect(page).toContain(f);
    }
  });

  it('örneklem uyarısı sayfada ve "ölçülmedi ≠ yok" diyor', () => {
    // Bu uyarı kartın tüm yargısının dayanağı; dipnota atmak okunmaması
    // demek olurdu.
    expect(page).toContain('ÖRNEKLEM');
    expect(page).toContain('ölçülmedi');
  });

  it('sekme kayıtlı', () => {
    expect(sys).toContain("slug: 'k8s'");
    expect(sys).toContain('AdminK8sCoverage');
  });

  it('>100 satırlı tablo content-visibility taşıyor', () => {
    // CLAUDE.md sert kısıtı: satır sayısı 100'ü aşabilen tablolar.
    expect(page).toContain("contentVisibility: 'auto'");
  });
});

// ── POD ENVANTERİ (v0.10.41) ────────────────────────────────────────────

const pod = (o: Partial<PodRow> = {}): PodRow => ({
  namespace: 'demo', pod: 'svc-abc12', service: 'svc', spans: 100,
  firstSeen: 0, lastSeen: 0, nameStable: false, ...o,
});

describe('podSeenWindow', () => {
  const NS = 1e6; // ms → ns
  it.each([
    [30_000 * NS, '30 sn boyunca görüldü'],
    [5 * 60_000 * NS, '5 dk boyunca görüldü'],
    [3 * 3_600_000 * NS, '3 sa boyunca görüldü'],
  ])('%i ns', (span, want) => {
    expect(podSeenWindow(pod({ firstSeen: 0, lastSeen: span }))).toBe(want);
  });

  // ⚠ ADLANDIRMA SÖZLEŞMESİ: "görüldü", "ayakta" DEĞİL. Bu bir örneklem
  // penceresi; pod ondan önce de sonra da yaşıyor olabilir. Arayüzün
  // ürettiği cümle, ölçtüğü şeyden fazlasını iddia etmemeli.
  it('cümle "ayakta" demiyor', () => {
    const s = podSeenWindow(pod({ firstSeen: 0, lastSeen: 60_000 * NS }));
    expect(s).toContain('görüldü');
    expect(s).not.toContain('ayakta');
  });

  it('bozuk aralıkta çökmez', () => {
    expect(podSeenWindow(pod({ firstSeen: 100, lastSeen: 0 }))).toBe('—');
    expect(podSeenWindow(pod({ firstSeen: NaN, lastSeen: 0 }))).toBe('—');
  });
});

describe('podStabilityWarning', () => {
  // ⚠ Kimliğin tek zayıf noktası — sessiz bırakılırsa entity katmanının
  // İLK çıktısı yanlış olur.
  it('StatefulSet pod\'unda uyarı VAR ve sebebini söylüyor', () => {
    const w = podStabilityWarning(pod({ pod: 'kafka-0', nameStable: true }));
    expect(w).not.toBeNull();
    expect(w).toContain('İKİ ayrı pod ömrünü');
    expect(w).toContain('pod.uid');
  });

  it('Deployment pod\'unda uyarı YOK', () => {
    // Rastgele sonek ömürleri zaten ayırıyor; her satırda duran bir
    // uyarı, hiçbir satırda okunmayan bir uyarıdır.
    expect(podStabilityWarning(pod({ nameStable: false }))).toBeNull();
  });
});

describe('pod envanteri kablolaması', () => {
  const page = readFileSync(new URL('../AdminK8sCoverage.tsx', import.meta.url), 'utf8');

  it('sayfa pod envanterini çekiyor', () => {
    expect(page).toContain('api.k8sPods(');
  });

  // ⚠ Uyarı SATIRIN İÇİNDE olmalı. Dipnota atmak, birleşmiş ömrün
  // sessiz kalması demek — kimliğin tek zayıf noktası görünmez olur.
  it('ömür uyarısı satır içinde çiziliyor', () => {
    expect(page).toContain('podStabilityWarning(r)');
    expect(page).toContain('ömür belirsiz');
  });

  it('görülme aralığı saf yardımcıdan geliyor', () => {
    expect(page).toContain('podSeenWindow(r)');
  });

  // Hook'lar erken dönüşten ÖNCE — v0.10.36'da bu kuralı bir kez ihlal
  // ettim ve tsc görmedi.
  it('pod sorgusu erken dönüşten ÖNCE kuruluyor', () => {
    // ⚠ YORUMLARI ÖNCE SÖK. İlk yazımda ham metinde arıyordum ve test
    // KIRMIZI verdi — oysa sıra doğruydu: aranan dizge, v0.10.36'da
    // yazdığım bir YORUMUN içinde de geçiyordu ("… satırlarının ALTINA
    // koymuştum"). Kapının kendi dokümantasyonunu ısırması bu depoda
    // tekrar eden sınıf (v0.9.1375, v0.9.1382, v0.10.17, v0.10.25).
    const code = page
      .replace(/\/\*[\s\S]*?\*\//g, ' ')
      .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');
    const iQuery = code.indexOf("queryKey: ['k8s-pods']");
    const iReturn = code.indexOf('if (q.isPending) return');
    expect(iQuery).toBeGreaterThan(-1);
    expect(iReturn).toBeGreaterThan(-1);
    expect(iQuery).toBeLessThan(iReturn);
  });
});

// ── v0.10.62 — SÖZLEŞME NEREDE KARŞILANIYOR ────────────────────────────
//
// `fieldState`ın `unknown` dalı bugünkü arka uçta ULAŞILAMAZ: sorgu
// GROUP BY yapıyor ve sıfır satırlı bir grup HİÇ SATIR üretmiyor, yani
// `sampled` asla 0 gelmiyor. Kartın "en önemli sözleşmesi" diye yazılan
// ayrım hiç çalışmıyordu ([[feedback-empty-set-vanishes-not-zero]]).
//
// Dal silinmedi — sözleşme doğru, üreteni yok. Ama iddianın NEREDE
// karşılandığı yazılı olmak zorunda, yoksa bir sonraki okuyan yine
// çalıştığını sanar.
describe('unknown dalı ulaşılamaz — ama sözleşme yazılı', () => {
  it('fonksiyon hâlâ doğru davranıyor (girdi 0 ise unknown)', () => {
    expect(fieldState(0, 0)).toBe('unknown');
    expect(fieldState(0, 10)).toBe('none');
  });

  it('ulaşılamazlık ve gerçek işaretin yeri KAYNAKTA yazılı', () => {
    const src = readFileSync(new URL('./coverageRows.ts', import.meta.url), 'utf8');
    expect(src).toContain('ULAŞILAMAZ');
    // Ölçülmemişliğin gerçek işareti nerede: capped + servis-başına kota.
    expect(src).toContain('capped');
    expect(src).toContain('SERVİS BAŞINA');
  });
});

// TestCappedIsDeclared — TAVAN DOLDUYSA OPERATÖR GÖRMELİ.
describe('örneklem tavanı doldu uyarısı', () => {
  it('sayfa capped bayrağını çiziyor', () => {
    const page = readFileSync(new URL('../AdminK8sCoverage.tsx', import.meta.url), 'utf8');
    expect(page).toContain('data?.capped');
    expect(page).toContain('HİÇ girmemiş');
  });
});
