# Log Arama Audit — Kibana Discover Seviyesine Mesafe

**Tarih:** 2026-09-02 · **Sürüm tabanı:** v0.10.273 · **Kapsam:** salt-okuma inceleme (Explore ajanı + ana oturum grep doğrulaması)
**Kısıtlar:** ES yalnız okuma · ClickHouse tek storage · design-system token'ları · testler geçer
**Durum:** ONAY BEKLİYOR — kod yok.

## Yönetici özeti — brief'in varsayımı ile gerçeğin farkı

Brief "sorgu dili yok, alan paneli yok, sütun seçimi yok" varsayıyor. **Kod bunu doğrulamıyor.** `/logs` bugün Kibana Discover'ın gövde özelliklerinin çoğunu taşıyor: pill modeli (negasyon/devre-dışı/is-one-of/exists/aralık), alan paneli + top-değerler akordeonu, brush ile zaman daraltma, DataTable sütun seçimi, bağlam modalı, SSE canlı kuyruk, kaydedilmiş görünümler. Eksik olan üç şey ve bunların **ikisi mimari, biri yüzey**:

| # | Gerçek boşluk | Sınıf |
|---|---|---|
| **B1** | Sorgu dili **yalnız ES'te** çalışıyor. ClickHouse (VARSAYILAN backend) `key:value`'yu gövde substring'i olarak arıyor → yapısal olarak 0 satır, hata yok. | Mimari, P1 |
| **B2** | Go tarafında **ayrıştırıcı yok**. Sorgu frontend'de string olarak derlenip (`compileSearch`) ES'e verbatim geçiyor. Tek doğruluk noktası yok. | Mimari, P1 |
| **B3** | Pattern/imza gruplama **yüzeyi yok**. `NormalizeSignature` var ama `/logs`'a bağlı değil; `/api/logs/templates` var ama frontend tüketicisi **yok**. | Yüzey, P2 |

Bir de sessiz bir performans borcu: ana ES aramasında `_source` kısıtı yok — tam doküman tel üzerinden geliyor (§5.2).

Ana oturumun bağımsız doğrulamaları: `internal/logstore/signature.go:32 NormalizeSignature` + `:50 SignatureHash` (xxhash) — brief'in sorduğu "Problem enrichment normalize fonksiyonu" budur; log rotaları `api.go:831`'de kayıtlı (register fonksiyonu yok); `LogFieldsPanel/LogPillEditor/LogsHistogram/LogTable` bileşenleri mevcut.

---

## 1. Mevcut log arama akışı

### 1.1 Frontend — sayfa anatomisi

Tek sayfa: `frontend/src/pages/Logs.tsx` (1363 satır, `LogsInner`). Yardımcılar `components/Log*.tsx`, saf çekirdekler `lib/log*.ts`.

| Parça | Dosya:satır |
|---|---|
| Zaman aralığı | `pages/Logs.tsx:150` — `usePageZoomRange('30m', …)`; **`useUrlRange` doğrudan değil**, hook sarmalıyor (`lib/chart/usePageZoomRange.ts:32`). ContextBar'a bağlı değil; `Topbar` kendi `onRangeChange`'ini veriyor (`Logs.tsx:703`). |
| ns dönüşümü | `Logs.tsx:375-377` — `useMemo` içinde `timeRangeToNs` (v0.5.184 disiplini uygulanmış) |
| Kaydedilmiş görünüm | `Logs.tsx:716` — `<SavedViewsBar page="logs" />` |
| Alan paneli | `Logs.tsx:1099` — `<LogFieldsPanel>` |
| Histogram | `Logs.tsx:1125` — `<LogsHistogram … onRangeSelect={…} onZoomReset={…}>` |
| Tablo | `Logs.tsx:1272` — `<LogTable>` |
| Bağlam modalı | `Logs.tsx:1335` — `<LogContextModal>` |
| Canlı kuyruk | `Logs.tsx:507-546` — `new EventSource('/api/logs/stream?…')` |

### 1.2 Sorgu nasıl kuruluyor

**Serbest metin + yapısal pill'ler, ikisi tek Lucene dizesine derleniyor.**

- Pill modeli: `lib/logFilters.ts:12-33` (`LogFilter`: `key/value/negated/disabled/values[]/exists/op`).
- Derleyici: `lib/logFilters.ts:48` — `compileSearch(filters, query)`:
  - `exists` → `_exists_:key`
  - `op` → `key:>="v"` / `key:<="v"`
  - `values.length>1` → `key:("a" OR "b")`
  - düz → `key:"v"` (`phraseQuote`, `logFilters.ts:40`)
  - serbest metin sona `AND` ile ekleniyor; içinde `OR` varsa parantezleniyor.
- Sonuç `compiledSearch` olarak **tabloya, histograma, canlı kuyruğa ve Kibana deep-link'ine aynı** gidiyor (`Logs.tsx:1125` ve `:546` bağımlılık listesi).
- URL tek kaynak: `lib/logsUrl.ts:29-56` (`logsUrlSig` / `writeLogsParams` / `readLogsParams`), sig-guard `Logs.tsx:321-349`. `?q=`, `?filters=` (kompakt JSON tuple, `logFilters.ts:100+`), `?cols=`, `?severity=`, `?service=`, `?cluster=`, `?traceId=`, `?spanId=`, `?hasTrace=`, `?asc=`.

