# Trace View Audit — Coremetry
*Tarih: 2026-09-02 · Kapsam: /trace yüzeyi (frontend + backend + CH) · SALT OKUMA denetimi*
*Hedef çıta: Dynatrace / Jaeger trace görünümü. Her iddia dosya:satır ile kanıtlı; ölçülmemiş
şeyler "tahmin" / "doğrulanmadı" olarak işaretli. Keşif Explore ajanıyla yapıldı, ana oturum
kilit iddiaları bağımsız grep ile doğruladı (§0). Durum: ONAY BEKLİYOR — kod yok.*

## 0. Özet ve premis düzeltmeleri

- **Span link'leri artık düşmüyor.** Brief "convertSpan link'leri düşürüyor, kapatılması önkoşul"
  diyordu; bu bilgi bayat. v0.8.329'dan beri link'ler `span_links` (+ `span_links_reverse` MV)
  tablosuna yazılıyor, `GET /api/traces/{id}/links` okuyor, UI şeridi gösteriyor (§2.3 kanıt
  tablosu). **CH kolon migration'ı GEREKMİYOR.** Gerçek boşluk: link'ler span düzeyinde
  görünmüyor, SpanDetail'de Links bölümü yok, Tempo yolunda link okunmuyor,
  `dropped_links_count` daima 0.
- **Kritik yol ve self time istemcide zaten var** (`lib/criticalPath.ts`, `lib/selfTime.ts`);
  ancak sayfada İKİ farklı self-time tanımı yaşıyor (§1.4). Backend'e taşıma kararı = tek tanım.
- **İki trace karşılaştırma var** (`pages/TraceCompare.tsx`); Dilim 3 sıfırdan değil, iyileştirme.
- **Sanallaştırma yok**, `content-visibility` + 1500 span üstü otomatik katlama var; 1000-1500
  arası trace tamamen açık basılıyor (§1.3). `TraceCompare.tsx:40` yorumu yanlış güven veriyor.
- Öneri: ağaç + kritik yol + self time + servis özeti **Go'da tek payload** (`analysis?:` optional
  alan, cache `trace:v3:`), Dilim 1 beş alt dilim (1a→1b→1c→1e→1d), en riskli sanallaştırma sona.

---

## 1. Mevcut trace view

### 1.1 Bileşen haritası

| Dosya | Satır | Rol |
|---|---:|---|
| `frontend/src/pages/Trace.tsx` | 1390 | Sayfa kabuğu, veri çekme, kritik yol, filtre, sekmeler |
| `frontend/src/components/TraceWaterfall.tsx` | 831 | Şelale + servis öz-süre şeridi (`TraceServiceBreakdown`) |
| `frontend/src/components/traceWaterfall.tree.ts` | 66 | Saf ağaç yardımcıları (`collectSubtreeIds`, `clusterBadge`, `groupParentOf`) |
| `frontend/src/components/SpanDetail.tsx` | 633 | Sağdaki span detay paneli (drawer DEĞİL — inline flex sütun) |
| `frontend/src/components/TracePeekDrawer.tsx` | 245 | `/logs` içinden trace ön-izleme (`Modal`, tam sayfayı açmadan) |
| `frontend/src/pages/TraceCompare.tsx` | 437 | İki trace yan yana + hizalanmış fark tablosu |
| `frontend/src/pages/PublicTrace.tsx` | 197 | Anonim paylaşım görüntüleyici |
| `frontend/src/components/traces/TraceHonesty.tsx` | — | Provenance şeridi (orphan/root/sampling/dropped) |
| `frontend/src/lib/criticalPath.ts` | 118 | İstemci kritik yol (`computeCriticalPath:46`) |
| `frontend/src/lib/selfTime.ts` | 46 | Aralık-birleşimli öz süre (`childCoverageNs:19`, `selfTimeMs:41`) |
| `frontend/src/lib/spanAlign.ts` | — | İki trace span eşleme (`alignTraces`) |
| `frontend/src/lib/traceRepeats.ts` | — | N+1 / fan-out tekrar grupları |

`TraceWaterfall` tüketicileri **üç**: `Trace.tsx:582`, `TraceCompare.tsx:265`,
`PublicTrace.tsx:156`.

### 1.2 Veri akışı

```
/trace?id=X
  └─ Trace.tsx:143  api.trace(id)                      (raceGuard'lı, react-query DEĞİL)
       └─ GET /api/traces/{id}                         api.go:818 → getTrace api.go:4539
            └─ serveCached("trace:v2:"+id, 30s)        api.go:4550, cache.go:218
                 └─ resolveTraceSpans                  trace_resolve.go:78
                      ├─ 1) Tempo (3 sn bütçe)         trace_resolve.go:65 tempoFirstBudget
                      └─ 2) chstore.GetTrace           repo.go:4359
```

**Payload şekli** — `TraceDetailResponse` (`lib/types.ts`, `SpanRow` `:2496`):
`{traceId, spans[], source, spanCapped?, spanTotal?, stub?}`. Ağaç **sunucuda
kurulmuyor**: düz span listesi `ORDER BY time ASC LIMIT 50000` ile geliyor
(`repo.go` GetTrace SELECT bloğu), ağaç **istemcide** iki ayrı yerde kuruluyor —
`TraceWaterfall.tsx:255-278` (`tree` memo) ve `Trace.tsx:189-220`
(`orderedSpanIds`, j/k gezinme için ikinci bir DFS).

