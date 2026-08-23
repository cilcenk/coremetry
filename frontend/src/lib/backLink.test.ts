import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { navHref } from './navHref';

// backLink.test.ts — v0.9.1320 (§3.1 K7).
//
// KURAL: bir detay sayfasının listeye dönüş linki param-SIZ olamaz.
//
// Bug: operatör bir pencere kurar, detaya iner, "← Endpoints" der ve liste
// VARSAYILAN aralıkta açılır. En sık yaşandığı yol derin linktir: bir
// problemden `serviceHref(svc, { range: eventWindow })` ile gelen operatörün
// penceresi URL'dedir ama sticky kanalda DEĞİLDİR (useUrlRange yalnız
// `setRange` çağrıldığında yazar, gelen `?range=`i kayda geçirmez). Çıplak
// geri linki o pencereyi düşürür. Depo bu sınıfı kendi kapatmıştı —
// `pages/Pod.tsx:206-209` (v0.9.965): "Bir geri linkinin bağlamı
// değiştirmesi, geri linki olmaktan çıkması demek". Dokuz kardeş yüzey
// atlanmıştı.
//
// KAPI NEDEN ŞEKLE BAKAR, DİZEYE DEĞİL (memory feedback-gate-single-spelling).
// Depodaki geri linki DÖRT ayrı yazılışla çiziliyor ve tek bir dize arayan
// kapı üçünü muaf tutardı:
//
//   1. `←` metin oku            — Service.tsx, SlowQueries.tsx, Pod.tsx
//   2. "Back to …" sözcüğü      — DatabaseDetail.tsx, EndpointDetail.tsx
//   3. `<ArrowLeft/>` ikonu     — Incident.tsx  (ilk taramada KAÇMIŞTI)
//   4. `›` kırıntı ayracı       — TriageCrumb.tsx (ayraç KARDEŞ span'de)
//
// Bu yüzden tarayıcı `<Link>…</Link>` gövdesini bütün olarak okur ve dört
// işaretin herhangi biri varsa `to=`nun ÇIPLAK bir literal olmamasını ister.
// `to={navHref(...)}` / `to={serviceHref(...)}` / `to={expr}` geçer.

const SRC = join(__dirname, '..');

/** `to=` prop'u çıplak bir literal yol mu (üç yazılış: "…", {'…'}, {"…"}). */
const BARE_TO = /\bto=(?:"(\/[^"]*)"|\{\s*'(\/[^']*)'\s*\}|\{\s*"(\/[^"]*)"\s*\})/;
/** Bir <Link …>gövde</Link> ve ardından gelen 90 karakter (kırıntı ayracı için). */
const LINK = /<Link\b([^>]*)>([\s\S]*?)<\/Link>([\s\S]{0,90})/g;

// Muafiyetler — her biri YAZILI bir gerekçeyle. Liste küçülebilir, büyüyemez;
// buraya satır eklemek "düzeltme" değil, kuralı bir site için kapatmaktır.
const EXEMPT = new Map<string, string>([
  // (bugün boş — dokuz sitenin dokuzu da navHref'ten geçiyor)
]);

function sourceFiles(dir: string, rel = ''): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules') continue;
    const full = join(dir, entry);
    const relPath = rel ? `${rel}/${entry}` : entry;
    if (statSync(full).isDirectory()) { out.push(...sourceFiles(full, relPath)); continue; }
    if (!/\.tsx$/.test(entry) || /\.test\.tsx$/.test(entry)) continue;
    out.push(relPath);
  }
  return out;
}

export interface BackLinkSite {
  file: string;
  line: number;
  /** Hangi işaret onu geri linki yaptı. */
  marks: string[];
  /** Çıplaksa hedef yol, değilse null. */
  bare: string | null;
}

// Yorum satırlarını BOŞ satırla değiştirir (satır numaraları korunur).
//
// Neden gerekli: bir düzeltmenin eski hâlini yorumda saklamak yaygın —
// `{/* eski hâli: <Link to="/services">← All services</Link> */}` — ve ham
// metin tarayan bir kapı onu CANLI site sanar. O yanlış-pozitifin tek
// "düzeltmesi" açıklamayı silmek olurdu, yani tam tersi (serviceHref.test.ts
// aynı tuzağı aynı gerekçeyle belgeliyor).
//
// Blok yorumuna GİRİŞ, satırın kırpılmış hâlinin açıcıyla BAŞLAMASINI şart
// koşar: satır ortasındaki bir `/*` bir dizenin ya da regex'in içindedir ve
// onu yorum başlangıcı saymak, gerçek bir siteyi bir literalin arkasına
// saklamaya izin verirdi — kapı, koruduğu koddan gevşek olamaz.
export function stripComments(text: string): string {
  let inBlock = false;
  return text.split('\n').map(l => {
    const s = l.trim();
    const opensBlock = !inBlock && (s.startsWith('/*') || s.startsWith('{/*'));
    const commented = inBlock || opensBlock || s.startsWith('//') || s.startsWith('*');
    if (opensBlock) inBlock = true;
    if (inBlock && s.includes('*/')) inBlock = false;
    return commented ? '' : l;
  }).join('\n');
}

