// v0.9.560 — RCA verdict paneli.
//
// Tasarım: docs/cosre-verdict-design.md
//
// Backend v0.9.559'da kalkanlı, yapılandırılmış bir karar döndürmeye
// başladı ama ekran hâlâ yalnız düzyazıyı çiziyordu. Bu bileşen kararı
// görünür yapıyor — ve tasarımın DÖRT dürüstlük kuralını uyguluyor:
//
//  1. shields.parsed=false MUTLAKA gösterilir. Yoksa sahte bir "kanıt
//     yetersiz" (model yanıt veremedi) gerçek kanıt yokluğundan ayırt
//     edilemez ve operatör "AI baktı, bir şey bulamadı" sanır.
//
//  2. Kanıt, iddianın YANINDA değil ALTINDA ve "model bunu gösterdi"
//     ayrımıyla çizilir. Sunucu, modelin iddiasını kendi sesiyle
//     onaylamaz — çünkü gerçek bir kanıt kimliğinin ALAKASIZ bir
//     iddiaya iliştirilmesini hiçbir mekanik kalkan yakalayamaz.
//
//  3. Ölçülemeyen etki SIFIR olarak çizilmez. "0 hata" ile "ölçemedik"
//     ekranda aynı görünürse, tam patlama anında "etkilenen istek yok"
//     okunur.
//
//  4. Üç güven AYRI etiketlerle gösterilir. Aynı ekranda üç farklı
//     "confidence" var ve hepsini "güven" diye çizmek, hangisinin neyi
//     ölçtüğünü kaybettirirdi.
import { useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { api } from '@/lib/api';
import { verdictTone, verdictIsDegraded, verdictHasShieldWarning, measuredText, canRateVerdict } from './rcaVerdictView';
import type { RCAVerdict } from '@/lib/types';

const VERDICT_LABEL: Record<RCAVerdict['verdict'], string> = {
  root_cause_identified: 'Kök neden belirlendi',
  probable_cause: 'Olası neden',
  insufficient_evidence: 'Kanıt yetersiz',
};

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 8, marginTop: 6, fontSize: 12.5, lineHeight: 1.5 }}>
      <span style={{ color: 'var(--text3)', minWidth: 92, flexShrink: 0 }}>{label}</span>
      <span style={{ color: 'var(--text2)' }}>{children}</span>
    </div>
  );
}

function SubHead({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 10, fontWeight: 700, letterSpacing: '.06em', textTransform: 'uppercase',
      color: 'var(--text3)', marginTop: 14, marginBottom: 5,
    }}>
      {children}
    </div>
  );
}

/** Ölçülmemiş sayıyı SIFIR gibi göstermemek için tek yer.
 *  Karar measuredText'te (saf, testli); burası yalnız çizim. */
function measured(n: number | null | undefined, fmt: (v: number) => string): React.ReactNode {
  const t = measuredText(n, fmt);
  if (t === null) {
    return <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}>ölçülemedi</span>;
  }
  return t;
}

