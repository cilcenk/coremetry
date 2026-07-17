# Audit — Coremetry MCP server'ını Claude Code/Desktop'a bağlama + üretim sertleştirmesi

**Tarih:** 2026-07-16 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Kapsam:** bağlantı, doğrulama, test, rate-limit. Yazma-tool'u YOK.

## 1. Kök durum — teyit

Ön incelemenin altı maddesi de kodla doğrulandı; iki düzeltme/ek bulgu var.

| İddia | Durum |
|---|---|
| MCP yalnız api/all modunda kurulur, ayrı flag yok | ✅ `main.go` `if mode.api { mcp.New… }` — her api pod'unda aktif |
| Rotalar `GET /api/mcp/sse` + `POST /api/mcp/messages`, HTTP+SSE (2024-11-05) | ✅ `api.go:463-464`; `mcp.go` `ProtocolVersion = "2024-11-05"`; paket yorumu Streamable-HTTP'yi "sonra eklenebilir" diye işaretlemiş |
| Claude Code'da SSE deprecated, `http` öneriliyor | ✅ güncel CC dokümantasyonu `--transport http`'yi birincil gösteriyor; `--transport sse` çalışıyor ama legacy |
| `cmk_` token'ları MCP dahil her korumalı route'ta geçerli, yeni backend gerekmez | ✅ `auth.go:315` `IsAPIToken` → bellek-içi hash cache; `/api/mcp/*` global middleware'in içinde ("Both gated by the same auth middleware", api.go:461) |
| 11 salt-okunur tool + 7 prompt; `ack_problem` implemente değil | ✅ tools.go'da 7 + pivots.go'da 4 = 11; `grep ack_problem internal/mcptools/` boş. ⚠️ **Yorum nit'i:** `mcp.go` paket yorumu v0.6.5 listesinde `ack_problem`'ı örnek olarak sayıyor — hiç var olmadı, dokunulan ilk PR'da düzeltilmeli (yanıltıcı) |
| `internal/mcp/mcp.go` testsiz | ✅ `internal/mcp/` altında tek dosya: mcp.go |
| MCP'ye özel rate-limit yok; `subRateBy` emsali uyarlanabilir | ✅ api.go:174-182 (IP-anahtarlı map+mutex, pencere bazlı) |

### EK BULGU (kritik) — session'lar pod-lokal, çok-pod prod'da transport kırılgan

`mcp.Server.sessions` bellek-içi map (`mcp.go:313-356`). SSE akışı A
pod'una bağlanır, `?sessionId=` taşıyan POST LB tarafından B pod'una
düşerse → `unknown session` 404. Chart bunun için `service.
sessionAffinity: ClientIP` (v0.6.21) taşıyor ama:
- NAT arkasındaki istemcilerde ClientIP çöker (NOTES.txt cookie
  önerisi tarayıcı için; **Claude Code CLI cookie jar taşımaz**).
- OpenShift Route/farklı ingress'lerde afinite garantisi kurulum
  detayı — "üretimde güvenle" hedefi buna yaslanmamalı.

Bu bulgu Seçenek B'nin (stateless Streamable-HTTP) ana gerekçesi.

## 2. Transport seçenekleri

### Seçenek A — SSE ile devam (kod değişikliği yok)

`claude mcp add --transport sse coremetry https://<host>/api/mcp/sse
--header "Authorization: Bearer cmk_…"` bugün çalışır (CC header'ı
hem GET hem POST'a taşır; auth middleware bearer'ı kabul ediyor).

| + | − |
|---|---|
| Sıfır kod; hemen canlı doğrulama yapılabilir | CC'de deprecated — bir sonraki major'da kaldırılma riski |
| | Pod-lokal session sorunu aynen kalır (tek pod/afinite şartı) |
| | SSE, ingress buffering'ine duyarlı (X-Accel-Buffering var ama her proxy saymaz) |

### Seçenek B — Streamable-HTTP (2025-03-26) STATELESS modda ekle — ÖNERİLEN

