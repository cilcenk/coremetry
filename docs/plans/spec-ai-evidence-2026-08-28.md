# AI Explain — modele giden KANIT (keşif raporu + plan)

Tarih: 2026-08-28 · Taban: v0.10.111 · Operatör direktifi: "sorun model
kalitesi değil, modele giden kanıt" · Onay: operatör 2026-08-28 ("onay
önerine veriyorum") — plan ONAYLI sayılır, dilimler `/release` ile ayrı
ayrı çıkar.

## AŞAMA 1 — Keşif (kod yazılmadı)

### Q1 · Bağlam nasıl toplanıyor, frame→dosya çözümü, sıra, tavan

Kod bağlamı **yalnız iki uçta** var; ikisi de `includeCode` gövdesiyle:
`POST /api/copilot/explain-trace/{id}` (`internal/api/ai_routes.go:103` →
`api.go:8855`) ve `POST /api/copilot/explain-exception/{fp}`
(`ai_routes.go:130` → `copilot_exception.go:32`). Problem / span / incident /
root-cause uçları kod bağlamı taşımaz (`decodeExplainOptions` üç yerde:
`copilot_code.go:34`, `api.go:8869`, `copilot_exception.go:32`).

Zincir:

```
handler ─ opts.IncludeCode ─► buildCodeContext        (api/copilot_code.go:61)
   stack kaynağı: trace → explain_trace_input.go:62-84 · exception → anomaly/exception_context.go:35 (pickExceptionStack)
   katalog pini (fail-CLOSED, copilot_code.go:80-92,141) → devops.ResolveRepo (:93)
   └► FetchCode(repo, hint, ParseJava(stack), ResourceRefs(stack), ErrorCodeTokens(stack))   (copilot_code.go:116)
         targets = stackparse.AppFrames(frames, 10)                  (devops/code.go:434 · stackparse.go:232)
         huntWindows(targets, {windows:3, lookups:6, radius:30})      (code.go:501 · :845)
            find = BestPathForFrame(ağaç)  → ıska + ağaç kesikse scopedHunt.find   (code.go:1437 · :976)
            fetch = fetchItemContent (ADO items?path&includeContent)  (code.go:1313)
         + huntSearchWindows (yalnız ıskalayanlar, opt-in CodeSearch) (code.go:524 · codesearch.go:353)
         + huntErrorCodeWindows (hata-kodu token'ı, opt-in)          (code.go:550 · codesearch.go:420)
         + huntResources (hata metninin andığı XML/SQL, ilk 200 satır) (code.go:571 · :1987)
         ClampCodeWindows(windows, 4000 rune)                         (code.go:578 · :1565)
   └► PromptBlock() user mesajının SONUNA eklenir; system prompt WithCode varyantına döner
         (code.go:1723 · copilot_code.go:284 · prompts.go:934-935)
```

**Sıra:** `AppFrames` en derin `Caused by` segmentini başa alır, segment
içinde stack metin sırası korunur; **başka hiçbir skor yok**
(`stackparse.go:232-256`, `huntWindows` yeniden sıralamaz `code.go:855-863`).

**Tavan:** `codeLookupLimit = 6` sabit (`code.go:60`), `huntLimits.lookups`
olarak geçer (`code.go:506`), `code.go:864-867`'de uygulanır. v0.10.71'den
beri **yalnız dosya ÇEKİMİ** sayılır (`lookups++` `code.go:897`); ağaç ıskası
bedava (`:876-893`), birebir (dosya,satır) tekrarı bedava (`:870-874`).
**Ama** `lookups++` gövde-cache kontrolünden (`bodies[p]`, `:898`) ÖNCE artar:
aynı dosyanın farklı satırı (filter zinciri, dispatcher döngüsü) GET
atmadan tavanı yer — üstteki doküman yorumu ("ikinci bir GET yok") doğru,
"sabır harcamaz" iması yanlış. Ayrı bütçeler: `scopedFetchLimit 6`
(`:110`), `codeSearchLimit 2` (`codesearch.go:172`), `errCodeSearchLimit 2`
(`:413`), `resourceFetchLimit 2` (`code.go:1974`), toplam süre 25 sn (`:70`).

Sürüm tarihçesi: v0.10.71 ıska tavandan düştü (`frame_budget_test.go:37`);
v0.10.73 hata metnindeki kaynak dosyalar; v0.10.74 ıskalayanlar için org
araması (varsayılan KAPALI); v0.10.100 hata-kodu token'ı.

### Q2 · Kaynak kod nereden, satır numarası, pencere, prompt'a gömme

