import { useState } from 'react';
import { api } from '@/lib/api';
import { IconButton } from '@/components/ui/IconButton';

// AIFeedbackButtons — tek-atış ✨ Explain cevaplarının 👍/👎 rayı
// (v0.9.1119 backend'i, Faz 0.3b).
//
// NEDEN PAYLAŞILAN BİLEŞEN: aynı üç satırlık iyimser-güncelleme +
// geri-alma mantığı depoda ZATEN üç kez yazılmıştı (ChatBubble.rateTurn,
// RCAVerdictPanel.rateVerdict, AIAnalysisPanel.rateAnalysis). Dördüncü
// ile sekizinci kopyayı yazmak yerine tek atom: sekiz yüzey aynı
// davranışı paylaşır, sözleşme tek yerde ayrışmadan durur.
//
// SÖZLEŞME (üçü de v0.9.592'nin dersi):
//  1. Kimlik yoksa AFFORDANCE HİÇ ÇİZİLMEZ. Cevabını kaydedemeyeceğimiz
//     bir soruyu sormak — "yararlı mıydı?" deyip hiçbir yere yazmamak —
//     bu rayın var olma sebebi olan ÖLÜ affordance hatasının ta kendisi.
//     `exchangeId` omitempty: sunucu-cache'li (explain-charts) ya da eski
//     sürümden gelen bir gövdede alan HİÇ gelmez.
//  2. İyimser güncelleme + hata hâlinde GERİ ALMA. Başarısız bir POST'tan
//     sonra seçili kalmak, "kaydedildi" yalanının daha sessiz biçimi.
//  3. Aynı oya ikinci tık NO-OP; ters oya tık YENİDEN POST eder (sunucu
//     exchangeId'ye göre değiştirir, çift satır yazmaz).
//
// Seçili durum `IconButton active` ile: `aria-pressed` + `.active` tint.
// v0.9.891'de opacity+borderColor ile ELLE çizilen seçili durum tam da
// bu yüzden emekli edilmişti — durum yalnız GÖRÜLMEMELİ, DUYURULMALI da.
//
// Oy, exchangeId'ye BAĞLI tutuluyor: "Yeniden sor" taze bir kimlik
// üretir ve eski cevabın oyu yeni cevabın üstünde kalamaz.
export function AIFeedbackButtons({ exchangeId }: { exchangeId?: string }) {
  const [rated, setRated] = useState<{ xid: string; verdict: 1 | -1 } | null>(null);
  // Türetilmiş: kimlik değiştiğinde sıfırlama efekti GEREKMEZ (ve
  // efektle sıfırlamak bir render geciktirir — eski oy bir kare boyunca
  // yeni cevabın altında görünürdü).
  const verdict = rated && rated.xid === exchangeId ? rated.verdict : null;

  if (!exchangeId) return null;

  const rate = (v: 1 | -1) => {
    if (verdict === v) return; // aynı oya ikinci tık: no-op
    const prior = verdict;
    setRated({ xid: exchangeId, verdict: v });
    api.postAIFeedback({ exchangeId, verdict: v }).catch(() => {
      setRated(prior ? { xid: exchangeId, verdict: prior } : null);
    });
  };

  return (
    <div style={{ display: 'inline-flex', gap: 2, alignItems: 'center', marginTop: 8 }}>
      <IconButton variant="ghost" size="sm" active={verdict === 1}
        onClick={() => rate(1)} title="Faydalı"
        aria-label="Cevabı faydalı işaretle" icon="👍" />
      <IconButton variant="ghost" size="sm" active={verdict === -1}
        onClick={() => rate(-1)} title="Faydasız"
        aria-label="Cevabı faydasız işaretle" icon="👎" />
    </div>
  );
}
