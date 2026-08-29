import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import type { ChatMessage, ChatTurn } from '@/lib/types';
import { rateTurn } from './ChatBubble';
import { isAbortError, settleStoppedTurn } from './chatAbort';
import {
  PERSIST_DEBOUNCE_MS, hasCompletedExchange, persistMessages, restoreTurns,
} from './chatPersist';

// useChatThread — sohbet turlarının state'i + gönderme döngüsü
// (v0.9.479). CopilotChat.tsx'in içinden çıkarıldı; AI çekmecesindeki
// sohbet AYNI çekirdeği kullanır (ikinci bir chat implementasyonu yok).
// Yüzeyler yalnız KABUKTA ayrışır: global pencere sağ-alt FAB + kendi
// drawer'ı, çekmece sohbeti ise açıklamanın altındaki bölüm.
//
// Her gönderimde tüm geçmiş /api/copilot/chat'e postlanır, SSE tüketilir.
//
// v0.9.1139 (Faz 4.1) — konuşma artık `persist: true` ile KALICI:
// tamamlanan her alışverişten sonra saved_views(page='ai-chat') satırına
// yazılır (POST /api/ai/conversations). Kimliği SUNUCU basar, istemci
// yanıttan devralır ve sonraki yazımlarda taşır.
//
// v0.10.55 (operatör ürün kararı) — kalıcılık artık AI çekmecesine de
// AÇIK: "Chat'te devam et" sohbeti de saved_views(page='ai-chat')
// satırına yazılıyor, aynı "🕘 Geçmiş" listesinde CoSRE penceresiyle
// yan yana görünür. v0.9.1139'daki "NPE fp-abc123 kabuğu" kaygısı iki
// yerde zaten karşılanıyor: (1) başlık ÖZNE kodeğinden değil, ekrandaki
// gerçek ilk kullanıcı mesajından türüyor (resolveChatTitle, backend) —
// çekmece çağrısı ayrıca AÇIK bir `title` de basıyor (AIDrawer.tsx,
// aiSubjectTitle+aiSubjectSubtitle) ki thread listede "Explain trace ·
// a1b2c3d4…" gibi tanınabilir dursun; (2) tek-mesajlık boş açıklama
// turları zaten arşive girmiyor — hasCompletedExchange en az bir takip
// sorusu + tamamlanmış cevap şart koşuyor.
//
// Kaydetme HİÇBİR ZAMAN gönderme yolunu bloklamaz: ateşle-ve-unut,
// hatası yutulur (sonraki tur zaten tüm geçmişi yeniden yazacak).

