// v0.10.416 — log arama denetimi B2: canlı kuyruk (SSE) trace kilidini
// taşır. Kaynak pini: iki p.set + efekt bağımlılıkları (kilit
// değişince EventSource yeniden açılmalı — eslint exhaustive-deps kapalı,
// linter yakalamaz).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

describe('live tail carries the trace lock (B2)', () => {
  const src = readFileSync(resolve(__dirname, './Logs.tsx'), 'utf8');
  const i = src.indexOf("new EventSource('/api/logs/stream?'");
  const block = src.slice(Math.max(0, i - 1200), i);

  it('SSE URL traceId ve spanId taşır', () => {
    expect(block).toContain("p.set('traceId', filter.traceId)");
    expect(block).toContain("p.set('spanId', filter.spanId)");
  });

  it('efekt bağımlılıkları kilidi içerir', () => {
    const deps = src.slice(i).match(/\}, \[live, filter\.service[^\]]*\]\);/);
    expect(deps).not.toBeNull();
    expect(deps![0]).toContain('filter.traceId');
    expect(deps![0]).toContain('filter.spanId');
  });

  it('kilitli canlı kuyrukta boş durum "backend kaydı yok" demez', () => {
    expect(src).toContain('filter.traceId && live ? (');
  });
});
