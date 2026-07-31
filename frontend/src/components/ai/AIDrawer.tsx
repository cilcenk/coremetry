import { useState } from 'react';
import { Drawer, DrawerSection } from '@/components/ui/Drawer';
import { CopilotExplain } from '@/components/CopilotExplain';
import { IconSparkles } from '@/components/icons';
import { aiSubjectSubtitle, aiSubjectTitle, formatAiParam, type AISubject } from '@/lib/aiSubject';
import { emitAiEvidence, emitAiFocus, scrollToAttr } from './aiEvents';
import { useAiSubject } from './useAiSubject';
import { useCopilotEnabled } from './useCopilotEnabled';

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
  const enabled = useCopilotEnabled(subject !== null);
  if (!subject) return null;
  // Copilot kapalıyken (veya cevap gelmeden) boş bir kabuk açmak yerine
  // hiç açma — butonlar da zaten görünmüyor, elle yazılmış bir ?ai= linki
  // sessizce yok sayılır.
  if (enabled !== true) return null;

  const key = formatAiParam(subject);
  return (
    <Drawer onClose={() => setSubject(null)} width={620}
      header={
        <div style={{ minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 700 }}>
            <IconSparkles size={14} /> {aiSubjectTitle(subject)}
          </div>
          <div className="mono" style={{
            fontSize: 11, color: 'var(--text3)', marginTop: 2,
            overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          }} title={aiSubjectSubtitle(subject)}>
            {aiSubjectSubtitle(subject)}
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
