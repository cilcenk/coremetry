---
name: otlp-converter
description: Checklist for every change to Coremetry's OTLP→ClickHouse conversion (internal/otlp/) — the field-by-field map of what reaches CH and what is silently dropped, plus the six things that break quietly — span links, exemplars, attribute routing (promoted → well-known → generic array), semconv alias chains and their back-compat contract, unknown-field policy, and the golden-payload test obligation. Use BEFORE editing convert.go, semconv.go, the ingest handlers, or any chstore column an OTLP field feeds. Do NOT use for CH schema design (use /clickhouse-schema), for OTel SDK/collector configuration outside this repo, or for reading conventions (use /otel-conventions).
---

# /otlp-converter — dönüşüme dokunuyorsan bu liste

Tek giriş noktaları: `ConvertTraces` (`convert.go:30`) + `convertSpan`
(`:89`) + `appendSpanLinks` (`:68`) · `ConvertLogs` (`:174`) ·
`ConvertMetrics` (`:225`) + `convertMetric` (`:281`) + `appendExemplars`
(`:392`).

> **`make audit`'in OTLP ile ilgili TEK CHECK'i yok** (doğrulandı: 0
> eşleşme). Bu alanda otomatik koruma sıfır — bu liste onun yerine geçiyor.

## 1. Alan haritası

### Span (16 proto alanı): 7 tam · 4 kısmi · **5 kayıp**

| OTLP | CH | Durum |
|---|---|---|
| `trace_id`, `span_id`, `parent_span_id`, `name`, `status` | aynı adlı kolonlar | TAM |
| `kind` | `kind` | TAM ama **daraltma: 6 girdi → 5 çıktı** (`UNSPECIFIED` + `INTERNAL` → ikisi de `internal`) |
| `start_time_unix_nano` | `time` DateTime64(9) | TAM — **`0` gelirse 1970 partition, fallback YOK** |
| `end_time_unix_nano` | yalnız `duration` | KISMİ; negatif sessizce 0'a kelepçelenir |
| `attributes` | `attr_keys`/`attr_values` + 11 tipli kolon | KISMİ (tip düzleştirme) |
| `events` | `spans.events` JSON blob | KISMİ (map daraltması) |
| `links` | `span_links` tablosu | KISMİ (6 alandan 3'ü) |
| **`trace_state`** | — | **KAYIP** (0 referans, doğrulandı) |
| **`flags`** | — | **KAYIP** — W3C `SAMPLED` biti + `PARENT_IS_REMOTE` |
| **`dropped_attributes_count`** | — | **KAYIP** |
| **`dropped_events_count`** | — | **KAYIP** |
| **`dropped_links_count`** | — | **KAYIP** |

**Hata kuralı TEK ve KATI:** `status_code = 'error'`. **HTTP 5xx tek başına
hata değildir** — `http_status >= 500` yalnız alarm değerlendiricisinin
ayrı filtresi. Bunu değiştirmek 8+ okuma yolunu ve iki MV'yi etkiler.

### Resource / Scope

`Resource.Attributes` **tümü verbatim** → `res_keys`/`res_values`.
`service.name` (yoksa `"unknown"`), `host.name`, `deployment.environment`
(çift-adlı zincir, v0.8.379), `Scope.Name` → kendi kolonlarına.
**KAYIP:** `Scope.Version`, `Scope.Attributes`, `Resource.entity_refs`,
her iki `schema_url`, tüm `dropped_*_count`.

### Span Event → `spans.events` JSON blob'u, ayrı tabloya GİTMİYOR

`ConvertLogs` span event'lerine hiç bakmaz; event'ler `logs` tablosuna
girmez. Attribute'lar `map[string]string`'e düşürülür → **sıra kaybolur,
yinelenen anahtar çakışır (son yazan kazanır)**.
`dropped_attributes_count` kayıp. Boş event `""` yazılır, `"[]"` değil
(bilinçli alloc tasarrufu; okuma korumalı).

**Exception ayıklaması Go'da DEĞİL, CH MATERIALIZED kolonlarında**
(`ex_match`/`ex_type`/`ex_msg`/`ex_stack`; ilk exception event'i konumdan
bağımsız `arrayFirst` ile, v0.8.563). İkinci besleme yolu: exception
event'siz ama `error.type` attr'lı hata span'leri (v0.8.494).
`cluster_name` unset dış Distributed'da ALTER atlanır → okuma ifade yoluna
düşer: davranış korunur, **yavaşlar**.

### Span Link (6 alan): 3 taşınıyor, 3 kayıp

`trace_id`/`span_id`/`attributes` taşınıyor; **`trace_state`, `flags`,
`dropped_attributes_count` kayıp**. Link'in kendi zamanı OTLP'de yok →
`Time` = **sahip span'in başlangıç zamanı**.

> **Bu, kod tabanındaki en DÜRÜST dönüşüm hattı:** geçerlilik kapısı +
> `spanLinksIngested`/`spanLinksDroppedInvalid` sayaçları + `/admin/stats`
> + testler. Diğer bütün kayıplar sessiz. §3'teki politika bu hattı
> standart ilan ediyor.

### Metrik: 5/5 tip işleniyor

Gauge · Sum · Histogram · ExponentialHistogram · Summary — hiçbiri komple
düşmüyor. Ama `switch d := m.Data.(type)` **`default` dalsız**
(doğrulandı) → `m.Data == nil` sessizce 0 satır, log yok.

| Alan | Durum |
|---|---|
| `name`, `description`, `unit`, `data`→`instrument` | TAM |
| `Metric.metadata` | **KAYIP** |
| **`ScopeMetrics.scope`** | **KAYIP** — `metric_points`'te `scope_name` kolonu yok; span/log'ta var → **asimetri** |
| `start_time_unix_nano` | yazılıyor, **hiç okunmuyor** |
| `value` | TAM — **int64 > 2^53 hassasiyet kaybı** |
| `min_value` | yazılıyor, **hiç okunmuyor** (`max_value` okunuyor) |
| `bucket_counts`/`bucket_bounds` | yalnız **İKİSİ de doluysa**; aksi hâlde satır avg-only'ye düşer |
| DP `flags` | **KAYIP** (her tipte) |
| `aggregation_temporality`, `is_monotonic` | TAM |

## 2. Kontrol listesi — HER değişiklikte

### 2.1 Span links
- [ ] `appendSpanLinks` hâlâ `parentID` mi kullanıyor (all-zero → `""`),
      `hexID` değil?
- [ ] `ConvertTraces`'in **ikinci dönüş değeri** HER İKİ taşımada da
      tüketiliyor mu — HTTP **ve** gRPC? (Birini unutmak linkleri sessizce
      yok eder.)
- [ ] Geçerlilik kapısı + iki sayaç yerinde mi, `/admin/stats`'a çıkıyor mu?
- [ ] `Time` hâlâ sahip span'in başlangıç zamanı mı?
- [ ] `go test ./internal/otlp/ -run 'Link'`

### 2.2 Exemplar
- [ ] **Dört** DP tipinde de `appendExemplars` çağrısı duruyor mu?
      Summary'de OLMAMALI (proto'da alan yok).
