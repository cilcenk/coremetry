// msgBalance — /messaging "Denge" kolonunun saf çekirdeği (v0.9.815).
//
// SORU: bu topic'te üretim ile tüketim birbirini tutuyor mu? Tablo
// bugüne dek Produce/min ve Consume/min'i YAN YANA iki sayı olarak
// gösteriyordu ve ilişkiyi kurmayı operatörün gözüne bırakıyordu —
// 1.240 ile 1.190 arasındaki farkın önemli olup olmadığı, iki sayıya
// bakarak anlaşılmıyor. Denge o farkı ORANA çevirir ve eşiğe vurur.
//
// ORAN: (üretim − tüketim) / üretim.
//   · pozitif → üretim tüketimi geçiyor, iş birikiyor (BACKLOG RİSKİ)
//   · negatif → tüketim üretimi geçiyor, birikmiş iş boşalıyor
//   · sıfıra yakın → dengede
//
// Bölen ÜRETİM: "üretilenin yüzde kaçı tüketilemedi" sorusunun doğal
// bölenidir ve ölçekten bağımsızdır (dakikada 10 mesajlık bir topic ile
// dakikada 100.000'lik topic aynı eksende okunur).
//
// UYARI — BU BİR CONSUMER LAG DEĞİLDİR. Lag broker tarafı bir ölçüdür
// (kafka consumer group offset farkı) ve bu kurulumda broker metriği
// ingest edilmiyor. Denge yalnız span sayılarının ORANI: aynı pencerede
// kaç producer, kaç consumer span'i görüldü. Bir tüketici mesajı alıp
// span üretmiyorsa ya da örnekleme iki tarafı farklı oranda kırpıyorsa
// bu oran yanıltır — eşik bu yüzden dar değil (%10), ve çip "risk"
// diyor, "lag şu kadar" demiyor.

export type MsgBalanceState =
  | 'accumulating' // birikiyor — üretim tüketimi geçiyor
  | 'balanced'     // dengede
  | 'draining'     // boşalıyor — tüketim üretimi geçiyor
  | 'unknown';     // ayrım yok / iki taraf da sıfır

export interface MsgBalanceResult {
  state: MsgBalanceState;
  // Sıralama değeri. null → useDataTable'ın "null'lar en alta, kendi
  // aralarında KARARLI" davranışına düşer (lib/dataTable.ts sortRows),
  // yani ölçülemeyen satırlar ölçülenlerin arkasında gelen sırayı korur.
  ratio: number | null;
}

// MSG_BALANCE_THRESHOLD — ±%10. DAR DEĞİL, bilinçli: iki taraf ayrı
// SDK'lardan, ayrı örnekleme oranlarıyla ve 5 dakikalık kova
// sınırlarında sayılıyor. %2'lik bir eşik her topic'i sürekli
// yanıp sönen bir uyarıya çevirirdi — yani hiçbir şey söylemezdi.
export const MSG_BALANCE_THRESHOLD = 0.10;

export function msgBalance(produce?: number, consume?: number): MsgBalanceResult {
  const p = Number.isFinite(produce) ? (produce as number) : 0;
  const c = Number.isFinite(consume) ? (consume as number) : 0;

  // İki taraf da yok — bu satırda kind ayrımı hiç oluşmamış (broker
  // chatter'ı, ya da SDK kind bilgisi taşımıyor). '—' göstermek,
  // "mükemmel dengede" demekten dürüst.
  if (p <= 0 && c <= 0) return { state: 'unknown', ratio: null };

  // Üretim yok ama tüketim var: pencerede yalnız tüketici tarafı
  // görüldü (üretim pencereden önce olmuş olabilir). Tam boşalma.
  if (p <= 0) return { state: 'draining', ratio: -1 };

  const ratio = (p - c) / p;
  if (ratio > MSG_BALANCE_THRESHOLD) return { state: 'accumulating', ratio };
  if (ratio < -MSG_BALANCE_THRESHOLD) return { state: 'draining', ratio };
  return { state: 'balanced', ratio };
}

// msgP99Delta — "kötüleşenler önce" sıralamasının saf çekirdeği
// (v0.9.815; Endpoints v0.9.761'in sortEndpointsByP99Delta ikizi).
//
// prior yoksa null döner ve satır listenin ALTINA, kendi aralarında
// GELDİĞİ SIRAYLA (sunucudan spanCount DESC) düşer — brief'teki
// "prior yoksa spanCount DESC'e düş" davranışı, ayrı bir kod yolu
// olmadan, sortRows'un null-sonda + kararlı sözleşmesinden.
//
// ŞERH: burada ölçülen ŞEY messaging SPAN p99'unun kötüleşmesi, uçtan
// uca (produce→consume) gecikmenin değil. Genel bakış payload'ı E2E
// TAŞIMIYOR (E2E span_links korelasyonu gerektiriyor ve yalnız
// destination başına, drawer'da hesaplanıyor) — dolayısıyla "E2E p95 Δ"
// diye sıralamak, var olmayan bir veriye göre sıralamak olurdu.
export function msgP99Delta(p99Ms: number, priorP99Ms?: number): number | null {
  if (priorP99Ms === undefined || !(priorP99Ms > 0)) return null;
  return (p99Ms - priorP99Ms) / priorP99Ms;
}