export interface ChatThreadOpts {
  // Context-awareness (v0.9.164/184) — bulunulan sayfanın servisi ve
  // seçili operasyonu; sunucudaki guided router varsayılan alır.
  service?: string;
  operation?: string;
  // explain (v0.9.479) — AI çekmecesindeki açıklamanın metni. Sunucu
  // narration bloğuna katar; boşken tüm davranış global chat'inkiyle
  // aynıdır (internal/api/copilot_drawer.go).
  explain?: string;
  // subject (v0.9.482) — çekmecenin öznesi, `?ai=` kodeği biçiminde
  // (formatAiParam). Sunucu bundan ilgili explain'in HAM KANITINI
  // (trace span'leri + ilişkili loglar / exception paketi) yeniden kurar;
  // açıklamanın metni takip sorularına yetmiyordu (operatör raporu).
  // Global sohbette YOKTUR — orada guided/tool yolları veriyi kendi çeker.
  subject?: string;
  // seed (v0.9.479) — tele giden ama EKRANDA ÇİZİLMEYEN ön turlar.
  // Çekmece, öznesinin doğal sorusunu buraya koyar: sunucudaki
  // takip-devralma (v0.9.410) onu "önceki soru" olarak görür, böylece
  // "peki hata logları?" gibi takipler guided router'da servise oturur.
  seed?: ChatMessage[];
  // rangeS (v0.9.529) — EKRANDAKİ zaman aralığı, saniye. Soru açık bir
  // pencere taşımıyorsa sunucu sabit 30dk yerine bunu kullanır; soru
  // pencere taşıyorsa ("son 24 saatte…") soru kazanır.
  rangeS?: number;
  // toMs (v0.10.33) — MUTLAK pencerenin bitiş anı; yalnız custom/zoom
  // aralıkta dolu. Göreli aralıkta boş bırakılıyor ki sunucu şimdiye
  // çapalasın — sabitlemek cevabı dondururdu.
  toMs?: number;
  // trace (v0.9.537) — EKRANDAKİ trace ID'si (/trace?id=). "bu trace
  // neden yavaş" gibi ID'siz sorular sunucuda buna oturur.
  trace?: string;
  // env (v0.9.1259) — Topbar'daki global env seçimi (rangeS aynası).
  env?: string;
  // persist (v0.9.1139) — konuşma sunucuda saklansın mı. Global CoSRE
  // penceresi ve (v0.10.55'ten beri) AI çekmecesi ikisi de true.
  persist?: boolean;
  // title (v0.10.55) — kalıcı satır için AÇIK başlık. Boşsa backend ilk
  // kullanıcı mesajından türetir (global CoSRE'nin bugüne kadarki
  // davranışı); çekmece bunu ÖZNEYE göre basıyor (aiSubjectTitle +
  // aiSubjectSubtitle) ki "Geçmiş" listesinde hangi trace/span/problem
  // olduğu takip sorusunun lafından değil, açıklamanın öznesinden okunsun.
  title?: string;
}