/** Bir dosya gövdesindeki geri/kırıntı linklerini şekle göre bulur. */
export function findBackLinks(file: string, raw: string): BackLinkSite[] {
  const text = stripComments(raw);
  const out: BackLinkSite[] = [];
  LINK.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = LINK.exec(text))) {
    const [, attrs, inner, after] = m;
    // `{' '}` gibi JSX boşluk ifadeleri gövdeyi kirletiyor — at.
    const body = inner.replace(/\{'[^']*'\}/g, ' ').trim();
    const marks: string[] = [];
    if (body.startsWith('←')) marks.push('arrow');
    if (/back to/i.test(body)) marks.push('wording');
    if (/<ArrowLeft\b/.test(body)) marks.push('icon');
    if (after.includes('›')) marks.push('crumb');
    if (!marks.length) continue;
    const bare = BARE_TO.exec(attrs);
    out.push({
      file,
      line: text.slice(0, m.index).split('\n').length,
      marks,
      bare: bare ? (bare[1] ?? bare[2] ?? bare[3]) : null,
    });
  }
  return out;
}

describe('navHref — geri linkinin taşıdığı bağlam', () => {
  it('mutlak pencereyi ve env\'i taşır', () => {
    const href = navHref('/endpoints', '?range=custom:1000-2000&env=prod&name=checkout');
    const q = new URLSearchParams(href.slice(href.indexOf('?')));
    expect(q.get('range')).toBe('custom:1000-2000');
    expect(q.get('env')).toBe('prod');
    // Detay sayfasının KENDİ kimlik paramı listeye taşınmaz.
    expect(q.get('name')).toBeNull();
  });

  it('taşınacak bir şey yoksa yolu aynen döndürür', () => {
    expect(navHref('/databases', '?stmt=abc')).toBe('/databases');
  });
});

describe('tarayıcı — yorumdaki eski hâl canlı site sayılmaz', () => {
  const live = '<Link to={navHref(\'/services\', s)}>← All services</Link>';
  it('yorumlanmış geri linki bulunmaz', () => {
    for (const commented of [
      `{/* eski hâli: <Link to="/services">← All services</Link> */}\n${live}`,
      `// <Link to="/services">← All services</Link>\n${live}`,
      `/*\n * <Link to="/databases">Back to Databases →</Link>\n */\n${live}`,
    ]) {
      expect(findBackLinks('x.tsx', commented).filter(s => s.bare !== null)).toEqual([]);
    }
  });

  it('satır ORTASINDAKİ /* yorum başlatmaz — gerçek site literalin arkasına saklanamaz', () => {
    const sneaky = `const re = "/*"; <Link to="/services">← All services</Link>`;
    expect(findBackLinks('x.tsx', sneaky).map(s => s.bare)).toEqual(['/services']);
  });
});

describe('kaynak taraması — detay sayfasının geri linki param-sız olamaz', () => {
  const sites = sourceFiles(SRC).flatMap(rel =>
    findBackLinks(rel, readFileSync(join(SRC, rel), 'utf8')));

  it('taranan şekil gerçekten var (kapı boşa koşmuyor)', () => {
    // Pozitif kontrol: tarayıcı hiçbir şey bulmuyorsa kapı ölmüştür ve
    // "0 ihlal" sonucu anlamsızdır. Dört işaretin her biri en az bir kez
    // görünmeli — biri kaybolduysa o yazılış sessizce muaf kalıyor demektir.
    expect(sites.length).toBeGreaterThanOrEqual(9);
    for (const mark of ['arrow', 'wording', 'icon', 'crumb']) {
      expect(sites.some(s => s.marks.includes(mark)), `"${mark}" yazılışı hiç bulunamadı`).toBe(true);
    }
  });

  it('çıplak literal hedefli geri linki YOK', () => {
    const violations = sites
      .filter(s => s.bare !== null)
      .filter(s => !EXEMPT.has(s.file))
      .map(s => `${s.file}:${s.line} to="${s.bare}" [${s.marks.join('+')}]`)
      .sort();
    expect(violations, [
      'Bu geri/kırıntı linkleri hedefi çıplak bir literal olarak yazıyor,',
      'yani operatörün penceresini ve env\'ini düşürüyorlar (§3.1 K7).',
      'navHref(to, search) kullan — sidebar / ⌘K / `g x` zaten onu kullanıyor.',
      'Emsal: pages/Pod.tsx:209, pages/ServiceBacktrace.tsx:108.',
    ].join('\n')).toEqual([]);
  });

  it('muafiyet listesi bayat girdi taşımıyor', () => {
    const withBare = new Set(sites.filter(s => s.bare !== null).map(s => s.file));
    for (const [file] of EXEMPT) {
      expect(withBare.has(file), `${file} artık çıplak link yazmıyor — muafiyeti SİL`).toBe(true);
    }
  });
});
