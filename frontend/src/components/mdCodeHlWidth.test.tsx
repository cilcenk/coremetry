import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { renderToStaticMarkup } from 'react-dom/server';
import { RenderedMarkdown } from './Markdown';

// mdCodeHlWidth.test.tsx — v0.10.514, operatör-bildirimli (prod, CoSRE
// "Kod incelemesi"): "highlight ettiği satır uzun olunca CoSRE genişliğini
// geçince yarım kalıyor."
//
// Kök neden: .cm-md-code-line `display:block`; kaydırılan <pre> içinde bir
// blok, KONTEYNER genişliğini alır, kaydırma genişliğini değil. Uzun satırda
// vurgu zemini (+ sol uyarı çubuğu) konteynerin sağ kenarında bitiyor,
// sağa kaydırınca metin vurgusuz devam ediyordu.
//
// Çözüm: <code> blok + `width:max-content; min-width:100%` — <code> en uzun
// satır kadar genişler, blok satırlar onu doldurur, vurgu kaydırma
// genişliğinin tamamını kaplar. Kural DOM'da gerçekten eşleşmeli
// ([[feedback-tested-but-unreachable]]): render yarısı da pinli.

const css = readFileSync(new URL('../styles/globals.css', import.meta.url), 'utf8');

describe('vurgulu kod satırı kaydırma genişliğini kaplar', () => {
  it('CSS: .cm-md-code pre > code blok, max-content, min-width 100%', () => {
    const m = css.match(/\.cm-md-code pre > code \{([^}]*)\}/);
    expect(m, 'kural yok').toBeTruthy();
    expect(m![1]).toContain('display: block');
    expect(m![1]).toContain('width: max-content');
    expect(m![1]).toContain('min-width: 100%');
    // satır bloğu + vurgu sınıfı yerinde (seçici zinciri değişmedi)
    expect(css).toMatch(/\.cm-md-code-line \{ display: block; \}/);
    expect(css).toMatch(/\.cm-md-code-line\.hl \{/);
  });

  it('DOM: çit <pre><code> içinde blok satırlar; >>> satırı .hl taşır', () => {
    const long = 'executeRemoteNonTx("CREDITCARD_CCCORE_YNO_CARD_LIST_INQUIRY_BAG", paramBag); // ' + 'x'.repeat(120);
    const html = renderToStaticMarkup(<RenderedMarkdown text={'```java\nfoo();\n>>> ' + long + '\nbar();\n```'} />);
    expect(html).toMatch(/<pre><code>/);
    expect(html).toContain('class="cm-md-code-line hl"');
    expect(html).toContain('paramBag);');
  });
});
