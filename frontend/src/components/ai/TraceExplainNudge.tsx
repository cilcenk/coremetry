import { useEffect, useState } from 'react';
import { useLocation, useSearchParams } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { IconSparkles } from '@/components/icons';
import { STORAGE_KEYS, getRaw, getSessionRaw, setRaw, setSessionRaw } from '@/lib/storage';
import { useAiSubject } from './useAiSubject';
import { shouldNudgeExplain } from './traceNudge';

// TraceExplainNudge — v0.10.432 (CoSRE router boşlukları D8): trace ilk
// açılışta FAB'ın üstünde çıkan küçük baloncuk. CopilotChat'in FAB bloğunda
// yaşar (sohbet açıkken çizilmez, copilot kapalıyken de — CopilotChat'in
// kendi kapıları), yani /public/trace'te hiç yok. Karar traceNudge.ts'te.
// "Evet" → AI çekmecesi bu trace için, src=nudge ile (sunucu yüzeyi
// explain-trace:nudge); "Sağol, gerek yok" → kalıcı ret. Baloncuk gösterildiği
// an sekme oturumuna "soruldu" yazılır: cevapsız bırakılırsa da bir daha
// dürtmez. Sayfa-düzeyi yapışkan şerit DEĞİL: FAB'a bağlı, 320px'lik callout.
export function TraceExplainNudge() {
  const { pathname } = useLocation();
  const [searchParams] = useSearchParams();
  const [subject, setAi] = useAiSubject();
  const traceId = pathname === '/trace' ? (searchParams.get('id') ?? '') : '';
  const spanOpen = searchParams.has('span'); // v0.10.445 — span paneli (z-nav) baloncuğun altında kalıyordu
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const show = shouldNudgeExplain({
      pathname, traceId, aiOpen: subject !== null, spanOpen,
      declined: getRaw(STORAGE_KEYS.aiTraceNudgeDeclined) === '1',
      askedThisTab: getSessionRaw(STORAGE_KEYS.aiTraceNudgeAsked) === '1',
    });
    if (show) setSessionRaw(STORAGE_KEYS.aiTraceNudgeAsked, '1');
    setVisible(show);
  }, [pathname, traceId, subject, spanOpen]);

  if (!visible) return null;
  const accept = () => { setVisible(false); setAi({ kind: 'trace', id: traceId }, 'nudge'); };
  const decline = () => { setVisible(false); setRaw(STORAGE_KEYS.aiTraceNudgeDeclined, '1'); };
  return (
    <div className="cm-ai-nudge" role="region" aria-label="CoSRE önerisi">
      <div className="cm-ai-nudge-text">
        <IconSparkles size={14} />
        <span>Bu trace'i açıklamamı ister misin?</span>
      </div>
      <div className="cm-ai-nudge-actions">
        <Button variant="accent" size="sm" onClick={accept} title="CoSRE bu trace'i açıklasın (explain-trace)">Evet</Button>
        <Button variant="secondary" size="sm" onClick={decline} title="Bir daha sorma">Sağol, gerek yok</Button>
      </div>
    </div>
  );
}
