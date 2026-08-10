// navHref — K2 kapısı (v0.9.932)
//
// Ne çiviliyor: sinyaller arası gezintide MUTLAK pencerenin taşındığı, göreli
// presetin taşınMAdığı, ve hedefin kendi niyetinin ezilmediği.
//
// Neden bu üçü birlikte sınanıyor: üçü de "yanlış yön" hatasına açık.
// Hiç taşımamak bugünkü bug (custom pencere düşüyor). Her şeyi taşımak yeni
// bir bug (paylaşılan link "6h" diye donar ve sticky'yi ezer). Hedefin
// paramını ezmek üçüncüsü (⌘K'nın kendi pencereli pivotu bozulur).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { navHref } from './navHref';

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
    .replace(/^\s*\/\/.*$/gm, '');
}
const comp = (f: string) =>
  stripComments(readFileSync(resolve(__dirname, '..', 'components', f), 'utf8'));

describe('navHref — taşınan pencere', () => {
  const cases: Array<{ name: string; to: string; search: string; want: string }> = [
    // ── Taşınan: yalnız MUTLAK pencere ──────────────────────────────────
    { name: 'custom pencere taşınıyor', to: '/logs', search: '?range=custom:100-200', want: '/logs?range=custom%3A100-200' },
    { name: 'env taşınıyor', to: '/logs', search: '?env=prod', want: '/logs?env=prod' },
    { name: 'ikisi birden', to: '/logs', search: '?range=custom:1-2&env=uat', want: '/logs?range=custom%3A1-2&env=uat' },

    // ── Taşınmayan: göreli preset (sticky kanalı zaten akıtıyor) ────────
    { name: 'göreli preset taşınMIYOR', to: '/logs', search: '?range=30m', want: '/logs' },
    { name: 'göreli preset ama env varsa yalnız env', to: '/logs', search: '?range=6h&env=prod', want: '/logs?env=prod' },

    // ── Taşınacak şey yoksa girdi AYNEN döner ──────────────────────────
    { name: 'boş search', to: '/logs', search: '', want: '/logs' },
    { name: 'alakasız paramlar', to: '/logs', search: '?service=api&sort=time', want: '/logs' },
    { name: 'hedefin kendi query\'si korunuyor', to: '/explore?source=logs', search: '', want: '/explore?source=logs' },

    // ── Hedef HER ZAMAN kazanır ────────────────────────────────────────
    { name: 'hedefin range\'i ezilmiyor', to: '/traces?range=1h', search: '?range=custom:1-2', want: '/traces?range=1h' },
    { name: 'hedefin env\'i ezilmiyor', to: '/traces?env=dev', search: '?env=prod', want: '/traces?env=dev' },
    { name: 'hedefin range\'i durur, env yine eklenir', to: '/traces?range=1h', search: '?range=custom:1-2&env=prod', want: '/traces?range=1h&env=prod' },

    // ── Hedefin kendi paramlarıyla BİRLEŞME ────────────────────────────
    { name: 'hedefin paramlarına eklenir', to: '/explore?source=logs', search: '?range=custom:5-9', want: '/explore?source=logs&range=custom%3A5-9' },

    // ── Fragment korunur ───────────────────────────────────────────────
    { name: 'hash korunuyor', to: '/service?name=api#deploys', search: '?range=custom:1-2', want: '/service?name=api&range=custom%3A1-2#deploys' },
    { name: 'query\'siz hash', to: '/settings#smtp', search: '?env=prod', want: '/settings?env=prod#smtp' },
    { name: 'taşınacak şey yokken hash\'e dokunulmuyor', to: '/settings#smtp', search: '', want: '/settings#smtp' },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(navHref(c.to, c.search)).toBe(c.want);
    });
  }

  it('boş env değeri taşınmıyor (env= yazmak "hepsi"ni ezerdi)', () => {
    expect(navHref('/logs', '?env=')).toBe('/logs');
  });

  it('custom öneki olmayan bir range asla taşınmıyor', () => {
    // decodeRange yalnız 'custom:' önekini mutlak sayıyor; başka bir şeyi
    // mutlak varsaymak hedefte sessizce reddedilen bir token üretirdi.
    expect(navHref('/logs', '?range=customish')).toBe('/logs');
  });
});

// Saf çekirdek doğru olsa bile üç nav yüzeyinden biri çıplak kalırsa bug o
// yüzeyde AYNEN sürer — ve fark edilmez, çünkü diğer ikisi çalışıyordur.
describe('üç nav yüzeyi de helperdan geçiyor', () => {
  it('Sidebar bağlantıları', () => {
    const src = comp('Sidebar.tsx');
    expect(src).toContain('navHref(n.href, search)');
    // Çıplak biçim geri gelmemeli.
    expect(src.includes('<Link key={n.href} to={n.href}')).toBe(false);
  });

  it('g x kısayolları — ve `search` DEP listesinde', () => {
    const src = comp('GlobalShortcuts.tsx');
    expect(src).toContain('navHref(p.to, search)');
    // Dep'siz kayıt ilk mount'un penceresini sonsuza dek taşırdı: aynı
    // bug'ın daha sinsi hâli, ve tamamen sessiz.
    expect(src).toContain('[search]');
  });

  it('⌘K sonuçları', () => {
    const src = comp('CommandPalette.tsx');
    expect(src).toContain('navHref(r.to, locationSearch)');
    expect(/navigate\(r\.to\)/.test(src)).toBe(false);
  });
});
