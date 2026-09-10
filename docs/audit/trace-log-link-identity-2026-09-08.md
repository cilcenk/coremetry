# Trace → Log İzleme derin linki: kimlik seçimi — AUDIT (kapanış kaydı)

**Tarih:** 2026-09-08 · **Durum:** KAPANDI — v0.10.566–572 + v0.10.578
Banka host/alan adları bu belgeye yazılmaz; örnekler sentetik.

## Soru
Bir trace'ten dış log platformuna giden link hangi kimlikle kurulmalı
(request_id / function_id), birden fazla aday varsa hangisi, tarih hangi
zamandan?

## Ölçülen zemin
- `request_id` yapılandırılmış alan olarak **iki backend'de de** dolabiliyor:
  ES `flatten()` kanonik olmayan alanları `Attributes`'a düzleştirir, CH
  `arraysToMap` ile doldurur. (İlk audit "hiçbir backend'de dolmuyor" demişti —
  YANLIŞTI; v0.10.578'de düzeltildi. Ders: "hiçbir yerde yok" iddiası tek bir
  üretim kaydına bakılmadan kayda geçmez.)
- Bazı servisler kimliği yalnız gövde METNİNE basıyor (`"BsaRequestId": …`
  gibi farklı anahtar adlarıyla) → gövde taraması yedek olarak gerekli.
- Aynı trace'te farklı span'ler farklı `function_id` taşıyabiliyor.

## Kararlar (operatör)
1. Sıra: **yapılandırılmış alan → gövde metni** (578). Şablonun istediği
   anahtarlar (`?keys=`) bilinen yazımlardan (`request_id`, `requestId`,
   `request.id`) önce.
2. Span sırası: **seçili span → ilk hatalı span → root → kalanlar**; tarih
   trace zamanından (log kaydından değil).
3. Birden fazla kimlik → kullanıcıya **seçim menüsü**, anahtar adı olduğu gibi
   gösterilir (uydurulmaz); gövde yolunda gerçek JSON alan adı okunur.
4. Biçimi tutmayan değer düşürülmez, "BİÇİM DOĞRULANAMADI" ile işaretlenir.
5. Trace'te log yoksa doğrudan `function_id`.

## Gemide
566 (kural + tek nokta backend) · 567–572 (menü, split-button, bölüm
başlıkları, gerçek alan adı) · 578 (yapılandırılmış alan önce).
Test: `internal/api/trace_link_identity_attrs_test.go` — alan gövdeyi yener,
anahtar adı taşınır; mutasyonla pinli.
