import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.657 — v0.9.655 backend'i yazıldı ama şablonu girmenin tek yolu
// `curl` ile admin ucuna vurmaktı. Özellik yazıldı, KULLANILAMIYORDU.
//
// Bu testler ekranın backend'le AYRIŞMAMASINI çiviliyor: ortam listesi
// ve yer tutucu iki yerde tanımlı ve zamanla ayrışabilirler — bu kod
// tabanının tekrar eden hata sınıfı.

const tab = readFileSync(resolve(__dirname, './LogBridgeTab.tsx'), 'utf8');
const go = readFileSync(
  resolve(__dirname, '../../../../internal/api/correlation_link.go'), 'utf8');

describe('ekran ↔ backend eşleşmesi', () => {
  // Backend: var correlationEnvSuffixes = []string{"int", "uat", "prep"}
  const goEnvs = (go.match(/correlationEnvSuffixes\s*=\s*\[\]string\{([^}]*)\}/)?.[1] ?? '')
    .split(',').map(s => s.trim().replace(/"/g, '')).filter(Boolean);

  it('backend ortam listesi okunabiliyor', () => {
    expect(goEnvs.length).toBeGreaterThan(0);
  });

  it('her backend ortamı ekranda alan taşıyor', () => {
    for (const e of goEnvs) {
      expect(tab, `${e} ortamı için alan yok`).toContain(`key: '${e}'`);
    }
  });

  // Soneksiz servisler prod demek ve backend onu "default" anahtarında
  // arıyor; ekran başka bir ad kullanırsa prod şablonu HİÇ okunmaz.
  it('prod anahtarı backend ile aynı: default', () => {
    expect(go).toContain('tpls["default"]');
    expect(tab).toContain("key: 'default'");
  });

  // Yer tutucu backend'den GELİYOR (GET yanıtında), ekrana sabit
  // yazılmıyor — ikisi ayrışırsa operatör çalışmayan bir şablon girer.
  it('yer tutucu backend yanıtından okunuyor', () => {
    expect(tab).toContain('s.placeholder');
    expect(go).toContain('"placeholder": correlationLinkPlaceholder'.replace(/"/g, '"'));
  });
});

// v0.9.1142 — yapılandırılmış request kimliği → trace dilimi. Şablona
// OPSİYONEL zaman yer tutucuları geldi ve kimliğin saat dilimi ayarlanır
// oldu. İkisi de "iki yerde tanımlanıp ayrışma" sınıfına aday.
describe('zaman yer tutucuları + kimlik saat dilimi', () => {
  const apiTs = readFileSync(resolve(__dirname, '../../lib/api.ts'), 'utf8');
  const reqidGo = readFileSync(
    resolve(__dirname, '../../../../internal/reqid/reqid.go'), 'utf8');

  it('yer tutucu listesi backend yanıtından okunuyor', () => {
    expect(tab).toContain('timePlaceholders');
    expect(go).toContain('corrTimePlaceholders');
    expect(go).toContain('"timePlaceholders": corrTimePlaceholders');
  });

  it('dört yer tutucu Go tarafında tanımlı', () => {
    for (const p of ['{from}', '{to}', '{from_ms}', '{to_ms}']) {
      expect(go, `${p} backend'de yok`).toContain(`"${p}"`);
    }
  });

  // Varsayılan saat dilimi TEK kaynakta (reqid.DefaultTZ) ve ekrana
  // sunucudan geliyor. Ekrana sabit yazılırsa operatör tz'yi değiştirdiğinde
  // ipucu metni yalan söyler.
  it('varsayılan saat dilimi ekrana sabit YAZILMIYOR', () => {
    expect(reqidGo).toContain('DefaultTZ = "Europe/Istanbul"');
    expect(tab).not.toContain('Europe/Istanbul');
    expect(tab).toContain('reqidTzDefault');
  });

  it('saat dilimi alanı kaydediliyor', () => {
    expect(tab).toContain('api.putCorrelationLink(tpls, tz)');
    expect(tab).toContain('setTz(');
  });

  // İşaretçi semantiği: tz göndermeyen çağıran (curl / eski ekran) saklı
  // değeri SİLMEMELİ.
  it('tz gönderilmezse gövdeye yazılmıyor, backend işaretçi bekliyor', () => {
    expect(apiTs).toContain('reqidTz === undefined');
    expect(go).toContain('ReqidTz *string');
  });
});

describe('ekran davranışı', () => {
  it('boş ortam kapalı sayılıyor ve bu söyleniyor', () => {
    expect(tab).toContain('Boş bırakılan ortam kapalıdır');
  });

  // Backend hangi ortamın hatalı olduğunu gövdede söylüyor; "geçersiz
  // şablon" tek başına operatöre hangi alanı düzelteceğini söylemez.
  it('backend hata mesajı olduğu gibi gösteriliyor', () => {
    expect(tab).toContain('err.message');
  });
});
