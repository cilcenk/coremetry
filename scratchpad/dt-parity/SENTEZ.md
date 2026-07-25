# DT-parite sentezi — /endpoints · /databases · /messaging

Kaynak: `endpoints-brief.md`, `databases-brief.md`, `messaging-brief.md` (+ üç mockup).
Sürüm tabanı v0.9.256. **Hiçbir ürün dosyası değiştirilmedi**; bu belge ve mockup'lar
`scratchpad/dt-parity/` altında. Ana tablo düzeni, kolon sırası ve drawer akışı üç sayfada
da **korunuyor** — her kalem ek yüzey (yeni kolon / yeni panel / yeni pivot / drawer bölümü).

---

## 1 · Ortak parite açığı

Üç sayfa da "envanter tablosu" olarak doğru çalışıyor — ne var, ne kadar var sorusunu
cevaplıyor — ama Dynatrace'in ayırt edici hattı olan **satır → tek örnek → tek neden**
son metresi üçünde de kopuk, ve kopukluk hep aynı üç biçimde tekrar ediyor: (a) **bedeli
zaten ödenmiş veri atılıyor** — MV'nin ürettiği TDigest yüzdelikleri okunmuyor
(`endpoints.go:456-458` index 2'yi hiç almıyor, `dependencies.go:616` yalnız index 3'ü
alıyor), `/api/databases/trends` payload'ının `errorRate`+`p99Ms` serileri sparkline'a
girmeden çöpe gidiyor, `p95DurationMs` API'de dolu gelip hiç render edilmiyor,
`slow_/error_exemplar_state` yalnız drawer'da çözülüyor; (b) **kapsam ve kırpma sessizce
yalan söylüyor** — EnvPicker render ediliyor ama `api_databases.go` `env`'i hiç parse
etmiyor (grep: sıfır eşleşme), endpoint drawer'ı seçili env'i yok sayıp tüm env'leri
topluyor, 200-satır kapağı ve kind-split kapağı dışında kalan hücreler `—` yerine `0.0`
basıyor, receiver satırları `0 çağrı / %0 hata` diye sağlıklı okunuyor; (c) **sayfalar
ada** — `/endpoints` ve `/messaging`'e ürün içinden **tek bir link yok** (yalnız Sidebar +
CommandPalette), mevcut çıkış linkleri `from/to` taşımıyor, yani bir olay araştırması bu üç
tabloya asla inmiyor ve inse bile karşı taraf başka pencere açıyor. Sonuç: parite açığı
"daha çok metrik" değil, **elde olan veriyi göstermek, kapsamı dürüst söylemek ve satırdan
bir trace'e/bir nedene çıkmak**.

---

## 2 · Birleşik iş listesi (operatör değeri sırasında)

Sayfa kısaltmaları: **E** = /endpoints, **D** = /databases, **M** = /messaging.