**Boyut tahmini (ÖLÇÜLMEDİ, hesap):** her span kendi `attributes` **ve**
`resourceAttributes` haritasını taşıyor (`chstore.SpanRow`). Resource attribute'ları
trace boyunca **span başına tekrar ediyor** — 1000 span'lik bir trace'te aynı
`k8s.*`/`service.*`/`host.*` seti 1000 kez. Kaba hesap: taban alanlar ~250 B +
attrs 300-800 B + res-attrs 400-900 B ≈ **1.0-2.0 KB/span**. 5000 span →
**5-10 MB ham JSON**, gzip sonrası ~0.6-1.2 MB. Bu, tavanın (50k) onda biri.

### 1.3 Render performansı — 1000+ span

- **Sanallaştırma YOK.** `TraceWaterfall.tsx:578` `rows.map(...)` ile TÜM görünür
  satırları DOM'a basıyor. Kurtarıcı iki mekanizma:
  1. `content-visibility: auto` — `globals.css:1432-1445` (sınıf) **ve**
     `TraceWaterfall.tsx:686` (satır içi). ⚠️ İkisi çelişiyor:
     CSS `contain-intrinsic-size: auto 22px`, inline `auto 28px` — inline kazanıyor,
     yani scrollbar tahmini 22px varsayan CSS ile uyumsuz (kaydırma çapası sapması).
  2. **Otomatik katlama** — `TraceWaterfall.tsx:225-231`: `spans.length > 1500` ise
     çocuğu olan HER span katlanıyor. Yani 1000-1500 arası trace **tamamen açık**
     basılıyor; 1500 üstü kök satırlara iniyor.