- [ ] `series_fingerprint` data point ile exemplar satırlarında **AYNI** mı?
      Farklıysa pivot join sessizce boş döner.
- [ ] `trace_id`/`span_id` `parentID` ile mi normalize ediliyor?
      `spans.trace_id` ile aynı kodlama olmadan join tip uyuşmazlığına düşer.
- [ ] `timeUnixNano == 0` çapası duruyor mu? Kaldırılırsa 1970 satırı.
- [ ] `filtered_attributes` nil → `map[string]string{}` dönüşümü INSERT'te
      duruyor mu? CH Map kolonu nil reddeder.
- [ ] `require_trace_context` (varsayılan **true**) ve cap kapıları hâlâ
      Ingester'da mı — convert'te DEĞİL? (Tek politika, iki taşıma.)

### 2.3 Attribute yönlendirme
- [ ] **Çift saklama korundu mu?** Terfi kolonu çıkarımı `sp.Attributes`'tan
      **okumalı**, slice'tan eleman **çıkarmamalı**.
- [ ] Okuma üçlüsünün sırası **üç yerde de** aynı mı:
      **terfi kolonu → well-known semconv kolonu → `attr_values[indexOf(...)]`**
      (`filterexpr.go`, `repo.go`, `business_dims.go`).
- [ ] `wellKnown` ile `wellKnownResource` **ayrı** mı? Birleştirilirse
      `resource.http.method` span-attr kolonuna düşer.
- [ ] Yeni tipli kolon: `hasXCol` boot probe + koşullu INSERT +
      `cluster_name` unset'te ALTER atlama (→ `/clickhouse-schema` §9).
- [ ] `spanAppendArgs`/`metricAppendArgs` argüman sırası kolon listesiyle
      **birebir** mi? **Aynı-tipli komşu takası hiçbir testi kırmaz.**
- [ ] `attrsToArrays`'in `cap == len` özelliği bozulmadı mı? Prealloc
      büyütmek pipeline enrich'in append dalını paylaşılan array'e yazdırır.

### 2.4 Semconv geriye uyum
- [ ] `attrFirst`/`attrIntFirst` semantiği: **ilk BOŞ-OLMAYAN değer kazanır**
      — "anahtar var" ≠ "değer var".
