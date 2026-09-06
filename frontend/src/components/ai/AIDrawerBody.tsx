import { useEffect, useMemo, useRef, useState } from 'react';
import { mergeOpenHref } from '@/lib/openHref'; // v0.10.460
import { useNavigate } from 'react-router-dom';
import { DrawerSection } from '@/components/ui/Drawer';
import { CopilotExplain } from '@/components/CopilotExplain';
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { aiSubjectSubtitle, aiSubjectTitle, formatAiParam, type AISubject } from '@/lib/aiSubject';
import { emitAiEvidence, emitAiFocus, scrollToAttr } from './aiEvents';
import { ChatBubble } from './ChatBubble';
import { ServiceChartsExplainBody } from './ServiceChartsExplainBody';
import { aiSubjectQuestion, buildExplainContext, drawerFollowups } from './drawerChat';
import { useChatThread } from './useChatThread';
import { useCopilotConfig } from './useCopilotEnabled';

// AIDrawerBody — v0.10.483 (operatör, üçüncü kez: "Explain trace ile CoSRE
// iki ayrı drawer olarak çalışıyor. Hepsi Explain trace gibi olsun"): ✨
// Explain GÖVDESİ artık kendi çekmecesini açmıyor — CoSRE çekmecesi
// (CopilotChat) `?ai=<kind>:<id>` gelince AYNI kabuğun içinde bu gövdeyi
// çizer (açıklama + kanıt + çekmece sohbeti). Tek kabuk, tek başlık, tek
// ✕. Eski AIDrawer.tsx yalnız CopilotChat'e delege eden ince bir sarmalayıcı.
// Gövde/sohbet mantığı v0.9.477-v0.10.460 ile bayt-bayt aynı; yalnız
// dosya değişti.

export function AIDrawerBody({ subject, onClose }: { subject: AISubject; onClose: () => void }) {
  const [spanIds, setSpanIds] = useState<string[]>([]);
  const [traceIds, setTraceIds] = useState<string[]>([]);
  // v0.9.479 — açıklamanın metni: çekmece-içi sohbetin BAĞLAMI.
  const [explainText, setExplainText] = useState('');

  // v0.9.1033 — `charts` öznesinin gövdesi AYRI: bu yüzey düz metin
  // değil, anlatım + YAPISAL sinyal tablosu + pivot linkleri döndürüyor
  // (onaylı ServiceCharts AI mockup'ı). CopilotExplain yalnız
  // `{explanation}` çizdiği için ona bir dal EKLENMEDİ — kanıt/anlatım
  // ayrımı bu bileşenin sözleşmesi. Sohbet bölümü ve `key` ile state
  // sıfırlama aynen paylaşılıyor: ikinci bir çekmece kabuğu YOK.
  if (subject.kind === 'charts') {
    return (
      <div style={{ paddingTop: 8 }}>
        <ServiceChartsExplainBody
          service={subject.id} fromNs={subject.fromNs} toNs={subject.toNs}
          scope={subject.scope} onAnswer={setExplainText} />
        {explainText && (
          <AIDrawerChat subject={subject} explainText={explainText}
            spanIds={[]} traceIds={[]} />
        )}
      </div>
    );
  }

  return (
    <div style={{ paddingTop: 8 }}>
      <CopilotExplain
        auto
        kind={subject.kind}
        id={subject.id}
        spanId={subject.kind === 'span' ? subject.spanId : undefined}
        fromNs={subject.kind === 'service-health' ? subject.fromNs : undefined}
        toNs={subject.kind === 'service-health' ? subject.toNs : undefined}
        // v0.9.408 / v0.9.414 kanıt sözleşmesi: çekmece kanıtı hem kendi
        // listesinde gösterir hem de sayfaya duyurur — waterfall satırları
        // ve exception örnek-trace satırları `.wf-evidence` ile kutulanmaya
        // devam eder (çekmece kapansa da kutular kalır).
        onEvidence={ids => { setSpanIds(ids); emitAiEvidence({ spanIds: ids }); }}
        onEvidenceTraces={ids => { setTraceIds(ids); emitAiEvidence({ traceIds: ids }); }}
        onAnswer={setExplainText}
      />

      {/* Kanıt span'leri yalnız trace yüzeylerinde tıklanabilir bir hedefe
          karşılık gelir (waterfall). exception yanıtı da span id taşıyabilir
          ama o sayfada gidilecek satır yok — ölü affordance koymuyoruz. */}
      {spanIds.length > 0 && (subject.kind === 'trace' || subject.kind === 'span') && (
        <div style={{ marginTop: 16 }}>
          <DrawerSection title={`Kanıt span'leri (${spanIds.length})`}>
            {spanIds.map(id => (
              <EvidenceRow key={id} id={id}
                title="Waterfall'da bu span'e git"
                onClick={() => {
                  onClose();
                  emitAiFocus({ spanId: id });
                  scrollToAttr('data-span-id', id);
                }} />
            ))}
          </DrawerSection>
        </div>
      )}

      {traceIds.length > 0 && (
        <div style={{ marginTop: 16 }}>
          <DrawerSection title={`Kanıt trace'leri (${traceIds.length})`}>
            {traceIds.map(id => (
              <EvidenceRow key={id} id={id}
                title="Örnek trace satırına git"
                onClick={() => {
                  onClose();
                  emitAiFocus({ traceId: id });
                  scrollToAttr('data-trace-id', id);
                }} />
            ))}
          </DrawerSection>
        </div>
      )}

      {/* Sohbet devamı (v0.9.479) — açıklama geldiyse. Global CoSRE
          penceresi AÇILMAZ: cevap burada, aynı çekmecede sürer. */}
      {explainText && (
        <AIDrawerChat subject={subject} explainText={explainText}
          spanIds={spanIds} traceIds={traceIds} />
      )}
    </div>
  );
}

