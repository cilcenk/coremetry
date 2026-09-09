// traceEventLogs.ts (v0.8.407) — the zero-ES leg of trace↔log
// correlation. OTel span EVENTS (exceptions + log-bridge records) ride
// the spans we already loaded from ClickHouse, so the trace view can
// show "log-like" rows and per-span markers without a single extra
// Elasticsearch query. ES rows and event rows are complementary — a
// log that was BOTH bridged onto the span and shipped to ES appears
// twice, visibly distinguished by the `origin: 'span-event'` chip
// (dedup would need content hashing across sources; not worth the
// false-merge risk).

import type { LogRow, SpanRow } from './types';

// OTel severity-text → severity-number floor (log data model).
const SEV_NUM: Record<string, number> = {
  trace: 1, debug: 5, info: 9, warn: 13, warning: 13,
  error: 17, fatal: 21, critical: 21,
};

// eventSeverity — an `exception` event is an ERROR by definition; a
// log-bridge event carries its level in one of the common attribute
// spellings; anything else reads as INFO (it's an annotation, not a
// failure).
function eventSeverity(name: string, attrs: Record<string, string>): { num: number; text: string } {
  if (name === 'exception') return { num: 17, text: 'ERROR' };
  const raw = (attrs['log.severity'] ?? attrs['log.level'] ?? attrs['level'] ?? attrs['severity'] ?? '')
    .toLowerCase();
  const num = SEV_NUM[raw];
  if (num) return { num, text: raw.toUpperCase() === 'WARNING' ? 'WARN' : raw.toUpperCase() };
  return { num: 9, text: 'INFO' };
}

// eventBody — exceptions render "Type: message" (the line an operator
// would grep for); log-bridge events surface the message text; plain
// annotations fall back to the event name.
function eventBody(name: string, attrs: Record<string, string>): string {
  if (name === 'exception') {
    const t = attrs['exception.type'] || 'exception';
    const m = attrs['exception.message'] || '';
    return m ? `${t}: ${m}` : t;
  }
  return attrs['log.message'] || attrs['message'] || attrs['event.body'] || attrs['body'] || name;
}

// spanEventLogRows — flatten every span event in the trace into a
// pseudo LogRow the shared <LogTable> can render. IDs are negative
// sequence numbers: CH row keys (cityHash64) and ES ids are
// non-negative, so synthetic rows can never collide with a real one
// in React keys or dedup sets.
export function spanEventLogRows(spans: SpanRow[]): LogRow[] {
  const out: LogRow[] = [];
  let i = 0;
  for (const s of spans) {
    for (const ev of s.events ?? []) {
      i++;
      const attrs = ev.attributes ?? {};
      const sev = eventSeverity(ev.name, attrs);
      out.push({
        id: -i,
        timestamp: ev.timeNano,
        severity: sev.num,
        severityText: sev.text,
        body: eventBody(ev.name, attrs),
        serviceName: s.serviceName,
        traceId: s.traceId,
        spanId: s.spanId,
        attributes: attrs,
        resourceAttributes: {},
        origin: 'span-event',
      });
    }
  }
  return out;
}

export interface SpanLogSignal {
  n: number;    // total correlated rows (ES logs + span events)
  err: boolean; // any of them at ERROR or above
}

// perSpanLogSignals — spanId → correlated-row count for the waterfall
// chips. ES rows arrive only after the Logs tab has been opened once
// (react-query cache; the fetch stays lazy), event rows are always
// available — so the chips work immediately and get richer after the
// first tab visit, at zero extra backend cost.
export function perSpanLogSignals(
  esLogs: LogRow[] | undefined,
  eventRows: LogRow[],
): Map<string, SpanLogSignal> {
  const m = new Map<string, SpanLogSignal>();
  const add = (l: LogRow) => {
    if (!l.spanId) return;
    const cur = m.get(l.spanId) ?? { n: 0, err: false };
    cur.n++;
    if (l.severity >= 17) cur.err = true;
    m.set(l.spanId, cur);
  };
  for (const l of eventRows) add(l);
  for (const l of esLogs ?? []) add(l);
  return m;
}

// traceServicesWithoutTraceField — the D leg: given the services in
// this trace and the (5-min-cached) logstore trace-context coverage,
// name the services whose shipped logs carry NO trace field — the
// actionable reason the Logs tab is empty. A service absent from the
// coverage list has no logs at all in the sample window (a different,
// honest message).
export function traceServicesWithoutTraceField(
  traceServices: string[],
  coverage: { service: string; total: number; withTrace: number }[],
): string[] {
  const bad: string[] = [];
  const byName = new Map(coverage.map(c => [c.service, c]));
  for (const svc of [...new Set(traceServices)].sort()) {
    const c = byName.get(svc);
    if (c && c.total > 0 && c.withTrace === 0) bad.push(svc);
  }
  return bad;
}

// --- gRPC per-message event gürültüsü (v0.10.577) -------------------------

// OTel gRPC instrumentation'ı akıştaki HER mesaj için bir span event basar.
// Operatörün getirdiği trace'te bu 253 satırdı ve 11 gerçek log satırını
// görünmez yapıyordu. Teşhis değeri sıfır: hangi mesajın gönderildiğini
// değil, yalnız kaçıncı mesaj olduğunu söylüyorlar.
const GRPC_MSG_KEYS = new Set(['message.id', 'message.type']);
const GRPC_MSG_TYPES = new Set(['SENT', 'RECEIVED']);

// isGrpcMessageEvent — DÖRT koşul birden. Yüklem bilerek dar: burada yanlış
// pozitif, operatörün bir daha göremeyeceği bir teşhis satırı demek.
//
//   1. satır bir span event'i (ES log satırına ASLA dokunma)
//   2. gövde tam olarak "message" — yani eventBody'nin bütün gerçek-gövde
//      adayları boş çıkmış, geriye event adı kalmış
//   3. attribute anahtarları {message.id, message.type} kümesinin ALT KÜMESİ
//      — tek bir fazladan anahtar (rpc.grpc.status_code gibi) event'i
//      kurtarır, çünkü artık bir şey anlatıyordur
//   4. message.type SENT ya da RECEIVED (operatör 2026-09-09: "sadece grpc
//      sent and received"). Bilinmeyen bir tip KORUNUR.
//
// Exception event'leri 2 ve 3'ten geçemez: gövdeleri "Type: message"
// biçiminde ve exception.* attribute'ları taşırlar.
export function isGrpcMessageEvent(row: LogRow): boolean {
  if (row.origin !== 'span-event') return false;
  if (row.body !== 'message') return false;
  const attrs = row.attributes ?? {};
  for (const k of Object.keys(attrs)) {
    if (!GRPC_MSG_KEYS.has(k)) return false;
  }
  return GRPC_MSG_TYPES.has(attrs['message.type']);
}

// splitGrpcMessageEvents — Logs sekmesinin görünür listesi + gizlenen sayısı.
// Gizlenecek hiçbir şey yoksa GİRDİ DİZİSİNİN KENDİSİ döner: yeni bir dizi
// kimliği, aşağıdaki memo zincirini ve LogTable'ı boşuna yeniden render
// ettirirdi.
export function splitGrpcMessageEvents(rows: LogRow[]): { visible: LogRow[]; hidden: number } {
  let hidden = 0;
  for (const r of rows) if (isGrpcMessageEvent(r)) hidden++;
  if (hidden === 0) return { visible: rows, hidden: 0 };
  return { visible: rows.filter(r => !isGrpcMessageEvent(r)), hidden };
}
