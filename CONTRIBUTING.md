# Coremetry'ye katkı

Kısa sürüm: küçük commit, her mantıksal değişiklik kendi `v0.10.X` tag'i, kapılar
lokalde yeşil, müşteri tanımlayıcısı asla. Uzun sürüm aşağıda. Mimari kısıtların
tam listesi [CLAUDE.md](CLAUDE.md), olay hikâyeleri [docs/INCIDENTS.md](docs/INCIDENTS.md),
kararlar [docs/DECISIONS.md](docs/DECISIONS.md), hazırlık denetimi
[docs/audit/team-readiness-audit.md](docs/audit/team-readiness-audit.md).

## 1. Yerel kurulum

- Go **1.25** (go.mod), Node **22**, Docker (ClickHouse/Redis için), `helm` (chart işi için).
- Taze klonda önce frontend: `make build-ui` — Go binary'si `frontend/dist`'i
  `//go:embed` ile gömer (main.go); dist yokken `go build ./...` düşer.
- Bağımlılıklar: `make docker-up` (ClickHouse + Redis + OTel collector, compose)
  → `make build && ./coremetry --config config.yaml` → http://localhost:8088
  (demo hesabı `config.yaml` / `.env.example`).
- Frontend geliştirme: `make dev-ui` (Vite, `/api` → 8088 proxy).
- Sentetik trafik: `make build-demo` + `go run ./cmd/demo -endpoint http://localhost:14318`
  (collector) ya da `make docker-up-demo`; JBoss demosu `jboss-demo/`.
- Prod paritesi (dağıtık ClickHouse): `make minikube-up` — chart'a dokunmadan
  imaj güncellemek için `kubectl set image`, `helm upgrade` DEĞİL
  ([.claude/skills/helm-chart-coremetry](.claude/skills/helm-chart-coremetry/SKILL.md)).
- Roller: `COREMETRY_MODE=all|ingest|api|worker` (varsayılan all). Env
  değişkenlerinin tam listesi `internal/config/config.go`; `COREMETRY_JWT_SECRET`
  verilmezse her restart'ta yeni anahtar üretilir ve oturumlar düşer.

## 2. Değişiklik akışı

1. 3+ dosyaya dokunacak ya da yeni yüzey açacak işte önce plan: `/spec`
   (Claude Code) ya da `docs/audit/` altında kısa bir audit. Mevcut bir yüzeyin
   davranışını (kapsam, varsayılan, ayrım) değiştiren iş önce sorulur.
2. Kural dosyaları iş türüne göre: yeni `/api/*` ucu → [api-route](.claude/skills/api-route/SKILL.md)
   (`internal/api/api.go` BÜYÜMEZ, `registerRoutesExtra` ile kendi dosyası);
   ClickHouse tablo/MV/sorgu → [clickhouse-schema](.claude/skills/clickhouse-schema/SKILL.md);
   frontend bileşen/tablo/grafik → [frontend-conventions](.claude/skills/frontend-conventions/SKILL.md)
   ve [frontend-design-system](.claude/skills/frontend-design-system/SKILL.md)
   (önce mevcut primitifi ara); chart → helm-chart-coremetry; OTLP alanı →
   otel-conventions + otlp-converter.
3. Bug fix = regresyon testi: saf fonksiyon, tablo-testli, başlıkta düzelttiği
   `vX.Y.Z` (kanonik: `internal/api/cache_key_test.go`). Testin gerçekten
   ısırdığını bir mutasyonla göster (satırı geri al → test düşmeli).
4. Kaynak-tarama "pin" testleri (bir kuralın kodda yaşadığını dosyayı okuyarak
   çivileyen testler, ör. `internal/api/mux_routes_test.go`) bu repoda
   yaygındır; bir pin düşerse kuralı okuyup ya kodu ya pini gerekçesiyle
   güncelle — pini silmek düzeltme değildir.

## 3. Kapılar (PR şablonundaki liste)

```bash
cd frontend && npx tsc --noEmit && npx eslint src && TZ=UTC npx vitest run
go build ./... && CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...
make audit                       # 🔴 = 0; 🟡 gerekçeyle geçer
gofmt -l $(git ls-files '*.go')  # boş
go mod tidy -diff                # boş
```

CI (`.github/workflows/ci.yml`) UTC'de koşar: tarih/saat beklentilerini
makine saatinden değil `Date.UTC(...)`/sabit dilimden kur (v0.10.614).
Ağır kapılar: `make test-race` (agent/notify/sse/cache), CH duman testi
`-tags=chsmoke` (canlı ClickHouse ister), `make perfcheck`.

## 4. Commit, tag, sürüm

- Her mantıksal değişiklik **kendi sürümü**: `v0.10.X — type(scope): başlık` (≤70),
  gövde 72 sütun (ne / neden / kök neden; operatör bildirimi "Operator-reported: …"
  ile başlar). Bug fix'ler batch'lenmez.
- Tag = changelog (`git tag v0.10.X` + `git push --tags`). Tag push
  `.github/workflows/release.yml`'i tetikler: imaj `ghcr.io/cosretr/coremetry:<sürüm>`,
  chart `oci://ghcr.io/cosretr/charts/coremetry`. Chart'a dokunan iş
  `Chart.yaml` version/appVersion'ı birlikte bump'lar.
- Claude Code ile yazılan commit'lerde `Co-Authored-By: Claude … <noreply@anthropic.com>`
  trailer'ı; insan commit'lerinde gerekmez.

## 5. Dokümantasyon

- Olay hikâyesi: `docs/INCIDENTS.md` (`### vX.Y.Z — başlık` + kök neden + ders).
- Mimari karar: `docs/DECISIONS.md`.
- Denetim / plan: `docs/audit/`, `docs/plans/`; PR şablonu referans ister.
- Yorum dili: tanımlayıcılar İngilizce; yorumlar ve olay anlatıları Türkçe ya da
  İngilizce olabilir — "neden" bilgisi yorumda yaşar, silme, taşı.

## 6. Veri hijyeni (zorunlu)

Repo public. Hiçbir dosyaya, commit mesajına, teste ya da dokümana müşteri/kurum
adı, alan adı, iç hostname/IP, gerçek şema/tablo/kolon adı, gerçek iş yükü/pod
adı, LDAP DN'i, kişi adı ya da e-posta yazılmaz. Sentetik adlar kullan:
`shop`, `shop-payment-prod`, `prod-eu`, `apigateway.example.com`, TEST-NET
IP'leri (`203.0.113.x`), `SHOP.ORDER_PHONE`. Yerel manifestler ve `.env`
gitignore'lu kalır; şüphede `git diff --cached` ile bak.

## 7. Güvenlik

Açıkları issue olarak değil [SECURITY.md](SECURITY.md)'deki özel kanaldan bildir.
