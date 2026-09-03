// Pod.annotationLane.test.ts — v0.10.312 (chart-layer audit Dilim 2.1):
// annotation şeridi /pod'da RED yığınının ALTINDA ve Service.tsx ile aynı
// sözleşmeyle (ns pencere + sayfa zoom yığını) monte. Kaynak pini: şerit
// kaldırılır ya da Overview linkinin üstüne kayarsa kırmızı.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const src = readFileSync(resolve(__dirname, 'Pod.tsx'), 'utf8');
const svc = readFileSync(resolve(__dirname, 'Service.tsx'), 'utf8');

describe('Pod annotation şeridi', () => {
  it('RED yığınının altında, ns pencere + handleZoom ile monte', () => {
    const mount = '<ServiceAnnotationLane service={service} fromNs={from} toNs={to} onZoomTo={handleZoom} />';
    const i = src.indexOf(mount);
    expect(i).toBeGreaterThan(-1);
    expect(i).toBeGreaterThan(src.indexOf('storageKey="pod-response-time"'));
    expect(i).toBeLessThan(src.indexOf('Bu pod bir Coremetry servisine eşlenmedi'));
    expect(src.split('<ServiceAnnotationLane ').length - 1).toBe(1);
  });
  it('Service.tsx ile aynı sözleşme: onZoomTo sayfa zoom yığınına gider', () => {
    expect(svc).toContain('<ServiceAnnotationLane service={svc} fromNs={rangeNs.from} toNs={rangeNs.to}');
    expect(svc).toMatch(/<ServiceAnnotationLane[^>]*\n?[^>]*onZoomTo=\{handleZoom\}/);
    expect(src).toContain("usePageZoomRange('1h')");
  });
});
