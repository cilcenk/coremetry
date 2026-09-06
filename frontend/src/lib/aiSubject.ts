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
//   charts          → "charts:<enc(service)>:<fromNs>:<toNs>:<scope>"
//
// id HER ZAMAN encodeURIComponent'ten geçer: servis adı / fingerprint
// içinde ':' geçse bile ayraç belirsizleşmez (parse ':' üzerinden böler).

export const AI_PARAM = 'ai';

// AI_CODE_PARAM (v0.10.81, operatör-bildirimli): "Kodu da incele"
// seçimi URL'de yaşar. Paylaşılan bir Explain linki kodsuz açılıyordu
// ve alıcı kutuya yeniden basmak zorunda kalıyordu — ikinci bir yerel
// LLM turu. URL-tek-gerçek-kaynak ailesinin (v0.8.256/265/267,
// v0.10.76) çekmece üyesi.
//
// ⚠ v0.10.60 kararıyla ÇELİŞMEZ, birleşir: kutu uygulama İÇİNDE her
// açılışta kapalı başlar (gizli kalıcılık yok — useAiSubject.setSubject
// her kullanıcı-açılışında paramı SİLER); param yalnız paylaşılan /
// yapıştırılan linkte ve sayfa yenilemede hayatta kalır. Yani varsayılan
// kapalı kalır, paylaşım ise gördüğünü aynen taşır.
export const AI_CODE_PARAM = 'aicode';

// readAiCodeParam — CANLI adres çubuğundan okur. Router'a bağlanmıyor,
// bilerek: CopilotExplain router'sız da mount ediliyor (testler) ve bu
// çekmece ailesi zaten ham history API'siyle konuşuyor (useAiSubject'in
// "canlı adres çubuğu üst kümedir" gerekçesi).
export function readAiCodeParam(): boolean {
  if (typeof window === 'undefined') return false;
  return new URLSearchParams(window.location.search).get(AI_CODE_PARAM) === '1';
}

// writeAiCodeParam — seçimi adrese yazar/siler. Ham replaceState:
// /trace ve Dashboard emsali; yabancı parametreler AYNEN korunur ve
// history'ye durak eklenmez.
export function writeAiCodeParam(on: boolean): void {
  if (typeof window === 'undefined') return;
  const url = new URL(window.location.href);
  if (on) url.searchParams.set(AI_CODE_PARAM, '1');
  else url.searchParams.delete(AI_CODE_PARAM);
  window.history.replaceState(window.history.state, '', url.toString());
}

// AI_SRC_PARAM (v0.10.432, CoSRE router boşlukları D8) — çekmecenin
// hangi AFFORDANS'tan açıldığı: "nudge" = trace ilk açılış baloncuğu.
// Explain isteğine `?src=` olarak taşınır, sunucu yüzey etiketini
// "explain-trace:nudge" yapar (/ai ayrı sayar). Beyaz listeli: bilinmeyen
// değer HİÇ gönderilmez. aicode gibi yalnız o açılışta yaşar — useAiSubject
// her özne değişimi/kapanışta siler.
export const AI_SRC_PARAM = 'aisrc';
export const AI_SRC_VALUES = ['nudge', 'chat'] as const; // chat (v0.10.460) — sohbetten açılan Explain
export type AISrc = typeof AI_SRC_VALUES[number];

export function parseAiSrc(raw: string | null | undefined): AISrc | null {
  return raw && (AI_SRC_VALUES as readonly string[]).includes(raw) ? (raw as AISrc) : null;
}

// readAiSrcParam — readAiCodeParam'ın ikizi: CANLI adres çubuğundan.
export function readAiSrcParam(): AISrc | null {
  if (typeof window === 'undefined') return null;
  return parseAiSrc(new URLSearchParams(window.location.search).get(AI_SRC_PARAM));
}

export const AI_KINDS = [
  'trace', 'span', 'problem', 'incident', 'anomaly',
  'service-health', 'runbook', 'exception', 'charts',
] as const;

export type AIKind = typeof AI_KINDS[number];

