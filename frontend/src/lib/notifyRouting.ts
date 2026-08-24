import type { NotificationLogEntry } from '@/lib/types';

// v0.9.1344 — bir problemin bildirim geçmişinden çıkarılan SAF özet.
//
// Backend sentetik bir "kimseye gitmedi" satırı yazıyor
// (channelKind='none', channelName='unmatched', ok=false, error=gerekçe).
// O satır bir KANAL DEĞİL — gerçek gönderimlerle aynı listede
// gösterilmesi "başarısız bir kanal" gibi okunurdu. GİTMEDİ ile
// BAŞARISIZ farklı şeylerdir: ilki yönlendirme kusuru, ikincisi
// teslimat arızası ve ikisinin düzeltmesi de farklı.
export const UNMATCHED_KIND = 'none';

export interface NotifyRoutingSummary {
  /** Sentetik işaret satırı — varsa bu probleme KİMSE bakmıyor olabilir. */
  unmatched?: NotificationLogEntry;
  /** Gerçek gönderimler (işaret hariç), yeniden eskiye. */
  sends: NotificationLogEntry[];
  /** Başarılı gönderim sayısı. */
  okCount: number;
  /** Başarısız gönderim sayısı — TESLİMAT arızası, yönlendirme değil. */
  failCount: number;
}

/**
 * summarizeNotifyRouting — satırları işaret / gerçek gönderim diye ayırır.
 *
 * Boş girdi boş özet döner; çağıran "hiç bildirim yok" ile "kimseye
 * gitmedi"yi AYRI çizer. İkisi aynı şey değil: yapılandırılmamış bir
 * kurulumda backend işaret hiç yazmaz, yani boş liste bir kusur iddiası
 * DEĞİLDİR.
 */
export function summarizeNotifyRouting(
  rows: NotificationLogEntry[] | undefined,
): NotifyRoutingSummary {
  const out: NotifyRoutingSummary = { sends: [], okCount: 0, failCount: 0 };
  for (const r of rows ?? []) {
    if (r.channelKind === UNMATCHED_KIND) {
      // En YENİ işaret kazanır. Satırlar yeniden eskiye geliyor, ama
      // sıraya güvenmek yerine damgayı karşılaştırıyoruz: bir gün
      // sıralama değişirse bu sessizce eski gerekçeyi gösterirdi.
      if (!out.unmatched || r.sentAt > out.unmatched.sentAt) out.unmatched = r;
      continue;
    }
    out.sends.push(r);
    if (r.ok) out.okCount++;
    else out.failCount++;
  }
  return out;
}
