# /databases — DT-parity ek yüzey brief'i

Mockup: `scratchpad/dt-parity/databases-mockup.html` (tek dosya, dark+light,
harici istek yok; grafikler uPlot yerine temsilî SVG — gerçek uygulamada **yalnız uPlot**).

## Değişmeyen şeyler (kasıtlı)

- **Ana tablonun düzeni aynı.** Kolon sırası korunuyor (System · Database ·
  Instance · Calls · Err % · Avg · … · P99 · Trend · Top callers); P50/P95
  Avg ile P99 **arasına** giriyor, Health ise Trend'den **sonra** — mevcut
  hiçbir kolon yer değiştirmiyor. (v0.9.61 Operations redesign / v0.8.428
  kart-feed / v509 Problems drawer reddedildi; ders alındı.)
- **Drawer sekmelere çevrilmiyor.** Mevcut dikey bölüm akışı (stat şeridi →
  wait/lock → By client → statements) korunuyor, araya bölüm ekleniyor.
- **İki panel ayrımı korunuyor** (span kaynaklı "Called from services" vs
  metric_points kaynaklı "DB receiver instances").
- **Ham `spans` üzerinden aggregate yok.** Tek istisna: exemplar nokta-okuması
  (aggregate değil, `ORDER BY duration DESC LIMIT 1`) ve N+1 tekrar analizi —
  ikisi de MV'ye çevrilemez, doğaları gereği trace-içi.

---

## 1 · P50 + P95 kolonları (ana tablo)

**Ne gösteriyor.** Her DB satırının tipik çağrısı ile kuyruğu arasındaki
farkı. Bugün operatör `avg 18.6ms / p99 912ms` görüp arada ne olduğunu
bilemiyor; oracle satırında p50 6.2ms / p95 74ms okuması "tipik iş sağlam,
kuyruk yanıyor" teşhisini tek bakışta veriyor.

**Besleyen MV / sorgu.** Mevcut ve **bedava**:
`db_summary_5m.duration_q_state = quantilesTDigestState(0.5, 0.95, 0.99)`
(`internal/chstore/store.go:2477` bloğu). Okuma bugün yalnız
`arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 3)`
alıyor (`dependencies.go:616`); eleman **1** ve **2** aynı merge'den, **sıfır
ek tarama** maliyetiyle çıkar. Aynı üçlü `db_caller_summary_5m` okumasında da
var (`dependencies.go:245, :269`) → drawer'ın By client tablosu da bedava P50/P95 alır.

**API ucu.** Mevcut `GET /api/databases` — yeni uç **yok**. Değişiklik:
`chstore.DBInstance` (`dependencies.go:19-41`) `P50Ms` / `P95Ms` alanları +
`lib/types.ts` `DBInstance` + `DepRow` (P50 zaten var, `p50DurationMs`) +
`DependenciesTable` koşullu-kolon deseni (`hasClusterCol` / queue-only
kolonların ikizi, satır 170-209).

**Efor.** ~1 saat.

**Riskler.**
- Cache key: payload şekli değişiyor ama key değişmiyor → rolling deploy'da
  eski cache slotu yeni alanları taşımaz. 30s TTL ile kendini toparlar;
  ön yüz `undefined` P50/P95'i "—" olarak çizmeli (queue tarafındaki
  `p50DurationMs === undefined` deseni birebir).
- `useDataTable` kalıcı kolon genişlikleri `storageKey='deps-db'`'de; yeni
  kolon eski kayıtta yok → varsayılan genişlikle gelir (sorun değil, ama
  **iki tablo aynı storageKey'i paylaşıyor** — bu zaten var olan bug, bu
  dilimde düzeltmek ucuz: `deps-db-spans` / `deps-db-receiver`).
- Kantil ondalığı: TDigest yaklaşıktır; p50 ile avg'ın çakıştığı düşük
  varyanslı DB'lerde "aynı sayı iki kolon" görünür — kabul edilebilir.

---

## 2 · Health kolonu — doygunluk + açık problem (ana tablo, ağırlıklı receiver)

