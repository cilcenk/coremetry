import { useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { escapeHTML } from '@/lib/utils';
import { Button } from '@/components/ui/Button';
import type { ChatTurn } from '@/lib/types';
import { CosreChart, type CosreChartSpec } from '@/components/CosreChart';

// ChatBubble — bir sohbet turunun ÇİZİMİ. v0.9.479'da CopilotChat.tsx'ten
// buraya taşındı: AI çekmecesi içindeki sohbet (AIDrawer) aynı balonu
// kullanır — ikinci bir chat implementasyonu YOK. Taşıma sırasında
// davranış değişmedi (mdLite/renderMessage/balon gövdesi birebir);
// yalnız `Turn` tipi lib/types.ts'teki paylaşılan ChatTurn oldu.
//
// Balonun taşıdığı affordance'lar (adım çipleri, RAG kaynakları, derin
// linkler, kopyala, 👍/👎) böylece çekmeceye de bedelsiz gelir.

// mdLite — güvenli hafif markdown: ÖNCE escapeHTML (XSS), sonra 32-hex
// trace id'leri tıklanabilir link (v0.9.419 — href salt hex'ten kurulur,
// injection yüzeyi yok; data-nav ile SPA navigate, sayfa yenilenmez ve
// chat state'i yaşar), sonra `kod` + **kalın**. Satır sonları/madde
// tireleri container'ın white-space:pre-wrap'ıyla korunur.
export function mdLite(raw: string): string {
  return escapeHTML(raw)
    .replace(/\b[0-9a-f]{32}\b/g, '<a href="/trace?id=$&" data-nav="1">$&</a>')
    .replace(/`([^`\n]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>');
}

// renderMessage (v0.9.183) — asistan metnini ```chart {json}``` bloklarına göre
// böler: metin parçaları mdLite ile, chart blokları canlı <CosreChart> ile
// çizilir. Blok, backend guided-health tarafından DETERMİNİSTİK üretilir (LLM
// biçimlemesine güvenmeyiz — gemma4 küçük model). Akış sürerken kapanmamış bir
// blok JSON.parse'ı fail eder → o tur düz metin görünür, blok tamamlanınca
// grafiğe döner (kademeli). Bozuk/eksik spec sessizce atlanır (asla crash).
export function renderMessage(text: string) {
  const re = /```chart\s*([\s\S]*?)```/g;
  const out: ReactNode[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) {
      out.push(<span key={i++} dangerouslySetInnerHTML={{ __html: mdLite(text.slice(last, m.index)) }} />);
    }
    try {
      const spec = JSON.parse(m[1].trim()) as CosreChartSpec;
      if (spec && typeof spec.service === 'string' && typeof spec.agg === 'string') {
        out.push(<CosreChart key={i++} spec={spec} />);
      }
    } catch { /* kapanmamış/bozuk blok — atla (akış sürüyor olabilir) */ }
    last = re.lastIndex;
  }
  if (last < text.length) {
    out.push(<span key={i++} dangerouslySetInnerHTML={{ __html: mdLite(text.slice(last)) }} />);
  }
  return out.length ? <>{out}</> : <span dangerouslySetInnerHTML={{ __html: mdLite(text) }} />;
}

// rateTurn — 👍/👎 POST'u. İyimser güncelleme + hata hâlinde geri alma;
// iki yüzey de aynı davranışı paylaşsın diye burada (v0.9.479).
export function rateTurn(
  turns: ChatTurn[], idx: number, verdict: 1 | -1,
  setTurns: (fn: (prev: ChatTurn[]) => ChatTurn[]) => void,
) {
  const turn = turns[idx];
  if (!turn?.exchangeId || turn.verdict === verdict) return;
  const prior = turn.verdict;
  const exchangeId = turn.exchangeId;
  setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict } : t)));
  api.postAIFeedback({ exchangeId, verdict }).catch(() => {
    setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict: prior } : t)));
  });
}

