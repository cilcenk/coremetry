// dbSaturation — havuz doygunluğu karosunun SAF çekirdeği (v0.9.822).
//
// Karonun sayısı ile karonun anlattığı satır ASLA ayrışmamalı: ikisi de
// buradan çıkıyor. Tablo-güdümlü test dbSaturation.test.ts.

import type { DBSaturationRow } from './types';

/**
 * worstSaturation — EN DAR havuz. Yüzdesi en yüksek satır; eşitlikte
 * mutlak kalan alan (limit − usage) küçük olan kazanır.
 *
 * NEDEN İKİNCİ ÖLÇÜT: iki havuz da %90'daysa, 100'lük tavanın 10'u ile
 * 10.000'lik tavanın 1.000'i aynı aciliyette DEĞİL. Yüzde tek başına
 * ölçek saklıyor (v0.9.818'de Endpoints'in hata oranı hücresinde
 * kapattığımız sınıfın aynısı).
 *
 * Boş liste → null. Çağıran o zaman karoyu HİÇ KURMAZ.
 */
export function worstSaturation(rows: DBSaturationRow[]): DBSaturationRow | null {
  let best: DBSaturationRow | null = null;
  for (const r of rows) {
    if (!Number.isFinite(r.pct)) continue;
    if (best === null) { best = r; continue; }
    if (r.pct > best.pct) { best = r; continue; }
    if (r.pct === best.pct && (r.limit - r.usage) < (best.limit - best.usage)) best = r;
  }
  return best;
}

/**
 * saturationLabel — en dar havuzun insan-okur adı.
 * "oracle · corebank-dg.prod · sessions" ya da boyutlu kontrolde
 * "… · tablespace USERS".
 */
export function saturationLabel(r: DBSaturationRow): string {
  const check = r.subkey ? `${r.check} ${r.subkey}` : r.check;
  return `${r.system} · ${r.instance} · ${check}`;
}

/**
 * saturationTone — karonun rengi. Eşikler evaluator'ın
 * capacityDecision'ıyla AYNI (crit ≥ 90, warn ≥ 85): sayfa ile alarm
 * aynı sayıya bakıp farklı renk göstermemeli.
 */
export function saturationTone(pct: number): 'err' | 'warn' | 'ok' {
  if (pct >= 90) return 'err';
  if (pct >= 85) return 'warn';
  return 'ok';
}