**Ne gösteriyor.** "Hangi veritabanım **şu an** tehlikede" sorusunun satır
seviyesindeki cevabı: connection/session/tablespace doluluk metresi + o
instance için açık Problem rozeti. Bugün bu bilgi 14-19 kutucuk derinlikteki
motor panelinde gömülü, ve receiver satırları RED'i her zaman 0 döndüğü için
(`dependencies.go:766-771`) panelde **"0 çağrı, %0 hata" sağlıklı gibi
okunuyor**. Mockup'ta receiver satırlarının RED hücreleri sahte sıfır yerine
"—", tek dürüst sinyal Health kolonunda.

**Besleyen MV / sorgu.** Mevcut:
- Doygunluk: `chstore/db_capacity.go` `UsageLimit` — instance başına
  (usage, limit) gauge çifti, tek trip, 10dk pencere, `LIMIT` +
  `max_execution_time` mevcut. Kapsanan metrikler: `postgresql.backends` /
  `postgresql.connection.max`, `mysql.connection.count` /
  `max_used_connections`, `oracledb.sessions.usage|limit`, tablespace.
- Açık problem: `problem.service = capacityService(instance, subkey)` =
  `instance` veya `instance·SUBKEY` (`evaluator/db_capacity.go:184`).
  Toplu "servis kümesine göre açık problemler" okuma deseni hazır:
  `chstore/problem.go:468` / `:660`.

**API ucu.** **Yeni bir uç değil**, mevcut `/api/databases` yanıtına iki alan
(`saturationPct`, `saturationLabel`, `openProblems`) — ama backend'de **iki ek
okuma** (capacity + problem toplu). `serveCached` altında, 30s TTL. Alternatif
(daha temiz): ayrı `GET /api/databases/health` ucu, ön yüzden **ikinci** bir
istekle çekilip satıra join edilir — böylece ana tablo bu iki okumayı
beklemez ve `/api/databases` p99 bütçesi (< 200ms warm) bozulmaz. **Önerilen:
ayrı uç.**

**Efor.** ~yarım gün.

**Riskler.**
- **Join anahtarı string.** Span satırının `instance`'ı (peer.service /
  server.address fallback zinciri) ile receiver'ın instance etiketi her zaman
  birebir tutmaz. Eşleşmezse "—" göster — asla tahmin etme. Mockup'ta
  `ora-crm-scan.corp` her iki tabloda da aynı; canlıda böyle olmayabilir.
- Span satırlarında pool verisi hiç yok → kolon çoğunlukla boş görünür.
  Dürüst boşluk, ama "yeni kolon niye boş" sorusu gelir; kolon başlığı
  tooltip'i "receiver metriklerinden; SDK-only DB'lerde yok" demeli.
