import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  fieldState, fieldPct, fleetSummary, fieldSeen, COVERAGE_FIELDS,
} from './coverageRows';
import type { K8sCoverageRow } from '@/lib/types';

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