| # | İş | Sayfa | Besleyen MV / veri | Fizibilite | Efor | Değer |
|---|---|---|---|---|---|---|
| 1 | Okunmayan TDigest yüzdeliklerini kolona çıkar (E: P90 · D: P50+P95 · M: P95 + drawer P50/P95) | E·D·M | `spanmetrics_1m` idx 2; `db_summary_5m`/`db_caller_summary_5m`/`messaging_*_summary_5m` idx 1-2 | Hazır — **sıfır ek tarama**, aynı merge | ~3 sa | Yüksek |
| 2 | Trend kolonu doğru MV'den: yeni `GET /api/messaging/trends` (bugün `db_summary_5m` okuyup boş dönüyor) | M | `messaging_summary_5m` | `db_trends.go` birebir aynası; `countIfMerge` şart | ~2 sa | Yüksek — ölü kolon **+** her range değişiminde boşa giden `LIMIT 200000` sorgusu ölür |
| 3 | Giriş/çıkış pivotları + `from/to` taşıma (ürün içinden üç sayfaya link, mevcut çıkış linklerine range) | E·D·M | Veri gerekmez | Saf FE; v0.9.256'da messaging için düzeltilen hatanın ikizi | ~1-2 sa | Yüksek |
| 4 | Satır-içi ve drawer exemplar pivotları (E: ⚡en yavaş/✖en kötü hata · D: iki nokta-okuma · M: E2E en yavaş 5 çift) | E·D·M | `spanmetrics_1m.*_exemplar_state`; `dbstmt_detail.go:304-340` deseni; mevcut `msgE2ESQL` scan'i | Hazır desen; E tarafı **yeni tarama yok** | 2+2+2 sa | Yüksek |
| 5 | Kırpma & yanlış-sıfır dürüstlüğü (200-satır rozeti, `LIMIT 5 BY (system,cluster,destination)`, `0.0`→`—`, receiver RED'i `—`) | M·D | Sorgu düzeyi; MV değişmiyor | Hazır; `*uint64`+omitempty sözleşme değişimi tek risk | ~3 sa | Yüksek — sessiz yalan sınıfı |
| 6 | Kapsam dürüstlüğü: drawer'a `env`/`cluster` taşıma + `scope:` chip'i (E, bugün yapılır) — D/M'deki **ölü EnvPicker** MV kolonu ister → §4 | E (·D·M) | `spans.deploy_env` typed kolon zaten var | Backend **önce**, FE chip'i sonra; cache key'e env/cluster girmezse çapraz zehirlenme | ~2 sa | Yüksek |
| 7 | Backlog Δ kolonu (net + tüketim/üretim oranı + koşullu pik rozeti) + drawer backlog paneli | M | Mevcut `ProduceCount`/`ConsumeCount` payload'ı; pik için #2'nin serisi | Hazır; **#2'ye bağımlı** (pik yarısı) | ~2 sa | Yüksek — sayfanın manşet parite eksiği |
| 8 | Statements tablosunu MV-first'e çevir (Faz A, `instance` boyutu yok — dürüst etiketle) | D | `db_statement_summary_5m` | **Ham `spans` GROUP BY'ı tamamen kaldırır** (MV-first invaryant ihlali) | ~yarım gün | Yüksek |
| 9 | Drawer RED zaman serisi paneli (calls/s · p99 · err %, tek hizalı x ekseni, uPlot) | D (M ikizi sonra) | `/api/databases/trends` — veri **bugün geliyor ve atılıyor** | Yeni uç yok, ikinci fetch yok | ~2 sa | Orta-yüksek |
| 10 | Err % hücresine triyaj pivotları (hatalı consume span'leri / loglar / exception grubu) | M (E·D'ye kopyalanabilir) | Ek okuma yok — `messagingTracesHref`'in mevcut `role`+`hasError` parametreleri | Saf FE, sıfır backend riski | ~1 sa | Orta-yüksek |
| 11 | "Compare vs prior" toggle + delta rozetleri | D | `db_summary_5m` ikinci kaydırılmış pencere; `api_databases.go:120-152` şablonu | Ön yüz (`TrendDelta`, `prior*` alanları) **zaten hazır, doldurulmuyor** | ~2 sa | Orta-yüksek |
| 12 | "Where the time goes" drawer bölümü — `Downstream` \| `Callers` + süre payı barları | E | Yeni bounded sorgu: ≤200 route-kapsamlı trace örneği, tek örneklemeden iki sekme | Guard'lar kritik (zaman-sınırlı WHERE **iki sorguda da**, `LIMIT`, `max_execution_time`, `shardSkipSetting`) | ~1 gün | Yüksek — en büyük triage boşluğu |
| 13 | Baseline: `vs Baseline` kolonu + drawer 7-gün aynı-slot medyan/p25-p75 bandı | E | `spanmetrics_1m` (30 gün TTL), tek 7 günlük sorgu; deploy çizgileri `GetServiceDeploys` | **Ayrı uç şart** — liste okumasının p99 bütçesini paylaştırma | ~1 gün | Yüksek — DT'nin ayırt edici sinyali |
| 14 | Health kolonu: doygunluk metresi + açık problem rozeti (receiver satırlarının tek dürüst sinyali) | D | `db_capacity.go UsageLimit` + `problem.go:468/660` toplu okuma | **Ayrı uç** (`/api/databases/health`) ki `/api/databases` p99 bütçesi bozulmasın | ~yarım gün | Orta-yüksek |
| 15 | Giriş-noktası sekmeleri: `HTTP` \| `RPC & Messaging` (gRPC + Kafka consumer giriş noktaları bugün sayfada **hiç yok**) | E | `spanmetrics_1m` — `name`+`kind` dim'leri ORDER BY'da hazır, projeksiyona girmiyor | MV yolu hazır; `cluster`/`env` seçiliyken `forcesRaw` → ya ham dal ya açık uyarı, **sessizce boş dönmesin** | ~1 gün | Orta-yüksek — kapsam dürüstlüğü |

Toplam: 1-11 ≈ 3 gün, 12-15 ≈ 3 gün. Her kalem kendi `v0.9.X` sürümü (batch yok).

---

## 3 · Önce bunlar (bugünkü veriyle, en yüksek değer/efor)

1. **#1 — Okunmayan TDigest yüzdelikleri (E P90 · D P50+P95 · M P95).** MV bu sayıları
   zaten üretiyor ve merge zaten yapılıyor; `arrayElement` indeksini eklemek dışında hiçbir
   maliyet yok — üç sayfada birden "tipik mi kuyruk mu" sorusunu açar. *(Tek şart: E tarafında
   ham yol `(0.5,0.95,0.99)`, MV yolu `(0.5,0.9,0.95,0.99)` — 0.9 eklenmezse P90 **kaydırılmış
   index** okur; MV/ham × shape açık/kapalı **dört kombinasyon** test edilir.)*
2. **#2 — `/api/messaging/trends`.** Negatif maliyetli: ölü Trend kolonunu canlandırırken her
   range değişiminde koşulsuz atılan `LIMIT 200000` `db_summary_5m` sorgusunu da öldürür.
3. **#3 — Pivot + `range` taşıma.** Yarım saatlik saf frontend işi; `/endpoints` ve
   `/messaging`'in ürün içinden erişilemez ada olmasını bitirir ve paylaşılan linkin karşı
   tarafta başka pencere açması hatasını (v0.9.256 messaging düzeltmesinin ikizi) kapatır.

*(4. sıradaki: #4 exemplar pivotları — E yarısı sıfır ek tarama.)*

---

## 4 · Yeni MV / yeni kolon gerektirenler

Hiçbiri "önce bunlar"da değil. Hepsi **distributed-safe kuralına** tabi: `hasXCol` probe +
koşullu okuma/INSERT + `cluster_name` unset kurulumda ALTER'ı atla (prod'u iki kez kırdı —
v0.8.185 `cluster`, v0.8.186 `op_group`). Kolon ekleme yolu **in-place**: iç tablo `ALTER` +
`MODIFY QUERY` (`reference-ch-inplace-mv-column-add`), DROP+RECREATE değil.

| Değişiklik | Neden | Şema maliyeti + kardinalite | Karar |
|---|---|---|---|
| `messaging_summary_5m` + `messaging_caller_summary_5m` → `deploy_env LowCardinality(String)` | **Ölü EnvPicker**: handler env parse etmiyor, MV'de boyut yok → prep/uat trafiği prod topic sayılarına karışıyor. Sessizce yanlış cevap veren kontrol, hiç olmayan kontrolden tehlikeli | Satır sayısı env sayısı kadar (~3-5×); LC sözlüğü ile disk etkisi küçük. Kısa vadeli çapraz doğrulama: `topology_edges_5m` queue kenarlarında `parent_env`/`child_env` zaten var | **Yap** — ayrı dilim, ~yarım gün, kendi doğrulaması |
| `db_statement_summary_5m` ORDER BY'ına `instance` (Faz B) | Bir `db_name`'i birden fazla host servis ediyorsa satırlar katlanıyor | Host-per-dbname tipik **1-3×**; ağır olan `sample_stmt_state` + TDigest state'leri de aynı oranda çoğalır → MV boyutu 1-3×. **`system.parts`'tan mevcut boyut ölçülmeden yapılmaz** | **Ölçüm sonrası.** Faz A (etiketle dürüst kal) ham-spans taramasını zaten öldürüyor |
| Yeni `db_error_summary_5m` (`error.type` boyutu) | Üç DB MV'si de yalnız `countIfState(status_code='error')` tutuyor; hata **tipi** yok | Yalnız hatalı trafik kadar satır → `db_summary_5m`'in küçük bir kesri | **Sıradaki en yüksek değerli kalem** (~1 gün). Ucuz ara adım: drawer açılınca PK-prefix kısıtlı ham `GROUP BY error.type` |
| Yeni `messaging_operation_summary_5m` (msg_system × cluster × destination × name × kind) | `dependencies.go:534-544` Top operations = sayfadaki tek MV-dışı aggregate | `name` kardinalitesi ölçülmedi | **Ölçüm kapısı arkasında** (~1 gün). Mockup'ta panel başlığına `⚠ ham spans` rozeti kondu |
| Messaging partition / consumer-group boyutu | Kafka'da gecikmenin en yaygın nedeni; topic p99'u sağlıklı görünürken tamamen görünmez | Attribute'lar `spans.attr_keys`'te verbatim durur **ama demo basmıyor**, `convert.go:126` yalnız `messaging.system`'i kolona alıyor | **Önce canlı veride varlığı ölçülmeli**; yoksa madde veri-yok'a düşer |
| `spanmetrics_1m`'e apdex `countIfState` kolonları (endpoint SLO rozeti) | Endpoint bazlı apdex/SLO | MV **state kolonu** değişimi = rolling-deploy okuma-hatası penceresi → dual-column geçiş zorunlu | **Bu turda hayır.** Kısmi çözüm (`operation_summary_5m` apdex'ini span-adı=route iken eşlemek) delikli; delikli SLI rozeti yanıltır |
| `endpoint_baseline_1h` rollup (route × saat × gün-sınıfı) | Filo geneli "her route için baseline dedektörü" | Servis başına ~50 route × 168 slot — ucuz, ama ayrı dilim | **Gerekmiyor.** #13 yalnız açık endpoint + görünen top-N için hesaplıyor |
| `topology_op_edges_5m`'e route boyutu (#12 için MV alternatifi) | Route→downstream MV'si | 180 s exec + grace_hash + 4 GB join + route×downstream kardinalitesi | **Reddedildi** — bounded örnekleme + dürüst etiket tercih edildi |

