// Cluster görsel eşikleri (v0.9.31, design handoff — README
// "Thresholds (pctColor)"). Tema-farkında CSS var token'ları
// döndürür (globals.css: --err/--warn/--ok); hardcoded hex YOK.

// pctColor — kullanım yüzdesi: >85 err, >65 warn, else ok.
export function pctColor(pct: number): string {
  if (pct > 85) return 'var(--err)';
  if (pct > 65) return 'var(--warn)';
  return 'var(--ok)';
}

// restartColor — restart sayısı: >8 err, >2 warn, else muted.
export function restartColor(n: number): string {
  if (n > 8) return 'var(--err)';
  if (n > 2) return 'var(--warn)';
  return 'var(--text3)';
}

// safePct — payda 0/absent olduğunda güvenli yüzde (0..100), veya
// null (bilinmiyor → çağıran gauge/bar'ı gizler).
export function safePct(used?: number, capacity?: number): number | null {
  if (!capacity || capacity <= 0 || used == null) return null;
  const p = (used / capacity) * 100;
  return p < 0 ? 0 : p > 100 ? 100 : p;
}
