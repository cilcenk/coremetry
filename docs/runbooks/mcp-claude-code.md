# Runbook — Coremetry MCP'yi Claude Code/Desktop'a bağlama

(v0.9.14, yetki katmanı v0.9.1136; tasarım:
docs/audit/mcp-claude-code-production-audit.md +
docs/plans/ai-assistant-design-2026-08-16.md §K7)

## 1. Token üret

Settings → API Tokens → **New token**, rol: **viewer** (33 tool'un
tamamı salt-okunur ve hepsi viewer seviyesinde — editor/admin
GEREKMEZ). `cmk_…` değeri yalnız oluşturma anında görünür; kasaya
koy. İptal: aynı ekrandan Revoke (anında, cache invalidation'lı).

### Token = rol, kullanıcı DEĞİL (bilinçli sınır)

`cmk_` token'ı bir kullanıcıya bağlı değildir; **kendi rolünü**
taşır (`Claims.Role` = token'ın rolü, `Claims.UserID` =
`token:<id>`). Sonuçları:

- **Yetki tamamen token'ın rolüdür.** Token'ı kim kullanıyorsa o rolle
  konuşur; kişi-bazlı kısıtlama YOK. Tek-kiracılı ürün duruşuyla
  tutarlı: kişiselleştirme değil, iptal edilebilir kimlik hedeflenir.
- **Rate bütçesi token başınadır** (aşağıdaki 60/dk). İki ajanı
  ayırmak istiyorsan iki token üret.
- **İzleme/denetim token adı üzerinden yapılır** (`token:<ad>`), kişi
  üzerinden değil. Ekip başına ayrı token üretmek en pratik ayrım.
- **Sıkma gerektiğinde**: en düşük rolle üret (viewer yeter), gerekmeyince
  Revoke et. Rol yükseltmek için yeni token üret — token'ın rolü
  sonradan değiştirilmez.

Rol zorlaması nerede: her tool/resource/prompt kaydı bir `MinRole`
taşır (`internal/mcp`), kapı (`internal/api/mcp_gate.go`) çağrı
öncesi token rolüyle karşılaştırır. Yetersizse JSON-RPC **-32001** ve
gereken rolü söyleyen okunur bir metin döner (model boşuna yeniden
denemez). Bugün 33 tool'un tamamı `MinRole=""` (viewer tabanı; v0.9.1141'ta
beş keşif tool'u eklendi — list_operations / list_environments /
list_clusters / list_deploys / find_trace_by_span; v0.9.1142'de
find_trace_by_request_id — yapılandırılmış kurumsal istek numarası →
trace, penceresi kimliğin İÇİNDEKİ damgadan gelir; v0.9.1146'da üç analiz
tool'u — get_topology / get_blast_radius / get_log_histogram, üçü de
mevcut MV/logstore okumasını köprüler; v0.9.1147'de dört guided-parite
tool'u — get_db_health / get_messaging_health / get_pod_health /
list_problem_window_events, in-app guided sohbetin kanıt paketleriyle
AYNI veri katmanından).

## 2. Bağlan

**Birincil — Streamable-HTTP (önerilen; çok-pod kurulumda afinite
GEREKTİRMEZ, stateless):**
```bash
claude mcp add --transport http coremetry \
  https://<coremetry-host>/api/mcp \
  --header "Authorization: Bearer cmk_..."
```