// Grafik kapsamı (v0.9.1033, onaylı ServiceCharts AI mockup'ı):
// Ⓐ toolbar düğmesi 'all', Ⓑ kart başlığındaki ✨ tek kartın kapsamı.
// Değerler backend'in normalizeChartScope'u ile BİREBİR aynı
// (internal/api/explain_service_charts.go) — ayrışırlarsa çekmecedeki
// çip ile modelin odaklandığı grafik farklı olur.
export const CHART_SCOPES = ['all', 'rps', 'err', 'dur'] as const;
export type ChartScope = typeof CHART_SCOPES[number];

const CHART_SCOPE_SET = new Set<string>(CHART_SCOPES);

// Kapsam etiketleri ServiceCharts'ın kart başlıklarıyla aynı sözcükler:
// çekmecedeki çip, operatörün az önce tıkladığı kartın adını göstermeli.
export function chartScopeLabel(s: ChartScope): string {
  switch (s) {
    case 'rps': return 'RPS by operation';
    case 'err': return 'Error rate by operation';
    case 'dur': return 'P99 latency by operation';
    default:    return 'Tüm RED grafikleri';
  }
}

// Basit özneler: tek bir id yeter (backend geri kalanını kendi toplar).
type SimpleKind = Exclude<AIKind, 'span' | 'service-health' | 'charts'>;

export type AISubject =
  | { kind: SimpleKind; id: string }
  // span → id = traceId, spanId = trace içindeki hedef span (v0.5.144).
  | { kind: 'span'; id: string; spanId: string }
  // service-health → id = servis adı; prompt CANLI RED serisini istediği
  // için pencere de linkte taşınır (aksi halde paylaşılan link başka bir
  // pencereyi açıklar).
  | { kind: 'service-health'; id: string; fromNs: number; toNs: number }
  // charts → id = servis adı; pencere linkte taşınır (service-health ile
  // aynı gerekçe) + hangi kartın sorulduğu.
  | { kind: 'charts'; id: string; fromNs: number; toNs: number; scope: ChartScope };

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
  if (s.kind === 'charts') return `${head}:${s.fromNs}:${s.toNs}:${s.scope}`;
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
  if (kind === 'service-health' || kind === 'charts') {
    // charts bir segment daha taşır (kapsam); pencere doğrulaması ORTAK.
    if (parts.length !== (kind === 'charts' ? 5 : 4)) return null;
    const fromNs = Number(parts[2]);
    const toNs = Number(parts[3]);
    // Pencere hem sonlu hem ARTAN olmalı; ters/sıfır pencere backend'e
    // anlamsız bir sorgu attırır, bunu URL katmanında kes.
    if (!Number.isFinite(fromNs) || !Number.isFinite(toNs)) return null;
    if (fromNs <= 0 || toNs <= fromNs) return null;
    if (kind === 'charts') {
      // Kapsam TANIMAZ isek reddetmiyoruz: elle düzenlenmiş bir linkte
      // çekmeceyi hiç açmamaktansa EN GENİŞ kapsamı açmak doğru davranış
      // (backend'in normalizeChartScope'u da aynısını yapar). Bu yüzden
      // parse GEVŞEK, format KANONİK: parse→format bilinmeyen kapsamı
      // 'all'a sabitler.
      const raw = parts[4] ?? '';
      const scope = (CHART_SCOPE_SET.has(raw) ? raw : 'all') as ChartScope;
      return { kind: 'charts', id, fromNs, toNs, scope };
    }
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
    // Mockup başlığı: çekmece bir GRAFİĞİ anlatıyor, servis sağlığını
    // triyaj etmiyor — iki yüzey aynı sayfada yaşadığı için ad ayrımı
    // operatörün hangi cevabı okuduğunu belirler.
    case 'charts':         return 'AI grafik özeti';
  }
}

// Başlığın altındaki ikinci satır: hangi nesne (kısaltılmış id / servis).
export function aiSubjectSubtitle(s: AISubject): string {
  if (s.kind === 'span') return `${short(s.id)} · span ${short(s.spanId)}`;
  if (s.kind === 'charts') return `${s.id} · ${chartScopeLabel(s.scope)}`;
  return s.kind === 'service-health' ? s.id : short(s.id);
}

function short(id: string): string {
  return id.length > 20 ? `${id.slice(0, 16)}…` : id;
}
