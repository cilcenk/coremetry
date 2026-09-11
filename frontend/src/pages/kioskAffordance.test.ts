import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.676 — kiosk eylemleri (audit §10 F11): trace listesi (NAME hücresi)
// ve trace detayı (eylem şeridi) kiosk URL'sini ÜRETİCİDEN (traceHref,
// kiosk:true) alır ve `noopener,noreferrer` ile yeni pencerede açar. Ham `/trace?`
// yazımı traceLogsLinkGate'in konusu; burası kiosk bayrağının iki yüzeyde
// de üreticiden geçtiğini pinler.
const traces = readFileSync(resolve(__dirname, 'Traces.tsx'), 'utf8');
const trace = readFileSync(resolve(__dirname, 'Trace.tsx'), 'utf8');

describe('kiosk eylemleri (v0.10.676)', () => {
  it('Traces satırı: traceHref(kiosk:true) + noopener, Link dışında IconButton', () => {
    expect(traces).toContain('traceHref(t.traceId, { kiosk: true, pageRange: range })');
    expect(traces).toContain("'_blank', 'noopener,noreferrer'");
    expect(traces).toContain('className="row-kiosk"');
  });
  it('Trace detayı: traceHref(kiosk:true, span) + noopener', () => {
    expect(trace).toContain('traceHref(id, { kiosk: true, span: selectedId, pageRange: range })');
    expect(trace).toContain('⧉ Kiosk');
  });
});
