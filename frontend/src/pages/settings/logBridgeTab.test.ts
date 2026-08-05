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
