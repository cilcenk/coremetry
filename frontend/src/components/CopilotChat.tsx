import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import { useLocation, useSearchParams } from 'react-router-dom';
import { api } from '@/lib/api';
import { escapeHTML } from '@/lib/utils';
import { Button } from '@/components/ui/Button';
import type { ChatMessage } from '@/lib/types';
import { useOpenCriticalCount } from '@/lib/queries';
import { CosreChart, type CosreChartSpec } from './CosreChart';

// CopilotChat (v0.6.53, v0.9.163 interaktif) — global in-app AI assistant.
// Sağ-alt animasyonlu sparkline logo (operatör seçimi B) bir drawer açar;
// operatör telemetrisine grounded cevap veren gemma4 ile (lokal) sohbet eder.
// AppShell'de bir kez mount (CommandPalette gibi), her sayfada erişilir.
//
// v0.9.163: markalı sparkline logo + Türkçe quick-start/follow-up çipleri +
// hafif markdown (kalın/kod) + copy + streaming imleç. Konuşma efemer bileşen
// state'i; her send tüm geçmişi /api/copilot/chat'e postlar, SSE tüketir.

type Turn = ChatMessage & {
  steps?: string[]; pending?: boolean; error?: string;
  exchangeId?: string; verdict?: 1 | -1;
  sources?: import('@/lib/types').RagSource[];
};

// Türkçe quick-start (v0.9.163 — eskiden İngilizce'ydi, cevaplar Türkçe geliyor).
const SAMPLE_QUESTIONS = [
  'Şu an hangi servisler sağlıksız?',
  'Açık problemler ve kök neden neler?',
  'En yavaş endpoint neden yavaş?',
  'Son 1 saatteki hatalar?',
];
// Follow-up önerileri — cevaptan sonra sıradaki faydalı drill-down'lar.
const FOLLOWUPS = [
  'Açık problemlerin kök nedeni?',
  'En yavaş servisler?',
  'Son deploy\'un etkisi?',
];

