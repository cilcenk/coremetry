# Güvenlik politikası

## Bildirim

Açıkları herkese açık issue olarak DEĞİL, GitHub **Private Vulnerability Reporting**
ile bildirin: https://github.com/cosretr/coremetry/security/advisories/new
(repo düzeyinde açık). Bildirimde: etkilenen sürüm (`/api/version`), bileşen,
yeniden üretme adımları, etki. Kurum/müşteri tanımlayıcıları maskeli.

Hedef: ilk yanıt 3 iş günü; critical → düzeltme aynı gün ayrı `v0.10.X` sürümü,
high → bir hafta. Düzeltme yayınlanana kadar ayrıntı paylaşılmaz.

## Desteklenen sürüm

Yalnız en yeni `v0.10.X` tag'i (her değişiklik kendi sürümü; sürüm zinciri
tek dal). Eski tag'lere geri-yama yok — yükseltin.

## Kapsam

- OTLP ingest (`internal/otlp`, gRPC 4317 / HTTP `/v1/*`)
- Kimlik ve yetki: JWT, LDAP/AD, OIDC, roller admin/editor/viewer
  (`internal/auth`, `internal/ldap`)
- Admin yüzeyleri: Settings, `/admin/sql`, ClickHouse yönetim uçları
- MCP sunucusu (`/api/mcp/*`, `internal/mcptools`) ve dış MCP istemcisi
  (`internal/mcpclient`)
- AI sağlayıcı sırları (`internal/copilot`, `internal/secretref` — env/file
  referansları; sırlar API cevabına ve loga girmez)
- Helm chart ve imajlar (`ghcr.io/cosretr/*`)

Kapsam dışı: demo üreteçleri (`cmd/demo`, `jboss-demo`), örnek manifestler.

## Otomatik tarama

CI'da `govulncheck` (erişilebilir Go CVE'leri), Trivy fs (HIGH/CRITICAL),
`npm audit` (high/critical), CodeQL (haftalık), GitHub secret scanning +
push protection, Dependabot alerts + security updates. Bilinçli kabul edilen
bulgular gerekçesiyle `.trivyignore` ve `.auditignore` dosyalarında.