export function ChatBubble({ turn, onRate }: { turn: ChatTurn; onRate?: (v: 1 | -1) => void }) {
  const isUser = turn.role === 'user';
  const navigate = useNavigate();
  const [copied, setCopied] = useState(false);
  // v0.9.419 — mdLite'ın enjekte ettiği data-nav linkleri (trace id'ler)
  // SPA içi gider: tam sayfa yenilenmesi efemer chat'i sıfırlardı.
  const onBodyClick = (e: React.MouseEvent) => {
    const a = (e.target as HTMLElement).closest?.('a[data-nav]');
    if (a) {
      e.preventDefault();
      navigate(a.getAttribute('href') ?? '/');
    }
  };
  const copy = () => {
    navigator.clipboard?.writeText(turn.text ?? '').then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    }).catch(() => {});
  };
  const done = !isUser && !turn.pending && !turn.error && !!turn.text;
  return (
    <div style={{ alignSelf: isUser ? 'flex-end' : 'flex-start', maxWidth: '85%' }}>
      <div onClick={onBodyClick} style={{
        padding: '8px 11px', borderRadius: 10, fontSize: 13, lineHeight: 1.5,
        whiteSpace: 'pre-wrap', wordBreak: 'break-word',
        background: isUser ? 'var(--accent2)' : 'var(--bg2)',
        color: isUser ? '#fff' : 'var(--text)',
        border: isUser ? 'none' : '1px solid var(--border)',
      }}>
        {/* Tool-call progress chips (assistant only) */}
        {!isUser && turn.steps && turn.steps.length > 0 && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginBottom: turn.text ? 6 : 0 }}>
            {turn.steps.map((s, i) => (
              <span key={i} style={{
                fontSize: 10, fontFamily: 'ui-monospace, monospace',
                padding: '1px 6px', borderRadius: 8,
                background: 'var(--bg3)', color: 'var(--text3)',
              }}>⚙ {s}</span>
            ))}
          </div>
        )}
        {turn.error ? (
          <span style={{ color: isUser ? '#fff' : 'var(--err)' }}>⚠ {turn.error}</span>
        ) : isUser ? (
          turn.text
        ) : turn.text ? (
          // Asistan metni: hafif markdown (escape'li) + gömülü canlı grafikler
          // (```chart``` blokları) + akış sürüyorsa imleç.
          <>
            {renderMessage(turn.text)}
            {turn.pending && <span className="cm-ai-cursor" />}
          </>
        ) : turn.pending ? (
          <span style={{ color: 'var(--text3)' }}>yazıyor<span className="cm-ai-cursor" /></span>
        ) : ''}
      </div>

      {/* Kaynak chip'leri (RAG dayanağı). v0.9.515 (operatör): doküman
          ADI çipte GÖSTERİLMİYOR — dosya adı iç artefakt, cevabın parçası
          değil. Çip yine de duruyor ki cevabın bir dokümana dayandığı
          görünsün; ad ipucuna (hover) taşındı, yani denetlenebilirlik
          kaybolmadan gürültü kalktı. */}
      {!isUser && !!turn.sources?.length && !turn.pending && !turn.error && (
        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
          {turn.sources.map((src, i) => src.ref ? (
            <a key={i} href={src.ref} target="_blank" rel="noopener"
              className="badge b-info" style={{ textDecoration: 'none', fontSize: 10 }}
              title={`${src.doc} §${src.chunk} · benzerlik ${(src.score * 100).toFixed(0)}%`}>
              📄 Kaynak §{src.chunk}
            </a>
          ) : (
            <span key={i} className="badge b-info" style={{ fontSize: 10 }}
              title={`${src.doc} §${src.chunk} · benzerlik ${(src.score * 100).toFixed(0)}%`}>
              📄 Kaynak §{src.chunk}
            </span>
          ))}
        </div>
      )}

      {/* Derin-link çipleri (v0.9.419) — cevabın konusuna tek tık.
          Sunucu rotadan deterministik üretir; SPA Link, chat yaşar. */}
      {!isUser && !!turn.links?.length && !turn.pending && !turn.error && (
        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
          {turn.links.map((l, i) => (
            // v0.9.709 — DIŞ URL çipi (log köprüsü, https://...) SPA
            // <Link>'e verilemez: router onu path sanıp uygulama içinde
            // gezinir ve link kırılır. Dış href <a target=_blank>.
            /^https?:/i.test(l.href) ? (
              <a key={i} href={l.href} target="_blank" rel="noopener noreferrer"
                className="badge b-info" style={{ textDecoration: 'none', fontSize: 10 }}>
                🔗 {l.label}
              </a>
            ) : (
              <Link key={i} to={l.href} className="badge b-info"
                style={{ textDecoration: 'none', fontSize: 10 }}>
                🔗 {l.label}
              </Link>
            )
          ))}
        </div>
      )}

      {/* Aksiyon satırı — copy + thumbs (tamamlanmış asistan cevabı) */}
      {done && (
        <div style={{ display: 'flex', gap: 2, marginTop: 2, alignItems: 'center' }}>
          <Button variant="ghost" size="sm" onClick={copy}
            title="Kopyala" aria-label="Cevabı kopyala"
            style={{ padding: '0 6px', fontSize: 12, color: copied ? 'var(--ok)' : undefined }}>
            {copied ? '✓' : '⧉'}
          </Button>
          {!!turn.exchangeId && onRate && (
            <>
              <Button variant="ghost" size="sm" onClick={() => onRate(1)}
                title="Faydalı" aria-label="Cevabı faydalı işaretle"
                style={{ padding: '0 6px', fontSize: 12, opacity: turn.verdict === 1 ? 1 : 0.4 }}>👍</Button>
              <Button variant="ghost" size="sm" onClick={() => onRate(-1)}
                title="Faydasız" aria-label="Cevabı faydasız işaretle"
                style={{ padding: '0 6px', fontSize: 12, opacity: turn.verdict === -1 ? 1 : 0.4 }}>👎</Button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
