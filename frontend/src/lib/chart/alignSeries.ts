// alignToUnion — farklı fetch'lerden gelen serileri TEK zaman eksenine
// hizalar (v0.9.87, Runtime paneli).
//
// Neden: ChartCard her çizgiyi ilk çizginin zaman eksenine INDEX ile
// eşler. RED grafiklerinde tüm agg'ler tek batch'ten geldiği için bu
// tutar; Runtime panelinde her metrik AYRI /api/metrics/query çağrısı —
// bucket kümeleri farklılaşabilir (bir metrik bir bucket'ta hiç satır
// üretmemiş olabilir; zero-fill YOK). Index eşlemesi o durumda değerleri
// yanlış zamana çizer. Union ekseni + time→value map bunu düzeltir;
// eksik bucket null olur (grafikte boşluk, uydurma değer değil).
export interface AlignedLines {
  times: number[];                 // union, artan (kaynak birimi korunur)
  cols: (number | null)[][];       // her giriş serisi için hizalı değerler
}

export function alignToUnion(
  lines: ReadonlyArray<ReadonlyArray<{ time: number; value: number | null }>>,
): AlignedLines {
  const timeSet = new Set<number>();
  for (const pts of lines) for (const p of pts) timeSet.add(p.time);
  const times = [...timeSet].sort((a, b) => a - b);
  const cols = lines.map(pts => {
    const byTime = new Map(pts.map(p => [p.time, p.value]));
    return times.map(t => {
      const v = byTime.get(t);
      return v == null ? null : v;
    });
  });
  return { times, cols };
}
