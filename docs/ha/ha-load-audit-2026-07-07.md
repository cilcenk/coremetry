# HA / Yüksek-Yük Hazırlık Audit'i — 2026-07-07

Operatör direktifi: "mevcut feature'ların performanslı iyi çalışması, bugların
giderilmesi ve yüksek yük altında HA çalışması çok önemli." Dört eksenli
paralel tarama (multi-pod doğruluk / ingest yükü / bağımlılık kesintisi /
rollout duruşu); en kritik 4 bulgu el ile kod üstünde doğrulandı. Eksenler
arası mükerrer bulgular tekilleştirildi. Rapor = bulgu + fix yönü; kod yok.

## 🔴 Kritikler (tekilleştirilmiş, en-kötü-önce)

### H1 — Her deploy'da veri kaybı: shutdown sırası + drain yarışı + durdurulmayan consumer'lar
- `main.go:858-866`: SIGTERM'de YALNIZ spans/logs/metrics consumer'ları
  durduruluyor — **exemplar + span_links consumer'ları hiç Stop() edilmiyor**;
  gRPC sunucusuna GracefulStop, HTTP'ye Shutdown YOK → SIGTERM sonrası gelen
  Export'lar ölü kanala 200-OK'lanıyor (collector kopyasını siliyor = kayıp).
  Abrupt gRPC kesişi otelcol zero-addresses wedge'inin sunucu-taraflı
  tetikleyicisi.
- `internal/consumer/consumer.go:109-112` (drain yolunda dispatch):
  `select { flushQ <- b | <-ctx.Done() }` — drain'de ctx ZATEN iptal →
  Go select'i rastgele seçer → **son batch'ler her deploy'da ~%50 ihtimalle
  sayaçsız çöpe gidiyor** (flushQ doluysa %100).
- Fix: preStop + sıralama (GracefulStop+Shutdown → consumer ctx cancel →
  5 consumer'ın hepsi Stop) + drain'de düz bloklu send.

### H2 — CH kesintisinde tel-hızında kayıp: flush retry'sız discard
- `consumer.go:153-156`: flush hatası = batch discard + log. CH 30 sn
  düşse/fail-fast etse, 8 worker tüm akışı writeFailed'e boşaltıyor — 500k'lık
  buffer hiç devreye girmiyor. + `store.go` DialTimeout var ama **ReadTimeout
  yok** (driver default 300 sn): CH askıda kalırsa flusher'lar 5'er dk kilitli.
- Fix: flusher'da sınırlı retry+backoff (batch slotta kalır → doğal
  backpressure); ReadTimeout ~30 sn.

### H3 — Liveness/readiness yanlış bağlanmış: overload = fleet kill; CH-down = yeşil health
- `deployment.yaml:178`: liveness aynı `/api/health`'e bağlı; kuyruk ≥%90 →
  503 → kubelet ~60 sn'de pod'u ÖLDÜRÜYOR (buffer'daki her şey gider,
  crash-loop, otelcol wedge). startupProbe yok (CH yavaş boot = kill loop).
- `api.go:8716`: health yalnız kuyruk atomics'i okuyor — CH tamamen düşmüşken
  (fail-fast discard, kuyruk boş) **yeşil** kalıyor; readiness CH'yi hiç
  bilmiyor; exemplar/span_links sayaçları health'te yok.
- Fix: `/livez` (süreç canlı = 200) + startupProbe; readiness'e ucuz CH ping
  (5 sn cache) + write-failed deltası; 5 sinyalin tamamının sayaçları.

### H4 — Split-brain çift alarm: leader demote yok + Redis boot'ta kalıcı Noop
- `internal/cache/leader.go:153`: refresh hatasında sonsuza dek held=true —
  lease Redis'te düşer, ikinci pod lider olur → **iki evaluator, çift Problem
  satırı (newID rastgele → dedup yok), çift e-posta/Slack/PagerDuty**.
- `main.go:420-425` + `internal/cache/redis.go:19-33`: boot'ta tek 3 sn ping;
  başarısızsa pod ömrü boyunca Noop cache + **always-leader** — Redis dönse de
  yeniden bağlanmıyor. Kısa Redis blip'i + rolling restart = N pod'un hepsi
  "lider".
