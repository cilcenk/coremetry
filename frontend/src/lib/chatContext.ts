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
// ⚠ BU LİSTE ELLE TUTULUYOR VE BEDELİ ŞU: yeni bir sayfa `?service=`
// taşımaya başlayıp buraya EKLENMEZSE, sohbet o sayfada BAĞLAM-KÖR
// açılır. Belirtisi yok — tsc göremez, hiçbir test kırılmaz, kapsam
// bandı da çizilmediği için operatör kör olduğunu fark etmez. İki rota
// tam bu yüzden gözden kaçtı (v0.10.45: /endpoint ve /service-map).
// chatContext.test.ts'teki kapı unutmayı en azından TEST hatasına
// çeviriyor.
const SERVICE_PARAM_ROUTES = new Set([
  '/traces', '/endpoints', '/logs', '/inbox', '/deploys',
  '/metrics', '/explore', '/clusters', '/profiling',
  // v0.10.45 — /endpoint (TEKİL detay) listede YOKTU. Sayfa `?service=`
  // hem yazıyor hem okuyor (endpointParam.ts), yani operatör bir
  // endpoint'in detayına inip "bu neden yavaş" diye sorduğunda sohbet
  // SERVİS BAĞLAMI OLMADAN açılıyordu. Kapsam bandı da çizilmediği için
  // "kör" olduğu görünmüyordu — v0.9.1226'nın kapatmayı amaçladığı
  // bulgunun aynısı, iki rotada açık kalmış.
  '/endpoint',
]);

// v0.10.45 — /service-map servis seçimini `?service=` DEĞİL `?focus=`
// ile taşıyor (ServiceMap.tsx:65,156). Ayrı bir eşleme, çünkü tek bir
// param adı varsaymak bu rotayı sessizce dışarıda bırakıyordu.
const FOCUS_PARAM_ROUTES = new Set(['/service-map']);
export function serviceFromRoute(pathname: string, search: string): string {
  const sp = new URLSearchParams(search);
  if (pathname === '/service' || pathname === '/service/backtrace') {
    return sp.get('name') || sp.get('service') || '';
  }
  if (pathname === '/pod') return sp.get('service') || '';
  if (SERVICE_PARAM_ROUTES.has(pathname)) return sp.get('service') || '';
  if (FOCUS_PARAM_ROUTES.has(pathname)) return sp.get('focus') || '';
  return '';
}

// v0.9.1260 — trace'e ek: ekrandaki problem/exception çekmecesi de
// başlangıç çipi olur. İkisinin de sorusu ANLAMLI ve cevaplanabilir
// (dosya başındaki şart): problem → get_problem_root_cause (v0.9.160),
// exception → list_exception_groups + get_exception_samples zinciri
// (v0.9.1233). Anomali BİLİNÇLİ dışarıda: global sohbetin anomali
// öznesini çözen bir yolu henüz yok — boş vaat çipi öneriden kötü.
const TRIAGE_ROUTES = new Set(['/inbox', '/problems', '/anomalies', '/exceptions']);
// Kaba kimlik süzgeci: boş/boşluklu/aşırı uzun değerle soru sormak
// "bulunamadı"yla biter; şekil doğrulamasının gerisi sunucuda.
const looksLikeId = (v: string) => v.length > 0 && v.length <= 64 && !/\s/.test(v);

export function contextStarter(pathname: string, search: string): ChatContextStarter | null {
  const sp = new URLSearchParams(search);
  if (pathname === '/trace') {
    const id = sp.get('id')?.trim() ?? '';
    // 32-hex: trace id'nin gerçek şekli. Yarım/bozuk bir id ile soru
    // sormak, backend'in "bulunamadı" demesiyle biter.
    if (!/^[0-9a-f]{32}$/i.test(id)) return null;
    return {
      chip: "Bu trace'i açıkla",
      question: aiSubjectQuestion('trace', id),
    };
  }
  if (TRIAGE_ROUTES.has(pathname)) {
    // Öncelik problem > exception (triage hiyerarşisiyle aynı).
    const prob = sp.get('problem')?.trim() ?? '';
    if (looksLikeId(prob)) {
      return { chip: 'Bu problemin kök nedeni?', question: aiSubjectQuestion('problem', prob) };
    }
    const fp = sp.get('exception')?.trim() ?? '';
    if (looksLikeId(fp)) {
      return { chip: "Bu exception'ın kök nedeni?", question: aiSubjectQuestion('exception', fp) };
    }
  }
  return null;
}
