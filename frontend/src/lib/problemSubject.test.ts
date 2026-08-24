import { describe, it, expect } from 'vitest';
import {
  parseDbSubject, subjectKind, subjectLabel, subjectIsLinkable, subjectTitle,
  derivedTeamTitle,
} from './problemSubject';

// problemSubject.test.ts — v0.9.1339.
//
// ORİJİNAL SEMPTOM: db_capacity.go'nun yazdığı `corebank-scan.prod` on
// küsur yüzeyde `<Link to={serviceHref(p.service)}>` olarak basılıyordu.
// Link geçerli görünüyor, tıklanıyor, servis sayfası BOŞ açılıyor.
// Sınıflandırma yanlışsa gate ısırmaz — bu yüzden testlerin ağırlığı
// NEGATİF tarafta: bir servis adı ASLA db öznesi sayılmamalı.

describe('parseDbSubject', () => {
  it('db:<system>@<instance> çözer', () => {
    expect(parseDbSubject('db:oracle@corebank-scan.prod'))
      .toEqual({ system: 'oracle', instance: 'corebank-scan.prod' });
  });

  it("instance'taki '@' gidiş-dönüşü bozmaz (ayırıcı İLK '@')", () => {
    expect(parseDbSubject('db:mysql@db@shard-3'))
      .toEqual({ system: 'mysql', instance: 'db@shard-3' });
  });

  // NEGATİF KONTROL — parse'ı "'@' varsa böl" diye yazmak cazip; o hâlde
  // bu satırların HEPSİ sessizce veritabanı olurdu.
  it.each([
    ['', 'boş'],
    ['checkout', 'düz servis adı'],
    ['checkout@v2', "'@' taşıyan servis adı"],
    ['queue:kafka:api.usage', 'kuyruk düğümü'],
    ['ext:stripe', 'dış düğüm'],
    ['database-router', "'db' ile başlıyor ama önek DEĞİL"],
    ['db:oracle', "'@' yok"],
    ['db:@corebank-scan.prod', 'system yarısı boş'],
    ['db:oracle@', 'instance yarısı boş'],
    ['corebank-scan.prod', "v0.9.1338 öncesi ham değer"],
    ['DB:oracle@x', 'önek büyük harf'],
  ])('%s (%s) db öznesi DEĞİL', (input) => {
    expect(parseDbSubject(input)).toBeNull();
  });
});

describe('subjectKind', () => {
  it("kind alanı yokken biçimden karar verir (eski sekmenin JSON'u)", () => {
    expect(subjectKind('db:oracle@x')).toBe('db');
    expect(subjectKind('checkout')).toBe('service');
  });

  it('kind alanı varsa onu kullanır', () => {
    expect(subjectKind('db:oracle@x', 'db')).toBe('db');
    expect(subjectKind('checkout', 'service')).toBe('service');
  });

  // İKİ-BOOT SÖZLEŞMESİ: kolonu ekleyen boot probe'u false okur, kind
  // boş gelir. Ürün o gövdede bugünkü davranışın birebir aynısını
  // göstermeli — yani düz bir servis adı SERVİS kalmalı.
  it('boş kind + servis adı = service', () => {
    expect(subjectKind('checkout', '')).toBe('service');
    expect(subjectKind('checkout', undefined)).toBe('service');
  });

  // Backend yarın 'queue' eklerse bu dosya onu SERVİS diye basmamalı:
  // karar biçimden veriliyor, "tanımadım → service" değil.
  it('tanınmayan kind, servis-şekilli olmayan özneyi servise ÇEVİRMEZ', () => {
    expect(subjectKind('db:oracle@x', 'queue')).toBe('db');
  });

  // AD ÇAKIŞMASI NEGATİF KONTROLÜ — InboxItem.kind ZATEN VAR ve
  // 'problem'|'exception'|'anomaly' taşıyor. Biri yanlışlıkla onu bu
  // fonksiyona verirse (nesne alan bir imzada bu SESSİZCE olurdu),
  // sonuç yine de doğru olmalı: o değerler 'db' DEĞİL.
  it.each(['problem', 'exception', 'anomaly'])(
    "InboxItem.kind='%s' özneyi db'ye çevirmez", (inboxKind) => {
      expect(subjectKind('checkout', inboxKind)).toBe('service');
      // Ve db-şekilli bir özne, yanlış alan verilse bile db kalır.
      expect(subjectKind('db:oracle@corebank-scan.prod', inboxKind)).toBe('db');
    });
});

