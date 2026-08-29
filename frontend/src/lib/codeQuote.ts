// codeQuote.ts — v0.10.165: CoSRE kod alıntısının saf çözümleyicileri
// (tasarım etüdü «cevap paneli» seçenek A, dilim 1 — sıfır backend).
// Sunucu kod penceresini GERÇEK satır numaralarıyla (`246| …`) ve hata
// satırını `>>>` ile verir (internal/devops/code.go, prompts.go
// systemCodeAddendum); ilk satır `// <yol>:<başlangıç>-<bitiş>`. Bunlar
// bugüne dek düz metindi — burada oluk, dosya başlığı ve stack katlaması
// olur. Sözleşme codeQuote.test.ts'te pinli:
//   - oluk yalnız TÜM boş-olmayan satırlar `N|` taşıyorsa (küçük model
//     önekleri bozarsa blok düz kalır; numara UYDURULMAZ),
//   - `>>>` işareti soyulur, satır vurgulanır (codeLineMark ile aynı),
//   - dosya başlığı `// yol:a-b` (# / -- / /* */ de olur; `yol:N` tek
//     satır ve `(insertBatch)` gibi ek de kabul) gövdeden düşer,
//   - başlık çözülemeyen ama gövdesi oluklu blokta ilk yorum satırı DÜZ
//     başlık olarak şeride çıkar, oluk gövdede kalır (splitHeader —
//     inceleme #1: tek yabancı satır bütün oluğu öldürüyordu; v0.10.154
//     gerçek yakalaması `// Handler.java:32` bu sınıftı),
//   - *Mapper*.xml → «kaynak penceresi (mapper)», öteki .xml → (XML),
//     .sql → (SQL) (uzantı sezgisi; files[].resource DTO alanı dilim 2),
//   - stack trace: ilk N kare + kalanı katlı; N en derin UYGULAMA
//     karesini kapsar (framework kareleri atlanır), 8..24.

export interface FileRef { path: string; from: number; to: number }
export interface GutterLine { no: number | null; text: string; hl: boolean }

const MARK_RE = /^(\s*)>>>\s?(.*)$/;
const GUTTER_RE = /^\s*(\d{1,6})\|\s?(.*)$/;
const FILE_HDR_RE = /^\s*(?:\/\/|#|--|\/\*)\s*([^\s:*]+\.[A-Za-z0-9]{1,8}):(\d+)(?:-(\d+))?\s*(?:\([^)]*\))?\s*(?:\*\/)?\s*$/;
const COMMENT_LINE_RE = /^\s*(?:\/\/|#|--|\/\*)\s*(.*?)\s*(?:\*\/)?\s*$/;

/** `>>>` işaretini soyar; vurgu bayrağı (Markdown.tsx codeLineMark ile aynı sözleşme). */
export function stripMarker(l: string): { text: string; hl: boolean } {
  const m = l.match(MARK_RE);
  return m ? { text: m[1] + m[2], hl: true } : { text: l, hl: false };
}

export function parseFileHeader(line: string | undefined): FileRef | null {
  if (!line) return null;
  const m = line.match(FILE_HDR_RE);
  if (!m) return null;
  const from = Number(m[2]), to = m[3] === undefined ? from : Number(m[3]);
  if (!(from > 0) || !(to >= from)) return null;
  return { path: m[1], from, to };
}

export interface SplitHeader { ref: FileRef | null; headerText: string | null; body: string[] }

/**
 * İlk satırı başlık olarak ayırır: çözülen `// yol:a-b` → ref; çözülmeyen
 * ama yorum-biçimli ilk satır + oluklu gövde → düz başlık metni (oluk
 * korunur). Hiçbiri değilse gövde aynen.
 */
export function splitHeader(lines: string[]): SplitHeader {
  const ref = parseFileHeader(lines[0]);
  if (ref) return { ref, headerText: null, body: lines.slice(1) };
  const m = lines[0]?.match(COMMENT_LINE_RE);
  if (m && m[1] && lines.length > 2 && hasGutter(lines.slice(1))) return { ref: null, headerText: m[1], body: lines.slice(1) };
  return { ref: null, headerText: null, body: lines };
}

/** Blok oluklu mu — boş olmayan HER satır (işaret soyulmuş) `N|` taşır ve en az 2 satır var. */
export function hasGutter(lines: string[]): boolean {
  let n = 0;
  for (const raw of lines) {
    const l = stripMarker(raw).text;
    if (l.trim() === '') continue;
    if (!GUTTER_RE.test(l)) return false;
    n++;
  }
  return n >= 2;
}

export function gutterLines(lines: string[]): GutterLine[] {
  return lines.map(raw => {
    const { text, hl } = stripMarker(raw);
    const m = text.match(GUTTER_RE);
    return m ? { no: Number(m[1]), text: m[2], hl } : { no: null, text, hl };
  });
}

/** Uzantı sezgisi: mapper XML / SQL pencereleri «hata satırı» taşımaz, kaynak penceresidir. */
export function resourceLabel(path: string | undefined): string | null {
  if (!path) return null;
  if (/\.xml$/i.test(path)) return /mapper[^/\\]*\.xml$/i.test(path) ? 'kaynak penceresi (mapper)' : 'kaynak penceresi (XML)';
  if (/\.sql$/i.test(path)) return 'kaynak penceresi (SQL)';
  return null;
}

const FRAME_RE = /^\s*(at\s|Caused by:|Suppressed:|\.\.\.\s*\d+\s+more)/;
const FRAMEWORK_RE = /^\s*at\s+(java\.|javax\.|jakarta\.|jdk\.|sun\.|com\.sun\.|org\.springframework|org\.apache|org\.hibernate|org\.postgresql|org\.jboss|org\.wildfly|io\.undertow|io\.netty|io\.grpc|okhttp3|reactor\.|kotlin\.|scala\.|net\.sf\.|ch\.qos|org\.slf4j|org\.eclipse|com\.zaxxer|io\.opentelemetry)/;

/** Stack trace bloğu mu — en az 6 kare/zincir satırı. */
export function isStackTrace(lines: string[]): boolean {
  let n = 0;
  for (const l of lines) if (FRAME_RE.test(l)) n++;
  return n >= 6;
}

/**
 * Katlama: baş = ilk max(min, en derin uygulama karesi+1) satır (üst sınır
 * max); kalan katlı. Katlamaya değmeyecek kadar kısa (kalan ≤ 3) → hepsi.
 */
export function foldStack(lines: string[], min = 8, max = 24): { head: string[]; rest: string[] } {
  let lastApp = -1;
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    if (/^\s*at\s/.test(l) && !FRAMEWORK_RE.test(l)) lastApp = i;
  }
  const n = Math.min(lines.length, Math.min(max, Math.max(min, lastApp + 1)));
  if (lines.length - n <= 3) return { head: lines, rest: [] };
  return { head: lines.slice(0, n), rest: lines.slice(n) };
}