- [ ] Yeni yazım eklendiyse **üç harita birlikte** güncellendi mi:
      `spanAttrAliases` + `wellKnown` + `WellKnownTraceCol`?
- [ ] `go test ./internal/otlp/ -run Semconv` — sözlük üzerinden dönen test
      yeni alias'ı otomatik kapsar.
- [ ] `cmd/demo/semconv_mix.go` listesi `spanAttrAliases` ile eşleşiyor mu?
      (Bugün eşleşiyor ama **bunu çiviyen test YOK** — §7.)
- [ ] **Tip ekseni:** yeni `attrIntFirst` eşlemesi ekleniyorsa string ve
      double varyantı da test edildi mi? Bugünkü testlerin hepsi `kvInt`
      kullanıyor.
- [ ] Frontend + acache katmanı güncellendi mi (`acache.go`,
      `useAttributeKeys.ts`, `ColumnManager.tsx`, `TraceWaterfall.tsx`,
      `Trace.tsx`)? **v0.9.628 bunları atladı.**

### 2.5 Bilinmeyen alan davranışı
- [ ] Yeni OTLP proto sürümü: yeni `AnyValue` case'i eklendi mi?
      `anyValStr` bilinmeyen case'te `""` döndürüyor — **sessiz**.
- [ ] `convertMetric`'in `switch`'i hâlâ `default` dalsız mı? Yeni metrik
      tipi 0 satır üretir, log yazmaz.
- [ ] `kindStr` yeni bir kind'ı `default → "internal"`e mi düşürüyor?
- [ ] Yeni alan §3 politikasına göre ya taşınır ya **sayaçla** düşürülür.

### 2.6 Golden test
- [ ] **Bugün golden test YOK** — `internal/otlp/testdata/` dizini hiç yok
      (doğrulandı). Yeni dönüşüm kodu golden testle birlikte gelir.
- [ ] Golden test **tam-struct** karşılaştırması mı (`want chstore.Span`
      literali)? Alan-alan iddia yetmez — **yeni alan eklendiğinde test
      kırılmak ZORUNDA.**
- [ ] Değişen alan fixture'da kapsanıyor mu?
- [ ] `go test ./internal/otlp/ -cover` — taban **%82.2**, düşmedi mi?

### 2.7 Kapanış
`go build ./...` · `go test ./...` · `go vet ./...` (protobuf `copylocks`
tuzağı, v0.9.726) · `make audit` (bu alanda koruma **yok**).

## 3. Bilinmeyen alan politikası

**Bugünkü fiili durum:** anahtar düzeyinde politika **doğru** — hiçbir
attribute atılmıyor, allowlist/denylist yok; tam fidelity duruşuyla
tutarlı. **Tip ve alan düzeyinde politika YOK, her şey sessiz:**

| Kategori | Bugün | Sonuç |
|---|---|---|
| Bilinmeyen `AnyValue` tipi | `""` döner | anahtar kalır, değer boşalır — sessiz |
| Bilinmeyen metrik `Data` tipi | `switch` default'suz | metrik tamamen kaybolur — sessiz |
| Bilinmeyen span kind | `→ "internal"` | sessiz daraltma |
| Tanınan ama beklenmedik tip | `attrInt` → 0 | **sessiz + yanıltıcı** |
| Degrade exp histogram | 4 koşulda boş kova | sessiz kalite düşüşü |

> **KURAL — bilinmeyen alan sessizce düşmez.** Her bilinmeyen/desteklenmeyen
> dal üç sınıftan birine düşer ve sınıf kodda **AÇIKÇA** seçilir:
>
> **A. TAŞI** (varsayılan) — değer generic diziye/blob'a girer, tip bilgisi
> kaybolsa bile değer korunur.
> **B. DEGRADE ET + SAY** — değer korunamıyorsa satır yazılır, bir sayaç
> tıklar ve sayaç `/admin/stats`'a çıkar. *(Bugün yalnız exemplar ve span
> link bu sınıfta — örnek alınacak hat budur.)*
> **C. DÜŞÜR + SAY + LOGLA** — satır hiç yazılmıyorsa sayaç **ve**
> rate-limited log şart.
>
> Sessiz `return ""`, sessiz `default:`, sessiz `return nil,nil,false`
> yeni kodda **kabul edilmez**.

## 4. Semconv geçiş yordamı

**Desen A — merkezi alias tablosu** (`internal/otlp/semconv.go`), 4 çift:

| Kolon | Öncelik (yeni → eski) |
|---|---|
| `http_method` | `http.request.method` → `http.method` |
| `http_status` | `http.response.status_code` → `http.status_code` |
| `db_system` | `db.system.name` → `db.system` |
| `db_statement` | `db.query.text` → `db.statement` |

