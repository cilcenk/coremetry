// publicBackground — kabuk bütünlüğü D3 kapısı (v0.9.981)
//
// Ne çiviliyor: public sayfalar (`/login`, `/public-status`,
// `/public/trace`) sayfa zeminini `var(--bg0)` ile boyuyor; `var(--bg)`
// alias'ı yasak.
//
// Neden `--bg` yasak: alias'ın anlamı TEMAYA GÖRE değişiyor —
//   dark   `--bg` = `--bg0` (#1c2128)  ✓ uygulama zemini
//   light  `--bg` = `--bg0` (#ffffff)  ✓ uygulama zemini
//   redhat `--bg` = `--bg1` (#ffffff)  ✗ uygulama zemini `#f0f0f0`
// Yani VARSAYILAN temada (redhat, index.html) giriş ekranı beyaz, giriş
// yapıldıktan sonraki uygulama gri: operatör her oturum açışta bir zemin
// SIÇRAMASI görüyordu. Dark ve light'ta ikisi aynı değere çözüldüğü için
// bu yıllarca fark edilmedi — kapının varlık sebebi tam olarak bu:
// "üç temanın ikisinde doğru" bir renk hiçbir kapıya takılmaz.
//
// D7 alias'ın anlamını sabitleyecek; o iş bittiğinde bile bu kapı
// geçerli kalır, çünkü public sayfaların istediği şey ZEMİN, alias
// hangi anlama sabitlenirse sabitlensin.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');

// `lib/auth-paths.ts` tek kaynak; sayfa dosyaları elle eşleniyor çünkü
// rota → dosya haritası App.tsx'te lazy import olarak duruyor.
const PUBLIC_PAGES = [
  'pages/Login.tsx',
  'pages/PublicStatus.tsx',
  'pages/PublicTrace.tsx',
];

describe('D3 — public zemin sıçraması', () => {
  it('PUBLIC_PATHS listesi hâlâ üç rota (harita bayat değil)', () => {
    const ap = readFileSync(join(SRC, 'lib/auth-paths.ts'), 'utf8');
    for (const r of ['/login', '/public-status', '/public/trace']) {
      expect(ap, `${r} PUBLIC_PATHS'ten düşmüş — sayfa haritası güncellensin`)
        .toContain(r);
    }
  });

  it.each(PUBLIC_PAGES)('%s içinde var(--bg) alias\'ı yok', file => {
    const src = readFileSync(join(SRC, file), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
      .split('\n').filter(l => !/^\s*\/\//.test(l)).join('\n');
    // `var(--bg0)` / `var(--bg1)` / `var(--bg2)` serbest; çıplak alias yasak.
    const bad = /var\(--bg\)/.test(src);
    expect(bad, `${file}: var(--bg) redhat'te uygulama zeminiyle UYUŞMUYOR`).toBe(false);
  });

  it.each(PUBLIC_PAGES)('%s sayfa zeminini --bg0 ile boyuyor', file => {
    const src = readFileSync(join(SRC, file), 'utf8');
    expect(src, `${file}: sayfa zemini beyanı kayboldu`).toContain("background: 'var(--bg0)'");
  });

  // Login'in mobil kaydırma kaçışı (D3.5). `position: fixed; inset: 0`
  // taşan içeriği ERİŞİLEMEZ yapıyordu: SSO butonu + hata bandı + bilgi
  // bandı aynı anda görünürken alttaki kısma inmenin yolu yoktu.
  it('Login kaydırılabilir ve formu akışkan', () => {
    const src = readFileSync(join(SRC, 'pages/Login.tsx'), 'utf8');
    expect(src, 'fixed+inset kaydırma kaçışını yine kapatmış')
      .not.toMatch(/position: 'fixed', inset: 0, display: 'grid'/);
    expect(src).toContain("minHeight: '100dvh'");
    expect(src).toContain("overflow: 'auto'");
    expect(src, 'form genişliği yine sabit 340px — 320px viewport\'ta taşar')
      .toContain("width: 'min(340px, 100%)'");
  });

  // D3.2 — depodaki tek satır-içi `#content` ezmesi kalktı.
  it('AdminSql viewport aritmetiği kullanmıyor', () => {
    const src = readFileSync(join(SRC, 'pages/AdminSql.tsx'), 'utf8')
      .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
    expect(src, "calc(100vh - N) [data-density] padding değişimine KÖR")
      .not.toMatch(/height: 'calc\(100vh/);
    expect(src).toContain("height: '100%', minHeight: 0");
  });
});
