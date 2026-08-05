import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useLocation, useSearchParams } from 'react-router-dom';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import { useOpenCriticalCount, useProblems } from '@/lib/queries';
import { useAuth } from '@/components/AuthProvider';
import { useUrlRange } from '@/lib/useUrlRange';
import { timeRangeToNs } from '@/lib/utils';
import { ChatBubble } from './ai/ChatBubble';
import { useChatThread } from './ai/useChatThread';
import { greetHello, greetStatus } from './ai/greeting';

// CopilotChat (v0.6.53, v0.9.163 interaktif) — global in-app AI assistant.
// Sağ-alt animasyonlu sparkline logo (operatör seçimi B) bir drawer açar;
// operatör telemetrisine grounded cevap veren gemma4 ile (lokal) sohbet eder.
// AppShell'de bir kez mount (CommandPalette gibi), her sayfada erişilir.
//
// v0.9.163: markalı sparkline logo + Türkçe quick-start/follow-up çipleri +
// hafif markdown (kalın/kod) + copy + streaming imleç. Konuşma efemer bileşen
// state'i; her send tüm geçmişi /api/copilot/chat'e postlar, SSE tüketir.
//
// v0.9.479: bu pencere FİLO GENELİ sorular için AYNEN kaldı. Tur state'i +
// gönderme döngüsü (useChatThread) ve balon çizimi (ChatBubble) ai/ altına
// çıkarıldı; AI çekmecesindeki özne-kapsamlı sohbet aynı çekirdeği kullanır.

// Türkçe quick-start (v0.9.163 — eskiden İngilizce'ydi, cevaplar Türkçe
// geliyor). v0.9.375 (operatör): SRE perspektifli katalog — her çip guided
// router'daki BİR intent'e eşlenir (problems / my_services / my_problems /
// slow_traces / log_errors / deploy_impact / service_health). Backend'in
// yanıtlayamadığı şekle çip koymuyoruz; pod/JVM intent'i gelince çipi de
// gelir (v0.9.376 adayı).
// Follow-up önerileri — cevaptan sonra sıradaki faydalı drill-down'lar.
const FOLLOWUPS = [
  'Açık problemlerin kök nedeni?',
  'Takımımın servisleri nasıl?',
  'En yavaş servisler?',
  // v0.9.652 (operatör: "son deploy etkisine gerek yok") — deploy
  // sorusu STATİK listeden çıktı. Rotaya bağlı follow-up'larda duruyor:
  // orada bir CEVABIN devamı, burada ise bağlamsız bir menü maddesiydi.
];

