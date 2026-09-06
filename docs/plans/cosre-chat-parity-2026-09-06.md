# CoSRE sohbet paritesi — "Claude chat kadar iyi" plan (2026-09-06)

Operatör isteği: CoSRE asistanı trace'leri hızlı getirsin, kullanıcıya kolaylık
sağlasın; "mobile bff" yazınca servisler arasında bulabilsin. Bu belge ÖLÇÜLMÜŞ
bugünkü davranıştan yola çıkar; her dilim kendi sürümü.

## 1. Bugün (ölçüm 2026-09-06, v0.10.460)

Kademeler: guided router (28 deterministik niyet) → drawer → RAG → LLM niyet
sınıflandırıcısı → serbest tool döngüsü (33 MCP tool, küçük yerel model).

Router probu (`routeGuidedIntent`, sentetik katalog `mobile-commercial-bff-prod`,
`mobile-retail-bff-prod`, `checkout-service`…):

| Mesaj | Bugün | Olması gereken |
|---|---|---|
| `mobile bff` / `Mobile bff` / `mobile bff'yi bul` / `mobile bff servisini bul` | **none** → LLM'e düşer | varlık kartı + 2 aday çipi |
| `mobile-retail-bff-prod` (tam ad) / `checkout` / `checkout servisini göster` | **none** | varlık kartı + eylem çipleri |
| `servisleri listele` / `hangi servisler var` | **none** | trafiğe göre ilk N + /services linki |
| `mobile bff hataları` | family_health | doğru |
| `mobile bff son hatalı traceler` / `…hatalı trace'lerini getir` | family_health (**çalıyor**) | aile üzerinden hatalı trace LİSTESİ |
| `mobile bff yavaş traceler` | ask_service (2 aday) | doğru |

Diğer bulgular:
- `list_services.name_contains` HAM alt-dize: "mobile bff" tireli adla asla
  eşleşmez; model "mobile" ya da "bff"e bölmeyi bilmek zorunda.
- Sohbet girişi düz `<input>` (`CopilotChat.tsx:531`, `AIDrawer.tsx:296`);
  öneri çipleri var, ad tamamlama yok.
- Önceki turun servisi devralınıyor (`copilot_guided.go` ~1490) ama ekranda
  görünmüyor, sıfırlanamıyor.
- Evalset küçük: `intent.json` 7 vaka (bul/listele/çıplak ad sınıfı yok).

## 2. Dilimler (sıra = değer / bağımlılık)

| # | Dilim | Ne | Tahmin |
|---|---|---|---|
| D1 | **Bul/aç varlık kademesi** | Yeni niyet `find_entity`: çıplak ad, `bul/göster/aç/listele/hangi` fiilleri → canlı katalogda `nearNames` (servis · operasyon · pod · takım; trace/span/request id zaten var). Cevap = kompakt kart (ad, env, sahip takım, canlı RED — `service_summary_5m`) + çipler: Sağlık · Yavaş trace'ler · Hatalı trace'ler · Hata logları · Sayfayı aç (`open`). 2+ aday → aday çipleri (ask_service deseni, AskIntent=find). "servisleri listele" → trafiğe göre ilk 10 + /services. | ~2 saat |
| D2 | **Aile rotası trace'i çalmasın** | "hatalı/yavaş trace(ler)(i getir)" + aile → aile kapsamlı trace_search / slow_traces (çok-servis filtre, /traces?services=…); family_health yalnız sağlık şekilleri. | ~1 saat |
| D3 | **Tool eşleşmesi + sınıflandırıcı** | `list_services`/`list_operations` `name_contains` → jeton/bulanık eşleşme (`nearNames`), cevapta `matched_by`; tool açıklaması "kullanıcı ifadesini AYNEN geç" (mcp-builder / tool-design). Sınıflandırıcı promptuna çıplak-ad örnekleri → `find_entity`. | ~1 saat |
| D4 | **Girişte ad tamamlama** | `CopilotChat` + `AIDrawer` girişi: ≥3 karakter → sunucu taraflı debounced aday listesi (mevcut servis araması; picker kuralı), ok/Enter kanonik adı yerleştirir → router tam eş. | ~2 saat |
| D5 | **Bağlam çipi** | Aktif varlık girişin üstünde kaldırılabilir çip ("mobile-commercial-bff-prod ×"); "hataları?", "yavaş mı?" ona çözülür; × = bağlamı sıfırla. (conversation-memory: entity memory, görünür.) | ~1 saat |
| D6 | **Trace listesi cevabı** | Liste sonuçlarında LLM anlatımı YOK: deterministik tablo (Start time · Service · Name · Süre · Durum, satır=link) + "Daha fazla → /traces" aynı filtreyle. Anlatım yalnız analiz sorularında. | ~2 saat |
| D7 | **Evalset** | `intent.json` 7 → ~40: çıplak ad, Türkçe ekler ('yi/'nin), yazım hatası, tire/boşluk varyantı, bul/listele fiilleri, aile+trace; `-tags evalset` sürüm kapısında. | ~1 saat |
| D8 | **Serbest döngü koruması** | router none + sınıflandırıcı none → serbest döngüden ÖNCE ucuz varlık taraması; varlık varsa D1 kartı + "ne sormak istedin?" çipleri (tahmin eden LLM yerine). | ~30 dk (D1'e bağlı) |

Önerilen sıra: D1 → D3 → D2 → D7 → D4 → D5 → D6 → D8 (~1,5 gün).

## 3. Kullanılan dış skill'ler (2026-09-06 kuruldu)

`mcp-builder` (tool adı/açıklama disiplini, D3) · `context-engineering` +
`context-engineering-collection` (küçük modelin bağlam bütçesi, D3/D8) ·
`conversation-memory` (D5) · `ai-ui-patterns` (D4/D6 sohbet UI kalıpları).
Elenen: assistant-ui (kütüphaneye özel), vercel chat-sdk (Slack botu),
mcp-apps-builder (ChatGPT uygulamaları).

## 4. Açık sorular

1. D1 kartında canlı RED olsun mu (bir MV okuması, cache'li) — öneri: evet.
2. D4 tetikleyici: `@` öneki mi, 3 karakterden sonra otomatik mi — öneri: otomatik + klavye gezinmesi.
3. Testler yalnız sentetik adlarla (müşteri servis adları repoya girmez).
