// clickableAffordances — Zincir E bulgusu / Dalga 6 kapısı (v0.9.923)
//
// Ne çiviliyor: interaktif kabuk öğelerinin klavyeyle erişilebilir bir
// öğe olduğu — `<div onClick>` değil.
//
// Neden dar kapsam: `<div onClick>` deponun her yerinde meşru olarak
// var (tablo satırları `data-row-idx` + `useTableNav` üzerinden klavye
// alıyor, kartlar içlerindeki gerçek linke delege ediyor). Genel bir
// yasak yüzlerce yanlış pozitif üretir ve hiçbir şeyi çivilemez.
// Bunun yerine: uygulama KABUĞUNUN (sidebar/topbar) tetikleri. Oradaki
// bir div, çıkış yolu dahil tüm hesap eylemlerini fare olmadan
// ULAŞILAMAZ yapar — v0.9.923'te sidebar kullanıcı menüsü tam bu
// durumdaydı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const SRC = resolve(__dirname, '..');

describe('kabuk tetikleri klavyeyle erişilebilir', () => {
  it('sidebar kullanıcı menüsü bir <button>', () => {
    const src = readFileSync(resolve(SRC, 'components/Sidebar.tsx'), 'utf8');
    const i = src.indexOf('className="sb-user' + '-trigger"');
    expect(i, 'tetik sınıfı kayboldu — kapı BAYAT, yeniden bul').toBeGreaterThan(-1);
    const decl = src.slice(Math.max(0, i - 200), i + 400);
    expect(decl).toContain('<button');
    expect(decl).toContain('aria-expanded');
    expect(decl).toContain('aria-haspopup');
  });

  it('tetiğin hover kuralı background BİLDİRİYOR', () => {
    // v0.9.895: element seviyesindeki `button:hover:not(:disabled)`
    // (özgüllük 0,2,1) bir :hover kuralıdır; sınıfın (0,1,0) taban
    // bildirimi onu yenemez. Modifier :hover'ı background yazmazsa
    // buton üstüne gelindiğinde DOLU MAVİ olur.
    const css = readFileSync(resolve(SRC, 'styles/globals.css'), 'utf8');
    const HOVER = ':hover' + ':not\\(:disabled\\)';
    const m = new RegExp(`\\.sb-user-trigger${HOVER}\\s*\\{[^}]*background`).exec(css);
    expect(m, 'hover kuralı background bildirmiyor → element kuralı kazanır').not.toBeNull();
  });

  it('element seviyesi buton hover kuralı hâlâ var (kısıt hâlâ gerekli)', () => {
    // Kural kaldırılırsa yukarıdaki zorunluluk gereksiz kalır; kapı
    // gereksiz yere kısıtlıyor olmasın diye haber versin.
    const css = readFileSync(resolve(SRC, 'styles/globals.css'), 'utf8');
    expect(css.includes('button:hover:not(:disabled)')).toBe(true);
  });
});
