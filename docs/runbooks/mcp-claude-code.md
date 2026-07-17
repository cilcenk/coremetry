# Runbook — Coremetry MCP'yi Claude Code/Desktop'a bağlama

(v0.9.14; tasarım: docs/audit/mcp-claude-code-production-audit.md)

## 1. Token üret

Settings → API Tokens → **New token**, rol: **viewer** (11 tool'un
tamamı salt-okunur — editor/admin gerekmez). `cmk_…` değeri yalnız
oluşturma anında görünür; kasaya koy. İptal: aynı ekrandan Revoke
(anında, cache invalidation'lı).

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
| İz → log/metrik pivotları | `get_trace`, `get_logs_for_trace`, `get_metrics_for_span` |
| Metrik sorgusu / histogram ucu | `query_metric`, `get_exemplar_traces` |
| Async zincir takibi | `get_linked_traces` |
| Olay anlatımı / runbook önerisi | prompt: `explain_problem`, `suggest_runbook` |
| Deploy şüphesi / iz karşılaştırma | prompt: `deploy_impact`, `compare_traces` |

## 4. Limitler ve davranış

- **Rate limit:** kimlik (token) başına **60 tools/call/dk**; aşımda
  LLM'e JSON-RPC hatası olarak "rate limited … retry in Ns" döner —
  model bekleyip devam eder (bağlantı kopmaz, 429 yok).
  `initialize`/`tools/list`/`prompts/*` limitsiz.
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
| `/mcp` "failed to connect" (http) | URL `/api/mcp` mi (sse path'i değil)? Proxy POST gövdesini kesiyor mu? |
| SSE bağlanıyor, çağrılar "unknown session" | Çok-pod + afinite yok → Streamable-HTTP'ye geç (kalıcı çözüm) |
| Sık "rate limited" | Ajan tool-loop'ta — sorguyu daraltın; limit kimlik başına, ikinci token ayrı bütçe demektir |
