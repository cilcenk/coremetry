# CoSRE doğal-dil tooling — router boşlukları programı (onay 2026-09-06)

Kaynak: prod `/ai` "Router boşlukları" paneli (operatör ekran görüntüleri,
2026-09-05/06). Müşteri, servis, takım ve alan adları bu belgeye ve koda
GİRMEZ; örnekler sentetiktir (checkout/payments/inventory, `SY-XYZ`).

## Soru sınıfları

| # | Sınıf | Örnek (sentetik) | Dilim |
|---|---|---|---|
| 1 | A→B giden isteklerin listesi / sayfası | "A'dan B'ye giden isteklerin tamamını göster" | D2 |
| 2 | A→B çağrı periyodu | "A'dan B'ye atılan isteklerde her 5 dk gibi periyot var mı" | D3 |
| 3 | A→B→C fan-out | "A→B gidenlerin hepsi C'ye gidiyor mu, istek başına ortalama kaç" | D4 |
| 4 | Takım koduna ait servisler | "SY-XYZ'e ait servisleri listele" | D1 |
| 5 | Bulunamayan/belirsiz servis adı | "login external servisinde hata var mı" | D1 |
| 6 | Log alan süzgeci | "url.full field'ında \"/x/y\" geçen loglar" | D5 |
| 7 | Mutlak tarih + iki pencere | "08/08/2026 saat 04-08 ile 08-09 arası servis süreleri" | D6 |
| 8 | Eksik özne / sayfa aç / sözlük | "yavaşlığın sebebi ne", "sayfasını aç", "requestid nedir" | D7 |
| 9 | Doğal dilde trace araması | "X servisinden içinde <host/route/sorgu> geçen trace'ler" | D2 |
| — | Trace ilk-açılış CoSRE baloncuğu | "Bu trace'i açıklamamı ister misin?" Evet / Sağol | D8 |

Kapsam dışı (bilinçli `none`): genel bilgi soruları.

## Kararlar (açık sorular, 2026-09-06)

- D2: yönlü A→B sayımı 200 trace'lik örnekten + eş-görünüm `/traces?services=A,B`
  linki (anlatım eş-görünüm olduğunu söyler).
- D3: ham `spans` dakikalık sayım (servis-kapsamlı, ≤6 sa); yeni 1 dk edge MV'si YOK
  (5 dk MV Nyquist yüzünden 5 dk periyodu göremez).
- D6: tarayıcı saat dilimi (`tzOffsetMin`), Europe/Istanbul sabiti değil.
- D8: sekme oturumu başına bir kez + kalıcı ret (localStorage); `/ai` etiketi
  `explain-trace:nudge`.
- D7 sözlük: deterministik Go haritası (LLM uydurmaz).
- Yeni CH şeması yok, yeni rota yok; her dilim kendi sürümü.

## Durum

| Dilim | Sürüm | Not |
|---|---|---|
| D1 belirsizlik + takım | v0.10.429 GEMİDE | `nearNames`/`serviceCandidates`; `guidedAskService` ("hangisini kastettin?" çipleri tam kılavuz cümle); sağlık/hata şekli + parça adı → aile rotası korunur (v0.9.192), neden/yavaş/deploy/log şekli + aile → SORAR (eskiden filo geneline çöküyordu); takım kodu jeton eşi, `team` slotu, `team_services` niyeti; sınıflandırıcı prompt'u yaklaşık adı AYNEN verir |
| D8 trace baloncuğu | v0.10.432 GEMİDE | `TraceExplainNudge` FAB'ın üstünde (CopilotChat içinde; sohbet açıkken/copilot kapalıyken/public trace'te yok); karar `shouldNudgeExplain` saf; Evet → çekmece `?ai=trace:<id>&aisrc=nudge` → istek `?src=nudge` → yüzey `explain-trace:nudge` (whitelist'li sonek, yalnız explain-*); Sağol → localStorage kalıcı ret; sekme başına bir kez (sessionStorage). Not: explain önbelleği isabet ederse ai_calls satırı yazılmaz (deliverExplain kısa devresi) |
| D5 log alan süzgeci | — | |
| D7 eksik özne / sayfa / sözlük | — | |
| D2 A→B + attribute araması | — | |
| D6 mutlak tarih + iki pencere | — | |
| D3 çağrı periyodu | — | |
| D4 fan-out | — | |
