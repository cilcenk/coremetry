// ExplainBody — v0.10.165: CoSRE cevap kartının SABİT gövde anatomisi (tasarım
// etüdü «cevap paneli» seçenek A «Yapılandırılmış kart», iki yargıçta birinci;
// dilim 1, sıfır backend). Sıra her türde aynı:
//   Karar (Kök Neden / Olası neden bölümünün ilk cümlesi; bölüm tek cümleyse
//   ÇİZİLMEZ — kopya olurdu; çizilince aynı cümle gövdeden DÜŞER; kod kartı
//   açıkken ilk kart Karar çizmez — iki rakip Karar olmasın, `verdict`) →
//   Kanıt (span/trace kimlik sayısı; listeleri AIDrawer'ın altındaki bölümler
//   taşır — yalnız gerçekten listelenenler sayılır) → kod alıntıları (oluklu
//   çitler gövdeden buraya hoist; Markdown CodeBlock anatomy: satır numarası
//   oluğu, dosya başlığı, mapper etiketi) → bölümler (Stacktrace Detayı'nın
//   stack çiti yerinde kalır, katlanır) → dipnot/geri bildirim (çağıran çizer).
// Akış SÜRERKEN ham metin aynen akar (yarım çit titremesin); anatomi yalnız
// bitmiş metne uygulanır. Güven puanı çizilmez (böyle bir alan yok — brief §5).
import { useMemo } from 'react';
import { RenderedMarkdown, CodeBlock } from '@/components/Markdown';
import type { IdLink } from '@/components/ai/inlineIdLinks';
import { verdictLine, hoistCodeQuotes, dropVerdictSentence } from './explainAnatomy';

export interface ExplainEvidence { spans: number; traces: number }

export function ExplainBody({ text, busy, links, evidence, verdict: wantVerdict = true }: {
  text: string;
  busy: boolean;
  links?: IdLink[];
  /** ilk cevabın kanıt kimlik sayıları (exception/trace); yoksa satır çizilmez */
  evidence?: ExplainEvidence;
  /** false → Karar satırı çizilmez (kod kartı açıkken ilk kart) */
  verdict?: boolean;
}) {
  const verdict = useMemo(() => (busy || !wantVerdict ? null : verdictLine(text)), [busy, text, wantVerdict]);
  const hoisted = useMemo(() => {
    if (busy) return { quotes: [], rest: text };
    const h = hoistCodeQuotes(text);
    return verdict ? { quotes: h.quotes, rest: dropVerdictSentence(h.rest, verdict) } : h;
  }, [busy, text, verdict]);
  const ev = evidence && (evidence.spans > 0 || evidence.traces > 0) ? evidence : null;
  return (
    <>
      {verdict && (
        <div className="cx-verdict"><span className="cx-verdict-k">Karar</span>{verdict}</div>
      )}
      {!busy && ev && (
        <div className="cx-evidence">
          Kanıt: {ev.spans > 0 ? `${ev.spans} span` : ''}{ev.spans > 0 && ev.traces > 0 ? ' · ' : ''}{ev.traces > 0 ? `${ev.traces} trace` : ''}
          <span className="field-hint"> · kimlikler çekmecenin altında, satır satır</span>
        </div>
      )}
      {hoisted.quotes.map((q, i) => <CodeBlock key={`q${i}`} lang={q.lang} lines={q.lines} anatomy />)}
      <RenderedMarkdown text={hoisted.rest} idLinks={links} anatomy />
    </>
  );
}