export function useChatThread(opts: ChatThreadOpts = {}) {
  const [turns, setTurns] = useState<ChatTurn[]>([]);
  const [busy, setBusy] = useState(false);
  // send stabil kimlikli (useCallback []) — güncel seçenekleri/turları
  // ref üstünden okur, böylece her render'da yeniden kurulmaz ve
  // CopilotChat'in `coremetry:ai-ask` köprüsü bayat closure çağırmaz.
  const optsRef = useRef(opts);
  optsRef.current = opts;
  const turnsRef = useRef(turns);
  turnsRef.current = turns;
  const busyRef = useRef(false);
  const abortRef = useRef<AbortController | null>(null);
  // Kalıcılık (v0.9.1139). conversationId state OLARAK da tutuluyor ki
  // kabuk "kayıtlı thread" bilgisini çizebilsin; yazım yolu ref'i okur
  // (bayat closure yok).
  const [conversationId, setConversationId] = useState<string | null>(null);
  const convIdRef = useRef<string | null>(null);
  const saveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    abortRef.current?.abort();
    if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
  }, []);

  // schedulePersist — ATEŞLE VE UNUT. `send` bunu await ETMEZ ve
  // etmemeli: kaydetme, cevabın gösterilmesiyle ya da sıradaki soruyla
  // aynı yolda değil. Hata sessiz yutuluyor — sonraki tur tüm geçmişi
  // yeniden yazacağı için kayıp kendini onarır; bir toast, çalışan
  // sohbetin ortasında yanlış alarm olurdu.
  const schedulePersist = useCallback(() => {
    if (!optsRef.current.persist) return;
    if (saveTimerRef.current) clearTimeout(saveTimerRef.current);
    saveTimerRef.current = setTimeout(() => {
      saveTimerRef.current = null;
      const snapshot = turnsRef.current;
      if (!hasCompletedExchange(snapshot)) return;
      void api.saveAiConversation({
        id: convIdRef.current ?? undefined,
        title: optsRef.current.title || undefined,
        subject: optsRef.current.subject || undefined,
        messages: persistMessages(snapshot),
      }).then(c => {
        // Kimliği SUNUCU basar. Aynı thread'e yazmaya devam etmek için
        // yanıttan devralıyoruz — silinmiş bir thread'e yazım sunucuda
        // YENİ kimlikle açılır ve o kimlik de buradan devralınır.
        convIdRef.current = c.id;
        setConversationId(c.id);
      }).catch(() => { /* sessiz — sonraki tur yeniden dener */ });
    }, PERSIST_DEBOUNCE_MS);
  }, []);

  const send = useCallback(async (text: string) => {
    const q = text.trim();
    if (!q || busyRef.current) return;
    const o = optsRef.current;
    const history: ChatMessage[] = [
      ...(o.seed ?? []),
      // v0.10.63 — DURDURULAN TUR YARIM OLDUĞUNU SÖYLER.
      //
      // ⚠ `stopped` bayrağı burada DÜŞÜYORDU: yalnız {role,text} geçiyor.
      // Yani operatörün yarıda kestiği cevap modele TAMAMLANMIŞ kendi
      // cevabı gibi geri gidiyor ve model kendi yarım cümlesinin üstüne
      // inşa ediyordu — "bir önceki cevabımda dediğim gibi…" diye devam
      // ettiği şey hiç söylenmemiş olabiliyor.
      //
      // Tur ELENMİYOR: operatör ona atıfta bulunabilir ("az önceki listeyi
      // tamamla"). Elenirse o atıf bağlamsız kalırdı. Yarımlık METNE
      // yazılıyor, ki model neye baktığını bilsin.
      ...turnsRef.current.filter(t => !t.error).map(t => ({
        role: t.role,
        text: t.stopped ? `${t.text}\n\n[Bu cevap operatör tarafından YARIDA DURDURULDU — tamamlanmadı.]` : t.text,
      })),
      { role: 'user', text: q },
    ];
    setTurns(prev => [
      ...prev,
      { role: 'user', text: q },
      { role: 'assistant', text: '', steps: [], pending: true },
    ]);
    busyRef.current = true;
    setBusy(true);
    const ac = new AbortController();
    abortRef.current = ac;

    const patchLast = (fn: (t: ChatTurn) => ChatTurn) =>
      setTurns(prev => prev.map((t, i) => (i === prev.length - 1 ? fn(t) : t)));

    try {
      await api.copilotChat(history, (e) => {
        if (e.kind === 'step') {
          // v0.9.1181 (Faz 4.3) — etiket ile detay AYRI birikiyor. Detay
          // burada `preview`siz açılıyor: çip tool çalışmadan ÖNCE
          // görünmeli (ilerleme geri bildirimi), veri sonra doluyor.
          patchLast(t => ({
            ...t,
            // v0.10.161 — etiket adımı (tool yok) çipte etiketiyle görünür;
            // detayda tool '' kalır → şeffaflık paneli o satırı çizmez/saymaz.
            steps: [...(t.steps ?? []), e.tool ?? e.label ?? ''],
            stepDetails: e.i == null
              ? t.stepDetails
              : [...(t.stepDetails ?? []), { i: e.i, tool: e.tool ?? '', label: e.label, args: e.args, origin: e.origin }],
          }));
        } else if (e.kind === 'step-result') {
          // Sonuç, `i` ile kendi çipine yazılır. Eşleşme bulunamazsa
          // SESSİZCE düşer — eski bir sunucuya karşı akan bir FE'de
          // (rolling deploy) `step` olayı `i` taşımaz ve o çip detaysız,
          // yani tıklanamaz kalır: eksik affordance, kırık affordance'tan
          // iyidir.
          patchLast(t => ({
            ...t,
            stepDetails: (t.stepDetails ?? []).map(d =>
              d.i === e.i
                ? { ...d, ok: e.ok, preview: e.preview, truncated: e.truncated, bytes: e.bytes, href: e.href, durationMs: e.durationMs }
                : d),
          }));
        } else if (e.kind === 'delta') {
          patchLast(t => ({ ...t, text: (t.text ?? '') + e.text }));
        } else if (e.kind === 'answer') {
          patchLast(t => ({ ...t, text: e.text, exchangeId: e.exchangeId, sources: e.sources, suggestions: e.suggestions, links: e.links, pending: false }));
        } else if (e.kind === 'error') {
          patchLast(t => ({ ...t, error: e.error, pending: false }));
        } else if (e.kind === 'done') {
          patchLast(t => ({ ...t, pending: false }));
        }
      }, ac.signal, o.service || undefined, o.operation || undefined, o.explain || undefined,
        o.subject || undefined, o.rangeS || undefined, o.trace || undefined, o.env || undefined,
        o.toMs || undefined);
    } catch (err) {
      // v0.10.23 — İPTAL ARIZA DEĞİL. Durdurulan bir fetch AbortError
      // fırlatıyor; ayırmazsak operatörün kasıtlı eylemi kırmızı bir
      // "⚠ signal is aborted" balonuna dönüşür, yani düzeltme yeni bir
      // kusur üretir. Akan metin KORUNUYOR (chatAbort.ts).
      if (isAbortError(err)) {
        patchLast(settleStoppedTurn);
      } else {
        patchLast(t => ({ ...t, error: err instanceof Error ? err.message : String(err), pending: false }));
      }
    } finally {
      busyRef.current = false;
      setBusy(false);
      abortRef.current = null;
      // TEK tetikleyici: bir alışveriş bitti (cevap ya da hata). Hata
      // turu persistMessages'ta düşer, yani hatalı bir tur arşivi
      // kirletmez ama ondan ÖNCEKİ turlar korunur.
      schedulePersist();
    }
  }, [schedulePersist]);

  const rate = useCallback((idx: number, verdict: 1 | -1) => {
    rateTurn(turnsRef.current, idx, verdict, setTurns);
  }, []);

  // clear = YENİ KONUŞMA (v0.9.1139'da anlamı genişledi). Turların
  // yanında kalıcı kimlik de düşer: aksi hâlde "Temizle" sonrası ilk
  // yazım, arşivdeki dolu thread'in ÜSTÜNE boş/yeni bir gövde yazardı.
  // Bekleyen bir kaydetme de iptal edilir (o zamanlayıcı silinmiş
  // turları yazmak üzereydi).
  const clear = useCallback(() => {
    if (saveTimerRef.current) {
      clearTimeout(saveTimerRef.current);
      saveTimerRef.current = null;
    }
    setTurns([]);
    convIdRef.current = null;
    setConversationId(null);
  }, []);

  // load — arşivden bir thread'i ekrana getirir. Akış sürerken
  // yüklemiyoruz: yarı yazılmış bir cevabın üstüne başka bir konuşmayı
  // basmak, gelen SSE parçalarının yanlış thread'e yapışması demekti.
  const load = useCallback(async (id: string) => {
    if (busyRef.current) return;
    if (saveTimerRef.current) {
      clearTimeout(saveTimerRef.current);
      saveTimerRef.current = null;
    }
    const c = await api.aiConversation(id);
    setTurns(restoreTurns(c.messages));
    convIdRef.current = c.id;
    setConversationId(c.id);
  }, []);

  // Takip çipleri yalnız son tur TAMAMLANMIŞ bir asistan cevabıysa görünür.
  const last = turns[turns.length - 1];
  const showFollowups = !busy && !!last && last.role === 'assistant' && !last.pending && !last.error && !!last.text;

  // v0.10.23 — İPTAL AFFORDANCE'I. AbortController zaten kuruluydu ama
  // yalnız unmount'ta ateşleniyordu ve CopilotChat AppShell'de KALICI
  // monte, yani çekmeceyi kapatmak bile akışı durdurmuyordu. Tek GPU'da
  // istenmeyen bir 5-turlu döngü, sıradaki meşru soruyu dakikalarca
  // tıkıyordu.
  const stop = useCallback(() => {
    abortRef.current?.abort();
  }, []);

  return { turns, busy, send, stop, rate, clear, load, conversationId, last, showFollowups };
}
