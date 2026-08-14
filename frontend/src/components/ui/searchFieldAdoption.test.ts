// SearchField benimseme kapısı — K1/K2 (v0.9.1012, M8 / KN-5).
//
// NE ÇİVİLİYOR: arama kutusunun bir TÜR olduğunu.
//
// Ölçülen taban (denetim, 39 arama-niyetli kutu): büyüteç 2'sinde,
// kısayol ipucu 0'ında, `type="search"` 1'inde. Sebep bilgi eksikliği
// değildi — `SearchField` diye bir tür yoktu, dolayısıyla her özellik
// çağırana kalmıştı ve hepsi dağılmıştı.
//
// KAPI DAR TUTULDU (denetimin kendi önerisi): dönüştürülen yüzeyler
// DONMUŞ bir listede. Depo genelinde "her arama-niyetli input
// SearchField olmalı" demek 31 girdilik bir izin listesi doğururdu ve
// gürültüye dönerdi. Liste yalnız BÜYÜR: bir yüzey atomdan geri
// çıkarsa kapı haber verir.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { stripTsComments } from '../../styles/zLayers.test';

const SRC = resolve(__dirname, '..', '..');
const read = (rel: string) => stripTsComments(readFileSync(join(SRC, rel), 'utf8'));

// v0.9.1012'de dönüştürülen yüzeyler.
const ADOPTED = [
  'pages/Metrics.tsx',
  'pages/Inbox.tsx',
  'pages/Dashboards.tsx',
  'pages/Endpoints.tsx',
  'features/anomalies/AnomaliesPage.tsx',
];

// Denetimin 8 sayfalık listesinden BİLİNÇLİ olarak çıkarılan ikisi.
// Gerekçeleri kayıtlı, çünkü "8'den 5'e düştü" diye okunan bir liste
// bir gerileme gibi görünür — oysa ikisi de ürün kararı.
const DELIBERATELY_OUT: Record<string, string> = {
  // /services'in arama kutusu düz bir input DEĞİL, sunucu-taraflı
  // `ServicePicker`. Ona ikon eklemek atomu değil PICKER'ı değiştirir
  // ve picker 10+ sayfada; kabuk görünümü tek dalgada 10 yüzeyde
  // birden değişirdi. Picker açılırları zaten G3'ün (mockup-first)
  // konusu.
  'pages/Services.tsx': 'kutu ServicePicker — picker değişikliği G3 hattında',
  // /traces'in `.trace-lookup`ı bir ARAMA değil bir ATLAYIŞ: diğer
  // her alanı geçersiz kılıp tek bir trace'e gider ve v0.9.304'te
  // operatör kararıyla görsel olarak AYRIŞTIRILDI. Atoma indirmek o
  // kararı geri alırdı.
  'pages/Traces.tsx': 'trace-lookup bilinçli olarak ayrışık (v0.9.304)',
};

describe('M8 — SearchField atomu', () => {
  const atom = read('components/ui/SearchField.tsx');
  const css = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');

  it('type="search" atomda SABİT — çağıran düşüremez', () => {
    // L2: depoda `type="search"` TEK bir inputta vardı. Mobil klavyede
    // "Ara" tuşu + native temizleme; ikisi de bedava ve ikisi de
    // çağıranın hatırlamasına bırakılamaz.
    expect(atom).toMatch(/type="search"/);
    expect(atom).toMatch(/Omit<[^>]*InputHTMLAttributes<HTMLInputElement>,\s*'type'/);
  });

  it('bastığı her sınıfın CSS karşılığı var', () => {
    // primitiveClasses (v0.9.884) dersi: atomun çalışma anında bastığı
    // sınıf statik taramanın kör noktası. CSS aynı commit'te gelmezse
    // ikon kutunun üstüne biner ve dolgu hiç uygulanmaz.
    for (const cls of ['.sf-wrap', '.sf-icon', '.sf-hint', '.sf-clear', '.sf-input']) {
      expect(css.includes(cls), `${cls} tanımsız`).toBe(true);
    }
  });

  it('ikon TIKLAMA YUTMUYOR', () => {
    // Bir büyütece tıklayan operatör odak bekler, hiçbir şey
    // olmamasını değil.
    expect(css).toMatch(/\.sf-icon\s*\{[^}]*pointer-events:\s*none/);
  });

  it('ipucu rozeti ODAKTA kayboluyor', () => {
    // İpucu kutuyu KULLANIRKEN değil BULURKEN gerekli; odaktayken
    // metnin üstünde durması yeni bir kusur olurdu.
    expect(css).toMatch(/\.sf-wrap:focus-within \.sf-hint\s*\{\s*opacity:\s*0/);
  });

  it('native ✕ bastırılıyor — iki temizle düğmesi yan yana gelmez', () => {
    expect(css).toMatch(/\.sf-input::-webkit-search-cancel-button/);
  });
});

describe('M8 — dönüştürülen yüzeyler', () => {
  for (const rel of ADOPTED) {
    it(`${rel} SearchField kullanıyor`, () => {
      expect(read(rel)).toMatch(/<SearchField[\s/>]/);
    });
  }

  it('bilinçli kapsam dışı ikisi hâlâ kendi gerekçesine uyuyor', () => {
    // Gerekçe bayatlarsa muafiyet de bayatlar.
    expect(read('pages/Services.tsx')).toMatch(/<ServicePicker[\s\S]{0,300}?Filter services/);
    expect(read('pages/Traces.tsx')).toMatch(/className="trace-lookup"/);
    expect(Object.keys(DELIBERATELY_OUT)).toHaveLength(2);
  });

  it('kısayol ipucu artık ekranda — 0/39 hâli bitti', () => {
    // K2'nin özü: bir operatör `/` kısayolunun VARLIĞINI hiçbir
    // ekranda göremiyordu.
    const withHint = [...ADOPTED, 'pages/Logs.tsx']
      .filter(rel => /hint="\/"|className="sf-hint"/.test(read(rel)));
    expect(withHint.length).toBeGreaterThanOrEqual(4);
  });

  it('L1 — ölü Search importu Sidebar’dan kalktı', () => {
    // Denetimin "en açık edici kanıtı": arama affordance'ı planlanmış,
    // hiç basılmamış. Ölü sembol varmış gibi görünmemeli.
    const sb = read('components/Sidebar.tsx');
    expect(sb).not.toMatch(/(?<![\w])Search,/);
  });

  it('L4/L5 — eylemsiz ve boş placeholder’lar kalmadı', () => {
    expect(read('pages/Slos.tsx')).not.toMatch(/placeholder="…"/);
    const traces = read('pages/Traces.tsx');
    expect(traces).not.toMatch(/placeholder="Service…"/);
    expect(traces).not.toMatch(/placeholder="Operation…"/);
    expect(read('pages/Endpoints.tsx')).not.toMatch(/placeholder="All services…"/);
  });

  it('L3 — palette placeholder’ı dar modalda kesilmiyor', () => {
    const m = /placeholder="([^"]*)"/.exec(read('components/CommandPalette.tsx'));
    expect(m).toBeTruthy();
    expect(m![1].length).toBeLessThanOrEqual(40);
  });
});
