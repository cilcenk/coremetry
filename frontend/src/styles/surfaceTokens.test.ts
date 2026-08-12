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
