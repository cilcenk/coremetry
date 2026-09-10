# Coremetry — Ekip Geliştirmesi ve GitHub Yayını Hazırlık Denetimi

Tarih: 2026-09-10 · Taban: HEAD `2f9ada62` = v0.10.612 · Kapsam: salt-okunur denetim, hiçbir dosya değiştirilmedi.
Yöntem: dört paralel salt-okunur tarama (sızıntı + git geçmişi, `.claude/` + guardrail, CI + katkı altyapısı, onboarding); her bulgu dosya:satır referanslı. Ölçüm komutları ve sayımlar bölüm içinde.

> **Maskeleme politikası (bu doküman repoya girecek):** müşteri/kurum adı, alan adları, LDAP DN'leri ve e-posta yerel kısımları burada `<KURUM>`, `coremetry-prod.<KURUM-domain>`, `CN=<user>` biçiminde maskelidir. Ham değer listesi repo DIŞINDA tutulur (operatöre ayrıca iletilen `leak-raw.txt`). İş kodları yalnız önekiyle (`BSA_0xx`, `ERR_*`) anılır.

---

## 0. Yönetici özeti

| # | Bulgu | Kanıt |
|---|---|---|
| 1 | **Ağaçta kurum adı yok, dolaylı kimlikler var; geçmişte kurum adı VAR → history rewrite gerekli.** 1 intranet FQDN + 6 prod IP (test fixture ve 1 ürün-kodu yorumu), Oracle şema/kolon taksonomisi ürün varsayılanı olarak, 82 gerçek OpenShift iş yükü adı 41 dosyada, LDAP OU/kişi adları; geçmişte kurumsal e-posta + `com.<KURUM>` paket adı + Influx bucket/Flux şablonları. | §1 |
| 2 | **`main` 10 gündür kırmızı; hiçbir required check yok; `release.yml` CI'a bağlı değil.** 2026-08-31'den bu yana 300 koşu: 186 failure, 114 cancelled, 0 success. Neden lokalde tekrarlanıyor: v0.10.567'nin saat-dilimine bağlı vitest'i (UTC runner'da 2 test düşer) ve `npm audit` 2 HIGH. Branch protection 404, ruleset `enforcement: disabled`. | §4 |
| 3 | **"Metrikler yalnız VM'e" kuralı aspirasyonel: kod CH `metric_points`'e de yazıyor**, kapatan bayrak yok; CLAUDE.md VM'i hiç anmıyor. | §2.3 B3 |
| 4 | **CLAUDE.md tek sahibe yazılmış** (15 örtük varsayım: kişisel memory atıfları, `kuyruk` protokolü, minikube deploy, ikinci Türkçe H1); 3 skill + agent dosyası api.go kuralıyla çelişiyor; `/kuyruk` kişisel. | §2 |
| 5 | **Katkı altyapısı yok**: CONTRIBUTING, CODEOWNERS, PR/issue şablonu, SECURITY.md, .editorconfig, .gitattributes; Dependabot alerts/security updates kapalı; 4 Mayıs'tan kalma Dependabot PR açık. LICENSE MIT, bağımlılıklar uyumlu (GPL/AGPL yok). | §5 |
| 6 | **Onboarding**: taze klonda `go build` `frontend/dist` olmadan patlar; 110 `COREMETRY_*` env değişkeni, belgesi yok; api.go 12113 satır; test kültürü ("pin test", mutasyon) belgesiz; yorumların %72-76'sı Türkçe — "neden" bilgisi Türkçe okumayanlar için kayıp. | §6 |
| 7 | **Biçim borcu gerçek**: `gofmt -l` 83 dosya (go1.24/1.25/1.26 aynı liste — toolchain sapması değil), prettier 1042 dosya, golangci 123, gosec 490 (kapı değil), CodeQL 64 açık alert (11 critical). | §4.3 |

Tek cümle: yayından önce iki zorunlu blok var — (a) ağaç sentetikleştirme + history rewrite + kök manifestlerin repo dışına çıkması, (b) CI'ı yeşile döndürüp required check + ruleset açmak. Gerisi ekip verimliliği (§8 matrisi).

---

## 1. Sızıntı taraması (en yüksek öncelik)

Kapsam: 3237 takipli dosya + untracked (`.mcp.json`, `.ai/`, `.claude/agent-memory/`, `.claude/settings*.json`, `scratchpad/`) + gitignore'lu kök manifestler (yalnız bilgi) + 3654 commit'lik geçmiş (`git log --all -p`, pickaxe `-S`). Ham (maskesiz) 1524 satırlık liste repo dışında: `scratchpad/leak-raw.txt` (bölümler TREE / IGNORED / UNTRACKED / HISTORY).

**TL;DR**
- Ağaçta kurum adı / alan adı **doğrudan yok** — v0.9.656 ve v0.9.698 temizliği tutmuş. Ama **dolaylı** kimlikler duruyor: 1 intranet FQDN, 6 prod IP, Oracle şema/tablo/kolon taksonomisi, bir fraud tablosu (Türkçe kolonlu), Influx bucket/alan adları (docs), 82 farklı gerçek OpenShift iş yükü adı (`kurum-öneki*`), cluster adları (`ocp*`), LDAP OU ve iki kişi adı.
- Gitignore'lu kök manifestler (`deployment-coremetry-prod*.yaml`, `route-*.yaml`, `.env`) kurum alan adlarını **yoğun** taşıyor; git'e hiç girmemişler (pickaxe 0 commit). Tek savunma gitignore; dosyalar repo kökünde durdukça `git add -f` / `-A` riski.
- **Yalnız geçmişte:** bir çalışanın kurumsal e-postası, `com.<KURUM>.bsa.core.exception.*` Java paket adı, Influx bucket'ları + Flux şablonları (commit mesajlarında da).
- **History rewrite: EVET** (gerekçe 1.8).

### 1.1 Hostname / FQDN / IP / URL

| Konum | Kategori | Maskeli değer | Öneri |
|---|---|---|---|
| `internal/chstore/ddl_queue_health_test.go:80` | FQDN | `lc*****01.<KURUM-intranet-domain>` (prod CH düğümü) | fixture sentetik (`chc-prod-01.intranet.example`) |
| `internal/api/clickhouse_nodework.go:118-119` | IP + host, **ürün kodu yorumu** | `172.31.240.x`, `lc*****01` | tamamen kaldır (yorumu jenerikleştir) |
| `internal/api/clickhouse_nodework_test.go:137` | IP | `172.31.240.x` | TEST-NET (`203.0.113.x`) |
| `internal/chstore/mv_fallback_test.go:47` | IP | `172.31.240.x`, `100.80.13.x` (gerçek prod hata satırı) | sentetik |
| `internal/chstore/distributed_batching_test.go:167` | IP | `172.31.240.x` | sentetik |
| `internal/chstore/exception_fingerprint_test.go:84-85` | IP | `10.244.5.x` (prod pod IP) | sentetik |
| `scripts/migrate-0009-state-unify.sh:98-100` | IP + URL (help metni) | `--host 172.31.240.x`, `http://coremetry.internal` | `<ch-host>` yer tutucu |
| `frontend/src/pages/clusters/podWorkload.test.ts:33` | cluster + ns | `ocp*3`, gerçek ns | sentetik (`ocp-test`, `shop`) |
| `scratchpad/copilot-tools/{compare,option-C}.html` (untracked, **ignore edilmemiş**) | IP | `10.40.2.x:9200` (prod ES) | kaldır + gitignore |

Gitignore'lu yerel dosyalarda (git'te YOK): `coremetry-prod.<KURUM-domain>`, `coremetry-test.<…>`, `ocp-test-es.<…>:9200`, `api-int.ocp*.<…>`, `docker.artifacthub.<KURUM-preprod-domain>/…`, NO_PROXY'de intranet/dmz/iştirak alanları + CIDR'lar, ns `coremetry-prod/-prep` (159 satır). **Öneri:** repo dışına taşı (`~/coremetry-deploy/`), gitignore tek savunma olmasın.

Geçmiş: `history:930a4196` (v0.9.316) `internal/anomaly/log_patterns_cost_test.go:74` `com.<KURUM>.bsa.core.exception.ExternalSystemException` → kaldırıldı `966334a8` (v0.9.656). `history:3d0c17e9` (v0.9.660) `frontend/src/pages/usersColumns.ts:37` yorumda `<ad.soyad>@<KURUM>.com` → kaldırıldı `156f95eb` (v0.9.698). Kurum alan adları: **0 commit** (hiç girmemiş). `172.31.240.*` ilk `6e115529` (2026-07-26), 4 commit, hâlâ ağaçta.

### 1.2 Veritabanı şema / tablo / kolon

| Konum | Maskeli değer | Öneri |
|---|---|---|
| `internal/oracle/poller_test.go:71,110` | `FROM <ŞEMA>.ERROR_LOG` (gerçek şema sahibi) | sentetik `SHOP.APP_ERROR_LOG` |
| `frontend/src/pages/settings/oracleForm.ts:45-72` (**ürün varsayılanı**) | `ORACLE_DEFAULT_TIMESTAMP_COLUMN`, `ORACLE_MAPPING_FIELDS` → 15 `ERR_*` kolonu (`_CHANNELCODE`, `_TASKCODE`, `_TELLERID`, `_CUSTOMERID` …) | config'e taşı (`system_settings` `oracle_sources` eşlemesi; varsayılanlar jenerik `ERR_*`) — Go tarafı `internal/oracle/mapping.go` `DefaultColumns()` aynı |
| `frontend/src/pages/settings/OracleTab.tsx:174,326,329,351` | placeholder `<ŞEMA>.ERROR_LOG`, `ERR_CODE NOT IN ('BSA_0xx')` | jenerik placeholder |
| `frontend/src/components/LogTable.tsx:449`, `lib/types.ts:1536`, `internal/chstore/store.go:1962`, `oracle_error_log.go:5`, `purge.go:25` | yorum/tooltip `ERROR_LOG` | jenerik |
| `internal/oracle/{mapping,settings,client,poller}_test.go`, `internal/api/oracle_logs_routes_test.go` | ~20 `ERR_*` fixture kolonu | sentetik `ERR_*` |
| `docs/audit/oracle-error-log-2026-09-09.md:115,195…`, `.claude/agent-memory/…` | `<SAHİP>.ERROR_LOG`, kolon listesi, kod semantiği | jenerik |
| `internal/appschema/appschema_test.go:16-153`, `appschema.go:9`, `internal/devops/mapper_statement_test.go:32-133`, `frontend/src/pages/settings/DevOpsTab.tsx:630` | `<kurum-kısaltması>.INT_T*` Türkçe kolonlu fraud tablosu, gerçek ORA-12899 satırı | sentetik `SHOP.ORDER_PHONE (CUSTOMER_ID, PHONE, NOTE)` |
| `migrations/00{01,02,03,08,09,10,11,12,14}*.sql` (≈400 geçiş), docs, `state_repartition_admin_test.go` | `ON CLUSTER uptrace_all` (docs "yer tutucu" diyor; müşteri prod küme adıyla aynı) | `values.yaml` `clickhouse.clusterName` → `{cluster}` makro — düşük hassasiyet |

`cmd/demo` içindeki `COREBANK.*`, `PKG_LEDGER.*` sentetik — OK. Sayım: `<kurum-kolon-öneki>_*` 156 / 19 dosya; fraud tablosu 40+ / 4; şema sahibi 2 / 1. Hepsi ağaçta (ilk `dcfc81db` 2026-08-28, `f7c9dd22` 2026-09-09, `5b11e51a` 2026-09-10).

### 1.3 Influx bucket / measurement (entegrasyon v0.10.606'da söküldü)