describe('subjectLabel', () => {
  it('db öznesini okunabilir basar, ham makine kimliğini DEĞİL', () => {
    expect(subjectLabel('db:oracle@corebank-scan.prod'))
      .toBe('oracle · corebank-scan.prod');
  });
  it('servis adına dokunmaz', () => {
    expect(subjectLabel('checkout')).toBe('checkout');
  });
});

describe('subjectIsLinkable', () => {
  // ÖLÇÜLDÜ 2026-08-24: receiver instance'ı (corebank-scan.prod) ile
  // db_summary_5m.instance ('oracle') AYRI kimlik uzayları, kesişim 0
  // satır. /databases linki de boş bir satıra giderdi.
  it('db öznesi linklenmez', () => {
    expect(subjectIsLinkable('db:oracle@corebank-scan.prod', 'db')).toBe(false);
  });
  it('boş özne linklenmez (global log-query kuralları)', () => {
    expect(subjectIsLinkable('', 'service')).toBe(false);
  });
  it('servis öznesi LİNKLENİR — regresyon kapısı', () => {
    // Bu satır olmadan "hiçbir şeyi linkleme" mutasyonu testleri geçerdi
    // ve ürün her problem satırının servis linkini kaybederdi.
    expect(subjectIsLinkable('checkout', 'service')).toBe(true);
    expect(subjectIsLinkable('checkout')).toBe(true);
  });
});

describe('subjectTitle', () => {
  it('linksiz db öznesi NEDEN tıklanamadığını söyler', () => {
    const t = subjectTitle('db:oracle@corebank-scan.prod');
    expect(t).toContain('oracle');
    expect(t).toContain('corebank-scan.prod');
    expect(t).toContain('servis değil');
  });
  it('servis öznesinde title YOK (gereksiz tooltip gürültüsü)', () => {
    expect(subjectTitle('checkout')).toBeUndefined();
  });
});

// derivedTeamTitle — v0.9.1345. TÜRETİLMİŞ sahipliğin çekincesi.
//
// Operatör kuralı: bir db öznesinin sahibi, onu en çok çağıran servisin
// takımıdır. Ama çözüm db SİSTEMİ düzeyinde yapılıyor (iki kimlik uzayı
// kesişmiyor — backend identity.go), yani bir YAKLAŞIKLIK.
//
// Bu testlerin ağırlığı ÇEKİNCENİN KENDİSİNDE: metin kanıtı (hangi
// servis) VE sınırı (sistem düzeyi) birlikte söylemezse kesin bir atıf
// gibi okunur, ve o hâlde ürün kendinden emin bir yanlış cevap verir.
describe('derivedTeamTitle', () => {
  const t = derivedTeamTitle('account-service', 'db:oracle@corebank-scan.prod');

  it('KANITI söyler — takım hangi servis üzerinden türetildi', () => {
    expect(t).toContain('account-service');
  });
  it('kesin atıf OLMADIĞINI açıkça söyler', () => {
    expect(t).toContain('Türetilmiş');
    expect(t).toContain('kesin atıf değil');
  });
  it('SINIRI söyler — çözüm sistem düzeyinde, tekil örnek düzeyinde değil', () => {
    // Bu cümle düşerse iki Oracle kümesi olan bir filoda operatör,
    // ikisinin de aynı takıma yazıldığını HİÇBİR yerde göremez.
    expect(t).toContain('SİSTEMİ düzeyinde');
    expect(t).toContain('birden çok küme');
  });
  it('db sistemini adlandırır', () => {
    expect(t).toContain('oracle');
  });
  it('özne verilmezse de çekince tam kalır (yalnız ad genelleşir)', () => {
    const bare = derivedTeamTitle('account-service');
    expect(bare).toContain('account-service');
    expect(bare).toContain('kesin atıf değil');
    expect(bare).toContain('SİSTEMİ düzeyinde');
  });
});
