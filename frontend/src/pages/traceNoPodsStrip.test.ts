// traceNoPodsStrip.test.ts — v0.10.163, operatör-bildirimli (prod, 16 pod'lu
// trace): "Trace'te çok fazla pod olduğunda üstte pod çok yer kaplıyor,
// gereksiz. Göstermeyelim; kullanıcı isterse span'lerden pod'lara gider."
//
// Sözleşme: Trace sayfası pod şeridi (TracePodsStrip, v0.10.137) ÇİZMEZ ve
// bileşen repoda yok; pod'a giden yol span detayındaki k8s attribute linkleri
// (v0.10.150) — o yol da burada pinli ki şerit gidince pivot sessizce
// kaybolmasın.
import { describe, it, expect } from 'vitest';
import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const SRC = resolve(__dirname, '..');
const read = (p: string) => readFileSync(resolve(SRC, p), 'utf8').replace(/^\s*\/\/.*$/gm, '').replace(/\{\/\*[\s\S]*?\*\/\}/g, '');

describe('Trace sayfası pod şeridi (v0.10.163)', () => {
  it('Trace.tsx TracePodsStrip çizmez; bileşen dosyası yok', () => {
    expect(read('pages/Trace.tsx')).not.toMatch(/TracePodsStrip/);
    expect(existsSync(resolve(SRC, 'components/TracePodsStrip.tsx'))).toBe(false);
  });
  it('pod pivotu span detayında yaşıyor: k8sAttrHref + SpanK8sSection', () => {
    const src = read('components/SpanDetail.tsx');
    expect(src).toMatch(/k8sAttrHref\(/);
    expect(src).toMatch(/<SpanK8sSection /);
  });
});
