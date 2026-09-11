import type { LogRow, SpanRow, TraceBundleResponse } from '@/lib/types';

// kioskModel — v0.10.675 (trace kiosk modu Dilim 4): TraceKiosk'un SAF
// çekirdeği; tablo testi kioskModel.test.ts.

export const KIOSK_LOG_LIMIT_DEFAULT = 500;
export const KIOSK_LOG_LIMIT_MAX = 1000; // sunucu tavanı (trace_bundle.go traceBundleLogMax)

// pickRootSpan — analysis.rootSpanId (sunucu, v0.10.275) öncelikli; yoksa
// parent'sız ilk span (Trace.tsx:397 kuralı); o da yoksa ilk span.
export function pickRootSpan(
  spans: SpanRow[],
  analysis: { rootSpanId?: string } | undefined,
): SpanRow | undefined {
  if (analysis?.rootSpanId) {
    const r = spans.find(s => s.spanId === analysis.rootSpanId);
    if (r) return r;
  }
  return spans.find(s => !s.parentSpanId) ?? spans[0];
}

export interface KioskLogsState {
  // TraceLogsPanel sözleşmesi: undefined = yükleniyor, null = hata.
  logs: LogRow[] | null | undefined;
  degraded: string | null;
  logsTotal?: number;
  oracleRows: LogRow[];
  oracleError: boolean;
}

// bundleLogsState — bundle'ın log + Oracle slotlarını panel durumuna çevirir.
// Degraded slot BOŞ LİSTE + neden: sekme "hiç log yok" diye YANLIŞ teşhis
// koymasın (v0.8.332 sözleşmesi), event satırları yine listelenir.
export function bundleLogsState(b: TraceBundleResponse | undefined, isError: boolean): KioskLogsState {
  if (isError) return { logs: null, degraded: null, oracleRows: [], oracleError: false };
  if (!b) return { logs: undefined, degraded: null, oracleRows: [], oracleError: false };
  const degraded = b.logs?.degraded ? (b.logs.reason || 'log backend slow/unreachable') : null;
  return {
    logs: b.logs?.logs ?? [],
    degraded,
    logsTotal: b.logs?.total,
    oracleRows: b.oracle?.logs ?? [],
    oracleError: !!b.oracle?.degraded,
  };
}

// toggleSpanSelection — v0.10.685 (operatör: "tekrar üzerine basınca
// kapanmıyor"): aynı satıra ikinci tık seçimi kaldırır (Tempo davranışı).
export function toggleSpanSelection(prev: string | null, id: string): string | null {
  return prev === id ? null : id;
}

// formatAttrValue — v0.10.686: Tempo gösterimi — düz ondalık sayılar
// tırnaksız ve "numeric" (UI mavi), her şey tırnaklı dizge. Yalnız düz
// ondalık (1e3, 0x10 dizge kalır): amaç port/sayaç gibi alanları ayırt
// etmek, tip çıkarımı değil.
export function formatAttrValue(v: string): { text: string; numeric: boolean } {
  if (/^-?\d+(\.\d+)?$/.test(v)) return { text: v, numeric: true };
  return { text: '"' + v + '"', numeric: false };
}