**Desen B — tek-kullanımlık zincirler** (tablo dışında, 3 adet):
`deploy_env`, `http_route` (üç kollu), `messaging.destination` (tipli kolon
yok, SQL coalesce'ı).

**Kapsam dışı ve gerekçeli:** `peer.service` → `server.address` **yapılmaz**
— semantik farklı (mantıksal servis adı vs host), topoloji düğüm adlarını
bozar. Aynı şekilde `url.full`, `client.address`, `db.operation.name`,
`db.namespace` (karşılık gelen kolon yok).

**Yeni geçiş eklerken dokunulacak TAM liste:**
1. `internal/otlp/semconv.go` — `spanAttrAliases` (yeni ad ÖNCE)
2. `internal/chstore/filterexpr.go` — `wellKnown`, aynı kolona
3. `internal/chstore/repo.go` — `WellKnownTraceCol`
4. `cmd/demo/semconv_mix.go` — demo iki yazımı da üretsin
5. Test: `semconv_test.go` sözlük-üzerinden dönen testler otomatik kapsar
6. Frontend: `acache.go`, `useAttributeKeys.ts`, `ColumnManager.tsx`,
   `TraceWaterfall.tsx`, `Trace.tsx`

**Harf-duyarlılık:** attribute anahtarları harf-duyarlı eşleşir. Bu sınıf
prod'u bir kez kırdı (v0.9.626: `CHANNEL_CODE` vs `channel_code`, 11 gün
boş kolon, 10 dk penceresinde küçük harfli 2.67M span / büyük harfli
sıfır). Yeni bir anahtar eklerken **prod'da hangi yazımın geldiğini ÖLÇ** —
varsayma. Teşhis anlatısı `/perf-triage` örnek vakasında.

## 5. Golden test — bugün yok, nasıl kurulur

Mevcut testler elle kurulmuş proto struct'ları üzerinden çalışıyor;
`testdata/` altında kayıtlı payload yok.

```go
// convert_golden_test.go — v0.9.X
// Girdi: testdata/<vaka>.json (OTLP JSON, gerçek collector çıktısı)
// Beklenen: TAM chstore.Span literali — alan-alan iddia DEĞİL.
// Neden tam struct: yeni bir alan eklendiğinde bu test KIRILMAK ZORUNDA;
// kırılmıyorsa alanın taşındığını kimse doğrulamamış demektir.
func TestConvertSpanGolden(t *testing.T) {
    raw := mustReadTestdata(t, "span_http_server.json")
    spans, links := ConvertTraces(mustUnmarshalTraces(t, raw))
    want := []chstore.Span{{ /* … TÜM alanlar … */ }}
    if diff := cmp.Diff(want, spans); diff != "" {
        t.Fatalf("span mismatch (-want +got):\n%s", diff)
    }
    _ = links
}
```

Fixture'ın kapsaması gereken, **bugün test edilmeyen** dallar: span links ·
exemplar (dört DP tipi) · exponential histogram degrade dalları ·
array/kvlist attribute · `start_time == 0` · her semconv çiftinin **eski**
yazımı.

## 6. Sessizce bozulanlar

| Ne | Sonuç | Yakalayan |
|---|---|---|
| `spanAppendArgs` argüman sırası takası (aynı tip) | Kolonlar yer değiştirir, veri "geçerli" görünür | **hiçbiri** |
| İkinci taşımada (gRPC) link tüketimini unutmak | Linkler sessizce yok | **hiçbiri** |
| `series_fingerprint` ayrışması | Exemplar pivot join'i boş | **hiçbiri** |
| Terfi kolonu çıkarımının slice'tan eleman çıkarması | Generic dizi eksilir, filtre sessizce boş | **hiçbiri** |
| Yeni `AnyValue` case'i | Değer boşalır | **hiçbiri** |
| Demo listesi ile alias tablosunun ayrışması | Demo yanlış şeyi doğrular | **hiçbiri** |

## 7. Açık sorular

1. **33 protokol alanı kayıp.** En ucuz düzeltme: `schema_url` +
   `dropped_*_count` saf ek kolon olarak. Dilim mi?
2. **`ScopeMetrics.scope` asimetrisi** — span/log'ta `scope_name` var,
   metrikte yok.
3. **`min_value` ve `start_time` yazılıyor ama hiç okunmuyor** — ölü kolon
   mu, gelecek özellik mi?
4. **`trace_state` + `flags`** — W3C `SAMPLED` biti kayıp olduğu için
   "bu span örneklenmiş miydi" sorusu cevaplanamıyor.
5. **Demo↔alias eşleşmesini çiviyen test yok** — tek satırlık kapı.
