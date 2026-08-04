import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.9.641 — operatör-bildirimli: "neden kök neden ipucu veya öncelikli
// inceleme başlıkları bold yazmıyor".
//
// Model markdown üretiyor (**Kök Neden İpucu:**) ama CopilotExplain
// {text}'i HAM basıyordu — yıldızlar ekranda görünüyordu. Oysa
// RenderedMarkdown v0.7.0'dan beri VAR ve tam bu işi yapıyor; yalnız
// Runbook sayfaları kullanıyordu. Bileşen var, AI yüzeyi ondan
// habersizdi.

function read(rel: string) {
  return readFileSync(resolve(__dirname, rel), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')            // blok yorumlar
    .split('\n').map(l => l.replace(/\/\/.*$/, '')).join('\n'); // satır yorumları
}

describe('yorum sıyırma', () => {
  it('açıklamadaki alıntıyı koda saymaz', () => {
    const src = read('./CopilotExplain.tsx');
    // Bu dosyanın YORUMU "{text}" dizgisini alıntılıyor; sıyrılmazsa
    // aşağıdaki test kendi açıklamasıyla eşleşip hep kırmızı kalırdı.
    expect(src).not.toContain('HAM');
  });
});

describe('AI açıklama yüzeyi markdown basıyor', () => {
  const src = read('./CopilotExplain.tsx');

  it('RenderedMarkdown kullanıyor', () => {
    expect(src).toContain('<RenderedMarkdown text={text} />');
  });

  it('ham {text} bırakılmadı', () => {
    // Tek başına bir satırda çıplak {text} — ham basımın imzası.
    expect(src).not.toMatch(/^\s*\{text\}\s*$/m);
  });

  // pre-wrap + Markdown birlikte satır aralarını ikiye katlıyor:
  // Markdown zaten <p>/<ul>/<h*> üretiyor.
  it('metin kutusunda pre-wrap kalmadı', () => {
    const i = src.indexOf('maxWidth:');
    expect(src.slice(Math.max(0, i - 300), i)).not.toContain('pre-wrap');
  });
});

describe('RenderedMarkdown sözleşmesi', () => {
  const md = read('./Markdown.tsx');
  it('bold desteği duruyor', () => {
    expect(md).toMatch(/renderInline/);
    // Gerçek yayıcı <b> — "strong" aramak sözleşmeyi değil kelimeyi
    // çivilerdi.
    expect(md).toContain('<b key=');
  });
});
