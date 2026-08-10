import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join, relative } from 'node:path';

// undefinedCssRefs.test.ts — v0.9.868.
//
// Kaynak: tutarlılık denetimi (docs/audit/frontend-consistency-audit.md)
// BB1 (`.seg`/`.on` tanımsız) ve mT9 (`var(--muted)` tanımsız).
//
// KAPATTIĞI KAPI BOŞLUĞU: tanımsız bir CSS custom property'si ya da tanımsız
// bir sınıf adı SESSİZCE başarısız olur. `tsc --noEmit`, `eslint`, `vitest`
// ve `make audit` — DÖRDÜ DE temiz geçer; tarayıcı bildirimi düşürür, öğe
// yanlış boyanır. Her frontend hata sınıfı yakalanıyor, bu yakalanmıyordu.
//
// İki gerçek olay bu testi doğurdu:
//   • BB1 — `.seg`/`.on` hiçbir CSS'te yok, dolayısıyla iki hop butonu
//     element-seviyesi global `button` kuralına düşüyordu: AKTİF SEÇİM
//     GÖRSEL OLARAK AYIRT EDİLEMİYORDU.
//   • mT9 — `var(--muted)` diye bir token yok; renk inherit'e düşüyor ve
//     "ikincil bilgi" tonu kayboluyordu. (Aynı taramada `--text1`'in 5
//     sitesi de böyle bulundu — hiçbir denetimde yoktu.)
//
// TASARIM NOTU — izin listesi BAŞINDAN İTİBAREN bulgu kimliğiyle yazıldı.
// Aksi hâlde test, Dalga 1/3'ün BİLİNEN ve sıraya alınmış açıklarını
// (MT12 `.tbl`, mB3 `nav-group-header`) kırmızı yakar ve bu dalgayı tıkardı.
// Her girdi 2026-08-10'da tek tek koddan doğrulandı; kimliksiz girdi
// eklenmemeli — kimliksiz bir muafiyet, testin kendisini anlamsızlaştırır.

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

const ALL = walk(SRC);
const rel = (p: string) => relative(SRC, p).split('\\').join('/');
const stripComments = (s: string) =>
  s.replace(/\/\*[\s\S]*?\*\//g, '').split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

// Satır NUMARALARINI koruyan yorum temizleme. Düz `strip` kullanılamaz:
// bulguları `dosya:satır` olarak raporluyoruz, kayan numara raporu bozar.
// (İlk sürüm bunu atlamış ve globals.css'in `/* … */` bloğunun İÇİNDEKİ
// örnek kodu gerçek bir bulgu sanmıştı.)
const blankComments = (s: string) =>
  s.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '))
   .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n');

// ── Tanımlar ──────────────────────────────────────────────────────────────
// Token'lar YALNIZ .css dosyalarından toplanır. Sınıflar ayrıca TSX içine
// gömülü <style> bloklarından da toplanır — AdminCatalog'un yapışkan-başlık
// hack'i böyle yaşıyor ve onu saymamak yanlış pozitif üretiyordu.
const definedTokens = new Set<string>();
const definedClasses = new Set<string>();
for (const p of ALL) {
  if (p.endsWith('.css')) {
    const t = stripComments(readFileSync(p, 'utf8'));
    for (const m of t.matchAll(/(--[A-Za-z0-9_-]+)\s*:/g)) definedTokens.add(m[1]);
    for (const m of t.matchAll(/\.(-?[A-Za-z_][\w-]*)/g)) definedClasses.add(m[1]);
  } else if (p.endsWith('.tsx') || p.endsWith('.ts')) {
    const t = readFileSync(p, 'utf8');
    for (const blk of t.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g))
      for (const m of blk[1].matchAll(/\.(-?[A-Za-z_][\w-]*)/g)) definedClasses.add(m[1]);
  }
}

