// externalLinks.ts — v0.10.345: trace sayfası dış link şablonu render'ı (SAF).
//
// Sözleşme (internal/chstore/external_links.go başlığı):
//   {{attr.KEY}}          attribute (kök span önce, sonra öteki span'ler), URL-kodlu
//   {{attrTime.KEY:FMT}}  attribute içindeki yyyyMMddHHmmss → FMT (dd MM yyyy yy HH mm ss)
//   {{time:FMT}}          trace başlangıcı, TARAYICI YEREL SAATİ → FMT
//   {{endTime:FMT}}       trace BİTİŞİ (en geç span sonu), tarayıcı saati → FMT
//                         (v0.10.371 — operatör: log platformu dakika penceresi
//                         function_id'nin gömülü zamanından (istek üretimi) sonra
//                         biten trace'in loglarını kaçırıyordu)
//   {{traceId}} {{service}}
// Eksik/çözülemeyen değişken → `missing` dolar, url yok; düğme pasif ve
// sebebini söyler. Çözülen değerler encodeURIComponent ile kodlanır.

export interface ExternalLinkCtx {
  traceId: string;
  service: string;
  startMs: number;
  /** Trace bitişi (ms). Süre bilinmiyorsa startMs. */
  endMs: number;
  attrs: Record<string, string>;
}

const VAR_RE = /\{\{\s*([A-Za-z]+)(?:\.([A-Za-z0-9_.-]+))?(?::([A-Za-z]+))?\s*\}\}/g; // anahtarda ':' yok (biçim ayracı)
const ANCHOR_RE = /20\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])([01]\d|2[0-3])[0-5]\d[0-5]\d/;

function pad(n: number, w = 2): string { return String(n).padStart(w, '0'); }

/** FMT tokenları: dd MM yyyy yy HH mm ss (uzun token önce eşleşir). */
export function formatParts(p: { y: number; M: number; d: number; H: number; m: number; s: number }, fmt: string): string {
  return fmt.replace(/yyyy|yy|dd|MM|HH|mm|ss/g, tok => {
    switch (tok) {
      case 'yyyy': return pad(p.y, 4);
      case 'yy': return pad(p.y % 100);
      case 'dd': return pad(p.d);
      case 'MM': return pad(p.M);
      case 'HH': return pad(p.H);
      case 'mm': return pad(p.m);
      case 'ss': return pad(p.s);
    }
    return tok;
  });
}

/** Değerin içindeki ilk geçerli yyyyMMddHHmmss (dilim dönüşümü YOK — olduğu gibi). */
export function attrTimeParts(value: string): { y: number; M: number; d: number; H: number; m: number; s: number } | null {
  const m = ANCHOR_RE.exec(value);
  if (!m) return null;
  const t = m[0];
  return { y: +t.slice(0, 4), M: +t.slice(4, 6), d: +t.slice(6, 8), H: +t.slice(8, 10), m: +t.slice(10, 12), s: +t.slice(12, 14) };
}

function localParts(ms: number) {
  const dt = new Date(ms);
  return { y: dt.getFullYear(), M: dt.getMonth() + 1, d: dt.getDate(), H: dt.getHours(), m: dt.getMinutes(), s: dt.getSeconds() };
}

export function renderExternalLink(template: string, ctx: ExternalLinkCtx): { url?: string; missing: string[] } {
  const missing: string[] = [];
  const url = template.replace(VAR_RE, (_all, kind: string, key: string | undefined, fmt: string | undefined) => {
    switch (kind) {
      case 'attr': {
        const v = key ? ctx.attrs[key] : undefined;
        if (!v) { missing.push(key ?? 'attr'); return ''; }
        return encodeURIComponent(v);
      }
      case 'attrTime': {
        const v = key ? ctx.attrs[key] : undefined;
        const parts = v ? attrTimeParts(v) : null;
        if (!parts || !fmt) { missing.push(`${key ?? 'attr'} (zaman)`); return ''; }
        return encodeURIComponent(formatParts(parts, fmt));
      }
      case 'time':
        if (!fmt) { missing.push('time'); return ''; }
        return encodeURIComponent(formatParts(localParts(ctx.startMs), fmt));
      case 'endTime':
        if (!fmt) { missing.push('endTime'); return ''; }
        return encodeURIComponent(formatParts(localParts(ctx.endMs), fmt));
      case 'traceId': return encodeURIComponent(ctx.traceId);
      case 'service': return encodeURIComponent(ctx.service);
    }
    missing.push(kind);
    return '';
  });
  return missing.length ? { missing } : { url, missing };
}

/** Span'lerden attribute bağlamı: kök span (parentId boş) önce, sonra sırayla; ilk dolu değer kazanır. */
export function collectLinkCtx(spans: { traceId: string; serviceName: string; startTime: number; durationMs?: number; parentId?: string; attributes?: Record<string, string> }[]): ExternalLinkCtx | null {
  if (!spans.length) return null;
  const root = spans.find(s => !s.parentId || s.parentId === '0000000000000000') ?? spans[0];
  const ordered = [root, ...spans.filter(s => s !== root)];
  const attrs: Record<string, string> = {};
  for (const s of ordered) {
    for (const [k, v] of Object.entries(s.attributes ?? {})) {
      if (v !== '' && v != null && !(k in attrs)) attrs[k] = String(v);
    }
  }
  const startNs = Math.min(...spans.map(s => s.startTime));
  // Bitiş = en geç (start + durationMs); süre taşımayan span kendi başlangıcı.
  const endNs = Math.max(...spans.map(s => s.startTime + (s.durationMs ?? 0) * 1e6));
  return {
    traceId: root.traceId, service: root.serviceName,
    startMs: Math.round(startNs / 1e6), endMs: Math.round(Math.max(startNs, endNs) / 1e6), attrs,
  };
}