| Konum | Maskeli değer | Öneri |
|---|---|---|
| `docs/audit/influx-integration.md:4,9,180,259-262,365,383` (**takipli**) | `GG*Bckt/<measurement>/<field>` serisi, `(<operasyon-kolonu> × <hata-kodu-kolonu>)`, `<kanal-kolonu>/<fonksiyon-kolonu>` alan adları | tamamen kaldır ya da `<bucket>/<measurement>/<field>` |
| `docs/audit/cosre-agent-v2.md:248,440,599` | measurement adı | jenerik |
| `docs/plans/spec-influx-error-ratio.md` (untracked, ignore edilmemiş) | oran formülü + alan adları | kaldır |
| `internal/anomaly/external_test.go:94,121,133`, `external_cluster_test.go:87`, `internal/chstore/external_seasonal_test.go:41-42`, `frontend/src/features/anomalies/externalEvidence.test.ts:8,55` | `<hata-sayacı-kolonu>`, `<hata-adedi-kolonu>`, `<operasyon-kolonu>`, `<hata-kodu-kolonu>` | sentetik (`ext-src`, `fail_count`, `op_code`, `err_code`) |

Yalnız geçmişte: bucket adları + tam Flux şablonları (`frontend/src/pages/settings/influxForm.ts`, `InfluxTab.tsx`, `internal/influx/*` — ilk `ee8aaf0f` v0.10.222 2026-09-01 → silindi `6ab9c4cf` v0.10.606; 47 satır, 9 commit); commit mesajları da ekibi/bucket'ı anıyor (v0.10.526) — yalnız rewrite ile temizlenir.

### 1.4 OpenShift / K8s adları

