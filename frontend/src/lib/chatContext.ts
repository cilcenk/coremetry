// v0.9.653 — operatör: "Ekranda bir trace açıksa CoSRE 'bu trace'i
// açıklamamı ister misin' diye sorsun — chati açınca."
//
// Boş sohbetteki üç sabit çip (v0.9.652) EKRANDAN habersiz: operatör bir
// trace'e bakarken sohbeti açtığında ona takımının servislerini soruyor.
// Elindeki bağlam kayboluyor ve soruyu elle yazması gerekiyor.
//
// Bu çözümleyici sayfayı bir SORUYA çeviriyor. Saf: rota + query string
// girer, çip metni + sorulacak soru çıkar.
//
// NEDEN OTOMATİK AÇIKLAMA DEĞİL de ÇİP: sohbeti açmak bir LLM çağrısını
// tetiklememeli. Operatör sohbeti başka bir şey sormak için de açıyor
// olabilir ve her açılışta bir açıklama üretmek hem para hem gürültü.
// Çip teklif eder, tık karar verir.

import { aiSubjectQuestion } from '@/components/ai/drawerChat';

export type ChatContextStarter = { chip: string; question: string };

/**
 * contextStarter — ekrandaki özneyi bir başlangıç çipine çevirir.
 *
 * null = bağlamsal öneri yok; boş sohbet yalnız statik çipleri gösterir.
 *
 * Şimdilik YALNIZ trace: operatörün istediği bu, ve soru metni
 * (aiSubjectQuestion) orada zaten iyi tanımlı. Yeni yüzey eklemek tek
 * satır — ama eklemeden önce o özne için sorunun ANLAMLI olduğu
 * doğrulanmalı; "Bu sayfayı açıkla" gibi boş bir soru öneriden kötüdür.
 */
// v0.9.1226 — chat'in varsayılan servis kapsamı: hangi rotada hangi
// paramdan okunacağı TEK yerde. /service kendi ?name='ini kullanır;
// ?service= URL-durumu taşıyan liste sayfaları da (Traces/Endpoints/
// Logs/Inbox/Deploys/Metrics/Explore/Clusters/Profiling) bağlamı
// devretsin — bugüne dek chat bu rotalarda kör açılıyordu (denetim
// bulgusu: sunucu tarafı ctxService + kapsam bandı zaten hazırdı,
// değer hiç gelmiyordu). SAF — vitest'i chatContext.test.ts'te.
const SERVICE_PARAM_ROUTES = new Set([
  '/traces', '/endpoints', '/logs', '/inbox', '/deploys',
  '/metrics', '/explore', '/clusters', '/profiling',
]);
export function serviceFromRoute(pathname: string, search: string): string {
  const sp = new URLSearchParams(search);
  if (pathname === '/service' || pathname === '/service/backtrace') {
    return sp.get('name') || sp.get('service') || '';
  }
  if (pathname === '/pod') return sp.get('service') || '';
  if (SERVICE_PARAM_ROUTES.has(pathname)) return sp.get('service') || '';
  return '';
}

export function contextStarter(pathname: string, search: string): ChatContextStarter | null {
  if (pathname !== '/trace') return null;
  const id = new URLSearchParams(search).get('id')?.trim() ?? '';
  // 32-hex: trace id'nin gerçek şekli. Yarım/bozuk bir id ile soru
  // sormak, backend'in "bulunamadı" demesiyle biter.
  if (!/^[0-9a-f]{32}$/i.test(id)) return null;
  return {
    chip: "Bu trace'i açıkla",
    question: aiSubjectQuestion('trace', id),
  };
}