describe('undefinedCssRefs — sessizce düşen token/sınıf referansları', () => {
  it('fallback\'siz her var(--x) tanımlı bir token\'a işaret ediyor', () => {
    // KURAL: `var(--x, birşey)` GÜVENLİ — fallback bilinçli bir seçim ve
    // depoda meşru kullanımı var (`--panel2, var(--bg)`; `--mono, monospace`;
    // PageControls'ün çalışma anında yazdığı `--controls-h, 0px`). Yalnız
    // ÇIPLAK `var(--x)` bir vaattir: token yoksa bildirim düşer.
    const bare = /var\(\s*(--[A-Za-z0-9_-]+)\s*\)/g;
    const offenders: string[] = [];
    for (const p of ALL) {
      if (!/\.(tsx?|css)$/.test(p)) continue;
      if (/\.test\.tsx?$/.test(p)) continue; // test fixture'ları (`var(--a)`) kapsam dışı
      const raw = readFileSync(p, 'utf8').split('\n');
      blankComments(readFileSync(p, 'utf8')).split('\n').forEach((code, i) => {
        for (const m of code.matchAll(bare))
          if (!definedTokens.has(m[1]))
            offenders.push(`${rel(p)}:${i + 1}: var(${m[1]}) tanımsız — ${raw[i].trim().slice(0, 90)}`);
      });
    }
    expect(offenders, `Tanımsız CSS custom property (sessizce düşer):\n${offenders.join('\n')}`).toEqual([]);
  });

  it('className literalleri tanımlı sınıflara işaret ediyor', () => {
    // Yalnız LİTERAL `className="…"`. Şablon/koşullu ifadeler kapsam dışı:
    // statik olarak çözülemezler ve zorlamak yanlış pozitif üretir.
    const ALLOW = new Map<string, string>([
      // ── Dalga 3'ün BİLİNEN açıkları — sıraya alındı, burada YAKALANMAMALI
      // (`tbl` / MT12 v0.9.874'te KAPANDI ve buradan çıkarıldı — izin listesi
      //  bayatlarsa test yanlış sebeple yeşil kalır.)
      // (`nav-group-header` / `nav-group` — mB3, v0.9.899'da KAPANDI ve
      //  buradan çıkarıldı: ilki gerçek bir CSS kuralına kavuştu,
      //  ikincisi ölü sınıftı ve elementten silindi.)
      // ── Davranışsal kancalar: stil sınıfı DEĞİL, querySelector hedefi.
      // Stilleri satır içinde; sınıf yalnız DOM\'da bulunmak için var.
      ['tsp-tooltip',        'JS kancası — TimeSeriesPanel.tsx:223,572,789 querySelector ile buluyor'],
      ['tsp-ypill',          'JS kancası — TimeSeriesPanel.tsx:573 querySelector ile buluyor'],
      // ── Öksüz süs sınıfları: ne CSS ne JS tüketicisi var. Zararsız ama
      // yanıltıcı; hiçbir dalgada sahiplenilmedi, Dalga 0 kapsamı değil.
      ['fgb',                'öksüz — FilterGroupBuilder.tsx:77, tüketicisi yok (2026-08-10 tarandı)'],
      ['fgb-group',          'öksüz — FilterGroupBuilder.tsx:90, tüketicisi yok'],
      ['metric-panel-title', 'öksüz — MetricPanel.tsx:230; mB13 bu sınıfın VAR olduğunu varsayıyor, YOK'],
      ['wf-err-dot',         'öksüz — AggregatedStructure.tsx:141, tüketicisi yok'],
    ]);
    const lit = /className\s*=\s*"([^"{}]+)"/g;
    const offenders: string[] = [];
    for (const p of ALL) {
      if (!p.endsWith('.tsx') || /\.test\.tsx$/.test(p)) continue;
      const raw = readFileSync(p, 'utf8').split('\n');
      blankComments(readFileSync(p, 'utf8')).split('\n').forEach((code, i) => {
        for (const m of code.matchAll(lit))
          for (const c of m[1].split(/\s+/))
            if (c && !definedClasses.has(c) && !ALLOW.has(c))
              offenders.push(`${rel(p)}:${i + 1}: .${c} tanımsız — ${raw[i].trim().slice(0, 90)}`);
      });
    }
    expect(offenders, `Tanımsız CSS sınıfı (öğe stilsiz kalır / global kurala düşer):\n${offenders.join('\n')}`).toEqual([]);
  });

  it('BB1 ve mT9 düzeltmeleri yerinde', () => {
    // Nokta iddialar: bu iki site tam olarak neyi kaybetmişti.
    const fn = readFileSync(resolve(SRC, 'components/topology/FocusedNeighborhood.tsx'), 'utf8');
    expect(fn).toContain('className="segmented"');
    expect(fn).not.toContain('className="seg"');
    expect(fn).toMatch(/hops === 1 \? 'active'/);

    const cb = readFileSync(resolve(SRC, 'pages/service/ServiceClusterBreakdown.tsx'), 'utf8');
    expect(stripComments(cb)).not.toContain('var(--muted)');
  });

  it('token izin listesi yok — çıplak var() muafiyeti kabul edilmiyor', () => {
    // Sınıf tarafının izin listesi var (öksüzler zararsız), token tarafının
    // YOK ve olmamalı: tanımsız bir renk/ölçü token'ı her zaman görünür bir
    // kusurdur. Bu iddia, ileride birinin "hızlıca" muafiyet eklemesini
    // görünür kılar.
    const self = readFileSync(__filename, 'utf8');
    const tokenBlock = self.slice(self.indexOf('fallback\\\'siz her var'), self.indexOf('className literalleri'));
    expect(tokenBlock).not.toMatch(/ALLOW/);
  });
});
