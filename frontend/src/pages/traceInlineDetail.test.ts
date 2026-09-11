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
  it('CSS: satır-içi panel statik; gövde kiosk düzeni (grid serpme yok)', () => {
    expect(css).toContain('#span-panel.span-panel-inline { position: static;');
    expect(css).not.toContain('.span-panel-inline #span-panel-body { padding: 0; display: grid;');
    expect(css).toContain('.span-panel-inline .ps-kv td:first-child { width: 1%;');
  });
  // v0.10.692 — operatör: "kiosk'taki gösterim daha güzel; üç parça dağınık".
  it('SpanDetail gövdesi kiosk düzeni: künye satırı + iki sütun gruplu attribute', () => {
    expect(detail).toContain('className="kiosk-span__facts"');
    expect(detail).toContain('className="kiosk-span__cols"');
    expect(detail).toContain('Span attributes <span className="kiosk-span__cnt">{attrCount}</span>');
    expect(detail).toContain('Resource attributes <span className="kiosk-span__cnt">{resCount}</span>');
    expect(detail).toContain('groupResourceAttrs(span.resourceAttributes)');
    expect(detail).not.toContain('<Section title="Info">');
    expect(detail).not.toContain('title={`Resource (${res.length})`}');
  });
});
