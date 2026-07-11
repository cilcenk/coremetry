// announcement — v0.8.486: sayfa-üstü duyuru şeridinin kapatma
// mantığı. Kullanıcı ✕ deyince O REVİZYON (updatedAtNs) localStorage'a
// yazılır; aynı duyuru bir daha çıkmaz, admin metni güncelleyince
// (yeni damga) şerit yeniden görünür. Pure — vitest'li.

export interface AnnouncementView {
  enabled: boolean;
  text?: string;
  linkUrl?: string;
  linkLabel?: string;
  tone?: 'info' | 'warn';
  updatedAtNs?: number;
}

// shouldShowAnnouncement — duyuru aktif + metinli VE bu revizyon
// kapatılmamışsa true.
export function shouldShowAnnouncement(
  a: AnnouncementView | null | undefined,
  dismissedRev: string | null,
): boolean {
  if (!a || !a.enabled || !a.text?.trim()) return false;
  if (!dismissedRev) return true;
  return dismissedRev !== String(a.updatedAtNs ?? 0);
}

export function announcementDismissValue(a: AnnouncementView): string {
  return String(a.updatedAtNs ?? 0);
}
