import { describe, it, expect } from 'vitest';
import { nodeLinkLabel } from './nodeLinkLabel';

// v0.9.1337 — v0.9.1326'nın mutasyon bulgusu: `/databases` dalını ters
// çevirmek 301 dosya / 4500+ testin HEPSİNİ yeşil bırakıyordu. Bu blok o
// boşluğu kapatıyor.
//
// Çivilenen şey estetik değil DÜRÜSTLÜK: bir link kataloğa gidiyorsa
// etiketi "instance aç" DEMEMELİ. Operatör "Open instance →" okuyup
// kataloğa düşerse, aradığı satırın orada olmadığını "yok" diye okur —
// ilk-200 tavanı var (v0.9.972).

describe('nodeLinkLabel — database', () => {
  it('instance ÇÖZÜLDÜ → detay sayfası → "Open instance"', () => {
    expect(nodeLinkLabel('database', '/database?system=postgresql&instance=pg-01&db=orders'))
      .toBe('Open instance →');
  });

  it('instance ÇÖZÜLEMEDİ → katalog → "kataloğunda göster"', () => {
    // v0.9.1318 öncesi yazılmış düz `db:<system>` düğümü: instance yok,
    // hedef motora daraltılmış katalog (v0.9.1326).
    expect(nodeLinkLabel('database', '/databases?dbsys=postgresql'))
      .toBe('Veritabanı kataloğunda göster →');
  });

  it('etiket HEDEFE bakar, düğümün adına değil', () => {
    // Ayırt edici: aynı kind, aynı sistem, FARKLI hedef → farklı etiket.
    // Karar kaynağı nodeDetailHref'in çıktısı; düğümün alanları değil.
    const a = nodeLinkLabel('database', '/databases?dbsys=oracle');
    const b = nodeLinkLabel('database', '/database?system=oracle&instance=x&db=COREBANK');
    expect(a).not.toBe(b);
  });
});

describe('nodeLinkLabel — queue', () => {
  it('üçlü TAM (destination var) → çekmece → "Topiği aç"', () => {
    expect(nodeLinkLabel('queue', '/messaging?msys=kafka&q=orders&destination=orders.v1'))
      .toBe('Topiği aç →');
  });

  it('destination YOK → katalog → "kataloğunda göster"', () => {
    // v0.9.1026 kararı: cluster kolonu inmemiş kurulum / eski kova.
    expect(nodeLinkLabel('queue', '/messaging?msys=kafka&q=orders'))
      .toBe('Messaging kataloğunda göster →');
  });
});

describe('iki kind BİRBİRİNİN yüklemini kullanmıyor', () => {
  // Negatif kontrol. Bir refactor db yüklemini queue'ya (veya tersine)
  // uygularsa buradaki iki vaka yakalar: queue hedefi `/databases` ile
  // BAŞLAMAZ, db hedefi `destination=` İÇERMEZ — yani karışan yüklem
  // sessizce "hep katalog" ya da "hep detay" derdi.
  it('queue hedefi db yüklemine takılmıyor', () => {
    expect(nodeLinkLabel('queue', '/messaging?msys=kafka&q=orders&destination=t'))
      .toBe('Topiği aç →');
  });

  it('db hedefi queue yüklemine takılmıyor', () => {
    expect(nodeLinkLabel('database', '/database?system=pg&instance=i&db=d'))
      .toBe('Open instance →');
  });
});
