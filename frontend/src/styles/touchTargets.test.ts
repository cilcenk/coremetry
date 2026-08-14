// touchTargets — dar ekran denetimi D2 kapısı (v0.9.980)
//
// Ne çiviliyor: `<640px`te ikon butonlarının ve buton/pager
// hedeflerinin en az 36px'e büyütüldüğü.
//
// Neden bir sayı kapıya değer: masaüstünde `.btn-icon.ib-xs` 20×20'dir
// ve bu DOĞRUdur — fare 20px'lik bir hedefi ıskalamaz. Aynı 20px
// parmakla WCAG 2.5.8'in 24px tabanının bile ALTINDA (Apple HIG 44px
// önerir). `[data-density="dense"]` bunu daha da küçültüyor. Yani hata
// yalnız DAR ekranda var ve yalnız orada düzeltilmeli — kural bu yüzden
// bir `@media` bloğunun içinde yaşamak zorunda, ve tam da bu yüzden
// kimse silindiğini fark etmez: masaüstünde hiçbir şey değişmez,
// telefonda operatör "tıklayamıyorum" der.
//
// 36px seçimi: `width/height` beyanları yerinde bırakılıp yalnız
// `min-width/min-height` eklendiği için masaüstü ölçüleri BİT BİT
// aynı kalıyor; 36 hem parmak için yeterli hem de dar bir topbar'da
// üç ikonu yan yana sığdırıyor (44 sığdırmıyordu).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const RAW = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');

// v0.9.1013 — YORUMLARI SOY. Bu kapı depodaki tek yorum SOYMAYAN stil
// testiydi ve tam da bu dosyada patlamaya hazırdı: G0'ın gerekçe bloğu
// kuralların kendi seçicilerini (`button.sm`, `.pager button`) düz metin
// olarak ANIYOR. Soyulmadan, bir gün biri kuralı silip yorumu bıraksa
// kapı YEŞİL kalırdı — yorumu "kural" sanarak. Karakter-karakter
// boşlukla değiştiriyoruz ki satır numaraları ve ofsetler korunsun
// (depo idiyomu: colorLeaks/radiusTokens/zLayers aynısını yapıyor).
const CSS = RAW.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));

const MIN_TOUCH = 36;
// WCAG 2.5.8 "Target Size (Minimum)" — 24×24 CSS px. Masaüstü tabanı.
const WCAG_MIN = 24;

