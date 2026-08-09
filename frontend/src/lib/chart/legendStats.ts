// legendStats.ts (v0.9.103, Grafana-parity #1) — grafik lejantı için seri-başı
// istatistik çekirdeği. Saf + test'li (lib/chart/tooltipModel.ts emsali);
// OVC + TC paylaşılan StatsLegend bileşeni tüketir.

export interface SeriesStat {
  last: number | null;   // son DOLU örnek (null → boş)
  min: number | null;
  max: number | null;
  mean: number | null;
  sum: number;           // dolu örneklerin toplamı (count=0 → 0)
  count: number;         // dolu örnek sayısı
}

// seriesStats — null/NaN atlayarak last/min/max/mean/sum/count. Hepsi boşsa
// last/min/max/mean null, sum 0.
export function seriesStats(values: ReadonlyArray<number | null | undefined>): SeriesStat {
  let last: number | null = null;
  let min = Infinity;
  let max = -Infinity;
  let sum = 0;
  let count = 0;
  for (const v of values) {
    if (v == null || !isFinite(v)) continue;
    last = v;            // son dolu örnek
    if (v < min) min = v;
    if (v > max) max = v;
    sum += v;
    count++;
  }
  if (count === 0) return { last: null, min: null, max: null, mean: null, sum: 0, count: 0 };
  return { last, min, max, mean: sum / count, sum, count };
}

// resolveLegendCollapsed (v0.9.483) — "▶/▼ Series (N)" tablosunun AÇILIŞ
// durumu. Öncelik legendVisibility'nin persist kuralının aynısı:
//   kullanıcının kalıcı seçimi > çağıranın default'u > seri-sayısı eşiği
// Kullanıcı bir kez açtıysa (stored=false) o grafikte açık kalır; hiç
// dokunmadıysa panel kendi default'unu söyler (Overview RED kartları:
// kapalı — operatör "dikey alanı yiyor" dedi). Bozuk/eksik kayıt null →
// default kazanır (getItem<T> zaten fallback'e düşer).
export function resolveLegendCollapsed(
  stored: boolean | null | undefined,
  defaultCollapsed: boolean | undefined,
  seriesCount: number,
  threshold: number,
): boolean {
  if (typeof stored === 'boolean') return stored;
  if (typeof defaultCollapsed === 'boolean') return defaultCollapsed;
  return seriesCount > threshold;
}

// isAdditiveUnit — Sum/Σ (ve "Toplam" satırı) bu birimde ANLAMLI mı?
// Toplanabilir: boş (sayaç/adet), oran/hız (rps, req/s, /s, ops), bytes
// (B/KB/MB/GB/bytes ve UCUM 'By' ailesi). Toplanamaz: yüzde (%),
// gecikme/süre (ms/s/µs/ns/min/h) — pod'lar arası p95 latency'yi TOPLAMAK
// anlamsız. Belirsizde KAPALI (Sum gizlensin — yanlış toplam göstermekten
// iyidir).
export function isAdditiveUnit(unit: string | undefined): boolean {
  const u = (unit || '').trim().toLowerCase();
  if (u === '') return true;                                   // birimsiz sayaç/adet
  if (u.includes('%')) return false;                          // yüzde
  // v0.9.851 — UCUM BAYT AİLESİ ('By', 'KiBy', 'MiBy', 'GiBy', 'kBy'…).
  //
  // Bu bir YAZIM boşluğuydu, semantik bir karar değil: 'MB', 'GB', 'bytes',
  // 'decbytes' zaten aşağıdaki desene takılıp toplanabilir sayılıyordu, yani
  // "bayt toplanır" kararı çoktan verilmişti. Yalnız OTel'in kendi yazdığı
  // biçim ('By') desende yoktu ve o birimi HAM taşıyan yüzeylerde Σ/"Toplam"
  // sütunu "—" basıyordu. Desene eklenmesi tek satır ama etkisi GENEL:
  // isAdditiveUnit hem CorePanel lejantının Σ'sını hem Explore GroupTable'ın
  // "Toplam" sütununu hem de Δ%'nin hangi sayı ailesini (toplam mı ortalama
  // mı) karşılaştıracağını yönetir.
  //
  // 'By' düz desene eklenemezdi: `\b[kmgt]?b\b` 'by' içinde eşleşmiyor (b'den
  // sonra 'y' geliyor, kelime sınırı yok). O yüzden AYRI ve TAM eşleşme.
  if (/^(ki|mi|gi|ti|[kmgt])?by$/.test(u)) return true;
  // hız / oran / adet / bytes → toplanabilir (süreden ÖNCE bakılır ki
  // "req/s" gecikme sanılıp elenmesin).
  if (/\/s\b|\brps\b|\breq|\bops\b|count|error|request|byte|\b[kmgt]?b\b/.test(u)) return true;
  // süre / gecikme → toplanamaz
  if (/\b(ms|s|sec|secs|ns|µs|us|min|h|hr)\b/.test(u)) return false;
  return false;
}
