import { describe, it, expect } from 'vitest';
import { summarizeNotifyRouting, UNMATCHED_KIND } from './notifyRouting';
import type { NotificationLogEntry } from '@/lib/types';

// v0.9.1344 — sentetik "kimseye gitmedi" satırı bir KANAL DEĞİL.
//
// /events'te `ok ? sent : failed` ikilisi onu "failed" diye çiziyordu ve
// operatör ölü bir kanal arardı. GİTMEDİ (yönlendirme kusuru: eşleşme
// kuralı dar) ile BAŞARISIZ (teslimat arızası: SMTP ölü) farklı
// arızalar ve farklı düzeltmeler. Bu ayrıştırıcı ikisini ayırıyor.

function row(over: Partial<NotificationLogEntry>): NotificationLogEntry {
  return {
    id: 'nl-1', sentAt: 1_000, channelKind: 'email', channelName: 'oncall',
    target: 'a@b.c', subject: 's', bodyPreview: '', relatedKind: 'problem',
    relatedId: 'p1', ok: true, error: '', ...over,
  };
}

describe('summarizeNotifyRouting', () => {
  it('boş girdi kusur İDDİA ETMEZ', () => {
    // Yapılandırılmamış bir kurulumda backend işaret hiç yazmaz, yani
    // boş liste "kimse haber almadı" diye okunmamalı.
    const s = summarizeNotifyRouting(undefined);
    expect(s.unmatched).toBeUndefined();
    expect(s.sends).toEqual([]);
    expect(s.okCount).toBe(0);
    expect(s.failCount).toBe(0);
  });

  it('işareti gerçek gönderimlerden AYIRIR', () => {
    const s = summarizeNotifyRouting([
      row({ id: 'a', channelKind: UNMATCHED_KIND, channelName: 'unmatched', ok: false, error: 'gerekçe' }),
      row({ id: 'b' }),
    ]);
    expect(s.unmatched?.id).toBe('a');
    // İşaret sends'e SIZMAMALI: sızarsa panelde "başarısız bir kanal"
    // gibi görünür ve operatör var olmayan bir kanalı tamir etmeye
    // çalışır.
    expect(s.sends.map(r => r.id)).toEqual(['b']);
    expect(s.okCount).toBe(1);
    expect(s.failCount).toBe(0);
  });

  it('teslimat hatası işaret DEĞİLDİR', () => {
    // Kanal doğru eşleşti, hedef kabul etmedi. Bu kayıp değil, arıza.
    const s = summarizeNotifyRouting([row({ ok: false, error: 'smtp timeout' })]);
    expect(s.unmatched).toBeUndefined();
    expect(s.failCount).toBe(1);
    expect(s.sends).toHaveLength(1);
  });

  it('birden çok işaret varsa EN YENİSİ kazanır', () => {
    // Açılış + çözülme ayrı işaret yazabilir; eski gerekçeyi göstermek
    // operatörü yanlış kanala bakmaya yollardı. Sıraya güvenilmiyor.
    const s = summarizeNotifyRouting([
      row({ id: 'eski', sentAt: 100, channelKind: UNMATCHED_KIND, error: 'eski gerekçe' }),
      row({ id: 'yeni', sentAt: 900, channelKind: UNMATCHED_KIND, error: 'yeni gerekçe' }),
    ]);
    expect(s.unmatched?.id).toBe('yeni');
    expect(s.unmatched?.error).toBe('yeni gerekçe');
  });

  it('yalnız işaret varsa gönderim listesi BOŞ kalır', () => {
    const s = summarizeNotifyRouting([
      row({ channelKind: UNMATCHED_KIND, ok: false, error: 'g' }),
    ]);
    expect(s.sends).toEqual([]);
    expect(s.okCount + s.failCount).toBe(0);
  });
});