- Fix: son başarılı refresh > lease TTL ise demote + acquire döngüsüne dön;
  arkaplanda Redis re-probe + hot-swap (veya Redis configliyken readiness'i
  Redis'e bağla).

### H5 — OTLP backpressure dürüstlüğü: retry-çiftlemesi + sessiz kayıp
- `grpc.go:78`: kısmî kabul sonrası ResourceExhausted → SDK TÜM batch'i
  retry'lar → kabul edilen 15k span MergeTree'ye İKİNCİ kez yazılır (MV'lerde
  çift sayım = yanlış error-rate/alarm) + yük amplifikasyonu.
- `http.go:233`: drop'ta bile boş 200 → collector kopyayı siler, hız kesmez —
  süresiz sessiz kayıp. `grpc.go:92+`: logs/metrics/exemplars/links Add()
  sonuçları çöpe atılıyor — %100 drop'ta bile OK.
- Fix: PartialSuccess (rejected_*) + tam-red'de 429/Retry-After (OTLP spec);
  tüm sinyallerde Add() sonucuna göre yanıt.

### H6 — api-role pod'ları ingest route'ları açık: distributed'da veri karadeliği
- `api.go:337-340`: POST /v1/traces|logs|metrics her rolde kayıtlı; consumer
  yalnız ingest'te başlıyor. main.go'daki "role guard internally" yorumu
  gerçek değil (internal/api'de COREMETRY_MODE referansı sıfır). Yanlış
  hedeflenmiş collector → 200-OK'lanan ölü kanal.
- Fix: runMode'u api.NewServer'a geçir; rol dışı ingest route'ları 501.

### H7 — Distributed default'ları auth-flap'li: jwtSecret boş + replicas=2
- `values.yaml:214` boş jwtSecret + distributed api.replicas=2 → pod-başına
  ephemeral anahtar → login flap (bilinen memory; chart'ta fail-guard yok).
- Fix: Helm `fail` (replicas>1/autoscaling/mode=distributed ∧ secret boş).

### H8 — ES boot'ta Fatalf: opsiyonel read-backend, tüm binary'yi düşürüyor
- `main.go:642`: backend=elasticsearch + ES down/yavaş boot = her rolde
  log.Fatalf. ES kesintisi sırasında herhangi bir pod restart'ı = tüm APM down.
- Fix: degraded boot (Switchable ile CH'ye düş) + 30 sn config-refresh
  döngüsünde ES'i yeniden dene.

### H9 — Wildcard LogQuery kuralı: tick başına N özdeş ES sorgusu
- `evaluator.go:313/448`: service="" kuralı her servise expand ediliyor ama
  LogQuery dalı servisi YOK SAYIYOR → 1000 servis = tick başına 1000 özdeş ES
  araması; ES brownout'unda 30 sn'lik timeout'lar SIRAYLA yanıp TÜM alerting'i
  durduruyor.
- Fix: LogQuery kurallarını kural-başına BİR kez değerlendir + tick başına
  logstore zaman bütçesi.

## 🟡 Sarılar (fix sırasına aday)
1. Buffer'lar adet-sınırlı, byte-sınırsız — şişman loglarla OOMKill senaryosu
   (byte bütçesi veya sinyal-başına bellek türevi boyut).
2. breachSince/lastResolved pod-belleğinde — her failover/deploy'da sustain
   saati sıfırlanıyor / cooldown deliniyor (Redis'e taşı).
3. PDB yok — node drain maxUnavailable:0'ı bypass edip ingest endpoint setini
   boşaltabiliyor (wedge); distributed deployment'ta rollingUpdate stratejisi
   hiç yok (default %25!).
4. warmDependenciesCache her pod'da — N× ağır GROUP BY (leader-gate/SETNX).
5. Redis client'ında timeout/breaker yok — blackhole Redis'te her API miss'i
   10-20 sn askıda (DialTimeout ~500ms, MaxRetries 0, async Set).
6. ES transport'unda dial/response timeout yok; ErrBackendSlow degrade'i yalnız
   trace yolunda — /logs arama/histogram/fieldstats/context 5xx'liyor.
7. CH boot Fatalf — bounded retry + "starting" 503 health listener.
8. Exemplar/span_links QueueFull/WriteFailed sayaçları health/stats'ta yok.
9. Ack/assign/resolve mutasyonları problems:* cache'ini invalidate etmiyor —
   pod'lar arası ~15 sn read-your-writes ihlali (kozmetik ama triage yüzeyi).

## Önerilen fix sırası (her biri kendi v0.8.X'i, regression testli)
1. H1 shutdown+drain (her deploy'daki kaybı durdurur) — consumer testi yazılabilir
2. H3 probe ayrımı + health dürüstlüğü (fleet-kill'i durdurur)
3. H2 flush retry+ReadTimeout (CH kesinti dayanımı)
4. H4 leader demote + Redis reconnect (çift alarm)
5. H5 OTLP PartialSuccess/429 (çift sayım + sessiz kayıp)
6. H9 LogQuery tekilleştirme (ES yükü + alerting stall)
7. H6 role guard · 8. H8 ES degraded boot · 9. H7 helm fail-guard
10+ 🟡 batch sırayla.

Temiz çıkanlar: auth Redis'siz stateless ✓, correlator RLock'lu ✓, notifier
kanal-izolasyonlu ✓, SSE Redis köprüsü affinity-bağımsız ✓, browser SSE ✓.
