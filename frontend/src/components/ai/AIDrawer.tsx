import { useEffect, useMemo, useRef, useState } from 'react';
import { Drawer, DrawerSection } from '@/components/ui/Drawer';
import { CopilotExplain } from '@/components/CopilotExplain';
import { Button } from '@/components/ui/Button';
import { Chip } from '@/components/ui/Chip';
import { IconSparkles } from '@/components/icons';
import { aiSubjectSubtitle, aiSubjectTitle, formatAiParam, type AISubject } from '@/lib/aiSubject';
import { emitAiEvidence, emitAiFocus, scrollToAttr } from './aiEvents';
import { ChatBubble } from './ChatBubble';
import { ServiceChartsExplainBody } from './ServiceChartsExplainBody';
import { aiSubjectQuestion, buildExplainContext, drawerFollowups } from './drawerChat';
import { useAiSubject } from './useAiSubject';
import { useChatThread } from './useChatThread';
import { useCopilotConfig } from './useCopilotEnabled';

// AIDrawer — uygulamadaki TEK AI açıklama yüzeyi (v0.9.477, onaylı
// mockup). AppShell'de bir kez mount edilir (CopilotChat / CommandPalette
// emsali), `?ai=<kind>:<id>` varsa açılır. Kabuk paylaşılan ui/Drawer
// primitifidir (overlay + slide-in + Esc/✕/overlay ile kapanma, v0.8.465);
// içerik AYNEN mevcut CopilotExplain'dir — fetch/render mantığı kopyalanmadı.
//
// Neden kabuk düzeyinde: sayfa içi sekme değişimi (Trace'in Trace/Logs
// şeridi, ProblemDetail'in kartları) çekmeceyi söküp cevabı çöpe atmasın;
// ayrıca `?ai=` her yüzeyde tek kodla çalışsın.
export function AIDrawer() {
  const [subject, setSubject] = useAiSubject();
  // Kapalıyken /api/copilot/config'e DOKUNMAZ (anonim /public/* sayfaları
  // dahil hiçbir sayfa boşuna istek atmaz).
  //
  // v0.9.1037 — aynı cevaptan model adı da okunuyor (çip için); İKİNCİ
  // bir istek yok, cache modül düzeyinde ve zaten tüm cevabı tutuyor.
  const cfg = useCopilotConfig(subject !== null);
  if (!subject) return null;
  // Copilot kapalıyken (veya cevap gelmeden) boş bir kabuk açmak yerine
  // hiç açma — butonlar da zaten görünmüyor, elle yazılmış bir ?ai= linki
  // sessizce yok sayılır.
  if (cfg?.enabled !== true) return null;

  const key = formatAiParam(subject);
  return (
    <Drawer onClose={() => setSubject(null)} width={620}
      header={
        <div style={{ minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 700 }}>
            <IconSparkles size={14} /> {aiSubjectTitle(subject)}
          </div>
          {/* Meta şeridi: özne + (v0.9.1037) cevabı üreten MODEL.
              Operatörün ilk sorusu "bunu hangi model yazdı" — cevap
              Settings'te gömülüydü, açıklamanın yanında değil.
              YALNIZ model adı yazılır: "yerel" gibi bir konum iddiası
              YANLIŞ olurdu (prod'da uzak bir uç). Model boşsa (Copilot
              kapalı / ad yapılandırılmamış) çip HİÇ çizilmez — burada
              boş bir rozet "bilmiyorum" demenin gürültülü hâli olurdu. */}
          <div style={{
            display: 'flex', alignItems: 'center', gap: 6,
            marginTop: 2, minWidth: 0,
          }}>
            <span className="mono" style={{
              fontSize: 11, color: 'var(--text3)',
              overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
            }} title={aiSubjectSubtitle(subject)}>
              {aiSubjectSubtitle(subject)}
            </span>
            {/* `.chip` (statik key/value meta rozeti) — ui/Chip DEĞİL:
                o atom `onRemove`suz hâlde her zaman bir <button> basar ve
                tıklanamayan bir rozeti buton yapmak sahte affordance
                olurdu (Chip.tsx'in kendi kuralı). ProblemDetail/Incident
                meta satırlarıyla aynı rozet. */}
            {cfg.model && (
              <span className="chip" style={{ flexShrink: 0, fontSize: 10.5 }}
                title="Bu açıklamayı üreten model">
                <span className="k">model</span>
                <b className="mono">{cfg.model}</b>
              </span>
            )}
          </div>
        </div>
      }>
      {/* key: özne değişince tüm alt-state (metin, kanıt) sıfırlanır ve
          otomatik çalıştırma yeni özne için bir kez daha tetiklenir. */}
      <AIDrawerBody key={key} subject={subject} onClose={() => setSubject(null)} />
    </Drawer>
  );
}

function AIDrawerBody({ subject, onClose }: { subject: AISubject; onClose: () => void }) {
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
  const { turns, busy, send, last, showFollowups } = useChatThread({
    explain, seed, subject: subjectParam,
    service: subject.kind === 'service-health' ? subject.id : undefined,
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
        <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
          Bu sohbet yukarıdaki açıklamayı bilir — takip sorusu sorabilirsin.
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