// CoSRE markası — çizilen gradient sparkline (APM göndermesi, varyant B).
function AiMark({ size = 26 }: { size?: number }) {
  const gid = useId();
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#6e5cff" />
          <stop offset="0.5" stopColor="#12b8ff" />
          <stop offset="1" stopColor="#38e8c6" />
        </linearGradient>
      </defs>
      <polyline className="cm-ai-spark" points="3,15 8,9 11,12 15,5 21,11"
        stroke={`url(#${gid})`} strokeWidth="2.2" fill="none"
        strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

// mdLite — güvenli hafif markdown: ÖNCE escapeHTML (XSS), sonra `kod` + **kalın**.
// Satır sonları/madde tireleri container'ın white-space:pre-wrap'ıyla korunur.
function mdLite(raw: string): string {
  return escapeHTML(raw)
    .replace(/`([^`\n]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>');
}

// renderMessage (v0.9.183) — asistan metnini ```chart {json}``` bloklarına göre
// böler: metin parçaları mdLite ile, chart blokları canlı <CosreChart> ile
// çizilir. Blok, backend guided-health tarafından DETERMİNİSTİK üretilir (LLM
// biçimlemesine güvenmeyiz — gemma4 küçük model). Akış sürerken kapanmamış bir
// blok JSON.parse'ı fail eder → o tur düz metin görünür, blok tamamlanınca
// grafiğe döner (kademeli). Bozuk/eksik spec sessizce atlanır (asla crash).
function renderMessage(text: string) {
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

export function CopilotChat() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  // v0.9.169 — proaktif rozet: açık KRİTİK problem sayısı (chat kapalıyken
  // FAB'da kırmızı rozet). Yalnız copilot açıkken pollar; RQ tab gizliyken durur.
  const criticalOpen = useOpenCriticalCount({ enabled: enabled === true }).data ?? 0;
  // v0.9.182 — Alternatif A: sayfa-içi tam-boy expand (operatör seçimi).
  const [expanded, setExpanded] = useState(false);
  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const abortRef = useRef<AbortController | null>(null);

  // Context-awareness (v0.9.164) — bulunulan sayfanın servisi. Mesaj servis
  // adı taşımıyorsa backend guided router bunu varsayılan alır ("neden yavaş?"
  // servis sayfasında → o servis). Banner scope'u şeffaf gösterir.
  const loc = useLocation();
  const [sp] = useSearchParams();
  const currentService = useMemo(() => {
    if (loc.pathname === '/service' || loc.pathname === '/service/backtrace') {
      return sp.get('name') || sp.get('service') || '';
    }
    if (loc.pathname === '/pod') return sp.get('service') || '';
    return '';
  }, [loc.pathname, sp]);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });
  }, [turns, open]);

  useEffect(() => () => abortRef.current?.abort(), []);

  // Explain→chat köprüsü (v0.9.165): sayfalardaki "chat'te devam et" bir global
  // event atar; chat açılır + soruyu sorar. sendRef her render güncellenir
  // (send closure'ı taze); listener stabil ref üstünden en güncel send'i çağırır.
  const sendRef = useRef<(q: string) => void>(() => {});
  useEffect(() => {
    const h = (e: Event) => {
      const q = (e as CustomEvent<{ question?: string }>).detail?.question;
      if (!q) return;
      setOpen(true);
      sendRef.current(q);
    };
    window.addEventListener('coremetry:ai-ask', h);
    return () => window.removeEventListener('coremetry:ai-ask', h);
  }, []);

  if (!enabled) return null;

  const rate = (idx: number, verdict: 1 | -1) => {
    const turn = turns[idx];
    if (!turn?.exchangeId || turn.verdict === verdict) return;
    const prior = turn.verdict;
    const exchangeId = turn.exchangeId;
    setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict } : t)));
    api.postAIFeedback({ exchangeId, verdict }).catch(() => {
      setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict: prior } : t)));
    });
  };

  const send = async (text: string) => {
    const q = text.trim();
    if (!q || busy) return;
    setInput('');
    const history: ChatMessage[] = [
      ...turns.filter(t => !t.error).map(t => ({ role: t.role, text: t.text })),
      { role: 'user', text: q },
    ];
    setTurns(prev => [
      ...prev,
      { role: 'user', text: q },
      { role: 'assistant', text: '', steps: [], pending: true },
    ]);
    setBusy(true);
    const ac = new AbortController();
    abortRef.current = ac;

    const patchLast = (fn: (t: Turn) => Turn) =>
      setTurns(prev => prev.map((t, i) => (i === prev.length - 1 ? fn(t) : t)));

    try {
      await api.copilotChat(history, (e) => {
        if (e.kind === 'step') {
          patchLast(t => ({ ...t, steps: [...(t.steps ?? []), e.tool] }));
        } else if (e.kind === 'delta') {
          patchLast(t => ({ ...t, text: (t.text ?? '') + e.text }));
        } else if (e.kind === 'answer') {
          patchLast(t => ({ ...t, text: e.text, exchangeId: e.exchangeId, sources: e.sources, pending: false }));
        } else if (e.kind === 'error') {
          patchLast(t => ({ ...t, error: e.error, pending: false }));
        } else if (e.kind === 'done') {
          patchLast(t => ({ ...t, pending: false }));
        }
      }, ac.signal, currentService || undefined);
    } catch (err) {
      patchLast(t => ({ ...t, error: err instanceof Error ? err.message : String(err), pending: false }));
    } finally {
      setBusy(false);
      abortRef.current = null;
    }
  };
  sendRef.current = send; // Explain→chat köprüsü en güncel send'i çağırsın.

  // Follow-up çipleri yalnız son tur TAMAMLANMIŞ bir asistan cevabıysa görünür.
  const last = turns[turns.length - 1];
  const showFollowups = !busy && last && last.role === 'assistant' && !last.pending && !last.error && !!last.text;

  return (
    <>
      {/* Launcher — markalı animasyonlu sparkline (varyant B). Yuvarlak FAB
          kendi anatomisi; shared <Button> atomu uygulanmaz (U1 batch-2 kararı). */}
      {!open && (
        <button
          className="cm-ai-fab"
          onClick={() => setOpen(true)}
          title={criticalOpen > 0 ? `CoSRE — ${criticalOpen} açık kritik problem` : "CoSRE'ye sor"}
          aria-label={criticalOpen > 0 ? `CoSRE, ${criticalOpen} açık kritik problem` : 'CoSRE'}
          style={{
            position: 'fixed', right: 18, bottom: 18, zIndex: 60,
            width: 48, height: 48, borderRadius: 24,
            background: 'linear-gradient(135deg, var(--accent-soft), var(--bg1))',
            border: '1px solid var(--accent2)',
            display: 'grid', placeItems: 'center',
            cursor: 'pointer', boxShadow: '0 2px 14px rgba(0,0,0,0.3)',
          }}>
          <AiMark size={26} />
          {criticalOpen > 0 && (
            <span aria-hidden="true" style={{
              position: 'absolute', top: -3, right: -3,
              minWidth: 18, height: 18, padding: '0 5px', boxSizing: 'border-box',
              borderRadius: 9, background: 'var(--err)', color: '#fff',
              fontSize: 10, fontWeight: 700, lineHeight: '14px',
              display: 'grid', placeItems: 'center',
              border: '2px solid var(--bg1)',
            }}>{criticalOpen > 9 ? '9+' : criticalOpen}</span>
          )}
        </button>
      )}

      {/* Drawer */}
      {open && (
        <div style={{
          position: 'fixed', zIndex: 60,
          display: 'flex', flexDirection: 'column',
          background: 'var(--bg1)', border: '1px solid var(--border)',
          boxShadow: '0 8px 40px rgba(0,0,0,0.5)',
          transition: 'width .16s, height .16s',
          // Alternatif A (v0.9.182): genişken içerik alanını doldurur (sidebar
          // ~208px + topbar ~56px görünür kalır); değilse sağ-alt drawer.
          ...(expanded
            ? { top: 56, left: 208, right: 12, bottom: 12, borderRadius: 12 }
            : { right: 18, bottom: 18, width: 'min(420px, calc(100vw - 36px))', height: 'min(620px, calc(100vh - 100px))', borderRadius: 10 }),
        }}>
          {/* Header */}
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            padding: '10px 14px', borderBottom: '1px solid var(--border)',
          }}>
            <AiMark size={18} />
            <span style={{ fontWeight: 600, fontSize: 13 }}>CoSRE</span>
            <span style={{ flex: 1 }} />
            <Button variant="ghost" size="sm" onClick={() => setExpanded(e => !e)}
              title={expanded ? "Drawer'a küçült" : 'Tam sayfa genişlet'}>
              {expanded ? '⊟' : '⤢'}</Button>
            {turns.length > 0 && (
              <Button variant="secondary" size="sm" onClick={() => setTurns([])}
                title="Konuşmayı temizle">Temizle</Button>
            )}
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)}
              title="Kapat">✕</Button>
          </div>

          {/* Context banner (v0.9.164) — bulunulan servis, scope şeffaflığı. */}
          {currentService && (
            <div style={{
              display: 'flex', alignItems: 'center', gap: 6, fontSize: 11,
              color: 'var(--text2)', background: 'var(--accent-soft)',
              padding: '5px 14px', borderBottom: '1px solid var(--border)',
            }}>
              📍 <b className="mono" style={{ color: 'var(--accent2)' }}>{currentService}</b> · sorular bu servise scope'lanır
            </div>
          )}

          {/* Messages */}
          <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 14, display: 'flex', flexDirection: 'column', gap: 10 }}>
            {turns.length === 0 && (
              <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                <div style={{ marginBottom: 10 }}>Merhaba 👋 Telemetrini sor — canlı veriye grounded cevap veririm.</div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {SAMPLE_QUESTIONS.map(q => (
                    <Button key={q} variant="secondary" size="sm" onClick={() => send(q)}
                      style={{ textAlign: 'left' }}>{q}</Button>
                  ))}
                </div>
              </div>
            )}
            {turns.map((t, i) => (
              <ChatBubble key={i} turn={t} onRate={v => rate(i, v)} />
            ))}
          </div>

          {/* Follow-up çipleri (v0.9.163) — sıradaki drill-down'lar. */}
          {showFollowups && (
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', padding: '0 12px 8px' }}>
              {FOLLOWUPS.map(q => (
                <button key={q} type="button" onClick={() => send(q)}
                  style={{
                    all: 'unset', cursor: 'pointer', fontSize: 12, color: 'var(--text)',
                    border: '1px solid var(--border)', borderRadius: 999, padding: '4px 11px',
                  }}>↳ {q}</button>
              ))}
            </div>
          )}

          {/* Composer */}
          <form
            onSubmit={e => { e.preventDefault(); send(input); }}
            style={{ display: 'flex', gap: 8, padding: 10, borderTop: '1px solid var(--border)' }}>
            <input
              value={input}
              onChange={e => setInput(e.target.value)}
              placeholder="CoSRE'ye sor…"
              disabled={busy}
              autoFocus
              style={{
                flex: 1, padding: '7px 10px', fontSize: 13,
                background: 'var(--bg)', color: 'var(--text)',
                border: '1px solid var(--border)', borderRadius: 6,
              }} />
            <Button type="submit" disabled={busy || !input.trim()}>
              {busy ? '…' : 'Gönder'}
            </Button>
          </form>
        </div>
      )}
    </>
  );
}