function phoneLayer(): string {
  const m = /@media \(max-width: 640px\) \{([\s\S]*)/.exec(CSS);
  expect(m, '640px telefon katmanı kayboldu').toBeTruthy();
  return m![1];
}

describe('D2 — dokunma hedefleri', () => {
  it('ikon butonları telefonda en az 36px', () => {
    const layer = phoneLayer();
    const rule = /\.btn-icon[^{]*\{([^}]*min-width[^}]*)\}/.exec(layer);
    expect(rule, '.btn-icon için <640px override kayboldu').toBeTruthy();
    const w = /min-width:\s*(\d+)px/.exec(rule![1]);
    const h = /min-height:\s*(\d+)px/.exec(rule![1]);
    expect(Number(w![1])).toBeGreaterThanOrEqual(MIN_TOUCH);
    expect(Number(h![1])).toBeGreaterThanOrEqual(MIN_TOUCH);
  });

  it('üç ib-* boyutunun HEPSİ kapsanıyor', () => {
    // `ib-xs` (20px) kapsam dışı kalırsa kural en zararlı vakayı ıskalar.
    const rule = /(\.btn-icon[^{]*)\{[^}]*min-width:\s*3\dpx[^}]*\}/.exec(phoneLayer())
      ?? /(\.btn-icon[^{]*)\{[^}]*min-width[^}]*\}/.exec(phoneLayer());
    const sel = rule![1];
    for (const size of ['ib-xs', 'ib-sm', 'ib-md']) {
      expect(sel, `${size} dokunma hedefi kuralının dışında kalmış`).toContain(size);
    }
  });

  it('pager ve genel butonlar da büyüyor', () => {
    const layer = phoneLayer();
    expect(layer, 'pager butonları ~20px — sayfalama telefonda tıklanamaz')
      .toMatch(/\.pager button\s*\{[^}]*min-height:\s*3\dpx/);
    // Yoğunluk modu kapsanmazsa `[data-density] .controls button` (0,2,1)
    // bu kuralı özgüllükle yener.
    expect(layer).toMatch(/\[data-density\] \.controls button/);
  });

  // ——— G0 (v0.9.1013) — ÖZGÜLLÜK YARIŞI —————————————————————————
  //
  // Bir üstteki iddia `button` (0,0,1) kuralını çiviliyordu ve bu
  // kapının KÖR NOKTASIYDI: `@media` özgüllük EKLEMEZ, dolayısıyla
  // `button.sm` (0,1,1) o kuralı yeniyor. Atomun en çok kullanılan iki
  // boyutu (`sm` ~19px, `xs` ~15px) telefonda 36px tabanını hiç
  // almıyordu — yani D2 sevk edilmişti ama pratikte uygulanmıyordu.
  //
  // Elle çizilmiş dört sayfalama şeridi (AnomaliesPage, Logs "Load
  // more", Metrics kataloğu, ServiceSignalTabs "200 satır daha")
  // `Button size="sm"` kullanıyor ve `.pager` sınıfı TAŞIMIYOR →
  // yukarıdaki `.pager button` iddiası onları hiç görmüyordu.
  it('atomun sm/xs boyutları telefonda 36px tabanını ALIYOR', () => {
    const layer = phoneLayer();
    for (const size of ['sm', 'xs']) {
      const rule = new RegExp(`button\\.${size}[^{]*\\{([^}]*)\\}`).exec(layer);
      expect(rule, `button.${size} için <640px min-height override'ı yok — ` +
        'taban `button` kuralı (0,0,1) bunu YENEMEZ').toBeTruthy();
      const h = /min-height:\s*(\d+)px/.exec(rule![1]);
      expect(h, `button.${size} kuralı min-height bildirmiyor`).toBeTruthy();
      expect(Number(h![1])).toBeGreaterThanOrEqual(MIN_TOUCH);
    }
  });

  it('masaüstü .pager button WCAG 24px tabanının ÜSTÜNDE', () => {
    // Ölçüm: 12px yazı (~14,4px satır kutusu) + 3px×2 dolgu +
    // `border:none` → ~20,4px. Telefon katmanı (36px) bunu yalnız dar
    // ekranda örtüyordu, yani kusur masaüstünde YAŞIYORDU.
    //
    // Taban kuralı `@media` DIŞINDA aranmalı — telefon override'ı bu
    // iddiayı yanlışlıkla karşılamasın diye katmandan öncesine bakıyoruz.
    const base = CSS.slice(0, CSS.indexOf('@media (max-width: 640px)'));
    const rule = /\.pager button\s*\{([^}]*)\}/.exec(base);
    expect(rule, 'masaüstü .pager button kuralı kayboldu').toBeTruthy();
    const h = /min-height:\s*(\d+)px/.exec(rule![1]);
    expect(h, '.pager button min-height bildirmiyor → ~20px hedef').toBeTruthy();
    expect(Number(h![1])).toBeGreaterThan(WCAG_MIN);
  });

  it('masaüstü ölçüleri DEĞİŞMEDİ (width/height beyanları yerinde)', () => {
    // Kapının ikinci işi: bir gün biri "sadeleştirme" diye taban
    // ölçüleri 36'ya çekerse masaüstü yoğunluğu bozulur. Taban
    // beyanları burada çivileniyor.
    expect(CSS).toMatch(/\.btn-icon\.ib-xs \{ width: 20px; height: 20px;/);
    expect(CSS).toMatch(/\.btn-icon\.ib-sm \{ width: 24px; height: 24px;/);
    expect(CSS).toMatch(/\.btn-icon\.ib-md \{ width: 28px; height: 28px;/);
  });

  it('sekme şeridi telefonda kaydırıyor', () => {
    // M6: `.tab-strip` ne sarıyor ne kaydırıyordu; `/service` 7 sekmeyle
    // ≈700px istiyor. 12 yüzey etkileniyor.
    const layer = phoneLayer();
    expect(layer).toMatch(/\.tab-strip\s*\{[^}]*overflow-x:\s*auto/);
    expect(layer).toMatch(/\.tab-strip > button\s*\{[^}]*flex:\s*none/);
    expect(layer).toMatch(/\.tab-strip > button\s*\{[^}]*white-space:\s*nowrap/);
  });

  it('iOS güvenli alanı + dvh kabukta karşılanıyor', () => {
    const layer = phoneLayer();
    // M4: `100vh` iOS'ta araç çubuğunu saymaz → yapışkan alt şerit
    // çubuğun altında kalır ve tıklanamaz.
    expect(layer).toMatch(/#app\s*\{[^}]*100dvh/);
    expect(layer).toMatch(/env\(safe-area-inset-bottom\)/);
    // Taban `100vh` fallback olarak DURMALI (dvh desteği yoksa).
    expect(CSS).toMatch(/#app \{[^}]*height: 100vh/);
  });
});