Tek rota: `POST /api/mcp` (+ `GET /api/mcp` → 405; spec'e göre
server-push akışı opsiyonel, açmıyoruz). İstemci JSON-RPC isteğini
POST'lar, yanıt DOĞRUDAN response body'de `application/json` döner.
Spec gereği server session İSTEMEYEBİLİR: `Mcp-Session-Id` hiç
verilmez → istemci sessionless çalışır. Mevcut `dispatch()`
transport'tan bağımsız — initialize/tools/prompts/resources aynen
yeniden kullanılır; `initialize` yanıtı `ProtocolVersion`'ı
"2025-03-26" döner (SSE yolu 2024-11-05'te kalır, iki sabit).

| + | − |
|---|---|
| **Pod-lokal session sorunu KÖKTEN çözülür** — her POST bağımsız, LB-güvenli, afinite şartı yok | ~150-250 satır yeni kod (handler + version sabiti + testler) |
| CC birincil transport'u: `claude mcp add --transport http` | İki transport'lu yüzey — dokümantasyon iki komut taşır |
| SSE/buffering/keepalive dertleri yok — düz istek/yanıt | Server-initiated notification bu yolda yok (bugün kullanan tool da yok) |
| SSE aynen kalır — mevcut istemci/copilot kırılmaz | |
| Rate-limit tek noktadan (aşağıda) iki transport'u da kapsar | |

**Zamanlama önerisi: şimdi.** Gerekçe: (a) CC deprecation'ı yönü
netleştirmiş, (b) stateless mod EK BULGU'daki üretim kırılganlığını
başka hiçbir seçeneğin çözemediği şekilde çözüyor, (c) dispatch()
hazır olduğundan maliyet düşük.

### Seçenek C — B + SSE'yi kaldırmak

Reddedildi: GenAI Studio entegrasyonu (v0.8.444 gerekçesi) ve olası
mevcut istemciler SSE'de; "backwards-compat shim ekleme" kuralı
KALDIRIRKEN geçerli, çalışan transport'u söküp risk almanın
karşılığı yok. SSE, B canlıda kanıtlanana kadar kalır; kaldırma
ayrı bir karar.

## 3. Rate-limit tasarımı (iki transport'u da kapsar)

- **Yer:** `mcp.Server`'a opsiyonel hook: `SetToolCallGate(func(ctx
  context.Context, tool string) error)` — `handleToolsCall` girişinde
  çağrılır; nil ise sıfır maliyet. Auth bilgisi mcp paketine
  SIZDIRILMAZ (paket auth-agnostik kalır); gate'i api katmanı kurar.
- **Anahtar:** `auth.ClaimsFromContext(ctx)` → token ID (cmk_) veya
  kullanıcı adı (JWT). IP değil — tek NAT arkasındaki iki ajan
  birbirini boğmasın.
- **Politika:** sabit pencere, `subRateBy` deseninin map+mutex'i:
  **60 tools/call / dakika / kimlik** (list/get/search zaten
  clampLimit'li ve MV-öncelikli; 60/dk bir Claude Code oturumunun
  agresif araştırmasını rahat taşır, kaçak döngüyü keser).
  Aşımda JSON-RPC error (code -32000, "rate limited, retry in Ns")
  — HTTP 429 DEĞİL: istemci kütüphaneleri JSON-RPC hatasını tool
  sonucu olarak LLM'e gösterir, LLM bekleyip devam edebilir.
- `initialize`/`tools/list`/`prompts/*` limitsiz (ucuz, keşif).

## 4. internal/mcp test planı (yeni: mcp_test.go)

httptest tabanlı, sahte tool'la (`RegisterTool` + kaydedici handler):

1. **SSE lifecycle:** GET → ilk event `endpoint` + sessionId; POST
   initialize → yanıt SSE'den `message` event'iyle gelir; bağlantı
   kopunca session silinir (`lookupSession` nil).
2. **JSON-RPC framing:** bilinmeyen method → -32601; bozuk JSON →
   400; notification → 202 + SSE'ye yanıt YAZILMAZ; non-"2.0" kabul
   ama loglanır (davranış pin'i).
3. **Unknown session:** POST `?sessionId=yok` → 404.
4. **tools/call:** args decode hatası → hata yanıtı; happy path →
   sonuç şekli; gate reddi → -32000 (rate-limit hook'u).
5. **Streamable-HTTP (B gelirse):** POST /api/mcp initialize →
   body'de JSON yanıt + 2025-03-26; tools/call round-trip; Accept
   başlığı ihmalinde de JSON dönmesi.
6. Wedged-consumer düşürmesi (2s timeout) — timeout'u test-edilebilir
   kılmak için pakete `sendTimeout` var'ı (davranış değişmez).

## 5. Canlı doğrulama planı (operatörle, implementasyon sonrası)

1. Settings → API Tokens → **viewer** rollü token oluştur (`cmk_…`
   yalnız oluşturma anında görünür).
2. Bağlan:
   - B geldiyse: `claude mcp add --transport http coremetry
     https://<host>/api/mcp --header "Authorization: Bearer cmk_…"`
   - A'da kalındıysa: `--transport sse … /api/mcp/sse`
3. Claude Code içinde `/mcp` → coremetry connected; sonra üç uçtan
   uca çağrı: `list_services` (MV, ucuz) → `list_problems` →
   `search_logs` (ES yolu — prod'da logstore ES olduğundan gerçek
   maliyet yolu test edilmiş olur).
4. Yanlış token + süresi geçmiş token → 401; viewer token'la yüzeyin
   tamamı okunabilir (salt-okunur 11 tool).
5. Çok-pod prod'da (B varsa) afinite KAPALIYKEN 20 ardışık tool-call
   — session hatası görülmemeli.

## 6. SRE runbook taslağı (docs/runbooks/mcp-claude-code.md olarak ayrı dosya)

- **Token:** Settings → API Tokens → New token, rol: viewer (salt
  okuma yeter; editor/admin MCP için gereksiz). Token'ı kasaya koy.
- **Bağlanma:** yukarıdaki `claude mcp add` komutu (kurulum
  transport'una göre tek satır). Claude Desktop: aynı URL+header,
  Settings → Connectors.
- **Ne zaman hangi tool:** giriş noktası `list_services` /
  `list_problems`; servis kazısı `get_service_health`; iz →
  `get_trace` + `get_logs_for_trace`; metrik → `query_metric`,
  histogram ucu → `get_exemplar_traces`; async zincir →
  `get_linked_traces`.
- **Prompt'lar:** olay anlatımı için `explain_problem` /
  `suggest_runbook`; deploy şüphesinde `deploy_impact`; iki izi
  ayırt etmede `compare_traces`.
- **Limitler:** 60 tool-call/dk/token; aşımda LLM'e "rate limited"
  döner, bekleyip devam eder. Token iptali: Settings → API Tokens →
  Revoke (anında, cache invalidation mevcut).

## 7. Tasarım notu — ileride yazma-tool'u gelirse (implementasyon YOK)

`ack_problem` benzeri bir mutation eklendiği gün: audit girdisi
`s.audit(...)`'in actor'ü bugün HTTP kullanıcısından geliyor; MCP
çağrısında actor = token sahibi olur ama KAYNAK ayırt edilemez.
O PR'da audit_log'a `source` alanı (ui | api | mcp) ve MCP yolunda
`source=mcp` + tool adı detail'e yazılmalı; ayrıca /mcp-tools
skill'indeki "operator-in-the-loop olmadan mutation ekleme"
anti-pattern'i gereği tool default-kapalı bir flag arkasında
gelmeli. Bugünkü kapsamda hiçbir adım yok.

## 8. Kapsam dışı (bilinçli)

- Yazma-tool'ları ve audit `source` alanı (yalnız §7 notu).
- SSE'nin kaldırılması (§2C — ayrı karar).
- OAuth/OIDC MCP handshake'i — cmk_ token yeterli, skill kuralı.
- Server-initiated notification'lar (Streamable-HTTP GET stream'i).
- mcp.go paket yorumundaki `ack_problem` nit'i — B implementasyonu
  aynı dosyaya dokunacağı için o commit'te düzeltilir.

## 9. Önerilen paket (onaya sunulan)

1. **B**: Stateless Streamable-HTTP `POST /api/mcp` (SSE kalır).
2. **§3**: token-anahtarlı 60/dk tools/call gate (iki transport).
3. **§4**: mcp_test.go (SSE + framing + gate + yeni transport).
4. **§5**: canlı doğrulama + **§6** runbook dosyası.

Tahmin: ~2 saat (tek release, `v0.8.575 — feat(mcp): …`).
