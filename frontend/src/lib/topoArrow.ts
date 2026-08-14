// topoArrow — v0.9.1032 (operatör isteği: "kesikli çizgiler var ama ok
// yönleri de olsa iyi olur"). TopologyFlowGraph kenarları yönlü
// (caller → callee) ama çizgide yön okunmuyordu; akış animasyonu yönü
// ima ediyor, durağan bakışta belirsiz.
//
// Kenar şekli TopologyFlowGraph ile SÖZLEŞMELİ:
//   M a  C (mx, a.y) (mx, b.y)  b      mx = (a.x + b.x) / 2
// Ok başı eğri ÜZERİNDE bir t noktasına, o noktadaki tanjanta hizalı
// konur. Düğüm pilleri (HTML) SVG'nin üstünde durduğundan uç noktaya
// (t=1) konan ok pilin altında kaybolur — hedefe yakın t (≈0.78)
// kolonlar arasındaki boşlukta kalır; kenar RED chip'i tam orta
// noktada (t=0.5) yaşadığından onunla da çakışmaz. Çift yönlü kenarda
// ikinci ok kaynağa yakın t'de (≈0.22) TERS tanjantla çizilir.
//
// SAF: DOM yok — de Casteljau yerine kapalı form nokta + türev.

export interface TopoPt { x: number; y: number }

export interface EdgeArrow {
  x: number;
  y: number;
  /** SVG rotate() derecesi — 0° = +x yönü (sağa). */
  angle: number;
}

/**
 * edgeArrow — kenarın t ∈ [0,1] noktası + akış yönü açısı.
 * `reverse: true` ters yön okunu (çift yönlü kenarın kaynak ucu) verir:
 * aynı nokta, 180° dönük açı.
 */
export function edgeArrow(a: TopoPt, b: TopoPt, t: number, reverse = false): EdgeArrow {
  const mx = (a.x + b.x) / 2;
  const c1 = { x: mx, y: a.y };
  const c2 = { x: mx, y: b.y };
  const u = 1 - t;
  // B(t) = u³a + 3u²t·c1 + 3ut²·c2 + t³b
  const x = u * u * u * a.x + 3 * u * u * t * c1.x + 3 * u * t * t * c2.x + t * t * t * b.x;
  const y = u * u * u * a.y + 3 * u * u * t * c1.y + 3 * u * t * t * c2.y + t * t * t * b.y;
  // B'(t) = 3u²(c1−a) + 6ut(c2−c1) + 3t²(b−c2)
  let dx = 3 * u * u * (c1.x - a.x) + 6 * u * t * (c2.x - c1.x) + 3 * t * t * (b.x - c2.x);
  let dy = 3 * u * u * (c1.y - a.y) + 6 * u * t * (c2.y - c1.y) + 3 * t * t * (b.y - c2.y);
  // Dejenere (a ≈ b): tanjant sıfır — kiriş yönüne, o da sıfırsa +x'e düş.
  if (Math.abs(dx) < 1e-9 && Math.abs(dy) < 1e-9) {
    dx = b.x - a.x;
    dy = b.y - a.y;
    if (Math.abs(dx) < 1e-9 && Math.abs(dy) < 1e-9) dx = 1;
  }
  let angle = (Math.atan2(dy, dx) * 180) / Math.PI;
  if (reverse) angle += 180;
  return { x, y, angle };
}