---

## 5 · Yapılmayacaklar

| Elenen | Neden |
|---|---|
| Ana tabloların yeniden düzenlenmesi, kart-feed'e çevrilmesi, drawer'ın sekmelere bölünmesi | Operatör kısıtı: v0.9.61 Operations redesign reddedildi, v0.8.428 kart-feed reddedildi, v509 Problems drawer revert edildi. Üç brief'te de **tek bir düzen değişikliği önerisi yok**; yeni kolonlar mevcut kolonların **arasına/sonuna** giriyor, hiçbiri yer değiştirmiyor, E'deki üç yeni kolon varsayılan **gizli** |
| Yeni grafik kütüphanesi | Yalnız **uPlot**. Üç mockup'taki SVG'ler temsilî; gerçek uygulamada uPlot + `data-theme` değişiminde seri yeniden çözme + drawer kapanınca `destroy()` (Explore v2 "hooks-in" tuzağı) |
| `db.statement` / mesaj gövdesi redaksiyonu, PII maskeleme | Operatör tercihi: tam fidelity. `sample_stmt_state` "örnek ifade" diye etiketlenir, kırpılmaz-maskelenmez |
| Tenant/müşteri kırılımı, per-tenant scope | Coremetry tek-tenant kalıyor |
| In-binary head/tail sampling | v0.8.73'te kaldırıldı, geri gelmiyor. #12'deki ≤200 trace ve E2E'deki `LIMIT 50000` **sorgu-tarafı bounded örneklem**; ingest 100% kalır ve UI "örnek tabanlı (N trace)" der |
| E2E örnekleminin "%X rastgele örneklem" diye etiketlenmesi | `msgE2ESQL`'deki `LIMIT 50000`'in **`ORDER BY`'ı yok** — CH'nin döndürdüğü ilk 50k satır, sistematik sapma taşıyabilir. Rozet yalnız "örneklem" der. Deterministik-rastgele (`ORDER BY cityHash64(span_id)`) ayrı dilim |
| Broker offset lag'inin sahte sıfırla çizilmesi | `messaging.kafka.consumer.lag` bu kurulumda `metric_points`'ta yok → panel "span sayımından türetilmiş tahmin" der, `WaitLockStrip`'in dürüst-yokluk deseni uygulanır |
| Gerçek yeniden-deneme sayısı (DLQ dilimi içinde) | Ne spans kolonu ne attribute var; SDK'nın `messaging.message.retry`/redelivery basması gerekir → **veri-yok** |
| KPI şeridinin "düzeltilmiş" gibi gösterilmesi (E) | `limit=100`'de "Total calls" pencere toplamı değil, dönen satırların toplamı. Düzeltme ayrı bir window-aggregate sorgusu; mockup mevcut davranışı **olduğu gibi** gösteriyor ki yanlış bir "düzeldi" izlenimi doğmasın |
| Deploy/versiyon before→after karşılaştırması (E) | Görsel olarak yeni yüzey üretmiyor; mevcut "Split by attribute" tablosunun içine `service.version` seçiliyken ikinci kolon grubu olarak giriyor. `service_version_5m` route dim'siz ve duration/error state taşımıyor → dürüst v1 iki pencereli aynı split okuması. Ayrı, küçük dilim |
| `DependencyStrip`'in eski önbellek/pencere davranışının #12'ye taşınması | **v0.9.257'de kaynağında düzeltildi**, taşınacak bir kusur kalmadı: şerit sayfa range'ini kullanıyor (24 sa tavan, "capped" yazıyor), önbellek `${service}@${since}` anahtarlı, sayımlar "≤N en büyük trace'lik örneklem" diye etiketli. `+N` linki de kırık değil — `/topology` emekli ama `App.tsx:118` query string'i koruyarak `/service-map`'e yönlendiriyor. #12 yine de gerekli, ama gerekçesi artık **route seviyesi + süre payı**; "range'e duyarsız cache" değil. #12'nin kendi cache'i: 60 s `serveCached`, anahtar `(service, route, sig, from/to snap, env, cluster)` — **sıralı + FNV digest**, `len()` değil (v0.5.187) |

---

/Users/cenk/Documents/gotrace/scratchpad/dt-parity/SENTEZ.md
