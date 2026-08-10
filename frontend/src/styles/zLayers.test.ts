// zLayers — Dalga 4 / MK2 kapısı (v0.9.910)
//
// Ne çiviliyor: `--z-*` merdiveninin TANIMLI, ARTAN ve anlamlı
// ilişkileri koruyor olduğu + `globals.css`te çıplak sayı kalmadığı.
//
// Neden bu kapı ŞART: z-index regresyonlarını dört kapının hiçbiri
// göremez. tsc bir sayıya bakmaz, eslint satır-içi stile bakmaz, jsdom
// yığın sırasını HESAPLAMAZ (`getComputedStyle` z-index'i döndürür ama
// "hangisi üstte" sorusunu cevaplamaz — bunun için gerçek bir compositor
// gerekir), `make audit` CSS'e bakmaz. Ekranda bir panel diğerinin
// altında kaybolur ve hiçbir test kırmızıya dönmez.
//
// Bu yüzden kapı DEĞERLERİ değil, İLİŞKİLERİ sınıyor: "dropdown
// popover'ın altında" gibi ifadeler, kaç sayı olduklarından bağımsız
// olarak doğru kalmak zorunda. Bir rung'un sayısı değişebilir; sırası
// değişirse bu bir davranış kararıdır ve testi güncellemek gerekir —
// tam da istenen sürtünme.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');
const SRC = resolve(__dirname, '..');

// `-` regex'te kelime sınırı DEĞİL: `\b--z-drawer\b` `--z-drawer-panel`in
// içinde de eşleşir ve YANLIŞ değeri döndürür (v0.9.894 dersi).
function z(name: string): number {
  const m = new RegExp(`--z-${name}(?![\\w-])\\s*:\\s*(\\d+)`).exec(CSS);
  if (!m) throw new Error(`--z-${name} tanımlı değil`);
  return Number(m[1]);
}

const LADDER = [
  'sticky-cell', 'sticky-head', 'sticky-foot', 'sticky-bar',
  'handle', 'tooltip', 'app-splash', 'nav', 'dropdown', 'popover',
  'drawer', 'drawer-panel', 'fab', 'modal', 'modal-nested', 'toast', 'debug',
];

describe('MK2 — z merdiveni', () => {
  it('17 rung tanımlı ve KESİN artan', () => {
    const vals = LADDER.map(z);
    expect(vals).toEqual([...vals].sort((a, b) => a - b));
    expect(new Set(vals).size, 'iki rung aynı değerde — beraberlik DOM sırasına kalır').toBe(vals.length);
  });

  it('dört yapışkan kademe ayrı ve doğru sırada', () => {
    // Hepsi tek --z-sticky'ye çökerse yapışkan başlık yapışkan filtre
    // barının ÜSTÜNE çizilir (v0.9.697'nin z-index sürümü).
    expect(z('sticky-cell')).toBeLessThan(z('sticky-head'));
    expect(z('sticky-head')).toBeLessThan(z('sticky-foot'));
    expect(z('sticky-foot')).toBeLessThan(z('sticky-bar'));
    expect(z('sticky-bar')).toBeLessThan(z('handle'));
  });

  it('üst katman zinciri: dropdown < popover < drawer < fab < modal < toast < debug', () => {
    const chain = ['dropdown', 'popover', 'drawer', 'fab', 'modal', 'toast', 'debug'].map(z);
    expect(chain).toEqual([...chain].sort((a, b) => a - b));
  });

  it('drawer paneli perdesinin TAM üstünde (+1)', () => {
    // Perde ile panel arasına başka bir şeyin girmesi mümkün olmamalı.
    expect(z('drawer-panel')).toBe(z('drawer') + 1);
  });

  it('iç içe modal dıştakinin üstünde', () => {
    // ZoomChannelPicker, ChannelModal'ın İÇİNDE açılıyor.
    expect(z('modal-nested')).toBeGreaterThan(z('modal'));
  });

  it('tooltip nav\'ın ALTINDA, yapışkanların üstünde', () => {
    // Tooltip bir sayfa içeriği süslemesi; sidebar/topbar onu örtmeli.
    expect(z('tooltip')).toBeGreaterThan(z('sticky-bar'));
    expect(z('tooltip')).toBeLessThan(z('nav'));
  });

  it('globals.css\'te çıplak z-index sayısı kalmadı (mikro bant hariç)', () => {
    // 0/1 gibi bileşen-İÇİ mikro katmanlar kapsam dışı: onlar bir
    // uygulama katmanı değil, tek bir kutunun kendi içindeki sıra.
    const stripped = CSS.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
    const bad: string[] = [];
    stripped.split('\n').forEach((l, i) => {
      const m = /(?<!-)z-index:\s*(\d+)/.exec(l);
      if (m && Number(m[1]) > 6) bad.push(`globals.css:${i + 1} ${l.trim().slice(0, 90)}`);
    });
    expect(bad).toEqual([]);
  });
});

