---
name: codebase-tour
description: Coremetry'ye yeni katılan geliştirici için müfredat — okuma sırası, 7 invariant + 3 gölge invariant, tekrarlayan bug sınıfları, ilk 3 görev, sözlük (TR yorum sözlüğü dahil). "Nerede?" için /where-is, iş yapmak için /api-route, /clickhouse-schema, /tdd, /release. Bir kez okunur; kod yazmaz.
---

# /codebase-tour — Coremetry'yi ilk gün anlamak

Bu skill bir **müfredattır**: "neden böyle, neyi bozmamalıyım, önce ne okuyayım".
`/where-is` bir kavram için ≤7 `file:line` işaretçisi döndürür (lookup, her
sorguda); tour bir kez okunur, iş yapılacaksa ilgili skill'e yönlendirir, kod
yazmaz. Ölçüm tabanı v0.10.629 (2026-09-10); satır numaraları kayabilir, sembol
adları kaymaz — önce sembolü grep'le.

## 1. Sıralı okuma listesi (25 dosya, ~2 saat)

| # | Dosya | Neden |
|---|---|---|
| 1 | `CLAUDE.md` | Hard constraint'ler, 7 invariant, workflow, "What goes WHERE" |
| 2 | `CONTRIBUTING.md`, `docs/local-dev.md`, `docs/ENV.md` | Kurulum, kapılar, 89 env değişkeni |
| 3 | `README.md` (Quick start, MCP, Development) | Ürün yüzeyi, portlar |
| 4 | `Makefile` (hedef listesi) | `build-ui` → `build` sırası, `image` vs `docker-up`, `audit` |
| 5 | `main.go` (mode/roller; servis kurulumları; `mcp.New` + `mcptools.Register`) | Tek binary, `COREMETRY_MODE` ile rol, boot sırası |
| 6 | `internal/config/config.go` (`applyEnv`) | env > config.yaml > defaults |
| 7 | `internal/chstore/store.go` (`migrate()`, iki-boot sözleşmesi) | Bildirimsel/idempotent DDL, küme kipinde ertelenen ALTER |
| 8 | `internal/chstore/problem.go` (Problem, Kind, `PollerOwnedRule`) | Problem modeli, kind=service/db/external |
| 9 | `internal/api/api.go` (yalnız GEZ: Server, `registerRoutes`) | 12113 satır; ratchet var, büyütme |
| 10 | `internal/api/route_registry.go` | api.go'ya dokunmadan rota kaydı (`registerRoutesExtra`) |
| 11 | `internal/api/cache.go` (`serveCached`) + `cache_key_test.go` | Hash-all-inputs cache anahtarı; kanonik regresyon testi |
| 12 | `internal/api/anomaly_extra.go` (`s.audit`) | Admin yazımı = audit satırı |
| 13 | `internal/api/ai_routes.go` | AI rotaları + `requireCopilot`; AI ucu api.go'ya değil buraya |
| 14 | `internal/api/ai_observability.go` (`copilotExplain*`) | Tüm Explain çağrıları bu sarmalayıcıdan (ai_calls) |
| 15 | `internal/copilot/copilot.go` + `prompts.go` | LLM runtime; TÜM sistem prompt'ları prompts.go'da |
| 16 | `internal/copilot/chat.go` + `internal/ai/provider/tools.go` | Tool döngüsü; tel şekli `provider.ToolSpec` |
| 17 | `internal/mcp/mcp.go` | Kendi MCP sunucusu; `Tool.MinRole`, `ShortDescription`, `CallGate` |
| 18 | `internal/mcptools/tools.go` + `internal/api/mcp_deps.go` | 56 tool + ortak read-layer (`ReadX`); import yönü api→mcptools→chstore |
| 19 | `internal/api/copilot_chat.go`, `copilot_guided.go` | Serbest döngü vs guided (deterministik router + prefetch) |
| 20 | `internal/mcpclient/mcpclient.go` + `internal/api/chat_mcp_bridge.go` | DIŞ MCP sunucuları; deny kazanır, `mcp.call` audit |
| 21 | `frontend/src/App.tsx` + `components/Sidebar.tsx` | Rota/nav defteri |
| 22 | `frontend/src/lib/types.ts` + `lib/api.ts` (yapıyı gez) | Tek şekil kaynağı + istemci (`qs()` boşları atar) |
| 23 | `components/ui/DataTable/DataTable.tsx` + `pages/SlowQueries.tsx` | Tablo şablonu (`useDataTable`, storageKey) |
| 24 | `components/chart/CorePanel.tsx` + `lib/useUrlRange.ts` | uPlot tek motor; URL = state |
| 25 | `docs/INCIDENTS.md`, `docs/DECISIONS.md` | "Neden" bilgisi; tuzak kurallarının hikâyeleri |

Sonra: `.claude/skills/clickhouse-schema/SKILL.md` §8 (migration üç yolu),
`docs/runbooks/mcp-claude-code.md`, `docs/audit/team-readiness-audit.md` §6.

## 2. İçselleştirilecek invariant'lar (CLAUDE.md)

1. OTel gerçeğin kaynağı — attribute'lar aynen, OTLP dışı ingest yok.
2. ClickHouse sıcak depo; Elasticsearch yalnız log **okuma** backend'i.
3. **MV-first**: aggregate için ham `spans` = bug (`service_summary_5m` vb.).
4. `ReplacingMergeTree(version)` + `FINAL`; tam-satır replace (alan taşımayı unutma).
5. Kullanıcı durumu tek tablo: `saved_views(page='<kind>')`.
6. Ayarlar `system_settings` (LoadPersisted/SavePersisted; şablon `internal/tempo/client.go`).
7. Roller admin/editor/viewer — viewer GÖRÜR, boş sayfa asla.

