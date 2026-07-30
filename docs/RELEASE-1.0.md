# v1.0.0 Kesim Prosedürü (hazırlık: 2026-07-30, v0.9.437 itibarıyla)

Amaç: "kes" dendiği gün 1.0'ın TEK oturumda, sürprizsiz çıkması.
Durum kontrolleri bu dosya yazılırken yapıldı; kesim günü yalnız
"Kesim adımları" koşulur.

## Ön durum (2026-07-30 doğrulandı)

- **Stale v1.0.0/v1.0.1 tag'leri YOK** — lokal + `git ls-remote` temiz
  (eski hafıza notu bayattı; silme adımı gerekmez).
- **Şema:** v0.9.355 → HEAD arası tüm DDL dokunuşları boot'ta idempotent
  `ADD COLUMN IF NOT EXISTS` (problems.pod v403, problems.ai_summary_at,
  exception_groups.ai_summary v415). Migration adımı gerektirmez;
  external-Distributed prod'da spans'a dokunan ALTER yok.
- **Env:** v0.9.355'ten beri YENİ zorunlu env değişkeni yok.
- **Rollup migrations 0001-0005 OPSİYONEL** — uygulanmadıkça fast-path'ler
  kendini kapalı tutar (v412/428 probe'ları), /api/rollup/red 424 döner.
  1.0 bunlara bağımlı DEĞİL.

## Kesim adımları (sırayla, tek oturum)

1. `git fetch --tags && git ls-remote origin "refs/tags/v1.0*"` — hâlâ boş
   olduğunu doğrula (paralel oturum çakışması dersi).
2. CLAUDE.md "Versioning" satırını güncelle: `v0.9.X` → `v1.0.X`
   (v0.9 zincirinin kapandığı son sürümü ve tarihi yaz).
3. `charts/coremetry/Chart.yaml`: `version` VE `appVersion` → `1.0.0`
   (helm-chart skill kuralı: ikisi birlikte).
4. Gate zinciri: `npx tsc --noEmit` (frontend) → `go build ./...` →
   `go test ./...` → `make audit` (🔴 = kesme).
5. Commit `v1.0.0 — release` + tag `v1.0.0` AYNI && zincirinde; push +
   push --tags; `git ls-remote` ile tek-tag doğrulaması.
6. İmaj: `docker build --build-arg VERSION=v1.0.0 --build-arg
   VITE_APP_VERSION=v1.0.0 …` (build-arg'sız imaj "dev" damgalar —
   2026-07-30 dersi).
7. Chart paketle/push et (tag push CI'ı yoksa elle `helm package` +
   GHCR OCI push).

## Smoke checklist (deploy sonrası ilk 10 dakika)

- [ ] `/api/version` → buildVersion `v1.0.0`, `overridden:false`
- [ ] Boot logunda `[chstore]` ALTER satırları hatasız
- [ ] /inbox doluyor; öncelik şeridi ve tür sayaçları tutarlı
- [ ] /problems (Exceptions) tam liste; bir grup detayı + Explain root cause
- [ ] /deploys zaman çizelgesi dolu; etki rozetleri en yeni deploylarda
- [ ] /services → bir servis → Overview RED + zoom çift-tık geri
- [ ] CoSRE: "dün gece neler oldu?" + bir takip sorusu ("peki X?")
- [ ] /ai sayfası çağrıları kaydediyor (explain + auto-explain yüzeyleri)
- [ ] Collector'lar rollout sonrası metrik/trace basıyor (zero-addresses
      belirtisi yoksa restart gereksiz)

## Kesim GÜNÜ karar gerektirenler (önceden cevapla)

- v0.9 zincirinin son sürümü ne? (kesim anındaki HEAD)
- Rollup migrations prod'a uygulandı mı? (uygulanmadıysa 1.0 notlarında
  "opsiyonel, sonradan uygulanabilir" diye geçer — engel değil)