- DevOps API: ağaç `items?recursionLevel=Full` (`code.go:1171-1215`),
  içerik `items?path=…&includeContent=true` (`:1313`), org araması
  `codesearchresults` (`codesearch.go:215`).
- **Satır numarası KORUNUYOR**: `WindowAround` her satırı `"%d| %s"` basar
  (`code.go:1534`); hata satırı render anında `>>>` ile işaretlenir
  (`frameMarker` `:1763`, `markFrameLine` `:1772`).
- Pencere **zaten ±30** (`codeWindowRadius` `:73`), en çok 3 pencere
  (`codeWindowLimit` `:49`), toplam **4000 rune** (`:78`); taşma 400'ünde
  yarıya (`Halved()` `:199`, `copilot_code.go:247-268`). Kırpma hata satırı
  MERKEZDE (`centerToBudget` `:1625`); sığmayan pencere DÜŞER.
- **Metot imzası eklenmiyor** — `WindowAround` simetrik dilim, AST/brace
  farkındalığı yok. Tek karşılığı prompt kuralı: "hedefin tanımı bu bağlamda
  yok" (`prompts.go:908-912`).
- Dosya başı/sonu/kısa dosya `WindowAround`'da clamp'li (`:1507-1538`);
  bulunamayan dosya `huntWindows` ıska listesine düşer (`:876`, `:909`).
- Gömme: `PromptBlock` başlık + pencere başına `pencere i/N — <yol> (satır
  F-T) — <frame>` + çit (`code.go:1734-1760`); **user mesajına** eklenir
  (`full := user + block`, `copilot_code.go:284-286`).

### Q3 · Uygulama frame'i ↔ çerçeve frame'i

- **Tek sinyal:** sabit 9 önek `java. javax. jakarta. sun. jdk.
  org.springframework. org.apache. org.jboss. io.undertow.`
  (`stackparse.go:71-81`, `IsAppClass` `:87`, damga `:182`).
- **Pozitif uygulama-önek listesi YOK; hiçbir yerden yapılandırılamıyor**
  (`frameworkPrefixes` unexported var; `devops_connection` blobu yalnız
  `repoPrefixes/branchOrder/codeSearch` taşır — `devops/client.go:59-103`).
- `repoPrefixes` (`bsa-`) **servis→depo** sözleşmesidir, frame sınıflamasına
  girmez. Deployment unit / war adı kullanılmıyor.
- **Süzülür, ÖNCELİKLENMEZ:** `AppFrames` çerçeveyi atar, kalanlar arasında
  "uygulamalık" sırası yoktur. Bankanın kendi çerçevesi
  (`…core.rest.RestFilter`, `BasicDispatcher`, `RestBackendExecutor`)
  listede olmadığı için **IsApp=true** sayılır ve stack'te iş sınıfından
  önce geldiği için tavanı önce o harcar. `exception_inbox.go:121-139`'daki
  ikinci (parmak-izi) çerçeve listesi kod çekiciyi BESLEMEZ.
- Java dışı stack → sıfır aday → `CodeNoStack` (`code.go:437-446`).

### Q4 · Veritabanı hatalarında şema

**Hiç yok.** `SQLCODE|SQLSTATE|information_schema|syscat|all_tab_columns`
repo genelinde sıfır; `go.mod`'da uygulama-DB sürücüsü yok; `database/sql`
tek import `chstore/scan.go:4` (Null* tarama). Explain girdileri
`db_system/db_statement` taşımıyor: `traceLite`
(`explain_trace_input.go:43-52`) ve `liteSpan`
(`exception_context.go:242-250`) bu alanları düşürüyor. CH'de var:
`spans.db_system`, `spans.db_statement` (`store.go:1012-1013`),
`db_stmt_hash` (`:3035`), `db.name` attribute'tan (`:3398`).
Mapper XML (v0.10.73): dosya adı hata metninden çıkarılıyor
(`resources.go:68`), **statement bloğu değil dosyanın ilk 200 satırı**
çekiliyor (`resourceWindowLines` `code.go:1981`) ve pencere listesinin
SONUNA ekleniyor → 4000 rune kırpması ÖNCE onu düşürüyor.

### Q5 · Prompt şablonu, alıntı zorunluluğu

- Sabitler: `systemTrace/systemException` (`prompts.go:55/221`),
  `+Code` varyantları = gövde + **`systemCodeAddendum`**
  (`prompts.go:893-928`), erişimciler `:934-935`.
- Addendum ZATEN şunları içeriyor: `KOD ALINTISI ZORUNLU … // <yol>:<baş>-<bit>
  … SANA VERİLEN satır numaralarıyla` (`:898-904`), `Numaraları UYDURMA`
  (`:905`), `Bir dosya sana verilmediyse "kaynak çözülemedi: <yol>" de`
  (`:918-919`), "bu pencerede görünmüyor" (`:915-917`). Test:
  `prompt_injection_test.go:143,176`.
