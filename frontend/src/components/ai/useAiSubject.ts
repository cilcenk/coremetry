import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { AI_CODE_PARAM, AI_PARAM, AI_SRC_PARAM, formatAiParam, parseAiParam, type AISrc, type AISubject } from '@/lib/aiSubject';

// useAiSubject — AI çekmecesinin açık öznesi, ADRESTEN okunur/yazılır
// (v0.9.477). Ev kuralı: her operatör seçimi `setSearchParams(prev => …,
// { replace: true })` ile yazılır, yabancı parametreler KORUNUR ve seçim
// history'ye durak eklemez.
export function useAiSubject(): [AISubject | null, (s: AISubject | null, src?: AISrc) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get(AI_PARAM);
  const subject = useMemo(() => parseAiParam(raw), [raw]);

  const setSubject = useCallback((s: AISubject | null, src?: AISrc) => {
    setSearchParams(prev => {
      // `prev` router'ın konumundan gelir; Trace sayfası ?span= / ?tab='ı
      // ham history.replaceState ile yazdığı için router BAYAT kalabilir —
      // o hâlde prev'i kopyalamak seçili span'i adresten SİLERDİ (ev
      // kuralının yasakladığı "yabancı parametre kaybı"). Canlı adres
      // çubuğu her zaman üst küme olduğundan onu taban alıp prev'de olup
      // canlıda olmayan anahtarları üstüne ekliyoruz.
      const live = typeof window !== 'undefined' ? window.location.search : '';
      const next = new URLSearchParams(live || prev.toString());
      prev.forEach((v, k) => { if (!next.has(k)) next.append(k, v); });
      if (s) next.set(AI_PARAM, formatAiParam(s));
      else next.delete(AI_PARAM);
      // v0.10.81 — aicode YALNIZ paylaşılan linkte yaşar: uygulama içi
      // her açılış/kapanış/özne değişimi onu siler. Silmeseydik bir kez
      // işaretlenen kutu SONRAKİ öznelere de sızardı ve v0.10.60'ın
      // "her açılışta kapalı" kararı arka kapıdan geri dönerdi.
      next.delete(AI_CODE_PARAM);
      // v0.10.432 (D8) — açılış kaynağı da yalnız o açılışta yaşar.
      if (s && src) next.set(AI_SRC_PARAM, src);
      else next.delete(AI_SRC_PARAM);
      return next;
    }, { replace: true });
  }, [setSearchParams]);

  return [subject, setSubject];
}
