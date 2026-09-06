import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { mergeOpenHref } from '@/lib/openHref'; // v0.10.460
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { Drawer } from '@/components/ui/Drawer';
import { useConfirm } from '@/components/ui/ConfirmDialog';
import { useOpenCriticalCount, useProblems } from '@/lib/queries';
import { useAuth } from '@/components/AuthProvider';
import { useUrlRange } from '@/lib/useUrlRange';
import { useUrlEnv } from '@/lib/useUrlEnv';
import { timeRangeToNs, tsRel } from '@/lib/utils';
import { contextStarter, serviceFromRoute } from '@/lib/chatContext';
import { syncChatParam } from '@/lib/chatUrl';
import { Empty, Spinner } from './Spinner';
import { ChatBubble } from './ai/ChatBubble';
import { TraceExplainNudge } from './ai/TraceExplainNudge';
import { useChatThread } from './ai/useChatThread';
import { useCopilotConfig } from './ai/useCopilotEnabled'; // v0.10.483
import { AI_DRAWER_WIDTH } from './ai/answerCard'; // v0.10.461
import { AIDrawerBody } from './ai/AIDrawerBody'; // v0.10.483 — ✨ Explain gövdesi aynı çekmecede
import { useAiSubject } from './ai/useAiSubject';
import { aiSubjectSubtitle, aiSubjectTitle, formatAiParam } from '@/lib/aiSubject';
import { greetHello, greetStatus } from './ai/greeting';
import type { AiConversationSummary } from '@/lib/types';

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
  // v0.10.483 — config TEK kaynaktan (useCopilotConfig: modül cache'i, AIDrawer
  // ile aynı); eskiden CopilotChat kendi effect'iyle ikinci bir istek atıyordu.
  const cfg = useCopilotConfig(true);
  const enabled: boolean | null = cfg ? cfg.enabled : null;
  // v0.10.183 — model profili seçici (>1 profil); boş = sunucu varsayılanı.
  const profiles = useMemo(() => cfg?.profiles ?? [], [cfg]);
  const defaultProfile = cfg?.defaultProfile ?? '';
  // v0.10.461 — başlık meta şeridindeki model çipi (AIDrawer ile aynı anatomi).
  const model = cfg?.model ?? ''; // v0.10.483 — config tek kaynaktan (useCopilotConfig)
  const [profile, setProfile] = useState('');
  const [open, setOpen] = useState(false);
  // v0.10.483 — ✨ Explain öznesi (`?ai=`): varsa çekmece AÇIK ve açıklama
  // kipinde (AIDrawerBody); genel sohbet kipi öznesizken. Tek kabuk.
  const [subject, setSubject] = useAiSubject();
  const drawerOpen = open || subject !== null;
  const closeDrawer = () => { setSubject(null); setOpen(false); };
  const navigate = useNavigate(); // v0.10.434 (D7b) — "sayfasını aç" cevabı SPA içinde gezer
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
  const [sp, setSp] = useSearchParams();
  // v0.9.653 — ekrandaki özneden türeyen başlangıç çipi. Saf çözümleyici
  // (lib/chatContext.ts); rota değişince kendiliğinden güncelleniyor.
  const ctxStarter = useMemo(
    () => contextStarter(loc.pathname, loc.search),
    [loc.pathname, loc.search]);
  // v0.9.1226 — saf çözümleyiciye taşındı (chatContext.serviceFromRoute):
  // yalnız /service|/pod değil, ?service= taşıyan tüm liste rotaları
  // bağlamı devrediyor; kapsam bandı + guided ctxService bedavaya doldu.
  const currentService = useMemo(
    () => serviceFromRoute(loc.pathname, loc.search),
    [loc.pathname, loc.search]);
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
  // v0.9.1259 — env devri: ekrandaki global env seçimi sohbete bağlam
  // olarak gider (rangeS aynası); kapsam bandı da gösterir.
  const [env] = useUrlEnv();
  const rangeS = useMemo(() => {
    const { from, to } = timeRangeToNs(range);
    const s = Math.round((to - from) / 1e9);
    return s > 0 ? s : undefined;
  }, [range]);
  // v0.10.33 — MUTLAK pencerenin BİTİŞ anı. rangeS pencereyi SÜREYE
  // çökertiyor ve sunucu onu şimdiye yeniden çapalıyordu: dün gece
  // 03:00-04:00'a zoom yapıp soru sorunca cevap aynı UZUNLUKTA ama
  // BUGÜNKÜ pencereden geliyordu. Sayılar gerçek olduğu için hata
  // sessizdi.
  //
  // ⚠ YALNIZ custom/zoom aralıkta gönderiliyor. Göreli aralıkta ("son 1
  // saat") çıpayı sabitlemek, uzun bir soruşturmada cevabı DONDURUR:
  // operatör yirmi dakika sonra "şimdi nasıl" diye sorduğunda hâlâ
  // yirmi dakika önceki pencereyi görürdü.
  const toMs = useMemo(
    () => (range.preset === 'custom' && range.toMs ? range.toMs : undefined),
    [range],
  );

  // persist: true — KALICILIK YALNIZ BURADA (v0.9.1139, Faz 4.1).
  // AI çekmecesindeki özne sohbeti efemer kalıyor; gerekçe
  // useChatThread'in dosya başında.
  const { turns, busy, send, stop, rate, clear, load, conversationId, last, showFollowups } =
    useChatThread({
      service: currentService, operation: currentOp, rangeS, toMs, trace: currentTrace, env,
      profile: profile || undefined,
      persist: true,
      onOpen: href => {
        const to = mergeOpenHref(href, window.location.pathname, window.location.search); // v0.10.434 (D7b); v0.10.460 aynı sayfa
        if (!to) return;
        // v0.10.483 — hedef ?ai= ise AYNI çekmece açıklama kipine geçer
        // (özne URL'den okunur); kapatmaya gerek yok, ikinci çekmece yok.
        navigate(to, { replace: true });
      },
    });

  // v0.9.1258 — konuşma deep-link'i (?chat=<convId>): URL → state yarısı.
  // Ref sig-guard: aynı değer bir kez yüklenir; load zaten akış sürerken
  // (busyRef) kendini iptal ediyor — yarım cevabın üstüne basılmaz.
  const chatParamRef = useRef('');
  useEffect(() => {
    const want = sp.get('chat') ?? '';
    if (!want || chatParamRef.current === want) return;
    chatParamRef.current = want;
    if (want !== conversationId) {
      setOpen(true);
      void load(want);
    }
  }, [sp, conversationId, load]);
  // state → URL yarısı: saf çekirdek karar verir (null = dokunma);
  // replace:true + prev kopyası — yabancı paramlar korunur.
  useEffect(() => {
    const next = syncChatParam(sp, conversationId, open);
    if (next) setSp(next, { replace: true });
  }, [sp, setSp, conversationId, open]);

  // ── Geçmiş (v0.9.1139) ──
  // Liste YALNIZ çekmece açılışında çekiliyor (ev kuralı: aç-üzerine
  // getir, prefetch yok, poll yok). Bu oturumda başlatılan bir konuşma
  // listede bir SONRAKİ açılışta görünür — bunun için ekstra istek
  // atmak, maliyet disiplininin karşılığını vermiyor.
  const confirm = useConfirm();
  const [showHistory, setShowHistory] = useState(false);
  const [threads, setThreads] = useState<AiConversationSummary[] | undefined>(undefined);
  const [histErr, setHistErr] = useState('');
  useEffect(() => {
    if (!open) return;
    let alive = true;
    setThreads(undefined);
    setHistErr('');
    api.aiConversations()
      .then(t => { if (alive) setThreads(t ?? []); })
      .catch(e => {
        if (alive) { setThreads([]); setHistErr(e instanceof Error ? e.message : String(e)); }
      });
    return () => { alive = false; };
  }, [open]);

  const openThread = async (id: string) => {
    try {
      setSubject(null); // v0.10.483 — arşivden konuşma açmak genel kipe döner
      await load(id);
      setShowHistory(false);
    } catch (e) {
      setHistErr(e instanceof Error ? e.message : String(e));
    }
  };

  const removeThread = async (t: AiConversationSummary) => {
    if (!await confirm({
      title: 'Konuşma silinsin mi?',
      body: <><b>{t.title}</b> konuşması kalıcı olarak silinecek. Yalnız
        sohbet kaydı gider — telemetri verisine dokunulmaz.</>,
      confirmLabel: 'Konuşmayı sil',
      danger: true,
    })) return;
    try {
      await api.deleteAiConversation(t.id);
      setThreads(prev => (prev ?? []).filter(x => x.id !== t.id));
      // Ekrandaki konuşma silinen thread ise, kabuk artık ölü bir
      // kimliğe yazmaya devam etmemeli: yeni konuşma hâline dönüyoruz.
      if (conversationId === t.id) clear();
    } catch (e) {
      setHistErr(e instanceof Error ? e.message : String(e));
    }
  };

  // Karşılamanın canlı yarısı (v0.9.528). `enabled` ÜÇ koşulu birden
  // taşır: copilot açık, pencere açık, ve henüz konuşma başlamamış —
  // yani sorgu SADECE karşılama gerçekten ekrandayken koşar. Kapalı
  // pencerede ya da sohbet ortasında tek istek bile gitmez.
  const p1q = useProblems({ status: 'open', priority: ['P1'], limit: 50 },
    { enabled: enabled === true && open && turns.length === 0 });
  const p1s = p1q.data?.items;


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
      {!drawerOpen && <TraceExplainNudge />}{/* v0.10.432 (D8) — FAB'ın üstündeki baloncuk */}
      {!drawerOpen && (
        <button
          className="cm-ai-fab"
          onClick={() => setOpen(true)}
          title={criticalOpen > 0 ? `CoSRE — ${criticalOpen} açık kritik problem` : "CoSRE'ye sor"}
          aria-label={criticalOpen > 0 ? `CoSRE, ${criticalOpen} açık kritik problem` : 'CoSRE'}
          style={{
            position: 'fixed', right: 18, bottom: 18, zIndex: 'var(--z-fab)',
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

      {/* v0.9.654 (operatör: "CoSRE drawer gibi çıksa … Chat'ten devam et
          özelliği drawerdı") — sohbet artık PAYLAŞILAN Drawer primitifini
          kullanıyor: Explain çekmecesiyle (AIDrawer) aynı kenar, aynı
          genişlik, aynı Esc/✕ davranışı. Öncesi kendi sağ-alt yüzen
          paneliydi ve iki AI yüzeyi iki ayrı kabuk gibi duruyordu.

          backdrop=false BİLİNÇLİ: operatör sohbet açıkken tabloyu
          kaydırıyor, başka bir trace açıyor, sonra sorusunu yazıyor.
          Overlay bunu imkânsız kılardı — sohbet bir özneyi İNCELEMİYOR,
          ona EŞLİK ediyor. Explain'in modal davranışı DEĞİŞMEDİ.

          Genişlet kipi korundu: geniş sohbet için 620 → içerik alanı. */}
      {drawerOpen && (
        <Drawer
          onClose={closeDrawer}
          backdrop={false}
          width={expanded ? 1100 : AI_DRAWER_WIDTH}
          bodyStyle={{ display: 'flex', flexDirection: 'column', padding: 0 }}
          header={
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flex: 1, minWidth: 0 }}>
              {/* v0.9.1253 (operatör: "CoSRE yazısı biraz daha belirgin,
                  büyük font olabilir") — 13→16 + 700 + hafif harf aralığı;
                  marka adı çekmece başlığında artık ilk bakışta okunur.
                  AiMark de eşlik etsin diye 18→20. */}
              {/* v0.10.483 (operatör: "Geçmiş/Temizle butonları kaymış") — TEK
                  SATIR: marka · meta (kapsam ya da ✨ Explain öznesi, ellipsis)
                  · model çipi · eylemler. 461'in iki satırlı başlığı Drawer'ın
                  ✕ hizasını ve eylem düğmelerini kaydırıyordu. */}
              <AiMark size={20} />
              <span style={{ fontWeight: 700, fontSize: 16, letterSpacing: 0.3, flexShrink: 0 }}>CoSRE</span>
              <span className="mono" style={{
                flex: 1, minWidth: 0, fontSize: 11,
                color: subject ? 'var(--text2)' : currentService ? 'var(--accent2)' : 'var(--text3)',
                overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
              }} title={subject ? `${aiSubjectTitle(subject)} · ${aiSubjectSubtitle(subject)}` : currentService ? `Sorular ${currentService} servisine kapsanır` : 'Filo geneli sorular'}>
                {subject
                  ? `✨ ${aiSubjectTitle(subject)} · ${aiSubjectSubtitle(subject)}`
                  : `${currentService ? `📍 ${currentService}` : 'filo geneli'}${env ? ` · env ${env}` : ''}`}
              </span>
              {model && (
                <span className="chip" style={{ flexShrink: 0, fontSize: 10.5 }} title="Cevapları üreten model">
                  <span className="k">model</span>
                  <b className="mono">{model}</b>
                </span>
              )}
              {/* v0.9.1139 — konuşma arşivi. Menü değil bir BÖLÜM:
                  çekmece zaten sağ kenarda ve ikinci bir uçan katman
                  (dropdown) sohbetin üstüne binerdi. */}
              {profiles.length > 1 && (
                <select value={profile} onChange={e => setProfile(e.target.value)} aria-label="Model profili"
                  title="Bu konuşma için model profili (boş = sunucu varsayılanı / yüzey eşlemesi)" style={{ maxWidth: 180 }}>
                  <option value="">model: varsayılan{defaultProfile ? ` · ${profiles.find(p => p.id === defaultProfile)?.label || defaultProfile}` : ''}</option>
                  {profiles.filter(p => p.id !== defaultProfile).map(p => <option key={p.id} value={p.id}>{p.label || p.id}{p.model ? ` · ${p.model}` : ''}</option>)}
                </select>
              )}
              <Button variant="ghost" size="sm" onClick={() => setShowHistory(h => !h)}
                aria-expanded={showHistory}
                title="Konuşma geçmişi">🕘 Geçmiş</Button>
              <Button variant="ghost" size="sm" onClick={() => setExpanded(e => !e)}
                title={expanded ? 'Daralt' : 'Genişlet'}>
                {expanded ? '⊟' : '⤢'}</Button>
              {!subject && turns.length > 0 && (
                <Button variant="secondary" size="sm" onClick={clear}
                  title="Konuşmayı temizle ve yeni konuşma başlat">Temizle</Button>
              )}
            </div>
          }>

          {/* Geçmiş bölümü (v0.9.1139) — başlık + son etkinlik + mesaj
              sayısı. Satıra tıklamak konuşmayı YÜKLER; satır sonundaki
              quiet-destructive düğme onaydan sonra siler. Yükleniyor /
              hata / boş üç hâlin hepsi çizilir (ev kuralı: boş panel yok). */}
          {showHistory && (
            <div style={{
              borderBottom: '1px solid var(--border)', background: 'var(--bg1)',
              maxHeight: 240, overflowY: 'auto',
            }}>
              <div style={{
                display: 'flex', alignItems: 'center', gap: 8,
                padding: '6px 10px 6px 14px',
              }}>
                <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)' }}>
                  Geçmiş
                </span>
                <span style={{ flex: 1 }} />
                <Button variant="secondary" size="xs"
                  onClick={() => { clear(); setShowHistory(false); }}
                  title="Ekranı boşalt, yeni bir konuşma başlat">+ Yeni konuşma</Button>
              </div>
              {threads === undefined && (
                <div style={{ padding: '4px 14px 10px' }}>
                  <Spinner label="Konuşmalar yükleniyor…" />
                </div>
              )}
              {histErr && (
                <div style={{ padding: '0 14px 8px' }}>
                  <span className="badge b-err" title={histErr}>Geçmiş okunamadı</span>
                </div>
              )}
              {threads?.length === 0 && !histErr && (
                <Empty icon="🕘" compact title="Kayıtlı konuşma yok">
                  Bir soru sorduğunda konuşma otomatik saklanır.
                </Empty>
              )}
              {threads?.map(t => (
                <div key={t.id} style={{
                  display: 'flex', alignItems: 'center', gap: 6,
                  padding: '0 8px 0 14px',
                  background: t.id === conversationId ? 'var(--accent-soft)' : undefined,
                }}>
                  <Button variant="ghost" size="sm"
                    onClick={() => void openThread(t.id)}
                    title={t.title}
                    style={{
                      flex: 1, minWidth: 0, justifyContent: 'flex-start',
                      textAlign: 'left',
                    }}>
                    <span style={{
                      display: 'block', maxWidth: '100%',
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }}>{t.title}</span>
                  </Button>
                  <span style={{ fontSize: 10, color: 'var(--text3)', whiteSpace: 'nowrap' }}
                    title={`${t.messages} mesaj`}>
                    {tsRel(t.updatedAt)} · {t.messages}
                  </span>
                  <Button variant="ghost-danger" size="xs"
                    onClick={() => void removeThread(t)}
                    aria-label={`${t.title} konuşmasını sil`}
                    title="Konuşmayı sil">✕</Button>
                </div>
              ))}
            </div>
          )}

          {/* v0.10.483 — ✨ Explain kipi: aynı çekmece, açıklama + kanıt +
              çekmece sohbeti (AIDrawerBody). key: özne değişince sıfırlanır. */}
          {subject ? (
            <div style={{ flex: 1, overflowY: 'auto', padding: 'var(--sp-7)' }}>
              <AIDrawerBody key={formatAiParam(subject)} subject={subject} onClose={closeDrawer} />
            </div>
          ) : (<>
          {/* Messages */}
          <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 'var(--sp-7)', display: 'flex', flexDirection: 'column', gap: 10 }}>
            {turns.length === 0 && (
              <div style={{ color: 'var(--text3)', fontSize: 12 }}>
                {/* Karşılama (v0.9.528) — LLM çağrısı YOK; ad
                    /api/auth/me'den, durum açık P1'lerden. Durum satırı
                    yüklenirken BOŞ döner: "P1 yok" yanlış iddiası
                    operatörü yanıltırdı (greeting.ts). */}
                <div style={{ marginBottom: 4, color: 'var(--text)', fontSize: 13, fontWeight: 600 }}>
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
                  {/* v0.9.653 — EKRANDAKİ özne, statik çiplerden ÖNCE.
                      Operatör bir trace'e bakarken sohbeti açtığında
                      elindeki bağlam kaybolup ona takımının servisleri
                      soruluyordu. Vurgulu çizilir: tek anlamlı öneri o.
                      Otomatik açıklama DEĞİL — sohbeti açmak bir LLM
                      çağrısı tetiklememeli (gerekçe lib/chatContext.ts). */}
                  {ctxStarter && (
                    <Chip pill tone="accent" onClick={() => submit(ctxStarter.question)}>
                      ✨ {ctxStarter.chip}
                    </Chip>
                  )}
                  {STARTERS.map(q => (
                    <Chip key={q} pill onClick={() => submit(q)}>{q}</Chip>
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
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', padding: '0 var(--sp-7) 8px' }}>
              {(last.suggestions?.length ? last.suggestions : FOLLOWUPS).map(q => (
                <Chip key={q} pill onClick={() => submit(q)}>↳ {q}</Chip>
              ))}
            </div>
          )}

          {/* Composer */}
          <form
            onSubmit={e => { e.preventDefault(); submit(input); }}
            style={{ display: 'flex', gap: 8, padding: 'var(--sp-5) var(--sp-7)', borderTop: '1px solid var(--border)' }}>
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
            {/* v0.10.23 — DURDUR. AbortController zaten kuruluydu ama
                hiçbir affordance'a bağlı değildi; yerel gemma4 tek GPU'da
                koştuğu için istenmeyen bir 5-turlu döngü, operatörün
                sıradaki meşru sorusunun önünü dakikalarca tıkıyordu.
                Akarken Gönder'in YERİNİ alıyor: iki düğme yan yana
                durursa hangisinin etkin olduğu belirsizleşir. */}
            {busy ? (
              <Button variant="secondary" type="button" onClick={stop}
                title="Cevabı durdur — o ana kadar akan metin korunur">
                Durdur
              </Button>
            ) : (
              <Button variant="primary" type="submit" disabled={!input.trim()}>
                Gönder
              </Button>
            )}
          </form>
          </>)}
        </Drawer>
      )}
    </>
  );
}
