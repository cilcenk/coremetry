import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.691 — operatör: "inline gösterim trace'te iyiymiş, Coremetry içindeki
// trace'lerde de yapalım". Trace sayfasında SpanDetail artık sağda yüzen panel
// değil, tıklanan satırın altında (TraceWaterfall renderDetail, kiosk 682
// deseni). Pin: renderDetail bağlı, eski kardeş render yok; inline kipte
// tutamaç ve sabit genişlik yok; CSS satır-içi kuralı var. PublicTrace
// (inline vermeyen) aynen.
const trace = readFileSync(resolve(__dirname, 'Trace.tsx'), 'utf8');
const detail = readFileSync(resolve(__dirname, '../components/SpanDetail.tsx'), 'utf8');
const css = readFileSync(resolve(__dirname, '../styles/globals.css'), 'utf8');

describe('Trace sayfası satır-içi span detayı (v0.10.691)', () => {
  it('SpanDetail renderDetail ile satırın altında; kardeş render yok', () => {
    expect(trace).toContain('renderDetail={id => (sel && sel.spanId === id ? (');
    expect(trace).toContain('<SpanDetail inline span={sel}');
    expect(trace).not.toContain('{sel && <SpanDetail');
  });
  it('inline kip: tutamaç yok, sabit genişlik yok', () => {
    expect(detail).toContain("className={inline ? 'span-panel-inline' : undefined} style={inline ? undefined : { width: panelW }}");
    expect(detail).toContain('{!inline && <div className="span-panel-resizer"');
    expect(detail).toContain('inline = false');
  });
  it('CSS: satır-içi panel statik, gövde çok sütun, geniş bölümler tam satır', () => {
    expect(css).toContain('#span-panel.span-panel-inline { position: static;');
    expect(css).toContain('.span-panel-inline #span-panel-body { padding: 0; display: grid;');
    expect(css).toContain('.span-panel-inline .ps-sec-wide { grid-column: 1 / -1; }');
  });
});