**Legacy — SSE (eski istemciler; çok-pod'da sticky-session ister):**
```bash
claude mcp add --transport sse coremetry \
  https://<coremetry-host>/api/mcp/sse \
  --header "Authorization: Bearer cmk_..."
```

Claude Desktop: Settings → Connectors'a aynı URL + header.
Doğrulama: Claude Code'da `/mcp` → coremetry **connected**; sonra
üç uçtan uca çağrı iste: `list_services` (MV, ucuz) →
`list_problems` → `search_logs` (prod'da ES yolu — gerçek maliyet
yolu da test edilmiş olur).

## 3. Hangi tool / prompt ne zaman

| İhtiyaç | Araç |
|---|---|
| "Şu an ne sağlıksız?" girişi | `list_services`, `list_problems`, `list_anomalies` |
| Servis kazısı | `get_service_health` |
| "Yukarımda/aşağımda ne var" · servis grafiği | `get_topology` (odaklı: upstream/downstream; boş `service` = filo kenarları; 1 hop) |
| "Bu bozulursa kim bozulur" · etki cümlesi | `get_blast_radius` (YUKARI-akış çağıranlar + cascade bayrağı; aşağı-akış get_topology'de) |
| "Hata ne zaman başladı" · log hacmi şekli | `get_log_histogram` (severity bantlı kovalar; satırlar için `search_logs`) |
| "Pod'lar / JVM ne durumda" | `get_pod_health` (servisli: envanter + heap; servissiz: filo heap sıralaması; restart/faz YOK) |
| "Hangi db yavaş" · "kuyruk tarafı nasıl" | `get_db_health`, `get_messaging_health` (filo geneli; consumer-lag ÖLÇÜLMÜYOR) |
| "Dün gece neler oldu" · vardiya devri | `list_problem_window_events` (açılan + ÇÖZÜLEN; `list_problems` yalnız şu anki kümeyi verir) |
| İz → log/metrik pivotları | `get_trace`, `get_logs_for_trace`, `get_metrics_for_span` |
| Metrik sorgusu / histogram ucu | `query_metric`, `get_exemplar_traces` |
| Async zincir takibi | `get_linked_traces` |
| Olay anlatımı / runbook önerisi | prompt: `explain_problem`, `suggest_runbook` |
| Deploy şüphesi / iz karşılaştırma | prompt: `deploy_impact`, `compare_traces` |

## 4. Limitler ve davranış

- **Rate limit:** kimlik (token) başına **60 çağrı/dk**; aşımda LLM'e
  JSON-RPC **-32000** olarak "rate limited … retry in Ns" döner — model
  bekleyip devam eder (bağlantı kopmaz, 429 yok). v0.9.1136'dan beri
  bütçe `tools/call` + `resources/read` + `prompts/get` toplamıdır
  (üçü de aynı chstore okumalarını yapıyor; eskiden yalnız
  `tools/call` sayılıyordu). `initialize`/`ping`/`*/list` limitsiz —
  veri okumazlar.
- **Rol reddi (-32001) bütçeyi TÜKETMEZ** ve "bekle" demez: metin
  gereken rolü söyler, model istemeden yeniden denemez.
- Streamable-HTTP tamamen stateless: her POST bağımsız — LB hangi
  pod'a düşürürse düşürsün çalışır. SSE yolunda ise session pod-lokal:
  çok-pod'da `service.sessionAffinity: ClientIP` (chart v0.6.21) ya da
  cookie-sticky ingress şart.
- Tüm tool'lar salt-okunur; mutation tool'u yok (eklenirse audit_log
  source alanı tasarım notu: audit §7).

## 5. Sorun giderme

| Belirti | Neden / çözüm |
|---|---|
| 401 | Token süresi/yanlış değer — Settings'ten yeni token |
| JSON-RPC -32001 "requires the … role" | Token'ın rolü o tool/resource/prompt için yetersiz. Token rolü sonradan değişmez: doğru rolle YENİ token üret, eskisini Revoke et |
| -32001 "unrecognized MinRole" | Kayıt defterinde yazım hatası (Coremetry bug'ı) — kapı bilinçli olarak KAPALI yönde davranır; sürümü not edip bildir |
| `/mcp` "failed to connect" (http) | URL `/api/mcp` mi (sse path'i değil)? Proxy POST gövdesini kesiyor mu? |
| SSE bağlanıyor, çağrılar "unknown session" | Çok-pod + afinite yok → Streamable-HTTP'ye geç (kalıcı çözüm) |
| Sık "rate limited" | Ajan tool-loop'ta — sorguyu daraltın; limit kimlik başına, ikinci token ayrı bütçe demektir |