- **Boşluk 1:** kod avı boş dönünce model **düz** system prompt'u alıyor ve
  "kod istendi ama çözülemedi" bilgisi yalnız ai_calls kopyasına yazılıyor
  (`explainNoCode`, `copilot_code.go:269-280` — v0.9.1243 bilinçli kararı).
  Addendum'un "kaynak çözülemedi" kuralı o dalda **kapsam dışı**; model
  stack'teki satır numarasını okuyup alıntısız "X. satırda hata var" yazıyor.
- **Boşluk 2:** addendum işareti `">>"` diyor (`prompts.go:901`), render
  `">>>"` basıyor (`code.go:1763`).
- **Boşluk 3:** arama-türevi pencerelerde `w.Path = repo:path`
  (`codesearch.go:401,467`) → `LogSummary` `c.Repo + w.Path` (`code.go:1847`)
  depo adını iki kez yazıyor.

### Q6 · Token bütçesi paylaşımı

| Bölüm | Tavan | Yer | Modele söyleniyor mu |
|---|---|---|---|
| Kod pencereleri | 4000 rune (yarı: 2000) · 3 pencere · ±30 | `code.go:49-78` | **HAYIR** (Reason UI+log'a gider, `PromptBlock` basmaz) |
| Exception stack | 1800 / 900 (ikinci örnek) | `exception_context.go:38,106` | hayır (`…`) |
| Exception trace span | hata önce 20, toplam 60 | `:287,294` | hayır |
| Exception log | 30 sorgu / 12 tutulan / 500 rune gövde | `:332-359` | hayır |
| Trace span | 100 (`pickExplainSpans`) | `explain_trace_input.go:136` | evet (`:282-286`) |
| Trace log | 30 / 15 / 600 rune | `:162-192` | hayır |
| Trace stack | 900, ilk/en ağır 1500 | `:210-214` | hayır |
| Çıktı | max_tokens 4096 (256–32768) | `copilot.go:340-359,764` | — |

Merkezi bütçe paylaştırıcı YOK; her bölüm kendi sabitini taşır, öncelik
sırası (kod > şema > SQL > log) hiçbir yerde ifade edilmiyor.

### Gözlemlenebilirlik (bugün)

Explain başına **OTel span YOK** (`grep tracer.Start internal/copilot
internal/ai internal/api/copilot_*` = 0); yalnız `otelhttp` sunucu span'ı
(`api.go:1393`). `ai_calls` satırı (`copilot.go:687-733` →
`chstore/ai_calls.go:47`) token/süre/karakter taşır ama frame sayısı
taşımaz. `code_stats.go` süreç-içi atomik sınıf sayaçları (13 sınıf),
frame çözünürlüğü sayılmaz. selfobs tracer hazır (`selfobs.Tracer()`
`selfobs.go:191`), emsal `chstore/traced_conn.go:148`.

### Semptom → kök neden

| Semptom | Ölçülmüş neden | Kanıt |
|---|---|---|
| "X. satırda hata var" ama alıntı yok | Kod çözülmediğinde model düz prompt + stack'teki satır numarası; çözüldüğünde işaret uyuşmazlığı ve kırpma bilgisizliği | `copilot_code.go:269-280`, `prompts.go:901` vs `code.go:1763` |
| "deneme tavanı (6) doldu — 4 frame denenmedi", denenenler çerçeve sınıfları | Kurum-içi çerçeve IsApp=true; pozitif liste yok; sıra yalnız segment derinliği; aynı dosyanın farklı satırı tavan yiyor | `stackparse.go:71-87,232`, `code.go:897-898` |
| SQLCODE'da "muhtemelen telefon numarası" | Şema hiç yok; `db_statement` girdide yok; mapper XML'in ilk 200 satırı sona ekleniyor ve önce kırpılıyor | Q4 |

**Varsayımlar (doğrulanamadı, prod erişimi yok):** (V1) prod ≥ v0.10.71 —
değilse ıskalar da tavan yiyordu, semptomun ikinci kaynağı; (V2) bankanın
DB'si DB2 (`SQLCODE=-302, SQLSTATE=22001` JDBC mesaj kalıbı) — prod
`spans.db_system` ile teyit edilmeli; (V3) çerçeve sınıflarının paket
kökü `com.<banka>.core.*` gibi tek bir önek altında.

## AŞAMA 2 — Plan (dosya bazında)

Bütçe önceliği (operatör): **uygulama kod alıntısı > şema > SQL artefaktı
> log**; kırpma modele bildirilir. Maske: kod/şema/SQL bloklarının hiçbiri
`ai_calls`'a yazılmaz (`MaskCodeInPrompt` sözleşmesi bloğun tamamına
uygulanır).

