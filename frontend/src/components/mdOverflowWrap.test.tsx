import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { renderToStaticMarkup } from 'react-dom/server';
import { RenderedMarkdown } from './Markdown';

// mdOverflowWrap.test.tsx — v0.10.79, operatör-bildirimli:
// "Burada kod method ismi çok uzun olunca kayıyor."
//
// AI cevabındaki uzun NİTELİKLİ adlar (`com.x.y.Uzun$Sınıf.metot`) tek
// token: tarayıcı kıramıyor, satır balonu YANA itiyor ve panel yatay
// kayıyordu — operatör metnin yarısını göremiyordu. Blok kodun kendi
// kaydırıcısı vardı (cm-md-code pre, overflow-x:auto); kusur SATIR İÇİ
// koddaydı ve CLAUDE.md'nin sessiz-kırpma ailesinin inline ikizi.
//
// Çözüm KÖKTE: overflow-wrap KALITSAL, tek sınıf (.cm-md-wrap) paragrafı,
// li'yi ve <code>u birden kapsıyor. İki renderer var ve İKİSİ de
// giydirilmek zorunda ([[feedback-fixes-have-second-halves]]).

const LONG = 'com.example.loan.interrogation.kkbkrsinq.service.KrsMusteriSkorService.KrsMusteriSkorExternalService';

describe('uzun nitelikli ad balonu itmez', () => {
  it('RenderedMarkdown kökü cm-md-wrap taşıyor (fragment değil)', () => {
    const html = renderToStaticMarkup(<RenderedMarkdown text={'kök neden `' + LONG + '` içinde'} />);
    expect(html).toContain('cm-md-wrap');
    expect(html).toContain(LONG); // içerik kaybolmadı
  });

  it('ChatBubble balonu düğümsüz sarıyor — ikinci renderer', () => {
    // ⚠ İlk düzeltme mesajı bir <div>e sarmıştı ve akış imlecini alt
    // satıra attı — ChatBubble.render.test.tsx yakaladı (imleç satır içi
    // SPAN'a yapışık olmalı). Sarma bu yüzden YENİ düğümle değil,
    // balonun MEVCUT stiline eklendi.
    const src = readFileSync(new URL('./ai/ChatBubble.tsx', import.meta.url), 'utf8');
    expect(src).toContain("overflowWrap: 'anywhere'");
    expect(src).not.toContain('className="cm-md-wrap">{renderMessage(');
  });

  it('sınıf CSS\'te sarma kuralını gerçekten tanımlıyor', () => {
    const css = readFileSync(new URL('../styles/globals.css', import.meta.url), 'utf8');
    const m = css.match(/\.cm-md-wrap \{([^}]*)\}/);
    expect(m, '.cm-md-wrap globals.css\'te yok').toBeTruthy();
    expect(m![1]).toContain('overflow-wrap: anywhere');
  });

  it('blok kod sözleşmesi BOZULMADI: pre hâlâ pre + kendi kaydırıcısı', () => {
    // overflow-wrap köke gelince blok kodun "uzun SQL satırı balonu
    // genişletmesin, KENDİ içinde kaysın" sözleşmesi durmalı.
    const css = readFileSync(new URL('../styles/globals.css', import.meta.url), 'utf8');
    const pre = css.match(/\.cm-md-code pre \{([^}]*)\}/);
    expect(pre).toBeTruthy();
    expect(pre![1]).toContain('overflow-x: auto');
    expect(pre![1]).toContain('white-space: pre');
  });
});
