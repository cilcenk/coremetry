import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// aiCodeParam.scan.test.ts — v0.10.81'in KAYNAK yarısı. Ayrı dosya,
// bilerek: davranış testleri jsdom istiyor, kaynak taramaları ise node
// (jsdom'da import.meta.url http şemalı olur ve readFileSync reddeder).

describe('aicode kablolaması kaynakta', () => {
  // ⚠ TOHUM TEMBEL BAŞLATICIDA OLMALI. Auto-koşu efekti mount'ta atıyor;
  // sonradan bir useEffect ile düzeltmek "kodsuz istek çoktan yola çıktı"
  // demek — dosyanın kendi v0.9.1238 dersi. Kaynak taraması bunu pinliyor.
  it('CopilotExplain tohumu URL\'den ve TEMBEL başlatıcıda', () => {
    const src = readFileSync(new URL('./CopilotExplain.tsx', import.meta.url), 'utf8');
    expect(src).toContain('useState(() => codeCapable && readAiCodeParam())');
    expect(src).toContain('writeAiCodeParam(next)');
  });

  // Uygulama içi açılış paramı SİLER — bir öznede işaretlenen kutu
  // sonrakine sızarsa v0.10.60 arka kapıdan geri döner.
  it('useAiSubject.setSubject aicode paramını siliyor', () => {
    const src = readFileSync(new URL('./ai/useAiSubject.ts', import.meta.url), 'utf8');
    expect(src).toContain('next.delete(AI_CODE_PARAM)');
  });
});
