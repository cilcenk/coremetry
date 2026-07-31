// throughputTotal.ts (v0.9.483, operatör: "throughput yığılmış alan yerine
// çizgi olsun") — Overview Throughput kartının "Toplam" çizgisini üreten saf
// çekirdek.
//
// Yığılmış alan kalktığı için toplam artık GÖRSEL olarak (bantların üst
// kenarı) değil, VERİ olarak çizilmeli: Toplam = OK + Errors, eleman-eleman.
//
// Boşluk (null) semantiği bilinçli ve OVC sözleşmesiyle aynı (OvChartSeries:
// null = veri yok, grafikte GAP çizilir — uydurma 0 değil):
//   null + null = null   → iki seride de veri yoksa toplam da yok
//   null + x    = x      → bir seri boşsa diğerinin değeri toplamdır
//   x    + y    = x + y
// Dizi boyları farklıysa uzun olana göre hizalanır, eksik taraf null sayılır
// (kısa dizi sessizce toplamı KIRPMAZ — kırpma x eksenini kaydırırdı).
// NaN/Infinity boş sayılır (seriesStats ile aynı kural).

export function sumNullableSeries(
  a: ReadonlyArray<number | null | undefined>,
  b: ReadonlyArray<number | null | undefined>,
): (number | null)[] {
  const n = Math.max(a.length, b.length);
  const out: (number | null)[] = new Array(n);
  for (let i = 0; i < n; i++) {
    const x = num(a[i]);
    const y = num(b[i]);
    out[i] = x == null ? y : y == null ? x : x + y;
  }
  return out;
}

function num(v: number | null | undefined): number | null {
  return v == null || !isFinite(v) ? null : v;
}
