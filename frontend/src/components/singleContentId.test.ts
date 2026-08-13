// singleContentId — kabuk bütünlüğü D3 kapısı (v0.9.981)
//
// Ne çiviliyor: aynı ANDA render olabilecek iki `id="content"` yok.
//
// Neden bu kapı ŞART: `id` tekilliği HTML'in kuralıdır ama hiçbir
// tarayıcı onu zorlamaz, tsc göremez, eslint göremez, jsdom render'ı da
// şikâyet etmez. Bozukluk yalnız `document.getElementById('content')`
// çağıran biri olduğunda görünür ve o zaman bile SESSİZDİR: fonksiyon
// hata vermez, İLK düğümü döndürür — bu depoda o düğüm `display:none`
// olanıydı, yani `clientWidth` 0. `useContentWidth` 0'ı 1200 fallback'ine
// çevirdiği için grafik adımı "yanlış ama makul" bir değere düşerdi.
// Hiçbir test kırmızıya dönmez, hiçbir ekran boş kalmaz.
//
// KURAL BİLEREK GEVŞEK (D3 seviyesi): "dosya başına birden fazla kabuk
// kabı olabilir, AMA aynı return ağacında olamaz". Onlarca dosya
// erken-dönüş dallarında (`if (loading) return <PageShell>…`) kabı
// tekrarlıyor ve bu dallar KARŞILIKLI DIŞLAYICI — aynı anda ikisi
// render olmaz. Ayırt etmenin tek kaynak-seviyesi yolu `return`
// sayısıyla karşılaştırmak.
//
// Tam kilit (`id="content"` yalnız `components/ui/PageShell.tsx`te)
// v0.9.1000'de YÜRÜRLÜĞE GİRDİ — `pageShellAdoption.test.ts` allowlist'i
// boşaldı. Bu kapı buna rağmen GEVŞEK kalıyor ve kaldırılmıyor: iki kapı
// aynı şeyi ölçmüyor. O, kabın nereden BASILDIĞINI kilitliyor; bu, aynı
// return ağacında KAÇ TANE basıldığını. `<PageShell>` iki kez çağrılırsa
// yine iki `id="content"` doğar ve o hâlâ geçersiz HTML — erken-dönüş
// dalları kabı meşru şekilde tekrarladığı için (TraceCompare 2 kap /
// 3 return) kural "dosya başına tek kap"a sertleştirilemez.
//
// v0.9.993 — KAPSAM GENİŞLETİLDİ, kural DEĞİL. D9 geçişi başladığından
// beri kap iki biçimde yazılabiliyor: elle `<div id="content">` ya da
// `<PageShell>`. Tarama yalnız birinciyi arıyordu, yani GEÇEN her sayfa
// bu kapının görüş alanından ÇIKIYORDU — v0.9.992'de yedi pilot geçti ve
// o günden beri onlarda "aynı return ağacında iki kap" kuralı hiç
// ölçülmüyordu. Boşluk sessizdi: kapı yeşil kalıyor, çünkü ölçecek dosya
// bulamıyor. Artık iki biçim de sayılıyor ve "tarama bir şey buluyor"
// assert'i geçişin İLERLEMESİYLE ERİMİYOR (44 dosya, biçimden bağımsız).
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// Yorumları soy: bu dosyanın ve sayfaların KENDİ açıklamaları
// `id="content"` dizesini içeriyor ("kendi yorumunu eşleyen test"
// tuzağı — bu depoda beş kez ısırdı).
function stripComments(src: string): string {
  let out = '';
  let mode: 'code' | 'line' | 'block' = 'code';
  for (let i = 0; i < src.length; i++) {
    const c = src[i];
    const n = src[i + 1] ?? '';
    if (mode === 'code') {
      if (c === '/' && n === '/') { mode = 'line'; out += '  '; i++; continue; }
      if (c === '/' && n === '*') { mode = 'block'; out += '  '; i++; continue; }
      out += c;
    } else if (mode === 'line') {
      if (c === '\n') { mode = 'code'; out += '\n'; } else out += ' ';
    } else {
      if (c === '*' && n === '/') { mode = 'code'; out += '  '; i++; continue; }
      out += c === '\n' ? '\n' : ' ';
    }
  }
  return out;
}

