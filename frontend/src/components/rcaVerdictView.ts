// v0.9.560 — RCA verdict panelinin SAF karar mantığı.
//
// Panelin kendisi JSX; repoda bileşen testi altyapısı yok. Ama panelin
// DÜRÜSTLÜK kuralları test edilebilir olmalı, çünkü hepsi "operatör
// yanlış bir şey okur mu" sorusuna bakıyor ve sessizce bozulabilirler.
//
// Buradaki üç fonksiyon, tasarımın (docs/cosre-verdict-design.md) dört
// dürüstlük kuralından üçünü taşıyor. Dördüncüsü (kanıtın iddianın
// ALTINDA, ayrı sesle çizilmesi) yerleşim kararı — o JSX'te.
import type { RCAVerdict } from '@/lib/types';
import type { Tone } from '@/components/ui/Badge';

/** Verdict → rozet tonu. */
export function verdictTone(v: RCAVerdict['verdict']): Tone {
  switch (v) {
    case 'root_cause_identified':
      return 'success';
    case 'probable_cause':
      return 'warning';
    default:
      // "Kanıt yetersiz" bir HATA değil, geçerli bir cevap. Kırmızı
      // yapmak onu bir başarısızlık gibi gösterir; modeli bu cevaptan
      // kaçınmaya iten sinyal tam olarak istemediğimiz şey.
      return 'neutral';
  }
}

/**
 * Model gerçekten yanıt verdi mi?
 *
 * false ⇒ ekranda UYARI ŞART. Aksi hâlde "kanıt yetersiz" kararı bir
 * BULGU sanılır; oysa bir ULAŞILAMAMA'dır ve ikisi çok farklı şeyler:
 * biri "AI baktı, bir şey bulamadı", diğeri "AI hiç cevap veremedi".
 */
export function verdictIsDegraded(v: RCAVerdict): boolean {
  return v.shields?.parsed === false;
}

/**
 * Kalkanlar bir şey yakaladı mı — doğrulama uyarısı gösterilmeli mi?
 *
 * Kalkanın tetiklenmesi kararı geçersiz kılmaz ama operatör "bu karar
 * neye RAĞMEN üretildi"yi görebilmeli.
 */
export function verdictHasShieldWarning(v: RCAVerdict): boolean {
  const s = v.shields;
  if (!s) return false;
  return Boolean(
    (s.unknownEntities && s.unknownEntities.length > 0) ||
    (s.rejectedEvidence && s.rejectedEvidence.length > 0) ||
    s.refutationInvalid,
  );
}

/**
 * Ölçülmüş sayıyı metne çevirir; ölçülmemişse null döner.
 *
 * TEK GİRİŞ NOKTASI olmasının sebebi: `0` ile `ölçülemedi` ekranda
 * asla aynı görünmemeli. Tam patlama anında MV henüz materyalize
 * olmamış olabilir ve "0 hata" yazmak "etkilenen istek yok" demektir —
 * operatörün okuyacağı en yanlış cümle.
 */
export function measuredText(
  n: number | null | undefined,
  fmt: (v: number) => string,
): string | null {
  if (n === null || n === undefined) return null;
  return fmt(n);
}
