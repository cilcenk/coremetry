// logFieldLift — v0.10.509 (log arama denetimi C5): alan panelinin
// "Hatalıyı ayıran" rozeti. lift = hata seçimindeki pay − tabandaki pay
// (puan). ±5 puan altı gürültü sayılır (BubbleUp'ın 5 puan eşiği).
export type LiftBadge = { kind: 'none' } | { kind: 'up' | 'down' | 'flat'; label: string; lift: number };

export const LIFT_NOISE_PT = 5;

export function liftBadge(v: { lift?: number; selPct?: number }, enabled: boolean): LiftBadge {
  if (!enabled || typeof v.lift !== 'number') return { kind: 'none' };
  const l = v.lift;
  const label = `${l > 0 ? '+' : ''}${Math.abs(l) >= 10 ? l.toFixed(0) : l.toFixed(1)} pt`;
  if (l >= LIFT_NOISE_PT) return { kind: 'up', label, lift: l };
  if (l <= -LIFT_NOISE_PT) return { kind: 'down', label, lift: l };
  return { kind: 'flat', label, lift: l };
}
