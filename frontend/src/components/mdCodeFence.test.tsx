import { describe, it, expect } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { RenderedMarkdown, dedent, codeLineMark, stripMarkdown } from './Markdown';

// mdCodeFence.test.tsx — v0.10.154, operatör-bildirimli (prod):
// "Kod bloğu gösterimi çok kötü, hiç profesyonel değil."
//
// Model kod alıntısını bir madde işaretinin ALTINA girintili çitle yazıyor
// (`  ```java` … `  ````); RenderedMarkdown çiti `startsWith('```')` ile
// arıyordu → girintili çit düz metne düşüyor, ekranda "```java" ve kapanış
// çiti satır olarak görünüyordu. Sözleşme: çit girintiden bağımsız tanınır,
// ortak girinti soyulur, blok ChatBubble'ın kod paneliyle AYNI sınıf
// ailesinde çizilir (cm-md-code: dil şeridi + pre), modelin `>>>` hata
// satırı vurgulanır (.hl), stripMarkdown girintili çiti de atar.

const SAMPLE = [
  '- **Kök Neden:** `svc` servisi hatayı fırlatmıştır.',
  '  ```java',
  '  // src/main/java/com/x/Handler.java:32',
  '  >>> 32| throw builder.build();',
  '      33| }',
  '  ```',
  'Hata BFF tarafında değil.',
].join('\n');

describe('girintili çitli kod bloğu (v0.10.154)', () => {
  const html = renderToStaticMarkup(<RenderedMarkdown text={SAMPLE} />);
  it('çit düz metin olarak GÖRÜNMEZ, kod paneli çizilir', () => {
    expect(html).not.toContain('```');
    expect(html).toContain('cm-md-code');
    expect(html).toContain('cm-md-code-lang');
    expect(html).toContain('java');
    expect(html).toContain('throw builder.build();');
  });
  it('ortak girinti soyulur, göreli girinti korunur', () => {
    expect(dedent(['  a', '    b', '', '  c'])).toEqual(['a', '  b', '', 'c']);
    expect(dedent(['x', '  y'])).toEqual(['x', '  y']);
    expect(dedent([])).toEqual([]);
  });
  it('`>>>` hata satırı vurgulanır, işaret ekranda kalmaz', () => {
    expect(codeLineMark('>>> 32| throw x;')).toEqual({ text: '32| throw x;', hl: true });
    expect(codeLineMark('    33| }')).toEqual({ text: '    33| }', hl: false });
    expect(html).toContain('cm-md-code-line hl');
    expect(html).not.toContain('&gt;&gt;&gt;');
  });
  it('bloktan sonraki paragraf normal akar', () => {
    expect(html).toContain('Hata BFF tarafında değil.');
  });
  it('stripMarkdown girintili çiti de atar (kart önizlemesi)', () => {
    const flat = stripMarkdown(SAMPLE);
    expect(flat).not.toContain('```');
    expect(flat).toContain('throw builder.build();');
  });
});
