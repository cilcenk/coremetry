import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { LinkButton } from '@/components/ui/LinkButton';
import { Spinner, LoaderMark } from '@/components/Spinner';
import { useCopilotEnabled } from '@/components/ai/useCopilotEnabled';
import { aiSubjectQuestion } from '@/components/ai/drawerChat';
import type { AIKind } from '@/lib/aiSubject';
import type { AICodeContext } from '@/lib/types';
import { IconSparkles } from './icons';
import { ExplainBody, type ExplainEvidence } from '@/components/ai/ExplainBody';
import type { IdLink } from '@/components/ai/inlineIdLinks';
import { readAiCodeParam, writeAiCodeParam } from '@/lib/aiSubject';
import { AIFeedbackButtons } from '@/components/ai/AIFeedbackButtons';
import { shouldAskForCode, type CodeAskState } from './codeAsk';

// CopilotExplain — drop-in Explain button that calls the
// CoSRE (copilot) endpoint for the given subject and renders the
// reply inline beneath the button. Self-hides when the copilot is
// not configured (no API key on the server).
//
// v0.9.477 (onaylı AI-drawer mockup'ı): bu bileşen artık esas olarak
// AIDrawer'ın İÇİNDE, `auto` ile yaşıyor — yüzeylerdeki tetik
// AIExplainButton, cevabın evi tek sağ-kenar çekmece. Satır-içi kullanım
// (auto'suz) yalnızca zaten çekmece olan yüzeylerde kaldı
// (AnomalyDetailDrawer). Fetch/render mantığı DEĞİŞMEDİ.
//
// Six subject types share this component to avoid a button per call site:
//   - kind="trace"          → POST /api/copilot/explain-trace/{id}
//   - kind="problem"        → POST /api/copilot/explain-problem/{id}
//   - kind="incident"       → POST /api/copilot/explain-incident/{id}
//   - kind="anomaly"        → POST /api/copilot/explain-anomaly/{id}
//   - kind="service-health" → POST /api/copilot/explain-service?service=…&from=…&to=…
//                             ↑ takes (id=service, fromNs, toNs) instead of an
//                               ID lookup, because the prompt needs the live
//                               RED series for the current window.
//   - kind="runbook"        → POST /api/copilot/runbook/{id}
//                             ↑ same id shape as problem, but the model output
//                               is a numbered, actionable checklist anchored
//                               in past resolved instances of the same rule.
// Each endpoint uses a kind-specific system prompt so the model's
// answers match the operator's question.
//
// v0.9.831 — "Kodu da incele" (yalnız exception + trace): işaretlenince
// istek `includeCode:true` ile YENİDEN gider, sunucu stack trace'teki
// uygulama satırlarının kaynak kodunu Azure DevOps/TFS'ten çekip
// prompt'a ekler. Varsayılan KAPALI. Kod GELMEZSE cevabın başında tek
// satır dürüst not, geldiyse altında hangi depo/branş/dosya okunduğu.
// Kodun kendisi tarayıcıya inmez — yalnız künyesi.
export function CopilotExplain({ kind, id, label, fromNs, toNs, spanId, auto, onEvidence, onEvidenceTraces, onAnswer }: {
  kind: AIKind;
  id: string;
  label?: React.ReactNode;
  // v0.9.477 — AIDrawer içinde mount edildiğinde: açıklamayı mount'ta bir
  // kez çalıştır ve KENDİ tetik butonunu gizle. Tetik zaten yüzeydeki
  // "✨ Explain" butonuydu; çekmecede ikinci bir tık istemek olurdu.
  // Satır-içi (auto'suz) kullanım birebir eski davranışta.
  auto?: boolean;
  // v0.9.408 — kind="trace" yanıtındaki deterministik kanıt span'leri
  // (backend hesaplar; LLM'e bağımlı değil). Üst bileşen waterfall'ı
  // kutulamak için kullanır.
  onEvidence?: (spanIds: string[]) => void;
  // v0.9.414 — kind="exception" kanıt TRACE'leri; exception detayı
  // örnek-trace satırlarını kutular.
  onEvidenceTraces?: (traceIds: string[]) => void;
  // v0.9.479 — açıklama metnini yukarı ver. AI çekmecesi bunu sohbetin
  // BAĞLAMI yapar (`context.explain`): operatör raporunda chat ekrandaki
  // exception'ı bilmiyordu. onEvidence/onEvidenceTraces ile aynı sözleşme.
  onAnswer?: (text: string) => void;
  // Only used when kind === 'service-health'. Ignored otherwise.
  fromNs?: number;
  toNs?: number;
  // Only used when kind === 'span'. The target span inside the
  // trace identified by `id`. v0.5.144 per-span explain.
  spanId?: string;
}) {
  // v0.9.477 — config artık paylaşılan, modül düzeyinde cache'lenen hook'tan
  // geliyor (satır başına bir istek atan eski mount-içi fetch yerine).
  const enabled = useCopilotEnabled();
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState<string | null>(null);
  // v0.10.165 — kanıt kimlik SAYILARI (listeleri AIDrawer taşır); kart üstündeki Kanıt satırı.
  const [evidence, setEvidence] = useState<ExplainEvidence>({ spans: 0, traces: 0 });
  // v0.10.35 — sunucunun linklenebilir saydığı kimlikler (request_id).
  // Aynı köprü, çipin kullandığının aynısı; burada SATIR İÇİ çiziliyor.
  const [links, setLinks] = useState<IdLink[] | undefined>(undefined);
  const [meta, setMeta] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // v0.9.1121 (Faz 0.3b) — cevabın ai_calls kimliği; 👍/👎 buna asılı.
  // Her çalıştırmada SIFIRLANIR: "Yeniden sor" yeni bir kimlik üretir ve
  // eski cevabın oyu yeni cevabın üstünde kalamaz.
  const [exchangeId, setExchangeId] = useState<string | undefined>(undefined);
  // v0.9.409 (operatör isteği): buton ilk kullanıma kadar nabız atar —
  // gözden kaçıyordu. İlk tıklama kalıcı susturur (bu mount için).
  const [used, setUsed] = useState(false);
  // v0.9.831 — "Kodu da incele" (yalnız exception/trace). Varsayılan
  // KAPALI: kod okumak bir depo listelemesi + dosya çekmesi demek ve
  // her Explain tıkında ödenecek bir maliyet değil. İşaretlemek
  // isteği YENİDEN çalıştırır — kutuyu işaretleyip ekranda eski
  // cevabı bırakmak, olmayan bir kod analizini varmış gibi gösterir.
  //
  // v0.9.1238 — tercih artık HATIRLANIYOR (lib/aiCodePref). Çekmece her
  // özne için yeni bir mount kuruyor (AIDrawer `key`), yani kutu her
  // açılışta sıfırlanıyordu ve auto-koşu İLK turu her seferinde kodsuz
  // atıyordu; kutuyu yeniden işaretleyen operatör aynı soruya İKİNCİ bir
  // yerel LLM turu ödüyordu. İlk kullanım varsayılanı KAPALI kalır —
  // hatırlamak, yukarıdaki maliyet kararını çevirmek değil.
  //
  // Tohum `useState`'in TEMBEL biçiminde: ilk render zaten doğru değerle
  // gelmeli, çünkü auto-koşu efekti mount'ta çalışır. Sonradan bir
  // useEffect ile düzeltmek, kodsuz isteğin çoktan yola çıkmış olması
  // demekti (düzeltmeye çalıştığımız hatanın ta kendisi).
  const codeCapable = kind === 'exception' || kind === 'trace';
  // v0.10.60 — OPERATÖR KARARI: kutu HER AÇILIŞTA KAPALI başlar.
  //
  // v0.9.1238 tercihi hatırlıyordu (çekmece her özne için yeni mount
  // kurduğu için kutu sıfırlanıyor ve auto-koşu ilk turu kodsuz atıyordu).
  // Operatör bunu tersine çevirdi: "Kodu incele default seçili olmasın."
  // Hatırlama, kod okumanın maliyetini (depo listelemesi + dosya çekmesi +
  // ikinci bir yerel LLM turu) operatörün haberi olmadan her Explain'e
  // yayıyordu.
  // v0.10.81 — tohum URL'den (paylaşılan link kodlu açılsın; operatör:
  // "paylaştıktan sonra tekrar basmak gerekiyor"). TEMBEL başlatıcıda,
  // çünkü auto-koşu efekti mount'ta atıyor: sonradan düzeltmek, kodsuz
  // isteğin çoktan yola çıkması demekti — bu dosyanın kendi dersi.
  // Uygulama içi açılışta param zaten silinmiş olur (useAiSubject),
  // yani v0.10.60'ın "her açılışta kapalı" kararı bozulmuyor.
  const [includeCode, setIncludeCode] = useState(() => codeCapable && readAiCodeParam());
  const [code, setCode] = useState<AICodeContext | null>(null);
  // v0.10.83 — isabet yaşı; null = taze LLM cevabı.
  const [cachedAtMs, setCachedAtMs] = useState<number | null>(null);
  // v0.10.153 — "Kodu da inceleyeyim mi?" akışı (codeAsk.ts): ikinci geçiş
  // kendi state'ine akar, ilk cevap (text) DOKUNULMAZ.
  const [codeAsk, setCodeAsk] = useState<CodeAskState>('idle');
  const [codeText, setCodeText] = useState<string | null>(null);
  const [codeBusy, setCodeBusy] = useState(false);
  const [codeError, setCodeError] = useState<string | null>(null);
  const [codeLinks, setCodeLinks] = useState<IdLink[] | undefined>(undefined);
  const [codeExchangeId, setCodeExchangeId] = useState<string | undefined>(undefined);
  const codeAbortRef = useRef<AbortController | null>(null);

  // applyText — cevabı hem panele yaz hem üst bileşene duyur (v0.9.479).
  // BOŞ model cevabı bağlam olamaz: onAnswer'a boş string gider, çekmece
  // sohbeti açılmaz (bağlamsız sohbet operatör raporundaki hatanın ta
  // kendisiydi).
  const applyText = (raw: string) => {
    setText(raw || '⚠ Model boş yanıt döndürdü (reasoning modu + düşük max_tokens olabilir).');
    onAnswer?.(raw);
  };

  // v0.9.1127 (Faz 1.5) — akan cevap. `?stream=1` ile giden istek
  // token'ları onDelta'dan verir; panel onları BİRİKTİREREK çizer.
  //
  // Nihai metin YİNE `answer` çerçevesinden gelir ve biriken metni EZER
  // (applyText). Delta toplamı cevaba eşit OLMAYABİLİR: model reasoning
  // üretip cevabı sonda toparladığında sunucu düşünce bloğunu akıtmaz ve
  // kurtarılmış cevabı tek parça yollar. Delta'lar ilerleme gösterimi,
  // doğrunun kaynağı değil.
  //
  // Sunucu şeffaf biçimde buffered'a düşerse (akıyamayan uç) sıfır delta
  // gelir ve cevap tek seferde görünür — bu bileşende ayrı bir dal YOK.
  const abortRef = useRef<AbortController | null>(null);
  const streamOpts = (ac: AbortController) => ({
    onDelta: (d: string) => setText(prev => (prev ?? '') + d),
    signal: ac.signal,
  });

  // withCode parametresi state yerine ARGÜMAN: checkbox onChange'inden
  // hemen sonra run() çağrıldığında setState henüz uygulanmamış olur ve
  // istek eski değerle gider (React batching). Operatör kutuyu
  // işaretler, kodsuz cevap alır, nedenini göremez.
  // fresh (v0.10.83): yalnız "Yeniden sor" true geçer — sunucu explain
  // önbelleğini atlar. Otomatik/ilk koşular önbellekten faydalanır.
  const run = async (withCode = includeCode, fresh = false) => {
    // "Yeniden sor" uçuştaki akışı KESER. Kesilmezse eski akışın
    // delta'ları yeni cevabın üstüne yazmaya devam eder ve panelde iki
    // cevap iç içe geçer.
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    const opts = { ...streamOpts(ac), fresh };

    setUsed(true);
    setBusy(true); setError(null); setText(null); setMeta(null); setCode(null); setEvidence({ spans: 0, traces: 0 });
    setExchangeId(undefined);
    setCachedAtMs(null);
    // v0.10.153 — yeni ilk geçiş: kod geçişi ve karar sıfırlanır.
    codeAbortRef.current?.abort();
    setCodeAsk('idle'); setCodeText(null); setCodeBusy(false); setCodeError(null); setCodeLinks(undefined); setCodeExchangeId(undefined);
    onAnswer?.(''); // "Yeniden sor" bayat bağlamı taşımasın
    try {
      if (kind === 'runbook') {
        const r = await api.copilotRunbook(id, opts);
        applyText(r.explanation);
        setLinks(r.links);
        setExchangeId(r.exchangeId);
        setCachedAtMs(r.cached ? (r.cachedAtMs ?? 0) : null);
        // Surface the "based on N past resolutions" hint so the
        // operator knows whether the steps are grounded in
        // real history or first-principles.
        setMeta(r.similarCount > 0
          ? `Based on ${r.similarCount} past resolved instance${r.similarCount === 1 ? '' : 's'} of this rule on this service.`
          : `No past resolutions found — first-principles only.`);
      } else {
        const r = kind === 'trace'          ? await api.copilotExplainTrace(id, withCode, opts).then(rr => {
                                                  if (rr.evidenceSpanIds?.length) { onEvidence?.(rr.evidenceSpanIds); setEvidence(e => ({ ...e, spans: rr.evidenceSpanIds?.length ?? 0 })); }
                                                  setCode(rr.code ?? null);
                                                  setLinks(rr.links);
                                                  return rr;
                                                })
                : kind === 'exception'      ? await api.copilotExplainException(id, withCode, opts).then(rr => {
                                                  // Kanıt satırı span SAYMAZ: exception sayfasında span listesi
                                                  // çizilmiyor (AIDrawer v0.9.408 — gidilecek satır yok); yalnız
                                                  // trace kimlikleri listelenir, yalnız onlar sayılır.
                                                  if (rr.evidenceSpanIds?.length) onEvidence?.(rr.evidenceSpanIds);
                                                  if (rr.evidenceTraceIds?.length) { onEvidenceTraces?.(rr.evidenceTraceIds); setEvidence(e => ({ ...e, traces: rr.evidenceTraceIds?.length ?? 0 })); }
                                                  setCode(rr.code ?? null);
                                                  setLinks(rr.links);
                                                  return rr;
                                                })
                : kind === 'span'           ? await api.copilotExplainSpan(id, spanId ?? '', opts)
                : kind === 'problem'        ? await api.copilotExplainProblem(id, opts)
                : kind === 'incident'       ? await api.copilotExplainIncident(id, opts)
                : kind === 'anomaly'        ? await api.copilotExplainAnomaly(id, opts)
                :                             await api.copilotExplainServiceHealth(id, fromNs ?? 0, toNs ?? 0, opts);
        applyText(r.explanation);
        setLinks(r.links);
        setExchangeId(r.exchangeId);
        setCachedAtMs(r.cached ? (r.cachedAtMs ?? 0) : null);
      }
    } catch (e: unknown) {
      // İptal HATA DEĞİL: "Yeniden sor" kendi öncülünü keser, bileşen
      // unmount olurken de akış kesilir. Kullanıcının kendi eylemini
      // kırmızı bir kutu olarak göstermek yanlış (api.ts'in CanceledError
      // disiplini, v0.9.603).
      if (ac.signal.aborted) return;
      setError(e instanceof Error ? e.message : 'Explain failed');
    } finally {
      // Yalnız GÜNCEL akış spinner'ı kapatır — iptal edilmiş eski çağrı
      // yeni akışın "düşünüyor" durumunu düşüremez.
      if (abortRef.current === ac) setBusy(false);
    }
  };

  // Unmount: uçuştaki akışı kes. Çekmece kapanınca sunucuya doğru açık
  // kalan bir SSE bağlantısı, kimsenin okumadığı token'ları akıtmaya
  // devam eder.
  // v0.10.153 — Evet: ikinci explain (includeCode=true) "Kod incelemesi"
  // ayracı altına akar. İlk cevap korunur; kanıt span'leri kod geçişinden
  // gelirse vurgu güncellenir; URL'e ai=code yazılır ki paylaşılan/yenilenen
  // sayfa doğrudan kodlu açsın (eski davranış, aiCodeParam sözleşmesi).
  const runCode = async () => {
    codeAbortRef.current?.abort();
    const ac = new AbortController();
    codeAbortRef.current = ac;
    setCodeAsk('accepted');
    setCodeBusy(true); setCodeError(null); setCodeText(null); setCode(null);
    // Evet = kutuyu da işaretle (operatör: "Chip de kalsın"): "Yeniden sor"
    // artık tek turda kodlu koşar; URL'e de yazılır (deep link).
    setIncludeCode(true);
    writeAiCodeParam(true);
    const opts = { onDelta: (d: string) => setCodeText(prev => (prev ?? '') + d), signal: ac.signal, fresh: false };
    try {
      const finish = (r: { explanation: string; links?: IdLink[]; exchangeId?: string; code?: AICodeContext; evidenceSpanIds?: string[] }) => {
        // kod geçişi listeyi değiştirirse Kanıt sayısı da onu izler (trace: liste çizilir)
        if (r.evidenceSpanIds?.length) { onEvidence?.(r.evidenceSpanIds); if (kind === 'trace') setEvidence(e => ({ ...e, spans: r.evidenceSpanIds?.length ?? 0 })); }
        setCode(r.code ?? null);
        setCodeLinks(r.links);
        setCodeExchangeId(r.exchangeId);
        setCodeText(r.explanation || '⚠ Model boş yanıt döndürdü.');
      };
      if (kind === 'trace') {
        finish(await api.copilotExplainTrace(id, true, opts));
      } else {
        const r = await api.copilotExplainException(id, true, opts);
        if (r.evidenceTraceIds?.length) onEvidenceTraces?.(r.evidenceTraceIds);
        finish(r);
      }
    } catch (e: unknown) {
      if (ac.signal.aborted) return;
      setCodeError(e instanceof Error ? e.message : 'Kod incelemesi başarısız');
    } finally {
      if (codeAbortRef.current === ac) setCodeBusy(false);
    }
  };
  useEffect(() => () => { abortRef.current?.abort(); codeAbortRef.current?.abort(); }, []);

  // Explain→chat köprüsü (v0.9.165): açıklamayı okuduktan sonra tek tıkla
  // global CoSRE penceresinde devam et — konuya uygun bir soruyla açılır.
  // v0.9.479: bu köprü artık YALNIZ satır-içi kullanımda (auto'suz, örn.
  // AnomalyDetailDrawer). AI çekmecesinde global pencereyi çekmecenin
  // üstüne açıyordu ve sohbet ekrandaki açıklamayı bilmiyordu (operatör
  // raporu) — orada devam düğmesini çekmece kendi içinde çiziyor.
  const askInChat = () =>
    window.dispatchEvent(new CustomEvent('coremetry:ai-ask', {
      detail: { question: aiSubjectQuestion(kind, id) },
    }));

  // auto: çekmece bu özneyle açıldı → tek sefer çalıştır. Ref muhafızı
  // StrictMode'un çift-mount'unu (dev) ikinci bir LLM çağrısına çevirmesin;
  // özne değişimi AIDrawer'da `key` ile yeni bir mount olduğundan burada
  // bağımlılık takibine gerek yok.
  const autoRan = useRef(false);
  useEffect(() => {
    if (!auto || enabled !== true || autoRan.current) return;
    autoRan.current = true;
    void run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, enabled]);

  if (enabled !== true) return null;

  // Çekmecede tetik buton yok: sonuç gelene kadar Spinner, sonrasında
  // "Yeniden sor" (hata hâlinde tekrar deneme yolu da bu).
  const showButton = !auto || (!busy && (text !== null || error !== null));

  return (
    // v0.10.155 (operatör: "sayfanın ortasında değil") — çekmece (auto)
    // modunda kök TAM GENİŞLİK blok: inline-flex + flex-start, yükleniyor
    // bloğunu içerik genişliğinde sol-üste sıkıştırıyordu. Satır-içi
    // yüzeylerde (buton) inline-flex aynen kalır.
    <div style={{
      display: auto ? 'flex' : 'inline-flex', flexDirection: 'column', gap: 8,
      alignItems: 'flex-start', maxWidth: '100%', width: auto ? '100%' : undefined,
    }}>
            {/* v0.10.60 — DEPO LİNKİ KUTUNUN ALTINDA (operatör isteği).
          Depo adı çoğu kurulumda bir KONVANSİYON TAHMİNİ; operatörün o
          tahmini doğrulamasının tek yolu linke bakmak. Link SUNUCUDAN
          geliyor (taban adres + koleksiyon + proje yalnız orada biliniyor);
          burada yeniden kurmak iki yazımın sessizce ayrışmasına izin
          verirdi. URL yoksa hiç çizilmiyor — yanlış link, link
          olmamasından kötüdür. */}
{codeCapable && (
        // v0.9.1184 (operatör: "Kodu incele checkboxı da çok küçük daha
        // belirgin olabilir") — 11px'lik çıplak etiket dipnot gibi
        // okunuyordu, oysa bu bir KARAR: cevabın koda bakıp bakmayacağını
        // belirliyor ve yanındaki accent butonla aynı satırda yaşıyor.
        //
        // Yeni bir görünüm icat etmedim; deponun `.btn-chip` sözlüğüne
        // normalize ettim (`ch-sm` + işaretliyken `active`). Kazancı iki
        // katlı: kutu artık bir kontrol gibi çerçeveli, ve İŞARETLİ durum
        // uygulamanın her yerindeki "açık" durumuyla AYNI aksan tonunu
        // kullanıyor — durumu okumak için kutucuğun içine bakmak gerekmiyor.
        <Chip size="sm" active={includeCode} disabled={busy}
          title="Stack trace'teki uygulama satırlarının kaynak kodunu da modele ver (Ayarlar → Kod entegrasyonu gerekir)"
          onClick={() => {
            const next = !includeCode;
            setIncludeCode(next);
            // Seçim adrese yazılır: Copy link ekrandakini AYNEN üretir.
            writeAiCodeParam(next);
            // Kutuyu değiştirmek isteği YENİDEN çalıştırır — aksi
            // halde ekrandaki cevap kutunun durumuyla çelişirdi.
            if (text !== null || error !== null || auto) void run(next);
          }}>
          {/* Durum ÜÇ kez söyleniyor ve hiçbiri yeni CSS istemiyor: glif,
              çipin aksan tonu ve atomun bastığı aria-pressed. Yerel
              <input type=checkbox> yerine glif, çünkü atom bir <button> ve
              buton içine form kontrolü koymak sahte bir affordance olurdu —
              tık zaten butonun. */}
          <span aria-hidden="true">{includeCode ? '☑' : '☐'}</span>
          <span>Kodu da incele</span>
        </Chip>
      )}
      {includeCode && code?.browseUrl && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, wordBreak: 'break-all' }}>
          <a href={code.browseUrl} target="_blank" rel="noreferrer noopener"
             style={{ color: 'var(--accent2)' }} title="Depoyu tarayıcıda aç">{code.browseUrl}</a>
          {code.source && <span> · kaynak: {code.source === 'pin' ? 'katalog pini' : 'konvansiyon'}</span>}
        </div>
      )}
      {/* v0.9.1127 — Spinner yalnız İLK token'a kadar. Token'lar akmaya
          başladıktan sonra ilerlemeyi metnin kendisi gösteriyor; ikisini
          birlikte çizmek "hem bekliyor hem yazıyor" gibi okunur. */}
      {auto && busy && text === null && (
        // v0.10.152 (operatör): küçük köşe spinner'ı yerine gövde ortasında
        // BÜYÜK, OTel işaretli yükleniyor durumu — "loading page" gibi.
        <div style={{ display: 'grid', placeItems: 'center', width: '100%', alignSelf: 'stretch', minHeight: 'min(60vh, 420px)', padding: '32px 16px' }}>
          <LoaderMark size="lg"
            label={includeCode ? 'CoSRE kodu okuyor…' : 'CoSRE düşünüyor…'}
            hint={includeCode ? 'Kanıt span\'leri, trace ve ilgili kaynak kodu birlikte inceleniyor.' : 'Kanıt span\'leri ve trace bağlamı üzerinden açıklama üretiliyor.'} />
        </div>
      )}
      {showButton && (
        <Button variant="accent" size="sm" onClick={() => void run(includeCode, true)} disabled={busy}
          className={used || auto ? undefined : 'ai-attn'}>
          {busy
            ? <><IconSparkles /> <span>Thinking…</span></>
            : auto
              ? <><IconSparkles /> <span>Yeniden sor</span></>
              : (label ?? <><IconSparkles /> <span>AI explain</span></>)}
        </Button>
      )}
      {error && (
        <div style={{
          padding: 10, borderRadius: 6, fontSize: 12,
          background: 'rgba(255,82,82,.10)', color: 'var(--err)',
          border: '1px solid rgba(255,82,82,.25)', maxWidth: 'min(720px, 100%)',
        }}>{error}</div>
      )}
      {text && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'color-mix(in srgb, var(--accent) 8%, transparent)',
          border: '1px solid color-mix(in srgb, var(--accent) 25%, transparent)',
          color: 'var(--text)', maxWidth: 'min(720px, 100%)',
        }}>
          {/* v0.9.1380 (operatör: "CoSRE yazısı daha büyük font olabilir
              burada") — 10→14, ikon 11→13.

              Bu, v0.9.1253'ün İKİZİ. O sürüm operatörün neredeyse aynı
              cümlesiyle ("CoSRE yazısı biraz daha belirgin, büyük font
              olabilir") sohbet çekmecesinin başlığını 13→16 yaptı;
              explain paneli 10px'te kaldı ve marka, ETİKETLEDİĞİ
              içerikten küçük göründü.

              16 DEĞİL, 14: sohbetteki bir çekmece BAŞLIĞI, burası ise
              13px'lik bir gövdenin üstündeki bölüm etiketi. 16 yapmak
              etiketi kendi içeriğinden büyük gösterirdi — operatörün
              istediği belirginlik değil, ters bir hiyerarşi olurdu.
              RUNBOOK de aynı etiketi kullanıyor, o da büyüyor. */}
          <div style={{ fontSize: 14, color: 'var(--accent2)', marginBottom: 6, fontWeight: 700, letterSpacing: '.5px',
                        display: 'inline-flex', alignItems: 'center', gap: 5 }}>
            <IconSparkles size={13} /> {kind === 'runbook' ? 'RUNBOOK' : 'CoSRE'}
          </div>
          {meta && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, fontStyle: 'italic' }}>
              {meta}
            </div>
          )}
          {/* v0.9.831 — DÜRÜSTLÜK NOTU. Operatör "Kodu da incele"yi
              işaretledi ama kod gelmedi: cevabın BAŞINDA tek satır
              söylenir. Sessiz kalmak, kodsuz bir çıkarımı kod
              destekliymiş gibi okutur — bu yüzeyin verebileceği en
              pahalı yanlış izlenim. */}
          {code && !code.files?.length && (
            <div style={{
              fontSize: 11, color: 'var(--warn, var(--text3))', marginBottom: 8,
              display: 'flex', gap: 6, alignItems: 'flex-start',
            }}>
              <span>⚠</span>
              <span>Kod okunamadı — <strong>kodsuz</strong> analiz.
                {code.reason ? ` (${code.reason})` : ''}</span>
            </div>
          )}
          {/* v0.9.641 — operatör-bildirimli: "neden kök neden ipucu veya
              öncelikli inceleme başlıkları bold yazmıyor". Model markdown
              üretiyor (**Kök Neden İpucu:**) ama burası {text}'i HAM
              basıyordu, yıldızlar ekranda görünüyordu.

              RenderedMarkdown ZATEN vardı (v0.7.0, Notebook'tan çıkarıldı)
              ve **bold** / başlık / madde / `kod` / link destekliyor —
              yalnız Runbook sayfaları kullanıyordu. Bileşen var, AI
              yüzeyi ondan habersizdi.

              whiteSpace:'pre-wrap' KALKTI: Markdown kendi blok düzenini
              kuruyor, ikisi birlikte satır aralarını ikiye katlıyordu. */}
          {cachedAtMs !== null && (
            /* v0.10.83 — İSABET ETİKETİ. Önbellekten gelen cevabı taze
               sanmak, bu gecenin kapattığı sınıfın ta kendisi olurdu.
               "Yeniden sor" zaten taze üretir; etiket bunu da söylüyor. */
            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>
              ♻ önbellekten{cachedAtMs > 0 ? ` · ${Math.max(1, Math.round((Date.now() - cachedAtMs) / 60000))} dk önce üretildi` : ''} — taze cevap için “Yeniden sor”
            </div>
          )}
          {/* kod kartı açıkken Karar'ı o kart taşır — iki rakip KARAR şeridi olmaz */}
          <ExplainBody text={text} busy={busy} links={links} evidence={evidence} verdict={codeAsk !== 'accepted'} />
          {/* v0.9.1127 — akan cevabın imleci (ChatBubble'ın `.cm-ai-cursor`
              atomu). Cevap BİTMEDEN de metin görünür olduğu için, bittiğini
              gösteren bir işaret gerekiyor: imleçsiz akan metin yarıda
              kesilmiş bir cevaptan ayırt edilemez. */}
          {busy && <span className="cm-ai-cursor" />}
          {/* Kaynak satırı: hangi depo/branş okundu, hangi dosyalar.
              Depo çözümü bir TAHMİN olabilir (konvansiyon) — cevabın
              yanlış dosyaya dayandığından şüphelenen operatör bunu
              görmeden anlayamaz. */}
          {code && !!code.files?.length && (
            <div style={{ marginTop: 10, fontSize: 10.5, color: 'var(--text3)', lineHeight: 1.6 }}>
              <div>
                📄 Kaynak: <strong>{code.repo}</strong>
                {code.branch ? ` · ${code.branch}` : ''}
                {code.source === 'pin' ? ' · katalog pini' : code.source === 'convention' ? ' · ad konvansiyonu' : ''}
              </div>
              {/* v0.9.1236 — kod GELDİĞİNDE de bir not olabilir: depo adı
                  sunucunun listesine bakılarak düzeltilmiş olabilir
                  ("cashmanagement-cashflow → CashManagement.CashFlow") ya
                  da bütçe dolduğu için pencereler kısalmış olabilir.
                  Eskiden bu satır yalnız kod GELMEYİNCE çiziliyordu, yani
                  başarılı ama DÜZELTİLMİŞ bir çekmede operatör yukarıdaki
                  depo adının neden beklediğinden farklı olduğunu hiçbir
                  yerden öğrenemiyordu. */}
              {code.reason && (
                <div style={{ color: 'var(--warn, var(--text3))' }}>{code.reason}</div>
              )}
              {code.files.map(f => (
                <div key={`${f.path}:${f.fromLine}`} style={{ fontFamily: 'var(--mono, monospace)' }}>
                  {/* v0.10.353 (operatör) — dosya+satır DevOps'ta açılır (url sunucudan, FileURL). */}
                  {f.url
                    ? <a href={f.url} target="_blank" rel="noreferrer" title={`DevOps'ta aç: ${f.path}${f.line ? ` satır ${f.line}` : ''}`}>{f.path}:{f.fromLine}-{f.toLine} ↗</a>
                    : <>{f.path}:{f.fromLine}-{f.toLine}</>}
                  {/* v0.9.1254 — modelin ">>>" ile işaretlendiği satır;
                      içerik tarayıcıya gitmez, numara yeter. */}
                  {!!f.line && <span style={{ color: 'var(--text3)' }}> · hata satırı {f.line}</span>}
                </div>
              ))}
            </div>
          )}
          {/* v0.9.1121 (Faz 0.3b) — 👍/👎. Kimlik gelmediyse (eski gövde,
              cache'li yüzey) HİÇ çizilmez; bileşenin kendi kapısı. */}
          <AIFeedbackButtons exchangeId={exchangeId} />
          {/* v0.9.479 — çekmecede (auto) bu link ÇİZİLMEZ: sohbet aynı
              çekmecenin içinde, ekrandaki açıklamayı bağlam alarak açılır
              (AIDrawer). Satır-içi yüzeylerde köprü aynen duruyor. */}
          {!auto && (
            <div style={{ marginTop: 8 }}>
              <LinkButton onClick={askInChat}
                title="CoSRE chat'te bu konuda devam et"
                style={{ fontSize: 11 }}>
                💬 Chat'te devam et →
              </LinkButton>
            </div>
          )}
        </div>
      )}
      {/* v0.10.153 — soru satırı: yalnız ilk cevap bittikten sonra, kod-yetenekli
          türde, URL kodlu değilken ve karar verilmemişken (codeAsk.ts). */}
      {shouldAskForCode({ codeCapable, includeCode, hasText: text !== null, busy, hasError: error !== null, state: codeAsk }) && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', fontSize: 13, maxWidth: 'min(720px, 100%)' }}>
          <IconSparkles size={13} />
          <span>Kodu da inceleyeyim mi?</span>
          <Button variant="accent" size="sm" onClick={() => void runCode()}
            title="Stack trace'teki uygulama satırlarının kaynak kodunu da modele ver (Ayarlar → Kod entegrasyonu gerekir)">Evet</Button>
          <Button variant="secondary" size="sm" onClick={() => setCodeAsk('declined')}>Hayır, yeterli</Button>
        </div>
      )}
      {codeAsk === 'accepted' && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'color-mix(in srgb, var(--accent) 8%, transparent)',
          border: '1px solid color-mix(in srgb, var(--accent) 25%, transparent)',
          color: 'var(--text)', maxWidth: 'min(720px, 100%)',
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 14, fontWeight: 600, marginBottom: 8 }}>
            <IconSparkles size={13} /> Kod incelemesi
            <span style={{ fontSize: 11, fontWeight: 400, color: 'var(--text3)' }}>· kaynak kodla yeniden değerlendirildi; yukarıdaki ilk cevap korunur</span>
          </div>
          {codeBusy && codeText === null && (
            <div style={{ display: 'grid', placeItems: 'center', minHeight: 200, padding: '24px 16px' }}>
              <LoaderMark size="lg" label="CoSRE kodu okuyor…" hint="Kanıt span'leri, trace ve ilgili kaynak kodu birlikte inceleniyor." />
            </div>
          )}
          {codeError && (
            <div style={{ padding: 10, borderRadius: 6, fontSize: 12, background: 'rgba(255,82,82,.10)', color: 'var(--err)', border: '1px solid rgba(255,82,82,.25)' }}>{codeError}</div>
          )}
          {code && !code.files?.length && (
            <div style={{ fontSize: 11, color: 'var(--warn, var(--text3))', marginBottom: 8, display: 'flex', gap: 6, alignItems: 'flex-start' }}>
              <span>⚠</span>
              <span>Kod okunamadı — <strong>kodsuz</strong> analiz.{code.reason ? ` (${code.reason})` : ''}</span>
            </div>
          )}
          {codeText && <ExplainBody text={codeText} busy={codeBusy} links={codeLinks} />}
          {codeBusy && codeText !== null && <span className="cm-ai-cursor" />}
          {code && !!code.files?.length && (
            <div style={{ marginTop: 10, fontSize: 10.5, color: 'var(--text3)', lineHeight: 1.6 }}>
              <div>📄 Kaynak: <strong>{code.repo}</strong>{code.branch ? ` · ${code.branch}` : ''}{code.source === 'pin' ? ' · katalog pini' : code.source === 'convention' ? ' · ad konvansiyonu' : ''}</div>
              {code.reason && <div style={{ color: 'var(--warn, var(--text3))' }}>{code.reason}</div>}
              {code.files.map(f => (
                <div key={`${f.path}:${f.fromLine}`} style={{ fontFamily: 'var(--mono, monospace)' }}>
                  {f.url
                    ? <a href={f.url} target="_blank" rel="noreferrer" title={`DevOps'ta aç: ${f.path}${f.line ? ` satır ${f.line}` : ''}`}>{f.path}:{f.fromLine}-{f.toLine} ↗</a>
                    : <>{f.path}:{f.fromLine}-{f.toLine}</>}{!!f.line && <span style={{ color: 'var(--text3)' }}> · hata satırı {f.line}</span>}
                </div>
              ))}
            </div>
          )}
          {!codeBusy && codeText && <AIFeedbackButtons exchangeId={codeExchangeId} />}
        </div>
      )}
    </div>
  );
}
