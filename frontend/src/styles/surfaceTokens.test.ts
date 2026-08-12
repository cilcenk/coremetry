// surfaceTokens — arka plan denetimi D1 kapısı (v0.9.978)
//
// Ne çiviliyor: her YÜZEY KABI (`.table-wrap`, `.vt-scroll`, `.empty`,
// `.card`, `.modal-dialog`) bir `background:` bildirimi TAŞIYOR ve o
// değer yükseltilmiş kademelerden biri (`--bg1` / `--bg2`).
//
// Neden bu kapı ŞART: bu bozukluk sınıfı dört kapının hiçbirine
// takılmaz. tsc CSS'e bakmaz, eslint satır-içi stile bakmaz,
// `colorLeaks` yalnız hex LİTERALİ arar (eksik bir bildirim onun için
// görünmez), `undefinedCssRefs` yalnız TANIMSIZ `var(--x)` arar — hiç
// yazılmamış bir `background` tanımsız değildir, YOKTUR. `make audit`
// CSS'e hiç bakmaz. Yani "kabın zemini yok" hatası ekranda yıllarca
// durabilir ve tek belirtisi operatörün "sayfaların beyaz arka planı
// yok" demesi olur — bu kapının doğuş sebebi tam olarak budur.
//
// Somut kayıp: `.table-wrap` 104 çağrı noktasında 13 rotanın TEK
// yüzeyi; `.empty` 209 çağrı noktasıyla tüm boş + hata ekranları.
// Bildirimlerden biri silinirse o yüzeyler SESSİZCE gri sayfa zeminine
// düşer — hiçbir test kırmızıya dönmez, hiçbir tip hatası çıkmaz.
//
// Muafiyet anahtarı GEREKÇEdir, satır numarası değil (colorLeaks'ten
// devralınan v0.9.887 dersi: satıra bağlı muafiyet bir import
// eklenince kayar).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const CSS = readFileSync(resolve(__dirname, 'globals.css'), 'utf8');

// Yorumları BOŞALT, silme — satır numaraları rapora giriyor ve
// yorumların İÇİNDEKİ örnek kodlar kural sanılmamalı (depoda yedi kez
// ısıran tuzak).
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
}

const CLEAN = stripComments(CSS);

// Bir seçicinin GÖVDESİNİ döndürür. Seçici listesi virgüllü olabilir
// (`th.sticky-right, td.sticky-right`), bu yüzden tam eşleşme değil
// "seçici listesinde geçiyor mu" aranıyor.
function ruleBodies(selector: string): string[] {
  const out: string[] = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(CLEAN)) !== null) {
    const sels = m[1].split(',').map(s => s.trim());
    // `.empty` ararken `.empty.empty-compact` ya da `.card .empty`
    // eşleşmemeli: seçicinin TAM kendisi olmalı.
    if (sels.includes(selector)) out.push(m[2]);
  }
  return out;
}

