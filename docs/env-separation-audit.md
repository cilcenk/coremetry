# Ortam (int/uat/prep) Ayrıştırma — deploy_env Audit (Stage 1)

2026-07-08. Operatör isteği: "service ismi mobile-bff gözükse de testte 3 farklı
ortamı int, uat, prep olarak ayrıştırabilecek bir özellik isterim." Kod
değişikliği YOK — Stage 2 bu dokümanın onayıyla başlar. Şablon: CLUSTER
filtresi mimarisi (v0.5.372+). Zemin: v0.8.379 ingest fix'i sonrası
`spans.deploy_env` doluyor (lokal 24h: 2.60M/2.83M span = %92).

## Yönetici özeti

deploy_env temeli spans tarafında SAĞLAM: LowCardinality kolon
(store.go:496), filterexpr map'i (filterexpr.go:37-39), Explore facet'i
(facets.go:42), Traces group-by (repo.go:2364, Traces.tsx:76). Eksik olan
YÜZEY: hiçbir handler ?env= almıyor, hiçbir sayfada env picker yok
(cluster'ın 4 sayfada var). Kritik MV sorusunun cevabı cluster emsalinde
gizli: **cluster hiçbir MV'ye boyut olarak EKLENMEDİ** — filtre set edilince
bounded raw-spans yoluna düşülüyor (api.go:1259 `useMV := … && cluster == ""`).
Env aynısını daha ucuza yapar (typed LC kolon vs 6-anahtarlı indexOf derive).
Öneri: **hibrit (c)** — cluster deseni + global Topbar env picker; MV boyutu
ölçülü/ertelenmiş dilim. Audit sırasında 2 bug bulundu (stability direktifi:
önce onlar): topology env derive'ı yeni semconv anahtarını kaçırıyor
(v0.8.379 sınıfı) ve metric_points'te bare `deployment.environment` filtresi
missing-column hatası üretiyor.

## 1. ENVANTER — cluster'ın olup env'in olmadığı yüzeyler

| Yüzey | Cluster bugün | Env bugün | Boşluk |
|---|---|---|---|
| Services | picker + ?cluster= (Services.tsx:98-102,418-425; api.go:1245) | YOK | picker + param + raw-path dalı (repo.go:616 emsali) |
| Endpoints | picker + ?cluster= (Endpoints.tsx:98-101,276-281; endpoints.go:552-556) | YOK (drawer attr listesinde pasif, DetailDrawer.tsx:432) | aynı dal (getEndpointsRaw zaten cluster için tutuluyor, endpoints.go:491-498) |
| Traces | YOK | group-by deploy_env VAR (Traces.tsx:76; repo.go:2364); filtre yalnız DSL'den | ?env= → FilterExpr enjeksiyonu (sıfır şema) |
| Explore (traces) | YOK | facet VAR (facets.go:42; Explore.tsx:641-644 FacetsPanel) | picker'a bağlama |
| Topology/ServiceMap | client-side pick (ServiceMap.tsx:116,306-310) | parent_env/child_env KOLONLARI VAR (store.go:1276-1277; topology.go:482-483) ama UI hiç render etmiyor (types.ts:64 `env?` ölü) + ⚠ aynı-isim-3-env node'ları TEK node'a çöküyor (ORDER BY'da env yok, store.go:1281; any() pick topology.go:1057-1058) | env chip + filtre; strict ayrışma = edge kimliği rebuild (ayrı dilim) |
| Problems/Inbox | ClusterChips (display-only; AnomaliesPage.tsx:897, streams.tsx:464) | YOK | env enrichment (GetServiceClusterMap emsali, repo.go:352) |
| Logs | ?cluster= + res-attr derive (Logs.tsx:242,575-581; logstore/clickhouse.go:121-132) | YOK — CH logs tablosunda deploy_env kolonu yok (store.go:526-546), ES Filter'da Env alanı yok (logstore.go:35-82), log ingest env çıkarmıyor (convert.go:157-197) | cluster'la birebir aynı res-attr derive deseni |
| Metrics/Explore | YOK | metric_points'te env kolonu YOK (store.go:1110-1145); bare `deployment.environment` filtresi → deploy_env kolonuna çözülüyor → **CH missing-column HATASI** (filterexpr.go:37 + metricquery.go:51; frontend bu anahtarı sunuyor: metricQuery.ts:27). Span-türevi resolver'da env filtresi rollup'tan düşüp raw-spans'e iner (metricresolve.go:150-160) — orada ÇALIŞIR | önce bug fix; sonra `resource.` yoluyla (filterexpr.go:133-136) |
| Alert rules | YOK | YOK — AlertRule'da filtre/env alanı hiç yok (problem.go:16-61; DDL store.go:802-821); evaluator yalnız service_name (evaluator.go:466-470) | ayrı spec (şema + evaluator kapsamı) |
| Demo üreticileri | — | ÜÇÜ DE yalnız legacy `deployment.environment=demo` (cmd/demo/main.go:402,1693; java-demo.yaml:34; docker-compose.yml:312,381) — tek ortam, yeni anahtar hiç test edilmiyor | int/uat/prep emisyonu + iki anahtar varyantı |

## 2. MV MALİYETİ — kritik soru

Env boyutu OLMAYAN MV'ler: service_summary_5m (store.go:2009), operation_
summary_5m (:2040), operation_group_summary_5m (:2083), spanmetrics_1m/10s/1s
(:2115-2151), spanmetrics_calls/hist/duration_5m (:2359-2432), trace_summary,
db_*, messaging_* — 10+ MV. Lokal ölçüler: spans 24h **2.83M** satır
(spans_local 3.2M / 181MiB), service_summary_5m **75k**, operation_summary_5m
**302k**, topology_edges_5m **120k satır / 2.9MiB**.

**(a) Raw-fallback (cluster emsali):** env seçilince `useMV=false` → bounded
raw GROUP BY (`LIMIT 5000` + `max_execution_time=20`, repo.go:618-639).
deploy_env typed LC kolon = cluster'ın indexOf derive'ından UCUZ. Lokal:
saniye altı. Prod 1B span/gün + 24h pencere = cluster filtresinin BUGÜN
ödediği bedelin aynısı (20s tavan; scale-test'te 7.4× hacimde bütçe içinde).
Maliyet sıfır-migrasyon; risk yalnız geniş pencerede timeout.

**(b) MV'lere env boyutu:** Satır çarpanı ~×(servis başına env sayısı, ≤3):
op_summary 302k→~900k — MB mertebesi, depolama SORUN DEĞİL. Gerçek maliyet
migrasyon: env ORDER BY'a girmek ZORUNDA (AggregatingMergeTree sort-key'de
olmayan kolonu merge'de yutar — in-place inner-ALTER hilesi BURADA GEÇERSİZ),
yani MV başına dropCombinedMV + tarih kaybı (bucket'lar sıfırdan; spans
retention'ı içinden kısmi backfill) + rolling-deploy okuma-hatası penceresi
(CLAUDE.md pitfall) + distributed-safety sınıfı (prod'u İKİ kez kırdı;
spanmetrics_* highVolumeTables follow-up'ı hâlâ açık) × 10+ MV. Alternatif:
kardeş MV (`service_env_summary_5m`, operation_group emsali) = sıfır drop
riski ama çift yazma amplifikasyonu ve yine okuma tarafı çatallanır.

**(c) Hibrit — ÖNERİLEN:** Cluster deseniyle başla ((a) semantiği): env
filtresi Services/Endpoints'te raw dala, Traces/Explore'da zaten var olan
filterexpr yoluna, Logs'ta res-attr derive'a. Topology'de kolonlar hazır
(WHERE parent_env=? okuma-ucuz). MV boyutu YALNIZ prod'da geniş-pencere
env-filtreli listeler ölçülüp yavaş çıkarsa, kardeş-MV varyantıyla ayrı
dilim. Gerekçe: operatörün senaryosu (test ortamı, 3 env) raw-fallback
bütçesinin rahat içinde; 10-MV migrasyonu bugün satın alınacak risk değil.

## 3. UX ŞEKLİ — global env seçici

Öneri: **Topbar'da global env picker** (cluster gibi sayfa-başı DEĞİL).
Datadog'da `env` çekirdek tag'dir — APM sayfalarının sol üstünde kalıcı env
dropdown'ı; Dynatrace management zone'ları global filtredir. Cluster infra
boyutu (sayfa-başı kalabilir), env MANTIKSAL boyut — operatör "uat'a bak"
der ve her sayfa onu takip etmeli. URL: `?env=` her sayfada range gibi
taşınır (replace:true + sig-guard, v0.8.253 kuralı); boş = tüm ortamlar;
viewer read-only aynen görür. Kaynak: `/api/environments` — DISTINCT
deploy_env, 1h clamp (clusterScanWindow emsali, repo.go:413), warmer'a
gerek yok (LC kolon DISTINCT'i ucuz).

Tel çerçeve: `[Coremetry] [sayfa] ……… [env: all ▾][int|uat|prep] [range] [tema]`
Env seçiliyken desteklemeyen yüzeyde (ör. Alerts) picker'da "bu sayfada
uygulanmaz" tooltip'i — sessizce yok saymak yok.

## 4. FAZLI PLAN (küçükten büyüğe; her dilim kendi v0.8.X'i)

- **Dilim 0a — BUG (~1s):** topology env derive'ı `deployment.environment.name`
  okumuyor + dolu deploy_env kolonunu kullanmıyor (topology.go:459,489,546) —
  v0.8.379'un ikizi. Fix: coalesce başına deploy_env kolonu + yeni anahtar;
  regresyon testi v0.8.379'u cite eder. Önce repro (bug-repro disiplini).
- **Dilim 0b — BUG (~1-2s):** bare `deployment.environment` metric filtresi
  metric_points'te missing-column (filterexpr wellKnown spans-şekilli map'inin
  metrics sorgusuna sızması). Fix: metric yolunda env→res_values çözümü veya
  frontend anahtar listesinden çıkarma; iki anahtar varyantı da test edilir
  (unit-mixing kuralı).
- **Dilim 0c — hızlı kazanım (~2s, sıfır şema):** demo'lara int/uat/prep
  emisyonu (go-demo yeni anahtar, java-demo legacy — fallback zincirinin İKİ
  dalı da canlı test edilir) + topology node'larına env chip'i (types.ts:64
  zaten taşınıyor, hiç render edilmiyor).
- **Dilim 1 (~yarım gün):** `/api/environments` + Topbar global picker +
  `?env=` URL konvansiyonu (henüz hiçbir sorguya bağlanmadan picker + taşıma).
- **Dilim 2 (~yarım gün):** Services + Endpoints `?env=` uygular — mevcut
  cluster raw-dalına `deploy_env = ?` eklenir (repo.go:616 ve
  endpoints.go:552 deseninin birebir kopyası; useMV gate'ine `&& env==""`).
  Cache key'e env girer (hash-all-inputs).
- **Dilim 3 (~yarım gün):** Traces/Explore `?env=` → FilterExpr enjeksiyonu;
  Problems/Inbox env enrichment + chip (ClusterChips emsali, deep-link
  `/services?env=`).
- **Dilim 4 (~yarım gün):** Logs env — CH: res-attr derive (clickhouse.go:121
  cluster emsali); ES: resource alan filtresi (prod ES field path'i AÇIK SORU).
- **Dilim 5 — ERTELENMİŞ/ölçülü:** MV env boyutu (kardeş-MV varyantı) yalnız
  prod ölçümü gerektirirse; topology strict ayrışması (edge kimliğine env —
  plain tablo + 14g TTL, MV migrasyonundan ucuz); alert per-env eşikleri
  (AlertRule şema + evaluator kapsamı = kendi /spec'i).

## 5. AÇIK SORULAR (bloklayan)

1. **Değerler:** Test ortamı yalnız `deployment.environment.name` ile mi
   int/uat/prep gönderiyor; emit ETMEYEN servis var mı? (Boş env UI'da
   "(none)" kovası olarak mı görünsün?)
2. **Global picker onayı:** Topbar global `?env=` mi, cluster gibi sayfa-başı
   mı? (Önerim global.)
3. **Prod bütçe kabulü:** env-filtreli Services/Endpoints geniş pencerede
   cluster'la aynı raw-scan bedelini ödeyecek (20s tavan). Kabul mü, yoksa
   Dilim 5 (MV) baştan mı fonlansın?
4. **Topology strict ayrışması:** mobile-bff 3 env'de TEK node görünmeye
   devam edecek (env chip "karışık" gösterir) — Dilim 5'e ertelemek kabul mü?
5. **Prod ES log alanı:** loglar prod'da hangi env alanını taşıyor
   (`resource.deployment.environment[.name]`? örnek doküman lazım) — Dilim 4
   bunu bilmeden şekillenemez.
6. **Alert per-env eşiği:** şimdi gerekli mi? (Şema + evaluator işi; gerekiyorsa
   ayrı spec çıkarırım.)

**Stage 1 burada bitiyor — onayını bekliyorum.**

---

## KARAR KAYDI (2026-07-08, operatör onayı)

- **Picker:** GLOBAL Topbar env picker'ı — `?env=` range gibi sayfalar arası
  taşınır. ("Global Topbar picker" seçildi.)
- **Maliyet stratejisi:** cluster-parity RAW-FALLBACK — env filtresi aktifken
  okumalar sınırlı raw yoluna düşer; MV'lere env boyutu YOK (ileride ölçülüp
  gerekirse ayrı dilim). ("Cluster gibi raw-fallback" seçildi.)
- Audit'in bulduğu iki bug kapatıldı: topology env derive v0.8.380,
  metrik filtresi kolon hatası v0.8.381. Ingest fallback v0.8.379'da.
- Fazlar: 0c+1 (demo 3-env + /api/environments + Topbar picker + /traces
  ilk tüketici) v0.8.383 olarak uçuşta; 2 (Services/Endpoints) → 3
  (Problems) → 4 (Logs) sırayla. Soru 5 (prod ES log env alanı) hâlâ açık —
  Dilim 4'ün ön koşulu.
