import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { tuningToForm, tuningToWire } from './aiTuning';

// aiTuning.test.ts — v0.9.1120 (Faz 0.5).
//
// KORUNAN SÖZLEŞME: bu üç alan ÜSTÜNE YAZMA (override), efektif değer
// DEĞİL. 0 / null = "yerleşik varsayılan çalışıyor" (4096 / 0.2 / 180s).
//
// Korkulan kusur SESSİZ: form boş kutuya varsayılanı yazarsa ekranda her
// şey doğru görünür, ama ilk Kaydet'te o varsayılan blob'a çakılır ve
// kurulum bir daha hiçbir varsayılan değişikliğini görmez. Hiçbir hata
// mesajı, hiçbir görsel iz yok — bu yüzden kapı testte yaşamak zorunda.
//
// Tablo hem "boş = sıfırla" yönünü hem de TERS yönü (sunucudaki 0/null
// tekrar boş kutuya dönüyor mu) sürüyor: tek yön test edilirse gidiş-
// dönüş bir sürüm sonra ayrışır.

describe('tuningToWire — kutu metni → tel', () => {
  const cases: Array<{ name: string; in: { maxTokens: string; temperature: string; timeoutS: string };
                       want: { maxTokens: number; temperature: number | null; timeoutS: number } }> = [
    { name: 'üçü de boş → tamamen sıfırla',
      in: { maxTokens: '', temperature: '', timeoutS: '' },
      want: { maxTokens: 0, temperature: null, timeoutS: 0 } },
    { name: 'üçü de dolu → aynen geçiyor',
      in: { maxTokens: '8192', temperature: '0.7', timeoutS: '300' },
      want: { maxTokens: 8192, temperature: 0.7, timeoutS: 300 } },
    // temperature 0 GEÇERLİ bir seçim; boşla aynı yere düşerse
    // operatörün bilerek istediği determinizm sessizce 0.2'ye kayar.
    { name: 'temperature 0 sıfırlama DEĞİL',
      in: { maxTokens: '', temperature: '0', timeoutS: '' },
      want: { maxTokens: 0, temperature: 0, timeoutS: 0 } },
    { name: 'sadece biri dolu — diğerleri varsayılanda kalıyor',
      in: { maxTokens: '', temperature: '', timeoutS: '600' },
      want: { maxTokens: 0, temperature: null, timeoutS: 600 } },
    { name: 'boşluk dolu kutu boş sayılıyor',
      in: { maxTokens: '   ', temperature: ' ', timeoutS: '\t' },
      want: { maxTokens: 0, temperature: null, timeoutS: 0 } },
    // Kullanıcı bir number input'a yapıştırma / silme sırasında çöp
    // bırakabiliyor. NaN göndermek backend'den açıklamasız 400 alırdı.
    { name: 'ayrıştırılamaz girdi sıfırlamaya düşüyor',
      in: { maxTokens: 'abc', temperature: '--', timeoutS: '1e' },
      want: { maxTokens: 0, temperature: null, timeoutS: 0 } },
    { name: 'ondalıklı token değeri yuvarlanıyor (int alan)',
      in: { maxTokens: '4096.6', temperature: '', timeoutS: '' },
      want: { maxTokens: 4097, temperature: null, timeoutS: 0 } },
    { name: 'temperature ondalık kalıyor (float alan)',
      in: { maxTokens: '', temperature: '1.25', timeoutS: '' },
      want: { maxTokens: 0, temperature: 1.25, timeoutS: 0 } },
  ];
  for (const c of cases) {
    it(c.name, () => expect(tuningToWire(c.in)).toEqual(c.want));
  }
});