### Dilim A — Frame önceliklendirme + ayarlanabilir tavan
- `internal/stackparse/stackparse.go`: `Frame.Tier` (0 app-önek, 1 diğer
  uygulama, 2 çerçeve); `RankFrames(frames, n, appPrefixes)` = `AppFrames`
  + birincil anahtar Tier, ikincil segment derinliği, üçüncül stack sırası.
  `appPrefixes` boşsa davranış bugünle BİRE BİR (`AppFrames` sarmalayıcı).
- `internal/devops/client.go`: `Settings.AppPrefixes []string`,
  `Settings.CodeLookupLimit int` (0 = varsayılan 6, clamp 1..30) — aynı
  `devops_connection` blobu; Snapshot'ta yankı.
- `internal/devops/code.go`: `FetchCode` `RankFrames(frames, 10,
  cfg.AppPrefixes)`; `huntLimits.lookups = cfg.lookupLimit()`;
  `lookups++` gövde-cache isabetinde ARTMAZ (sözleşme: tavan = GET);
  not metni `codeLookupLimit` sabiti yerine yürürlükteki değeri basar.
- `internal/api/devops_handlers.go`: PUT/GET alanları + audit detayı.
- `frontend/src/lib/types.ts` `DevOpsSnapshot/DevOpsSettingsInput`,
  `frontend/src/pages/settings/DevOpsTab.tsx`: "Uygulama paket önekleri"
  + "Kod çekme deneme tavanı" alanları.
- Test: `stackparse/rank_test.go` (karışık app/çerçeve/kurum-içi listeler,
  boş önek = eski sıra), `devops/frame_budget_test.go` (cache isabeti tavan
  yemez; yapılandırılmış tavan uygulanır).

### Dilim B — Kod penceresi: imza + kırpma bildirimi + işaret tutarlılığı
- `internal/devops/code.go`: `EnclosingSignature(lines, line, from)` —
  pencere başından yukarı doğru, hata satırından az girintili ilk
  metot/ctor bildirimi (Java/Kotlin/Scala regex; `if/for/while/switch`
  hariç); `CodeWindow.Signature{Line, Text}`; `PromptBlock` pencere
  başlığına `imza (satır N): …` ekler. `WindowAround` imzayı hesaplar
  (pencere içindeyse boş).
- `PromptBlock`: `trimmed`/düşen pencere/`Halved` bilgisi tek satırla
  modele söylenir (`NOT: kod bütçesi doldu — N pencere düştü …`).
- `internal/copilot/prompts.go:901`: `">>"` → `frameMarker` ile tek yazım
  (`devops.FrameMarker` export; prompt testi pinler).
- `codesearch.go:401,467` + `code.go:1847`: çift depo öneki düzeltmesi.
- Test: `code_window_signature_test.go` (dosya başı, dosya sonu, kısa dosya,
  imza pencere içinde, ctor, Kotlin `fun`), `PromptBlock` kırpma notu.

### Dilim C — SQL artefaktı: statement id → blok
- `internal/stackparse/resources.go`: `ResourceRef.Member` (nitelikli
  kimliğin son parçası, `ariCTelefonSelect`).
- `internal/devops/code.go`: `MapperStatementWindow(body, id)` — XML'de
  `<select|insert|update|delete|sql id="…">…</…>` bloğu, gerçek satır
  numaralarıyla (±0, tavan 80 satır); bulunamazsa eski ilk-200 davranışı.
  `huntResources` önce bloğu dener.
- Test: `mapper_statement_test.go` (id var/yok, iç içe include, CDATA,
  aynı id iki tag).

### Dilim D — Şema zenginleştirme (SQLCODE/SQLSTATE)
- Yeni paket `internal/appschema/`: `Catalog` arayüzü
  (`Columns(ctx, table) ([]Column, error)`), `Column{Name, Type, Length,
  Scale, Nullable}`; `SQLErrorSignal(text)` (SQLCODE/SQLSTATE/ORA-/PG kodu
  saf parser); `TargetsOf(sql)` (INSERT/UPDATE/SELECT/MERGE'ten tablo +
  kolon listesi, saf, `?` bind'li MyBatis SQL'i); `PromptSection(cols,
  budget)` — `table.col TYPE(len) NULL/NOT NULL` satırları, kırpılınca
  `[şema kırpıldı: N kolon gösterilmedi]`.
