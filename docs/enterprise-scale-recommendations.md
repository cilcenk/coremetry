# Coremetry — Enterprise-Scale Güçlendirme Tavsiyeleri

**Tarih:** 2026-06-07 · **Kapsam:** statik kod + Helm + CI analizi (4 eksen: IAM, HA/ölçek/güvenilirlik, veri-ölçek/maliyet, güvenlik/uyumluluk/ops) · **Hedef profil:** bankalar, regüle ortamlar, milyar-span/gün.

> Bu rapor *gerçekçi* olmaya çalışır: tek-binary ve "operatör tercihi" gibi bilinçli tasarım kararlarına saygı gösterir, her öneriyi mevcut koddan kanıtla bağlar, etki/efor verir. Bazı "boşluklar" aslında bilinçli kararlardır (single-tenant, redaction yok) — bunları "yeniden değerlendir" olarak işaretledim, "hata" olarak değil.

---

## Yönetici özeti

Coremetry mimari olarak **ölçek-dışına-açılma (stateless ingest/api pod'ları) ve performans (async batch, MV-öncelikli okuma, 3-katmanlı cache)** için iyi tasarlanmış. Gerçek zayıflık **dayanıklılık ve uyumluluk** tarafında: ingest kuyruğu yalnızca bellekte (pod restart = veri kaybı), worker durumu kalıcı değil (alert flap riski), sırlar ClickHouse'ta düz metin, ve okuma/erişim denetimi (audit) yok. Bir bankanın **birincil** APM'i olmak için önce bu "veri-bütünlüğü + uyumluluk" katmanı kapatılmalı; SaaS çok-kiracılılık ise ayrı, stratejik bir karar.

**En kritik 5 (bu sırayla):**
1. Dayanıklı ingest (disk-WAL / kalıcı kuyruk) — pod restart'ta span kaybını bitir.
2. Sırların at-rest şifrelenmesi (system_settings düz metin → envelope encryption).
3. Worker leader-election sağlamlaştırma + evaluator durumunu kalıcılaştır (split-brain + alert flap).
4. Erişim/okuma denetimi (kim hangi trace/log'u sorguladı) + değiştirilemez audit + retention hold.
5. Cardinality guardrail'leri (ingest-zamanı allow/deny + limit + alarm), şu an yalnızca salt-okunur raporlama var.

---

## Önceliklendirme matrisi

| # | Öneri | Eksen | Etki | Efor | Öncelik |
|---|---|---|---|---|---|
| 1 | Dayanıklı ingest kuyruğu (disk WAL / opsiyonel Kafka) | Güvenilirlik | Çok yüksek | Yüksek | P0 |
| 2 | Sır şifreleme (envelope/KMS) | Güvenlik | Çok yüksek | Orta | P0 |
| 3 | Leader-election fencing + evaluator state kalıcılığı | Güvenilirlik | Yüksek | Orta | P0 |
| 4 | Erişim audit + değiştirilemez/zincirli audit + hold | Uyumluluk | Yüksek | Orta | P0 |
| 5 | Cardinality guardrail (allow/deny + limit + alarm) | Veri-ölçek | Yüksek | Orta | P1 |
| 6 | Opt-in PII redaction/masking pipeline kuralı *(standing no-redaction kararıyla çelişir — bkz. §6 notu)* | Uyumluluk | Yüksek | Orta | P1 |
| 7 | Backpressure + kaynak-bazlı ingest rate-limit | Güvenilirlik | Orta | Orta | P1 |
| 8 | Servis hesapları / API anahtarları (M2M) | IAM | Orta | Orta | P1 |
| 9 | Sorgu maliyet yönetimi (eşzamanlılık cap + async tier + chart cache) | Veri-ölçek | Orta | Orta | P1 |
| 10 | NetworkPolicy + CH pod hardening + CI hard-gate + imza/SBOM | Güvenlik/Ops | Orta | Düşük-Orta | P1 |
| 11 | Collector-side tail sampling politika rehberi (örnek config + Helm values) | Veri-ölçek | Orta | Düşük | P2 |
| 12 | MFA/TOTP + parola politikası + oturum iptali | IAM | Orta | Orta | P2 |
| 13 | GDPR sil (RTBF) API'si | Uyumluluk | Orta | Orta | P2 |
| 14 | Tiered storage + downsampling rollup (uzun retention) | Maliyet | Orta | Yüksek | P2 |
| 15 | Çok-kiracılılık (multi-tenancy) | Mimari | Stratejik | Çok yüksek | P3 |
| 16 | SAML, ABAC/satır-seviyesi yetki | IAM | Orta | Yüksek | P3 |

---

## P0 — Veri bütünlüğü ve uyumluluk tabanı (bunlar olmadan "birincil APM" olmaz)

### 1. Dayanıklı ingest kuyruğu
**Mevcut:** OTLP kabul edildikten sonra span'ler `make(chan T, BufferSize)` ile **yalnızca bellekte** tutuluyor; batch ClickHouse'a yazılmadan pod çökerse o pencere (≈1–5 sn) kayboluyor. `wait_for_async_insert=1` bile CH-tarafı buffer'a yazıldığında döner, diske flush'ı garanti etmez. Kuyruk dolunca `Add()` `false` döner ve `dropped` sayacı artar — ama **pasif alarm yok**. (`internal/consumer/consumer.go`, `internal/chstore/repo.go`, `internal/otlp/grpc.go`)

**Tavsiye (gerçekçi, tek-binary'yi koruyarak):**
- Kabul ile CH-flush arasına **disk-destekli WAL** koy (ör. pod'un `emptyDir`/PVC'sinde segment dosyaları; flush başarınca sil). Restart'ta WAL'dan replay. Tek-binary felsefesini bozmaz.
- Bankalar için **opsiyonel Kafka/Redpanda** ingest tamponu (env ile aç/kapa) — "tek imaj" pitch'i korunur, kuyruk dışarı taşınır.
- `dropped` sayacı artınca **kendi alert kuralını otomatik kur** (self-observability zaten var; sadece alarm bağla).
- CH yazımında kritik sinyaller için `wait_for_async_insert=1` + periyodik `SYSTEM FLUSH` ya da senkron yol opsiyonu.

### 2. Sırların at-rest şifrelenmesi
**Mevcut:** LDAP bind parolası, SMTP kimlikleri, AI API anahtarları `system_settings` tablosunda **düz metin**; CH'ye `SELECT` yetkisi olan herkes okuyabilir. Kod yorumunda dahi "plaintext — explicit deployment-time decision" deniyor. (`internal/chstore/settings.go`, `internal/ldap/ldap.go`)

**Tavsiye:**
- **Envelope encryption**: operatör tarafından sağlanan bir ana anahtar (`COREMETRY_SECRET_KEY` / KMS / Vault) ile AES-256-GCM; `system_settings`'e yalnızca şifreli blob yaz. Mevcut `LoadPersisted/SavePersisted` deseni tek noktadan sarmalanabilir → küçük yüzey.
- Anahtar yoksa eski davranışa düş (geriye uyumlu), ama log'da uyar.
- UI'da "stored / rotate by pasting new" desenini netleştir (kısmen var).

### 3. Worker leader-election sağlamlaştırma + durum kalıcılığı
**Mevcut:** Redis SETNX tabanlı `LeaderHolder` iyi; ama (a) Redis erişilemezse Noop kilit "herkes lider" yapıyor → evaluator/topology **çift çalışır**; (b) uzun süren işte iş ortasında `IsLeader()` yeniden kontrol edilmiyor (**fencing yok**); (c) evaluator'ın `breachSince/lastResolved` map'leri **yalnızca bellekte** → pod restart'ta alarm flap'ı / yanlış re-fire. (`cache/leader.go`, `internal/evaluator/evaluator.go`)

**Tavsiye:**
- Redis erişilemezse Noop-lider yerine **fail-closed** (worker işini durdur, alarm üret) — sessiz çift-çalışmadan iyidir.
- Uzun işlerde periyodik `IsLeader()` / **fencing token** kontrolü; lider kaybında işi iptal et.
- Evaluator breach/cooldown durumunu **ClickHouse'a (ReplacingMergeTree) kalıcılaştır** → restart'ta süreklilik, flap yok.
- Helm/dokümanda Redis için **AOF/persistence + replica** zorunluluğunu netleştir (kod bunu varsayıyor ama dayatmıyor).

### 4. Erişim denetimi + değiştirilemez audit + retention hold
**Mevcut:** Audit yalnızca **mutasyonları** kaydediyor (69 aksiyon, iyi). Ama **okuma/sorgu denetimi yok** ("kim hangi trace/log'u görüntüledi"), başarısız login'ler audit'e yazılmıyor, audit `ReplacingMergeTree(version)` olduğu için **soft-delete edilebilir** (kriptografik değişmezlik yok), ve audit kayıtları diğer sinyallerle aynı TTL'e tabi — **regülatör "hold"** yok. (`internal/chstore/audit.go`, `internal/api/anomaly_extra.go`)

**Tavsiye (PCI-DSS 10.2 / SOX / HIPAA):**
- Hassas okuma uçlarına (traces/logs/spans sorguları) **erişim audit** ekle (kullanıcı, zaman, sorgu parametreleri) — örnekleme ile hacmi yönet.
- Başarısız kimlik doğrulama → audit.
- Audit'i **append-only + hash zinciri** (her satır öncekinin hash'ini taşır) ile tamper-evident yap.
- Audit için ayrı, **uzun ve override edilemez retention** + "compliance hold" bayrağı.

---

## P1 — Ölçek güvenliği ve güvenlik sertleştirme

### 5. Cardinality guardrail'leri
**Mevcut:** Şema baştan sona `LowCardinality` + dizi-tabanlı attribute'lar (iyi), `/api/admin/cardinality` ile top-30 raporu var — ama **ingest-zamanı koruma yok**: allow/deny listesi, attribute-başına tavan, ya da eşik aşılınca alarm yok. Tek bir servis sınırsız distinct değer (user-id, request-id) basabilir. (`internal/chstore/cardinality.go`, `internal/pipeline/`, `internal/otlp/convert.go`)

**Tavsiye:**
- `internal/pipeline`'a **"drop/hashla if cardinality > X"** kural tipi ekle (allow/deny + attribute maskleme).
- Arka planda **cardinality patlaması dedektörü** → otomatik anomali/alert (UI zaten ver/i gösteriyor; alarmı bağla).
- Servis-başına yumuşak kota + operatöre görünür "en pahalı attribute" paneli (kısmen var).

### 6. Opt-in PII redaction/masking
> ⚠️ **Bu madde, kalıcı operatör kararıyla çelişir** (CLAUDE.md hard
> constraint: "No PII redaction features — full fidelity > theoretical
> safety"). Operatör o kararı açıkça geri almadan UYGULANMAZ; burada
> yalnızca denetim (PCI/GDPR/HIPAA) bağlamı için kayıt altındadır.

**Mevcut:** Bilinçli olarak **hiç redaction yok** (CLAUDE.md). Tek-tenant tek-banka için savunulabilir; ama PCI/GDPR/HIPAA için çoğu denetimde blokör. (`CLAUDE.md` satır 27)

**Tavsiye (ancak karar geri alınırsa):** Varsayılan tam-sadakat kalsın; regüle müşteriler için **opt-in `internal/pipeline` redaction kuralı** (regex/semconv-anahtar bazlı: PAN, IBAN, SSN, e-posta) sun. "Operatör isterse açar" — ama önce yukarıdaki standing karar revize edilmeli.

### 7. Backpressure + kaynak-bazlı rate-limit
**Mevcut:** `/api/health` ≥%90 kuyrukta 503 veriyor (readiness sinyali), ama OTLP sıcak yolunda istemciye "yavaşla" sinyali **batch buffer'a alındıktan sonra** (geç) geliyor; kaynak/servis-başına ingest rate-limit veya öncelik kuyruğu (hata span'leri öncelikli) yok. (`internal/api/api.go` health, `internal/otlp/grpc.go`)

**Tavsiye:** Kaynak/servis-başına ingest rate-limit + öncelik (error/root span'leri koru), ve aşımda erken `ResourceExhausted` ile SDK retry/backoff'u tetikle.

### 8. Servis hesapları / API anahtarları (M2M)
**Mevcut:** Yalnızca insan login → JWT (24s TTL, refresh yok). Otomasyon ya insan JWT'si çalmak ya da trusted-proxy header kullanmak zorunda; uzun ömürlü/kapsamlı **API anahtarı yok**. (`internal/auth/auth.go`, `internal/api/api.go` login)

**Tavsiye:** Kapsamlı, iptal edilebilir **API anahtarı / servis hesabı** tipi (rol + opsiyonel IP allowlist + son-kullanım izleme), audit'e bağlı. MCP server ve CI/cron tüketicileri bunu kullanır.

### 9. Sorgu maliyet yönetimi
**Mevcut:** MV-öncelikli okuma + `max_execution_time` + 4GB bellek tavanı + singleflight/L1/L2 cache (iyi). Ama: kullanıcı-başına **eşzamanlılık limiti yok**, **async ağır-sorgu tier'ı yok**, ve `/api/spans/metric` chart ucu **cache'siz** (perf dokümanında "kritik" işaretli), alt-5dk granülerlik ham span'e düşüyor. (`internal/chstore/spanmetric.go`, `internal/api/api.go`, `docs/perf/phase1-baseline.md`)

**Tavsiye:** Kullanıcı/uç-başına eşzamanlılık cap'i; pahalı sorgular için **async "çalıştır-sonra-poll"** tier; chart ucunu `serveCached`'e al; negatif cache + stale-while-revalidate.

### 10. Deployment/güvenlik sertleştirme (düşük efor, yüksek getiri)
**Mevcut:** Coremetry pod securityContext sağlam (nonroot, readOnlyRoot, cap drop — iyi). Ama: **NetworkPolicy yok**, bundled ClickHouse securityContext `{}` (vanilla k8s'te kırılır), CI güvenlik kapıları (govulncheck/Trivy/npm audit) **advisory** (merge'i bloklamıyor), **imza/SBOM yok**. (`charts/coremetry/templates/*.yaml`, `.github/workflows/ci.yml`, `Dockerfile`)

**Tavsiye:** Helm'e deny-all + explicit-egress **NetworkPolicy** şablonları; bundled CH'yi Coremetry ile aynı sertlikte çalıştır; CI kapılarını **hard-block** yap; release'e **cosign imza + CycloneDX/SPDX SBOM** ekle.

---

## P2 — Olgunlaşma

- **11. Sampling (collector-side):** In-binary head/tail sampling v0.8.73'te kaldırıldı — Coremetry %100 saklar, sampling **collector katmanında** yapılır; geri getirme. Yapılacak iş: OTel collector `tailsampling` processor için **dokümante örnek politika** (error+slow %100 koru, per-operation oran) + Helm values şablonu gemiye ekle.
- **12. IAM güçlendirme:** TOTP **MFA**, parola politikası (uzunluk/karmaşıklık/rotasyon), kullanıcı pasifleştirince **anında oturum iptali** (şu an JWT süresi dolana dek geçerli).
- **13. GDPR RTBF:** bir özne/kullanıcının trace/log/ai_calls verisini **talep üzerine silen** API (audit'li). Şu an yalnızca TTL var.
- **14. Maliyet/uzun retention:** sıcak/ılık/soğuk **tiered storage** + 1s/1m'nin ötesinde **downsampling rollup** (30g+ pencerede 1h/1d). Mimari dokümanda öneri var, uygulanmamış. (`docs/architecture/scale-billions.md`)

---

## P3 — Stratejik / büyük yatırım

- **15. Çok-kiracılılık:** Bugün **tek-kiracı** (hiçbir tabloda tenant_id; tüm sorgular deployment kapsamı). SaaS hedefi yoksa **dokunma**; varsa bu, tüm sorgulara tenant scoping + satır-seviyesi güvenlik + kiracı-başına anahtar/kota gerektiren **en büyük iş**. Tek-banka-tek-deployment modeli için gereksiz.
- **16. Federasyon & ileri yetki:** SAML (kurumsal SSO mandası varsa), **ABAC/satır-seviyesi yetki** (ör. "fraud-team yalnızca payment-service"), cross-cluster DR replikasyonu.

---

## Güçlü yanlar (korunmalı)

- Stateless ingest/api + `COREMETRY_MODE` ile temiz ölçek-dışı; ClickHouse Distributed + sharding + replikasyon + wrapper auto-repair.
- MV-öncelikli okuma invariant'ı + her sorguda `max_execution_time`/bellek tavanı + 3-katman cache + singleflight.
- LDAP (nested group + role mapping), OIDC (PKCE + domain allowlist), trusted-proxy (CIDR sınırlı) — sağlam AuthN tabanı.
- Mutasyon audit'i geniş (69 aksiyon, CSV export), online & idempotent CH migrasyonları, retention enforce (v0.6.36 regresyon testli).
- Sağlam container hardening (OpenShift restricted-v2), air-gap (`global.imageRegistry`), Dependabot + tarama (advisory de olsa mevcut).

---

## Önerilen yol haritası (gerçekçi sıralama)

1. **Çeyrek 1 — "Veri kaybetme, sır sızdırma":** P0/1 dayanıklı ingest, P0/2 sır şifreleme, P0/3 leader+state, P0/4 audit. Drop/queue alarmlarını bağla.
2. **Çeyrek 2 — "Ölçekte güvenli + denetimden geç":** P1/5 cardinality guardrail, P1/6 opt-in redaction *(yalnızca standing karar geri alınırsa)*, P1/10 NetworkPolicy+CI hard-gate+imza/SBOM, P1/8 API anahtarları.
3. **Çeyrek 3 — "Maliyet + sorgu dayanıklılığı":** P1/7 backpressure, P1/9 sorgu maliyet yönetimi, P2/11 collector-side sampling rehberi, P2/14 tiered/rollup.
4. **Çeyrek 4+ — "Olgunluk/stratejik":** P2/12-13 MFA+RTBF, P3 (yalnızca SaaS/mandate gerekiyorsa).

> Her madde, mevcut desenlere (pipeline kuralları, system_settings `LoadPersisted/SavePersisted`, `serveCached`, leader lock, MV invariant) **eklemeli** olarak oturur — tek-binary felsefesini bozmadan.