// AIDrawerChat — çekmece içindeki sohbet bölümü (v0.9.479, operatör
// raporu: "Chat'te devam et" global pencereyi çekmecenin ÜSTÜNE açıyordu
// ve sohbet ekrandaki açıklamayı bilmiyordu).
//
// İkinci bir chat implementasyonu YOK: tur state'i + gönderme döngüsü
// useChatThread, balon çizimi ChatBubble (adım çipleri, RAG kaynakları,
// derin linkler, kopyala, 👍/👎 hepsi bedelsiz gelir). Bu bileşen yalnız
// KABUK: aç/kapa, çipler, composer.
//
// Bağlam devri iki parça (drawerChat.ts):
//   seed  → öznenin doğal sorusu, konuşmanın ilk (çizilmeyen) turu;
//           sunucudaki takip-devralma bunu "önceki soru" sayar.
//   explain → `context.explain`; sunucu narration bloğuna katar ve
//           özneye oturmayan guided rotayı bastırır.
function AIDrawerChat({ subject, explainText, spanIds, traceIds }: {
  subject: AISubject;
  explainText: string;
  spanIds: string[];
  traceIds: string[];
}) {
  // v0.10.82 (operatör isteği: "Chat'te devam et demesine gerek yok,
  // kullanıcı isterse hemen yazabilsin"). `open` kapısı kaldırıldı —
  // salt aşamalı-gösterimdi ve bir tık vergisiydi: composer'ın mount'u
  // BEDAVA (useChatThread yalnız send'de istek atar), yani kapının
  // koruduğu hiçbir maliyet yoktu.
  const [input, setInput] = useState('');
  const endRef = useRef<HTMLDivElement>(null);

  const explain = useMemo(
    () => buildExplainContext({ subject, text: explainText, spanIds, traceIds }),
    [subject, explainText, spanIds, traceIds],
  );
  const seed = useMemo(
    () => [{ role: 'user' as const, text: aiSubjectQuestion(subject.kind, subject.id) }],
    [subject],
  );
  // v0.9.482 — öznenin KENDİSİ de tele gider: sunucu bundan ilgili
  // explain'in HAM KANITINI (trace span'leri + ilişkili loglar, exception
  // paketi) yeniden kurup anlatıma katar. Operatör raporu: "logda ne
  // yazıyor" gibi takipler açıklamanın metninde geçmediği için kör
  // cevaplanıyordu. `?ai=` kodeğinin AYNI biçimi — ikinci bir sözleşme yok.
  const subjectParam = useMemo(() => formatAiParam(subject), [subject]);
  // title (v0.10.55) — ÖZNEDEN türer, takip sorusunun lafından değil:
  // "Geçmiş" listesinde "Explain trace · a1b2c3d4…" gibi tanınabilir
  // dursun (gerekçe useChatThread.ts dosya başında).
  const persistTitle = useMemo(
    () => `${aiSubjectTitle(subject)} · ${aiSubjectSubtitle(subject)}`,
    [subject],
  );
  // service-health öznesinde sayfa bağlamı da geçer: guided router
  // servisi mesajda bulamazsa bunu varsayılan alır (v0.9.164 sözleşmesi).
  // v0.10.183 — model profili seçici (çoklu model dilim C): >1 profil varsa
  // görünür; boş = sunucu varsayılanı / yüzey eşlemesi. Çekmece ömrü kadar
  // yaşar (URL/kalıcılık yok — operatör: "kalıcı olmasın" sınıfı).
  const cfgP = useCopilotConfig(true);
  const [profile, setProfile] = useState('');
  const navigate = useNavigate(); // v0.10.445 — "sayfasını aç" çekmece sohbetinde de gezer
  const { turns, busy, send, last, showFollowups } = useChatThread({
    explain, seed, subject: subjectParam,
    onOpen: href => { const to = mergeOpenHref(href, window.location.pathname, window.location.search); if (to) navigate(to, { replace: true }); }, // v0.10.460
    service: subject.kind === 'service-health' ? subject.id : undefined,
    profile: profile || undefined,
    // persist (v0.10.55, operatör ürün kararı) — çekmece sohbeti artık
    // global CoSRE penceresiyle AYNI arşive yazılıyor; kapatılan çekmece
    // "🕘 Geçmiş"ten yeniden açılabilir (gerekçe useChatThread.ts).
    persist: true, title: persistTitle,
  });

  useEffect(() => {
    if (turns.length) endRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }, [turns]);

  const submit = (text: string) => { setInput(''); void send(text); };

  // Bağlam kurulamadıysa (yalnız-boşluk cevap) sohbeti hiç açma —
  // bağlamsız sohbet operatör raporundaki hatanın ta kendisiydi.
  if (!explain) return null;

  // Çipler: sunucu rotadan öneri gönderdiyse onlar (v0.9.411), yoksa
  // özneye göre üretilen liste — global chat'in filo çipleri burada
  // konu dışı kalırdı.
  const chips = last?.suggestions?.length ? last.suggestions : drawerFollowups(subject);
  const showChips = turns.length === 0 || showFollowups;

  return (
    <div style={{ marginTop: 16 }}>
      <DrawerSection title="Sohbet">
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span>Bu sohbet yukarıdaki açıklamayı bilir — takip sorusu sorabilirsin.</span>
          {cfgP?.profiles && cfgP.profiles.length > 1 && (
            <label style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              model
              <select value={profile} onChange={e => setProfile(e.target.value)} title="Bu sohbet için model profili (boş = sunucu varsayılanı)">
                <option value="">varsayılan{cfgP.defaultProfile ? ` · ${cfgP.profiles.find(p => p.id === cfgP.defaultProfile)?.label || cfgP.defaultProfile}` : ''}</option>
                {cfgP.profiles.filter(p => p.id !== cfgP.defaultProfile).map(p => <option key={p.id} value={p.id}>{p.label || p.id}{p.model ? ` · ${p.model}` : ''}</option>)}
              </select>
            </label>
          )}
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {turns.map((t, i) => <ChatBubble key={i} turn={t} />)}
          <div ref={endRef} />
        </div>

        {showChips && (
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginTop: turns.length ? 8 : 0 }}>
            {chips.map(q => (
              <Chip key={q} pill onClick={() => submit(q)} disabled={busy}>↳ {q}</Chip>
            ))}
          </div>
        )}

        <form
          onSubmit={e => { e.preventDefault(); submit(input); }}
          style={{ display: 'flex', gap: 8, marginTop: 10 }}>
          <input
            value={input}
            onChange={e => setInput(e.target.value)}
            placeholder="Bu konuda sor…"
            disabled={busy}
            autoFocus
            style={{
              flex: 1, minWidth: 0, padding: '7px 10px', fontSize: 13,
              background: 'var(--bg)', color: 'var(--text)',
              border: '1px solid var(--border)', borderRadius: 6,
            }} />
          <Button variant="primary" type="submit" disabled={!input.trim()} loading={busy}>
            Gönder
          </Button>
        </form>
      </DrawerSection>
    </div>
  );
}

// Kanıt satırı — sayfadaki kutulanmış satırla AYNI görsel dil (.wf-evidence),
// böylece çekmecedeki liste ile waterfall'daki kutu aynı şeyi anlatır.
function EvidenceRow({ id, title, onClick }: { id: string; title: string; onClick: () => void }) {
  return (
    <div className="wf-evidence mono" role="button" tabIndex={0}
      title={title}
      onClick={onClick}
      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onClick(); } }}
      style={{
        cursor: 'pointer', fontSize: 11, padding: '5px 8px', marginBottom: 4,
        borderRadius: 'var(--radius-sm)', color: 'var(--text)',
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }}>
      {id} <span style={{ color: 'var(--text3)' }}>→</span>
    </div>
  );
}
