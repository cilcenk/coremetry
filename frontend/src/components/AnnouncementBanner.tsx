import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { Button } from '@/components/ui';
import { api } from '@/lib/api';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import { shouldShowAnnouncement, announcementDismissValue, type AnnouncementView } from '@/lib/announcement';

// AnnouncementBanner — v0.8.486 (operatör isteği). Kaldırılan
// What-changed şeridinin (v0.8.481) yerinde, admin'in Settings'ten
// girdiği duyuru: "sorularınız için: …" / "wiki: http://…". ✕ o
// revizyonu localStorage'a yazar; admin metni güncelleyince şerit
// herkese yeniden görünür. 5 dk staleTime — poll yok, duyuru anlık
// olmak zorunda değil.
export function AnnouncementBanner() {
  const q = useQuery<AnnouncementView>({
    queryKey: ['announcement'],
    queryFn: () => api.getAnnouncement(),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const [dismissedRev, setDismissedRev] = useState<string | null>(
    () => getRaw(STORAGE_KEYS.announcementDismissed));

  const a = q.data;
  if (!shouldShowAnnouncement(a, dismissedRev)) return null;

  const warn = a!.tone === 'warn';
  return (
    <div role="status" style={{
      display: 'flex', alignItems: 'center', gap: 10,
      padding: '6px 14px', fontSize: 12.5,
      background: warn
        ? 'color-mix(in srgb, var(--warn) 12%, var(--bg1))'
        : 'color-mix(in srgb, var(--accent) 8%, var(--bg1))',
      borderBottom: '1px solid var(--border)',
      color: 'var(--text)',
    }}>
      <span aria-hidden>{warn ? '⚠' : '📣'}</span>
      <span style={{ minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {a!.text}
      </span>
      {a!.linkUrl && (
        <a href={a!.linkUrl} target="_blank" rel="noreferrer"
          style={{ color: 'var(--accent2)', whiteSpace: 'nowrap' }}>
          {a!.linkLabel || a!.linkUrl} ↗
        </a>
      )}
      <span style={{ flex: 1 }} />
      <Button variant="secondary" size="sm" aria-label="Duyuruyu kapat"
        onClick={() => {
          const rev = announcementDismissValue(a!);
          setRaw(STORAGE_KEYS.announcementDismissed, rev);
          setDismissedRev(rev);
        }}>✕</Button>
    </div>
  );
}
