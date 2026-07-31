import { useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import { Spinner } from '@/components/Spinner';
import { useCopilotEnabled } from '@/components/ai/useCopilotEnabled';
import { aiSubjectQuestion } from '@/components/ai/drawerChat';
import type { AIKind } from '@/lib/aiSubject';
import { IconSparkles } from './icons';

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
  const [meta, setMeta] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // v0.9.409 (operatör isteği): buton ilk kullanıma kadar nabız atar —
  // gözden kaçıyordu. İlk tıklama kalıcı susturur (bu mount için).
  const [used, setUsed] = useState(false);

  // applyText — cevabı hem panele yaz hem üst bileşene duyur (v0.9.479).
  // BOŞ model cevabı bağlam olamaz: onAnswer'a boş string gider, çekmece
  // sohbeti açılmaz (bağlamsız sohbet operatör raporundaki hatanın ta
  // kendisiydi).
  const applyText = (raw: string) => {
    setText(raw || '⚠ Model boş yanıt döndürdü (reasoning modu + düşük max_tokens olabilir).');
    onAnswer?.(raw);
  };

  const run = async () => {
    setUsed(true);
    setBusy(true); setError(null); setText(null); setMeta(null);
    onAnswer?.(''); // "Yeniden sor" bayat bağlamı taşımasın
    try {
      if (kind === 'runbook') {
        const r = await api.copilotRunbook(id);
        applyText(r.explanation);
        // Surface the "based on N past resolutions" hint so the
        // operator knows whether the steps are grounded in
        // real history or first-principles.
        setMeta(r.similarCount > 0
          ? `Based on ${r.similarCount} past resolved instance${r.similarCount === 1 ? '' : 's'} of this rule on this service.`
          : `No past resolutions found — first-principles only.`);
      } else {
        const r = kind === 'trace'          ? await api.copilotExplainTrace(id).then(rr => {
                                                  if (rr.evidenceSpanIds?.length) onEvidence?.(rr.evidenceSpanIds);
                                                  return rr;
                                                })
                : kind === 'exception'      ? await api.copilotExplainException(id).then(rr => {
                                                  if (rr.evidenceSpanIds?.length) onEvidence?.(rr.evidenceSpanIds);
                                                  if (rr.evidenceTraceIds?.length) onEvidenceTraces?.(rr.evidenceTraceIds);
                                                  return rr;
                                                })
                : kind === 'span'           ? await api.copilotExplainSpan(id, spanId ?? '')
                : kind === 'problem'        ? await api.copilotExplainProblem(id)
                : kind === 'incident'       ? await api.copilotExplainIncident(id)
                : kind === 'anomaly'        ? await api.copilotExplainAnomaly(id)
                :                             await api.copilotExplainServiceHealth(id, fromNs ?? 0, toNs ?? 0);
        applyText(r.explanation);
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Explain failed');
    } finally {
      setBusy(false);
    }
  };

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
    <div style={{
      display: 'inline-flex', flexDirection: 'column', gap: 8,
      alignItems: 'flex-start', maxWidth: '100%',
    }}>
      {auto && busy && <Spinner label="CoSRE düşünüyor…" />}
      {showButton && (
        <Button variant="accent" size="sm" onClick={run} disabled={busy}
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
          color: 'var(--text)', whiteSpace: 'pre-wrap', maxWidth: 'min(720px, 100%)',
        }}>
          <div style={{ fontSize: 10, color: 'var(--accent2)', marginBottom: 6, fontWeight: 700, letterSpacing: '.5px',
                        display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <IconSparkles size={11} /> {kind === 'runbook' ? 'RUNBOOK' : 'CoSRE'}
          </div>
          {meta && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8, fontStyle: 'italic' }}>
              {meta}
            </div>
          )}
          {text}
          {/* v0.9.479 — çekmecede (auto) bu link ÇİZİLMEZ: sohbet aynı
              çekmecenin içinde, ekrandaki açıklamayı bağlam alarak açılır
              (AIDrawer). Satır-içi yüzeylerde köprü aynen duruyor. */}
          {!auto && (
            <div style={{ marginTop: 8 }}>
              <button type="button" onClick={askInChat}
                title="CoSRE chat'te bu konuda devam et"
                style={{
                  all: 'unset', cursor: 'pointer', fontSize: 11, color: 'var(--accent2)',
                  display: 'inline-flex', alignItems: 'center', gap: 4,
                }}>
                💬 Chat'te devam et →
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
