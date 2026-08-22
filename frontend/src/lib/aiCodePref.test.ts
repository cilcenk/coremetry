// @vitest-environment jsdom
//
// v0.9.1238 — "Kodu da incele" tercihinin KALICILIK sözleşmesi.
//
// Denetim bulgusu: kutu her çekmece açılışında sıfırlanıyordu; auto modda
// ilk tur her seferinde kodsuz gidiyor, operatör kutuyu yeniden
// işaretleyince aynı soruya İKİNCİ bir yerel LLM turu ödeniyordu.
//
// Bu tablo üç kararı birden ölçüyor, çünkü üçü birbirinin sigortası:
//   (1) anahtar YOKKEN false — hatırlamak, v0.9.831'in bilinçli KAPALI
//       varsayılanını çevirmek DEĞİL;
//   (2) yalnız tam olarak '1' açık sayılır — bozuk/eski bir değer
//       (JSON, 'true', boş) sessizce kod çekmesi BAŞLATAMAZ;
//   (3) codeCapable=false (kod okunamayan tür) kayıtlı tercihi EZER —
//       aksi hâlde /problems açıklaması "CoSRE kodu okuyor…" derdi,
//       hiçbir kod okunmazken.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readIncludeCodePref, writeIncludeCodePref } from './aiCodePref';
import { getRaw, setRaw, STORAGE_KEYS } from './storage';

// Bellek-içi localStorage. jsdom'unki YETMEZ: Node 22'nin deneysel
// yerleşik localStorage'ı onu gölgeliyor ve metotları çalışmıyor
// (CorePanel.smoke.test.tsx'te aynı tuzak belgeli). storage.ts yalnız
// getItem/setItem/removeItem çağırıyor, Map'lik bir taklit yeterli.
function stubStorage() {
  const store = new Map<string, string>();
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => { store.set(k, String(v)); },
    removeItem: (k: string) => { store.delete(k); },
  });
}

beforeEach(stubStorage);
afterEach(() => vi.unstubAllGlobals());

describe('readIncludeCodePref', () => {
  const cases: { name: string; stored?: string; codeCapable: boolean; want: boolean }[] = [
    { name: 'anahtar yok → KAPALI (ilk kullanım varsayılanı)', codeCapable: true,  want: false },
    { name: "'1' → operatörün açık seçimi hatırlanır",  stored: '1',      codeCapable: true,  want: true  },
    { name: "'0' → kapatma da bir seçimdir",            stored: '0',      codeCapable: true,  want: false },
    { name: "'true' (yabancı yazım) açık SAYILMAZ",     stored: 'true',   codeCapable: true,  want: false },
    { name: 'boş dize açık SAYILMAZ',                   stored: '',       codeCapable: true,  want: false },
    { name: 'kod okunamayan tür: kayıtlı açık tercih EZİLİR', stored: '1', codeCapable: false, want: false },
    { name: 'kod okunamayan tür + anahtar yok',         codeCapable: false, want: false },
  ];
  for (const c of cases) {
    it(c.name, () => {
      if (c.stored !== undefined) setRaw(STORAGE_KEYS.aiIncludeCode, c.stored);
      expect(readIncludeCodePref(c.codeCapable)).toBe(c.want);
    });
  }
});

describe('writeIncludeCodePref', () => {
  it('gidiş-dönüş: yazılan seçim aynen okunur', () => {
    writeIncludeCodePref(true);
    expect(readIncludeCodePref(true)).toBe(true);
    writeIncludeCodePref(false);
    expect(readIncludeCodePref(true)).toBe(false);
  });

  it('kapatma anahtarı SİLMEZ, "0" yazar', () => {
    // Silmek "hiç seçmedi" ile aynı hâle düşerdi: varsayılan ileride
    // değişirse operatörün açık HAYIR'ı sessizce EVET'e dönerdi.
    writeIncludeCodePref(false);
    expect(getRaw(STORAGE_KEYS.aiIncludeCode)).toBe('0');
  });
});
