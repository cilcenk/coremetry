import { describe, it, expect } from 'vitest';
import { shouldShowAnnouncement, announcementDismissValue } from './announcement';

// v0.8.486 — duyuru şeridi kapatma/revizyon mantığı.
describe('shouldShowAnnouncement', () => {
  const a = { enabled: true, text: 'Wiki: http://w', updatedAtNs: 111 };

  it('aktif + kapatılmamış → görünür', () => {
    expect(shouldShowAnnouncement(a, null)).toBe(true);
  });
  it('aynı revizyon kapatılmış → gizli', () => {
    expect(shouldShowAnnouncement(a, announcementDismissValue(a))).toBe(false);
  });
  it('metin güncellenince (yeni damga) yeniden görünür', () => {
    expect(shouldShowAnnouncement({ ...a, updatedAtNs: 222 }, '111')).toBe(true);
  });
  it('kapalı / boş metin / null → gizli', () => {
    expect(shouldShowAnnouncement({ ...a, enabled: false }, null)).toBe(false);
    expect(shouldShowAnnouncement({ enabled: true, text: '   ' }, null)).toBe(false);
    expect(shouldShowAnnouncement(null, null)).toBe(false);
    expect(shouldShowAnnouncement(undefined, '111')).toBe(false);
  });
});