describe('tuningToForm — sunucu override → kutu metni', () => {
  it('sıfır/null override BOŞ kutu (varsayılan placeholder görünsün)', () => {
    expect(tuningToForm({ maxTokens: 0, temperature: null, timeoutS: 0 }))
      .toEqual({ maxTokens: '', temperature: '', timeoutS: '' });
  });

  // Faz 0.4 öncesi bir backend bu alanları hiç göndermiyor. undefined da
  // "üstüne yazma yok" demektir — kutuya "undefined" basmak felaket olurdu.
  it('alan hiç gelmemişse de BOŞ', () => {
    expect(tuningToForm({})).toEqual({ maxTokens: '', temperature: '', timeoutS: '' });
    expect(tuningToForm(null)).toEqual({ maxTokens: '', temperature: '', timeoutS: '' });
  });

  it('gerçek override kutuya yazılıyor', () => {
    expect(tuningToForm({ maxTokens: 16384, temperature: 0.9, timeoutS: 45 }))
      .toEqual({ maxTokens: '16384', temperature: '0.9', timeoutS: '45' });
  });

  // temperature 0'ın iki ucu: tele 0 gidiyor (yukarıda) ve geri gelince
  // kutuda "0" görünüyor. Falsy kontrolü yazılırsa burada '' döner ve
  // operatörün ayarı her sayfa açılışında kaybolmuş gibi görünür.
  it('temperature 0 kutuda GÖRÜNÜYOR', () => {
    expect(tuningToForm({ temperature: 0 }).temperature).toBe('0');
  });

  it('gidiş-dönüş: sunucudan gelen override tele AYNEN geri gidiyor', () => {
    const server = { maxTokens: 8192, temperature: 0, timeoutS: 240 };
    expect(tuningToWire(tuningToForm(server))).toEqual(server);
  });
});

// KAYNAK PİNİ — saf test çeviriyi doğrular ama BAĞLANDIĞINI doğrulamaz.
// Bu kusur sınıfında asıl tehlike zaten formun yanlış davranması, saf
// fonksiyonun değil: doğru fonksiyonu yazıp kutuya `value={4096}` basmak
// testleri yeşil bırakır ve varsayılanı gene dondurur.
describe('AiTab bağlanması (v0.9.1120 Faz 0.5)', () => {
  const src = readFileSync(resolve(__dirname, './AiTab.tsx'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

  it('çeviriyi aiTuning modülünden alıyor', () => {
    expect(src).toContain("from './aiTuning'");
    expect(src).toContain('tuningToForm');
    expect(src).toContain('tuningToWire');
  });

  it('üç kutu da string state ile sürülüyor', () => {
    for (const knob of ['maxTokens', 'temperature', 'timeoutS']) {
      // İLK YAZIMIM ETKİSİZDİ: alternasyonun sol kolu (`useState('')`
      // + herhangi bir şey) dosyadaki BAŞKA bir string state'le
      // eşleşiyordu, yani knob silinse bile yeşil kalırdı. Kapı, adı
      // geçen state'in kendisini çivilemeli.
      expect(src, `${knob} state`).toMatch(
        new RegExp(`const \\[${knob}, set\\w+\\] = useState\\(''\\)`));
      expect(src, `${knob} value bağı`).toContain(`value={${knob}}`);
    }
  });

  it('varsayılanlar PLACEHOLDER, value DEĞİL', () => {
    // Yerleşik varsayılanlar (4096 / 0.2 / 180) yalnız placeholder
    // metninde geçmeli. `value={4096}` ya da useState('4096') = kusur.
    for (const d of ['4096', '0.2', '180']) {
      expect(src, `${d} placeholder'da`).toContain(`placeholder="${d} (default)"`);
      expect(src, `${d} state'e yazılmış`).not.toContain(`useState('${d}')`);
      expect(src, `${d} value'ya yazılmış`).not.toContain(`value={${d}}`);
    }
  });

  it('HER putAISettings çağrısı ayar üçlüsünü taşıyor', () => {
    const calls = src.split('\n').filter(l => l.includes('putAISettings('));
    // Kapsam koruması: çağrı sayısı düşerse test sessizce boş küme
    // üzerinde yeşil kalırdı.
    expect(calls.length, 'putAISettings çağrısı').toBeGreaterThanOrEqual(2);
    expect(calls.filter(l => !l.includes('...tuning()')), 'ayarsız PUT').toEqual([]);
  });
});
