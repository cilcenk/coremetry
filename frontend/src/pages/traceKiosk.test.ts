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
  // v0.10.681 — alt span paneli: seçili span → KioskSpanPanel, Esc kapatır.
  it('seçili span için alt panel + Esc katmanı', () => {
    expect(kiosk).toContain('<KioskSpanPanel');
    expect(kiosk).toContain('useEscLayer(!!selectedSpan, () => setSelectedId(null))');
  });
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
  // v0.10.679 — marka şeridi: özel logo varsa o, yoksa OTel işareti; ad Wordmark'tan.
  it('marka şeridi: useBranding + logo fallback + Wordmark', () => {
    expect(kiosk).toContain('const brand = useBranding();');
    expect(kiosk).toContain('brand.logoDataUri');
    expect(kiosk).toContain('<TelescopeIcon size={26} />');
    expect(kiosk).toContain('<Wordmark name={brand.appName} />');
  });
  // v0.10.680 — marka şeridinin sağında Logs bağlantısı: üreticiden href, yeni pencere.
  it('marka şeridi sağında Logs bağlantısı (logsHref, yeni pencere)', () => {
    expect(kiosk).toContain('className="trace-kiosk__brand-link" href={allLogsHref} target="_blank" rel="noopener noreferrer"');
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
