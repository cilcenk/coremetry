// @vitest-environment jsdom
//
// v0.9.1359 — CI kırmızıydı, lokal yeşildi. Bu dosya `sessionStorage`/
// `localStorage` KULLANIYOR ve vitest.config.ts ortamı bilinçli `node`
// (2000+ saf test jsdom bedelini ödemesin; dosya başına opt-in). Node 25
// bu global'leri yerleşik taşıyor, CI'ın Node 22.si TAŞIMIYOR — yani test
// lokalde ÇALIŞMA ZAMANI SÜRÜMÜ sayesinde geçiyordu, kendi hakkıyla değil.
// jsdom onları sürümden bağımsız sağlar.
// useUrlRange.test.ts — öncelik zinciri ve oturum kaydı sözleşmesi.
//
// v0.9.937 SÖZLEŞME DEĞİŞİKLİĞİ. Bu dosya v0.8.409'un tersini pinliyordu:
// "mutlak (custom:) pencere ASLA kalıcı olmaz". O kural doğruydu ÇÜNKÜ
// kayıt localStorage'daydı — brush'lanan bir pencere haftalar sonra bile
// her sayfayı geçmişte açıyordu ("yeni traceler gelmiyor, cacheten
// getiriyor sanırım"). Kayıt sekme OTURUMUNA taşındı ve kayıttan gelen
// aralık artık URL'E YAZILIYOR; o olayın iki kök nedeni (sınırsız kapsam
// + görünmezlik) de kesildi, dolayısıyla mutlak pencereyi elemek
// gereksiz ve operatörün seçimini yutan bir davranış hâline geldi.
//
// Eski kural silinmedi, YERİ DEĞİŞTİ: aşağıdaki oturum-semantiği testi
// v0.8.409'un asıl garantisini (bayat mutlak pencere yarına kalmaz)
// yeni mekanizmayla pinliyor.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pickRangeString, storedRangeString } from './useUrlRange';
import { getRaw, getSessionRaw, setSessionRaw, STORAGE_KEYS } from './storage';

describe('pickRangeString — öncelik zinciri (v0.9.855, v0.9.937)', () => {
  it('URL her şeyi ezer — sayfada görünen pencere kazanır', () => {
    expect(pickRangeString('6h', '24h', '30m')).toBe('6h');
    expect(pickRangeString('custom:1000-2000', '24h', '30m')).toBe('custom:1000-2000');
  });

  it('URL yoksa oturum kaydı, o da yoksa varsayılan', () => {
    expect(pickRangeString(null, '24h', '30m')).toBe('24h');
    expect(pickRangeString(null, null, '30m')).toBe('30m');
  });

  it('v0.9.937 — oturumdaki MUTLAK pencere de geçerli bir cevap', () => {
    // v0.8.409'da bu satır '30m' bekliyordu: kayıt KALICIYDI, yani
    // custom: geri sızarsa her sayfa geçmişte açılırdı. Kayıt oturum
    // kapsamına indiği ve URL'e yazıldığı için artık operatörün bilerek
    // seçtiği pencere sekme boyunca korunur.
    expect(pickRangeString(null, 'custom:1000-2000', '30m')).toBe('custom:1000-2000');
  });

  it('useUrlRange ile AYNI zinciri uygular', () => {
    // useUrlRange: raw ?? readStoredRange() ?? defaultPreset.
    // Üç basamak, aynı sıra, HİÇBİRİNDE eleme yok.
    for (const [url, stored, def, want] of [
      ['1h', '7d', '30m', '1h'],
      [null, '7d', '30m', '7d'],
      [null, 'custom:5-9', '30m', 'custom:5-9'],
      [null, null, '15m', '15m'],
    ] as Array<[string | null, string | null, string, string]>) {
      expect(pickRangeString(url, stored, def)).toBe(want);
    }
  });
});

describe('oturum kaydı (v0.9.937)', () => {
  beforeEach(() => {
    try { sessionStorage.clear(); } catch { /* jsdom */ }
  });

  it('anahtar SEKME oturumunda — sekme kapanınca kayıt gider', () => {
    // v0.8.409'un asıl garantisi burada: kayıt sessionStorage'da, yani
    // yeni bir sekme (ya da yarın) onu GÖRMEZ. localStorage'a düşerse
    // bu test kırılır ve donmuş-pencere olayı geri gelmiş olur.
    expect(STORAGE_KEYS.lastRange).toBe('cm.lastRange');
    setSessionRaw(STORAGE_KEYS.lastRange, 'custom:1000-2000');
    expect(sessionStorage.getItem('cm.lastRange')).toBe('custom:1000-2000');
    // getRaw = kalıcı (localStorage) kanal. Boş olmalı: kayıt oraya
    // düşerse v0.8.409'un donmuş-pencere olayı geri gelir.
    expect(getRaw(STORAGE_KEYS.lastRange)).toBeNull();
    expect(storedRangeString()).toBe('custom:1000-2000');
  });

  it('relatif rung ve mutlak pencere — İKİSİ de saklanır', () => {
    for (const enc of ['5m', '30m', '24h', '7d', 'custom:1751980000000-1751983600000']) {
      setSessionRaw(STORAGE_KEYS.lastRange, enc);
      expect(storedRangeString()).toBe(enc);
    }
  });

  it('kayıt yokken null — varsayılana düşülür', () => {
    expect(storedRangeString()).toBeNull();
  });

  it('storage devre dışıyken sayfa ÇÖKMEZ (özel mod / iframe)', () => {
    const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    expect(() => getSessionRaw(STORAGE_KEYS.lastRange)).not.toThrow();
    expect(getSessionRaw(STORAGE_KEYS.lastRange)).toBeNull();
    spy.mockRestore();
  });
});

// ── Kaynak-pinleri: sahiplik sözleşmesi ─────────────────────────────
//
// useUrlRange(defaultPreset) VERMEK "bu sayfanın penceresine sahibim"
// demek ve yalnız sahip olan çağıran oturumdaki aralığı URL'e yazar.
// Sözleşme derleyiciyle zorlanamıyor (ikisi de geçerli çağrı), o yüzden
// burada pinli.
describe('sahiplik sözleşmesi (v0.9.937)', () => {
  const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');

  it('salt-okur global tüketiciler ARGÜMANSIZ çağırır', () => {
    // Bu ikisi AppShell'de global mount ediliyor. Varsayılan verirlerse
    // (a) aralığı olmayan her sayfanın URL'i kirlenir ve (b) kendi
    // '30m' varsayılanları sayfanın '1h' varsayılanını EZER.
    for (const f of ['../components/CopilotChat.tsx', '../components/FilterBuilder.tsx']) {
      const src = read(f);
      expect(src).toMatch(/useUrlRange\(\)/);
      expect(src).not.toMatch(/useUrlRange\(['"]/);
    }
  });

  it('URL yazımı sahiplik VE boş-URL koşuluna bağlı', () => {
    const src = read('./useUrlRange.ts');
    expect(src).toContain("if (!owns || raw !== null || !stored) return;");
  });

  it('yabancı paramlar window.location.search üzerinden korunur', () => {
    // Router `prev`i ham replaceState yazan sayfalarda (Traces/Explore)
    // bayat bir ALT KÜME olabiliyor; updater biçimiyle yazsaydık o
    // sayfaların paramlarını sessizce düşürürdük.
    const src = read('./useUrlRange.ts');
    expect(src).toContain('new URLSearchParams(window.location.search)');
  });
});