- Problem sorgusu satır sayısı kadar değil **tek toplu** okuma olmalı
  (N+1 API çağrısı = /databases'i kilitler).
- Evaluator eşiği ile buradaki renk eşiği ayrışırsa operatör "kırmızı ama
  problem yok" görür → eşiği `evaluator/db_capacity.go` ile **aynı sabitten**
  oku, ön yüzde yeniden tanımlama.

---

## 3 · "Compare vs prior" toggle + delta rozetleri (filtre şeridi + mevcut hücreler)

**Ne gösteriyor.** "Bu DB şimdi bir saat öncesinden kötü mü" — triage'ın ilk
sorusu. Mockup'ta oracle satırı `Err ▲412%` / `P99 ▲306%` / `Calls ▼14%`
okuyor: throughput düşerken kuyruğun patlaması doygunluk imzası, yük artışı değil.

**Besleyen MV / sorgu.** Aynı `db_summary_5m`'e ikinci, **eşit uzunlukta
kaydırılmış** pencere okuması. Birebir şablon mevcut:
`internal/api/api_databases.go:120-152` (`compare=prior` bayrağı + ikinci
`GetMessagingRollup`) ve `mergeMessagingPrior` (`:154+`).

**API ucu.** Mevcut `/api/databases` + `?compare=prior`. Cache key **ayrı
slot** (`databases:cmp:` + `cacheBucket`) — messaging'deki gibi, varsayılan
key byte-identical kalsın ki `warm("databases", …)` ısıtma döngüsü hâlâ sayfa
yükünün vurduğu slotu doldursun.

**Ön yüz zaten hazır.** `DepRow.priorSpanCount/priorErrorCount/priorAvgMs/
priorP50Ms/priorP99Ms` alanları ve `TrendDelta` render'ı `DependenciesTable`
içinde **var ve hiç doldurulmuyor**; `extraControls` prop'u da tam bu iş için
duruyor. Backend'de eksik olan tek şey `DBInstance`'ta `Prior*` alanları
(messaging ikizinde `dependencies.go:91-97` var).

**Efor.** ~2 saat.

**Riskler.**
- CH maliyeti **iki katına** çıkar — opt-in olduğu için kabul; toggle
  varsayılan **kapalı**, URL'de `?compare=prior` (replace:true).
- Prior okuma hatası **fatal olmamalı**: messaging deseninde olduğu gibi
  hata durumunda mevcut satırlar delta'sız döner, 500 atılmaz.
- Prior pencerede olmayan satır → `Prior*` sıfır → `omitempty` ile JSON'dan
  düşer → rozet çizilmez. `prior === 0 && cur > 0` durumunda `TrendDelta`
  "NEW" basıyor; DB'de bu "yeni keşfedilen instance" demek, doğru okuma.
- `|Δ| < %5` eşiği nötr "·" — düşük hacimli DB'lerde gürültüyü bastırır ama
  gerçek küçük regresyonu da saklar; messaging ile aynı eşik, tutarlılık kazanır.

---

## 4 · Drawer: exemplar pivotları — "En yavaş trace" / "Hatalı trace"

**Ne gösteriyor.** Satırda `p99 912ms` gören operatörün "bana bunun bir
örneğini göster" hamlesi. Bugün `/databases` satırında **hiç yok**;
`/slow-queries` drawer'ında gerçek exemplar **var** — aynı ürün içinde iki
farklı derinlik.

**Besleyen MV / sorgu.** DB MV'lerinde `trace_id` yok (tasarım gereği).
Hazır desen: `chstore/dbstmt_detail.go:304-340` `DBStmtExemplars` —
`db_stmt_hash` üzerinde slow + errorOnly ikili **nokta-okuma**
(`ORDER BY duration DESC LIMIT 1`, `SETTINGS max_execution_time = 10` +
`shardSkipSetting`). DB satırı için aynısı: time-bounded WHERE +
`db_system` / instance eşleşmesi + **`service_name IN (<caller listesi>)`
PK-prefix kısıtlaması** (bu numara `dependencies.go:305-315`'te zaten
kullanılıyor) → iki nokta-okuma. **Yeni MV / kolon gerekmez.**

**API ucu.** Mevcut `GET /api/databases/detail` payload'ına iki alan
(`slowTraceId`, `errorTraceId`), bölüm-toleranslı (boş = "bu pencerede
exemplar yok", hata değil). Drawer zaten lazy — sayfa yükünde çalışmaz.

**Efor.** ~2 saat.

**Riskler.**
- PK-prefix kısıtlaması olmadan bu sorgu tüm `spans` partition'ını süpürür.
  Caller listesi boşsa (receiver satırı) **sorguyu hiç çalıştırma**.
- Distributed CH'de shard-skip ayarı unutulursa fan-out; `shardSkipSetting()`
  aynen kullanılmalı (`dbstmt_detail.go` deseni).
- "No rows" meşru sonuç, error değil — `dbstmt_detail.go:340` civarındaki
  no-rows toleransı birebir kopyalanmalı.
- Exemplar bir **uç** örnektir, tipik değil; etiket "En yavaş" demeli,
  "temsilî" değil.

---

## 5 · Drawer: RED zaman serisi paneli (uPlot, 3 seri)

**Ne gösteriyor.** "Bu DB ne zaman bozuldu, hata mı gecikme mi önce yükseldi,
throughput arttı mı yoksa düştü mü". Mockup'taki oracle olayında üçlü birlikte
okunduğunda teşhis net: **throughput düşerken** p99 patlıyor → doygunluk,
yük değil.

**Besleyen MV / sorgu.** **Veri sunucudan zaten geliyor ve atılıyor.**
`chstore/db_trends.go:16-21` `DBTrendPoint{T, Rps, ErrorRate, P99Ms}`,
`db_summary_5m` 5dk kovalarından; handler `/api/databases/trends`
(`api_databases.go:34`), TTL 30s. Ön yüz bugün **yalnız `rps`'i** 120px
sparkline'a çiziyor; `errorRate` ve `p99Ms` yalnız son-kova chip'lerine
düşüyor (`DependenciesTable.tsx:585-621`).

**API ucu.** **Yeni uç yok, backend'e tek satır dokunmadan.** Drawer,
tablonun zaten çektiği `DBTrend`'i prop olarak alır (ikinci fetch **yok**).

**Efor.** ~2 saat.

**Riskler.**
- `GetDBTrends`'in `LIMIT 200000` tavanı var: ~694 DB × 288 kova (24h)
  sonrası seriler **kesilir**. Panel bunu "trend yok / pencere kısaltın"
  diye **dürüstçe** göstermeli, yarım seriyi tam gibi çizmemeli.
- uPlot dışında kütüphane **yok**; tema değişiminde seriler
  `data-theme` üzerinden yeniden çözülmeli (mevcut chart hook'larındaki
  re-resolve deseni).
- Drawer içinde 3 uPlot instance × N açık satır → mount/unmount disiplini
  şart (satır kapanınca `destroy()`); "hooks-in" tuzağı Explore v2'de
  yaşandı (`project-explore-v2-progress`).
- **Tek eksen paylaşımı**: üç grafiğin x ekseni birebir hizalanmalı, yoksa
  "önce hangisi yükseldi" okuması yanlış çıkar.
- İleri adım (bu dilimde **değil**): `service_version_5m` + `GetServiceDeploys`
  ile dikey deploy marker'ları — aynı grafik bileşeni tekrar kullanılır.

---

## 6 · Drawer: Statements tablosu — MV kaynaklı, sıralanabilir, kalıcı kimlikli

**Ne gösteriyor.** "Bu veritabanını hangi sorgu yoruyor". Bugünkü cevap
80 karaktere kırpılmış, sıralanamaz, p95/p99/hata sayısı olmayan ve zaman
bütçesi dolarsa **sessizce boş** dönen bir liste — üstelik **ham `spans`
tarıyor** (`dependencies.go:315`), yani MV-first invaryantının ihlali.

**Besleyen MV / sorgu.** Mevcut: `db_statement_summary_5m`
(`store.go:2580` bloğu) — `stmt_hash` (xxHash64, **kalıcı**),
`sample_stmt_state`, `count/error/sum` + `quantilesTDigestState(0.5,0.95,0.99)`
+ `duration_max_state`; boyutlar `(db_system, db_name, service_name, stmt_hash,
time_bucket)`. Drawer `db_system` + `db_name` + caller servis listesini zaten
biliyor → ham spans GROUP BY **tamamen kalkar**.

**Tek boşluk — açıkça yazıyorum.** MV'de `instance` boyutu **yok**. Bir
`db_name`'i birden fazla host servis ediyorsa satırlar katlanır (aynı sorgu
iki hostta = tek satır). Kesin per-instance için:
- MV `ORDER BY`'ına `instance` eklenmeli → **in-place** yol:
  iç tablo `ALTER` + `MODIFY QUERY` (`reference-ch-inplace-mv-column-add`),
  DROP+RECREATE değil.
- **Kardinalite çarpanı**: host-per-dbname, tipik **1-3**. Baskın terim
  `stmt_hash` (binler) olduğu için satır sayısı ~1-3× artar, ama depolamada
  ağır olan `sample_stmt_state` + TDigest state'leri de aynı oranda çoğalır →
  **gerçek maliyet**: `db_statement_summary_5m` boyutu 1-3×. Ölçmeden
  yapılmamalı (`system.parts` üzerinden mevcut boyut alınıp çarpılmalı).
- **Distributed güvenliği zorunlu**: `hasXCol` probe + koşullu okuma +
  cluster_name unset kurulumda ALTER'ı atla (prod'u iki kez kırdı:
  v0.8.185 cluster, v0.8.186 op_group).