Gölge invariant'lar (aynı derecede zorunlu): cache anahtarı TÜM girdileri
hash'ler (`len(set)` yasak); AI explain yalnız `s.copilotExplain(...)`;
`internal/api/api.go` büyümez (ratchet: `.claude/baselines/api_go_lines`);
metrikler bugün CH `metric_points` + VictoriaMetrics ÇİFT yazılır (hedef VM tek
depo; `docs/audit/vm-metrics-migration.md` Aşama 3 açık).

## 3. Tekrarlayan bug sınıfları (docs/INCIDENTS.md)

1. **CH sorgu şekli / yanlış düzey** — WHERE (span) vs HAVING (trace), olmayan
   kolon adı (`parent_span_id` yok, `parent_id` var — v0.10.611), tüm-pencere
   birleştirme. Kural: SQL yüklemi saf kurucuda + kolon adı DDL'e karşı pinli.
2. **MV / şema evrimi** — combined-MV drop sırası, `quantilesState` yerine
   `quantilesTDigestState`, tip değişiminde okuma penceresi, iki-boot probe.
3. **Frontend tablo/kırpma** — `table-layout:fixed` + `nowrap`, sanal liste ile
   deep-link çakışması.
4. **Refetch / polling / URL** — `timeRangeToNs` JSX'te (sonsuz refetch),
   `document.hidden`, tek yönlü URL okuma (state'e yazıp URL'e yazmamak).
5. **Cache anahtarı + okuma-yolu sürüklenmesi** — `len(set)` cross-poison, CH↔VM
   kural asimetrisi (dışlama kuralları, `_count` vs `_avg` adı).
6. **Saat dilimi** — testler makine saatinden beklenti kurar, CI UTC'de düşer
   (v0.10.614). `Date.UTC`/sabit dilim.

## 4. İlk 3 görev (merdiven)

1. **Okuma ucu**: `internal/api/<domain>.go` + `init(){ registerRoutesExtra(...) }`,
   `s.serveCached` + FNV anahtar, MV'den okuma, `lib/types.ts` + `lib/api.ts` +
   `lib/queries/*`; `TestMuxRoutePatterns` geçmeli. Skill: `/api-route`.
2. **DataTable kolonu**: `pages/SlowQueries.tsx` şablonu, `COLS` + `sortValue`,
   `storageKey` benzersiz. Skill: `/frontend-conventions`.
3. **Kafka katalog metriği**: `internal/vmetrics/kafka.go` `KafkaCatalog`'a
   satır (Name/Side/Kind/Unit/Agg/TR/Labels), `kafka_test.go` tablo testi.
   Skill: `/tdd`.
Bonus: `/mcp-tools` ile bir tool (`ShortDescription` zorunlu, `range_s`, `clampLimit`).

## 5. Sözlük

| Terim | Anlam |
|---|---|
| Problem | Kural/anomali kaynaklı açık durum satırı; `Kind` service/db/external (`chstore/problem.go`) |
| kind=external, `ext:` özne | Öznesi servis olmayan dış kaynak (`ext:<kaynak>/<boyutlar>`, `anomaly/external.go`) |
| MV-first | Aggregate okumalar 5 dk MV'lerden; ham `spans` yasak |
| Rollup DAR / GENİŞ | `migrations/0001` (service/kind/status + tDigest) vs `0002` (+endpoint/channel/function, 20 kova) |
| saved_views / system_settings | Kullanıcı durumu / operatör ayarı — yeni tablo AÇMA |
| PollerOwnedRule | `anomaly:ext-down:` / `ext-cap:` — yaşam döngüsü poller'da, süpürücü yalnız YAŞAYAN kaynakta muaf |
| CoSRE | Gömülü asistan (copilot runtime + `internal/ai/*` çekirdek + `api/{copilot_,chat_,ai_}*` + `CopilotChat.tsx`) |
| seam | Saf, tablo-testlenebilir sınır (SQL kurucu, karar fonksiyonu) — `/tdd` |
| pin test / kaynak-pin | Kaynağı okuyup deseni çivileyen test (190 dosya); pini SİLMEK düzeltme değil |
| mutation check | Satırı geri al → test düşmeli; yalnız derleme hatası ölçüm değildir |
| iki-boot | Yeni kolon: 1. boot ALTER ertelenir/probe false, 2. boot true |
| range_s / cmk_ token | MCP tool zaman penceresi (sn, ns değil) / rol taşıyan servis jetonu |
| guided vs serbest döngü | Deterministik intent+prefetch vs tool-calling döngüsü (`copilot_guided.go`) |
| kuyruk / devam | Operatörün öncelikli iş kuyruğu ve "sıradakine geç" komutu |

Türkçe yorum sözlüğü: kapı = gate · çivi/pin = pinned assertion · dilim = slice ·
yüzey = surface · tavan = cap · özne = subject · sözleşme = contract · emsal =
template/precedent · defter = registry · sahiplik = ownership · vida = tunable
setting · süpürücü = stale sweeper · ısırmak = (test) bite.

## 6. Bu skill'in cevapladığı sorular

"Hangi sırayla okuyayım?", "`mcp` / `mcptools` / `mcpclient` / `copilot` / `ai`
farkı?", "Yeni endpoint/kolon/metrik/tool nereye, hangi skill'le?", "Şema
değişikliği nasıl çıkar (migrate() vs migrations/ vs chmigrate)?", "Release /
commit kuralı?", "Pin/mutasyon/regresyon testi nasıl yazılır?", "Local ortam
nasıl kalkar?", "Şu Türkçe yorumdaki sözcük ne?".
