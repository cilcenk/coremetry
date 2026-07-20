// runtimeSmooth (v0.9.127, operatör talebi 2026-07-20) — Runtime (JVM/.NET/Go)
// heap/GC/thread grafikleri için hareketli-ortalama yumuşatma.
//
// Kök neden (CH ground-truth): heap.used / jvm.memory.used gauge'u her export'ta
// GC döngüsünün rastgele fazında yakalanır → ardışık örnekler ±40MB savrulur
// (GERÇEK GC sawtooth'u, render bug'ı değil; aliasing/step değil — her bucket
// dolu). Infra CPU/Mem düz görünür çünkü rate[5m] / working_set doğal düz.
// Operatör "infra gibi smooth" istedi (GC tepe/dip gizlenir kabul).
//
// Neden frontend: seriler zaten sınırlı (≤40 pod × ≤2000 nokta), CH/API'ye
// dokunmadan sıfır ek sorgu yükü ("performansa dikkat"). Backend rolling-avg
// yerine burada; O(n) prefix-sum → ihmal edilebilir.

// smoothWindow — merkezli SMA pencere uzunluğu (tek sayı). n/12'ye oturur ⇒
// efektif zaman-penceresi ≈ span/12 (K×spacing = (n/12)×(span/n)), yani
// cadence'tan BAĞIMSIZ, görünen aralıkla orantılı yumuşatma (infra-benzeri).
// n<8 → 1 (yumuşatma yok, kısa/seyrek seri). [3,41] clamp: compute + aşırı-düz
// koruması.
export function smoothWindow(n: number): number {
  if (n < 8) return 1;
  let k = Math.round(n / 12);
  if (k < 3) k = 3;
  if (k > 41) k = 41;
  if (k % 2 === 0) k += 1;
  return k;
}

// smoothValues — merkezli basit hareketli ortalama, null-farkında. Her çıktı
// i = [i-h, i+h] penceresindeki NULL-OLMAYAN değerlerin ortalaması (h=(k-1)/2).
// Pencere tamamen null → null (spanGaps köprüler). k≤1 → passthrough kopya.
// Prefix-sum ile O(n).
export function smoothValues(values: (number | null)[], k: number): (number | null)[] {
  const n = values.length;
  if (k <= 1 || n === 0) return values.slice();
  const h = (k - 1) >> 1;
  const ps = new Float64Array(n + 1); // prefix sum of finite values
  const pc = new Int32Array(n + 1);   // prefix count of finite values
  for (let i = 0; i < n; i++) {
    const v = values[i];
    const finite = v != null && Number.isFinite(v);
    ps[i + 1] = ps[i] + (finite ? (v as number) : 0);
    pc[i + 1] = pc[i] + (finite ? 1 : 0);
  }
  const out: (number | null)[] = new Array(n);
  for (let i = 0; i < n; i++) {
    const lo = Math.max(0, i - h);
    const hi = Math.min(n - 1, i + h);
    const cnt = pc[hi + 1] - pc[lo];
    out[i] = cnt > 0 ? (ps[hi + 1] - ps[lo]) / cnt : null;
  }
  return out;
}

// smoothPoints — bir serinin {time,value} noktalarını yumuşatır (x korunur).
// Pencere serinin KENDİ nokta sayısından türer (fanout'ta pod'lar ayrı seri).
export function smoothPoints(
  points: { time: number; value: number | null }[],
): { time: number; value: number | null }[] {
  const k = smoothWindow(points.length);
  if (k <= 1) return points;
  const sm = smoothValues(points.map(p => p.value), k);
  return points.map((p, i) => ({ time: p.time, value: sm[i] }));
}