- `VirtualTable` / `VirtualList` primitifleri repoda VAR (`components/ui/VirtualTable.tsx`,
  `Traces.tsx:1387` kullanıyor) ama şelale bunları **kullanmıyor**. Gerekçe
  `globals.css:1437-1442` yorumunda yazılı: sabit yükseklikli scroller sayfa
  kaydırmasını bozardı. Bu gerekçe hâlâ geçerli ama `VirtualList` (değişken
  yükseklik + sayfa scroll'u) bugün mevcut — gerekçe güncellenmedi.
- **`TraceCompare.tsx:40` yorumu YANLIŞ**: "TraceWaterfall already virtualises long
  traces internally". Sanallaştırma yok; content-visibility var. Bu yorum yanlış bir
  güven yaratıyor ve compare sayfasında **iki** şelale birden basılıyor.

### 1.4 Memo / effect tuzakları

- ✅ `timeRangeToNs` **hiçbir trace dosyasında yok** (grep: Trace/TraceCompare/
  TraceWaterfall/SpanDetail/PublicTrace = 0 eşleşme). v0.5.184 sınıfı bu yüzeyde temiz.
- ⚠️ **`Math.min(...spans.map(...))` spread** — `TraceWaterfall.tsx:268-269` ve
  `Trace.tsx:355-356`. 50k elemanlı spread bazı motorlarda `Maximum call stack size
  exceeded` verir. Tavan 50k olduğu için bu **erişilebilir** bir sınır.
  (Prod'da tetiklendiği doğrulanmadı — teorik.)
- ⚠️ **`Trace.tsx:353-366` her render'da `spans.find` × 2 + `filter` + `Set` + iki
  spread** — hook değil, memo yok. Seçili span değişiminde 5 × O(N) tarama.
- ⚠️ **İki farklı "self time" tanımı aynı sayfada.**
  `TraceWaterfall.tsx:118-122` (servis şeridi) doğrudan çocukların sürelerini **naif
  toplayıp** düşüyor; `lib/selfTime.ts:19` (span paneli) **aralık birleşimi** yapıyor.
  Paralel/async çocuklu bir span'de iki sayı ayrışır — üstelik `selfTime.ts` başlığı
  naif toplamı açıkça "YANLIŞ" diye reddediyor. Aynı trace'te üst şerit ile panel
  farklı öz süre gösterebilir.
- ⚠️ `TraceWaterfall.tsx:233-236` — `collapsed` state'i `initialCollapsed` her
  değiştiğinde sıfırlanıyor. `initialCollapsed` `[spans, defaultCollapsed]`'e bağlı,
  `spans` da state olduğu için pratikte trace başına bir kez; ama `spans` referansı
  değişen herhangi bir gelecek refactor operatörün açtığı dalları sessizce kapatır.
- ☠️ **Ölü prop:** `defaultCollapsed` (`TraceWaterfall.tsx:174`) — üç çağıranın
  hiçbiri geçmiyor (grep doğrulandı). `groupSimilar`'ın v0.9.226→v0.9.1277 arasında
  1000+ sürüm ölü kalması ile aynı sınıf.
- ⚠️ `Trace.tsx:168-178` URL yazıcısı ham `history.replaceState` kullanıyor ve
  router'ı haberdar etmiyor. Gerekçe yorumda yazılı ve savunulabilir, ama
  `/frontend-conventions §4`'ün `setSearchParams(prev, {replace:true})` kuralının
  **belgeli istisnası**; yeni bir URL alanı eklenirken bu yazıcıya katılmak ZORUNLU.
- ⚠️ `Trace.tsx:143` react-query **kullanmıyor** (elle `useState` + `raceGuard`),
  `TraceCompare.tsx:53-65` **kullanıyor** (`staleTime: 5dk`). Aynı veri için iki
  önbellek disiplini; /trace'te sekme dönüşü her seferinde yeniden fetch.

---

## 2. Backend: tek trace okuma zinciri

### 2.1 ClickHouse sorgusu

`internal/chstore/repo.go:4359 GetTrace`:
1. **Pencere bulma** — `trace_summary_5m`'den kademeli (`traceWindowSteps`) pencere;
   her adım `max_execution_time = 3`. v0.9.578 olayının çözümü: eskiden zaman
   sınırsızdı ve her trace açılışında 3 sn yakıyordu.
2. **Ana sorgu** — `FROM spans WHERE trace_id = ? AND time BETWEEN … ORDER BY time ASC
   LIMIT 50000 SETTINGS max_execution_time = 20`. Pencere çözülemezse
   `traceScanFloor` tabanı uygulanıyor (sınırsız tarama YOK). CLAUDE.md'nin
   "LIMIT + max_execution_time + zaman sınırlı WHERE" kısıtına **uyumlu**.

**Gelen kolonlar:** `trace_id, span_id, parent_id, name, kind, service_name,
host_name, time, duration, status_code, status_msg, attr_keys, attr_values,
res_keys, res_values, events, scope_name, db_system, db_statement, http_method,
http_route, http_status, peer_service`.
→ **events VAR** (JSON blob, `sp.Events` `interface{}` olarak unmarshal ediliyor).
→ **links YOK** (bu sorguda; §2.3).
→ `ex_*` MATERIALIZED kolonları (`/otlp-converter §1`) bu SELECT'te **çekilmiyor** —
   exception ayıklaması frontend'de `events.filter(e => e.name === 'exception')` ile
   tekrar yapılıyor (`SpanDetail.tsx:62`, `TraceWaterfall.tsx:648`).

### 2.2 Tempo fallback

`internal/api/trace_resolve.go:78` — **sıra: önce Tempo, sonra ClickHouse**
(v0.9.632 operatör kararı), 3 sn bütçe (`:65`). Tempo yanıtı `SpanRow`'a çevriliyor
ve **events taşınıyor** (`internal/tempo/client.go:399-407, 648-674`, v0.9.859
operatör bildirimi ile eklenmişti). **Tempo yolunda span link YOK** — `client.go`'da
link okuması yok (doğrulandı: `grep Links internal/tempo` → yalnız events yorumu).
Sonuç: Tempo-only bir trace'te "Linked traces" şeridi sessizce boş kalır.

### 2.3 SPAN LINK GAP — premis DÜZELTMESİ 🔴

> **Brief'te "BİLİNEN GAP: internal/otlp convertSpan span link'leri düşürüyor" deniyor.
> Bu bilgi BAYAT. Gap v0.8.329'da KAPANDI ve zincirin tamamı yerinde.**
> (Ana oturum bağımsız doğruladı: `internal/otlp/convert.go:53-98 appendSpanLinks`,
> `internal/chstore/span_links.go:66 spanLinksInsertSQL`, `internal/api/pivot.go:381 LinksFromTrace`.)

Kanıt zinciri:

| Aşama | Dosya:satır | Durum |
|---|---|---|
| Dönüşüm | `internal/otlp/convert.go:53-62`, `:88`, `:95 appendSpanLinks` | `ConvertTraces` **ikinci dönüş değeri** olarak link satırları üretiyor |
| gRPC taşıma | `internal/otlp/grpc.go:149-162` | linkleri tüketiyor, sayaçlarla |
| HTTP taşıma | `/otlp-converter §2.1` bunu kontrol maddesi yapıyor | (kod satırı doğrulanmadı, checklist var) |
| Yazma | `main.go:450-451` `consumer.NewSized("span_links", …, store.InsertSpanLinks)` | ayrı consumer |
| Tablo | `internal/chstore/store.go:1973` `span_links` | `ORDER BY (trace_id, time)` |
| Ters tablo | `store.go:1997` `span_links_reverse` | `ORDER BY (linked_trace_id, time)` |
| MV | `store.go:3993` `span_links_reverse_mv TO span_links_reverse` | tek yönlü kopya |
| Okuma | `internal/chstore/span_links.go:112 LinksFromTrace` / `:116 LinksToTrace` | iki PK taraması, JOIN yok |
| API | `internal/api/pivot.go:53` `GET /api/traces/{id}/links` | serveCached 30s |
| Tip | `lib/types.ts:5613-5630` `SpanLink` / `TraceLinks` | |
| İstemci | `lib/api.ts:801-802 traceLinks` | |
| UI | `Trace.tsx:760-836 LinkedTracesSection` | trace-düzeyi çip şeridi |
| Test | `internal/otlp/convert_test.go:295-428` | geçerlilik kapısı + iki sayaç |
| İkinci tüketici | `internal/chstore/messaging_e2e.go:120` | producer→consumer e2e korelasyonu |

**Yani `spans.links` kolonu YOK ve OLMAMALI** — mimari karar bilinçli ve
`/clickhouse-schema §O4`'te kanonlaşmış: `trace_id` **sol başta** (nokta araması,
link satırları span hacminin %1-5'i, kopyalamak ucuz). `store.go:1958-1965`
"nested kolon reddedildi" gerekçesini yazıyor. **Dilim 1'in önkoşulu olarak
kolon-ekleme migration'ı GEREKMİYOR** — bu iş zaten yapılmış.

**GERÇEK boşluklar (kapanması gereken):**

1. 🔴 **Link'ler span düzeyinde görünmüyor.** `LinkedTracesSection:771-789` iki yönü
   **trace başına dedupe** ediyor (`out:${linkedTraceId}` anahtarı) ve `spanId`
   alanını **atıyor**. Yani "bu span hangi span'e link veriyor" cevaplanamıyor;
   `SpanDetail.tsx`'te Links bölümü **hiç yok** (Section listesi: Attributes:278,
   Info:283, Kubernetes:343, Resource:349, Exceptions:355, Events:375, Logs:394,
   hotspots:451, profiles:476 — Links **yok**).
2. 🔴 **Async/messaging trace'lerde şelale link kenarlarını çizmiyor.** Bir consumer
   span'i producer'a `follows_from` linkiyle bağlıysa, waterfall bunu ne satırda
   ne barda gösteriyor; ağaç yalnız `parentSpanId` üzerinden kuruluyor
   (`TraceWaterfall.tsx:259-262`). Kuyruk üzerinden akan bir iş **iki kopuk trace**
   olarak okunuyor; tek ipucu üstteki çip şeridi.
3. 🟡 **`Time` = sahip span'in başlangıç zamanı** (`/otlp-converter §1`, link'in
   kendi zamanı OTLP'de yok) — link satırı zaman ekseninde konumlandırılamaz.
4. 🟡 **`dropped_links_count` kayıp** (`/otlp-converter §1`). `TraceHonesty.tsx:46`
   bunu `attributes['otel.dropped_links_count']`'ten okumaya çalışıyor; OTLP alanı
   taşınmadığı için **daima 0** — sessiz yanlış güven.
5. 🟡 **Tempo yolunda link yok** (§2.2).
6. 🟡 **Yön başına 100 tavanı** (`span_links.go:41-48`); UI tavanı `Trace.tsx:790`'da
   dürüstçe ilan ediyor ("⚠ ilk 100") — bu doğru desen.

**Eğer yine de yeni kolon gerekirse** (`/clickhouse-schema §3, §9`) — bu dilim için
GEREKMİYOR ama kural kayda geçsin: `spans` `highVolumeTables`'ta; CH mevcut
kolonlardan türetebiliyorsa **MATERIALIZED** (Distributed forward'ında silinir,
rolling deploy'da boşluk yok), Go yazacaksa **EXPLICIT + `hasXCol` probe + koşullu
INSERT + ALTER-skip**; küme kipinde DDL ertelenir → **iki-boot sözleşmesi**; probe
varlığı değil **veriyi** kanıtlamalı (v0.9.621).

---

## 3. Eksik özellikler — mevcut kodla mesafe

| # | Özellik | Durum | Nerede başlanır |
|---|---|---|---|
| 3.1 | **Kritik yol** | **VAR (istemcide)** | `lib/criticalPath.ts:46`; `Trace.tsx:272-280` hesap, `:357` toggle, waterfall `.wf-critical` + focus dimming `TraceWaterfall.tsx:619,628` |
| 3.2 | **Self time** | **KISMEN + ÇELİŞKİLİ** | `lib/selfTime.ts:41` (doğru, birleşim) panelde; `TraceWaterfall.tsx:118-122` (naif) şeritte. Şelale **satırında** self/total kırılımı YOK |
| 3.3 | **Servis renklendirme** | **VAR** | `TraceWaterfall.tsx:104-105 svcColorToken` → `traces/shared.tsx:14 svcColor` → `chartFmt.ts:153 seriesColor`. ⚠️ 10 **sabit hex**, tema-bağımsız (`/frontend-design-system §7` bilinçli diyor) — ama `.wf-cat` çipleri `var(--warn)` vb. token kullanıyor: **aynı satırda iki renk sistemi** |
| 3.4 | **statusColor** | **YOK (ayrı yol)** | Hata rengi sınıfla: `.wf-err` (`TraceWaterfall.tsx:632`); bar rengi servis rengini koruyor. Kanonik bir `statusColor()` fonksiyonu trace tarafında yok |
| 3.5 | **Collapsed alt ağaç özeti** | **KISMEN** | Katlanmış satır çocuk sayısı/süresi/hata sayısı GÖSTERMİYOR. Yalnız `groupSimilar` ×N satırı özet taşıyor (`TraceWaterfall.tsx:747-758`, tooltip'te total/avg/max). Alt ağaç rollup'ı yok |
| 3.6 | **Minimap** | **YOK** | Repoda `minimap` 0 eşleşme (frontend+backend). En yakın akraba: `TraceServiceBreakdown` (yatay yığılmış şerit, `TraceWaterfall.tsx:113`) |
| 3.7 | **Span detay yan paneli** | **VAR ama gruplama YOK** | `SpanDetail.tsx`. Attribute'lar **düz** listeleniyor (`:277-281`, `Object.entries` sırası). http/db/messaging/custom **gruplaması yok**. Events VAR (`:375`), Exceptions ayrı (`:355`), **Links YOK** |
| 3.8 | **"Bu span'in loglarını göster"** | **KISMEN — span değil TRACE scope** | `SpanDetail.tsx:144` `api.logs({traceId, …})` — `spanId` **geçmiyor**. `logstore.Filter`'da `SpanID` alanı VAR ve ES+CH iki backend de destekliyor (`internal/logstore/clickhouse.go:38,77`, `elasticsearch.go:247,272`). Yani **arayüz hazır, çağıran kullanmıyor** — tek satırlık boşluk. Waterfall'daki `≡N` çipi (`TraceWaterfall.tsx:762-781`) span başına sayıyor ama tıklayınca trace-scope Logs sekmesine gidiyor |
| 3.9 | **Flame graph (trace)** | **YOK (parça VAR)** | `components/FlameGraph.tsx` mevcut ama `FlameNode` **profil** verisi için; tek tüketici `pages/Profile.tsx:245,256`. Trace span ağacını `FlameNode`'a çeviren adaptör yok. `AggregateFlame.tsx` + `FlameDiff.tsx` de profil tarafında |
| 3.10 | **Servis grafiği sekmesi (trace-scope)** | **YOK** | `TopologyFlowGraph.tsx` global topoloji için (`ServiceMap` tipi, dagre). Tek trace'ten servis-DAG'ı türeten kod yok |
| 3.11 | **İki trace karşılaştırma** | **VAR** | `pages/TraceCompare.tsx` — split + diff sekmeleri, `lib/spanAlign.ts` yol-tabanlı eşleme, `DIFF_COLS` `useDataTable` ile. Ek olarak AI: `POST /api/copilot/compare-traces` (`ai_routes.go:125`) |

**Bonus mevcut (Dynatrace paritesi tarafında zaten kazanılmış):** in-trace filtre
(`Trace.tsx:289-307`), ×N gruplama (`TraceWaterfall.tsx:298-335`), tekrar/N+1 çipleri
(`lib/traceRepeats.ts`), j/k gezinme (`Trace.tsx:226-264`), provenance şeridi
(`TraceHonesty.tsx`), cluster çipi (`traceWaterfall.tree.ts:44`), span→profil hotspot
(`SpanDetail.tsx:451`), paylaşım linki + snapshot'a donmuş loglar (`api.go:4650-4658`).

---

## 4. Veri modeli önerisi

### 4.1 Karar: ağaç + kritik yol + self time BACKEND'de

**Evet.** Gerekçeler ölçülebilir:
1. Bugün ağaç **iki kez** kuruluyor (`TraceWaterfall.tsx:255`, `Trace.tsx:189`),
   kritik yol bir kez daha DFS yapıyor (`criticalPath.ts:46`), öz süre span başına
   **tüm listeyi filtreliyor** (`selfTime.ts:20` — `all.filter(...)` her çağrıda
   O(N), yani panel açılışı O(N) ama tüm span'ler için hesaplansa O(N²)).
2. İki self-time tanımı ayrışmış durumda (§1.4). Tek kaynak = tek tanım.
3. `TraceCompare` aynı hesapları **iki kez** yapıyor.
4. Backend'de hesaplamak `go test -race` ile tablo testi alınabilir; JS tarafında
   bugün yalnız `selfTime.test.ts` var.

### 4.2 Payload taslağı

```go
// internal/chstore/tracetree.go — YENİ
type TraceNode struct {
    SpanID   string `json:"spanId"`
    ParentID string `json:"parentSpanId,omitempty"`
    Depth    uint16 `json:"depth"`
    // Ağaç sırası: DFS index. Frontend sıralamayı TEKRAR yapmaz.
    Order    uint32 `json:"order"`
    ChildN   uint32 `json:"childCount"`   // doğrudan çocuk
    SubtreeN uint32 `json:"subtreeCount"` // tüm alt ağaç — §3.5 katlanmış özet
    SubtreeErrN uint32 `json:"subtreeErrors"`
    SubtreeNs   int64  `json:"subtreeNs"` // alt ağaç toplam duvar saati
    SelfNs      int64  `json:"selfNs"`    // ARALIK BİRLEŞİMLİ (selfTime.ts:19 tanımı)
    Critical    bool   `json:"critical,omitempty"`
}

type TraceServiceSummary struct {
    Service   string  `json:"service"`
    SpanN     uint32  `json:"spanCount"`
    ErrN      uint32  `json:"errorCount"`
    SelfNs    int64   `json:"selfNs"`
    SelfPct   float64 `json:"selfPct"`
    EntryN    uint32  `json:"entryCount"` // servise giriş sayısı (handoff)
}

type TraceAnalysis struct {
    Version      int                   `json:"v"`            // 1 — §4.4
    Nodes        []TraceNode           `json:"nodes"`
    CriticalNs   int64                 `json:"criticalNs"`
    CriticalIDs  []string              `json:"criticalIds"`  // sıralı zincir
    Services     []TraceServiceSummary `json:"services"`
    RootSpanID   string                `json:"rootSpanId"`
    OrphanN      uint32                `json:"orphanCount"`
    Truncated    bool                  `json:"truncated"`    // 50k tavanı
}
```

```ts
// frontend/src/lib/types.ts — YENİ (tek şekil kaynağı, CLAUDE.md)
export interface TraceNode {
  spanId: string; parentSpanId?: string;
  depth: number; order: number;
  childCount: number; subtreeCount: number;
  subtreeErrors: number; subtreeNs: number;
  selfNs: number; critical?: boolean;
}
export interface TraceServiceSummary {
  service: string; spanCount: number; errorCount: number;
  selfNs: number; selfPct: number; entryCount: number;
}
export interface TraceAnalysis {
  v: number; nodes: TraceNode[];
  criticalNs: number; criticalIds: string[];
  services: TraceServiceSummary[];
  rootSpanId: string; orphanCount: number; truncated: boolean;
}
// TraceDetailResponse'a EK ALAN (kırılmaz):
//   analysis?: TraceAnalysis;
```

### 4.3 Boyut

5000 span için `nodes` dizisi: span başına ~9 sayısal alan + 2 hex id.
Kaba hesap **~180-220 B/düğüm JSON** → **0.9-1.1 MB**. Mevcut span payload'ının
(§1.2 tahmini 5-10 MB) **~%12-20'si** — kabul edilebilir ek. (ÖLÇÜLMEDİ.)

**Asıl kazanç ayrı bir dilimde:** `resourceAttributes` span başına tekrar ediyor
(§1.2). `resources: {id: {…}}` + `SpanRow.resourceId` normalizasyonu 5000 span'de
payload'ı yarıya indirir. Bu **kırıcı** bir değişiklik, Dilim 1'e girmez.

### 4.4 Cache + uyum

- Anahtar: `"trace:v3:" + id` — mevcut `"trace:v2:"` (`api.go:4550`) **v3'e çıkar**;
  şekil değiştiği için eski anahtar üstüne yazmak rolling deploy'da eski FE'ye yeni
  gövde servis eder. TTL 30 sn korunur (immutable veri + Tempo ayarı flip'i).
- `serveCached` sözleşmesi aynen (`cache.go:218`); `s.serveCached` atlanmaz.
- **Eski istemci uyumu: ALAN EKLEME, versiyonlama DEĞİL.** `analysis?:` optional;
  `analysis` okumayan bir istemci bugünkü davranışı aynen alır. `TraceAnalysis.v`
  gövde içinde taşınır ki gelecekte alan **anlamı** değişirse FE dallanabilsin
  (alan **eklemek** v'yi artırmaz).
- Ayrı endpoint (`/api/traces/{id}/analysis`) **tercih edilmez**: aynı span
  taramasını iki kez yapar ve iki cache anahtarı bayatlaşma yarışına girer.

---

## 5. Design-system uyumu

| Primitif | Kanonik yer | Trace yüzeyinde durum |
|---|---|---|
| `useDataTable` / `DataTable` | `components/DataTable.tsx` | ✅ `TraceCompare.tsx` DIFF_COLS'ta; ❌ şelalede hiç yok (şelale tablo değil — meşru) |
| `VirtualTable` / `VirtualList` | `components/ui/VirtualTable.tsx` | ❌ kullanılmıyor (§1.3) — Dilim 1'in sanal satır adayı `VirtualList` |
| `ContextBar` | `components/ContextBar/ContextBar.tsx` | ❌ /trace kullanmıyor; `Topbar` + elle `.crumbs` (`Trace.tsx:378-382`) |
| Entity link | `lib/entityHref.ts`, `components/spanEntityLinks.ts` | ✅ `SpanDetail.tsx:11` (`spanAttrHref`, `spanEndpointHref`), `lib/spanK8s.ts` |
| `Drawer` | `components/ui/Drawer.tsx:12` (Esc katmanı, backdrop, odak) | ❌ **SpanDetail Drawer DEĞİL** — `Trace.tsx:574-592`'de inline flex sütun + elle resize (`SpanDetail.tsx:188-200`) + `useOutsideClose` (`Trace.tsx:336`) + kendi `useEscLayer`'ı |
| `Modal` | `components/ui/Modal.tsx` | ✅ `TracePeekDrawer.tsx:3` |
| `Button` | `components/ui/Button.tsx` | ✅ çoğunlukla; ❌ **`Trace.tsx:417-429`** "Compare trace" ham `<Link>` + 10 satır inline style (`/frontend-design-system` AS-1: gezinme atomu yok — 36 sahipsiz siteden biri) |
| `Field` | `components/ui/Field.tsx` | ⚠️ `SpanFilterBar` (`Trace.tsx:656`) — doğrulanmadı, incelenmedi |
| `Badge` | `components/ui/Badge.tsx` | ❌ elle sınıf: `Trace.tsx:390` `<span className={badge b-err}>` (K4 sınıfı: atom 14, elle 297) |
| `Chip` | `components/ui/Chip.tsx` | ❌ `.wf-cat` / `.wf-cluster` / `.wf-group` / `.facet` elle |
| `Stat` | atom YOK (6 tanım) | ❌ `Trace.tsx:1369 KPI` — **7. kopya** |
| `Spinner`/`Empty` | `components/Spinner.tsx` | ✅ `Trace.tsx:465-466, 505-507` |

**El yapımı yoğunluğu (kanıt):** `TraceWaterfall.tsx:762-781` log çipi tek başına
9 satırlık statik inline style bloğu (`/frontend-design-system §8`: "blok tamamen
statik → 🔴 yasak"). `Trace.tsx:472-503` (aged-out kutusu), `:511-522` (Tempo
banner'ı), `:794-800` (link şeridi) aynı sınıf.

**Dilim 1'de nereye ne:**
- Span detay → **`ui/Drawer`**'a taşınmalı mı? **HAYIR, Dilim 1'de değil.** Şelale
  ile yan yana durması bilinçli (satır seçip panelde okumak, backdrop olmadan). Ama
  Esc/odak yönetimi bugün **iki ayrı yerde** (`SpanDetail` kendi `useEscLayer`'ı +
  `Trace.tsx:336 useOutsideClose`) — birleştirme ayrı bir alt dilim.
- Yeni attribute grup başlıkları → `SpanDetail.tsx:499 Section` (mevcut, dosya-yerel).
- Yeni "Links" bölümü → aynı `Section` + `Row` (`:607`).
- Minimap → yeni bileşen, **`ui/` altına DEĞİL** (trace'e özel) →
  `components/traces/TraceMinimap.tsx`, CSS ailesi `.tm-*` öneki.
- Servis renkleri → **`svcColor` tek yol** (`traces/shared.tsx:14`); yeni bir hash
  paleti açılmaz (v0.9.398'de tam bu hata düzeltildi).

---

## 6. Dilimleme

**Ortak kısıt:** yeni route `internal/api/trace_routes.go` içinde
`func (s *Server) registerTraceRoutes(mux *http.ServeMux)`, kayıt
`route_registry.go:30 registerRoutesExtra` defterine `init()` ile
(v0.10.247 yolu) → **`api.go`ya SIFIR satır**. `TestMuxRoutePatterns` çakışmayı
`buildMux` (`route_registry.go:55`) üzerinden yakalar.

### Dilim 1 — trace görünümü çekirdeği (L toplam, 5 alt dilime bölündü)

> ⚠️ Brief'teki "link'leri koru + CH links kolonu migration" adımı **düşüyor** (§2.3):
> iş v0.8.329'da yapılmış. Yerine gerçek boşluklar geçti (1a).

| Alt dilim | Efor | Risk | Dosya planı |
|---|---|---|---|
| **1a — Link'ler span düzeyine** | **S** | Düşük | `internal/api/pivot.go` (mevcut uç; yanıt zaten `spanId` taşıyor → yalnız FE); `Trace.tsx:771-789` dedupe'a `spanId` ekle; **`SpanDetail.tsx`'e `Links` Section**; `TraceWaterfall` link kenarı için `linkIds?: Set<string>` prop. Bağımsız merge edilir. |
| **1b — Backend ağaç/kritik yol/self/servis özeti** | **M** | Orta | YENİ `internal/chstore/tracetree.go` (saf: `BuildTraceAnalysis([]SpanRow) TraceAnalysis` — CH'ye dokunmaz, `GetTrace` çıktısı üstünde çalışır) + `internal/api/trace_routes.go` (`getTrace`i api.go'dan **taşımadan**, `analysis` alanını ekleyen sarmalayıcı) + cache `trace:v3:` + `lib/types.ts` + `lib/api.ts`. **Risk:** `api.go:4539 getTrace` hâlâ api.go'da; yeni alanı oraya eklemek api.go'yu 1-2 satır büyütür → tercih: hesap `trace_routes.go`'da yaşar, `getTrace` tek fonksiyon çağırır. |
| **1c — Şelale görsel katmanı** | **M** | Orta | `TraceWaterfall.tsx`: kritik yol vurgusu (VAR, korunur), **self/total ikili bar** (bar içinde self kısmı koyu), **collapsed alt ağaç özeti** (`subtreeCount`/`subtreeNs` `TraceNode`'dan), servis renkleri (VAR). ⚠️ `TraceServiceBreakdown:118-122` naif hesabı **silinir**, `analysis.services` okur → iki-tanım çelişkisi kapanır. |
| **1d — Sanal satırlar + minimap** | **M** | **Yüksek** | `VirtualList` (`ui/VirtualList.tsx`) ile satır penceresi; `defaultCollapsed` ölü prop'u ya kullanılır ya silinir; `Math.min(...spread)` → reduce (`TraceWaterfall.tsx:268-269`, `Trace.tsx:355-356`). YENİ `components/traces/TraceMinimap.tsx`. **Risk yüksek:** `content-visibility` + sayfa scroll'u sözleşmesi (`globals.css:1437`) ve `?span=` deep-link scroll'u (`Trace.tsx:315-325`, `.wf-sel` seçicisi) sanallaştırmada **kırılır** — hedef satır mount olmayabilir (`/frontend-conventions §2`: "deep-link hedef satırı mount istiyorsa sanallaştırma"). Bu alt dilim **kendi başına merge edilebilir ve geri alınabilir olmalı**. |
| **1e — Span detay drawer içeriği** | **M** | Düşük | `SpanDetail.tsx`: attribute **gruplama** (http.* / db.* / messaging.* / rpc.* / k8s.* / custom — saf fonksiyon `lib/spanAttrGroups.ts` + vitest); events zaten var (`:375`) — zaman sıralı + attribute açılır; **"bu span'in loglarını göster"** → `api.logs({traceId, spanId, …})` (`SpanDetail.tsx:144`, `logstore.Filter.SpanID` hazır) + trace-scope'a düşme anahtarı; `Links` bölümü (1a). |

**Dilim 1 sırası:** 1a → 1b → 1c → 1e → 1d (1d en riskli, en sona).

### Dilim 2 — flame graph + servis grafiği sekmeleri (M)

- `lib/traceFlame.ts` — `SpanRow[] → FlameNode` adaptörü (saf, vitest'li).
  `FlameGraph.tsx` **değişmez** (profil tüketicisi kırılmaz).
- `lib/traceServiceGraph.ts` — trace'ten servis DAG'ı (düğüm = servis, kenar =
  cross-service parent→child + **span link**). Render `TopologyFlowGraph`'i
  yeniden kullanır mı? `ServiceMap` tipini beslemek gerekir — **doğrulanmadı**,
  tip uyumu ayrıca ölçülmeli.
- `Trace.tsx:551-566` `.tab-strip`'e iki sekme (`?tab=flame|graph`, URL'e
  `Trace.tsx:168-178` yazıcısıyla).
- Risk: düşük (yeni sekme, mevcut yol dokunulmaz). Bağımsız merge.

### Dilim 3 — trace karşılaştırma (S)

**Sıfırdan değil, iyileştirme.** `TraceCompare.tsx` zaten var. Yapılacak:
1c'nin analysis payload'ını iki tarafta da kullanmak (kritik yol + self time
karşılaştırması), `:40`'taki **yanlış sanallaştırma yorumunu** düzeltmek,
`Trace.tsx:417-429` inline-style Link'i atoma taşımak. Risk düşük.

**Merge bağımsızlığı:** 1a, 1b, 1c, 1e, 2, 3 birbirinden bağımsız merge edilebilir
(1c ve 1e 1b'nin payload'ını **optional** okur → 1b olmadan da derlenir). 1d yalnız
1c'den sonra anlamlı.

---

## 7. Test stratejisi

### 7.1 Backend — tablo testleri (`/tdd` deseni, testify YOK)

Yeni `internal/chstore/tracetree_test.go`, saf `BuildTraceAnalysis` seam'i üstünde.
Fixture trace'ler (hepsi elle kurulmuş `[]SpanRow` literalleri):

| Fixture | Neyi çiviler |
|---|---|
| `linear` (A→B→C, iç içe) | kritik yol = tüm zincir; self = her span'de kendi payı |
| `parallel` (A→{B,C} çakışan) | **aralık birleşimi**: naif toplam negatif self üretirdi |
| `async_link` (producer trace ⇢ consumer trace, `span_links`) | link kenarı ağacı değiştirmez; kritik yol trace içinde kalır |
| `error` (derinde hata + exception event) | `subtreeErrors` yukarı toplanır |
| `orphan` (parent trace'te yok) | çok kök; `OrphanN`; kritik yol en uzun kökten |
| `zero_duration` / `end < start` | negatif self **asla** (`selfTime.ts:43` sözleşmesi) |
| `deep_10k` | özyineleme YOK (iteratif) — `collectSubtreeIds` deseni (`traceWaterfall.tree.ts:11-13`) |
| `truncated_50k` | `Truncated: true` ve kritik yol "kısmi" ilan eder |

Ek: `internal/api/trace_routes_key_test.go` — cache anahtarı `trace:v3:` + tüm
girdiler (emsal `internal/api/cache_key_test.go`, CLAUDE.md ship-checklist #11).
Kapı: `go test -race ./internal/chstore/ ./internal/api/`.

### 7.2 Frontend

**@testing-library YOK** (doğrulandı: `frontend/package.json` dependencies +
devDependencies'te `@testing-library/*` 0 eşleşme). Mevcut sözleşme:

- **jsdom + `createRoot` + `act`** — kanonik emsal
  `frontend/src/components/traceWaterfallGroup.test.tsx:1-45`:
  `// @vitest-environment jsdom` pragma + `NoopResizeObserver` shim (jsdom
  `ResizeObserver` uygulamıyor; shim olmadan `TraceWaterfall` **her vakada**
  `ReferenceError` ile düşer) + `querySelector`/`textContent` ile DOM iddiası.
  **Yeni waterfall testleri bu dosyayı şablon alır.**
- **Saf çekirdek testleri** (`.test.ts`, jsdom'suz) — repoda baskın desen:
  `selfTime.test.ts`, `traceRepeats.test.ts`, `traceColumns.test.ts`,
  `spanEntityLinks.test.ts`, `traceWaterfall.subtree.test.ts`.
  → 1e'nin `lib/spanAttrGroups.ts`'i, 2'nin `lib/traceFlame.ts`'i **buraya**.
- **Contract testleri** — `ui/*.contract.test.tsx` deseni (Modal, Drawer, Chip…);
  yeni `TraceMinimap` bir contract testi almalı.

### 7.3 Render benchmark — 5000 span

Emsal **`frontend/src/components/TopologyFlowGraph.perf.test.tsx`** (v0.10.116,
`docs/perf/perf-budget-2026-08-28.md` P4). Sözleşmesi aynen kopyalanır:

- `renderToString` (react-dom/server) — **boya hariç**, yerleşim + memo maliyeti
  ölçülür; tarayıcı gerekmez.
- Bir ısınma render'ı, sonra **3 koşu, medyan** (`perf.test.tsx:38-46`).
- Konsola tek satır etiketli çıktı (`TOPOLOGY_LAYOUT_PERF` muadili
  `TRACE_WATERFALL_PERF`) + **tavanda tek `expect`**.
- Bütçe **ölçülerek** konur, tahminle değil: taban ölçüm alınır (500/1000/5000/
  20000 span), sonra ~4× pay ile eşik yazılır (CI makinesi payı — `perf.test.tsx:9`
  gerekçesi).
- `{ timeout: 120_000 }` (`perf.test.tsx:35`).
- ⚠️ `renderToString` **`content-visibility`'yi ölçmez** (CSS, boya zamanı). Yani
  bu benchmark 1d'nin (sanallaştırma) kazancını gösterir, bugünkü
  content-visibility kurtarmasını **göstermez** — rapor bunu ilan etmeli, yoksa
  "5000 span 900 ms" sayısı prod'daki algılanan hızdan kötümser okunur.

Ayrıca `BuildTraceAnalysis` için Go tarafında `testing.B` (5000 span fixture) —
payload üretimi p99 200 ms bütçesinin (CLAUDE.md) içinde kalmalı.

### 7.4 Kapılar

`cd frontend && npx tsc --noEmit && npm run lint && npm run test -- --run` ·
`go build ./...` · `go test -race ./...` · `make audit` (🔴 tag'i bloklar).

---

## 8. Onay soruları

1. Dilim 1 önkoşulu (CH links kolonu migration'ı) **düşsün**, yerine 1a (link'ler span
   düzeyine + SpanDetail Links) geçsin — kabul mü?
2. Ağaç/kritik yol/self time **backend'de** (`analysis?:` ek alan, cache `trace:v3:`) — kabul mü?
3. 1d (sanal satırlar + minimap) en sona ve ayrı geri alınabilir commit — kabul mü?
4. Span detay paneli Dilim 1'de `ui/Drawer`'a TAŞINMASIN (yan yana okuma bilinçli) — kabul mü?

---

## Durum (2026-09-03)

Dilim 1 GEMİDE: 1a span link'leri (v0.10.274) · 1b `BuildTraceAnalysis` tek payload + `trace_routes.go` defter kaydı (275) · 1c şelale kritik yol / öz süre / servis renkleri / kapalı alt-ağaç özeti (276) · 1e span çekmecesi gruplu attr + olaylar + span-kapsamlı loglar (277) · 1d 400+ satırda pencere sanallaştırma + 150+ satırda trace haritası + `?span=` derin bağlantı (278). Dilim 2 (flame + servis grafiği) ve Dilim 3 (karşılaştırma) ayrı oturum.