| Konum | Maskeli değer | Öneri |
|---|---|---|
| `frontend/src/pages/service/runtimePodLabel.ts:34` (ürün kodu yorumu), `runtimePodLabel.test.ts:31-49`, `podDetailPath.test.ts:40-43`, `lib/logCluster.test.ts:14-15`, `docs/audit/entity-layer-discovery-2026-08-28.md:134` | cluster `ocp*a`, `ocp*b` | sentetik `prod-eu` / `prod-us` |
| `internal/logstore/elasticsearch.go:2695` (ürün kodu yorumu), `frontend/src/components/LogsHistogram.test.ts` | cluster adları | sentetik |
| `frontend/src/pages/clusters/podWorkload.test.ts` (47 satır), `podWorkload.ts:8,28-29,78-79,153-154` (ürün kodu yorumu) | `kurum-öneki*-prep` iş yükleri, RS hash'li pod adları | sentetik (`shop-login-prep`) |
| `internal/chstore/job_service.go:37,50,52,108-110` (ürün kodu), `job_service_test.go` | `<ns>/kurum-öneki*-uat` | sentetik |
| `internal/api/service_metric_throughput.go:156,714-720` (ürün kodu) | `kurum-öneki*-uat/-prod` | sentetik |
| `internal/devops/{repo_resolve,frame_links,resolve_dryrun,repo_catalog}_test.go` (43 satır) | `kurum-öneki*-prod` | sentetik |
| `internal/api/{near_names,throughput_candidates,servicegraph_hidden,correlation_link,chat_tool_links}_test.go`, `internal/evaluator/selfhealth_volume_test.go`, `internal/logstore/elasticsearch_servicefilter_test.go`, `internal/vmetrics/throughput_test.go`, `internal/notify/…`, `internal/mcptools/…` | `kurum-öneki*` (login/mobile/limit/ödeme/bpm/customer …) | sentetik |
| `docs/DEPLOY-EVENTS.md:24` | `kurum-öneki*-prod` | `shop-checkout-prod` |
| `scratchpad/exc-pods/fixture.html` (untracked, ignore edilmemiş, 54 satır) | gerçek prod pod adları (RS hash'li), düğüm adları `ocp*wrp*`, cluster `ocp*a/b` | tamamen kaldır + `scratchpad/` gitignore |
| `charts/coremetry/values.yaml:12-18,166`, `examples/openshift/*` | `registry.example.com`, `coremetry.local` | zaten sentetik — OK |

Sayım (takipli): `kurum-öneki*` 82 farklı ad, 234 geçiş, 41 dosya (5'i ürün kodu yorumu); `ocp*` 31 geçiş / 13 dosya. İlk `e7449af4` (2026-07-18), `eba82688` (2026-07-20). Geçmiş-only yok; hepsi ağaçta.

### 1.5 İş kodu taksonomileri

| Konum | Maskeli değer | Öneri |
|---|---|---|
| `frontend/src/pages/settings/OracleTab.tsx:351`, `oracleForm.test.ts:171` | `NOT IN ('BSA_0xx')` | jenerik `ERR_CODE NOT IN ('E_GENERIC')` |
| `internal/oracle/{poller,mapping,settings,client}_test.go`, `internal/api/oracle_logs_routes_test.go` | `BSA_0xx` (3 kod) | `ERR_0xx` |
| `docs/audit/oracle-error-log-2026-09-09.md:224-360` | belirli kodların semantiği ("jenerik kod", "asla birleşmez") | jenerik |
| `cmd/demo/business_dims.go:40-46,63` | 6 haneli kanal kodu listesi + Türkçe bankacılık kanal yorumları | değerler sentetik; yorumları e-ticaret kanallarına çevir |
| `internal/chstore/promoted_mismatch_test.go:15,95`, `filterexpr_promoted_test.go:26-83`, `trace_filters_trace_level_test.go:18` | `channel_code = '0x0x0x'` | sentetik `'web'` |
| `frontend/src/pages/Traces.tsx`, `docs/audit/traces-attribute-columns.md:182` | `DEFAULT_TRACE_COLUMNS = [openshift.cluster.name, channel_code, function_code, function_id]` (müşteriye ayarlı) | config'e taşı (`system_settings`) |
| `oracleForm.ts:64-71`, `migrations/0002,0005,0007`, `internal/chstore/promoted_attr.go` | promoted attr taksonomisi `channel.code`, `function_code`, `operation.code`, `error.external_code`, `task.code`, `teller.id`, `customer.id` (430 geçiş) | İngilizce/jenerik — kalabilir; `teller.id` → `agent.id` |

Sayım: `BSA_0xx` 14 / 8 dosya; kanal kodu değerleri 12 / 4 dosya.

### 1.6 Kullanıcı / LDAP / e-posta

| Konum | Maskeli değer | Öneri |
|---|---|---|
| `internal/ldap/team_attr_test.go:25-27,39-43,52-60,113,131-133` (18 satır) | `CN=<user>,OU=<grup>,OU=<grup>,DC=bank,DC=local`; `division: "<grup> Ekibi"` — OU'lar gerçek görünümlü Türkçe birim adları, CN'ler gerçek ad (yazar + 1 meslektaş); ilk `a7979328` (2026-07-10) | sentetik (`CN=alice,OU=Payments,OU=Engineering,DC=corp,DC=example`) |
| `frontend/src/pages/settings/LdapTab.tsx`, `internal/ldap/sync_test.go`, `ldap.go` | `DC=corp,DC=example`, `Coremetry-Admins` | OK |
| e-postalar (ağaç) | hepsi sentetik; `charts/coremetry/Chart.yaml:21` yazarın kişisel e-postası | yazar kararı |
| geçmiş `3d0c17e9` | meslektaşın kurumsal e-postası (yorum) → `156f95eb`'de silindi | rewrite |
| git author'lar | 3 kimlik, ikisi `<name>@<host>.local` | kozmetik |

### 1.7 Secret / token / parola (yalnız varlık)

| Konum | Bulgu | Öneri |
|---|---|---|
| Takipli ağaç | **Canlı secret yok.** `internal/copilot/provider_parity_test.go:56` sentetik token; `internal/api/logs_body_test.go:19,29` base64 cursor; `.env.example`, `config.yaml`, `examples/openshift/05-coremetry-secret.yaml`, compose dosyaları yer tutucu | OK |
| `values-minikube.yaml` (takipli) | sabit 64-hex JWT secret literal ("LOCAL DEV ONLY") + `existingSecret` | ENV/`--set-string`; literal'ı kaldır |
| `.env` (ignored) | tünel token'ı, JWT secret, ilk parola, CH parolası, AI API anahtarı dolu | ignored kalsın; makine paylaşılıyorsa rotate |
| `.claude/settings.local.json:8` (ignored) | izin satırında gömülü yerel dev JWT | ignored kalsın |
| `.mcp.json`, `.claude/settings.json`, `.ai/`, `.claude/agent-memory/` | secret yok; agent-memory'de 1 satır tablo adı | §2 |

### 1.8 Özet ve history-rewrite kararı

| Kategori | Farklı tanımlayıcı | Ağaç geçiş / dosya | Untracked-not-ignored | Yalnız geçmişte | İlk commit |
|---|---|---|---|---|---|
| Host/IP/URL | 1 FQDN + 6 IP + 2 cluster/ns | 20 / 10 | 4 | 2 sınıf (e-posta domain'i, `com.<KURUM>` paketi) | `6e115529` 2026-07-26 |
| DB şema | şema sahibi, `ERROR_LOG`, ~20 `ERR_*`, fraud tablosu(+3 kolon), `uptrace_all` | 220+410 / 30 | 0 | 0 | `dcfc81db` 2026-08-28 |
| Influx | bucket, measurement/field, `<kanal-kolonu>` … | 26 / 8 | 1 | bucket'lar + Flux (47 satır, 9 commit) | `ee8aaf0f` 2026-09-01 |
| OCP/K8s | 82 `kurum-öneki*` + 6 cluster + 6 düğüm | 265 / 54 | 54 | 0 | `e7449af4` 2026-07-18 |
| İş kodları | `BSA_0xx`×3, kanal kodları ×8, `DEFAULT_TRACE_COLUMNS` | 45 / 12 | 0 | `<kanal-kolonu>` | `f7c9dd22` 2026-09-09 |
| LDAP/e-posta | 2 OU, 2 kişi | 18 / 1 | 0 | 1 kurumsal e-posta | `a7979328` 2026-07-10 |
| Secret | 0 canlı (1 local-dev literal) | 1 / 1 | 0 | 0 | — |

**History rewrite gerekli: EVET.** (a) Kurumsal e-posta ve `com.<KURUM>.bsa.*` paket adı kurum adını doğrudan taşıyor; ağaçtan silinmiş olsalar da `git log -S` ile iki komutta bulunur. (b) Influx bucket adları + tam Flux sorguları müşterinin Influx topolojisini ifşa eder ve commit mesajlarında da geçer — mesajlar yalnız rewrite ile düzelir. (c) Prod IP'leri ve intranet FQDN ağaçta olduğundan sıra: önce ağaç sentetikleştirme (1.1–1.6), sonra `git filter-repo --replace-text` tek geçiş (kurum adı, preprod alan adı, intranet TLD, 2 e-posta, 2 bucket adı, `172.31.240.*`, `100.80.13.*`); BFG gereksiz. 3654 commit / 69 MiB → dakikalar; sonrasında 3477 tag yeniden yazılır, tüm klonlar yenilenir. Ağaç temizliği rewrite'tan ÖNCE yapılmazsa yeni commit'ler yine kirlenir.

---

## 2. `.claude/` dizini: çok geliştiricili hale getirme

### 2.1 Ölçülen taban

| Ölçüm | Değer |
|---|---|
| `internal/api/api.go` | 12113 satır; `registerRoutes` api.go:600–1393 (793 satır), 341 `mux.Handle*`, 286 `func` |
| api.go içinden doğrudan `s.registerXxxRoutes(mux)` | 29 çağrı; `registerRoutesExtra` (`route_registry.go:29`) ile `init()` kaydı yapan dosya: 23 |
| Go toolchain | lokal go1.26.2 / `go.mod:3` `go 1.25.0`, `toolchain` satırı yok → `gofmt -l ./internal ./cmd .` = **164 dosya** (yalnız yorum hizalaması) |
| Lint | `.golangci.yml` v2 (standard + gofmt); CI lint job `continue-on-error` + `--issues-exit-code=0`; ESLint `\|\| echo warning` |
| Git hook / pre-commit / gitleaks / husky | yok |
| `.claude/` git'e takılı | 22 dosya (settings.json, hooks/1, agents/1, skills/19) |
| `.claude/settings.local.json` | 708 satır; 692 allow (653 `Bash`), mutlak `/Users/<user>/…` yol, `enableAllProjectMcpServers: true`; gitignore'da (`.gitignore:65`) |
| `.mcp.json` (untracked, **gitignore'da değil**) | tek stdio sunucu, mutlak `/Users/<user>/…` yol; secret yok |
| `.claude/agent-memory/` (untracked, **gitignore'da değil**) | 196 dosya / 920K kişisel memory (87 feedback, 106 project) |
| `docs/DECISIONS.md` | v0.5.208 → v0.6.8'de biter; v0.7–v0.10 kararları yok |
| `docs/INCIDENTS.md` | v0.5 madde listesi + v0.10.338…v0.10.611 arası 11 bölüm; CLAUDE.md'nin andığı v0.8/v0.9 olayları yalnız commit mesajında |

### 2.2 CLAUDE.md'deki "yalnız repo sahibinin bildiği" örtük varsayımlar

| # | CLAUDE.md | Sorun | Öneri |
|---|---|---|---|
| A1 | :20 `memory feedback-no-redaction.md` | Repo dışı kişisel memory dosyasına atıf; ekip göremez | Gerekçeyi satıra yaz + `docs/DECISIONS.md` girdisi; memory atfını kaldır |
| A2 | :37-41 Versioning | "operatör kararı 2026-08-25", "v0.9 zinciri v0.9.1388'de kapandı" — bağlamsız, TR/EN karışık | `docs/RELEASE-1.0.md` + DECISIONS "tag politikası"; CLAUDE.md'de tek satır |
| A3 | :30 "Go 1.22+" | go.mod 1.25.0, README:469 "1.25+", CI 1.25 → bayat | go.mod tek kaynak |
| A4 | :75 Triage paragrafı | "v0.9.1205 operatör direktifi", "vida: `exception_triage`" — gerekçe ve dosya yok | Tablo + gerekçe DECISIONS'a; "vida" → `system_settings` anahtarı + dosya |
| A5 | :79 `kuyruk` / `devam` / "Hangisi?" | Tek operatörün sohbet protokolü; oturum durumuna bağlı | Kişisel `~/.claude/CLAUDE.md`'ye taşı; repo CLAUDE.md'den çıkar |
| A6 | :80 "ship as v0.10.X+1 immediately, never batch" | Tek committer trunk varsayımı; PR akışı yok | "bugfix ayrı PR + ayrı tag" |
| A7 | :82-89 release hattı "… → deploy (background, ONE at a time)" | "deploy" = operatörün minikube'u; `release.yml` zaten tag'de imaj+chart üretiyor | `docs/RELEASING.md` (CI tabanlı); lokal deploy opsiyonel |
| A8 | :91-93 commit trailer | Politika ekip için belirsiz (insan commit'inde de mi?) | Açıkla ya da kaldır |
| A9 | :95-111 skill listesi | `/kuyruk`, `/release` kişisel; `/scale-audit (quarterly)` kim tetikler? | Ekip / kişisel ayrımı |
| A10 | :133-145 pitfall'lar (16 tag atfı) | v0.6–v0.10.337 olayları INCIDENTS.md'de yok | Atıf yapılan her tag için INCIDENTS girdisi (v0.8.253/265/267/270, v0.9.339/612/1194/1205) |
| A11 | :145 "maxUnavailable: 0 … restart collector" | Küme erişimi olan tek kişiye ops talimatı | `docs/runbooks/` |
| A12 | :195-196 "Decision log v0.5.208 → v0.6.8" | Sonraki kararlar untracked agent-memory'de ve `docs/audit/*`'ta dağınık | DECISIONS.md'yi v0.7→v0.10 için doldur |
| A13 | :198-207 ikinci H1 "Coremetry Development Rules" | api.go kuralının TAM hâli yalnız burada ve Türkçe; leader lock, uPlot, SSE kuralları da yalnız TR | Ana yapıya erit, tek dil |
| A14 | genel | Başlıklar EN, kritik kurallar TR | Kural metni tek dil (öneri EN); olay anlatıları TR kalabilir |
| A15 | `.claude/agents/coremetry-feature-shipper.md` (tracked, 25 KB) | CLAUDE.md kopyası + fazlası; checklist 7. madde "route in api.go" api.go kuralıyla **çelişir**; kişisel memory'ye bağlı | CLAUDE.md'ye link versin, kural kopyalamasın |

### 2.3 Mimari kısıtlar açık kural mı, kapı var mı?

| # | Kural | Yazılı mı | Zorlayan test/hook | Boşluk |
|---|---|---|---|---|
| B1 | api.go büyümez; yeni uç kendi dosyasında (`registerXxxRoutes` / `registerRoutesExtra`) | Evet: CLAUDE.md:99-101 (EN özet), :202 (TR tam), api-route SKILL :8-19; mekanizma `route_registry.go:29-38`, `buildMux` :57-64 | `mux_routes_test.go:16-27` yalnız kalıp çakışması; alan-bazlı "api.go büyümedi" testleri (`admin_attr_index_test.go:31`, `ai_budget_test.go:44`, `changes_routes_test.go:27` …) kendi string'ini arar; `post-edit-check.sh` yalnız build/tsc; `scripts/audit.sh:277-288` CHECK 7 çift kayıt | **Global satır-sayısı kapısı yok**; `registerRoutes` 793 satır ve 29 doğrudan çağrı; agent dosyası kuralla çelişiyor |
| B2 | Trace/log tek depo ClickHouse; ES yalnız log okuma | Evet: CLAUDE.md:45-46 (invariant 1-2), :74, :153-154 | Yapısal kapı yok; davranış testleri `logstore/skip_total_test.go:12`, `api/log_field_search_test.go:47` | Düşük risk; "logstore'da Insert/Index sembolü yok" yapısal testi eklenebilir |
| B3 | Metrikler VictoriaMetrics'e; ham OTLP metrik CH'ye yazılmaz | **Hayır.** CLAUDE.md'de "VictoriaMetrics" 0 geçiş; :18/:70 `metric_points` sorgusunu normal sayar; :153 "CH writes (spans/metrics/logs)" | — | **Kural aspirasyonel, kod ÇİFT yazıyor:** `main.go:445` `consumer.NewSized("metrics", …, store.InsertMetrics)` → `:475 otlp.NewIngester` koşulsuz; `chstore/repo.go:212` `INSERT INTO metric_points`; `otlp/http.go:395-437` önce VM forward (`forward.go:17-33`, `vmSvc.WriteReady` kapılı) sonra CH. CH yazımını kapatan bayrak yok. `docs/audit/vm-metrics-migration.md:265` Aşama 1 (çift yazım) tamam, `:284` Aşama 3 (CH yazımını kapat) yapılmadı. CLAUDE.md'ye gerçek durum yazılmalı. |

### 2.4 Skill değerlendirmesi (19)

Kitle: **E** ekip geneli · **K** kişisel · **R** repo-özel ama ekip için gerekli.

| Skill | Amaç | Kitle | Dil | Bayatlık | Hüküm |
|---|---|---|---|---|---|
| api-route | Yeni uç kendi dosyasında + route/auth/audit/cache | R | TR | Satır atıfları kaymış (`editorRoles` 519→578, `spaHandler` 1338→1374, `writeJSONError` 11609→11838); semboller geçerli | Keep; atıfları sembole çevir |
| bugfix | Prod bug → kök neden → regresyon → release | K→R | EN | :25-62 zorunlu kişisel memory okuma; :183 `docker exec` vs release:143 minikube çelişkisi; :187 `/tmp/cm.cookies`, demo hesabı | Rewrite: memory → INCIDENTS; ortam → `docs/local-dev.md` |
| clickhouse-schema | CH şema/MV/ORDER BY/dağıtık sözleşme | R | TR | :8 "0001–0008" → bugün 0001–0014; :81 `cluster.go:1315`→1368; :211 memory atfı | Keep; sayım + §6/§8 güncelle, memory atfını kaldır |
| copilot-surface | Yeni ✨ Explain yüzeyi | R | EN | :85-99 "route + handler api.go'ya" — api-route ile **çelişir**; :104 `http.Error`; :181 ham `<button>` | Rewrite |
| frontend-conventions | FE ev kuralları + kapılar | R | EN | Atıflar geçerli | Keep |
| frontend-dashboard-panel | Yeni panel tipi | R | EN | :41 katalog 6 tip → `lib/types.ts:4203` bugün 9; örnek "heatmap" zaten var | Rewrite |
| frontend-design-system | Primitif ara, sonra yaz | R | TR | AS-3 (PageShell barrel) çözülmüş (`ui/index.ts:87`); sayımlar tarihsiz | Keep; açık soruları güncelle |
| helm-chart-coremetry | Chart guardrail'leri | R | EN | :217 "version/appVersion senkron" kuralı uygulanmıyor: Chart 0.10.1 vs app v0.10.612; :235 `oci://ghcr.io/<owner>/…` sahip-özel | Keep; sürüm politikasını yeniden yaz |
| kuyruk | Operatör kuyruğu, "Hangisi?" | **K** | EN | :8 kişiye özel; :9 olmayan CLAUDE.md bölümüne atıf; memory linki | **Repo'dan çıkar** (kişisel skills'e) |
| mcp-tools | MCP tool/resource/prompt ekleme | R | EN | :214 "test yok" → bugün 31 `_test.go`; :83 `rangeWindow` imzası değişti | Rewrite (küçük) |
| otel-conventions | OTel kuralları + triage | R | EN | §6 `internal/sampling` anlatıyor — paket yok (v0.8.73'te kaldırıldı); kendi içinde çelişki | Rewrite (§6'yı sil) |
| otlp-converter | OTLP→CH alan haritası | R | TR | Satır atıfları kaymış; "golden test yok" → `internal/otlp/testdata/` + golden test **var** | Keep; atıf + golden bölümü güncelle |
| perf-triage | Tek yavaşlık şikâyeti → ölçüm | R | TR | `localhost:8090`, demo hesabı, `kubectl exec chc-0` kişisel ortam | Keep; ortamı değişkenleştir |
| release | Tag kes, kapılar, push, imaj | K→R | EN | Doğrudan `main`'e push; lokal `make image`/minikube; `release.yml` CI'da zaten üretiyor | Rewrite (PR + tag → CI) |
| review-changes | Diff'i CLAUDE.md'ye karşı inceleme | E | EN | Güncel | Keep (PR review çekirdeği) |
| scale-audit | Çeyreklik tarama | R | EN | :152 rotalar artık 23+ dosyada | Keep; küçük düzeltme |
| spec | Fikir → plan → onay | E | EN | "10-step" vs CLAUDE.md 11; :54 "api.go (+N lines) route" çelişki | Rewrite (küçük) |
| tdd | Test-first döngü | E | EN | :62 "route registration in api.go" çelişki | Keep; :62 düzelt |
| where-is | Kavram → file:line | E | EN | :134 "8000+ lines" → 12113 | Keep |

Ortak: (1) copilot-surface / spec / tdd / agent dosyası hâlâ "api.go'ya route" diyor; (2) bugfix / perf-triage / release üç farklı lokal ortam varsayıyor (compose :8088 / minikube :8090 / `chc-0`); (3) memory atıfları repo dışına işaret ediyor (bugfix, kuyruk, clickhouse-schema); (4) dil: 5 TR, 13 EN, 1 karışık.

**Eksik skill'ler:** codebase-tour (bkz. §6), local-dev-setup, clickhouse-local-bootstrap (`--reset-schema`, migrations 0001–0014, `chsmoke`), pr-review-checklist, release-from-ci (chart `appVersion` kuralı), incident-writeup (INCIDENTS `###` şablonu), data-hygiene (müşteri adı/hostname/şema politikası + gitleaks), memory-to-docs damıtma.

**`.mcp.json` ve `agent-memory`:** ikisi de gitignore'a; `.mcp.json.example` sun. `settings.local.json` 87 mutasyon-yetenekli izin (`kubectl exec *`, `helm upgrade *`, ES `curl -XDELETE`, `pkill`) ve demo şifreli curl satırları taşıyor → gitignore'lu kalmalı; dağıtılacak `settings.json` yalnız salt-okunur komut (bugünkü :3-36 iyi; :35 `Bash(python3 -c ' *)` geniş).

---

## 3. Guardrail / hook tasarımı (yalnız tasarım)

### 3.1 api.go büyüme kapısı (ratchet)

**Taban:** `.claude/baselines/api_go_lines` = `12113` (dizin bugün yok). Mevcut `post-edit-check.sh`: hook JSON'undan `file_path` (:19-21), `jq` yoksa sessiz geçer (:13-17), `.go` düzenlemesinde `go build ./...` (:25-29), `frontend/**/*.ts(x)`'te `tsc --noEmit` (:30-34); hata → `exit 2`.

Tek script `scripts/guard-api-go-size.sh`, üç tetik:

```bash
#!/usr/bin/env bash
# kullanım: guard-api-go-size.sh [--staged|--worktree]   (CI: --worktree)
set -u; cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}"
f=internal/api/api.go; b=.claude/baselines/api_go_lines
base=$(tr -d '[:space:]' < "$b")
case "${1:---worktree}" in
  --staged) now=$(git show ":$f" | wc -l | tr -d ' ');;
  *)        now=$(wc -l < "$f" | tr -d ' ');;
esac
if (( now > base )); then
  echo "api.go $now satır > baseline $base. Yeni route/handler kendi dosyasına (/api-route, route_registry.go init())." >&2
  exit 2
fi
if (( now < base )); then
  echo "api.go küçüldü ($base → $now). Ratchet: '$now' > $b yaz, aynı commit'e ekle." >&2
  [[ "${GUARD_STRICT_RATCHET:-0}" == 1 ]] && exit 2   # CI'da 1: bayat baseline PR'ı düşürür
fi
exit 0
```

| Tetik | Nasıl | Çıkış |
|---|---|---|
| Claude Code PostToolUse | `post-edit-check.sh:25` öncesine `[[ "$file" == */internal/api/api.go ]] && bash scripts/guard-api-go-size.sh` (ucuz kontrol pahalı build'den önce) | `exit 2` → Claude düzeltir |
| git pre-commit | `.githooks/pre-commit` + `git config core.hooksPath .githooks` (Makefile `setup`); api.go staged ise `--staged` | commit engellenir |
| CI | backend job'una `GUARD_STRICT_RATCHET=1 scripts/guard-api-go-size.sh --worktree` | büyüme ve bayat baseline kırmızı |
| `go test` çift kilit | `internal/api/api_go_size_test.go` baseline dosyasını okur; `/release` ve CI `go test ./...` ile otomatik | |

Ratchet yalnız aşağı iner. Kaçış: baseline yükseltmek = `.claude/baselines/` için CODEOWNERS onayı + commit trailer `Api-Go-Baseline: <gerekçe>`; `API_GO_GUARD=skip` yalnız yerelde. Hedef: `registerRoutes` (600–1393) aileler hâlinde dosyalara taşındıkça 12113 → ~11000.

### 3.2 Lint / format

| Araç | Bugün | Öneri |
|---|---|---|
| gofmt | toolchain drift → 164 dosya | (1) go.mod'a `toolchain go1.25.x`; hook'ta `"$(go env GOROOT)/bin/gofmt"`; (2) pinli sürümle tek seferlik sweep commit'i; (3) sonra hook'ta yalnız düzenlenen dosya, CI'da `test -z "$(gofmt -l …)"` hard gate |
| golangci-lint | v2 config var, CI advisory (~32 bulgu) | PR'da `--new-from-rev=origin/main` hard; tam tarama advisory; hook'ta düzenlenen paket `--fast` |
| ESLint | ~108 uyarı tabanı (0 hata), CI advisory | Hook: düzenlenen dosya; CI: değişen dosyalarda `--max-warnings 0` |
| tsc | hook + CI hard | Aynen |
| Prettier | `format:check` var, CI'da yok | CI advisory → gate |

### 3.3 Secret / müşteri-verisi taraması

Bugün: pre-commit yok; CI Trivy fs `secret` tarayıcısı yalnız genel kalıplar (AWS key vb.) — hostname/şema adı sınıfını görmez; müşteri manifestleri yalnız `.gitignore:97-101` ile korunuyor.

1. **gitleaks** pre-commit (`gitleaks protect --staged --redact --config .gitleaks.toml`) + CI (`gitleaks-action`, PR aralığı) + ilk kurulumda bir kez tam geçmiş; Trufflehog haftalık `--only-verified`.
2. **Repo'ya özel kurallar** (`.gitleaks.toml`, `useDefault = true`) — müşteri adı regex'e yazılmaz, sınıf düzeyi: `kurum-hostname` (`\.(com\.tr|gov\.tr)\b`, `\.(internal|corp|intra|lan)\b`, YAML `host:` allowlist dışı); `schema-qualified-oracle` (`\b[A-Z][A-Z0-9_]{2,}\.<KOLON-ÖNEKİ>_[A-Z0-9_]+\b`; çıplak `<KOLON-ÖNEKİ>_` Oracle özelliğinde meşru → path allowlist); `business-code-literal` (`(channel|function)_code\s*[:=]\s*['"][A-Z0-9]{2,8}`); `k8s-manifest-root` (`^(deployment|services|route)-coremetry.*\.ya?ml$`); `ip-private-prod` (`10\.x` yalnız docs/charts/yaml). Tam müşteri suffix'i gerekiyorsa CI secret'ından şablonlanır, repoya girmez.
3. **Sentetik veri allowlist'i:** `cmd/demo/`, `jboss-demo/`, `docs/DEMO-REALISM.md`, `internal/otlp/testdata/`, `*_test.go`, `*.test.ts(x)`; regex: `admin@coremetry\.local`, `example\.com`, `svc\.cluster\.local`, `localhost`; satır içi `# gitleaks:allow` yalnız gerekçeyle.
4. **Claude tarafı:** PostToolUse'a `gitleaks detect --no-git --source "$file"` (tek dosya ~50 ms) → `exit 2`.

---

## 4. CI kapı durumu

### 4.1 Durum
- `main`: 2026-08-31'den bu yana 300 CI koşusu — 186 failure, 114 cancelled (`concurrency.cancel-in-progress`, ci.yml:11-13 + her commit'e tag kadansı), **0 success**; son yeşil v0.10.215.
- 09-02→09-07 `Security :: npm audit` (2 HIGH); 09-08→bugün `Frontend :: Unit tests (vitest)`: `src/lib/externalLinks.test.ts` 2 test makine yerel saatinden beklenti kuruyor (`externalLinks.test.ts:62,64,182,186,190`), üretim kodu `Europe/Istanbul` (`externalLinks.ts:68,85-90`, v0.10.567) → UTC runner'da +3 saat fark. Lokal `TZ=UTC npx vitest run` ile tekrarlanır. Ek: `topoBfsLayout.test.ts:90` yük altında 5 s timeout (flake adayı).
- `release.yml` `needs:` içermez; her tag'de `success` — CI kırmızıyken imaj + chart yayınlandı (09-10'da 3 kez).
- Branch protection: 404. Ruleset `protectd` (id 17084905) `enforcement: disabled` (yalnız deletion + non_fast_forward). Tüm iş doğrudan push; PR yalnız Dependabot + 1 dış katkı.
- Toolchain üç stilde pinli: ci.yml:61-71 `go-version: '1.25'` + `check-latest`; codeql.yml:40-43 `check-latest` yok; perf-nightly.yml:32-35 `go-version-file`; Dockerfile:25 `golang:1.25-alpine`; `go.mod` `toolchain` satırı yok.

### 4.2 `continue-on-error: true` envanteri

| Dosya:satır | Job / adım | Komut | Required olsa bugün |
|---|---|---|---|
| ci.yml:135 (job) + :162 (`--issues-exit-code=0`) | `lint` / golangci-lint v2.12.2 | `golangci-lint run` | **FAIL — 123 bulgu** |
| ci.yml:301 (`if: always()`) | `security` / Trivy SARIF upload | `codeql-action/upload-sarif@v4` | PASS (yalnız yükleme; asıl Trivy adımı :254-297 hard ve **FAIL**) |
| release.yml:28 (job) | `docker` / build+push | `docker/build-push-action@v7` → GHCR | son 3 sürüm success; düşerse Release notu olmayan imaja işaret eder |
| release.yml:100 (job) | `helm` / package+push OCI | `helm package/push` | aynı |
| perf-nightly.yml:29 (job) | `perfcheck (advisory)` | compose + demo 3 dk + perfcheck | son 3 gece success; tasarım gereği advisory |
| ci.yml:37 (`\|\| echo ::warning`) | `frontend` / ESLint | `npx eslint src` | **PASS** — 0 error / 238 warning |

### 4.3 Kapı matrisi (lokal ölçüm, komutlar CI ile birebir)

| Kapı | CI'da | Sonuç | Bulgu | Lokal / CI süre |
|---|---|---|---|---|
| tsc | hard | PASS | 0 | 28 s / 35 s |
| ESLint | advisory | PASS (0 err) | 238 warn / 107 dosya | 11 s / 13 s |
| vitest | hard | **FAIL (TZ=UTC: 2)** | 495 dosya / 6052 test | 34-72 s / 79 s |
| Vite build | hard | CI'da 09-08'den beri hiç koşmadı (vitest sonrası) | — | — |
| go vet / build / test | hard | PASS | 63 paket | 3+8+56 s / 113+4+72 s |
| `CGO_ENABLED=0 go build` | **CI'da yok** (yalnız Dockerfile:37) | PASS | — | 6 s |
| CH duman (`-tags=chsmoke`) | hard | son yeşilde PASS | 1 test | — / 15 s |
| `go test -race` | hard, **4/63 paket** | PASS | — | 14 s / 60 s |
| govulncheck | hard | PASS (`GOTOOLCHAIN=go1.25.13`: 0 erişilebilir; lokal 1.26.2'de 14 stdlib "Fixed in 1.26.3" = lokal artefakt) | 0 | 43 s / 60 s |
| npm audit | hard (ci.yml:226-248) | **FAIL — 2 high** | `js-yaml` (override `package.json:62` `^4.3.1`, fix 4.3.2), `browserslist` | 5 s / 43 s |
| Trivy fs | hard | **FAIL — 4** | `js-yaml` HIGH; `golang.org/x/crypto` v0.53.0 **CRITICAL** (fix 0.55.0, govulncheck: erişilemez); `google.golang.org/grpc` v1.82.1 2×HIGH (fix 1.83.1) | 65 s |
| golangci-lint | advisory ×2 | **FAIL — 123** | errcheck 50 (44'ü `*.Close`), staticcheck 40, unused 25, govet 3, gofmt 3, ineffassign 2 | 52 s / 126 s |
| gofmt -l | **CI'da yok** | **FAIL — 83 dosya** | go1.24.12 / 1.25.0 / 1.25.13 / 1.26.2 → aynı 83 (yorum/struct hizası) — toolchain sapması DEĞİL | 2 s |
| helm lint + template | hard | PASS | 1 INFO | 1 s / 10 s |
| `make audit` | **CI'da yok** | PASS 0 🔴 0 🟡 | 9 kontrol | 3 s |
| prettier | **CI'da yok** | **FAIL — 1042 dosya** | — | 13 s |
| `go mod tidy -diff` | **CI'da yok** | FAIL (küçük) | go.sum'da 3 bayat satır çifti | 2 s |
| gosec | CI'da yok | 490 (HIGH 220: G115 ×101, G404 ×69, G118 ×31, G402 ×15 `internal/thanos/client.go:489`, G703 ×3 `internal/api/api.go:11759`, G709 ×1 `internal/api/topology.go:1376`) | | 70 s |
| hadolint / actionlint | CI'da yok | 2+1 / 3 warn | Dockerfile:58 DL3018; perf-nightly.yml:47,59, release.yml:128 shellcheck | 3 s |
| CodeQL | ayrı workflow, kapı değil | **64 açık alert** | critical 11 (`go/request-forgery` 5, `go/unsafe-quoting` 4, `go/email-injection`, `go/command-injection`); high 46 | — |
| Secret scanning | GitHub yerleşik | 0 açık; push protection **açık** | | |
| Dependabot alerts | — | **403 "disabled"** | | |

Kurulu olmayan araçlar (dürüstlük): golangci-lint, staticcheck, gosec, hadolint, actionlint, go-licenses, codeql, gitleaks — golangci/gosec `go run …@ver`, hadolint/actionlint docker ile koşuldu; go-licenses ve codeql koşulmadı (yerine `go list -m` + `gh api`). Tedarik zinciri: ci.yml:255 `aquasecurity/trivy-action@master` tek pin'siz action.

### 4.4 Required check'e çevirme sırası (en ucuz yeşilden)

| Sıra | Kapı | Bugün | Ön koşul | Efor |
|---|---|---|---|---|
| 1 | helm, tsc, go vet/build/test, CH duman, race, govulncheck | PASS | branch protection'da required işaretle (3. adımdan sonra anlamlı: `needs: frontend` zinciri kırmızıyken hiç koşmuyor) | 0 |
| 2 | ESLint | 0 error | ci.yml:37 `\|\| echo` sil; `--max-warnings 238` ratchet | 15 dk |
| 3 | **vitest** (CI'ı yeşile döndürür) | FAIL 2 | `externalLinks.test.ts` beklentisini `Europe/Istanbul` üzerinden kur (ya da `vitest.config.ts` `test.env.TZ='UTC'`); `topoBfsLayout.test.ts:90` timeout; regresyon başlığı vX.Y.Z | 1-2 saat (v0.10.613) |
| 4 | npm audit | FAIL 2 | `package.json:62` `js-yaml: ^4.3.2`; `npm update browserslist` | 30 dk |
| 5 | Trivy | FAIL 4 | `go get golang.org/x/crypto@v0.55.0 google.golang.org/grpc@v1.83.1` + tidy + test (Dependabot PR #42 muhtemelen kapsıyor) | 1 saat |
| 6 | `make audit` | PASS | backend job'a `make audit` | 15 dk |
| 7 | `go mod tidy -diff` | 3 satır | `go mod tidy` | 15 dk |
| 8 | gofmt | 83 dosya | tek mekanik sweep commit'i; adım `test -z "$(gofmt -l $(git ls-files '*.go'))"`; `go.mod` `toolchain go1.25.14` + üç workflow `go-version-file` | 30 dk |
| 9 | actionlint / hadolint | 6 warn | tırnak/`for _ in`; `apk add pkg=ver`; `--ignore DL3006` | 1 saat |
| 10 | golangci-lint | 123 | errcheck `exclude-functions` (CH `Close` −44); 25 `unused` sil; SA4000 ×13 = bilinçli determinizm iddiaları `f(x) != f(x)` — gerçek hata DEĞİL; v0.10.634 iki-bağlama biçimine çevirdi (`k1, k2 := f(x), f(x)`), lint sıfırlandı; SA1012 ×3 (`acache_test.go:223-229` nil ctx); SA1019 ×5; QF* ertele | 1-2 gün |
| 11 | prettier | 1042 | tek mekanik `npm run format` commit'i (sessiz pencere) | 1 saat |
| 12 | CodeQL | 64 | 11 critical triyajı; codeql.yml:42 `check-latest`; branch protection "Code scanning results" | 2-3 gün |
| 13 | gosec | 490 | kapı değil; `-severity high -confidence high -exclude G115,G404,G118` → G402/G703/G709 kalır | 1 gün |

Ayrıca: `release.yml`'e `needs: [ci]`/`workflow_run` bağı; `go test -race` kapsamı 4 → tüm paketler (nightly); ESLint `ignores: src/**/*.test.ts` (eslint.config.js:14) test dosyalarını lint dışında bırakıyor.

---

## 5. Katkı altyapısı

### 5.1 Var / yok

| Dosya | Durum | Not |
|---|---|---|
| `LICENSE` | **VAR** — MIT (2026, tek telif sahibi) | README:493-497 |
| `.github/dependabot.yml` | VAR — gomod, npm, actions; haftalık gruplu | 7 açık PR; #7/#14/#15/#16 Mayıs'tan (gruplama öncesi major'lar) kapatılmalı; Dependabot **alerts + security updates kapalı** |
| `CONTRIBUTING.md` | YOK | |
| `CODEOWNERS` | YOK | öneri 5.4 |
| `.github/PULL_REQUEST_TEMPLATE.md` | YOK | öneri 5.3 |
| `.github/ISSUE_TEMPLATE/` | YOK | `has_issues: true` |
| `SECURITY.md` | YOK | secret scanning + push protection açık ama bildirim kanalı yazılı değil; Private Vulnerability Reporting kapalı |
| `CHANGELOG` | YOK | README:503-506 "Releases = changelog"; 3477 **lightweight** tag (annotated değil), tag mesajı = commit başlığı |
| `.editorconfig` / `.gitattributes` | YOK | `frontend/.prettierrc.json` var; Go/SQL/lockfile için tanım yok |
| pre-commit / husky / lint-staged | YOK | |
| Branch protection | YOK (404); ruleset `protectd` `enforcement: disabled` | `delete_branch_on_merge: false`; merge/squash/rebase üçü açık |
| Kök hijyeni | — | izlenmeyen ikililer: `acache.test` 35 MB, `otlp.test` 31 MB, `coremetry` 82 MB, `demo`, `perfcheck`, `make-image.log`, `0001-…patch`, 11 PNG, `.github/modernize/`; `VERSION.txt` = `v0.10.492-dirty` (bayat) |

### 5.2 Lisans uyumluluğu
Go: 30 doğrudan bağımlılık Apache-2.0/MIT/BSD; grafta tek zayıf-copyleft `hashicorp/go-uuid` MPL-2.0 (dosya düzeyi, MIT ile uyumlu); `mongo-driver`, `gorm-oracle` derlenmiyor. npm (645 paket): MIT 473, Apache 101, ISC 25, BSD 21, MPL-2.0 2, CC-BY-4.0 1 (caniuse verisi), BlueOak 2, alan yok 3 (`@grafana/ui` geçişli); `@grafana/ui|data` 13.1.2 Apache-2.0. **GPL/AGPL/SSPL/BUSL yok** → MIT dağıtımı uyumlu. CI önerisi: `go-licenses check --disallowed_types=forbidden,restricted` + `license-checker --failOn 'GPL*;AGPL*'`.

### 5.3 Eksik dosya taslakları (bu repoya özel)
- **PR şablonu:** başlık/gövde biçimi (`v0.10.X — …`, 72 sütun); sınıf (operator-reported → gövde "Operator-reported:", ayrı sürüm, asla batch); **"Audit dokümanı / `docs/audit/` referansı" alanı (zorunlu, yoksa "yok")**; kapı checklist'i (`tsc`, `eslint` 0 error, **`TZ=UTC npx vitest run`**, `go build/vet/test` (+`-race` agent/notify/sse/cache), `make audit` 🔴=0, `gofmt -l` boş, `go mod tidy -diff` boş); "regresyon testi başlığı vX.Y.Z anıyor" (kanonik `internal/api/cache_key_test.go`); CH şeması değiştiyse `/clickhouse-schema` + migration + chsmoke; chart değiştiyse version/appVersion bump + lint/template; OTel alanı değiştiyse `/otel-conventions` + golden; yeni `/api/*` → kendi dosyası (`api.go` +1 satır); etkilenen alan (CODEOWNERS); canlı doğrulama (hostname'ler maskeli).
- **CONTRIBUTING.md:** sürüm modeli (tag = changelog, "1'e geçmeyelim"), yerel kurulum (Go ≥ go.mod, Node 22, compose vs minikube, **`frontend/dist` `//go:embed` ön koşulu**), commit/tag formatı + trailer, kapılar ve **CI'ın UTC'de koştuğu**, hard constraint özeti + `scripts/audit.sh`, bugfix = regresyon + vX.Y.Z, skill akışları, frontend ev kuralları, INCIDENTS/audit dokümanı, **müşteri adı/hostname hiçbir dosyaya yazılmaz** politikası.
- **SECURITY.md:** desteklenen sürüm = en yeni `v0.10.X`; Private Vulnerability Reporting; kapsam (OTLP ingest, auth/RBAC/LDAP/OIDC, Admin SQL, MCP sunucusu, AI sağlayıcı sırları); tarayıcılar (govulncheck, Trivy, CodeQL, push-protection); `.trivyignore`/`.auditignore` gerekçeleri; SLA critical → aynı gün.
- **Issue şablonları:** `bug_report.yml` (operator-reported?, sayfa/uç, `/api/version` build+overridden, `COREMETRY_MODE`, chart sürümü, CH tek/dağıtık, log backend, ekran görüntüsü maskeli), `feature_request.yml` (3+ dosya → `/spec`, alan, audit referansı, ölçek kısıtı), `perf_regression.yml` (sayfa, TTFB, `X-Cache`, `query_log read_rows`, perfcheck JSON), `config.yml` `blank_issues_enabled: false`.
- **.editorconfig:** lf/utf-8/final newline; Go tab; ts/tsx/json/yml 2 boşluk, 100 sütun; Makefile tab; md trailing ws korunur. **.gitattributes:** `* text=auto eol=lf`; `*.png binary`; `package-lock.json`/`go.sum` `linguist-generated`; `migrations/*.sql linguist-language=SQL`; `*.patch -text`.
- **CHANGELOG:** tag konvansiyonu kalabilir; eksik olan annotated tag (`git tag -a`) ve PR'sız ortamda boş Release notu; `git-cliff` ile her `v0.10.X0`'da toplu üretim.
- **Branch protection (ruleset `protectd` etkin):** required = Frontend, Backend, Security, Helm job'ları; sonra Lint; CODEOWNERS review `bypass_actors` ile admin'e açık; `delete_branch_on_merge: true`.

### 5.4 CODEOWNERS türetimi
Ağaç: `internal/` 51 paket / 1731 Go dosyası; `internal/api` 508 dosya, domain'ler dosya öneki ile ayrışıyor (`copilot_` 16, `chat_` 15, `ai_` 10, `admin_` 9 …); `internal/chstore` 580 (trace* 52, metric* 33, exception* 16 …); `frontend/src` 1188 (components/ui 55, pages/settings 59, pages/service 56, pages/explore 47). `internal/sampling` ve `deploy/` YOK.

| Alan | Yollar |
|---|---|
| frontend | `frontend/src/**` (+ alt alanlar aşağıda) |
| chart layer | `components/chart` (CorePanel), `components/charts`, `components/viz`, `lib/chart` (67) |
| OTLP ingest | `internal/otlp` 22, `internal/pipeline` 4, `internal/consumer` 2, `internal/correlator` 11, `internal/profileconv`, `internal/stackparse` 8, `otel-collector-config*.yaml` |
| storage | `internal/chstore` 580, `internal/logstore` 69, `internal/chmigrate`, `internal/cluster`, `internal/acache`, `internal/cache`, `internal/appschema`, `migrations/` 19, `docs/SCHEMA.md`, `docs/rollup-design.md` |
| metrics | `internal/vmetrics` 28, `internal/promql` 7, `internal/promapi`, `internal/api/metric*` 12, `vmetrics*`, FE `pages/Metrics.tsx`, `MetricsExplorer.tsx`, `lib/metric*.ts` |
| CoSRE / AI | `internal/copilot` 27, `internal/ai` 44, `internal/mcp` 9, `internal/mcptools` 60, `internal/mcpclient` 8, `internal/rag` 9, `internal/rca` 4, `internal/promptfmt`, `internal/agent`, `internal/api/{ai_,copilot_,chat_,mcp_,rca,rag,explain,guided}*`, FE `components/ai` 57, `pages/ai`, `CopilotChat.tsx` …, `docs/cosre-*.md`, `docs/evals` |
| anomaly / alerts | `internal/anomaly` 69, `internal/evaluator` 52, `internal/notify` 21, `internal/watcher`, `internal/monitor`, `internal/templater`, `internal/api/{anomaly,alert,problem,inbox,slo,watcher,runbook}*` 50, FE `features/anomalies` 37, `pages/alerts` 13 |
| integrations | `internal/oracle` 8, `internal/devops` 28, `internal/tempo`, `internal/thanos` 13, `internal/entity` 18, `internal/rollout` 10, `internal/elasticml`, `internal/dql`, `internal/logql`, `internal/api/{oracle,devops,thanos,tempo,kibana,entity,rollout}*` 29 |
| auth / security | `internal/auth` 10, `internal/ldap` 10, `internal/secretref`, `internal/reqid`, `internal/api/admin*` 12, `ldap*`, FE `AuthProvider.tsx`, `Login/Users/Admin*`, `SECURITY.md`, `.trivyignore` |
| helm / ops | `charts/coremetry/**` 22, `Dockerfile`, `docker-compose*.yml`, `values-minikube.yaml`, `otel-collector-config*.yaml`, `examples/openshift/**`, `scripts/**`, `Makefile`, `.github/workflows/**`, `docs/{ha,operator,runbooks}`, `docs/clickhouse-cluster.md` |
| perf / demo | `cmd/demo` 14, `cmd/perfcheck`, `cmd/paritycheck`, `internal/perfcheck`, `java-demo/`, `jboss-demo/` 41, `perf/`, `scripts/perf/`, `docs/perf`, `docs/DEMO-REALISM.md` |
| docs | `docs/**` 142, `README.md`, `CLAUDE.md`, `.claude/skills/**` |
| core | `main.go`, `go.mod/sum`, `internal/config`, `internal/selfobs`, `internal/sse`, `internal/api/api.go`, `route_registry.go` |

Önerilen `.github/CODEOWNERS` (son eşleşen satır kazanır; `@org/<takım>` yer tutucu):

```
*                                               @org/core

/frontend/                                      @org/frontend
/frontend/src/components/chart/                 @org/frontend @org/chart-layer
/frontend/src/components/charts/                @org/frontend @org/chart-layer
/frontend/src/components/viz/                   @org/frontend @org/chart-layer
/frontend/src/lib/chart/                        @org/frontend @org/chart-layer
/frontend/src/components/ai/                    @org/frontend @org/cosre-ai
/frontend/src/pages/ai/                         @org/frontend @org/cosre-ai
/frontend/src/components/CopilotChat.tsx        @org/frontend @org/cosre-ai
/frontend/src/components/CopilotExplain.tsx     @org/frontend @org/cosre-ai
/frontend/src/lib/chat*.ts                      @org/frontend @org/cosre-ai
/frontend/src/lib/ai*.ts                        @org/frontend @org/cosre-ai
/frontend/src/features/anomalies/               @org/frontend @org/anomaly-alerts
/frontend/src/pages/alerts/                     @org/frontend @org/anomaly-alerts
/frontend/src/pages/Metrics.tsx                 @org/frontend @org/metrics
/frontend/src/lib/metric*.ts                    @org/frontend @org/metrics
/frontend/src/components/AuthProvider.tsx       @org/frontend @org/security

/internal/otlp/                                 @org/otlp
/internal/pipeline/                             @org/otlp
/internal/consumer/                             @org/otlp
/internal/correlator/                           @org/otlp
/internal/profileconv/                          @org/otlp
/internal/stackparse/                           @org/otlp
/otel-collector-config*.yaml                    @org/otlp @org/helm-ops

/internal/chstore/                              @org/storage
/internal/logstore/                             @org/storage
/internal/chmigrate/                            @org/storage
/internal/cluster/                              @org/storage
/internal/acache/                               @org/storage
/internal/cache/                                @org/storage
/internal/appschema/                            @org/storage
/migrations/                                    @org/storage
/docs/SCHEMA.md                                 @org/storage
/docs/rollup-design.md                          @org/storage

/internal/vmetrics/                             @org/metrics
/internal/promql/                               @org/metrics
/internal/promapi/                              @org/metrics
/internal/api/metric*.go                        @org/metrics
/internal/api/vmetrics*.go                      @org/metrics
/internal/chstore/metric*.go                    @org/storage @org/metrics

/internal/copilot/                              @org/cosre-ai
/internal/ai/                                   @org/cosre-ai
/internal/mcp/                                  @org/cosre-ai
/internal/mcptools/                             @org/cosre-ai
/internal/mcpclient/                            @org/cosre-ai
/internal/rag/                                  @org/cosre-ai
/internal/rca/                                  @org/cosre-ai
/internal/promptfmt/                            @org/cosre-ai
/internal/agent/                                @org/cosre-ai
/internal/api/ai_*.go                           @org/cosre-ai
/internal/api/copilot_*.go                      @org/cosre-ai
/internal/api/chat_*.go                         @org/cosre-ai
/internal/api/mcp_*.go                          @org/cosre-ai
/internal/api/rca*.go                           @org/cosre-ai
/internal/api/rag*.go                           @org/cosre-ai
/internal/api/explain*.go                       @org/cosre-ai
/internal/api/guided*.go                        @org/cosre-ai
/docs/cosre-*.md                                @org/cosre-ai
/docs/evals/                                    @org/cosre-ai

/internal/anomaly/                              @org/anomaly-alerts
/internal/evaluator/                            @org/anomaly-alerts
/internal/notify/                               @org/anomaly-alerts
/internal/watcher/                              @org/anomaly-alerts
/internal/monitor/                              @org/anomaly-alerts
/internal/templater/                            @org/anomaly-alerts
/internal/api/anomaly*.go                       @org/anomaly-alerts
/internal/api/alert*.go                         @org/anomaly-alerts
/internal/api/problem*.go                       @org/anomaly-alerts
/internal/api/inbox*.go                         @org/anomaly-alerts
/internal/api/slo*.go                           @org/anomaly-alerts
/internal/api/watcher*.go                       @org/anomaly-alerts
/internal/api/runbook*.go                       @org/anomaly-alerts
/internal/chstore/anomaly*.go                   @org/storage @org/anomaly-alerts
/internal/chstore/alert*.go                     @org/storage @org/anomaly-alerts

/internal/oracle/                               @org/integrations
/internal/devops/                               @org/integrations
/internal/tempo/                                @org/integrations
/internal/thanos/                               @org/integrations
/internal/entity/                               @org/integrations
/internal/rollout/                              @org/integrations
/internal/elasticml/                            @org/integrations
/internal/dql/                                  @org/integrations
/internal/logql/                                @org/integrations
/internal/api/oracle*.go                        @org/integrations
/internal/api/devops*.go                        @org/integrations
/internal/api/thanos*.go                        @org/integrations
/internal/api/tempo*.go                         @org/integrations
/internal/api/kibana*.go                        @org/integrations
/internal/api/entity*.go                        @org/integrations
/internal/api/rollout*.go                       @org/integrations

/internal/auth/                                 @org/security
/internal/ldap/                                 @org/security
/internal/secretref/                            @org/security
/internal/reqid/                                @org/security
/internal/api/admin*.go                         @org/security
/internal/api/ldap*.go                          @org/security
/SECURITY.md                                    @org/security
/.trivyignore                                   @org/security
/.github/dependabot.yml                         @org/security @org/helm-ops

/charts/                                        @org/helm-ops
/examples/openshift/                            @org/helm-ops
/Dockerfile                                     @org/helm-ops
/cmd/demo/Dockerfile                            @org/helm-ops
/docker-compose*.yml                            @org/helm-ops
/values-minikube.yaml                           @org/helm-ops
/scripts/                                       @org/helm-ops
/Makefile                                       @org/helm-ops
/.github/workflows/                             @org/helm-ops @org/core
/docs/ha/                                       @org/helm-ops
/docs/operator/                                 @org/helm-ops
/docs/runbooks/                                 @org/helm-ops
/docs/clickhouse-cluster.md                     @org/helm-ops @org/storage

/cmd/demo/                                      @org/perf-demo
/cmd/perfcheck/                                 @org/perf-demo
/cmd/paritycheck/                               @org/perf-demo
/internal/perfcheck/                            @org/perf-demo
/java-demo/                                     @org/perf-demo
/jboss-demo/                                    @org/perf-demo
/perf/                                          @org/perf-demo
/scripts/perf/                                  @org/perf-demo
/docs/perf/                                     @org/perf-demo

/docs/                                          @org/docs
/README.md                                      @org/docs
/CLAUDE.md                                      @org/core @org/docs
/.claude/skills/                                @org/core @org/docs
/.github/CODEOWNERS                             @org/core
/go.mod                                         @org/core
/go.sum                                         @org/core
/main.go                                        @org/core
/internal/api/api.go                            @org/core
/internal/api/route_registry.go                 @org/core
/internal/config/                               @org/core
```
Glob'lar `internal/api` dosya-öneki konvansiyonuna dayanır; `/api-route` kuralı ("her domain kendi `<domain>.go`") sürdükçe CODEOWNERS bakımsız kalmaz.

---

## 6. Onboarding ihtiyacı

### 6.1 Dört AI paketinin sorumluluk haritası

| Paket | Amaç | Kanıt |
|---|---|---|
| `internal/mcp` | Coremetry'nin **kendi** MCP sunucusu (gelen yön): JSON-RPC 2.0, HTTP+SSE ve Streamable-HTTP, oturum, `tools/resources/prompts` defterleri, `CallGate`. Depoya bağımsız (hiçbir `internal/*` import etmez), 4 dosya. | `mcp.go:1-64`, `:239-290` (`Tool.MinRole`, `ShortDescription`), `:480`, `:698/749/826` |
| `internal/mcptools` | Sunucuya kaydedilen 56 tool + resource + prompt kataloğu (`Deps{Store, LogStore, …}`), `range_s`/`clampLimit` sözleşmesi. AYRICA guided sohbetin ortak veri okuma katmanı (`ReadDBHealth`, `ReadMessagingHealth`, `ReadPodHealth`) — ad "tools" ama aynı zamanda read-layer. | `tools.go:1-60, :182-200, :260, :387`; `api/mcp_deps.go:1-15` |
| `internal/mcpclient` | **Dış** MCP sunucularının istemcisi (giden yön): `ServerConfig` izin listesi, stdio/HTTP, `Registry` (5 dk TTL katalog, `<sunucu>__<tool>` öneki), `Service` (system_settings, 30 s refresh, sırsız Snapshot, `Test`). JSON-RPC şekilleri bilinçli kopya. | `mcpclient.go:1-18`, `registry.go:12-31`, `settings.go:12-22` |
| `internal/copilot` | LLM runtime: `Service` (provider/model/anahtar/kota/profil), `Explain`, `StreamText`, `ChatWithTools`, `Recorder` (ai_calls). TÜM sistem prompt'ları `prompts.go` (1634 satır). Provider gövdeleri `internal/ai/provider`'a taşındı; tasarım belgesi "dondurulmuş, emekliye" der ama hâlâ ana runtime. | `copilot.go:1-28, :697`, `chat.go:40-67`, `prompts.go:1-20`; `docs/plans/ai-assistant-design-2026-08-16.md:285-330` |

Import yönü (grep ile doğrulandı): `api → mcptools → {chstore, mcp}`; `mcptools` asla `api`'yi, `copilot` asla `chstore`'u import etmez (`Recorder` adaptörü `main.go:2034-2044`'te); `mcp` ↔ `mcpclient` paylaşım yok. Boot: `copilot.New` :919 → `LoadPersisted` :934 → `mcpclient.NewService` :1184 → `api.NewServer` :1253 → `mcp.New` + `mcptools.Register` :1340-1353 (yalnız `mode.api`) → `SetMCP` :1353 → `SetMCPClient` :1369.

Üç akış: (a) UI sohbet → `POST /api/copilot/chat` (`ai_routes.go:105`) → guided router (`copilot_guided.go:1-40`) ya da serbest döngü `ChatWithTools` (`copilot_chat.go:482-491`) → `mcp.ToolHandler` → chstore; SSE + `ai_calls`. (b) Dış LLM → `/api/mcp/sse|messages|` streamable (`api.go:708-714`) → `CallGate` (rol, 60/dk) → `mcptools` handler → MV; `ai_calls` YAZILMAZ (`mcp_observe.go:18-19`). (c) Operatör-tanımlı dış MCP: `PUT /api/settings/mcp-servers` → `Registry.Configure` → `chat_mcp_bridge.go:90-134` dış tool'u `mcp.Tool{MinRole: viewer}` olarak sarar (deny kazanır, `mcp.call` audit) → `Registry.Call` (15 s tavan).

### 6.2 En kafa karıştırıcı 5 adlandırma çakışması

| # | Çakışma | Nerede | Öneri |
|---|---|---|---|
| 1 | Üç "Tool" tipi + üç "tools" dosyası/paketi (`mcp.Tool`, `provider.ToolSpec`, `copilot.ToolSpec` takma ad; `mcptools/tools.go`, `ai/provider/tools.go`, paket `ai/agent/tools`) | `mcp.go:239`, `ai/provider/tools.go:36`, `copilot/chat.go:40`, `ai/agent/tools/executor.go:1` | `ai/agent/tools` → `toolexec`; `copilot.ToolSpec` `Deprecated:` |
| 2 | "prompts" üç yerde: `copilot/prompts.go` (sistem), `mcptools/prompts.go` (MCP `prompts/get`), `ai/insight/prompt.go` (kullanıcı) | ilgili dosya başlıkları | `mcp_prompt_catalog.go`, `user_prompt.go`; CLAUDE.md'ye prompt taksonomisi satırı |
| 3 | Explain ailesi: `copilot.Service.Explain` ≠ `s.copilotExplain` (+4 kardeş) ≠ 17 `copilotExplainX` handler ≠ `anomaly.*Explainer` ≠ MCP prompt `explain_trace` ≠ `explain_*.go` | `copilot.go:697`, `ai_observability.go:164-191`, `ai_routes.go:109-115` | Sarmalayıcı `s.aiCall*`, handler'lar `handleExplainX`; üç-katman şeması |
| 4 | `copilot` vs `ai` vs "CoSRE": tasarım "copilot emekli" der, hâlâ runtime; api önekleri `copilot_*` 13, `chat_*` 12, `ai_*` 8; "CoSRE" 215 dosyada geçer, paket adı yok | `docs/plans/ai-assistant-design-2026-08-16.md:285-330` | Paket doc'una "legacy ad"; tek önek kararı (`ai_`); README haritası |
| 5 | `copilot_deps.go` DI DEĞİL (bağımlılık sağlığı bundle'ı), `mcp_deps.go` gerçek DI; kurucular tutarsız; `chmigrate` migration değil veri kopyalayıcı | `mcp_deps.go:1-15`, `copilot_deps.go:1-8`, `chmigrate/migrate.go:1-4` | `guided_dependency_health.go`; `mcpclient` → `mcpext`; `chmigrate` → `chcopy` |

### 6.3 Yeni geliştirici nerede takılır

| Alan | Bulgu | Kanıt |
|---|---|---|
| Yerel kurulum | `make dev` yok; taze klonda `go build ./...` **`//go:embed all:frontend/dist`** yüzünden `frontend/dist` olmadan patlar; README önce `make build-ui` demiyor (CI biliyor: `ci.yml:74-82`). "Yalnız CH'yi kaldır" tarifi yok. Redis boşsa lider kilidi/cache sessizce yok. | `main.go:60`, `.gitignore:21`, `README.md:467-489`, `config.yaml:6,39` |
| Env değişkenleri | Depoda **110 benzersiz** `COREMETRY_*`; README'de 2, chart yorumlarında 23. Tek referans belgesi yok. Varsayılansız/belgesiz: `COREMETRY_JWT_SECRET` (yoksa her restart'ta üretilir → oturumlar düşer), `REDIS_URL`, `SELF_OBS_OTLP_ENDPOINT`, `ACACHE_*`, `SKIP_MIGRATE`, `PUBLIC_URL`, `LOGS_PATTERNS_DEADLINE`, `TRUSTED_HEADER_*`. | `internal/config/config.go:200-205, 555-600`, `main.go:117,189,264,292` |
| Demolar | `cmd/demo` kullanımı yalnız dosya başlığında; `jboss-demo` README'siz; depo kökünde 17 MB `demo` arm64 binary'si; 14318 (collector) vs 8088 ayrımı belgesiz; `java-demo/` yalnız `target/`. | `cmd/demo/main.go:13-16`, `Makefile:23,149,238` |
| Release | Yalnız CLAUDE.md + `/release`; README'de sıfır bahis. Pratik `v0.10.X — type(scope): başlık` ama CLAUDE.md:91 `type(scope)` demiyor; trailer sürümü farklı. 3477 tag. | `CLAUDE.md:82-93`, `Makefile:26-29` |
| Frontend kuralları | Yalnız `.claude/skills`'te → Claude Code kullanmayan geliştirici görmez; `features/README.md` hedef yapıyı anlatır ama yalnız 2 feature göçmüş, 119 sayfa `pages/`'te; `frontend/README.md` yok. | `frontend/src/features/README.md` |
| CH şema evrimi | `migrate()` (~1500 satır DDL), iki-boot sözleşmesi, `migrations/` kimlik numaraları, `chmigrate` yanıltıcı ad — tek düzgün anlatım `clickhouse-schema` SKILL :365-390; `docs/SCHEMA.md` yalnız kolon kuralları. | `store.go:1854, :3775-3783`, `migrations/embed.go:1-30` |
| Devasa dosyalar | `api.go` 12113 (286 fonksiyon, 336 route), `repo.go` 5721, `store.go` 4748, `copilot_guided.go` 3745; FE `types.ts` 7423, `api.ts` 3957, `AdminClickhouse.tsx` 3502. | `wc -l` |
| Test kuralları | 996 test dosyasının 982'si `vX.Y.Z` anıyor; 190 dosya Go kaynağını okuyup desen pinliyor; "pin test", "kaynak-pin", "mutation check" hiçbir `docs/*.md`'de tanımlı değil. | `ai_routes_test.go:10-20`, `evaluator/selfhealth_test.go:8` |
| Dil | Türkçe yorum içeren dosya: `internal/` %76 (540/714), testler %60, `frontend/src` %72; 4 AI paketi ~%98. "Neden" bilgisi yorumlarda → Türkçe okumayan geliştirici "ne"yi görür, "neden"i kaybeder. | örneklem grep (`için`, `yalnız`, `değil`, `çünkü`, `artık`) |

### 6.4 "Codebase turu" skill'i içerik planı (`.claude/skills/codebase-tour/SKILL.md`, tasarım)

**Sıralı okuma (25):** `CLAUDE.md` · `README.md:137-156, 332-366, 467-489` · `Makefile:15-60, 108-160, 194-247` · `main.go:100-130, 905-960, 1175-1210, 1320-1370` · `internal/config/config.go:542-600` · `chstore/store.go:1854-1900, 3775-3790` · `chstore/problem.go:95-140, 1437` · `api/api.go:63, 450, 600` (gez) · `api/route_registry.go:1-20` · `api/cache.go:218` + `cache_key_test.go:1-25` · `api/anomaly_extra.go:275` (`s.audit`) · `api/ai_routes.go:1-60` · `api/ai_observability.go:140-200` · `copilot/copilot.go:1-60` + `prompts.go:1-20` · `copilot/chat.go:1-70` · `ai/provider/tools.go:1-60` · `mcp/mcp.go:1-64, 239-290` · `mcptools/tools.go:1-200` + `api/mcp_deps.go` · `api/copilot_chat.go:30-60, 318-375, 482-495` · `api/copilot_guided.go:1-40` · `mcpclient/mcpclient.go:1-18` + `api/chat_mcp_bridge.go:1-30` · `frontend/src/App.tsx` + `Sidebar.tsx` · `lib/types.ts` + `lib/api.ts` (yapı) · `ui/DataTable/DataTable.tsx:1-25` + `pages/SlowQueries.tsx` · `chart/CorePanel.tsx:1-25` + `lib/useUrlRange.ts:1-10`. Sonra `docs/INCIDENTS.md`, `docs/DECISIONS.md`, clickhouse-schema SKILL :365-390, `docs/runbooks/mcp-claude-code.md`.

**7 invariant** (CLAUDE.md:43-51) + üç "gölge invariant": hash-all-inputs cache key (:16), `copilotExplain` sarmalayıcı (:64-65), api.go'ya rota eklenmez (:202).

**INCIDENTS'tan 5 tekrarlayan bug sınıfı:** (1) CH sorgu şekli / yanlış düzey (WHERE span vs HAVING trace, olmayan kolon adı — v0.10.341/.362/.611); (2) MV/şema evrimi tuzakları (combined-MV drop, `quantilesState`, iki-boot — v0.10.339/.355); (3) FE tablo kırpma / sanal liste (v0.10.338/.357); (4) FE refetch/polling/URL-state (`timeRangeToNs` JSX, `document.hidden`, tek yönlü URL); (5) cache anahtarı + okuma-yolu sürüklenmesi (`len(set)`, CH↔VM kural asimetrisi v0.10.367/.370/.373).

**İlk 3 görev:** (1) okuma ucu `registerRoutesExtra` + `serveCached` + types/api.ts (`/api-route`, `TestMuxRoutePatterns`); (2) DataTable kolonu (`SlowQueries.tsx` şablonu, `/frontend-conventions`); (3) Kafka katalog metriği (`vmetrics/kafka.go:49` `KafkaCatalog`, `/tdd`). Bonus: `/mcp-tools` ile bir tool (`ShortDescription` zorunlu).

**Sözlük:** Problem · kind=external · `ext:` özne · MV-first · rollup DAR/GENİŞ · saved_views · system_settings · PollerOwnedRule · CoSRE · seam · pin test / kaynak-pin · mutation check · kuyruk · iki-boot · range_s · cmk_ token · guided vs serbest döngü · ShortDescription — ve Türkçe yorum sözlüğü (kapı=gate, çivi/pin, dilim=slice, yüzey=surface, tavan=cap, özne=subject, sözleşme=contract, emsal=template, defter=registry).

**`/where-is` farkı:** where-is bir kavram için ≤7 `file:line` işaretçisi döndürür (lookup, her sorguda); tour müfredattır ("neden böyle / neyi bozmamalıyım / önce ne okuyayım"), tek seferlik, iş yapılacaksa ilgili skill'e yönlendirir, kod yazmaz.

---

## 7. Organizasyona taşıma planı (operatör 2026-09-10: repo bir GitHub organizasyonuna taşınacak)

Sıra (her adım bir öncekine bağlı):
1. **Ağaç temizliği** (§1.1–1.6 sentetikleştirme, §1.7 `values-minikube.yaml` literal'ı) — `.gitignore`'a `.mcp.json`, `.claude/agent-memory/`, `scratchpad/`; kök manifestleri repo dışına.
2. **History rewrite** (`git filter-repo --replace-text`, §1.8) — henüz fork/klon yokken en ucuz an; tag'ler yeniden yazılır, `git push --force --tags` + tüm klonlar yenilenir.
3. **Transfer** (Settings → Transfer ownership → org). GitHub eski URL'yi yönlendirir; aşağıdakiler yine güncellenir:
   - `go.mod` modül yolu `github.com/cilcenk/coremetry` → org yolu: her Go import'unda geçer (tek seferlik mekanik ama geniş commit; yönlendirme `go get`i yaşatır ama kanonik yol değişmeli).
   - `release.yml` / `helm-chart-coremetry` skill'i `ghcr.io/cilcenk/…` → `ghcr.io/<org>/…`; ghcr paketleri repo ile taşınmayabilir, doğrulanacak.
   - `charts/coremetry/Chart.yaml` (home/sources/maintainer), README klon/`go install` satırları, `docs/` içindeki repo URL'leri.
4. **Org yetenekleri**: CODEOWNERS `@org/<takım>` handle'ları (§5), rulesets/branch protection (required checks — bugün hepsi advisory, §4), secret scanning + push protection (private repoda GHAS lisansı; public'te ücretsiz), Dependabot.
5. **Ekip daveti** ve `.claude/` ekip sürümü (§2 A5/A7/A13; `settings.local.json` paylaşılmaz).

---

## 8. Etki / efor matrisi ve "GitHub'a açmadan önce mutlaka" listesi

Etki: Y (yayın/güvenlik engeli) · O (ekip verimliliği) · D (kozmetik). Efor: S ≤ 2 saat · M ≤ 1 gün · L > 1 gün.

| # | İş | Etki | Efor | Bölüm |
|---|---|---|---|---|
| 1 | Ağaç sentetikleştirme: IP/FQDN fixture'ları (1.1), Oracle şema/kolon varsayılanlarını config'e (1.2), fraud tablosu fixture'ı (1.2), Influx docs (1.3), 82 `kurum-öneki*` + `ocp*` fixture'ları (1.4), `BSA_0xx`/kanal kodları/`DEFAULT_TRACE_COLUMNS` (1.5), LDAP fixture'ı (1.6) | **Y** | M-L (~1-1.5 gün; 100+ dosya ama mekanik) | §1 |
| 2 | Kök manifestler + `.env` repo dışına; `.gitignore`: `.mcp.json`, `.claude/agent-memory/`, `scratchpad/`; `scratchpad/exc-pods`, `copilot-tools` sil; `values-minikube.yaml` JWT literal'ı → env | **Y** | S | §1.1, 1.7, §2 |
| 3 | History rewrite (`git filter-repo --replace-text`; 3477 tag; klonlar yenilenir) — 1'den SONRA | **Y** | M | §1.8 |
| 4 | CI yeşil: vitest TZ düzeltmesi (v0.10.613), `js-yaml`/`browserslist`, `x/crypto`+`grpc` bump | **Y** | S-M | §4.4 #3-5 |
| 5 | Ruleset `protectd` etkin + required checks (Frontend/Backend/Security/Helm) + `release.yml` `needs: ci` + Dependabot alerts/security updates aç | **Y** | S | §4.1, §5.1 |
| 6 | `SECURITY.md` + Private Vulnerability Reporting; CodeQL 11 critical triyajı (`go/request-forgery` ×5, `go/unsafe-quoting` ×4, `go/email-injection`, `go/command-injection`) | **Y** | M-L | §4.3, §5.3 |
| 7 | CONTRIBUTING, CODEOWNERS, PR/issue şablonları, .editorconfig, .gitattributes; Mayıs Dependabot PR'larını kapat | O | M | §5.3-5.4 |
| 8 | CLAUDE.md ekip sürümü: memory atıfları, `kuyruk` çıkar, deploy → docs, ikinci H1 eritme, B3 (VM/çift yazım) gerçeğini yaz; agent dosyası link versin; copilot-surface/spec/tdd "api.go'ya route" düzelt; `/kuyruk` kişisel skills'e | O | M | §2.2-2.4 |
| 9 | api.go ratchet: `.claude/baselines/api_go_lines`=12113 + `api_go_size_test.go` + hook + CI adımı | O | S | §3.1 |
| 10 | go.mod `toolchain` + üç workflow `go-version-file`; gofmt sweep (83 dosya) + CI kapısı; `go mod tidy`; `make audit` CI'a | O | S | §3.2, §4.4 #6-8 |
| 11 | gitleaks pre-commit + CI + repo'ya özel kural seti + allowlist | O (yayın sonrası tekrar sızıntı önleyici) | M | §3.3 |
| 12 | golangci 123 → 0 (SA4000 ×13 determinizm iddiası → 634, gerçek hata değildi), ESLint hard; prettier sweep (1042 dosya) | O | L | §4.4 #10-11 |
| 13 | Onboarding: `docs/local-dev.md` (dist ön koşulu, compose/minikube, portlar), `docs/ENV.md` (110 değişken), `codebase-tour` skill'i, test kültürü sözlüğü, DECISIONS v0.7→v0.10, adlandırma çakışmaları (§6.2) | O | L | §6 |
| 14 | Org transferi + modül yolu + ghcr ad alanı + Chart/README URL'leri | O | M | §7 |
| 15 | Kök ikili/artefakt temizliği; annotated tag'ler; `release.yml` Release notu | D | S | §5.1 |

**GitHub'a (org altında, ekibe/dışa) açmadan ÖNCE mutlaka:** **1 → 2 → 3** (sızıntı ve geçmiş; sıra değişmez), **4 → 5** (yeşil CI ve zorunlu kapılar; aksi hâlde ilk PR'lar kırmızı bir main üzerine düşer ve `release.yml` kırık kodu yayınlar), **6** (SECURITY.md + critical CodeQL triyajı: public repoda alert'ler dışarıdan görünmez ama kod görünür), **2'deki `.gitignore` ekleri** (`.mcp.json` mutlak yol + `enableAllProjectMcpServers`, agent-memory 920K kişisel not). 7-9 ilk ekip üyesi gelmeden; 10-15 ilk sprint.

Repo dışında operatöre teslim: `leak-raw.txt` (1524 satır, maskesiz) — bu dokümana ve repoya asla kopyalanmaz.

## 9. Durum — 2026-09-10 (v0.10.630 itibarıyla)

§8 matrisinin satır satır durumu. "Operatör" = karar/işlem operatörde; kod tarafı bitti.

| # | İş | Durum |
|---|---|---|
| 1 | Ağaç sentetikleştirme | **GEMİDE** 618 (IP/FQDN), 619 (LDAP fixture), 620 (kurum öneki→`shop-`, cluster adları, 52 dosya), 621 (fraud fixture, Influx kalıntıları). Üç davranışsal varsayılan **GEMİDE 641**: Oracle sütunları jenerik `ERR_*`, Traces varsayılan kolonları `http.method/http.route/deployment.environment`, devops öneki boş (kurum değerleri Settings'ten; prod'da açıkça kaydedilmeli — deploy ön koşulu) |
| 2 | Kök manifestler + `.gitignore` | **GEMİDE** 618 (`.mcp.json`, `.claude/agent-memory/`, `scratchpad/`); kök compose/collector/tempo/minikube yaml'ları sentetik, ağaçta kalıyor |
| 3 | History rewrite | **Operatör** — zamanlama (force-push + 3477 tag; klonlar yenilenir) |
| 4 | CI yeşil | **GEMİDE** 614 (vitest TZ), 615 (js-yaml/browserslist), 616 (x/crypto, grpc); `main` 628'den beri 5/5 yeşil |
| 5 | Ruleset + required checks + release gate + Dependabot | **KISMİ**: release.yml `gate` job'ı 626 (docker/helm `needs: [gate]`); Dependabot alerts/security updates açık (624). Ruleset `protectd` etkinleştirme + required checks **operatör** (doğrudan push'u keser → PR akışı kararı) |
| 6 | SECURITY.md + PVR + CodeQL triyajı | **KISMİ**: SECURITY.md + Private Vulnerability Reporting 624. CodeQL açık 40: 2 critical + 5 `go/request-forgery` (webhook/Zoom/VM/Prom/devops URL'leri yönetici ayarından — by design), 5 `go/clear-text-logging` (bind-hata mesajı ve trusted-header e-postası; parola akmıyor), 2 `go/path-injection` (LDAP CA/parola dosyası yolu yönetici ayarından), 12 `go/incorrect-integer-conversion`, 12 `js/incomplete-sanitization` — kapatma gerekçeleri hazır, "by design" kapatma **operatör** |
| 7 | Katkı altyapısı | **GEMİDE** 623 (PR/issue şablonları, CODEOWNERS, .editorconfig, .gitattributes), 624 (CONTRIBUTING, SECURITY; eski Dependabot PR'ları kapatıldı) |
| 8 | CLAUDE.md ekip sürümü | **Operatör** — sahibinin dosyası; kapsam/üslup kararı |
| 9 | api.go ratchet | **GEMİDE** 625 (taban 12113, test, hook, CI adımı) |
| 10 | gofmt sweep + tidy + make audit + toolchain | **GEMİDE** 626 (`make audit`, `go mod tidy -diff`, CGO=0), 628 (86 dosya gofmt + CI kapısı). `go.mod` `toolchain` pini **operatör** (sorulmadan eklenmez) |
| 11 | gitleaks | **AÇIK** — yayın sonrası önleyici; ayrı dilim |
| 12 | golangci / prettier / ESLint hard | **golangci GEMİDE 634-639**: tavansız gerçek toplam 143 ("123" golangci'nin 50/3 tavanıyla kırpılmış sayıydı) → 0: SA4000 ×13 determinizm iddiası (634, gerçek hata ÇIKMADI), unused 26 (636), errcheck 60 politika+6 üretim kalemi (637), staticcheck 74 + govet 6 + ineffassign 2 (638), CI lint **gerçek kapı** tavansız (639). ESLint gerçek kapı 627. prettier 1042 dosya **AÇIK** (mekanik, sessiz pencere ister — operatör) |
| 13 | Onboarding | **GEMİDE** 629 (docs/ENV.md 89 değişken, docs/local-dev.md), 630 (`/codebase-tour` skill + test kültürü sözlüğü), 631 (README bağlantıları + `make build-ui` ön koşulu) |
| 14 | Org transferi + modül yolu + ghcr + URL'ler | **KISMİ**: org `cosretr` + tüm URL/imaj referansları 617; imaj paketi public (operatör). Kalan **operatör**: `go.mod` modül yolu, `charts/coremetry` paketinin public yapılması |
| 15 | Kök artefakt temizliği, annotated tag, Release notu | **AÇIK** (D) |

Bağımlılık: Dependabot 8 orta → 2 (632 go-ntlmssp/react-router-dom/vitest/postcss; 633 tarayıcı OTel seti 2.11/0.222). Kalan 2 = react-router v6 aralığı, düzeltme 7.18.3 major (router göçü) — **operatör**. Açık Dependabot PR'ları: #46 Go 21 paket (dep bump disiplini → operatör), #44 npm grubu (632/633 sonrası bayat), #41 actions v7 (**operatör**). docs/DECISIONS.md v0.7→v0.10.629 42 kayıt (635).

