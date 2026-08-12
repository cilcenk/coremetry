// systemShell — kabuk hizalaması D4 kapısı (v0.9.982)
//
// Ne çiviliyor: `/system/*` sekme gövdeleri (pages/Admin*.tsx) kabuk
// BASMIYOR — `<Topbar` ve `id="content"` o dosyalarda yasak; kabuğu
// yalnız `System.tsx` basıyor.
//
// Neden bu kapı ŞART: kabuk çatallanması TİP DÜZEYİNDE GEÇERLİDİR. Bir
// sekme gövdesi kendi `<Topbar>`ını render ettiğinde tsc memnun, eslint
// memnun, test yeşil, sayfa da "çalışıyor" — yalnız yanlış yerde:
// `#topbar` `.sys-content`in içinde blok eleman olduğu için içerikle
// KAYIYOR, `#content`in `flex:1 + overflow:auto`su ebeveyn blok olduğu
// için etkisiz kalıyor ve scrollport `#main`e çıkıyor, topbar da yalnız
// sağ kolon genişliğinde çiziliyor. Hiçbiri bir hata değil; hepsi bir
// YERLEŞİM yanlışı ve yerleşimi hiçbir statik kapı görmez.
//
// İkinci işi: geri kaymayı önlemek. On dosyanın herhangi birine
// "sayfanın başlığı görünsün" diye bir `<Topbar>` eklemek çok kolay ve
// sonucu ekranda İKİ topbar olur.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');
const PAGES = join(SRC, 'pages');

const ADMIN_FILES = readdirSync(PAGES)
  .filter(f => f.startsWith('Admin') && f.endsWith('.tsx'));

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .split('\n').map(l => (/^\s*\/\//.test(l) ? '' : l)).join('\n');
}

describe('D4 — /system/* kabuk hizalaması', () => {
  it('on sekme gövdesi de bulunabiliyor', () => {
    // Tarama boş dönerse aşağıdaki her şey hiçbir şey ölçmeden GEÇER.
    expect(ADMIN_FILES.length).toBeGreaterThanOrEqual(10);
  });

  it.each(ADMIN_FILES)('%s kendi <Topbar>\'ını basmıyor', f => {
    const src = stripComments(readFileSync(join(PAGES, f), 'utf8'));
    expect(src, `${f}: sekme gövdesi kabuk basıyor → ekranda iki topbar`)
      .not.toMatch(/<Topbar\b/);
  });

  it.each(ADMIN_FILES)('%s kendi #content\'ini basmıyor', f => {
    const src = stripComments(readFileSync(join(PAGES, f), 'utf8'));
    expect(src, `${f}: iç içe #content — scrollport ve padding çatallanır`)
      .not.toContain('id="content"');
  });

  it('System.tsx kabuğu SAYFA seviyesinde basıyor (Settings şablonu)', () => {
    // YORUMLARI SOY. İlk yazımda soymuyordu ve sıra assert'i YANLIŞ cevap
    // veriyordu: System.tsx'in `needsRange` açıklaması "<Topbar>" dizesini
    // örnek olarak içeriyor ve `indexOf` onu yakalıyordu, dolayısıyla
    // Topbar `#content`in İÇİNE taşınsa bile test yeşil kalıyordu
    // (mutasyonla yakalandı). Bu depoda ALTINCI "kendi yorumunu eşleyen
    // test" vakası — soymak artık refleks olmalı.
    const sys = stripComments(readFileSync(join(PAGES, 'System.tsx'), 'utf8'));
    const set = stripComments(readFileSync(join(PAGES, 'Settings.tsx'), 'utf8'));
    for (const src of [sys, set]) {
      expect(src).toMatch(/<Topbar title="/);
      expect(src).toContain('id="content"');
      expect(src).toContain('className="sys-layout"');
    }
    // Sıra da önemli: Topbar `#content`in DIŞINDA ve ÜSTÜNDE olmalı,
    // aksi hâlde `#main`in flex çocuğu olmaz ve yine kayar.
    expect(sys.indexOf('<Topbar')).toBeLessThan(sys.indexOf('id="content"'));
  });

  // Query sekmesi kabuğu yukarı taşırken kaybolabilecek TEK işlevsel
  // parçaydı: `<Topbar>`ı `range`+`onRangeChange` taşıyordu. Prop
  // plumbing yerine ikisi de `useUrlRange` çağırıyor — URL tek gerçek.
  // Varsayılanlar ayrışırsa picker sekmenin gerçekte sorguladığı
  // pencereden FARKLI görünür ve kimse fark etmez.
  it('Query sekmesinin aralık seçicisi korunmuş ve varsayılanlar eşit', () => {
    const sys = stripComments(readFileSync(join(PAGES, 'System.tsx'), 'utf8'));
    const q = stripComments(readFileSync(join(PAGES, 'AdminQuery.tsx'), 'utf8'));
    expect(sys, 'needsRange bayrağı kayboldu — Query sekmesi aralık seçicisiz kalır')
      .toMatch(/slug: 'query'[^}]*needsRange: true/);
    expect(sys).toMatch(/active\.needsRange \? \{ range, onRangeChange: setRange \}/);
    const dSys = /useUrlRange\('([^']+)'\)/.exec(sys)?.[1];
    const dQ = /useUrlRange\('([^']+)'\)/.exec(q)?.[1];
    expect(dSys, 'System.tsx useUrlRange çağrısı kayboldu').toBeTruthy();
    expect(dSys, `varsayılanlar ayrıştı: kabuk ${dSys}, sekme ${dQ}`).toBe(dQ);
  });

  // D4'ün yan etkisi: kabuk yukarı taşınınca `.sys-layout` blok
  // yüksekliğinde kalıyor ve `height: 100%` isteyen sekme gövdeleri
  // (SQL Console) AUTO'ya çöküyor.
  it('sys-layout içerik kolonunu geriyor', () => {
    const css = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');
    expect(css).toMatch(/\.sys-layout \{[^}]*min-height: 100%/);
    expect(css).toMatch(/\.sys-content \{[^}]*align-self: stretch/);
  });
});
