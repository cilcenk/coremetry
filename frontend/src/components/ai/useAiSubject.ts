import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { AI_PARAM, formatAiParam, parseAiParam, type AISubject } from '@/lib/aiSubject';

// useAiSubject — AI çekmecesinin açık öznesi, ADRESTEN okunur/yazılır
// (v0.9.477). Ev kuralı: her operatör seçimi `setSearchParams(prev => …,
// { replace: true })` ile yazılır, yabancı parametreler KORUNUR ve seçim
// history'ye durak eklemez.
export function useAiSubject(): [AISubject | null, (s: AISubject | null) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const raw = searchParams.get(AI_PARAM);
  const subject = useMemo(() => parseAiParam(raw), [raw]);

  const setSubject = useCallback((s: AISubject | null) => {
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
      return next;
    }, { replace: true });
  }, [setSearchParams]);

  return [subject, setSubject];
}
