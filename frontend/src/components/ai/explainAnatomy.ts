// explainAnatomy.ts — v0.10.165: CoSRE cevap kartının SABİT anatomisi için
// saf çözümleyiciler (etüt seçenek A «Yapılandırılmış kart», dilim 1):
//   - verdictLine: «**Kök Neden ve Sonraki Adım**» (trace/exception,
//     prompts.go:84/249) ya da düz `Olası neden: …` (problem, prompts.go:172;
//     kalın DEĞİL) bölümünün ilk cümlesi = Karar satırı; bölüm o cümleden
//     ibaretse Karar ÇİZİLMEZ (yargıç must-fix: kopya). span/incident/
//     anomaly/service-health cevapları başlıksız ("no headers") → Karar yok,
//     bilinçli.
//   - dropVerdictSentence: Karar çizilince AYNI cümle gövdeden düşer (madde
//     kalanını korur) — çift basım yok (inceleme #3).
//   - hoistCodeQuotes: gövdedeki OLUKLU kod alıntıları (N| önekli ya da
//     `// yol:a-b` başlıklı çitler) Kanıt'ın hemen altına taşınır; yerinde
//     «↑ kod alıntısı yukarıda» işareti kalır ki «aşağıdaki blok» gibi
//     göndermeler boşa düşmesin (#6); stack çitleri (oluksuz) «Stacktrace
//     Detayı»nda kalır.
// Sözleşme explainAnatomy.test.ts'te pinli. Akış SÜRERKEN çağrılmaz
// (yarım çit titremesin) — CopilotExplain yalnız bitmiş metne uygular.
import { hasGutter, parseFileHeader, stripMarker } from '@/lib/codeQuote';
import { dedent } from '@/components/Markdown';

// Kalın başlık (trace/exception) YA DA satır başı düz `Olası neden:` (problem).
const VERDICT_HDR = /(?:\*\*\s*(?:kök\s*neden[^*]*|olası\s*neden[^*]*|root\s*cause[^*]*)\*\*\s*:?\s*|^[ \t]*(?:kök\s*neden|olası\s*neden|root\s*cause)\s*:\s*)/im;

function stripInline(s: string): string {
  return s.replace(/\*\*([^*]+)\*\*/g, '$1').replace(/`([^`]+)`/g, '$1').replace(/^\s*[-*]\s+/, '').trim();
}

// Cümle sonu: [.!?] + boşluk + BÜYÜK harf / rakam / `kod` ya da metin sonu —
// «246. satırda» gibi sıra sayısı noktaları cümleyi bölmez; «2 span
// etkilendi.» ve «`orderId` boş.» gibi cevap-tipik cümle başları böler (#9).
function firstSentence(p: string): string {
  const m = p.match(/^(.+?[.!?])(?=\s+[A-ZÇĞİÖŞÜ0-9`]|\s*$)/);
  return (m ? m[1] : p).trim();
}

/** Karar satırı; yoksa ya da bölüm tek cümleyse null. ≤ 220 karakter. */
export function verdictLine(text: string | null | undefined): string | null {
  if (!text) return null;
  const m = text.match(VERDICT_HDR);
  if (!m || m.index === undefined) return null;
  const after = text.slice(m.index + m[0].length);
  // bölümün ilk paragrafı: boş satıra ya da bir sonraki **başlığa** kadar
  const para = after.split(/\n\s*\n|\n(?=\s*\*\*)/)[0] ?? '';
  const lines = para.split('\n').map(stripInline).filter(Boolean);
  if (lines.length === 0) return null;
  const sentence = firstSentence(lines[0]);
  if (!sentence) return null;
  const whole = lines.join(' ').trim();
  if (whole === sentence) return null; // bölüm = tek cümle → kopya olur
  // v0.10.358 — Operator-reported: "kod incelemesinde kök neden kesilmiş".
  // 220 karakterlik kesim cümleyi ortadan kırpıyordu ve dropVerdictSentence
  // cümleyi gövdeden de düşürdüğü için kalan yarısı HİÇBİR yerde
  // görünmüyordu. Karar cümlesi bütün gösterilir; yalnız patolojik (>1000)
  // çıktıya karşı tavan.
  return sentence.length > 1000 ? sentence.slice(0, 999) + '…' : sentence;
}