**Kademeli çıkış önerisi.** Faz A: MV'yi **olduğu gibi** kullan (instance
boyutu yok), tabloya "bu host'un db_name'i için toplam" etiketi koy —
ham spans taraması hemen ölür, kazanç anında. Faz B: instance boyutu,
ölçüm sonrası, ayrı sürüm.

**API ucu.** Mevcut `/api/databases/detail` içindeki `topOps` bölümünün
**kaynağı** değişir + alanlar zenginleşir (`p95`, `p99`, `max`, `errorCount`,
`stmtHash`). Satırdan pivot: `/databases/slow-queries?stmt=<hash>` — D2
drawer'ı (trend + caller + exemplar + prior) **zaten var**, tek eksik kapı.

**Efor.** ~yarım gün (Faz A). Faz B (instance boyutu) +yarım gün, ölçüm dahil.

**Riskler.**
- MV'de `service_name` boyutu var ama `instance` yok → **yanlış birleştirme**
  riski yukarıda; Faz A'da etiketle dürüst kal.
- `sample_stmt_state` `anyState` — gösterilen metin sınıfın **bir örneği**,
  parametreler farklı olabilir; UI "örnek ifade" demeli.
- `useDataTable` benimsenirken drawer içindeki her tablo **ayrı storageKey**
  almalı (`deps-callers-*` deseni), yoksa sıralama/genişlik birbirini ezer.
