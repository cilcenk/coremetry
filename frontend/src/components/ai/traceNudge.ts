// traceNudge — v0.10.432 (CoSRE router boşlukları D8): trace sayfası İLK
// açılışta CoSRE baloncuğunun ("Bu trace'i açıklamamı ister misin?")
// çıkıp çıkmayacağı. SAF: girdiler çağırandan (adres + depolama), karar
// burada; bileşen yalnız çizer. Kurallar (spec 2026-09-06):
//   - yalnız /trace (public trace ASLA — orada CopilotChat de yok),
//   - bir trace id'si varken,
//   - AI çekmecesi zaten açık değilken (?ai=),
//   - kalıcı ret yoksa (localStorage),
//   - bu sekme oturumunda daha önce sorulmadıysa (sessionStorage; cevapsız
//     bırakılan soru da "soruldu" sayılır — her trace'te dürtmek gürültü).
// Baloncuk LLM ÇAĞIRMAZ: "Evet" çekmeceyi açar, explain oradan koşar
// (chatContext.ts'in "chip önerir, tık karar verir" ilkesi).

export interface TraceNudgeInput {
  pathname: string;
  traceId: string;
  aiOpen: boolean;
  declined: boolean;
  askedThisTab: boolean;
}

export const TRACE_PAGE_PATH = '/trace';

export function shouldNudgeExplain(i: TraceNudgeInput): boolean {
  return i.pathname === TRACE_PAGE_PATH && i.traceId !== '' && !i.aiOpen && !i.declined && !i.askedThisTab;
}
