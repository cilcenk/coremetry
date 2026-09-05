// v0.9.594 — RCA kalite panelinin SAF karar mantığı.
//
// Panel JSX; repoda bileşen testi altyapısı yok. Ama buradaki
// kararların hepsi "operatör yanlış bir sonuç çıkarır mı" sorusuna
// bakıyor ve sessizce bozulabilirler — o yüzden saf ve testli.
//
// Asıl tehlike sıfıra bölme DEĞİL, ORANI SIFIR GİBİ GÖSTERMEK:
// "hiç veri yok" ile "%0" ekranda aynı görünürse, motor daha hiç
// çalışmamışken operatör "hiç kök neden bulamıyor" okur. Aynı hata
// sınıfı v0.9.560'ta etki rakamlarında düzeltildi ("ölçülemedi" ≠ 0).
import type { RCAVerdictQuality, RCAConfidenceBucket } from '@/lib/types';

/**
 * Yüzde — payda sıfırsa null.
 *
 * null çağırana "bunu yazma" der; 0 yazsaydık yokluk bir BULGU gibi
 * görünürdü.
 */
export function rcaPct(part: number, total: number): number | null {
  if (!Number.isFinite(part) || !Number.isFinite(total) || total <= 0) return null;
  return (part / total) * 100;
}

/** Yüzde metni; ölçülemeyende em-dash. */
export function rcaPctText(part: number, total: number): string {
  const p = rcaPct(part, total);
  return p === null ? '—' : `%${p.toFixed(0)}`;
}

/**
 * Motor sağlığı tonu — kalkanlar ve çözümlenememe oranından.
 *
 * NEDEN insufficient_evidence bu hesaba GİRMİYOR: o bir ARIZA değil,
 * geçerli bir cevap. Prompt modele açıkça "bunu demek ayıp değil"
 * diyor; panelde kırmızı yapmak modeli tam da kaçınmasını istemediğimiz
 * yöne iter — kendinden emin ve yanlış cevaba.
 *
 * Ölçülen iki şey gerçek arıza:
 *   unparsed → model şemaya uymadı, karar BİZİM deterministik düşüşümüz
 *   shielded → model uydurdu, kalkan yakaladı
 */
export function rcaEngineTone(q: RCAVerdictQuality): 'ok' | 'warn' | 'err' | undefined {
  if (q.total <= 0) return undefined; // veri yok — renk de yok
  const bad = rcaPct(q.unparsed + q.shielded, q.total);
  if (bad === null) return undefined;
  if (bad >= 30) return 'err';
  if (bad >= 10) return 'warn';
  return 'ok';
}

/**
 * Memnuniyet metni.
 *
 * Oy YOKSA "%0" değil "oy yok" — oylama seyrek bir jest ve sıfır oyu
 * sıfır memnuniyet diye okumak motoru haksız yere kötü gösterir.
 */
export function rcaSatisfactionText(q: RCAVerdictQuality): string {
  const votes = q.thumbsUp + q.thumbsDown;
  if (votes <= 0) return 'oy yok';
  return `%${((q.thumbsUp / votes) * 100).toFixed(0)} (${votes} oy)`;
}

/**
 * v0.10.410 — güven kovası etiketi; sınırlar SUNUCUDAN gelir (tek kaynak),
 * burada yalnız yazım kuralı: low üstü hariç, mid iki ucu dahil, high altı hariç.
 */
export function rcaBucketLabel(b: RCAConfidenceBucket): string {
  const f = (x: number) => x.toFixed(2);
  switch (b.bucket) {
    case 'low': return `düşük (<${f(b.hi)})`;
    case 'mid': return `orta (${f(b.lo)}–${f(b.hi)})`;
    default: return `yüksek (>${f(b.lo)})`;
  }
}

/** Kova memnuniyeti — oy yoksa "oy yok" (rcaSatisfactionText ile aynı kural). */
export function rcaBucketSatisfaction(b: RCAConfidenceBucket): string {
  const votes = b.thumbsUp + b.thumbsDown;
  if (votes <= 0) return 'oy yok';
  return `%${((b.thumbsUp / votes) * 100).toFixed(0)} (${votes} oy)`;
}

/**
 * Kalibrasyon notu — "yüksek güven" kovası yeterli oyla (≥5) çoğunlukla
 * 👎 aldıysa güven sayısı güvenilir değildir; operatöre bunu söyle.
 * Aksi hâlde null: az oyla hüküm vermek oylamayı cezalandırır.
 */
export function rcaCalibrationNote(buckets: RCAConfidenceBucket[]): string | null {
  const high = buckets.find(b => b.bucket === 'high');
  if (!high) return null;
  const votes = high.thumbsUp + high.thumbsDown;
  if (votes < 5) return null;
  if (high.thumbsUp / votes < 0.5) return 'yüksek güven kovası çoğunlukla 👎 — güven sayısı kalibre değil';
  return null;
}
