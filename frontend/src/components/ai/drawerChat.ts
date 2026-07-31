import { aiSubjectTitle, type AIKind, type AISubject } from '@/lib/aiSubject';

// drawerChat — AI çekmecesi içindeki sohbetin BAĞLAM DEVRİ kodeği
// (v0.9.479, operatör raporu: "Chat'te devam et" global CoSRE
// penceresini çekmecenin üstüne açıyordu ve sohbet ekrandaki
// açıklamayı bilmiyordu — ekranda exception dururken CoSRE "bu trace
// ID'ye ait veri yok" deyip filo geneli JVM GC uyarılarını anlattı).
//
// Devir İKİ parçadan oluşur, ikisi de burada saf üretilir:
//
//   1. seed SORUSU (aiSubjectQuestion) — konuşmanın ilk kullanıcı turu
//      olarak tele gider. Sunucudaki takip-devralma (v0.9.410
//      applyFollowUpContext) bunu "önceki soru" olarak görür: çekmece
//      bir SERVİS öznesindeyse "peki hata logları?" gibi takipler
//      guided router'da gerçek telemetriye oturur. Ekranda bubble
//      olarak ÇİZİLMEZ (açıklama zaten hemen üstünde duruyor).
//
//   2. explain BAĞLAMI (buildExplainContext) — chat isteğinin
//      `context.explain` alanı. Sunucu narration bloğuna katar
//      (internal/api/copilot_drawer.go). Konuşma geçmişine sentetik
//      bir "asistan cevabı" turu koymak yetmezdi: guided narration
//      çağrısı konuşma geçmişini HİÇ görmez, yalnız SORU+VERİ görür.
//
// Saf modül: React'ten bağımsız vitest (node ortamı) — emsal
// lib/aiSubject.ts / lib/inboxUrl.ts.

// EXPLAIN_CONTEXT_MAX — tele giden açıklamanın rune tavanı. Sunucu da
// aynı sınırla yeniden kırpar (drawerExplainMaxRunes); buradaki kesme
// 50 KB'lık bir POST'u baştan engeller. İki katman bilerek: sunucu
// kendi sözleşmesini frontend'e emanet etmez.
export const EXPLAIN_CONTEXT_MAX = 3000;

const TRUNC_SUFFIX = '\n…(kısaltıldı)';

// Kanıt id'lerinden bloğa girecek üst sınır — küçük modelde 40 tane
// trace id bağlamı boğar; ilk N zaten "örnek" niteliğinde.
const EVIDENCE_MAX = 10;

export function clampExplain(s: string): string {
  const t = (s ?? '').trim();
  const runes = [...t];
  if (runes.length <= EXPLAIN_CONTEXT_MAX) return t;
  return runes.slice(0, EXPLAIN_CONTEXT_MAX).join('').trim() + TRUNC_SUFFIX;
}

// subjectRef — açıklamanın hangi nesneye ait olduğunun tam (kısaltmasız)
// künyesi. Model fingerprint/trace id'yi cevabında kullanabilsin diye
// aiSubjectSubtitle'ın kısaltmalı hâli KULLANILMAZ.
function subjectRef(s: AISubject): string {
  switch (s.kind) {
    case 'trace':          return `trace ${s.id}`;
    case 'span':           return `trace ${s.id} · span ${s.spanId}`;
    case 'service-health': return `servis ${s.id}`;
    case 'exception':      return `exception grubu ${s.id}`;
    case 'problem':        return `problem ${s.id}`;
    case 'incident':       return `incident ${s.id}`;
    case 'anomaly':        return `anomali ${s.id}`;
    case 'runbook':        return `runbook (problem ${s.id})`;
  }
}

// buildExplainContext — `context.explain` alanının gövdesi: künye +
// açıklama metni + kanıt id'leri. Metin boşsa BOŞ döner (henüz cevap
// gelmemiş çekmecede sohbet açılmaz).
export function buildExplainContext(i: {
  subject: AISubject;
  text: string;
  spanIds?: string[];
  traceIds?: string[];
}): string {
  const body = (i.text ?? '').trim();
  if (!body) return '';
  const parts = [`KONU: ${aiSubjectTitle(i.subject)} — ${subjectRef(i.subject)}`, body];
  if (i.spanIds?.length) parts.push(`Kanıt span'leri: ${i.spanIds.slice(0, EVIDENCE_MAX).join(', ')}`);
  if (i.traceIds?.length) parts.push(`Kanıt trace'leri: ${i.traceIds.slice(0, EVIDENCE_MAX).join(', ')}`);
  return clampExplain(parts.join('\n'));
}

// aiSubjectQuestion — özneye karşılık gelen doğal soru. v0.9.165'te
// CopilotExplain içindeki `chatSeed`'di; iki çağıran (satır-içi explain
// ve çekmece sohbeti) olduğu için saf modüle taşındı — tek kaynak.
export function aiSubjectQuestion(kind: AIKind, id: string): string {
  switch (kind) {
    case 'service-health': return `${id} servisinin sağlığı nasıl?`;
    case 'problem':        return `Bu problemin kök nedeni ne? (problem ${id})`;
    case 'runbook':        return `${id} runbook'unun adımlarını özetle`;
    case 'anomaly':        return `Bu anomaliyi açıkla (${id})`;
    case 'incident':       return `Bu incident'i açıkla (${id})`;
    case 'exception':      return `Bu exception grubunun kök nedeni ne? (${id})`;
    case 'span':           return `Bu span'i açıkla (${id})`;
    default:               return `Bu trace'i açıkla (${id})`;
  }
}

// drawerFollowups — çekmece sohbetinin çip önerileri. Global chat'in
// statik FOLLOWUPS listesi ("Takımımın servisleri nasıl?") çekmecede
// konu dışıdır; çipin KENDİSİ yeni bir affordance değil, yalnız
// içeriği özneye göre üretilir.
//
// service-health öznesinde öneriler bilerek servis ADINI taşır: bu
// şekiller guided router'da tek başına yönlenir (copilot_followup.go
// guidedSuggestions ile aynı sözleşme) → çip tıklaması gerçek
// telemetriye gider, açıklama özetine değil.
export function drawerFollowups(s: AISubject): string[] {
  if (s.kind === 'service-health') {
    return [
      `${s.id} en yavaş trace'ler?`,
      `${s.id} hata logları?`,
      `${s.id} son deploy etkisi?`,
    ];
  }
  return ['Bu neden oluyor?', 'Nasıl düzeltirim?', 'Hangi kanıta dayanıyorsun?'];
}