// Kabuk kabının İKİ yazılışı: elle yazılmış `<div id="content">` ve D9
// atomu `<PageShell>`. `<PageShell[\s>]` yalnız ÇAĞRI noktalarını yakalar
// — atomun kendi `export function PageShell(` tanımında `<` yok, import
// satırında da yok.
const SHELL = /id="content"|<PageShell[\s>]/g;
const shellCount = (src: string) => (src.match(SHELL) ?? []).length;

const FILES = walk(SRC)
  .filter(p => !p.endsWith('.test.ts') && !p.endsWith('.test.tsx'))
  .map(p => ({ p: p.slice(SRC.length + 1), src: stripComments(readFileSync(p, 'utf8')) }))
  .filter(f => shellCount(f.src) > 0);

describe('D3 — tek #content', () => {
  it('tarama bir şey buluyor', () => {
    expect(FILES.length).toBeGreaterThan(30);
  });

  it('her dosyada kabuk kabı sayısı return sayısını AŞMIYOR', () => {
    // Aşıyorsa en az iki tanesi aynı return ağacındadır → aynı anda
    // DOM'a girerler. `<PageShell>` de sayılıyor: atom `#content`i
    // basan tek yer olduğu için iki `<PageShell>` de iki `#content`tir.
    const bad = FILES
      .map(f => ({
        p: f.p,
        ids: shellCount(f.src),
        returns: (f.src.match(/\breturn\s*\(/g) ?? []).length,
      }))
      .filter(f => f.ids > f.returns)
      .map(f => `${f.p}: ${f.ids} × kabuk kabı / ${f.returns} × return`);
    expect(bad, 'aynı return ağacında iki #content — geçersiz HTML').toEqual([]);
  });

  // D3.1'in ta kendisi. Bu iki sayfa detay açıkken liste kabını
  // UNMOUNT ETMİYOR (bilinçli: facet/seçim/scroll state'i korunuyor),
  // dolayısıyla kabın id taşımaması YAPISAL bir gereklilik.
  it('gizlenen liste kapları id değil SINIF taşıyor', () => {
    for (const file of ['pages/Inbox.tsx', 'features/anomalies/AnomaliesPage.tsx']) {
      const src = FILES.find(f => f.p === file)?.src
        ?? stripComments(readFileSync(join(SRC, file), 'utf8'));
      expect(src, `${file}: gizlenen kap id="content"a geri dönmüş — çift id`)
        .not.toMatch(/id="content"\s+style=\{problemParam/);
      expect(src, `${file}: .page-body kabı kayboldu`)
        .toMatch(/className="page-body"\s+style=\{problemParam/);
    }
  });

  // `.page-body` yalnız bir sınıf adı değil, `#content`in stil ikizi
  // olmak ZORUNDA — aksi hâlde liste kabı padding/scroll kaybeder.
  it('.page-body globals.css\'te #content ile aynı kuralı paylaşıyor', () => {
    const css = readFileSync(join(SRC, 'styles/globals.css'), 'utf8');
    expect(css).toMatch(/#content, \.page-body \{[^}]*flex: 1[^}]*overflow: auto/);
    for (const d of ['spacious', 'compact', 'dense']) {
      expect(css, `[data-density="${d}"] .page-body padding kuralı eksik`)
        .toContain(`[data-density="${d}"] .page-body`);
    }
  });

  // PageControls `--controls-h`i host'a yazıyor. `.page-body` host
  // listesinden düşerse /inbox ve /problems'ta yapışkan tablo başlığı
  // `top: 0`a düşüp filtre barının ALTINA gizlenir (v0.9.697 sınıfı).
  it('PageControls host araması .page-body\'yi de kabul ediyor', () => {
    const pc = readFileSync(join(SRC, 'components/ui/PageControls.tsx'), 'utf8');
    expect(pc).toContain("closest('#content, .page-body')");
  });
});
