import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, relative } from 'node:path';

// brokenAffordances.test.ts — v0.9.869.
//
// Kaynak: tutarlılık denetimi (docs/audit/frontend-consistency-audit.md)
// MT3 (var olmayan tık vaadi) ve MT4 (keyless fragment).
//
// İkisinin ortak yanı: EKRANDA YALAN SÖYLÜYORLAR ve hiçbir kapı görmüyor.
// `tsc` ikisini de tip-doğru bulur; MT4'ün React uyarısı yalnız çalışma
// anında konsola düşer, CI'da kimse okumaz.
//
//   MT3 — TraceCompare "click a row to focus the path in either waterfall"
//         diyordu. Satırlarda onClick yoktu, cursor:pointer bile yoktu.
//         Operatör tıklar, hiçbir şey olmaz, UI'ın bozuk olduğunu sanar.
//   MT4 — SlowQueries'in satır map'i keyless bir <> fragment döndürüyordu;
//         key içteki <tr>'lerdeydi, listenin ELEMANINDA değil. Sıralama
//         değişiminde reconcile yanlış eşleşiyor: açık olan satırın
//         genişletilmiş gövdesi başka bir ifadenin altında kalabiliyordu.

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx') && !p.endsWith('.test.tsx')) out.push(p);
  }
  return out;
}

// Yorumları BOŞALT (satır numaraları korunur). Şart: bu dosyaların çoğu
// vaadin GEÇMİŞİNİ yorumda anlatıyor (Messaging'in v0.9.256 notu, bu
// dalganın kendi şerhleri). Ham tarama onları bulgu sanar — ilk denemede
// 18 dosyanın 5'i böyle yanlış pozitifti.
const blank = (s: string) =>
  s.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
   .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

const FILES = walk(SRC);
const rel = (p: string) => relative(SRC, p).split('\\').join('/');

describe('brokenAffordances — ekranda yalan söyleyen yüzeyler', () => {
  it('MT3: "click a row" diyen her yüzey satır tıkını gerçekten bağlıyor', () => {
    // KOŞULLU kural, kara liste değil: vaat ETMEK serbest, vaat edip
    // BAĞLAMAMAK yasak. Bir sayfa satır tıkını sonradan bağlarsa test
    // kendiliğinden yeşil kalır; vaadi silerse de kalır. Yalnız ikisinin
    // ortasındaki durum — MT3'ün ta kendisi — kırmızı.
    const promise = /[Cc]lick (a|any) row/;
    // Bağlama biçimleri: satırda doğrudan onClick · paylaşılan
    // rowClickHandlers yardımcısı · useDataTable'ın onOpen/rowProps yolu.
    // v0.10.451 — rowActivation (D3) da bir bağlama biçimidir (onClick + klavye).
    const wired = /<tr[^>]*onClick|rowActivation\(|rowClickHandlers|onOpen[=:]|rowProps/;
    const offenders: string[] = [];
    for (const p of FILES) {
      const code = blank(readFileSync(p, 'utf8'));
      if (!promise.test(code)) continue;
      if (wired.test(code)) continue;
      const line = code.split('\n').findIndex(l => promise.test(l)) + 1;
      offenders.push(`${rel(p)}:${line} — satır tıkı VAAT EDİLİYOR ama bağlı değil`);
    }
    // İzin listesi YOK ve olmamalı. 2026-08-10'da ölçüldü: vaat eden 10
    // yüzeyin 10'u da bağlı. Buraya muafiyet eklemek, kuralın kendisini
    // (vaat = sözleşme) çürütür — doğru hamle ya tıkı bağlamak ya cümleyi
    // silmektir; MT3 ve HeatmapCellExemplars'ta ikincisi seçildi.
    expect(offenders, `Var olmayan etkileşim vaadi:\n${offenders.join('\n')}`).toEqual([]);
  });

  it('MT4: SlowQueries satır map\'i key TAŞIYAN bir fragment döndürüyor', () => {
    const src = readFileSync(resolve(SRC, 'pages/SlowQueries.tsx'), 'utf8');
    expect(src).toContain('<Fragment key={key}>');
    expect(src).toMatch(/import \{ Fragment,/);
    // Asıl regresyon: SATIR MAP'inin döndürdüğü kök öğe key taşımalı.
    // Kontrol map'in İÇİNE sabitlendi — dosyanın kendi kök `return ( <>`'ı
    // tamamen meşru ve ilk sürümde yanlış pozitif üretmişti (liste elemanı
    // değil, bileşenin kökü).
    const code = blank(src);
    const mapAt = code.indexOf('dt.sortedRows.map(');
    expect(mapAt, 'SlowQueries: satır map\'i bulunamadı — test hedefini kaybetti').toBeGreaterThan(-1);
    const retAt = code.indexOf('return (', mapAt);
    const head = code.slice(retAt, retAt + 400).replace(/\s+/g, ' ');
    expect(head, 'SlowQueries: satır map\'i keyless <> döndürüyor — MT4 nüksü')
      .toMatch(/^return \( <Fragment key=/);
  });

  it('MT3: iki düzeltilen yüzey vaadi geri getirmedi', () => {
    const tc = blank(readFileSync(resolve(SRC, 'pages/TraceCompare.tsx'), 'utf8'));
    expect(tc).not.toMatch(/click a row/i);
    expect(tc).toContain('Sorted by absolute Δ desc');

    // Satır gövdesi değil, trace-id hücresindeki <Link> tıklanabilir.
    const hx = blank(readFileSync(resolve(SRC, 'components/HeatmapCellExemplars.tsx'), 'utf8'));
    expect(hx).not.toMatch(/click a row/i);
    expect(hx).toContain('Click a trace id to open its waterfall.');
  });
});
