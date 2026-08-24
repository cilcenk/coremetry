import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// statTileGate — v0.9.1375.
//
// Üç detay yüzeyi (endpoint / database / statement) pencere toplamlarını
// aynı kutulu karoyla gösteriyordu ve üçü de KENDİ kopyasını taşıyordu.
// İlk ikisi bayt bayt aynıydı; üçüncüsü kaymıştı — etiketi ne büyük
// harfti ne harf aralıklı, ve `minWidth: 0` yoktu (grid çocuğu içeriğinin
// altına inemez, uzun değer karoyu kolonun dışına iter).
//
// Kaymanın hiçbir şeyi kırmaması bu kapının sebebi: bir sayfa hafifçe
// başka bir ürün gibi görünüyordu ve hiçbir gate bunu göremezdi, çünkü
// aynı fikirde OLMAYACAK paylaşılan bir tanım yoktu.
//
// KAPININ SINIRI, açıkça: burası yalnız o üç dosyayı denetliyor. "Karo
// şeklinde her yerel bileşen" diye genel bir yüklem denendi ve ATILDI —
// imza (`padding: '8px 10px'`, uppercase + letterSpacing) ev genelinde
// mikro-etiket dili olarak ~20 yerde yaşıyor, yani kapı sahte pozitif
// üretirdi. Dar bir kapı, yanlış geniş bir kapıdan iyidir; geniş olanın
// bedeli, ilk kırmızıda muafiyet listesiyle etkisizleştirilmesi olurdu.
const SURFACES = [
  'src/pages/EndpointDetail.tsx',
  'src/pages/databases/detailSections.tsx',
  'src/pages/slowqueries/stmtDetailSections.tsx',
];
const ROOT = resolve(__dirname, '..', '..', '..');

/** Blok + satır şerhlerini düşürür; kapılar KODA bakmalı, prozaya değil. */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');
}

describe('StatTile — detay ailesi tek tanımı kullanıyor (v0.9.1375)', () => {
  it.each(SURFACES)('%s paylaşılan atomu içe aktarıyor', file => {
    const src = readFileSync(resolve(ROOT, file), 'utf8');
    expect(src).toMatch(/import \{[^}]*\bStatTile\b[^}]*\} from '@\/components\/ui'/);
  });

  it.each(SURFACES)('%s yerel bir karo ikizi TANIMLAMIYOR', file => {
    const src = readFileSync(resolve(ROOT, file), 'utf8');
    // Ad değişse de ikiz ikizdir; yakalanan şey TANIM, çağrı değil.
    const localDecl = /\bfunction\s+(Stat|StatTile|HeaderStat|MetricStat)\s*\(/.exec(src);
    expect(localDecl?.[1] ?? null).toBeNull();
  });

  it('atom gerçekten üç yüzeyde de RENDER ediliyor — boş küme tuzağı', () => {
    // Import var ama kullanım yoksa kapı yeşil kalır ve hiçbir şey
    // korumaz; sayım o hâli ayırt ediyor.
    for (const file of SURFACES) {
      const uses = (readFileSync(resolve(ROOT, file), 'utf8').match(/<StatTile\b/g) ?? []).length;
      expect(uses, `${file} StatTile render etmiyor`).toBeGreaterThan(0);
    }
  });

  it('atom minWidth:0 taşıyor — kayan yarının somut kusuru', () => {
    // Bu satır düşerse uzun bir değer karoyu kolonunun dışına iter ve
    // hizalama sessizce bozulur; kaymış kopyada eksik olan tam buydu.
    //
    // ⚠ KOD okunuyor, DOSYA değil. İlk yazımda ham metne bakıyordu ve
    // mutasyon denetimi kapıyı ÖLÜ buldu: `minWidth: 0`ı koddan silmek
    // testi kırmadı, çünkü StatTile'ın kendi şerhi ("it was missing
    // `minWidth: 0`") o dizgeyi zaten içeriyordu. Kapı kendi
    // dokümantasyonu tarafından KARŞILANIYORDU — daha önce gördüğüm
    // "kapı kendi şerhini ısırıyor" tuzağının tersi ve daha sinsisi:
    // ısıran kapı gürültü yapar, karşılanan kapı sessizce yeşil kalır.
    const atom = stripComments(readFileSync(resolve(__dirname, 'StatTile.tsx'), 'utf8'));
    expect(atom).toContain('minWidth: 0');
    expect(atom).toContain("textTransform: 'uppercase'");
  });
});
