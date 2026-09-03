// redBands.ts — v0.10.316: OK / Errors band matematiği TEK yerde.
// rate × (1 − er/100) ve rate × er/100, 0'a kelepçeli; hata oranı noktası
// yoksa 0 sayılır (bant "hiç hata yok" der, çizgi kaybolmaz). Overview
// (v0.9.253) ve Pod (v0.9.391) aynı üç satırı ayrı ayrı taşıyordu; ghost
// (v0.10.315/316) üçüncü kopyayı doğurmasın diye çıkarıldı. Saf; React yok.
export interface RedPoint { time: number; value: number }

export function okErrorPoints(ratePts: RedPoint[], erPts: RedPoint[]): { ok: RedPoint[]; err: RedPoint[] } {
  const ok = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * (1 - (erPts[i]?.value ?? 0) / 100)) }));
  const err = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * ((erPts[i]?.value ?? 0) / 100)) }));
  return { ok, err };
}
