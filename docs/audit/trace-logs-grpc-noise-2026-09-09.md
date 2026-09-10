# Trace Detail › Logs sekmesi: gRPC span event gürültüsü — AUDIT (kapanış kaydı)

**Tarih:** 2026-09-09 · **Durum:** KAPANDI — v0.10.577, v0.10.579

## Sorun
"11 log satırı + 253 span event'i": event'lerin ezici çoğunluğu OTel gRPC
instrumentation'ının per-message event'i (`body: "message"`, attribute'ları
yalnız `message.id` + `message.type`). Teşhis değeri yok, gerçek logları
görünmez yapıyor.

## Ölçülen zemin — öncülü değiştiren bulgu
- Span event → log satırı dönüşümü **backend'de yok**; tamamen frontend'de
  (`lib/traceEventLogs.ts`). `origin: "span-event"` alanı yalnız TypeScript'te.
- Tek yükü (`/api/traces/{id}` → `spans.events` blob'u) **üç yüzey** okuyor:
  Logs sekmesi, waterfall exception rozeti, span detayı Events bölümü.
  Sunucuda süzmek üçünden birden düşürürdü → operatörün "yalnız Logs sekmesi,
  waterfall/detay olduğu gibi" kısıtıyla bağdaşmıyor. Filtre frontend'de.
- Logs sekmesi sayfalamıyor; span event'leri hiç limitlenmiyor → "yarım
  sayfa" riski bu yüzeyde yok (SQL'de `arrayJoin` sonrası LIMIT denenseydi
  doğardı).
- i18n kataloğu var ama bu sayfa kullanmıyor; sayaç metni iki dildeydi.
- Tercih kalıcılığı: sunucu tercih ucu kolon modelini doğrular (boolean 400)
  → `lib/storage.ts` + `STORAGE_KEYS`, `'1'/'0'`.

## Kural (operatör: "yalnız gRPC SENT ve RECEIVED")
577: origin span-event ∧ body "message" ∧ anahtarlar ⊆ {message.id,
message.type} ∧ message.type ∈ {SENT, RECEIVED}. Exception, ekstra
attribute'lu ve gerçek gövdeli event'ler KORUNUR.
579 (genişletme, onaylı): hiç attribute taşımayan işaretçiler
(`redis.encode.start/end` gibi) de gürültü — ad listesi yok, şekil kuralı;
**ERROR ve üstü hiçbir koşulda gizlenmez**.

## Davranış
Varsayılan gizli; `.facet` çipi ile açılır; tercih tarayıcıda küresel
(`trace.logs.grpcmsgs`); sayaç "N span event'i (M gürültü event'i
gizlendi)"; tümü gizliyse "hiç log yok" teşhisi YERİNE gizlenen sayısı +
çip; ham `eventRows` waterfall çiplerini beslemeye devam eder (kapsam
sınırı pin testi: `Trace.grpcmsgs.pin.test.ts`).