// ── TSX süpürmesi (v0.9.911) ───────────────────────────────────────────
describe('MK2 — TSX\'te çıplak zIndex kalmadı', () => {
  // Mikro bant (≤10) KAPSAM DIŞI, bilinçli: bir grafiğin kendi içindeki
  // ipucu/eksen/rozet sırası bir uygulama katmanı değil. Onları rung'a
  // çekmek 15 rung'luk skalayı çizim detaylarıyla kirletirdi.
  const MICRO_MAX = 10;

  function walk(dir: string, out: string[] = []): string[] {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.name === 'node_modules') continue;
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p, out);
      else if (e.name.endsWith('.tsx')) out.push(p);
    }
    return out;
  }

  it('uygulama katmanı irtifaları token üzerinden', () => {
    const bad: string[] = [];
    for (const file of walk(SRC)) {
      const src = readFileSync(file, 'utf8').replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
      src.split('\n').forEach((line, i) => {
        if (/^\s*\/\//.test(line)) return;
        const m = /zIndex:\s*(\d+)/.exec(line);
        if (m && Number(m[1]) > MICRO_MAX) {
          bad.push(`${file.slice(SRC.length + 1)}:${i + 1} zIndex: ${m[1]}`);
        }
      });
    }
    expect(bad).toEqual([]);
  });
});

