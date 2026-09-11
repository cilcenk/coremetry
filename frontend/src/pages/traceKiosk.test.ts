import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// v0.10.675 — TraceKiosk kaynak pini (audit §2.4 yol B): kiosk sayfası
// kromu (Topbar, AI, dış link, paylaşım, SpanDetail) İTHAL ETMEZ, veriyi
// TEK istekte (useTraceBundle) alır — ayrı trace/log/oracle çağrısı yok;
// Trace.tsx yalnız ?kiosk=1 ile dallanır ve TraceLogsPanel artık tek
// yerde (pages/trace/) yaşar.
const kiosk = readFileSync(resolve(__dirname, 'TraceKiosk.tsx'), 'utf8');
const trace = readFileSync(resolve(__dirname, 'Trace.tsx'), 'utf8');
const panel = readFileSync(resolve(__dirname, 'trace/TraceLogsPanel.tsx'), 'utf8');

describe('TraceKiosk (v0.10.675)', () => {
  it('krom bileşenlerini ithal etmez', () => {
    for (const bad of ["from '@/components/Topbar'", 'AIExplainButton', 'ExternalLinkButtons', 'SharePopover', "from '@/components/SpanDetail'", 'EventSource']) {
      expect(kiosk).not.toContain(bad);
    }
  });
  it('veriyi tek istekte alır (bundle); ayrı trace/log/oracle çağrısı yok', () => {
    expect(kiosk).toContain('useTraceBundle(');
    for (const bad of ['api.trace(', 'useCorrelatedLogs(', 'useOracleTraceLogs(', 'api.logs(']) {
      expect(kiosk).not.toContain(bad);
    }
  });
  it('/logs bağlantısı üreticiden (logsHref), ham yol yazılmaz', () => {
    expect(kiosk).toContain('logsHref({');
  });
});

describe('BAĞLANMA', () => {
  it('Trace.tsx varsayılan export ?kiosk=1 ile TraceKiosk\'a dallanır', () => {
    expect(trace).toContain("sp.get('kiosk') === '1'");
    expect(trace).toContain('{kiosk ? <TraceKiosk /> : <TraceDetailInner />}');
  });
  it('TraceLogsPanel tek yerde: pages/trace/ export eder, Trace.tsx tanımlamaz', () => {
    expect(panel).toContain('export function TraceLogsPanel(');
    expect(trace).not.toContain('function TraceLogsPanel(');
    expect(trace).toContain("import { TraceLogsPanel } from './trace/TraceLogsPanel'");
  });
});