- **Katalog kaynağı bu dilimde SNAPSHOT:** operatör her flavor için verilen
  salt-okunur SELECT'i (DB2 `SYSCAT.COLUMNS`, Oracle `ALL_TAB_COLUMNS`,
  PG/MySQL/MSSQL `information_schema.columns`) kendi tarafında koşturup
  CSV'yi `PUT /api/settings/schema-catalog` ile yükler; blob
  `system_settings["schema_catalog"]` (LoadPersisted/SavePersisted emsali
  `devops/client.go:204-265`), audit `schema_catalog.import`.
  **Sapma gerekçesi:** V2 (DB2) için cgo'suz Go sürücüsü yok;
  `go_ibm_db` = cgo + IBM clidriver → tek-binary/air-gapped imajı bozar.
  Sürücü eklemek prod `db_system` teyidinden ÖNCE kör bahis olur. Canlı
  bağlayıcı (`database/sql`, salt-okunur kullanıcı, 3 sn timeout) aynı
  `Catalog` arayüzünün ikinci uygulaması olarak flavor teyit edilince
  gelir; DB2 olursa seçenek REST/ODBC köprüsü — ayrı karar.
- `internal/api/explain_trace_input.go` + `internal/anomaly/exception_context.go`:
  `db_system/db_statement` HATA span'ından taşınır (yalnız hata span'ları,
  ≤2 ifade, 600 rune) — şema hedefi bu ifadeden çıkar; ifade yoksa mapper
  bloğundan (Dilim C).
- `internal/api/copilot_code.go`: `buildEvidence` → `EvidenceContext{Code,
  Schema, SQL}`; `PromptBlock` sırası kod → şema → SQL; toplam bütçe 5200
  rune (kod 4000 + şema 800 + SQL 400); kırpma notları modele.
- Yeni uç kendi dosyasında: `internal/api/schema_catalog.go`
  `registerSchemaCatalogRoutes(mux)` (api.go +1 satır); Settings → Kod
  entegrasyonu sekmesine "Şema kataloğu" kartı (yükle / satır sayısı /
  son yükleme; CSV içeriği geri yankılanmaz).
- Test: `appschema/*_test.go` (sinyal parser: DB2/Oracle/PG/MSSQL
  kalıpları; hedef çıkarımı: INSERT kolon listesi, UPDATE SET, alias'lı
  SELECT; prompt bölümü bütçe kırpma), `copilot_code_test.go` uçtan uca:
  SQLCODE'lu stack + sahte katalog + sahte DevOps → prompt'ta
  `TELEFON VARCHAR(10) NOT NULL` satırı, ai_calls kopyasında yok.

### Dilim E — Prompt: kod istendi ama çözülemedi → modele söyle
- `internal/api/copilot_code.go` `copilotExplainCode`: kod boşsa WithCode
  system prompt + user'a `KOD BAĞLAMI İSTENDİ, ÇÖZÜLEMEDİ: <reason>;
  denenen frame'ler: …` bloğu (maskede aynen kalır — kod yok).
  v0.9.1243 kararı TERSİNE çevrilir; gerekçe operatör direktifi 2026-08-28.
- `prompts.go` addendum: "kod verilmediyse satır numarası İDDİA ETME;
  `kaynak çözülemedi: <yol>` yaz" cümlesi + işaret tek yazım.
- Test: `copilot_code_test.go` (boş bağlam → WithCode prompt + blok),
  `prompt_injection_test.go` güncelleme.

### Dilim F — Gözlemlenebilirlik: Explain başına span
- `internal/api/ai_observability.go`: `copilotExplain*` sarmalayıcıları
  `selfobs.Tracer().Start(ctx, "ai.explain")`; attribute'lar:
  `ai.surface`, `ai.code.frames.total/tried/resolved/unresolved`,
  `ai.code.windows`, `ai.context.types` (`code,schema,sql,log`),
  `ai.context.trimmed`, `ai.tokens.input/output`, `ai.provider/model`,
  `ai.status`. Frame sayıları `huntOutcome`'dan `CodeContext.Stats`'a
  taşınır (sayı, prose değil).
- `ai_calls` satırına yeni kolon YOK (dağıtık-güvenli kolon ekleme
  maliyeti; span yeterli — self-telemetry `spans` tablosunda sorgulanır).
- Test: `CodeContext.Stats` birim testi; span attribute'ları
  `sdktrace` in-memory exporter ile.

Sıra: A → E → B → C → F → D (D en büyük; snapshot importu içinde).
Her dilim: `go build ./... · go vet ./... · go test -race ./... · make
audit · npx tsc --noEmit` → kendi `v0.10.X` tag'i.
