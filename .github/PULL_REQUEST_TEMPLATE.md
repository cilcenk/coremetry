<!-- Başlık: `v0.10.X — type(scope): kısa başlık` (≤70). Gövde 72 sütun. -->

## Ne / neden

<!-- Kök neden, ne değişti, neden böyle. Operatör bildirimiyse gövde
     "Operator-reported: …" ile başlar; kendi sürümü, asla batch. -->

## Sınıf

- [ ] operator-reported (bug)
- [ ] feat / fix / chore / docs / test / ci

## Audit dokümanı / `docs/audit/` referansı

<!-- docs/audit/<dosya>.md#bölüm ya da docs/audits/YYYY-MM-DD-*.md.
     3+ dosyaya dokunan işte /spec çıktısı ya da audit ZORUNLU; yoksa "yok". -->

## Kapılar (hepsi lokalde koşuldu)

- [ ] `cd frontend && npx tsc --noEmit`
- [ ] `cd frontend && npx eslint src` — 0 error
- [ ] `cd frontend && TZ=UTC npx vitest run` — CI UTC'de koşar (v0.10.614 dersi)
- [ ] `go build ./... && CGO_ENABLED=0 go build ./... && go vet ./...`
- [ ] `go test ./...` (+ `-race` agent/notify/sse/cache dokunulduysa)
- [ ] `make audit` — 🔴 = 0 (🟡 varsa gerekçe aşağıda)
- [ ] `gofmt -l $(git ls-files '*.go')` boş · `go mod tidy -diff` boş
- [ ] Bug fix ise regresyon testi var ve başlığı **vX.Y.Z**'yi anıyor (kanonik: `internal/api/cache_key_test.go`); mutasyon ölçüldü
- [ ] ClickHouse şeması / SQL değiştiyse `/clickhouse-schema` okundu; migration + `chsmoke` güncel
- [ ] `charts/coremetry/` değiştiyse `Chart.yaml` version/appVersion bump + `helm lint` + `helm template` (mono + distributed)
- [ ] OTel alan adı değiştiyse `/otel-conventions` + `/otlp-converter` golden payload
- [ ] Yeni `/api/*` route → kendi `internal/api/<domain>.go` (`registerRoutesExtra`), `api.go` büyümedi
- [ ] Müşteri/kurum adı, hostname, IP, şema/tablo adı, gerçek iş yükü adı YOK (sentetik: shop, prod-eu, TEST-NET)

## Etkilenen alan (CODEOWNERS)

<!-- frontend / chart-layer / otlp / storage / metrics / cosre-ai / anomaly-alerts / integrations / security / helm-ops / perf-demo / docs / core -->

## Doğrulama (canlı)

<!-- curl / ekran görüntüsü / CH sorgusu — hostname'ler maskeli. -->