**Yani "sorgu dili" bir istemci-tarafı dize üreticisidir; gramer sahibi ES'tir.**

### 1.3 Backend — `internal/api/api_logs.go` (1030 satır)

| Uç | Handler | Cache |
|---|---|---|
| `GET /api/logs` | `api_logs.go:275` → `serveLogsSearch:359` | `serveCached` 15s, `logsSearchKey:261` |
| `POST /api/logs/search` | `:288` (v0.9.1094 — ES PIT cursor'ı URL sınırını aşıyordu; gövde 1MB, `:306`) | aynı anahtar |
| `GET /api/logs/stream` | `:113` `streamLogs`, saf `tailStep:54` | cache YOK (canlı kenar) |
| `GET /api/logs/timeseries` | `:963` | 30s, `logsTimeseriesKey:217` |
| `GET /api/logs/fields` | `:504` | 60s, sabit anahtar `"logs-fields"` |
| `GET /api/logs/fieldstats` | `:886` | 60s, `logsFieldStatsKey:205` |
| `GET /api/logs/field-values` | `:651` | 30s |
| `GET /api/logs/context` | `:759` | 15s, `logsContextKey:754` |
| `GET /api/logs/templates` | `:708` | 30s — **frontend tüketicisi YOK** (grep: `frontend/src` içinde `logs/templates` 0 eşleşme) |

**Kayıt yeri — audit bulgusu:** bu rotaların hepsi `internal/api/api.go:831-854`'te, doğrudan `registerRoutes` içinde. `api_logs.go`'nun `registerXxxRoutes` metodu **yok** (dosya başlığı `api_logs.go:1-8` "api.go'dan ayrıldı, davranış korunarak" diyor). Bu, `/api-route` skill'inin bugünkü kuralıyla uyumsuz — §6.3'te plan var.

### 1.4 ES sürücüsü — üretilen DSL

`internal/logstore/elasticsearch.go` (126 KB). Ana yol `Search:1275` → `buildQuery:2434`.

Üretilen sorgu **`bool.must` listesi**:

1. **Zaman:** `range` on `s.fields.Timestamp`, RFC3339Nano (`:2445-2456`)
2. **trace/span:** `traceTermsAny` — dört yazım (`trace.id` / `TraceId` / `trace_id` / `traceId`) + operatör override üzerinde `bool.should` (`:2478`, `:2513`)
3. **hasTrace:** aynı dört yazım üzerinde `exists` fan-out (`:2519-2540`)
4. **service/cluster/pod/env:** `.keyword` ve çıplak keyword İKİ şekilde `term` (`:2545-2560`, v0.7.16 + v0.8.239)
5. **Serbest metin:** `query_string` (`:2714-2721`):
   ```
   query: expandShorthand(f.Search)
   default_field: s.fields.Body
   default_operator: "AND"
   allow_leading_wildcard: false
   lenient: true
   ```
   `case_insensitive` **yok** — v0.5.231'de kaldırıldı, ES 8.x reddediyor (`:2697-2712` şerhi + `docs/INCIDENTS.md`). Geri eklenmemeli.
6. **severity:** `range gte` on `s.fields.SeverityNo` (`:2724`)

**Kısayol genişletme:** `expandShorthand:2749` — `level|severity|service|trace|span|message|body|pod|container|namespace|deployment|cluster|host` anahtarlarını çok-şekilli OR gruplarına çeviriyor (`level:error` → `(log.level:error OR level:error OR severity:error OR …)`). Regex `shorthandRe:2740`.

**Arama gövdesi** (`:1412-1441`):
- `sort`: `[{timestamp: {order, unmapped_type: date}}, {_shard_doc|_doc: order}]`
- `size: limit`
- `docvalue_fields`: timestamp `epoch_millis` (v0.8.229 — format-agnostik zaman)
- `track_total_hits: 10000` (ES varsayılan sınırı; UI "10000+" gösteriyor)
- `timeout: esTimeoutFromEnv("10s")` — **soft** timeout
- `search_after` cursor varsa (`:1452`); PIT modu `retainPIT` disipliniyle (`:1395-1406`)
- `terminate_after` yalnız trace lookup + ilk sayfa (`:1456-1470`, v0.7.82)
- **`_source` kısıtı YOK** → tam doküman dönüyor (grep doğrulaması: `"_source":` yalnız `:2345` `patternCountBody` ve `RawSearchSamples` içinde)

**Dönen alanlar:** `LogRecord` (`logstore.go:~135`) — id/timestamp/severity/severityText/body/serviceName/traceId/spanId/attributes/resourceAttributes. `mapHit` ham `_source`'tan çok-şekilli okuma yapıyor.

**Sayfalama:** keyset. `Filter.Cursor` (`logstore.go` şerhi: ES = `base64(sort-values JSON + PIT id)`, CH = `base64("ch|"+timeNs+"|"+cityHash64 rowKey)`). `WantCursor` (v0.9.286) PIT tutma niyetini AÇIKÇA bildiriyor — sızan PIT sınıfının aşısı.

**Dürüstlük zarfı** (`logstore.go` `Page`): `Partial`, `ShardsFailed`, `TotalIsLowerBound`, `EnvUnapplied`, `HasTraceUnapplied`. Bu, Kibana'nın kendisinden **daha iyi** — kısmi cevabı tam gibi göstermiyor.

### 1.5 CH backend'iyle fark — en kritik ayrışma

`COREMETRY_LOGS_BACKEND` (`internal/config/config.go:725`) seçiyor. CH yolu `internal/logstore/clickhouse.go:28`.

CH'de `f.Search`:
```
clickhouse.go:292-311   AND multiSearchAnyCaseInsensitive(body, [?])
clickhouse.go:305-308   (bare hex id ise) OR trace_id = ? OR span_id = ?
clickhouse.go:612-620   histogram yolunda aynı
```

Yani `service.name:"checkout"` dizgesi **log gövdesinde birebir aranıyor**. Bu tespit repoda zaten yazılı ve **ölçülmüş**: `internal/logstore/query_syntax.go:8-27`

> `body LIKE '%service.name:"x"%'` → **0 satır**, ve tüm tabloda `countIf(body LIKE '%service.name%') = 0`.

`LooksLikeFieldQuery` (`query_syntax.go:56`) ve `BackendUnderstandsFieldQuery` (`query_syntax.go:68`) bu durumu **tespit ediyor ama çözmüyor** — uyarı katmanı. `internal/api/dead_log_query_test.go` bunu pinliyor.

**Sonuç:** varsayılan kurulumda pill bar'ın ürettiği her şey sessizce boş dönüyor. Kaydedilmiş sorgu alarmı (`evaluateLogQuery`) aynı yoldan geçtiği için alan yazımlı bir kural CH'de **daima 0 sayar, eşiği asla aşmaz** (çağrı yeri doğrulanmadı — şerhe dayanıyor, bkz. Ek).

---

## 2. ES index mapping'lerine erişim

### 2.1 Bugün var olan

`ListFieldsBounded` — `elasticsearch.go:557`:
- `esapi.IndicesGetMappingRequest` (**`_mapping`, `_field_caps` değil**)
- İndeksler 24 saatlik pencereye daraltılmış (`:558-560`, v0.9.292)
- `walkProperties:1147` özyinelemeli iniyor
- **Tip DÜŞÜYOR:** `:1164-1169` tipe göre *filtreliyor* (`keyword/text/long/integer/short/double/float/date/boolean/ip`) ama sonuca yalnızca **yol adını** koyuyor (`out[path] = struct{}{}`)
- `listFieldsMax = 500` (`:528`), alfabetik kırpma + `ensureConventionalFields:592` (v0.9.452 — `openshift.*`/`kubernetes.*` geç harfler kırpmaya kurban gitmesin)
- Handler: `api_logs.go:504`, cache **60s**, sabit anahtar

### 2.2 Eksik: alan TİPLERİ

Runtime'da tip **alınamıyor**. Bu üç şeyi bloke ediyor:
1. Aralık operatörünü yalnız sayısal/tarih alanlarda önermek (bugün `LogFilter.op` her alanda sunuluyor)
2. `_terms_enum` uygunluğunu önden bilmek (yalnız `keyword`/`constant_keyword`'de çalışıyor — `elasticsearch.go:951` şerhi)
3. Alan panelinde tip rozeti (Kibana'nın `t`/`#`/`🕐` işaretleri)

### 2.3 `_field_caps` nasıl eklenir — emsal ZATEN var

`internal/logstore/es_trace_context.go:263` — `func (s *ESStore) fieldCaps(ctx, indices, fields []string)`. Kullanımı `es_env_field.go:157`:
- 3s ayrı ctx (`envFieldCapsTimeout`, `es_env_field.go:43`)
- **10 dakika TTL**, pozitif VE negatif verdikt cache'leniyor (`envFieldTTL:40`)
- steady-state ≤1 `field_caps` / 10 dk / backend örneği

**Öneri:** `ListFieldsBounded`'ı `ListFieldTypes(ctx) (map[string]string, error)`'a genişlet; `_mapping` + `_field_caps` **tek turda** birleştirilebilir ya da `_field_caps?fields=*` tek başına yeterli (mapping walk'a gerek kalmaz, çünkü `field_caps` düz yol listesi + tip döner). Maliyet: metadata-only, shard başına term dictionary'ye dokunmuyor. TTL: mevcut `logs-fields` 60s'i **10 dk'ya çıkar** (mapping saatte bir değişen bir şey değil; v0.8.270 sınırlı-basamak disiplini).

> `_field_caps?fields=*`'ın binlerce dinamik yolu olan bir kümede yanıt boyutu — **doğrulanmadı**. `listFieldsMax=500` kırpması aynı şekilde uygulanmalı.

---

## 3. Eksik özelliklerin mevcut kodla mesafesi

| Özellik | Durum | Başlangıç dosyası |
|---|---|---|
| **Serbest metin + key:value + AND/OR/NOT + parantez** | **Kısmen** — ES'te tam (Lucene `query_string`), CH'de **hiç** | `internal/logstore/query_syntax.go` |
| Go tarafı ayrıştırıcı | **Yok** | — (aday: `internal/dql/dql.go`) |
| Alan adı autocomplete | **Var** | `Logs.tsx:779-793` → `KqlSearchInput` (`fields` + `fieldsTotal`, sıfır ek ES maliyeti — v0.9.955/970) |
| Değer autocomplete | **Var** | `LogPillEditor.tsx:67` → `api.logsFieldValues`; ES `_terms_enum` (`elasticsearch.go:969`), CH `[]` döner (`clickhouse.go:549`) |
| **Sol alan paneli + değer dağılımı** | **Var** | `components/LogFieldsPanel.tsx` — `:191` akordeon, `:57` `useQuery(['logs','fieldstats',…])`, `staleTime 60_000` sunucu TTL'ine eşit, **fetch-on-expand** (v0.8.255) |
| Tıkla-filtrele / hariç-tut | **Var** | `LogFieldsPanel.tsx:44-47` `onPillAdd`/`onPillExclude`, `:47` `onExists`; satır içi `LogTable.tsx:87` `kv-actions` |
| "daha fazla" 5→20 | **Var**, iki basamak (cache kardinalitesi sınırlı, v0.9.1223) | `LogFieldsPanel.tsx:54-58` |
| Kapsama yüzdesi | **Var**, sıfır ek sorgu | `LogFieldsPanel.tsx:76-82` |
| **Seviye renkli zaman histogramı** | **Var** | `components/LogsHistogram.tsx`; `breakdown` kırılımı (`Logs.tsx:1128`), uPlot (`:434`), tema token'ları (`:296`) |
| Drag → zaman daraltma | **Var** | `LogsHistogram.tsx:89-96` `onRangeSelect` + `onZoomReset`; `Logs.tsx:1126-1127` `handleZoom(fromNs/1e9, …)`; çift-tık geri (v0.9.373/431) |
| **Seçilebilir sütunlar** | **Var** | `LogTable.tsx:294` `useDataTable({storageKey:'logs'})`; `:35` `DEFAULT_LOG_COLUMNS`, `:39` `NON_REMOVABLE_COL_IDS`; **resize-only** (server-paged, v0.7.54 `:21-25`); `contentVisibility:'auto'` `:405` |
| **Satır detayı + pivot** | **Var, kısmen** | genişleme `LogTable.tsx:87` (her k/v için ⊕/⊖ + sütun ekle); trace'e git `:250` `onTracePeek` + `traceHref`; bağlam `:255` `onContextOpen` → `LogContextModal`; permalink `logsUrl.ts` `buildDocPermalink` |
| — pod loglarına git | **Kısmen** | `lib/logPod.ts` `podOfLog` var, `Filter.Pod` backend'de var (`logstore.go` Pod şerhi, v0.9.1249) — ama satırdan **tek tık pod pivotu** yok; `LogContextModal` pod'u kullanıyor |
| — imzaya göre grupla | **Yok** | grep: `Logs.tsx`/`LogTable.tsx` içinde `signature` 0 eşleşme |
| **Pattern gruplama (drain)** | **Backend var, yüzey yok** | `internal/templater/drain.go` + `puller.go:14-27` (1000 örnek / 5 dk, lider-kilitli); `internal/anomaly/log_templates.go:53` `DetectNewLogTemplates`; `internal/anomaly/log_patterns.go` |
| — `/api/logs/patterns` | **YOK** | CLAUDE.md perf bütçesi bu ucu adlandırıyor ama rota kaydı yok. Var olan: `GET /api/logs/templates` (`api.go:854`) ve `GET /api/anomalies/log-patterns` (`api.go:1017`) |
| **Tail modu (SSE)** | **Var** | `api_logs.go:113` `streamLogs`; saf `tailStep:54` (aynı-ns dedup + ilerleme garantisi + `gap` işareti); `logsTailCadence = 10s` (`:33`); FE `Logs.tsx:507-546` |
| **Kaydedilmiş / paylaşılabilir sorgu** | **Var** | `Logs.tsx:716` `<SavedViewsBar page="logs"/>`; `saved_views` tablosu `internal/chstore/store.go:1322`; `SavedViewsBar.tsx:132` `queryString: qs` — URL'in tamamını saklıyor (yeni şema gerekmiyor) |

### 3.1 Yeniden kullanılabilir ayrıştırıcılar

- **`internal/dql/dql.go`** (540 satır): boru şeklinde Kusto/DQL. `dql.go:22` şerhi: *"Logs (KQL-backed) deferred to Phase 2 — the parser knows the table name but the executor errors."* `TableLogs` sabiti `:56` **zaten tanımlı**. Grameri boru-tabanlı (`| filter … | summarize …`), Discover'ın tek satırlık serbest-metin+alan karışımı **değil**. `Plan.Filters []chstore.FilterExpr` (`:63`) → **hedef IR olarak uygun**, gramer olarak değil.
- **`chstore.FilterExpr`** — traces/Explore'un yapısal filtre tipi; `FilterQueryBox.tsx` ve `FilterBuilder.tsx` üretiyor. `key op value` üçlüsü; **AND/OR/parantez taşımıyor** (düz AND listesi). Log ağacı için doğrudan yetmez.
- **`lib/filterQuery.ts`** — `parseInlineFilter` / `splitListValues` / `opFromShorthand`. Satır içi `k=v`, `k in a,b` çözüyor; boole ağacı yok.

**Karar önerisi:** Yeni bir `internal/logql` paketi. `dql`'i genişletmek yanlış — boru grameri farklı bir dil ve `dql.Plan` toplama-odaklı. `FilterExpr`'i IR olarak yeniden kullan; ağaç için `LogExpr` (AND/OR/NOT/parantez) ekle.

---

## 4. Backend tasarımı

### 4.1 Sorgu ayrıştırıcısı — Go'da TEK yer

Yeni paket `internal/logql/`:

```
Parse(q string) (*Expr, error)     // Lucene/KQL alt kümesi → AST
Expr.ToES() map[string]any         // bool/must/should/must_not
Expr.ToCH() (sql string, args []any)
```

Gramer (Discover paritesi, fazlası değil):
```
expr    := or
or      := and ("OR" and)*
and     := not (("AND" | ε) not)*      // örtük AND (ES default_operator ile aynı)
not     := "NOT"? primary
primary := "(" expr ")" | clause
clause  := field ":" (value | "(" value ("OR" value)* ")" | range) | term | phrase
range   := (">=" | "<=" | ">" | "<") value
field   := "_exists_" | ident("." ident)*
```

**Neden Go'da:** bugün `compileSearch` (`lib/logFilters.ts:48`) tek doğruluk noktası ve TypeScript'te. Alarm yolu (`evaluateLogQuery`) o dizeyi yorumlamadan ES'e veriyor; CH'de anlamsız. Ayrıştırıcı Go'ya geçince:
- CH backend'i `key:value`'yu **gerçek** kolon/res-array yüklemine derler (§4.2)
- Alarm ve UI aynı anlamı paylaşır
- `LooksLikeFieldQuery` uyarı katmanı gerekmez (ama regresyon pini olarak kalsın)

Frontend `compileSearch` **kalır** — pill'ler hâlâ tek dize üretir; sunucu artık o dizeyi ayrıştırır. Sözleşme değişmez, anlam kazanır.

### 4.2 CH tarafında alan çözümü — mevcut emsaller

`clickhouse.go` zaten alan→ifade eşlemesi taşıyor:
- `FieldStats:564-580` — `service` → `service_name`; `severity|level|log.level|severity_text` → `if(severity_text != '', severity_text, toString(severity_num))`; diğerleri **res-array lookup** (v0.8.400)
- `chLogsPodExpr` / `logsPodChainSQL` — pod zinciri (`logstore.go` Pod şerhi)
- env: iki semconv yazımı üzerinde sınırlı res-array (`logstore.go` Env şerhi)

`logql`'in CH derleyicisi **aynı çözümleyiciyi** kullanmalı — `es_group_fields.go`'nun ES karşılığındaki rolü gibi tek yerde (`internal/logstore/field_resolve.go` önerisi).

### 4.3 Normalize fonksiyonu — SORULAN CEVAP

> **`logstore.NormalizeSignature` — `internal/logstore/signature.go:32`**
> Hash: **`logstore.SignatureHash`** — `internal/logstore/signature.go:50` (xxhash64)

- Yer tutucu `<x>`, boşluk sıkıştırma, 512 karakter tavanı (`signature.go:19`)
- Sıra taşıyıcı: UUID → ISO zaman → IP → hex → sayı (`signature.go:29-31` şerhi)
- **Redaksiyon değil, gruplama anahtarı** — örnek mesaj verbatim saklanıyor (`feedback-no-redaction`, CLAUDE.md kısıtı)
- Drain'den **ayrı**: örnek tabanı yok, tek mesaj → tek imza, deterministik

**Bugünkü tek tüketici:** `internal/influx/enrich.go:345` — `groupLogSignatures`, Problem zenginleştirmesindeki log imza grupları (`chstore.LogSignature`, `internal/chstore/rootcause_hypothesis.go:120`).

**Direktif:** `/api/logs/patterns` **bu fonksiyonu** kullanacak. `internal/templater/drain.go`'nun kendi masker'ı (`templater/masker.go`) ayrı bir uzay — imza yüzeyi ile Problem yüzeyi aynı grubu göstermezse operatör iki farklı "aynı log" tanımı görür.

### 4.4 Alan dağılımı / histogram — mevcut kullanım

| İhtiyaç | ES | CH |
|---|---|---|
| terms agg | **Var** — `FieldStats:1068` tek sınırlı terms (keyword tercihli, unmapped'de bir çıplak-alan yeniden denemesi); pattern agg `:2337` | `FieldStats:564` sınırlı `GROUP BY` |
| date_histogram | **Var** — `Histogram:1921`, `histogramDateAgg:1786`; **`min_doc_count:1`** (v0.8.3 olayı: `0` her boş aralığı materyalize ediyordu) | `Histogram:254` |
| significant_text | Var, `background_filter` + `sampler` şart (`elasticsearch.go:161-195`, INCIDENTS) | — |
| `_msearch` | **Var** — `CountPatterns:2139` (v0.5.241; per-pattern `_search` yasak) | `countOnePattern:415` sıralı |

### 4.5 `logstore.Store` — bugün ve eklenecekler

Bugünkü metotlar (`internal/logstore/logstore.go`, `type Store interface`):
`Search` · `CountPatterns` · `Histogram` · `EQLSearch` · `RawSearch` · `RawSearchPayload` · `RawSearchSamples` · `Indices` · `FieldValues` · `FieldStats` · `Backend` · `Ping`

Arayüzde **olmayan**, tip-assert ile çözülen kabiliyet: `ListFields` / `ListFieldsBounded` (`api_logs.go:507-515` — `logstore.Unwrap(s.logs)` ile `Switchable` sarmalayıcısı açılıyor, v0.8.232).

**Eklenecekler:**

```go
// Dilim 1
ListFieldTypes(ctx) (FieldTypeResult, error)   // ListFieldsBounded'ın tipli halefi;
                                               // ES: _field_caps (es_trace_context.go:263 emsali)
                                               // CH: sabit şema + res-array anahtar örneklemi
// Dilim 2
GroupBySignature(ctx, f Filter, limit int) ([]SignatureGroup, error)
```

`SignatureGroup{Hash string; Template string; Count int64; Sample string; Severity uint8; FirstSeen, LastSeen int64}`.

- **ES:** `terms` agg **kullanılamaz** (imza indekste yok). Sınırlı örnek çekip Go'da `NormalizeSignature` — `templater/puller.go`'nun örnekleme disiplini (`puller.go:14-24`) burada da geçerli; örnek tavanı + pencere zorunlu.
- **CH:** aynı örnekleme; `logs` tablosunda imza kolonu yok. Tam tarama **yasak** (INCIDENTS "Drain templating … sample-based").
- `Store` arayüzüne eklemeden önce: opsiyonel kabiliyet olarak tip-assert mi, arayüz metodu mu? **Arayüz metodu** — `CountPatterns`'ın gerekçesi (çoğul biçim ki backend batch'lesin) burada da geçerli ve "bir metodu bir backend'in uygulaması, ötekinin yok sayması" bu repo'nun tekrarlayan sessiz no-op sınıfı (`logstore.go` Pod şerhi bunu açıkça yazıyor).

---

## 5. Performans

### 5.1 Bugün uygulanan korumalar (regresyon vermemeli)

| Koruma | Yer |
|---|---|
| `track_total_hits: 10000` ana aramada | `elasticsearch.go:1440` |
| `track_total_hits: false` histogram / forward-tail / pattern | `:1647`, `:1781`, `:1916`, `:2315` |
| Soft `timeout` her gövdede | `:1441`, `:1648`, `:1782`, `:1917` |
| Go tarafı tavan | `api_logs.go:39-41` — `logsSearchBudget 30s`, `logsContextBudget 10s`; timeseries `:1009` ayrıca 30s |
| İndeks daraltma | `es_indices.go:273` `queryIndices` — pencereye göre + 1 gün pay; servis-pinli sorguda operatör şablonu (v0.8.231) |
| PIT tutma niyeti | `Filter.WantCursor` (v0.9.286) — cursor kullanmayacak çağıran segment pinlemiyor |
| Kova sayısı tavanı | `api_logs.go:233` `logsHistogramMaxBuckets = 5000`; `floorBucketByWindow:242` **tavan bölme** (v0.9.287) |
| FE kova merdiveni | `logsBucketSec` — piksel bütçeli, ≤240 kova, ≥5s (`LogsHistogram.tsx:264-268`, tek kaynak v0.9.707) |
| Sonuç boyutu tavanı | `ACC_CAP = 2000` (`Logs.tsx:78`), `LogsTailMax = 500` (`logstore.go:26`) |
| Ayrı istek disiplini | histogram / fieldstats / liste **üç ayrı uç**; fieldstats yalnız açılışta (`LogFieldsPanel.tsx:57`), `staleTime 60_000` = sunucu TTL |
| Sınırlı basamak | fieldstats `size` yalnız 5\|20 (`LogFieldsPanel.tsx:54`); v0.8.270 disiplini |
| Odak yenilemesi kapalı | `lib/queries/logs.ts` — `staleTime 15_000`, `refetchOnWindowFocus:false` (v0.8.3 PIT churn) |

### 5.2 Bulgular

**🔴 P1 — `_source` kısıtı yok.** `elasticsearch.go:1412-1441` arama gövdesinde `_source` yok; ES tam dokümanı dönüyor. 200 alanlı bir ECS dokümanında sayfa başına 100 satır × tam `_source` — koordinatör bellek + tel baytı. `mapHit` yalnız sayılı yolları okuyor. Düzeltme: `_source` include listesi = *çözümlenen alan yolları* ∪ *seçili sütunlar* ∪ *pivot için gereken trace/span/pod yolları*. **Dikkat:** `mapHit` çok-şekilli okuduğu için include listesi tüm aday yazımları kapsamalı; dar bir liste satırı sessizce boşaltır (v0.8.229 sınıfı). Kazanç **ölçülmedi** — dilim 1'in ilk perf işi ölçüm olmalı.

**🟡 P2 — `logs-fields` cache anahtarı sabit + TTL 60s.** `api_logs.go:530`. Mapping saatte bir değişmiyor; TTL 10 dk'ya çıkarılabilir (`envFieldTTL` emsali). Anahtar env/cluster taşımıyor ama yanıt da taşımıyor — çapraz zehirlenme yok, doğru.

**🟡 P2 — imza gruplama tam tarama riski.** `GroupBySignature` eklenirken tek kabul edilebilir tasarım örneklemedir (`puller.go:14-24`). Örnek tavanı + pencere sunucuda kıskaçlanmalı; `limit` cache anahtarına girmeli (`logsFieldStatsKey`'in `size` gerekçesi, v0.9.1223).

**🟢 Not — `terminate_after` yalnız ilk sayfada.** `:1456-1470`. ES #40201 nedeniyle derin sayfalarda sessiz kırpma yapardı. Yeni uçlar bunu kopyalamamalı.

**🟢 Not — sampler.** `significant_text` dışında `sampler` agg kullanılmıyor. Dağılım uçları için gerekmiyor (terms zaten `size`-sınırlı), imza gruplaması için **gerekli** olabilir — `background_filter` + `sampler` ikilisi INCIDENTS'ta yazılı.

---

## 6. Dilimleme, risk, dosya planı, test stratejisi

### 6.1 Dilim 1 (operatör tanımı) — S/M/L

| İş | Boy | Not |
|---|---|---|
| `internal/logql` ayrıştırıcı + AST + `ToES` | **M** | El yazımı token akışı (`dql.go:27-31` gerekçesi); harici bağımlılık yok |
| `logql.ToCH` + `field_resolve.go` | **L** | B1'i kapatan iş. `FieldStats:564-580` eşlemesi tek yere taşınır |
| `Search` yolunu `logql`'e bağlama (ES + CH) | **M** | `buildQuery:2684` dalı `expandShorthand` yerine AST'den derler |
| `ListFieldTypes` + `_field_caps` | **S** | `es_trace_context.go:263` hazır |
| Alan paneli tip rozeti | **S** | `LogFieldsPanel.tsx:249` |
| Histogram / DataTable / satır detayı / URL | **YOK — zaten var** | §3 |
| Pod pivotu satır menüsüne | **S** | `Filter.Pod` backend'de hazır |

**Dilim 1 gerçek kapsamı:** operatörün listesindeki 6 maddenin 4'ü şipşak; asıl iş **sorgu dili + parser + CH anlamı**.

### 6.2 Dilim 2 — S/M/L

| İş | Boy |
|---|---|
| `GroupBySignature` (ES + CH, örneklemeli) | **L** |
| `GET /api/logs/patterns` + cache + degrade | **M** |
| Frontend pattern görünümü (mevcut `/api/logs/templates`'i de bağla — bugün ölü uç) | **M** |
| Tail modu | **YOK — zaten var** |
| Kaydedilmiş sorgular | **YOK — zaten var** (`SavedViewsBar page="logs"`) |

### 6.3 Dosya planı — `api.go`'ya satır EKLENMEYECEK

**Bugünkü durum:** log rotaları `api.go:831-854`'te, `api_logs.go`'nun register fonksiyonu **yok**.

Plan:
1. Yeni dosya `internal/api/logs_routes.go`:
   ```go
   func init() { registerRoutesExtra("logs", (*Server).registerLogsRoutes) }
   func (s *Server) registerLogsRoutes(mux *http.ServeMux) { … }
   ```
   Emsal: `preferences_routes.go:40`, `admin_function_id.go:29`, `blast_radius_routes.go:25`. Defter: `route_registry.go:29-36`; `buildMux:50-60` adla sıralı boşaltıyor; çift ad init'te panic.
2. **Yeni** uçlar (`/api/logs/patterns`, gerekirse `/api/logs/field-types`) buraya.
3. **Mevcut** `api.go:831-854` bloğunu taşımak isteğe bağlı ve **ayrı** bir commit olmalı — taşıma sırasında bir rotanın düşmesi HTTP 404 değil, **boş sayfayla 200** verir (`/api-route` skill'inin sessiz-hata listesi). `TestMuxRoutePatterns` çakışmayı görür, **eksikliği görmez**.
4. Frontend dört dokunuş: `lib/types.ts` → `lib/api.ts` (⚠️ `qs()` `undefined|null|''|false` atar) → `lib/queries/logs.ts` (`staleTime` = sunucu TTL, `enabled:` ile fetch-on-open) → `lib/queries/index.ts` barrel; yeni hook `lib/queries/cancellation.test.ts` listesine kaydolmalı.

### 6.4 Test stratejisi

**Parser tablo testleri** (`internal/logql/parse_test.go`) — repo standardı: saf, tablo-güdümlü, stdlib, testify yok. Emsal: `internal/logstore/query_syntax_test.go`, `internal/logstore/signature_test.go`.
Kapsanacak: örtük AND · `NOT` önceliği · parantez iç içe · tırnaklı değerde iki nokta (`"connection refused: timeout"` — `query_syntax.go:44` bu tuzağı zaten belgeliyor) · `_exists_` · `key:(a OR b)` · `key:>=v` · kaçışlı tırnak · **her operatör her iki derleyicide** (ES + CH) — CLAUDE.md "value+unit her birimi test et" kuralının sözdizimi karşılığı.

**ES uç testleri — repoda ES httptest mock'u YOK.** Doğrulandı: `internal/logstore/` içinde `httptest` **0 dosya**. Mevcut desen **saf gövde-kurucu** testi: `elasticsearch_histogram_test.go:1-40` `buildHistogramBody`'yi çağırıp üretilen JSON'daki maliyet korumalarını (`min_doc_count`, `timeout`, `track_total_hits`) doğruluyor. Aynı desen: `elasticsearch_test.go`, `es_rawsearch_payload_test.go`, `es_group_fields_test.go`.
→ `logql.ToES` **tam bu şekilde** test edilir: AST → `map[string]any` → shape assert. HTTP mock'a gerek yok, eklemeyin.

**API katmanı:** `internal/api/logs_degrade_test.go:24-30` deseni — arayüzü gömen scripted `logstore.Store`, dokunulmaması gereken metotlarda panic. `/api/logs/patterns` için aynısı: degrade sözleşmesi (`ErrBackendSlow` → 200 `{degraded:true}`) + cache anahtarı saf testi (`logs_context_key_test.go` / `cache_key_test.go` emsali).

**Frontend vitest:** `lib/logFilters.test.ts`, `lib/logsUrl.test.ts` zaten var. Sorgu kutusu değişirse `KqlSearchInput.contract.test.tsx` genişletilir. Kapılar: `npx tsc --noEmit && npm run lint && npm run test -- --run`.

### 6.5 Risk

| Risk | Şiddet | Aşı |
|---|---|---|
| `logql` ES çıktısı bugünkü `query_string` semantiğinden saparsa kaydedilmiş görünümler + alarmlar **sessizce** başka şey sorar | 🔴 | Altın-dize testi: bugünkü `expandShorthand` çıktısı ile `ToES` çıktısının eşdeğerliği, mevcut `?filters=` biçimlerinin tamamı üzerinde |
| `_source` include listesi dar → satırlar sessizce boşalır | 🔴 | v0.8.229 sınıfı; include listesi `mapHit`'in okuduğu **tüm** yazımları kapsamalı, testi `mapHit` ile aynı sabit listeden türetilmeli |
| CH `key:value` derlemesi res-array üzerinde tam tarama yapar | 🟡 | `FieldStats:564+` gruplama kıskaçları + zaman-sınırlı WHERE + `LIMIT` + `SETTINGS max_execution_time` (CLAUDE.md sert kısıt) |
| İmza gruplama tam tarama | 🟡 | Örnekleme zorunlu (`puller.go:14-24`); sunucu-tarafı tavan |
| `case_insensitive` geri gelme cazibesi | 🟡 | ES 8.x reddediyor (INCIDENTS + `elasticsearch.go:2697-2712`); `logql` derleyicisi **üretmemeli** |
| Rota taşıma sırasında bir uç düşer | 🟡 | 404 değil, boş sayfayla 200; taşıma ayrı commit + elle curl listesi |
| Alan tipleri `_field_caps` yanıtı büyük küme | 🟢 | `listFieldsMax=500` kırpması + `ensureConventionalFields` aynen uygulanır |

---

## Ek: doğrulanmayanlar

- `_field_caps?fields=*`'ın binlerce dinamik yollu indekste yanıt boyutu ve gecikmesi — **ölçülmedi**.
- `_source` kısıtının gerçek kazancı (bayt / koordinatör bellek) — **ölçülmedi**.
- İmza gruplamasının 1B doküman/gün'de örneklemeyle 2s bütçesine sığıp sığmadığı — **ölçülmedi**; CLAUDE.md bütçesi `/api/logs/patterns` için 2s diyor ama uç bugün mevcut değil.
- `evaluateLogQuery`'nin tam yolu bu audit'te okunmadı; `query_syntax.go:29-33` şerhinin iddiası (alan yazımlı kural CH'de daima 0) **şerhe dayanıyor**, çağrı yeri doğrulanmadı.

## Onay soruları

1. Dilim 1'in ağırlığı **sorgu dili + Go parser + CH anlamı** (B1/B2) olsun; histogram/DataTable/drawer/URL zaten var — kabul mü?
2. Yeni `internal/logql` paketi (dql genişletilmez; `FilterExpr` IR + `LogExpr` ağacı) — kabul mü?
3. `/api/logs/patterns` `logstore.NormalizeSignature` üstünde, örneklemeli, Dilim 2 — kabul mü?
4. `_source` include listesi işi Dilim 1'e perf kalemi olarak girsin mi (önce ölçüm)?

---

## Durum (2026-09-03)

Dilim 1 KAPANDI: `internal/logql` parser + CH derleyici, üç CH yolu tek yüklem, `BackendUnderstandsFieldQuery("clickhouse")=true` (v0.10.279) · alan tipleri (ES mapping'den; `_field_caps` gerekmedi) + CH `ListFieldsBounded` + panel tip rozeti + CH'de sözdizimi hatası 400 (280) · rotalar `logs_routes.go` defter kaydı (281) · pod pivotu satır menüsü (282). Kararlar: ES yolu değişmedi (query_string sahibi; `ToES` yazılmadı); serbest metin örtük AND (ES ile hizalı); namespace filtresi identity.go sözlüğü, histogram kırılımı kendi zinciri (birleştirme ayrı dilim). Kalan: `_source` include ölçümü (lokalde ES yok → prod). Dilim 2 (patterns) onaylı, başlanmadı.
