// selfTime — span'in ÖZ süresi (v0.9.1273, Dynatrace-parite #4).
//
// Tanım: süre − doğrudan çocukların KAPSADIĞI zaman. Naif çocuk-toplamı
// YANLIŞ: async/paralel çocuklar çakışır ve toplam, ebeveyn süresini
// bile aşabilir ("self -%140" gibi saçma değerler). Doğru hesap aralık
// BİRLEŞİMİ: çocuk [start,end] aralıkları ebeveyn sınırına kırpılır,
// birleştirilir, kapsanan toplam süreden düşülür. Dynatrace/Datadog'un
// "exec time" tanımı da budur. SAF — tablo testli.

export interface SpanInterval {
  spanId: string;
  parentSpanId: string;
  startTime: number; // unix ns
  endTime: number;   // unix ns
}

/** Doğrudan çocukların ebeveyn içinde kapsadığı toplam ns. */
export function childCoverageNs(parent: SpanInterval, all: SpanInterval[]): number {
  const kids = all
    .filter(s => s.parentSpanId === parent.spanId && s.spanId !== parent.spanId)
    .map(s => ({
      a: Math.max(s.startTime, parent.startTime),
      b: Math.min(s.endTime, parent.endTime),
    }))
    .filter(iv => iv.b > iv.a)
    .sort((x, y) => x.a - y.a);
  let covered = 0;
  let curA = 0, curB = -1;
  for (const iv of kids) {
    if (iv.a > curB) {
      if (curB > curA) covered += curB - curA;
      curA = iv.a; curB = iv.b;
    } else if (iv.b > curB) {
      curB = iv.b;
    }
  }
  if (curB > curA) covered += curB - curA;
  return covered;
}

/** Öz süre ms — çocuk yoksa süreye eşit; asla negatif değil. */
export function selfTimeMs(parent: SpanInterval, all: SpanInterval[]): number {
  const total = parent.endTime - parent.startTime;
  const self = total - childCoverageNs(parent, all);
  return Math.max(0, self) / 1e6;
}
