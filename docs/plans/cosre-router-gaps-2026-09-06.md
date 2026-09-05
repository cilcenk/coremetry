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
| D5 log alan süzgeci | v0.10.433 GEMİDE | `extractLogFieldQuery` (KQL ya da `<alan> alanında/field'ında <tırnaklı değer>`, log kökü şart, Türkçe sözcük alan olamaz), `logFieldSearchQuery` backend'e göre (CH tırnaksız `*v*` LIKE; ES tam ifade + dürüst not), `guidedLogFieldBundle` (Search ≤20, kanıt: toplam/dağılım/örnek satır + alan değeri), link `/logs?q=<koşulan sorgu>`; sınıflandırıcı `log_field` + logField/logValue; alan sorgusunda D1 aday üreticisi kapalı |
| D7 eksik özne / sayfa / sözlük | v0.10.434 GEMİDE | (a) öznesiz "neden yavaş / yavaşlığın sebebi ne" → ask_service (filo geneli slow_traces'a çökmez; hata/problem şekli filo geneli kalır); (b) `open_page` (sayfa+aç fiili; overview/problems/logs/traces/endpoints; özne: mesaj → bağlam → önceki tur → sor; LLM'siz cevap + `open` alanı → frontend SPA gezer); (c) glossary.go Go haritası (~37 terim + takma adlar, TR/EN kalıp, ek kırpma), sinyal kapısından önce, LLM ve exchangeId yok |
| D2 A→B + attribute araması | v0.10.436 GEMİDE | `pair_requests` (A'dan B'ye / servisinden … servisine / from-to / ->; kaynak servis şart, hedef servis ya da dış/DB düğüm parçası; belirsiz taraf → sor, çip diğer yarıyı taşır); sayım topology_edges_5m'den (MV-first — spec'teki 200-trace örnek sayımı yerine tam sayım), örnek trace'ler RequireServices eş-görünüm (anlatım "birlikte içeren, doğrudan kenar garantisi değil") ya da düğümde Search; `trace_search` (içinde … geçen trace'ler; Search haystack = ad+http.route+attr değerleri, SQL parçası db.statement LIKE); sınıflandırıcı trace_search + searchText |
| D6 mutlak tarih + iki pencere | v0.10.437 GEMİDE | abs_window.go: dd/mm/yyyy, yyyy-mm-dd, "8 ağustos", saat aralıkları (04-08, 04:00-08:30, 4 ile 8), gece sarması, tarihsiz → bugün/dün; tarayıcı tz (`Context.tzOffsetMin`, api.ts gönderir); tek pencere → çıpa+uzunluk her rota için, iki pencere → `window_compare` (buildServiceContext ×2, RED yan yana + fark, linkler kendi range'iyle); servissiz → sor, çip pencere metnini taşır; sinyal kapısı mutlak şekli de geçirir |
| D3 çağrı periyodu | v0.10.438 GEMİDE | `call_period` (periyot/düzenli/her N dk sözcükleri; A→B çift ya da tek servis; servissiz → sor); üç seri: A→B yönlü 5 dk (yeni `TopologyEdgeSeries`, topology_edges_5m, 24 sa, Nyquist notu) + A giden client/dk + B gelen server/dk (QuerySpanMetric count 60 sn, servis kapsamlı ham spans ≤6 sa, "yön kesin değil" notu); otokorelasyon `detectPeriod` (r≥0.45, ≥3 döngü); tepeler UTC; yeni MV yok |
| D4 fan-out | v0.10.439 GEMİDE | `fanout` (hepsi/tamamı/istek başına/ortalama kaç + çift; A, B D2 bölücüsü, C sonraki yönelme); örnek ≤200 en yeni trace (RequireServices A,B) → `SpanEdgesForTraces` (spans trace_id IN, traceFetchPad, LIMIT) → Go'da A→B'li trace'lerde B→C oranı + istek başına ort/en çok (+ A→C doğrudan, dolaylı A…B); C dış/DB düğümse topology kova oranı dürüst notla; kanıt "örnek, kesin sayım değil" der. **Program KAPANDI (8/8 dilim).** |