function ChatBubble({ turn, onRate }: { turn: Turn; onRate?: (v: 1 | -1) => void }) {
  const isUser = turn.role === 'user';
  const [copied, setCopied] = useState(false);
  const copy = () => {
    navigator.clipboard?.writeText(turn.text ?? '').then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    }).catch(() => {});
  };
  const done = !isUser && !turn.pending && !turn.error && !!turn.text;
  return (
    <div style={{ alignSelf: isUser ? 'flex-end' : 'flex-start', maxWidth: '85%' }}>
      <div style={{
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

      {/* Kaynak chip'leri (RAG dayanağı) */}
      {!isUser && !!turn.sources?.length && !turn.pending && !turn.error && (
        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
          {turn.sources.map((src, i) => src.ref ? (
            <a key={i} href={src.ref} target="_blank" rel="noopener"
              className="badge b-info" style={{ textDecoration: 'none', fontSize: 10 }}
              title={`benzerlik ${(src.score * 100).toFixed(0)}%`}>
              📄 {src.doc} §{src.chunk}
            </a>
          ) : (
            <span key={i} className="badge b-info" style={{ fontSize: 10 }}
              title={`benzerlik ${(src.score * 100).toFixed(0)}%`}>
              📄 {src.doc} §{src.chunk}
            </span>
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