- 128 statement × açık satır → `content-visibility: auto` + sunucu tarafı
  `LIMIT` şart.

---

## Mockup'a girmeyen, ama sıradaki adaylar

| Gap | Neden şimdi değil | Not |
|---|---|---|
| **Hataların TİPİ + `/exceptions` pivotu** | DB MV'lerinin üçü de yalnız `countIfState(status_code='error')` tutuyor; hata **tipi boyutu yok**. Ucuz yol drawer açılınca ham spans üzerinde `service_name IN (callers)` PK-prefix kısıtlı `GROUP BY error.type`; kalıcı yol **yeni** `db_error_summary_5m` (yalnız hatalı trafik kadar satır → `db_summary_5m`'in küçük bir kesri) ama yeni MV = distributed-safe kurulum + `hasXCol` kapısı. | ~1 gün; en yüksek değerli **sonraki** kalem |
| **N+1 / çağrı-patlaması** | Dedektör **gemide** (`chstore/repeats.go` `QueryRepeatedSpans`, `GET /api/spans/repeats`, `GroupBy` varsayılanı zaten `['db.statement']`), yalnız kapısı yok. Drawer'a **aç-fetch** bölüm olarak eklenir (liste prefetch YOK). | ~yarım gün |
| **Deploy marker'ları** | `service_version_5m` + `GetServiceDeploys` hazır; #5'teki grafik bileşeni üzerine biner. | ~yarım gün |
| **Giriş pivotu + `range` taşıma** | `/databases`'e link veren **tek** yer Sidebar + CommandPalette. Ayrıca mevcut çıkış linkleri (Explore, Slow queries, statement→traces) `from/to` **taşımıyor** → paylaşılan link karşı tarafta başka pencere açıyor (v0.9.256'da messaging için düzeltilen hatanın DB ikizi). Veri gerekmez. | **~30dk — en ucuz kazanç, ilk gönderilecek kalem bu olmalı** |

## Sıra önerisi

1. Giriş pivotu + `range` taşıma (~30dk, veri gerekmez, mevcut bug sınıfı)
2. P50 + P95 (~1s, bedava veri)
3. Exemplar pivotları (~2s, hazır desen)
4. RED zaman serisi paneli (~2s, veri zaten geliyor ve atılıyor)
5. Compare vs prior (~2s, ön yüz hazır)
6. Statements MV-first Faz A (~yarım gün, **invaryant ihlalini de kapatır**)
7. Health kolonu (~yarım gün, iki ek okuma — ayrı uç)

Her kalem kendi `v0.9.X` sürümü olarak gider (batch yok).