// ── Katman-kombinasyon matrisi ─────────────────────────────────────────
// Operatörün istediği elle smoke matrisi Playwright YASAK olduğu için
// tarayıcıda koşturulamıyor; jsdom da yığın sırasını HESAPLAMIYOR
// (getComputedStyle z-index'i döndürür, "hangisi üstte"yi değil).
//
// Kapatılabilen kısım bu: her yüzeyin KAYNAKTA beyan ettiği rung okunup
// ikili sıralar sınanıyor. Bu, "iki panel gerçekten üst üste geldiğinde
// ne oluyor" sorusunu cevaplamaz — ama bu depoda z regresyonlarının
// TAMAMI yanlış rung seçiminden doğdu, çakışan konumlandırmadan değil.
// KAPATILAMAYAN kısım dürüstçe kayda geçsin: yığınlama bağlamı (bir ata
// elemanda transform/filter/contain) bir çocuğun z'sini hapseder ve
// hiçbir kaynak taraması bunu göremez. Ölçüldü — bugün depoda konuyla
// ilgili böyle bir ata yok, ama biri eklenirse bu matris YEŞİL kalarak
// yanlış cevap verir.
describe('MK2 — yüzey ikili sıraları (kaynak-seviyesi matris)', () => {
  function rungOf(file: string, marker: string): string {
    const src = readFileSync(join(SRC, file), 'utf8');
    const idx = src.indexOf(marker);
    expect(idx, `${file} içinde "${marker}" bulunamadı — matris girdisi BAYAT`).toBeGreaterThan(-1);
    const m = /zIndex:\s*(?:[^,\n]*?)'var\(--z-([\w-]+)\)'/.exec(src.slice(idx));
    expect(m, `${file}:${marker} için rung okunamadı`).not.toBeNull();
    return m![1];
  }

  // Drawer'ın irtifası KOŞULLU olduğu için ortak okuyucu ona uymuyor;
  // panel rungunu ayrı okuyoruz.
  function drawerPanelRung(): string {
    const m = /zIndex: backdrop \? 'var\(--z-([\w-]+)\)'/
      .exec(readFileSync(join(SRC, 'components/ui/Drawer.tsx'), 'utf8'));
    expect(m, 'Drawer panel rungu okunamadı — matris girdisi BAYAT').not.toBeNull();
    return m![1];
  }

  const above = (a: [string, string], b: [string, string]) =>
    expect(z(rungOf(a[0], a[1])), `${a[0]} ${b[0]}'nin üstünde olmalı`)
      .toBeGreaterThan(z(rungOf(b[0], b[1])));

  it('Command palette (modal) çekmece panelinin ÜSTÜNDE', () => {
    expect(z(rungOf('components/CommandPalette.tsx', 'alignItems: \'flex-start\'')))
      .toBeGreaterThan(z(drawerPanelRung()));
  });

  it('Çekmece paneli kolon dropdown\'ının ÜSTÜNDE', () => {
    expect(z(drawerPanelRung()))
      .toBeGreaterThan(z(rungOf('components/ColumnManager.tsx', 'Anchor left:0')));
  });

  it('Toast her modalın ÜSTÜNDE', () => {
    above(['components/ServiceCharts.tsx', 'aria-live'],
      ['components/dashboard/PanelEditor.tsx', 'position: \'fixed\'']);
  });

  it('DEV ölçer (debug) toast\'ın bile ÜSTÜNDE', () => {
    above(['components/perf/PerfMeter.tsx', 'position:'],
      ['components/ServiceCharts.tsx', 'aria-live']);
  });

  it('İç içe modal: ZoomChannelPicker, ChannelModal\'ın ÜSTÜNDE', () => {
    // Picker fiilen ChannelModal'ın İÇİNDE açılıyor; eşit rung'da olsalar
    // sıra DOM kumarına kalırdı.
    above(['pages/settings/ZoomChannelPicker.tsx', 'position: \'fixed\''],
      ['pages/settings/ChannelModal.tsx', 'position: \'fixed\'']);
  });

  it('CoSRE sohbeti (backdrop=false) zaman-aralığı panelinin ALTINDA', () => {
    // Sohbetin varlık gerekçesi: açıkken sayfayla ÇALIŞILABİLMESİ.
    // Sohbet dropdown rungının üstüne çıkarsa topbar'ın TimeRangePicker
    // paneli sohbetin altında kaybolur ve gerekçe tersine döner.
    const chat = /backdrop \? 'var\(--z-drawer-panel\)' : 'var\(--z-([\w-]+)\)'/
      .exec(readFileSync(join(SRC, 'components/ui/Drawer.tsx'), 'utf8'));
    expect(chat, 'Drawer irtifası artık kipe bağlı DEĞİL — R2 istisnası kayboldu').not.toBeNull();
    expect(z(chat![1])).toBeLessThan(z('dropdown'));
  });

  it('Flame ipuçları nav\'ın ALTINDA (sidebar onları örter)', () => {
    for (const f of ['components/FlameGraph.tsx', 'components/FlameDiff.tsx', 'components/AggregateFlame.tsx']) {
      expect(z(rungOf(f, 'position:'))).toBeLessThan(z('nav'));
    }
  });

  it('Mobil sidebar çekmecesi sayfa dropdown\'larının ÜSTÜNDE', () => {
    above(['components/Sidebar.tsx', 'position: \'fixed\', top: 0'],
      ['pages/endpoints/ColumnToggle.tsx', 'position:']);
  });
});
