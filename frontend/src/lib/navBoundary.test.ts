// navBoundary — j/k sayfa sınırı (v0.9.1018).
//
// NEDEN: v0.9.1017'ye kadar klavye gezinmesi sayfanın sonunda SESSİZCE
// duruyordu. Operatör 50. satırda j'ye basıp basıp hiçbir şey
// olmamasını izliyordu — oysa üç satır aşağıda bir "Next" butonu
// vardı. Klavye yolu, fare yolunun yapabildiğini yapamıyordu; ve bu
// tam da klavyeyle çalışan operatörün en çok kullandığı yerde.
//
// Karar saf bir fonksiyona çıkarıldı çünkü ilginç olan kısım kenar
// vakalar: boş liste, seçimsiz liste, tek satırlık liste, ve "sınır
// bildirdim ama sayfa DÖNMEDİ" hâli.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { navStep, type NavStep } from './useTableNav';

describe('navStep — tablo', () => {
  const CASES: Array<{
    name: string; selected: number; count: number; dir: 'down' | 'up'; want: NavStep;
  }> = [
    // ——— olağan hareket
    { name: 'ortadan aşağı',  selected: 3, count: 10, dir: 'down', want: { kind: 'move', to: 4 } },
    { name: 'ortadan yukarı', selected: 3, count: 10, dir: 'up',   want: { kind: 'move', to: 2 } },

    // ——— sınırlar
    { name: 'son satırda j → sonraki sayfa', selected: 9, count: 10, dir: 'down', want: { kind: 'boundary', dir: 'next' } },
    { name: 'ilk satırda k → önceki sayfa',  selected: 0, count: 10, dir: 'up',   want: { kind: 'boundary', dir: 'prev' } },

    // ——— seçim YOKKEN ilk tuş sayfa çevirmez, ilk satırı seçer.
    // Bu ayrım önemli: -1 "listenin başındayım" değil "hiç
    // girmedim" demek. Sayfa çevirseydi operatör tabloya ilk kez
    // dokunduğu anda başka bir sayfaya ışınlanırdı.
    { name: 'seçimsiz j', selected: -1, count: 10, dir: 'down', want: { kind: 'move', to: 0 } },
    { name: 'seçimsiz k', selected: -1, count: 10, dir: 'up',   want: { kind: 'move', to: 0 } },

    // ——— tek satır: HEM ilk HEM son. Yön kararı verir.
    { name: 'tek satırda j', selected: 0, count: 1, dir: 'down', want: { kind: 'boundary', dir: 'next' } },
    { name: 'tek satırda k', selected: 0, count: 1, dir: 'up',   want: { kind: 'boundary', dir: 'prev' } },

    // ——— boş liste: ne hareket ne sınır. Görünmeyen bir veri
    // kümesinde körlemesine sayfa çevirmek en kötü sonuç.
    { name: 'boş liste j', selected: -1, count: 0, dir: 'down', want: { kind: 'none' } },
    { name: 'boş liste k', selected: -1, count: 0, dir: 'up',   want: { kind: 'none' } },
    { name: 'boş liste, artık seçim', selected: 5, count: 0, dir: 'down', want: { kind: 'none' } },

    // ——— seçim listenin dışında kalmış (kırpılmış veri): aşağı
    // yön sınır sayar, yukarı yön listeye geri çeker.
    { name: 'taşmış seçim j', selected: 99, count: 10, dir: 'down', want: { kind: 'boundary', dir: 'next' } },
    { name: 'taşmış seçim k', selected: 99, count: 10, dir: 'up',   want: { kind: 'move', to: 98 } },
  ];

  for (const c of CASES) {
    it(c.name, () => {
      expect(navStep(c.selected, c.count, c.dir)).toEqual(c.want);
    });
  }
});

describe('sınır kanalı BAĞLI mı', () => {
  const SRC = resolve(__dirname, '..');
  const read = (rel: string) => readFileSync(resolve(SRC, rel), 'utf8');

  // Saf test ≠ BAĞLANMA. `navStep` mükemmel çalışıp hook onu hiç
  // çağırmasa test yeşil kalırdı — bu depoda ölçülmüş bir ders.
  it('hook navStep\'i GERÇEKTEN çağırıyor ve j/k ona bağlı', () => {
    const src = read('lib/useTableNav.ts');
    expect(src).toMatch(/const next = navStep\(/);
    expect(src, "j hâlâ eski inline hesabı yapıyor")
      .toMatch(/keys: 'j',[\s\S]{0,120}handler: \(\) => step\('down'\)/);
    expect(src, "k hâlâ eski inline hesabı yapıyor")
      .toMatch(/keys: 'k',[\s\S]{0,120}handler: \(\) => step\('up'\)/);
  });

  it('sayfa değişimi YENİ veriyi bekliyor', () => {
    // keepPreviousData yüzünden `items` bir süre ESKİ diziyi taşıyor.
    // Referans değişimini beklemezsek odak eski listenin satırına
    // düşer ve yeni sayfa gelince yanlış yerde kalır.
    const src = read('lib/useTableNav.ts');
    expect(src).toMatch(/itemsRef\.current === items/);
    expect(src).toMatch(/setPendingEdge\(null\)/);
  });

  it('/services kanalı kullanıyor ve son sayfada DURUYOR', () => {
    const src = read('pages/Services.tsx');
    expect(src).toMatch(/onPageBoundary:/);
    // false dönüşleri: yalancı hareket yapmaktansa durmak.
    expect(src).toMatch(/if \(!hasMore\) return false;/);
    expect(src).toMatch(/if \(page === 0\) return false;/);
  });

  it('birikimli yüzeyler kanalı KULLANMIYOR — bilinçli', () => {
    // /logs, /metrics ve ServiceSignalTabs satırları BİRİKTİRİYOR.
    // Orada "sonraki sayfa" listenin sonuna EKLEME yapar; odağı ilk
    // satıra atmak operatörü okuduğu yerden koparıp listenin en
    // başına fırlatırdı. Sınır kanalı sayfa TAKASI için var.
    for (const f of ['pages/Logs.tsx', 'pages/Metrics.tsx', 'pages/service/ServiceSignalTabs.tsx']) {
      expect(read(f), `${f} birikimli — onPageBoundary anlamsız`)
        .not.toMatch(/onPageBoundary/);
    }
  });
});
