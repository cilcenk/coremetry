# /messaging — DT-parity ek yüzey brief'i

Mockup: `scratchpad/dt-parity/messaging-mockup.html` (tek dosya, dark + light,
harici istek yok; grafikler uPlot yerine temsilî SVG — gerçek uygulamada **yalnız
uPlot**, yeni kütüphane yok).

Mockup'taki senaryo: 6 saatlik pencere, `kafka/kafka-prod-01/transfer.posted`
üzerinde 10:50–12:10 arası bir **tüketici durması** (üretim düz, tüketim %68
düşüyor, birikme 65.9k mesaja çıkıyor, E2E lag 41 s, 13:30'a kadar catch-up ile
boşalıyor). Diurnal hacim + log-normal gecikme; tohumlu PRNG, determinist.

## Değişmeyen şeyler (kasıtlı)

- **Ana tablonun düzeni aynı.** Mevcut kolon sırası korunuyor
  (System · Cluster · Destination · Calls · Produce/min · Consume/min · Err % ·
  Avg · P50 · P99 · Trend · Top callers). Backlog Δ, Consume/min ile Err %
  **arasına**; P95, P50 ile P99 **arasına**; Worst consumer, Trend'den **sonra**
  giriyor — hiçbir mevcut kolon yer değiştirmiyor, hiçbir kolon kaldırılmıyor.
  (v0.9.61 Operations redesign, v0.8.428 kart-feed ve v0.9.509 Problems drawer
  reddedildi; ders alındı — bu brief'te düzen değişikliği önerisi **yok**.)
- **Drawer sekmelere çevrilmiyor.** Mevcut dikey akış (stat şeridi → sparkline
  üçlüsü → E2E → Publishers → Consumers → Top operations) korunuyor, araya bölüm
  ekleniyor.
- **`useDataTable` sözleşmesi aynı** — yeni kolonlar da `sortValue` + `numeric`
  ile geliyor, genişlikler `storageKey='deps-queue'` altında kalıcı.
- **Ham `spans` üzerinden yeni aggregate yok.** Tek yeni okuma
  (`/api/messaging/trends`) `messaging_summary_5m`'e gidiyor. Mevcut ham-spans
  aggregate'i (Top operations) bu brief'te **düzeltilmiyor**, ek #4'te not düştüm.
- **Yeni MV / yeni kolon gerektiren hiçbir madde bu altı yüzeyde yok.** Hepsi ya
  mevcut payload aritmetiği, ya da mevcut MV'lere yeni bir bounded sorgu.

---

## 1 · Backlog Δ kolonu (ana tablo) + Backlog paneli (drawer)

**Ne gösteriyor.** "Hangi kuyrukta birikme var" sorusunun satır seviyesindeki
cevabı. Hücre üç parça:

- **net** = pencerede üretilen − tüketilen span sayısı (işaretli),
- **oran rozeti** = tüketim / üretim (`0.95×` warn, `<0.90×` err, `≈1.00×` ok,
  `>1.02×` info = **fan-out**, hata değil),
- **pik rozeti** (koşullu) = pencere içi en yüksek kümülatif birikme, yalnız
  `pik > |net| × 2.2 && pik > 5000` iken.

Pik rozeti tesadüfi değil: mockup'taki olayda **net yalnız +17.9k, pik 65.9k**.
Tek başına net, geçici bir duruşu tamamen gizler — Dynatrace Queues'un
"backlog spike" göstergesinin karşılığı bu iki sayının birlikte okunması.

Drawer paneli: produce vs consume overlay (aradaki dolgu = birikme alanı) +
kümülatif backlog alan grafiği + pencere neti / pik / şu an / boşalma süresi /
oran çipleri.

**Besleyen MV / sorgu.**

- **Net + oran: sıfır ek CH maliyeti.** `MessagingInstance.ProduceCount` /
  `ConsumeCount` `/api/messaging` payload'ında **zaten dolu**
  (`chstore/dependencies.go:911-936`, `applyMsgKindSplit`); `DepRow` da taşıyor
  (`DependenciesTable.tsx:54-55`). Kolon saf frontend aritmetiği.
- **Pik: madde #2'nin trends ucundan gelir.** `/api/messaging/trends`
  serisine `kind` kırılımı eklendiğinde kümülatif `Σ(produce−consume)` frontend'de
  hesaplanır. Yani madde #1'in pik yarısı **madde #2'ye bağımlı** — sıralama
  #2 → #1 olmalı, ya da pik rozeti ikinci dilime bırakılmalı.
- **Drawer paneli: mevcut sorgu.** `GetMessagingDetail`'in
  `messaging_caller_summary_5m` `(kind × time_bucket)` serisi (`dependencies.go:487-499`)
  zaten çekiliyor ve bugün yalnız iki sparkline'a besleniyor; kümülatif seri aynı
  diziden türer.

**API ucu.** Kolonun net+oran yarısı için **yeni uç yok**. Pik için #2'nin ucu.
Drawer paneli için **yeni uç yok** (mevcut `/api/messaging/detail`).

**Efor.** ~2 saat (kolon + drawer paneli), #2 gemiye girdikten sonra +30 dk (pik).

**Riskler.**
- **Semantik tuzağı — en önemlisi.** `produce` ve `consume` **span sayılarıdır**,
  mesaj sayısı değil. N tüketici grubu olan bir topic'te `consume > produce`
  normaldir (mockup'ta `dispute.opened`, `2.02×`). Ham Δ'yı tek başına
  "birikme" diye sunmak yanlış olur; bu yüzden oran rozeti **zorunlu**, tooltip'te
  fan-out açıklaması **zorunlu**.
- Kind-split `LIMIT 2000` kapağının dışında kalan satırda Δ hesaplanamaz →
  `0` değil `—` (madde #6).
- Broker'ın gerçek offset lag'i (`messaging.kafka.consumer.lag` /
  `kafka.consumer_group.lag`) bu kurulumda `metric_points`'ta **yok**. Panel
  bunu açıkça yazıyor ("span sayımından türetilmiş tahmindir"). Kafka receiver
  bağlanınca `chstore/metricquery.go`'nun GroupBy-attr desteğiyle ikinci seri
  olarak biner. **Sahte sıfır çizilmemeli** — `WaitLockStrip`'in dürüst-yokluk
  deseni birebir uygulanır.

---

## 2 · Trend kolonu canlanıyor — `/api/messaging/trends` (Düzeltme)

**Ne gösteriyor.** Mevcut Trend kolonu, mevcut yerinde, mevcut boyutunda
(120×20 sparkline + son-kova p99/err çipleri) — sadece **ilk kez veri
gösteriyor**. Kolon eklenmiyor, kaldırılmıyor, taşınmıyor.

**Neden bugün ölü.** `DependenciesTable.tsx:253-272` `kind` ayrımı yapmadan
`api.dbTrends` çağırıyor; o uç `db_summary_5m`'i okuyor, o MV ise
`WHERE db_system != ''` ile kuruluyor (`store.go`, db_summary_5m bloğu) →
**messaging satırı sıfır**. Sonuç: her satır `—` **ve** her range değişiminde
bedelsiz bir `LIMIT 200000` ClickHouse sorgusu.

**Besleyen MV / sorgu.** `messaging_summary_5m` — aynı 5dk kovalar, aynı state
kolonları (`span_count_state` / `error_count_state` / `duration_q_state`,
`store.go:2765-2797`). Yeni uç `db_trends.go:93-106`'nın birebir aynası:

```
SELECT msg_system, cluster, destination,
       toUnixTimestamp64Nano(toDateTime64(time_bucket, 9)) AS bucket_ns,
       countMerge(span_count_state), countIfMerge(error_count_state),
       arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 3)/1e6
FROM messaging_summary_5m
WHERE time_bucket >= ? AND time_bucket <= ?
GROUP BY msg_system, cluster, destination, time_bucket
ORDER BY msg_system, cluster, destination, time_bucket
LIMIT 200000 SETTINGS max_execution_time = 15
```

`countIfMerge` şart — MV `countIfState(status_code='error')` tanımlıyor,
`countMerge` sessizce yanlış aggregate okur (db tarafındaki not birebir geçerli).
`from` 5 dk grid'e `Truncate` edilir (cache key + kova hizası stabil kalsın).

**API ucu.** **Yeni:** `GET /api/messaging/trends`, `s.serveCached` +
`"msg-trends:"+cacheBucket(from,to)`, 30 s TTL, `api.go` içindeki
`warm("messaging", …)` hattına ikinci bir warm eklenebilir (yalnız 1h penceresi).
Frontend: `DependenciesTable` içindeki `useEffect` `kind==='queue'` iken
`api.messagingTrends` çağırır — **koşul zaten gerekli**, bugünkü koşulsuz
`dbTrends` çağrısı /messaging için ölü yük.

**Efor.** ~2 saat.

**Riskler.**
- `messaging_summary_5m` satır sayısı `destination × cluster × 5dk kova` —
  214 topic × 288 kova (24 s) ≈ 61k satır, `LIMIT 200000` rahat. Ama pencere
  7 güne çıkarsa 430k'ya vurur → `LIMIT` **kapağa çarpınca sessizce kesilir**;
  db tarafında da aynı davranış var, ya kapağı pencereye göre ölçekle ya da
  "kırpıldı" işareti bas (madde #6 deseni).
- `trendFor` join'i bugün `(system|instance|dbName)` şeklinde; queue tarafı
  `(system|cluster|destination)` — join anahtarı ayrı tutulmalı, aksi halde
  `(default)` cluster'lı satırlar birbirine karışır.
- Rolling deploy: eski frontend + yeni backend zararsız; yeni frontend + eski
  backend 404 → `trends=null` → bugünkü `—` davranışı (regresyon yok).

---

## 3 · Err % hücresine triyaj pivotları (Yeni pivot)

**Ne gösteriyor.** Err % hücresinin yanında üç mikro-link: **✖** hatalı tüketim
span'leri (`/traces`), **≡** ilgili servisin logları (`/logs`), **⚑** exception
grubu (`/inbox`). Aynı üçlü drawer'ın Publishers / Consumers satırlarında
servis + rol kapsamıyla tekrar ediyor. Kolon eklenmiyor; hücrenin içi zenginleşiyor.

**Besleyen MV / sorgu.** **Ek okuma yok — hepsi link üretimi.**
`messagingTracesHref` (`lib/pivotHref.ts:99-103`) `role?: 'producer'|'consumer'`
ve `hasError` parametrelerini **zaten destekliyor**; iki çağıranın
(`DependenciesTable.tsx:237`, `DetailDrawer.tsx:334`) hiçbiri geçmiyor. Log
pivotu için hazır desen `ServiceSignalTabs.tsx:98` (`/logs?service=&range=`);
exception hattı `status_code='error'` + `error.type` üzerinden zaten inbox
tarafında (v0.9.489-499).

**API ucu.** **Yok.** Saf frontend.

**Efor.** ~1 saat.

**Riskler.**
- Ana tablodaki Err % hücresi dar; üç ikon `.pv` ile 17×17 px. Yoğunluk modunda
  taşmaması için `content-visibility` satırlarında `overflow:hidden` + `title`
  şart (`table-layout:fixed` + `nowrap` kırpma tuzağı — INCIDENTS.md).
- `/inbox` pivotu **destination boyutunu bilmiyor**; en iyi ihtimalle
  `service + error.type` daraltması yapabilir. Yanlış vaat vermemek için tooltip
  "bu tüketicinin exception grubu" demeli, "bu topic'in" değil.
- Satır tıklaması drawer açıyor → pivot linkleri `stopPropagation` almalı
  (mevcut destination/caller hücrelerindeki desen birebir).

---

## 4 · P95 + Worst consumer kolonları (ana tablo) + drawer'da P50/P95

**Ne gösteriyor.** P95 = kuyruk SLO'larının fiili eşiği; `p95DurationMs`
API'de **zaten dolu geliyor ve hiçbir yerde render edilmiyor**. Worst consumer =
"en yavaş tüketici" parite temasının satır seviyesindeki cevabı (servis adı +
p99 rozeti, `/service`'e link) — bugün bunu görmek için her topic'in drawer'ını
tek tek açmak gerekiyor. Drawer'ın Publishers/Consumers tabloları da P50/P95
kazanıyor.

**Besleyen MV / sorgu.** **Sıfır ek tarama.**

- P95: `getMessaging` zaten `arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)
  (duration_q_state), 2)` okuyor (`dependencies.go:862-863`) ve
  `MessagingInstance.P95Ms` / `types.ts` `p95DurationMs` dolu; eksik olan tek
  şey `DepRow` alanı + kolon.
- Drawer P50/P95: `messaging_caller_summary_5m.duration_q_state` üçlüyü
  taşıyor, sorgular bugün yalnız `arrayElement(…, 3)` = p99 çekiyor
  (`dependencies.go:423`, `:450`). **1** ve **2**'yi eklemek aynı merge,
  aynı maliyet.
- Worst consumer: aynı caller sorgusunda `kind='consumer'` satırlarından
  `argMax(service_name, p99)` — ek bir GROUP BY değil, mevcut sonucun katlanması.
  Overview için `getMessaging`'in kind-split pass'ine `service_name` +
  `argMax` eklemek yeterli (LIMIT 2000 aynı kalır).

**API ucu.** **Yeni uç yok.** `chstore.MessagingInstance`'a `WorstConsumer` +
`WorstConsumerP99Ms`, `DBCallerBreakdown`'a `P50Ms`/`P95Ms`; `lib/types.ts` +
`DepRow` + kolon tanımları.

**Efor.** ~1 saat.

**Riskler.**
- Cache key değişmiyor ama payload şekli değişiyor → rolling deploy'da eski slot
  yeni alanları taşımaz; 30 s TTL kendini toparlar, frontend `undefined` → `—`
  çizmeli (queue tarafındaki `p50DurationMs === undefined` deseni birebir).
- Worst consumer **p99'a göre** seçiliyor; tek çağrı yapmış bir pod p99'u
  şişirip yanlış "en yavaş" üretebilir. Minimum çağrı eşiği (ör. ≥ 50 span)
  veya `spanCount × p99` etki sıralaması daha dürüst — kararı ship'te ver,
  tooltip'te hangi ölçütle seçildiğini yaz.
- TDigest yaklaşıktır; düşük varyanslı topic'lerde P50 ≈ Avg ≈ P95 aynı sayıyı
  üç kolonda gösterir — kabul edilebilir.
- `storageKey='deps-queue'` kalıcı genişliklerde yeni kolonlar yok → varsayılan
  genişlikle gelirler (sorun değil).

---

## 5 · E2E bloğu zenginleşiyor: kapsam/örneklem rozetleri + en yavaş 5 çift

**Ne gösteriyor.** Mevcut p50/p95/p99 çipleri ve "slowest → trace" linki **aynen
duruyor**; altına bir panel geliyor:

- **Örneklem rozeti** — `50k / 679k consumer span (%7.4)`;
- **Link kapsamı** — örneklem içinde kaç consumer span'i bir producer'a bağlandı;
- **Pencere rozeti** — "tüketici gecikmesi > 1 sa olan çiftler ölçüme girmiyor";
- **Kırpma rozeti** — "negatif lag 0'a kırpıldı" (saat kayması);
- **En yavaş 5 çift** mini tablosu (lag · tüketici · pod · kova · → trace).

Değer: mockup'taki olayda 5 çiftin 4'ü aynı servis, 2'si aynı pod — tek exemplar
bu deseni **söyleyemiyor**. Ayrıca "SDK link basmıyor" ile "link var ama hiç çift
korele olmadı" artık iki ayrı metin (birincisi enstrümantasyon işi, ikincisi
pencere/örneklem işi).

**Besleyen MV / sorgu.** **Mevcut tarama yeterli.** `msgE2ESQL`
(`messaging_e2e.go:104-157`) çift başına `lag_ms` hesaplayıp `argMax` ile **tek**
exemplar döndürüyor; `argMax` yerine alt sorguya `ORDER BY lag_ms DESC LIMIT 5`
eklemek aynı scan'den top-5'i çıkarır. Kapsam oranı bedelsiz: `MsgE2E.Count`
zaten var, payda için tüketici span sayısı `GetMessagingDetail`'in caller
sorgusunda zaten hesaplanıyor. Kapak sabitleri
(`msgE2ESpanSideLimit = 50000`, `msgE2EPairLimit = 200000`,
`msgE2EProducerSlack = time.Hour`) doğrudan rozet metnine yazılır.

**API ucu.** **Yeni uç yok** — `MsgE2E`'ye `TopPairs []MsgE2EPair` +
`SampledConsumerSpans` / `TotalConsumerSpans` alanları.

**Efor.** ~2 saat.

**Riskler.**
- **En kritik dürüstlük noktası:** `LIMIT 50000`'in **`ORDER BY`'ı yok**. Yani
  bu rastgele bir örneklem değil, ClickHouse'un döndürdüğü ilk 50k satır —
  p50/p95/p99 sistematik sapma taşıyabilir. Rozet "örneklem" demeli,
  "%6.1'lik rastgele örneklem" **dememeli**. Uzun vadede
  `ORDER BY cityHash64(span_id) LIMIT 50000` ile deterministik-rastgele
  örnekleme daha savunulabilir (ayrı dilim, bu brief'te değil).
- Producer penceresi yalnız 1 saat geriye genişletiliyor → 1 saati aşan tüketici
  gecikmesi çiftten **düşüyor**, yani **gerçek lag gösterilenden BÜYÜK** olabilir.
  Rozet bunu açıkça söylüyor; slack'i büyütmek tarama maliyetini doğrusal
  artırır, **büyütme önerilmiyor**.
- Top-5 tablosu 5 satır — `useDataTable` şart değil ama tutarlılık için
  aynı primitive kullanılabilir (sıralama zaten sabit).

---

## 6 · Kırpma & yanlış-sıfır dürüstlüğü (filtre şeridi + hücreler)

**Ne gösteriyor.** Üç sessiz yalanın işaretlenmesi:

1. Filtre şeridi sayacının yanında **`⚠ 200 satır kapağı — 214 topic'ten ilki`**
   rozeti. Bugün 200'den fazla topic'i olan kurulumda aranıp bulunamayan topic
   operatöre "trafik yok" gibi görünüyor.
2. Kind-split kapağının dışında kalan satırlarda Produce/min · Consume/min ·
   Backlog Δ hücreleri **`0.0` yerine `—` + tooltip** (mockup'ta `mobile.push`).
   `0.0` "hiç üretim yok" der; doğru anlam "bilmiyoruz".
3. Top callers kapağının **isim sırasına** göre çalıştığının düzeltilmesi.

**Besleyen MV / sorgu.** MV değişmeden, sorgu düzeyinde:

- Overview: `dependencies.go:869` `LIMIT 200` → `LIMIT 201` okunur, 201. satır
  gelirse rozet basılır (satır gösterilmez). Toplam sayı için
  `SELECT uniqExact((msg_system,cluster,destination))` ikinci ucuz sorgu da
  olur ama **gerekmez** — "200+" yeterince dürüst.
- Callers: `dependencies.go:951-956` bugün
  `ORDER BY msg_system, cluster, destination, c DESC LIMIT 1000`. Bu **toplam**
  bir kapak ve sıralama **alfabetik** — alfabede sonda kalan topic'ler
  Top callers'ı tamamen kaybediyor. Doğrusu
  `LIMIT 5 BY (msg_system, cluster, destination)` (+ güvenlik ağı olarak
  `LIMIT 2000`): her destination kendi ilk 5'ini **garanti** alır, tarama
  değişmez, dönen satır sayısı düşer.
- Kind-split: `dependencies.go:911-924` eşleşme bulunmayan satırda
  `ProduceCount`/`ConsumeCount` **yazılmaz** (Go `*uint64` / omitempty) →
  frontend `undefined` görür → `—`.

**API ucu.** **Yeni uç yok.** `MessagingInstance`'a `Truncated bool` (overview
kapağı) + sayı alanlarının pointer'a çevrilmesi.

**Efor.** ~2 saat.

**Riskler.**
- Sayı alanlarını `uint64` → `*uint64` yapmak **payload sözleşmesini değiştirir**;
  `DepRow`'daki `producePerMin` hesabı `?? 0` ile 0'a düşüyor
  (`Messaging.tsx:108-109`) — o iki satır da `undefined` taşımalı, yoksa
  düzeltme frontend'de geri yenir. Bu, dilimin **asıl riski**.
- `LIMIT 5 BY` distributed CH'de `GROUP BY` sonrası uygulanır; `cluster_name`
  unset olan prod kurulumunda `cluster()` sarmalıyla okunan sorgularda davranışı
  **doğrulanmalı** (prod-distributed latent bug sınıfı).
- Rozet metni yanlış pozitif üretmemeli: tam 200 satır dönen ama gerçekten 200
  topic'i olan kurulumda "200+" yazmak yanlış olur — bu yüzden `LIMIT 201`.

---

# Mockup'ta göstermediklerim (sıradaki, öncelik sırasıyla)

**Ek #1 — Env seçici ölü (EN YÜKSEK ÖNCELİK, gap#4).** Topbar EnvPicker
render ediliyor, operatör seçim yapıyor, **sayılar değişmiyor**: handler env
parse etmiyor (`api_databases.go:120-205`) ve `messaging_summary_5m` /
`messaging_caller_summary_5m` şemalarında `deploy_env` boyutu yok
(`store.go:2765-2833`). Sessizce yanlış cevap veren kontrol, hiç olmayan
kontrolden tehlikelidir — prep/uat trafiği prod topic sayılarına karışıyor.
Mockup'ta yalnızca **uyarı rozeti** olarak işaretledim (yeni yüzey değil, ölü
kontrolün düzeltilmesi). Gereken: iki MV'ye `deploy_env LowCardinality(String)`,
**inner-table ALTER + MODIFY QUERY** ile (DROP+RECREATE gerekmez,
`reference-ch-inplace-mv-column-add`), `hasXCol` probe + koşullu okuma
(distributed-safe kuralı, prod'u iki kez kırdı). Kardinalite artışı env sayısı
kadar (~3-5×), LowCardinality ile disk etkisi küçük. Kısa vadeli çapraz doğrulama
kaynağı: `topology_edges_5m` queue kenarlarında `parent_env`/`child_env` **zaten
var**. Efor ~yarım gün. Altı yüzeye almadım çünkü tek başına bir dilim ve
"çizilecek" bir yüzeyi yok.

**Ek #2 — Ters pivot: /service ve /topology'den kuyruğa yol yok (gap#8).**
`/messaging`'e giden tek link Sidebar + CommandPalette. `/service` sayfasına
"Queues & topics" paneli (`messaging_caller_summary_5m`, service_name boyutu
var — **ama ORDER BY prefix'i `(msg_system, cluster, destination, service_name…)`
olduğu için tek servis filtresi prefix budaması ALMAZ**: zaman sınırı + LIMIT 200
+ `max_execution_time` zorunlu). Topolojide graf düzenine dokunmadan, elenmiş
queue düğümü için "Messaging'de aç" bağlantısı. Efor ~yarım gün.

**Ek #3 — DLQ / dead-letter sinyali (gap#9).** Mockup'ta `transfer.posted.DLT`
satırı sıradan bir satır olarak duruyor (yanına soluk `DLQ?` rozeti koydum).
`.dlq` / `.dlt` / `-dead-letter` / `.retry` son eklerinden kardeş eşleştirmesi
saf string işi, **ek sorgu maliyeti sıfır**; "son 7 günde ilk kez DLQ trafiği"
karşılaştırması #2'nin trends ucundan çıkar (MV 90 gün TTL taşıyor). Gerçek
**yeniden deneme sayısı VERİ-YOK**: ne spans kolonu ne attribute var; SDK'nın
`messaging.message.retry` / redelivery basması gerekir. Efor ~2 saat.

**Ek #4 — Top operations hâlâ ham `spans`.** `dependencies.go:534-544`
sayfadaki tek MV-dışı aggregate (`GROUP BY name` over raw spans, LIMIT 20,
`max_execution_time=15`). Mockup'ta panelin başlığına `⚠ ham spans` rozeti
koydum ama düzeltmeyi altı yüzeye almadım: doğru çözüm bir
`messaging_operation_summary_5m` MV'si (boyut: msg_system × cluster ×
destination × name × kind × kova) ve bu **yeni MV** kararı — kardinalite
ölçülmeden alınmamalı. Efor ~1 gün, önce ölçüm.

**Ek #5 — Partition / consumer-group kırılımı (gap#10).** Kafka'da gecikmenin
en yaygın nedeni tek bir partition ya da tek bir consumer group'un geride
kalması; topic seviyesinde p99 sağlıklı görünürken bu tamamen görünmez.
`messaging_summary_5m` boyutları buna yetmiyor; `messaging.kafka.destination.partition`
/ `messaging.consumer.group.name` attribute'ları `spans.attr_keys` içinde
verbatim durur **ama demo bunları basmıyor** (`cmd/demo` yalnız
`messaging.system` / `destination` / `operation`) ve `convert.go:126` sadece
`messaging.system`'i kolona alıyor. **Önce canlı veride varlığı ölçülmeli**;
yoksa madde veri-yok'a düşer. Yeni MV + LowCardinality + distributed-safe
(`hasXCol` probe + koşullu INSERT) — efor ~1 gün, ölçüm sonrası.

---

# Önerilen ship sırası

| # | Dilim | Efor | Neden bu sırada |
|---|---|---|---|
| 1 | #6 kırpma dürüstlüğü | ~2 sa | Yanlış-sıfır düzeltmesi; #1'in `—` davranışı buna bağlı |
| 2 | #2 Trend ucu | ~2 sa | Ölü kolon + bedelsiz CH sorgusu; #1'in pik rozeti buna bağlı |
| 3 | #1 Backlog Δ + panel | ~2 sa | Sayfanın manşet parite eksiği |
| 4 | #4 P95 + Worst consumer | ~1 sa | Sıfır ek tarama, anında değer |
| 5 | #3 Err % pivotları | ~1 sa | Saf frontend, sıfır backend riski |
| 6 | #5 E2E kapsam + top-5 | ~2 sa | Async RCA derinliği |
| — | Ek #1 env | ~yarım gün | MV kolonu; ayrı dilim, ayrı doğrulama |

Her dilim CLAUDE.md ship checklist'ine tabi: MV-first okuma → `serveCached`
(hash-all-inputs key) → auth gate → `lib/types.ts` + `lib/api.ts` →
loading/error/empty → `npx tsc --noEmit` → `go build ./...` → `go test ./...`
→ `make audit` → kendi `v0.9.X` tag'i (batch **yok**).
