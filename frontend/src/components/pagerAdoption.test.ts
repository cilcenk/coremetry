// Pager benimseme kapısı — sayfalama sözleşmesi (v0.9.1017).
//
// NE ÇİVİLİYOR: sayfalamanın bir TÜR olduğunu.
//
// Ölçülen taban (v0.9.1013): `Pager` atomu vardı ama TEK tüketicisi
// /traces'ti. Diğer beş yüzey elle çizilmişti ve her biri kendi
// cevabını vermişti — konum (tablo altı mı üstü mü), vurgu (Next
// birincil mi), sayının anlamı (kesin mi tavanlı mı), commit anı
// (Enter mı blur mu). Sebep bilgi eksikliği değildi: sözleşme diye bir
// şey yoktu, dolayısıyla her karar çağırana kalmıştı.
//
// KAPI DAR TUTULDU — denetimin kendi ölçümüne uyarak. Depo genelinde
// "elle Prev/Next YASAK" diyen bir kural 30+ yanlış pozitif üretiyor:
// takvim ayı gezinmesi, trace şelalesinde span atlama, çekmece
// içi kayıt gezinmesi ve grafik pencere kaydırması hep "prev/next"
// sözcüklerini taşıyor ama HİÇBİRİ sayfalama değil. Liste bu yüzden
// DONMUŞ ve yalnız BÜYÜR: bir yüzey atomdan geri çıkarsa kapı haber
// verir, ama yeni bir "next" sözcüğü gürültü üretmez.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { stripTsComments } from '../styles/zLayers.test';

const SRC = resolve(__dirname, '..');
const read = (rel: string) => stripTsComments(readFileSync(join(SRC, rel), 'utf8'));

// v0.9.1014-1016'da sözleşmeye geçen ALTI yüzey.
const OFFSET = [
  'pages/Traces.tsx',
  'pages/Services.tsx',
  'features/anomalies/AnomaliesPage.tsx',
];
const CURSOR = [
  'pages/Logs.tsx',
  'pages/Metrics.tsx',
  'pages/service/ServiceSignalTabs.tsx',
];
const ADOPTED = [...OFFSET, ...CURSOR];

// Denetimin listesinden BİLİNÇLİ olarak çıkarılanlar. Gerekçe kayıtlı,
// çünkü "8'den 6'ya düştü" diye okunan bir liste gerileme gibi görünür
// — oysa ikisi de ürün kararı.
const DELIBERATELY_OUT: Record<string, string> = {
  // Bunlar SAYFALAMA değil: gösterilecek satır sayısını seçen bir
  // limit kontrolü. Sayfa kavramı yok, imleç yok, "sonraki" yok.
  // Pager'a indirmek olmayan bir gezinmeyi ima ederdi.
  'pages/Endpoints.tsx': 'limit seçici — sayfalama değil',
  'pages/Explore.tsx': 'limit seçici — sayfalama değil',
};

describe('Pager benimseme', () => {
  it('altı yüzeyin HEPSİ atomu kullanıyor', () => {
    for (const f of ADOPTED) {
      const src = read(f);
      expect(src, `${f} Pager'ı import etmiyor`).toMatch(/from '@\/components\/Pager'/);
      expect(src, `${f} <Pager> basmıyor`).toMatch(/<Pager\b/);
    }
  });

  it('her çağrı kipini ve sayısının anlamını BEYAN ediyor', () => {
    // `tsc` bunu zaten zorluyor (zorunlu prop). Kapı yine de duruyor:
    // bir gün prop opsiyonele dönerse tip hatası kaybolur ama bu iddia
    // kalır ve kararın bilinçli mi kaza mı olduğunu sorar.
    for (const f of ADOPTED) {
      const src = read(f);
      for (const call of src.match(/<Pager\b[\s\S]{0,400}?\/>/g) ?? []) {
        expect(call, `${f}: <Pager> mode belirtmiyor`).toMatch(/\bmode=/);
        expect(call, `${f}: <Pager> count belirtmiyor`).toMatch(/\bcount=/);
      }
    }
  });

  it('offset yüzeyleri offset, cursor yüzeyleri cursor', () => {
    for (const f of OFFSET) {
      expect(read(f), `${f} offset olmalı`).toMatch(/mode="offset"/);
    }
    for (const f of CURSOR) {
      expect(read(f), `${f} cursor olmalı`).toMatch(/mode="cursor"/);
    }
  });

  // ——— Elle şerit geri gelmesin ——————————————————————————————
  //
  // İKİ YAZILIŞ da kapsanıyor. Kapı yalnız `<Button>` atomunu arasaydı,
  // ham `<button>` ile yazılmış bir şerit sessizce geçerdi — ve atom
  // zorunlu hâle gelmeden önce depodaki şeritler TAM OLARAK ham
  // `<button>`dı (Pager'ın kendisi v0.9.1014'e kadar öyleydi).
  it('benimseyen dosyalarda elle Prev/Next BUTONU yok', () => {
    const HANDROLLED = /<[Bb]utton\b[^>]*>\s*(?:\{[^}]*\}\s*)?(?:←\s*Prev|Prev\s*←|Next\s*→|→\s*Next|⏮\s*First|Last\s*⏭)/;
    for (const f of ADOPTED) {
      const src = read(f);
      expect(HANDROLLED.test(src), `${f} elle sayfalama butonu geri gelmiş`).toBe(false);
    }
  });

  // ——— URL = tek kaynak ————————————————————————————————————
  //
  // Offset yüzeylerinde sayfa PAYLAŞILABİLİR olmalı. /traces bunu
  // v0.9.1017'ye kadar YARIM yapıyordu: `?page=` OKUNUYOR ama
  // yazılıyordu — hayır, tam tersi de olabilirdi. Tek yönlü okuma bu
  // depoda tekrar eden bir hata sınıfı (v0.8.256/265/267), o yüzden
  // İKİ yarı da ayrı ayrı iddia ediliyor.
  it('offset yüzeylerinde sayfa URL\'de — HEM okunuyor HEM yazılıyor', () => {
    for (const f of OFFSET) {
      const src = read(f);
      expect(src, `${f}: ?page= OKUNMUYOR`).toMatch(/get\('page'\)/);
      expect(src, `${f}: ?page= YAZILMIYOR (tek yönlü okuma)`)
        .toMatch(/set\('page'|\['page',/);
    }
  });

  it('cursor yüzeyleri imleci URL\'e YAZMIYOR — bilinçli', () => {
    // Birikimli bir listede imleç paylaşılabilir DEĞİL: `?cursor=X`
    // alıcıya X'ten BAŞLAYAN bir liste açar, oysa paylaşan X'e kadar
    // birikmiş her satırı görüyordu. Yani link, gönderenin gördüğü
    // ekranı yeniden üretmez — sessizce farklı bir görünüm verir.
    // Doğru paylaşım birimi bu yüzeylerde pencere + filtreler.
    for (const f of CURSOR) {
      expect(read(f), `${f}: imleç URL'e yazılmış — birikimli görünüm yeniden üretilemez`)
        .not.toMatch(/set\('cursor'|\['cursor',/);
    }
  });

  it('kapsam dışı bırakılanların GEREKÇESİ kayıtlı', () => {
    for (const [f, reason] of Object.entries(DELIBERATELY_OUT)) {
      expect(reason.length, `${f} gerekçesiz`).toBeGreaterThan(10);
      expect(() => read(f), `${f} artık yok — listeyi güncelle`).not.toThrow();
    }
  });
});
