// horizonNotice — /databases'in iki panelinin veri ufku beyanı
// (v0.10.18, F0.9a).
//
// ── DÜZELTİLEN ŞEY BİR EKSİKLİK DEĞİL, YANLIŞ TEŞHİS ─────────────────────
//
// Sayfada iki panel alt alta duruyor ve ufukları farklı: üstteki MV'den
// okuyor (90 gün), alttaki ham metric_points'ten (varsayılan 7 gün).
// Operatör `?range=30d` seçtiğinde üstteki dolu, alttaki boş çıkıyor ve
// boş panel şunu basıyordu:
//
//     "No receiver-detected instances in this window. Point an
//      OpenTelemetry database receiver … at one of your databases"
//
// Yani operatöre KURULUM eksik diyordu. Gerçekte receiver çalışıyor,
// veri 23 gün önce TTL'e düşmüştü. Bu, ilan edilmemiş bir sınırdan daha
// kötü: operatörü var olmayan bir kurulum sorununu kovalamaya gönderiyor.
//
// ── NEDEN SAF ───────────────────────────────────────────────────────────
//
// Kararın tamamı üç sayıdan çıkıyor (pencere, ufuk, satır sayısı) ve
// hangi cümlenin basılacağı bir doğruluk sorusu. Saf tutuluyor ki
// canlı CH olmadan test edilebilsin.

export interface HorizonNotice {
  /** Beyanın tonu: boş panelin sebebini AÇIKLIYOR mu, yoksa sadece sınır mı bildiriyor. */
  kind: 'explains-empty' | 'declares-limit';
  text: string;
}

/** Gün sayısını okunur süreye çevirir. */
export function fmtHorizon(days: number): string {
  if (days >= 2) return `${days} gün`;
  return '1 gün';
}

/**
 * receiverHorizonNotice — receiver paneli için beyan.
 *
 * @param windowSec  operatörün seçtiği pencere (saniye)
 * @param horizonDays sunucunun bildirdiği ETKİN saklama (0 = bilinmiyor)
 * @param rowCount   panelde kaç satır var
 *
 * null dönerse hiçbir şey ilan edilmez.
 */
export function receiverHorizonNotice(
  windowSec: number,
  horizonDays: number | undefined,
  rowCount: number,
): HorizonNotice | null {
  // Ufuk bilinmiyorsa SUS. Uydurma bir sayı ilan etmek, ilan etmemekten
  // kötüdür — düzeltmenin kendisi yeni bir yalan olur.
  if (!horizonDays || horizonDays <= 0) return null;
  if (!Number.isFinite(windowSec) || windowSec <= 0) return null;

  const horizonSec = horizonDays * 86400;
  // Pencere ufkun içindeyse ortada bir asimetri YOK; beyan gürültü olur.
  if (windowSec <= horizonSec) return null;

  const h = fmtHorizon(horizonDays);
  if (rowCount === 0) {
    // ⚠ ASIL DÜZELTME. Boş panelin sebebi kurulum DEĞİL, saklama.
    return {
      kind: 'explains-empty',
      text:
        `Bu panel ham metric_points okuyor ve saklama ${h} — seçtiğin pencere daha uzun. ` +
        `Panelin boş olması receiver'ın kurulu olmadığı anlamına GELMEZ; ` +
        `${h} öncesinin verisi silinmiş olabilir. Pencereyi ${h} ya da altına indirip tekrar bak.`,
    };
  }
  return {
    kind: 'declares-limit',
    text: `Bu panel ham metric_points okuyor; saklama ${h} — pencerenin daha eski kısmı boş görünür.`,
  };
}

/**
 * spanHorizonNotice — üst panel (spans/MV) için beyan.
 *
 * Denetimde YOKTU; doğrulama sırasında çıktı. env süzgeci açıkken okuma
 * MV'yi bırakıp ham spans'e düşüyor ve ufuk 90'dan spans saklamasına
 * iniyor. Sayfa KAYNAĞIN değiştiğini zaten söylüyor ("ham spans") ama
 * UFKUN kısaldığını söylemiyor.
 */
export function spanHorizonNotice(
  windowSec: number,
  horizonDays: number | undefined,
): HorizonNotice | null {
  if (!horizonDays || horizonDays <= 0) return null;
  if (!Number.isFinite(windowSec) || windowSec <= 0) return null;
  if (windowSec <= horizonDays * 86400) return null;
  return {
    kind: 'declares-limit',
    text: `Bu panelin ufku ${fmtHorizon(horizonDays)} — seçtiğin pencerenin daha eski kısmı boş görünür.`,
  };
}