/**
 * Karar cümlesini gövdeden düşürür: başlıktan sonraki ilk dolu satırda,
 * inline işaretler soyulunca `sentence` ile başlayan en kısa ham önek kesilir
 * (kalın/kod işaretleri kalanında korunur); satır boşalırsa satır gider,
 * madde imi kalanın başına taşınır. Bulunamazsa metin aynen.
 */
export function dropVerdictSentence(text: string, sentence: string): string {
  const m = text.match(VERDICT_HDR);
  if (!m || m.index === undefined) return text;
  const start = m.index + m[0].length;
  // başlığın bittiği satır (inline «Olası neden: …» aynı satırda sürer)
  let lineStart = text.lastIndexOf('\n', start - 1) + 1;
  let lineEnd = text.indexOf('\n', start); if (lineEnd < 0) lineEnd = text.length;
  let raw = text.slice(lineStart, lineEnd);
  let off = start - lineStart; // satır içinde cümlenin başladığı yer
  if (raw.slice(off).trim() === '') {
    // başlık kendi satırında → ilk dolu satır
    let p = lineEnd + 1;
    while (p < text.length) {
      let e = text.indexOf('\n', p); if (e < 0) e = text.length;
      if (text.slice(p, e).trim() !== '') { lineStart = p; lineEnd = e; raw = text.slice(p, e); off = 0; break; }
      p = e + 1;
    }
    if (off !== 0) return text;
  }
  const head = raw.slice(0, off);
  const tail = raw.slice(off);
  const bullet = tail.match(/^\s*[-*]\s+/)?.[0] ?? '';
  let cut = -1;
  for (let k = bullet.length + 1; k <= tail.length; k++) {
    if (stripInline(tail.slice(0, k)) === sentence) { cut = k; break; }
  }
  if (cut < 0) return text;
  const remainder = tail.slice(cut).trim();
  const keepHead = head.trim() !== '' && off > 0 && !/\*\*\s*$/.test(head) ? head : '';
  const line = remainder ? (keepHead || bullet.trimStart() || '') + remainder : keepHead.trim();
  const before = text.slice(0, lineStart);
  const after = text.slice(lineEnd);
  return line ? before + line + after : before + after.replace(/^\n/, '');
}

export interface CodeQuote { lang: string; lines: string[] }
const HOIST_MARK = '*↑ kod alıntısı yukarıda*';

/** Oluklu/başlıklı kod çitlerini gövdeden çıkarır; öteki çitler (stack) yerinde kalır. */
export function hoistCodeQuotes(text: string): { quotes: CodeQuote[]; rest: string } {
  const src = text.split('\n');
  const quotes: CodeQuote[] = [];
  const out: string[] = [];
  let i = 0;
  while (i < src.length) {
    const line = src[i];
    if (line.trimStart().startsWith('```')) {
      const lang = line.trimStart().slice(3).trim();
      const body: string[] = [];
      let j = i + 1;
      while (j < src.length && !src[j].trimStart().startsWith('```')) { body.push(src[j]); j++; }
      const closed = j < src.length;
      const dedented = dedent(body);
      const isQuote = closed && (hasGutter(dedented) || !!parseFileHeader(dedented[0]));
      if (isQuote) {
        quotes.push({ lang, lines: dedented });
        // çitin yerinde işaret: madde/prose göndermeleri boşa düşmesin (#6)
        out.push(line.slice(0, line.length - line.trimStart().length) + HOIST_MARK);
        i = j + 1;
        continue;
      }
      // çit olduğu gibi kalır
      for (let k = i; k <= Math.min(j, src.length - 1); k++) out.push(src[k]);
      i = j + 1;
      continue;
    }
    out.push(line);
    i++;
  }
  return { quotes, rest: out.join('\n').replace(/\n{3,}/g, '\n\n') };
}

export { stripMarker };