function backgroundOf(selector: string): string | null {
  for (const body of ruleBodies(selector)) {
    const m = /(?:^|[;{\s])background(?:-color)?\s*:\s*([^;]+)/.exec(body);
    if (m) return m[1].trim();
  }
  return null;
}

// Yükseltilmiş kademeler. `--bg0` sayfa zeminidir ve bir KAP için asla
// doğru değildir (bgZeroIsPageOnly kapısı onu ayrıca çiviliyor).
const ELEVATED = /var\(--bg1\)|var\(--bg2\)/;

describe('D1 — yüzey kapları zemin taşır', () => {
  const CONTAINERS = ['.table-wrap', '.vt-scroll', '.empty', '.card', '.modal-dialog'];

  it.each(CONTAINERS)('%s bir background bildirimi taşıyor', sel => {
    const bg = backgroundOf(sel);
    expect(bg, `${sel} zeminsiz — 104 tablo / 209 boş ekran gri sayfa zeminine düşer`).not.toBeNull();
  });

  it.each(CONTAINERS)('%s zemini yükseltilmiş bir kademe', sel => {
    const bg = backgroundOf(sel)!;
    expect(bg, `${sel} yüzey kabı ama zemini "${bg}" — --bg1/--bg2 olmalı`).toMatch(ELEVATED);
  });

  // D1.1'in zorunlu eşlikçisi. Sabitlenmiş kolon opak olmak zorunda ve
  // o opak değer kabın zemini DEĞİLSE her tabloda sağ kenarda yabancı
  // renkte bir şerit belirir.
  it('sabit sağ kolon kabın zeminiyle aynı token', () => {
    const bg = backgroundOf('th.sticky-right') ?? backgroundOf('td.sticky-right');
    expect(bg).toBe('var(--bg1)');
    expect(backgroundOf('.table-wrap')).toBe('var(--bg1)');
  });

  it('seçili satırın sabit kolonu kabın zeminiyle karışıyor', () => {
    const body = ruleBodies('tr.row-selected td.sticky-right')[0];
    expect(body, 'kural kayboldu').toBeTruthy();
    expect(body).toContain('var(--bg1)');
    expect(body, 'karışımın tabanı sayfa zemini kalmış').not.toContain('var(--bg0)');
  });

  // D1.6 / denetim riski V1. `.empty` artık bir kutu; bir yüzeyin
  // İÇİNDE ikinci kutu üretmemeli. 209 çağrının yalnız 11'i `compact`
  // geçtiği için bayrağa güvenilemez — kap-tabanlı soyma ŞART.
  it('yüzey içindeki boş durum kutusunu soyuyor', () => {
    const resets = /\.card \.empty[\s\S]{0,400}?\{([^{}]*)\}/.exec(CLEAN);
    expect(resets, 'kap-tabanlı .empty soyma kuralı silinmiş — kart içinde kart').toBeTruthy();
    expect(resets![1]).toMatch(/background\s*:\s*transparent/);
    expect(resets![1]).toMatch(/border\s*:\s*0/);
    for (const host of ['.ov-card-b .empty', '.table-wrap .empty', '.modal-dialog .empty']) {
      expect(CLEAN, `${host} soyma listesinden düşmüş`).toContain(host);
    }
  });

  it('.empty-compact yüzeyi geri kapatıyor', () => {
    const body = ruleBodies('.empty.empty-compact')[0];
    expect(body).toBeTruthy();
    expect(body).toMatch(/background\s*:\s*transparent/);
  });
});

// ── D7 (v0.9.990) — `--bg` alias'ı YÜKSELTİLMİŞ YÜZEY ───────────────────────
//
// Ne çiviliyor: `--bg` her yerde `--bg1` ile AYNI değeri taşır.
//
// Neden bu kapı ŞART: alias v0.9.990'a kadar temaya göre anlam
// değiştiriyordu — dark ve light'ta `--bg0` (sayfa zemini), redhat'te
// `--bg1` (yükseltilmiş yüzey). 46 çağrı noktası (çekmece paneli, komut
// paleti, KQL popover'ı, kod blokları, kart içi inset kutular) bu
// belirsizliği taşıyordu ve HİÇBİR kapı görmüyordu: `undefinedCssRefs`
// için `--bg` tanımlı, `colorLeaks` için hex literali yok,
// `bgZeroIsPageOnly` için bildirim `--bg0` içermiyor, tsc CSS'e bakmıyor.
// Tek belirti "çekmece dark'ta sayfayla aynı renk, redhat'te beyaz" olurdu.
//
// Kapının asıl işi bir DRIFT'i yakalamak: biri `--bg1`i ayarlar ve
// `--bg`yi unutursa alias sessizce yeniden çatallanır. Bu yüzden assert
// LİTERAL eşitlik üstünde — `--bg: var(--bg1)` yazıp geçmek kapıyı
// anlamsız (her zaman yeşil) hale getirirdi.
describe("D7 — --bg alias'ı yükseltilmiş yüzeye sabit", () => {
  // Bir kural bloğunun gövdesini seçici ADIYLA değil, seçicinin TAM
  // metniyle bulur (`[data-theme="redhat"] #sidebar` gibi bileşik
  // seçiciler de kapsama girsin diye).
  function blocksDeclaring(token: string): { selector: string; body: string }[] {
    const out: { selector: string; body: string }[] = [];
    const re = /([^{}]+)\{([^{}]*)\}/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(CLEAN)) !== null) {
      if (new RegExp(`(^|[;{\\s])${token}\\s*:`).test(m[2])) {
        out.push({ selector: m[1].trim().replace(/\s+/g, ' '), body: m[2] });
      }
    }
    return out;
  }

  const declOf = (body: string, token: string): string | null => {
    const m = new RegExp(`(?:^|[;{\\s])${token}\\s*:\\s*([^;]+)`).exec(body);
    return m ? m[1].trim() : null;
  };

  // Ana kural. Tema bloğu da olsa kapsanmış bir remap da olsa
  // (`[data-theme="redhat"] #sidebar` `--bg1`i #151515'e çeviriyor),
  // `--bg` bildiren her blok kendi `--bg1`ini de bildirmek ve İKİSİNİ
  // EŞİT tutmak zorunda. Kapsanmış remap'te bu şart, çünkü CSS özel
  // değişkenleri var() ikamesini BİLDİRİLDİĞİ elemanda çözer: üstteki
  // `--bg` aşağı miras kalır, alt bloğun `--bg1`ini görmez.
  it('--bg bildiren her blok --bg1 ile aynı değeri veriyor', () => {
    const blocks = blocksDeclaring('--bg\\b');
    expect(blocks.length, '--bg hiç bildirilmiyor — alias silinmişse bu kapı da gitmeli').toBeGreaterThan(0);
    const bad = blocks
      .map(b => ({ sel: b.selector, bg: declOf(b.body, '--bg\\b'), bg1: declOf(b.body, '--bg1') }))
      .filter(x => x.bg1 === null || x.bg !== x.bg1);
    expect(
      bad.map(x => `${x.sel} { --bg: ${x.bg}; --bg1: ${x.bg1} }`),
      'alias çatalladı — 46 yüzey yine temaya göre anlam değiştirir',
    ).toEqual([]);
  });

  // Değerleri ADIYLA da çiviliyoruz: yukarıdaki assert ikisini birlikte
  // kaydırarak da yeşil kalabilir. Bu blok D7'nin ölçülmüş tablosudur.
  it('üç temanın alias değeri D7 tablosuyla birebir', () => {
    const theme = (t: string) => t === 'dark'
      ? /:root\s*\{([\s\S]*?)\n\}/.exec(CLEAN)![1]
      : new RegExp(`\\[data-theme="${t}"\\]\\s*\\{([\\s\\S]*?)\\n\\}`).exec(CLEAN)![1];
    // dark #1c2128→#22272e · light #ffffff→#f6f8fa · redhat DEĞİŞMEDİ
    expect(declOf(theme('dark'), '--bg\\b')).toBe('#22272e');
    expect(declOf(theme('light'), '--bg\\b')).toBe('#f6f8fa');
    expect(declOf(theme('redhat'), '--bg\\b')).toBe('#ffffff');
  });

  // D7'nin tek istisnası. Tam-viewport splash yükseltilmiş bir yüzey
  // değil, SAYFA ZEMİNİ — alias'ta kalsaydı `body`den farklı renkte bir
  // dikdörtgen olarak yüklenirdi.
  it('PageLoader splash sayfa zemininde, alias\'ta değil', () => {
    const src = readFileSync(resolve(__dirname, '../components/Spinner.tsx'), 'utf8');
    const loader = /export function PageLoader[\s\S]*?\n\}/.exec(src)![0];
    expect(loader, 'splash yükseltilmiş yüzeye kaymış').not.toMatch(/background:\s*'var\(--bg\)'/);
    expect(loader).toMatch(/background:\s*'var\(--bg0\)'/);
  });
});
