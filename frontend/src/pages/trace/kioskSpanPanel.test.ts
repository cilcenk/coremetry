import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.681 — KioskSpanPanel kaynak pini: kromsuz (Link / AI / sayfa linki
// yok), EK İSTEK yok (api importu yok; loglar bundle'dan span_id ile süzülür),
// attribute grupları lib/spanAttrGroups'tan (ikinci gruplama yazımı olmasın).
const src = readFileSync(resolve(__dirname, 'KioskSpanPanel.tsx'), 'utf8');

describe('KioskSpanPanel (v0.10.681)', () => {
  it('krom ve fetch yok', () => {
    for (const bad of ["from 'react-router-dom'", 'AIExplainButton', "from '@/lib/api'", 'useQuery(', "from '@/components/SpanDetail'"]) {
      expect(src).not.toContain(bad);
    }
  });
  it('gruplama tek kaynaktan; loglar span_id ile süzülür', () => {
    expect(src).toContain("import { groupSpanAttrs, groupResourceAttrs } from '@/lib/spanAttrGroups'");
    expect(src).toContain('.filter(r => r.spanId === span.spanId)');
    expect(src).toContain('<LogTable logs={spanLogs} hideTraceColumn />');
  });
});
