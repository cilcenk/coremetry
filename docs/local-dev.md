# Yerel geliştirme rehberi

Yeni gelen için "klon → çalışan yığın → ilk değişiklik" yolu. Kurallar ve
kapılar [CONTRIBUTING.md](../CONTRIBUTING.md)'de; mimari kısıtlar
[CLAUDE.md](../CLAUDE.md)'de; env değişkenlerinin tamamı [docs/ENV.md](ENV.md)'de.
Burada tekrar edilmez, linklenir.

## 1. Önkoşullar

| Araç | Sürüm | Not |
|---|---|---|
| Go | **1.25** | `go.mod`; CI `go-version: '1.25'` |
| Node | **22** | Vite/TypeScript; CI ve `Dockerfile` `node:22-alpine` |
| Docker + compose v2 | güncel | ClickHouse / Redis / Elasticsearch / OTel collector için |
| `helm` + `minikube` + `kubectl` | opsiyonel | yalnız prod-parite (§7) |
| `golangci-lint` | opsiyonel | `make lint` (advisory) |

## 2. İlk derleme — sıra önemli

Go binary'si `frontend/dist`'i gömer (`main.go:60` `//go:embed all:frontend/dist`).
Taze klonda dizin yoktur (gitignore'lu) ve `go build ./...` düşer:
`pattern all:frontend/dist: no matching files found`.

```bash
make build-ui      # cd frontend && npm install && npm run build  → frontend/dist
make build-go      # go build -ldflags="-X main.Version=$(git describe …)" -o coremetry .
make build-demo    # go build -o demo ./cmd/demo
# üçü birden:
make build
```

`VERSION` `git describe --tags --always --dirty`'den türer (Makefile:12); login
sayfası ve `/api/version` gerçek tag'i gösterir. Binary + `demo` + `VERSION.txt`
gitignore'ludur.

## 3. Yığını compose ile ayağa kaldırmak

```bash
cp .env.example .env            # JWT secret / parola / CH parolası (opsiyonel yerelde)
make docker-up                  # çekirdek: coremetry + clickhouse + redis + elasticsearch + otel-collector
docker compose ps               # hepsi healthy/running olmalı
```

`make docker-up` imajı **yeniden derler** (`docker compose up -d --build`) ve
`./config.yaml`'ı `/app/config.yaml` olarak mount eder. Yalnız imaj üretmek
için `make image` (ikinci bir yığın başlatmaz).

| Servis | Host portu | Ne |
|---|---|---|
| coremetry | **8088** | Web UI + REST + SSE + OTLP/HTTP yedeği (`/v1/*`) |
| coremetry | 4317 / 4318 | konteyner içi; compose host'a **yayınlamaz** — collector iletir |
| otel-collector | **14317** / **14318** | OTLP gRPC / HTTP — uygulamalar ve demo buraya gönderir |
| otel-collector | 13133 | health |
| clickhouse | **9000** / 8123 | native / HTTP |
| redis | **6379** | L2 cache + lider kilidi |
| elasticsearch | 9200 | compose'da `COREMETRY_LOGS_BACKEND=elasticsearch` (index `logs-otel-default`) |
| jboss-demo · grafana · tempo · pyroscope | 8081 · 3000 · 3200 · 4040 | yalnız `--profile demo` (§5) |

Giriş: **`admin@coremetry.local` / `admin`** (`config.yaml` `auth.initial_*`,
compose env). `.env`'de `COREMETRY_INITIAL_PASSWORD` verirseniz ilk boot onu
kullanır; sonradan değiştirmek için `COREMETRY_ADMIN_RESET=1` ile bir kez boot.

Compose olmadan çıplak binary:

```bash
make build && ./coremetry --config config.yaml
# :8088 UI, :4317 OTLP/gRPC, :4318 OTLP/HTTP — CH 127.0.0.1:9000 bekler (config.yaml)
```

Redis URL'i `config.yaml`'da boş → cache yok + always-leader; yerel tek
instance için normaldir. Rol denemek için `COREMETRY_MODE=api ./coremetry`
(bkz. ENV.md §1). Dağıtık compose overlay'i: `make docker-distributed-up`
(ingest×2 + api + worker, Redis zorunlu).

## 4. Frontend geliştirme döngüsü

```bash
make dev-ui          # cd frontend && npm run dev  → http://localhost:4888
```

Vite (`frontend/vite.config.ts:32-37`) `/api` ve `/v1`'i `http://localhost:8088`'e
proxy'ler — arkada compose'daki ya da çıplak binary çalışıyor olmalı. Hot
reload burada; Go tarafı değişince `make build-go && ./coremetry` (ya da
`make docker-up`). Üretim derlemesi `npm run build` (tsc + Vite);
`npm run build:analyze` chunk haritası.

Kurallar `/frontend-conventions` ve `/frontend-design-system` skill'lerinde
(uPlot dışı grafik kütüphanesi yok, her tablo `useDataTable`, URL = state).

## 5. Demo trafiği

**Go üretici** (`cmd/demo`, retail-banking senaryoları, OTLP/HTTP):

```bash
go run ./cmd/demo -endpoint http://localhost:14318 -rps 2      # compose: collector üzerinden
go run ./cmd/demo -endpoint http://localhost:4318  -rps 2      # çıplak binary: doğrudan
go run ./cmd/demo -endpoint http://localhost:14318 -duration 10m   # süreli, temiz kapanır
```

Bayraklar: `-endpoint`, `-rps` (senaryo/s; span/s ≈ rps × ~10 × diurnal
çarpan), `-duration` (0 = sonsuz), `-profile-endpoint`. Yeni senaryo/metrik
yük modelinden (`L` / `DemoLoad`) okur — [docs/DEMO-REALISM.md](DEMO-REALISM.md).

Collector `otel-collector-config.yaml` trace'lerin **%30'unu** Coremetry'ye,
%100'ünü Tempo'ya yollar (metrik/log %100). "Demo 100 trace attı, 30 görüyorum"
normaldir; tam sayı için doğrudan `:4318`.

**Konteyner demoları** (jboss-demo WildFly + OTel javaagent, go-demo, tempo,
grafana, pyroscope):

```bash
make docker-up-demo          # docker compose --profile demo up -d --build
make demo-health             # /api/health + /api/services RED denetimi; 0 = sağlıklı
COREMETRY_URL=http://localhost:8090 make demo-health   # minikube port-forward'a karşı
make docker-down             # demo/tempo/pyroscope/grafana profilleri dahil kapatır
```

JBoss demosu kendi kendine ~2 rps üretir (`JBOSS_DEMO_RPS`); kaynağı
`jboss-demo/` (`pom.xml`, `scripts/start.sh`, `profile-pusher.sh`).

## 6. Test ve kapılar

Tam liste ve PR şablonu: [CONTRIBUTING.md §3](../CONTRIBUTING.md#3-kapılar-pr-şablonundaki-liste).
Kısa yol:

```bash
cd frontend && npx tsc --noEmit && npx eslint src && TZ=UTC npx vitest run && cd ..
go build ./... && go vet ./... && make test          # go test ./...
make audit                                           # scripts/audit.sh — 🔴 = 0
make test-race                                       # agent / notify / sse / cache
make lint                                            # golangci-lint (advisory)
```

CI (`.github/workflows/ci.yml`) **UTC'de** koşar; tarih/saat beklentisi olan
testler makine saatine değil `Date.UTC(...)` / sabit dilime dayanmalı. Yerelde
`TZ=UTC` ile çalıştırıp CI'ı taklit edin. Canlı ClickHouse isteyen duman
testi: `go test -tags=chsmoke ./internal/chstore/ -run TestCHSmokeReads`.
Perf bütçesi: `make perfcheck` (çalışan yığın ister, `scripts/perf/budget.json`).

Bug fix = saf, tablo-testli regresyon testi (başlıkta `vX.Y.Z`); yeni backend
işi `/tdd`. Kaynak-tarama "pin" testleri düşerse pini silme, kuralı oku.

## 7. ClickHouse yerel ipuçları

**Sorgu atmak** — binary'nin kendi alt komutu yapılandırılmış CH'ye bağlanır:

```bash
./coremetry ch "SELECT count() FROM spans"                 # config.yaml'daki CH
docker exec coremetry-clickhouse clickhouse-client -q "SELECT count() FROM service_summary_5m"
```

**DDL nerede yaşar:**

- Boot şeması: `internal/chstore/store.go` `migrate()` (`store.go:1854`) —
  her boot'ta idempotent koşar (`CREATE … IF NOT EXISTS`, `ALTER … IF NOT EXISTS`).
  Tablo/MV/kolon değişikliğinden **önce** `/clickhouse-schema` skill'i.
- `migrations/*.sql` (0001-0014): rollup zincirleri, entity/rollout katmanı,
  attr indeksi — **boot'ta koşmaz**, operatör uygular. Bir kısmı binary'ye
  gömülü ve Admin → ClickHouse sihirbazından koşar (`migrations/embed.go`:
  0001/0003/0008/0011/0012/0013/0014); 0002, 0004-0007, 0009, 0010 elle.
- Şema referansı [docs/SCHEMA.md](SCHEMA.md); dağıtık kurulum
  [docs/clickhouse-cluster.md](clickhouse-cluster.md).

**Şemayı sıfırlamak (hepsi DESTRUCTIVE — spans, logs, kullanıcılar, dashboard'lar gider):**

```bash
# 1) Compose: volume'u sil, en temiz yol
docker compose --profile demo down -v && make docker-up

# 2) Bayrak: drop + çık (Helm pre-install Job'un yaptığı)
./coremetry --reset-schema --config config.yaml && ./coremetry --config config.yaml

# 3) Env: drop + AYNI boot'ta yeniden kur — Deployment'ta ASLA bırakma
COREMETRY_CH_RESET_SCHEMA=1 ./coremetry --config config.yaml
```

Env yolu her restart'ta tekrar siler (`main.go:355-369`, olay v0.8.207).
`docker compose down` (`-v`'siz) veriyi korur. Yalnız şema kurup çıkmak için
`./coremetry --migrate-only`. Küme DDL'i lokal Keeper'ı zorlar — lokal
binary'yi küme CH'sine **bağlama** (memory: 2026-08-28 apiserver düşüşü).

## 8. Prod paritesi — minikube

```bash
make minikube-up      # docker build (tag=$VERSION) → minikube image load → helm upgrade --install -f values-minikube.yaml
kubectl port-forward -n coremetry svc/coremetry 8090:8088    # http://localhost:8090
make minikube-demo    # yalnız demo imajlarını yeniler (benzersiz DEMO_TAG)
make minikube-down    # helm uninstall + namespace sil
```

`values-minikube.yaml`: `mode: monolithic`, `replicaCount: 3`,
`pullPolicy: Never` (imaj side-load), paylaşımlı `jwtSecret` **şart** (yoksa
auth flap). İlk kurulumda `-f values-minikube.yaml`, sonrakilerde Makefile
`--reuse-values --set image.tag` ile yalnız tag'i oynatır.

**Kural:** chart'a dokunmayan imaj değişimi `helm upgrade` ile **değil**,
`kubectl set image` ile (`/helm-chart-coremetry`, olay v0.8.104 — helm orphan'ları):

```bash
docker build --build-arg VERSION=$TAG -t ghcr.io/cosretr/coremetry:$TAG . && minikube image load ghcr.io/cosretr/coremetry:$TAG
kubectl set image -n coremetry deploy/coremetry coremetry=ghcr.io/cosretr/coremetry:$TAG
kubectl get pod -n coremetry -o jsonpath='{.items[*].status.containerStatuses[*].imageID}'   # yeni imaj mı?
```

Aynı tag'i yeniden `image load` etmek **eski binary'yi bırakır** — her build
benzersiz tag. Rollout sonrası collector "zero addresses" ile takılırsa
`kubectl rollout restart -n coremetry deploy/coremetry-otelcol`.

## 9. Sorun giderme

| Belirti | Sebep | Çözüm |
|---|---|---|
| `go build`: `pattern all:frontend/dist: no matching files found` | UI derlenmemiş | `make build-ui` |
| `[boot] health listener on :8088: … address already in use` | compose yığını ile çıplak binary aynı port | `docker compose stop coremetry` ya da `COREMETRY_HTTP_ADDR=:8089` |
| Boot CH'ye bağlanamıyor, startup bütçesi sonunda `Fatalf` | CH ayakta değil / yanlış adres | `docker compose ps clickhouse`; `COREMETRY_CH_ADDR` (compose `clickhouse:9000`, çıplak `127.0.0.1:9000`) |
| `[cache] redis unavailable — running without cache + always-leader` | `COREMETRY_REDIS_URL` boş/ulaşılamaz | Tek instance'ta normal. Çok replika → işler çift koşar; Redis'i düzelt, `/admin/stats` `lockDegraded` |
| Her restart'ta login düşüyor | `COREMETRY_JWT_SECRET` boş → geçici anahtar (`auth.go:118`) | `.env`: `COREMETRY_JWT_SECRET=$(openssl rand -hex 32)`; çok pod'da aynı değer |
| Admin parolası unutuldu / UI'dan değiştirildi | tohum yalnız boş `users`'ta | Bir boot `COREMETRY_ADMIN_RESET=1` + `COREMETRY_INITIAL_PASSWORD=…`, sonra kaldır |
| Her restart'ta veri siliniyor | `COREMETRY_CH_RESET_SCHEMA` env'de kalmış | Env'i kaldır (`main.go:368` uyarısı) |
| `[logs] … ELASTICSEARCH` boot'ta, logs sayfası boş/CH'den | ES kapalı ama backend `elasticsearch` | Yerelde `COREMETRY_LOGS_BACKEND=clickhouse` ya da ES'i bekle (kendiliğinden toparlar) |
| Rebuild sonrası metrik akmıyor | collector eski konteyner IP'sini tutuyor | `docker restart coremetry-otel-collector` (`dns:///` çözücü notu, collector config) |
| vitest tarih testi yerelde geçiyor, CI'da düşüyor | CI UTC, makine yerel dilim | `TZ=UTC npx vitest run`; beklentiyi `Date.UTC` ile kur |
| `:4888`'de API 404/401 | backend 8088'de yok | Önce `make docker-up` ya da `./coremetry` |
| Demo 100 trace attı, 30 görünüyor | collector %30 örnekler | Normal; tam sayı için doğrudan `:4318` |
| minikube'da yeni imaj gelmiyor | aynı tag yeniden yüklendi | Benzersiz tag + pod `imageID` doğrula |
| `acquire conn timeout` + düşen batch | CH pool fan-out altında (`config.yaml` `max_open_conns` pinli) | `max_open_conns: 0` bırak (türetilir) ya da `COREMETRY_CH_MAX_OPEN_CONNS` yükselt |

## 10. Sonra ne

- İlk değişiklik: küçük, kendi `v0.10.X` tag'i, kapılar yeşil
  ([CONTRIBUTING §2-4](../CONTRIBUTING.md)).
- Yeni `/api/*` ucu → `/api-route`; CH → `/clickhouse-schema`; UI →
  `/frontend-conventions`; chart → `/helm-chart-coremetry`.
- Olay hikâyeleri [docs/INCIDENTS.md](INCIDENTS.md) — pitfall'ların çoğu
  oradan gelir; kararlar [docs/DECISIONS.md](DECISIONS.md).
- Veri hijyeni: müşteri adı / iç host / gerçek tablo adı hiçbir yere
  ([CONTRIBUTING §6](../CONTRIBUTING.md#6-veri-hijyeni-zorunlu)).