// v0.9.652 — BOŞ sohbetteki başlangıç çipleri.
//
// v0.9.579'da operatör "çıkar" demişti ve gerekçe kayıtlıydı: SEKİZ
// sabit soru bir MENÜYDÜ, araç değil — operatörün kendi sorusu
// neredeyse hiç listedekilerden biri olmuyor ve liste, asistanın yalnız
// onları anlayabildiği izlenimini veriyordu.
//
// Operatör kararı değiştirdi (2026-08-05) ama gerekçeyi ÖLDÜRMEDİK:
// sekiz değil ÜÇ çip, ve üçü de operatörün ADIYLA istediği sorular.
//
// Servis ADI taşımıyorlar ve taşıyamazlar: boş sohbette takım henüz
// çözülmemiş. İlkine tıklayınca zincir devralıyor — v0.9.651 takım
// servisleri listelendikten sonra servis-adlı çipler üretiyor, oradan
// da service_health'in dört drill-down'u açılıyor (loglar, en yavaş
// trace'ler, deploy etkisi, pod'lar).
const STARTERS = [
  'Takımımın servisleri nasıl?',
  "Takımımın exception'ları?",
  "En yavaş trace'ler?",
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

export function CopilotChat() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [open, setOpen] = useState(false);
  // v0.9.169 — proaktif rozet: açık KRİTİK problem sayısı (chat kapalıyken
  // FAB'da kırmızı rozet). Yalnız copilot açıkken pollar; RQ tab gizliyken durur.
  const criticalOpen = useOpenCriticalCount({ enabled: enabled === true }).data ?? 0;
  // v0.9.528 — karşılama operatörü ADIYLA selamlar ve o anki durumu
  // söyler. İki kaynak da UCUZ: ad zaten AuthProvider'da (login'de
  // gelen /api/auth/me), P1 listesi YALNIZ pencere açıkken ve henüz
  // soru sorulmamışken çekilir — ev kuralı: aç-üzerine-getir, liste
  // prefetch'i yok, poll yok.
  const { user } = useAuth();
  // v0.9.182 — Alternatif A: sayfa-içi tam-boy expand (operatör seçimi).
  const [expanded, setExpanded] = useState(false);
  const [input, setInput] = useState('');
  const scrollRef = useRef<HTMLDivElement>(null);

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
  // v0.9.537 (operatör raporu: "ekrandaki trace'i anlamıyor") — trace
  // sayfasındayken adresteki ID bağlam olarak gider; "bu trace neden
  // yavaş" sunucuda guidedTraceByID'ye oturur. Mesaja yapıştırılan
  // açık 32-hex her zaman bundan güçlüdür.
  const currentTrace = useMemo(
    () => (loc.pathname === '/trace' ? (sp.get('id') || '') : ''),
    [loc.pathname, sp]);
  // Operation-awareness (v0.9.184) — servis sayfasında seçili ?op=. "bu
  // operasyonun durumu" gibi bir soru guided router'da RED'i o span-name'e
  // daraltır (resolveGuidedOperation'ın context fallback'i).
  const currentOp = useMemo(() => {
    if (loc.pathname === '/service' || loc.pathname === '/service/backtrace') {
      return sp.get('op') || '';
    }
    return '';
  }, [loc.pathname, sp]);

  // Tur state'i + gönderme döngüsü paylaşılan çekirdekte (v0.9.479).
  // Global pencere explain bağlamı GÖNDERMEZ — filo geneli sorular
  // sunucuda aynen guided/RAG/serbest döngü yollarına gider.
  // v0.9.529 — ekrandaki zaman aralığı. Ev kuralı: timeRangeToNs bare
  // JSX'te ASLA (v0.5.184 sonsuz refetch); burada useMemo içinde ve
  // yalnız pencere kimliğinden türüyor, `now()` okunmuyor.
  const [range] = useUrlRange();
  const rangeS = useMemo(() => {
    const { from, to } = timeRangeToNs(range);
    const s = Math.round((to - from) / 1e9);
    return s > 0 ? s : undefined;
  }, [range]);

  const { turns, busy, send, rate, clear, last, showFollowups } = useChatThread({
    service: currentService, operation: currentOp, rangeS, trace: currentTrace,
  });

  // Karşılamanın canlı yarısı (v0.9.528). `enabled` ÜÇ koşulu birden
  // taşır: copilot açık, pencere açık, ve henüz konuşma başlamamış —
  // yani sorgu SADECE karşılama gerçekten ekrandayken koşar. Kapalı
  // pencerede ya da sohbet ortasında tek istek bile gitmez.
  const p1q = useProblems({ status: 'open', priority: ['P1'], limit: 50 },
    { enabled: enabled === true && open && turns.length === 0 });
  const p1s = p1q.data?.items;

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);

  useEffect(() => {
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });
  }, [turns, open]);

  // Explain→chat köprüsü (v0.9.165): satır-içi explain panelleri (çekmece
  // OLMAYAN yüzeyler, örn. AnomalyDetailDrawer) bir global event atar; chat
  // açılır + soruyu sorar. AI çekmecesi bu köprüyü KULLANMAZ — sohbeti kendi
  // içinde açar (v0.9.479, operatör raporu: iki üst üste yüzey + bağlamsız
  // cevap). send stabil kimlikli olduğu için listener doğrudan onu çağırır.
  useEffect(() => {
    const h = (e: Event) => {
      const q = (e as CustomEvent<{ question?: string }>).detail?.question;
      if (!q) return;
      setOpen(true);
      void send(q);
    };
    window.addEventListener('coremetry:ai-ask', h);
    return () => window.removeEventListener('coremetry:ai-ask', h);
  }, [send]);

  if (!enabled) return null;

  const submit = (text: string) => { setInput(''); void send(text); };


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
              <Button variant="secondary" size="sm" onClick={clear}
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
                {/* Karşılama (v0.9.528) — LLM çağrısı YOK; ad
                    /api/auth/me'den, durum açık P1'lerden. Durum satırı
                    yüklenirken BOŞ döner: "P1 yok" yanlış iddiası
                    operatörü yanıltırdı (greeting.ts). */}
                <div style={{ marginBottom: 4, color: 'var(--text1)', fontSize: 13, fontWeight: 600 }}>
                  {greetHello(user?.firstName)}
                </div>
                {greetStatus(p1s) && (
                  <div style={{ marginBottom: 6, color: p1s && p1s.length > 0 ? 'var(--err)' : 'var(--text2)' }}>
                    {greetStatus(p1s)}
                  </div>
                )}
                {/* v0.9.579 (operatör: "çıkar") — HAZIR SORU ÇİPLERİ
                    KALDIRILDI. Sekiz sabit soru bir MENÜYDÜ, bir araç
                    değil: operatörün kendi sorusu neredeyse hiçbir zaman
                    listedekilerden biri olmuyor ve liste, asistanın
                    yalnız onları anlayabildiği izlenimini veriyordu.
                    Aynı gerekçeyle /explore'un soru kartları da
                    kaldırılmıştı (v0.9.562).
                    Cevap SONRASI follow-up çipleri KALDI: onlar bir menü
                    değil, o cevaba bağlı sıradaki adım. */}
                <div style={{ marginBottom: 10 }}>Sana nasıl yardımcı olabilirim?</div>
                {/* v0.9.652 — başlangıç çipleri (operatör isteği). Gerekçe
                    ve v0.9.579 ile ilişkisi STARTERS tanımında. */}
                <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                  {STARTERS.map(q => (
                    <button key={q} type="button" onClick={() => submit(q)}
                      style={{
                        all: 'unset', cursor: 'pointer', fontSize: 12, color: 'var(--text)',
                        border: '1px solid var(--border)', borderRadius: 999, padding: '4px 11px',
                      }}>{q}</button>
                  ))}
                </div>
              </div>
            )}
            {turns.map((t, i) => (
              <ChatBubble key={i} turn={t} onRate={v => rate(i, v)} />
            ))}
          </div>

          {/* Follow-up çipleri (v0.9.163; v0.9.411 konuya-duyarlı) —
              guided cevap kendi rotasından öneri getirirse onlar,
              yoksa statik drill-down listesi. */}
          {showFollowups && (
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', padding: '0 12px 8px' }}>
              {(last.suggestions?.length ? last.suggestions : FOLLOWUPS).map(q => (
                <button key={q} type="button" onClick={() => submit(q)}
                  style={{
                    all: 'unset', cursor: 'pointer', fontSize: 12, color: 'var(--text)',
                    border: '1px solid var(--border)', borderRadius: 999, padding: '4px 11px',
                  }}>↳ {q}</button>
              ))}
            </div>
          )}

          {/* Composer */}
          <form
            onSubmit={e => { e.preventDefault(); submit(input); }}
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
