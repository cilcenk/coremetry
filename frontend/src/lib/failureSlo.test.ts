// v0.9.1036 — hata-oranı (%) SLO eşiği çözümlemesi.
//
// İki ayrı iddia, ikisi de ayrı bir hata sınıfını çiviliyor:
//
// (A) SAF ÖNCELİK — availability SLO'su > servis override'ı > filo
//     varsayılanı. Ortadaki basamağın 0 olabilmesi bu testin asıl
//     nedeni: `overrides[svc] ?? defaultPct` yazımı 0'ı "yok" sayar ve
//     "bu servis için çizgi istemiyorum" kararı sessizce varsayılana
//     dönerdi.
//
// (B) BAĞLANMA — v0.9.1024 dersi: saf fonksiyonu test etmek onun
//     ÇAĞRILDIĞINI ölçmez. `shouldAutoCommit` üç yıl test edildi ve
//     hiçbir yerden çağrılmadı. Burada ServiceCharts kaynağı taranıyor:
//     failureThresholds gerçekten çağrılıyor mu ve düzeltme ÖNCESİ
//     ifade (`errorThresholds:   err.length > 0 ? err : undefined`)
//     kaynaktan gitti mi.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'fs';
import { resolve } from 'path';
import { failureThresholds, resolveFailurePct } from './failureSlo';
import type { Threshold } from '@/components/MultiLineChart';
import type { FailureSLOConfig } from '@/lib/types';

const SLO_LINE: Threshold[] = [{ value: 0.5, label: 'err ≤ 0.50%', severity: 'err' }];

describe('resolveFailurePct — global → override önceliği', () => {
  const cases: Array<{
    name: string;
    cfg: FailureSLOConfig | undefined | null;
    service: string;
    want: number | null;
  }> = [
    { name: 'config yok → çizgi yok', cfg: undefined, service: 'api', want: null },
    { name: 'config null → çizgi yok', cfg: null, service: 'api', want: null },
    { name: 'yalnız varsayılan', cfg: { defaultPct: 1 }, service: 'api', want: 1 },
    {
      name: 'override varsayılanı EZER',
      cfg: { defaultPct: 1, overrides: { api: 5 } }, service: 'api', want: 5,
    },
    {
      name: 'başka servisin override\'ı bu servise sızmaz',
      cfg: { defaultPct: 1, overrides: { other: 5 } }, service: 'api', want: 1,
    },
    {
      name: 'override 0 → çizgi YOK (varsayılana DÜŞMEZ)',
      cfg: { defaultPct: 1, overrides: { api: 0 } }, service: 'api', want: null,
    },
    { name: 'varsayılan 0 → çizgi yok', cfg: { defaultPct: 0 }, service: 'api', want: null },
    {
      name: 'varsayılan 0 ama override dolu → override çizilir',
      cfg: { defaultPct: 0, overrides: { api: 2 } }, service: 'api', want: 2,
    },
    { name: 'negatif → çizgi yok', cfg: { defaultPct: -3 }, service: 'api', want: null },
    { name: '100 üstü kelepçelenir', cfg: { defaultPct: 250 }, service: 'api', want: 100 },
    {
      name: 'NaN → çizgi yok (bozuk blob grafiği bozmaz)',
      cfg: { defaultPct: Number.NaN }, service: 'api', want: null,
    },
    { name: 'ondalık korunur', cfg: { defaultPct: 2.5 }, service: 'api', want: 2.5 },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(resolveFailurePct(c.cfg, c.service)).toBe(c.want);
    });
  }
});

describe('failureThresholds — availability SLO en üst basamak', () => {
  it('gerçek SLO çizgisi varsa blob HİÇ konuşmaz', () => {
    expect(failureThresholds(SLO_LINE, { defaultPct: 1 }, 'api')).toBe(SLO_LINE);
  });

  it('SLO çizgisi varsa override bile ezemez', () => {
    expect(failureThresholds(SLO_LINE, { defaultPct: 1, overrides: { api: 9 } }, 'api'))
      .toBe(SLO_LINE);
  });

  it('boş SLO dizisi "yok" sayılır — blob devreye girer', () => {
    const got = failureThresholds([], { defaultPct: 1 }, 'api');
    expect(got).toEqual([{ value: 1, label: 'SLO %1', severity: 'err' }]);
  });

  it('SLO yoksa varsayılandan çizgi kurar', () => {
    const got = failureThresholds(undefined, { defaultPct: 2.5 }, 'api');
    expect(got).toEqual([{ value: 2.5, label: 'SLO %2.5', severity: 'err' }]);
  });

  it('etiket availability SLO etiketinden AYIRT EDİLEBİLİR', () => {
    // "SLO %X" (filo varsayılanı) vs "err ≤ X%" (SLO nesnesi):
    // operatör çizginin nereden geldiğini grafikte görmeli.
    const got = failureThresholds(undefined, { defaultPct: 1 }, 'api');
    expect(got?.[0].label).not.toMatch(/err ≤/);
  });

  it('çözümleme null ise thresholds prop\'u undefined kalır', () => {
    expect(failureThresholds(undefined, { defaultPct: 0 }, 'api')).toBeUndefined();
    expect(failureThresholds(undefined, undefined, 'api')).toBeUndefined();
  });
});

// ── (B) BAĞLANMA KAPISI ────────────────────────────────────────────
describe('ServiceCharts gerçekten bu çözümlemeden geçiyor', () => {
  const src = readFileSync(
    resolve(__dirname, '../components/ServiceCharts.tsx'), 'utf8');

  it('failureThresholds çağrılıyor', () => {
    expect(src).toMatch(/failureThresholds\s*\(/);
  });

  it('düzeltme ÖNCESİ satır-içi ifade kaynakta YOK', () => {
    // Eski hâl: errorThresholds doğrudan SLO memo'sundan geliyordu ve
    // blob hiç okunmuyordu. İmza DAR seçildi (anahtar adı + ternary),
    // yoksa "err.length > 0" tek başına alakasız yerlerde geçer.
    expect(src).not.toMatch(/errorThresholds:\s*err\.length\s*>\s*0/);
  });

  it('hata paneli thresholds prop\'unu çözümlenmiş değerden alıyor', () => {
    expect(src).toMatch(/thresholds=\{errorThresholds\}/);
  });
});