export function RCAVerdictPanel({ v, exchangeId }: { v: RCAVerdict; exchangeId?: string }) {
  const sh = v.shields;
  const degraded = verdictIsDegraded(v);
  // v0.9.592 — operatör oyu. undefined = henüz oy yok.
  const [rated, setRated] = useState<1 | -1 | undefined>(undefined);

  return (
    <div style={{
      marginTop: 10, padding: '12px 14px', borderRadius: 6,
      background: 'var(--bg2)', border: '1px solid var(--border)',
    }}>
      {/* ── 1. Dürüst degrade şeridi — HER ŞEYDEN ÖNCE ────────────────
          Model yanıt üretemediyse operatör bunu ilk okumalı. Aşağıdaki
          "kanıt yetersiz" kararı o zaman bir BULGU değil, bir
          ULAŞILAMAMA'dır ve ikisi çok farklı şeyler. */}
      {degraded && (
        <div style={{
          display: 'flex', gap: 8, alignItems: 'flex-start',
          fontSize: 12, lineHeight: 1.5, marginBottom: 10,
          padding: '8px 10px', borderRadius: 5,
          background: 'color-mix(in srgb, var(--warn) 12%, transparent)',
          border: '1px solid color-mix(in srgb, var(--warn) 35%, transparent)',
          color: 'var(--text2)',
        }}>
          <span aria-hidden="true">⚠</span>
          <span>
            <b>Yapay zekâ hakemi yanıt üretemedi.</b> Aşağıdaki karar,
            deterministik korelasyon motorundan türetildi — modelin
            "kanıt bulamadım" demesi DEĞİL.
          </span>
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        {/* v0.9.577 — bilinmeyen verdict BOŞ rozet çizmesin. Backend
            artık geçersiz enum'u düşüş yoluna gönderiyor, ama ileride
            yeni bir değer eklenirse panel sessizce boş bir rozetle
            değil, gelen değerin kendisiyle çizsin. */}
        <Badge tone={verdictTone(v.verdict)}>
          {VERDICT_LABEL[v.verdict] ?? (v.verdict || 'bilinmeyen')}
        </Badge>
        {v.title && (
          <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--text)' }}>{v.title}</span>
        )}
      </div>

      {v.summary && (
        <div style={{ marginTop: 8, fontSize: 12.5, lineHeight: 1.55, color: 'var(--text2)' }}>
          {v.summary}
        </div>
      )}

      {/* ── Kök neden ─────────────────────────────────────────────── */}
      {v.rootCause?.entity && (
        <>
          <SubHead>Kök neden</SubHead>
          <Row label="Varlık"><b>{v.rootCause.entity}</b></Row>
          {v.rootCause.failure_mode && <Row label="Arıza biçimi">{v.rootCause.failure_mode}</Row>}
          {v.rootCause.trigger && <Row label="Tetikleyici">{v.rootCause.trigger}</Row>}
          {v.rootCause.latent_weakness && (
            <Row label="Yapısal zayıflık">{v.rootCause.latent_weakness}</Row>
          )}
        </>
      )}

      {/* ── 2. Kanıt: iddianın ALTINDA, ayrı sesle ─────────────────────
          Başlık bilerek "Model şu kanıtları gösterdi" — "Kanıt" değil.
          Sunucu bu metinleri kendi ürettiği için doğrudur, ama İDDİAYLA
          İLİŞKİSİ modelin beyanıdır ve onu doğrulayamayız. Fark bir
          kelimede duruyor ve önemli. */}
      {v.evidence && v.evidence.length > 0 && (
        <>
          <SubHead>Model şu kanıtları gösterdi</SubHead>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, lineHeight: 1.6, color: 'var(--text3)' }}>
            {v.evidence.map(e => (
              <li key={e.id}>
                <code style={{ fontSize: 11, color: 'var(--text3)' }}>{e.id}</code>{' '}
                {e.text}
              </li>
            ))}
          </ul>
        </>
      )}

      {/* ── Nedensellik zinciri ───────────────────────────────────── */}
      {v.causalChain && v.causalChain.length > 0 && (
        <>
          <SubHead>Nedensellik zinciri</SubHead>
          <ol style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, lineHeight: 1.6, color: 'var(--text2)' }}>
            {v.causalChain.map((c, i) => (
              <li key={i}>
                {c.entity && <b>{c.entity}</b>}{c.entity && c.effect ? ' — ' : ''}{c.effect}
              </li>
            ))}
          </ol>
        </>
      )}

      {/* ── Elenen hipotezler — elemenin GERÇEKTEN yapıldığının kanıtı ── */}
      {v.rejectedHypotheses && v.rejectedHypotheses.length > 0 && (
        <>
          <SubHead>Elenen hipotezler</SubHead>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, lineHeight: 1.6, color: 'var(--text3)' }}>
            {v.rejectedHypotheses.map((r, i) => (
              <li key={i}>
                <span style={{ textDecoration: 'line-through' }}>{r.hypothesis}</span>
                {r.reason && <> — {r.reason}</>}
              </li>
            ))}
          </ul>
        </>
      )}

      {/* ── 3. Etki: ölçülemeyen SIFIR değil ──────────────────────── */}
      {v.impact && (
        <>
          <SubHead>Etki</SubHead>
          <Row label="Ölçülen">{v.impact.entity}</Row>
          <Row label="Hata">
            {measured(v.impact.errorCount, n => n.toLocaleString('tr-TR'))}
          </Row>
          <Row label="İstek">
            {measured(v.impact.requestCount, n => n.toLocaleString('tr-TR'))}
          </Row>
          <Row label="Hata oranı">
            {measured(v.impact.errorShare, n => `%${(n * 100).toFixed(2)}`)}
          </Row>
          {v.impact.note && (
            <div style={{ marginTop: 4, fontSize: 11.5, color: 'var(--text3)', fontStyle: 'italic' }}>
              {v.impact.note}
            </div>
          )}
        </>
      )}

      {/* ── Öneriler ──────────────────────────────────────────────── */}
      {v.remediation && v.remediation.length > 0 && (
        <>
          <SubHead>Öneriler</SubHead>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12.5, lineHeight: 1.6, color: 'var(--text2)' }}>
            {v.remediation.map((r, i) => (
              <li key={i}>
                <Badge tone={r.kind === 'fix' ? 'success' : 'info'}>
                  {r.kind === 'fix' ? 'kalıcı' : 'geçici'}
                </Badge>{' '}
                {r.action}
                {r.target && <span style={{ color: 'var(--text3)' }}> · {r.target}</span>}
              </li>
            ))}
          </ul>
        </>
      )}

      {v.missingEvidence && v.missingEvidence.length > 0 && (
        <>
          <SubHead>Eksik kanıt</SubHead>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 12, lineHeight: 1.6, color: 'var(--text3)' }}>
            {v.missingEvidence.map((m, i) => <li key={i}>{m}</li>)}
          </ul>
        </>
      )}

      {/* ── 4. Üç güven, üç ayrı etiket ───────────────────────────── */}
      <div style={{
        display: 'flex', gap: 16, flexWrap: 'wrap', marginTop: 14, paddingTop: 10,
        borderTop: '1px solid var(--border)', fontSize: 11.5, color: 'var(--text3)',
      }}>
        <span title="Kalkanlardan geçmiş nihai güven — ekranda esas alınacak sayı budur">
          Güven <b style={{ color: 'var(--text2)' }}>{(v.confidence * 100).toFixed(0)}%</b>
        </span>
        <span title="Modelin kendi beyanı; kalkanlar bunu düşürmüş olabilir">
          Model beyanı {(v.modelConfidence * 100).toFixed(0)}%
        </span>
        <span title="Deterministik korelasyon motorunun güveni — LLM'den bağımsız">
          Korelasyon motoru {(v.hypothesisConfidence * 100).toFixed(0)}%
        </span>
      </div>

      {/* ── Kalkan raporu — "bu karar neye RAĞMEN üretildi" ────────── */}
      {verdictHasShieldWarning(v) && (
        <div style={{
          marginTop: 10, padding: '8px 10px', borderRadius: 5, fontSize: 11.5, lineHeight: 1.5,
          background: 'color-mix(in srgb, var(--err) 10%, transparent)',
          border: '1px solid color-mix(in srgb, var(--err) 30%, transparent)',
          color: 'var(--text2)',
        }}>
          <b>Doğrulama uyarısı.</b>{' '}
          {sh.unknownEntities && sh.unknownEntities.length > 0 && (
            <>Metinde veride olmayan ad(lar): <code>{sh.unknownEntities.join(', ')}</code>.{' '}</>
          )}
          {sh.rejectedEvidence && sh.rejectedEvidence.length > 0 && (
            <>Geçersiz kanıt atfı: <code>{sh.rejectedEvidence.join(', ')}</code>.{' '}</>
          )}
          {sh.refutationInvalid && <>En az bir eleme geçersiz sayıldı.</>}
        </div>
      )}

      {/* ── 5. Operatör oyu (v0.9.592) ────────────────────────────────
          Kimlik yoksa HİÇ ÇİZİLMEZ. Bu dilim, depoda tıklanınca
          "Teşekkürler." yazıp hiçbir yere yazmayan ÖLÜ bir
          derecelendirme affordance'ı bulunduğu için var; aynı hatayı
          burada tekrarlamak tasarımın kendisini çürütürdü.

          İyimser güncelleme + hata hâlinde geri alma — sohbetteki
          rateTurn (ChatBubble.tsx) ile aynı davranış. Ray da aynı:
          POST /api/ai/feedback, surface SUNUCUDA çözülür. */}
      {canRateVerdict(exchangeId) && (
        <div style={{
          marginTop: 12, paddingTop: 10, borderTop: '1px solid var(--border)',
          display: 'flex', alignItems: 'center', gap: 8, fontSize: 11.5,
          color: 'var(--text3)',
        }}>
          <span>Bu değerlendirme doğru mu?</span>
          <Button
            variant="ghost" size="sm" aria-label="doğru"
            aria-pressed={rated === 1}
            style={rated === 1 ? { color: 'var(--ok)' } : undefined}
            onClick={() => rateVerdict(exchangeId!, 1, rated, setRated)}>👍</Button>
          <Button
            variant="ghost" size="sm" aria-label="yanlış"
            aria-pressed={rated === -1}
            style={rated === -1 ? { color: 'var(--err)' } : undefined}
            onClick={() => rateVerdict(exchangeId!, -1, rated, setRated)}>👎</Button>
          {rated !== undefined && <span>Kaydedildi.</span>}
        </div>
      )}
    </div>
  );
}

// rateVerdict — oyu gönderir. İyimser güncelleme, hata hâlinde GERİ
// ALMA: başarısız bir POST'tan sonra "Kaydedildi." yazmak, düzeltmeye
// çalıştığımız ölü affordance'ın daha sinsi bir biçimi olurdu.
function rateVerdict(
  exchangeId: string, verdict: 1 | -1,
  prior: 1 | -1 | undefined,
  setRated: (v: 1 | -1 | undefined) => void,
) {
  if (prior === verdict) return;
  setRated(verdict);
  api.postAIFeedback({ exchangeId, verdict }).catch(() => setRated(prior));
}
