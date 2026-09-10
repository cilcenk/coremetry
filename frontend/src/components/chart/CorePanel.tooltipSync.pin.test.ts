// v0.10.585 — senkron gruptaki panellerde tooltip YALNIZ kaynak panelde.
//
// Operatör (topic sayfası, istemci sekmesi): fareyi bir grafiğe getirince
// altı panelin altısı kendi kutusunu çiziyordu — uPlot cursor.sync her
// kardeşe setCursor gönderir ve kanca kutuyu koşulsuz kuruyordu. Bu pin,
// uzak-imleç guard'ının (1) var olduğunu, (2) satırlar kurulmadan ÖNCE
// geldiğini, (3) pin guard'ının ondan ÖNCE kaldığını (sabitlenmiş kutu
// donuk kalır) çiviler. Yorumlar süzülür: iddialar koda çapalı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const SRC = readFileSync(new URL('./CorePanel.tsx', import.meta.url), 'utf8');
const CODE = SRC.replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '');

describe('CorePanel tooltip — senkron kardeşte çizilmez', () => {
  const guard = /const realHover = \(u\.cursor as \{ event\?: unknown \}\)\.event != null;\s*if \(!realHover\) \{\s*tt\.style\.display = 'none';\s*return;\s*\}/;
  it('uzak imleç guard\'ı var', () => {
    expect(CODE).toMatch(guard);
  });
  it('guard, tooltip satırları kurulmadan ÖNCE', () => {
    const g = CODE.search(guard);
    const rows = CODE.indexOf('capTooltipRows(sortedTooltipRows(');
    expect(g).toBeGreaterThan(-1);
    expect(rows).toBeGreaterThan(g);
  });
  it('pin guard\'ı uzak-imleç guard\'ından ÖNCE — sabitlenmiş kutu kaybolmaz', () => {
    const pin = CODE.indexOf('if (pinRef.current != null) return;');
    expect(pin).toBeGreaterThan(-1);
    expect(CODE.search(guard)).toBeGreaterThan(pin);
  });
});
