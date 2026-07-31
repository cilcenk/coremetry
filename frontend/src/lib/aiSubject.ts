// aiSubject — `?ai=<kind>:<id>[:<extra>]` URL kodeği (v0.9.477, onaylı
// AI-drawer mockup'ı). Yüzey-başına inline CopilotExplain panelleri TEK
// sağ-kenar çekmeceye taşındı; çekmecenin ne göstereceğini artık ADRES
// belirliyor — "URL = tek doğruluk kaynağı" ev kuralı (kopyalanan link
// aynı AI açıklamasını açar).
//
// Neden ayrı, saf bir modül: parse/format React'ten bağımsız test
// edilebilsin (vitest node ortamı). Emsal: lib/inboxUrl.ts (v0.8.291).
//
// Biçim:
//   trace | problem | incident | anomaly | runbook | exception
//       → "<kind>:<encodeURIComponent(id)>"
//   span            → "span:<enc(traceId)>:<enc(spanId)>"
//   service-health  → "service-health:<enc(service)>:<fromNs>:<toNs>"
//
// id HER ZAMAN encodeURIComponent'ten geçer: servis adı / fingerprint
// içinde ':' geçse bile ayraç belirsizleşmez (parse ':' üzerinden böler).

export const AI_PARAM = 'ai';

export const AI_KINDS = [
  'trace', 'span', 'problem', 'incident', 'anomaly',
  'service-health', 'runbook', 'exception',
] as const;

export type AIKind = typeof AI_KINDS[number];

// Basit özneler: tek bir id yeter (backend geri kalanını kendi toplar).
type SimpleKind = Exclude<AIKind, 'span' | 'service-health'>;

export type AISubject =
  | { kind: SimpleKind; id: string }
  // span → id = traceId, spanId = trace içindeki hedef span (v0.5.144).
  | { kind: 'span'; id: string; spanId: string }
  // service-health → id = servis adı; prompt CANLI RED serisini istediği
  // için pencere de linkte taşınır (aksi halde paylaşılan link başka bir
  // pencereyi açıklar).
  | { kind: 'service-health'; id: string; fromNs: number; toNs: number };

const KIND_SET = new Set<string>(AI_KINDS);

// Bozuk yüzdeli dizide decodeURIComponent atar; çekmece bir URL yüzünden
// asla patlamamalı → boş string = "geçersiz" (parse null döner).
function safeDecode(s: string): string {
  try { return decodeURIComponent(s); } catch { return ''; }
}

export function formatAiParam(s: AISubject): string {
  const head = `${s.kind}:${encodeURIComponent(s.id)}`;
  if (s.kind === 'span') return `${head}:${encodeURIComponent(s.spanId)}`;
  if (s.kind === 'service-health') return `${head}:${s.fromNs}:${s.toNs}`;
  return head;
}

export function parseAiParam(raw: string | null | undefined): AISubject | null {
  if (!raw) return null;
  const parts = raw.split(':');
  const kind = parts[0];
  if (!KIND_SET.has(kind)) return null;
  const id = safeDecode(parts[1] ?? '');
  if (!id) return null;

  if (kind === 'span') {
    if (parts.length !== 3) return null;
    const spanId = safeDecode(parts[2] ?? '');
    if (!spanId) return null;
    return { kind: 'span', id, spanId };
  }
  if (kind === 'service-health') {
    if (parts.length !== 4) return null;
    const fromNs = Number(parts[2]);
    const toNs = Number(parts[3]);
    // Pencere hem sonlu hem ARTAN olmalı; ters/sıfır pencere backend'e
    // anlamsız bir sorgu attırır, bunu URL katmanında kes.
    if (!Number.isFinite(fromNs) || !Number.isFinite(toNs)) return null;
    if (fromNs <= 0 || toNs <= fromNs) return null;
    return { kind: 'service-health', id, fromNs, toNs };
  }
  // Basit özneler fazladan segment taşımaz — taşıyorsa bozuk/elle
  // düzenlenmiş bir link demektir, sessizce yanlış özne açmaktansa reddet.
  if (parts.length !== 2) return null;
  return { kind: kind as SimpleKind, id };
}

// Çekmece başlığı — operatör hangi soruyu sorduğunu görsün.
export function aiSubjectTitle(s: AISubject): string {
  switch (s.kind) {
    case 'trace':          return 'Explain trace';
    case 'span':           return 'Explain span';
    case 'problem':        return 'Explain problem';
    case 'incident':       return 'Explain incident';
    case 'anomaly':        return 'Explain anomaly';
    case 'exception':      return 'Explain root cause';
    case 'runbook':        return 'Runbook AI';
    case 'service-health': return 'AI triage';
  }
}

// Başlığın altındaki ikinci satır: hangi nesne (kısaltılmış id / servis).
export function aiSubjectSubtitle(s: AISubject): string {
  if (s.kind === 'span') return `${short(s.id)} · span ${short(s.spanId)}`;
  return s.kind === 'service-health' ? s.id : short(s.id);
}

function short(id: string): string {
  return id.length > 20 ? `${id.slice(0, 16)}…` : id;
}
