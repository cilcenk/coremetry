import { describe, it, expect } from 'vitest';
import { readdirSync, statSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

// detailSurfaceGate — v0.9.1366.
//
// NE ÇİVİLİYOR: veritabanının detay yüzeyinin TEK olduğu — bir SAYFA
// (`/database`), çekmece değil. `pages/databases/detailSections.tsx`in tüm
// tasarım kararı buna dayanıyor ("çekmece + sayfa aynı anda" sorusunun db
// tarafındaki cevabı: olamaz, çünkü çekmece v0.9.840'ta emekli oldu).
//
// NEDEN BİR KAPI GEREKİYOR — v0.9.846 DERSİ. `features/dependencies/
// DetailDrawer.tsx`in `kind === 'db'` dalı KODDA DURUYOR ama çalışma
// zamanında ERİŞİLEMEZ. Bu ayrım bir kez zaten yanlış okundu: v0.9.846'da
// bir bileşen "DetailDrawer hâlâ mount ediyor" gerekçesiyle yaşatıldı, oysa
// dal erişilemezdi — union, ölü-kod muhakemesini yanlışladı. Erişilemezliği
// sağlayan şey TEK BİR İFADE (`DependenciesTable.tsx` `isOpen = !onRowNavigate
// && …`) ve TEK BİR PROP (/databases'in `onRowNavigate` geçmesi). İkisinden
// biri sessizce giderse db çekmecesi geri gelir ve /database sayfasıyla
// AYNI payload'ı ikinci bir gövdeden çizmeye başlar — yani bu dilimin
// çözdüğü kopya, kimse fark etmeden geri döner.
//
// ── YOKLUK DEĞİL, MEKANİZMA ─────────────────────────────────────────────
//
// Kapı "çekmece açılmıyor" diye bir şey ölçemez (render davranışı, kaynak
// taraması değil). Ölçtüğü şey erişilemezliğin İKİ ÖN KOŞULU. Biri
// kaybolduğunda test kırılır ve kararın gözden geçirilmesi gerekir —
// sessizce geri gelmez.
//
// ── PENCERE FONKSİYONA/ETİKETE HAPSEDİLİR ───────────────────────────────
//
// Bir JSX etiketinin proplarını okurken pencereyi "ilk `>`e kadar" almak
// yanlış: `extraControls={<label …>…</label>}` gibi bir prop `>` içerir ve
// pencere erken kapanır, `kind` propu okunamaz. O yüzden tarayıcı `{}`
// derinliği sayıyor ve etiketi ancak DERİNLİK 0'daki `>` kapatıyor. Bu
// davranışın kendi meta-testi var (aşağıda), çünkü yanlış pencere kapıyı
// sessizce yanlış şeyi ölçer hâle getirir.
const SRC = resolve(__dirname, '../..');

/** Bir JSX etiketinin açılış penceresi — `{}` derinliğine duyarlı. */
export function tagWindow(src: string, tag: string, fromIndex = 0): { text: string; end: number } | null {
  const start = src.indexOf(`<${tag}`, fromIndex);
  if (start < 0) return null;
  let depth = 0;
  for (let i = start + tag.length + 1; i < src.length; i++) {
    const c = src[i];
    if (c === '{') depth++;
    else if (c === '}') depth--;
    else if (c === '>' && depth === 0) return { text: src.slice(start, i + 1), end: i + 1 };
  }
  return null;
}

function allTagWindows(src: string, tag: string): string[] {
  const out: string[] = [];
  let i = 0;
  for (;;) {
    const w = tagWindow(src, tag, i);
    if (!w) return out;
    out.push(w.text);
    i = w.end;
  }
}

const TEST_MARK = '.' + 'test' + '.';
function sourceFiles(dir: string, rel = ''): string[] {
  const out: string[] = [];
  for (const e of readdirSync(dir)) {
    const abs = join(dir, e);
    const r = rel ? `${rel}/${e}` : e;
    if (statSync(abs).isDirectory()) { out.push(...sourceFiles(abs, r)); continue; }
    if (!e.endsWith('.tsx') || e.includes(TEST_MARK)) continue;
    out.push(r);
  }
  return out;
}

describe('db detay yüzeyi TEK — sayfa, çekmece değil', () => {
  const files = sourceFiles(SRC);

  it('tarama gerçekten kaynak buluyor', () => {
    expect(files.length).toBeGreaterThan(100);
  });

  it('her <DependenciesTable kind="db"> mount noktası onRowNavigate GEÇİYOR', () => {
    const sites: string[] = [];
    const missing: string[] = [];
    for (const f of files) {
      if (f.endsWith('DependenciesTable.tsx')) continue; // bileşenin kendisi
      for (const w of allTagWindows(readFileSync(join(SRC, f), 'utf8'), 'DependenciesTable')) {
        if (!/kind\s*=\s*["']db["']/.test(w)) continue;
        sites.push(f);
        if (!/onRowNavigate\s*=/.test(w)) missing.push(f);
      }
    }
    // Boş küme kapıyı yeşil bırakmasın: mount noktası kalmadıysa kapı
    // ölçmüyor demektir, "temiz" demek değil.
    expect(sites.length, 'kind="db" mount noktası bulunamadı — kapı ölçmüyor')
      .toBeGreaterThan(0);
    expect(missing, 'onRowNavigate GEÇMEYEN db mount noktası: db çekmecesi geri gelir')
      .toEqual([]);
  });

  it('DependenciesTable erişilemezlik muhafızını (isOpen = !onRowNavigate) hâlâ yazıyor', () => {
    const src = readFileSync(join(SRC, 'components/DependenciesTable.tsx'), 'utf8');
    const line = src.split('\n').find(l => /const\s+isOpen\s*=/.test(l));
    expect(line, 'isOpen ataması bulunamadı').toBeTruthy();
    expect(line!.replace(/\s/g, '')).toContain('!onRowNavigate');
  });

  it('/database sayfası gövdeyi detailSections.tsx\'ten tüketiyor', () => {
    const page = readFileSync(join(SRC, 'pages/DatabaseDetail.tsx'), 'utf8');
    expect(page).toContain("from '@/pages/databases/detailSections'");
  });
});

// ── pencere tarayıcısının kendi meta-testi ──────────────────────────────
describe('tagWindow — pencere etikete hapsediliyor', () => {
  it('basit self-closing etiketi tam okur', () => {
    const w = tagWindow('<DependenciesTable rows={r} kind="db" onRowNavigate={go} />', 'DependenciesTable');
    expect(w!.text).toContain('kind="db"');
    expect(w!.text).toContain('onRowNavigate');
  });

  it('İÇ İÇE JSX taşıyan propta ERKEN KAPANMAZ (kapının asıl riski)', () => {
    const src = '<DependenciesTable\n  extraControls={<label><input type="checkbox" /> Compare</label>}\n  kind="db" onRowNavigate={go} />';
    const w = tagWindow(src, 'DependenciesTable');
    expect(w!.text, 'iç <label> ve <input/> penceresi erken kapatmamalı').toContain('kind="db"');
    expect(w!.text).toContain('onRowNavigate');
  });

  it('KOMŞU etikete TAŞMAZ — sonraki mount noktasının propunu kendi kanıtı saymaz', () => {
    const src = '<DependenciesTable kind="db" />\n<DependenciesTable kind="queue" onRowNavigate={go} />';
    const first = tagWindow(src, 'DependenciesTable')!;
    expect(first.text).toContain('kind="db"');
    expect(first.text, 'komşunun onRowNavigate\'i sızmamalı').not.toContain('onRowNavigate');
  });

  it('iki pencereyi ayrı ayrı sayar', () => {
    const src = '<X a="1" />\n<X a="2" />';
    expect(allTagWindows(src, 'X').length).toBe(2);
  });

  it('kapanmayan etiket için null döner (sonsuz döngü yok)', () => {
    expect(tagWindow('<X a={ ', 'X')).toBeNull();
    expect(allTagWindows('<X a={ ', 'X')).toEqual([]);
  });

  it('ön-ekli başka bir etiketi karıştırmaz ama ön-ek eşleşmesini de itiraf eder', () => {
    // `<XY>` de `<X` ile başlar. Bu tarayıcı ön-ek eşleştiriyor; yukarıdaki
    // gerçek kullanım için sorun değil (DependenciesTable diye başlayan
    // ikinci bir bileşen yok) ama davranış YAZILI olsun ki bir gün
    // olduğunda sürpriz olmasın.
    expect(tagWindow('<XY a="1" />', 'X')!.text).toContain('a="1"');
  });
});
