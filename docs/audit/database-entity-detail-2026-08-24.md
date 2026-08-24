# Database entity detay denetimi — 2026-08-24

**HEAD:** `79199285` (v0.9.1353) · **Yöntem:** salt okuma; hiçbir dosya değiştirilmedi, hiçbir build/test/git-yazma çalıştırılmadı. Beş mercek paralel çalıştı, bu doküman bulguları operatörün sorduğu sırayla birleştiriyor.

**Satır numarası sözleşmesi.** `internal/` numaraları HEAD'de doğrulandı (dosyalar çalışma ağacında kirli değil). `frontend/src/` numaraları **HEAD'de şu an** geçerli — bu denetim yazılırken başka bir ajan `/trace` + `/logs` link üreticileri üzerinde çalışıyordu (v0.9.1347+), numaralar kaymış olabilir. Alıntı **metinleri** sabit, numaralar değil.

**⚠ YÖNTEM KURALI (v1'de İHLAL EDİLDİ, düzeltildi).** *Bir yüzeyin YOKLUĞU tek sembol adı grep'iyle iddia edilmez.* v1 §4 satır 8 "trace pivotu YOK (grep: `traceHref` 0 eşleşme)" diyordu; depodaki üretici `dbTracesHref` (büyük T) ve pivot **gemide** (`DatabaseDetail.tsx:25`, `:214-218`). MEMORY *"Gate tek-yazım kör noktası"* dersinin birebir tekrarı — üstelik aynı doküman o dersi §2.2'de başkasına karşı kullanıyor. Yokluk iddiası artık **en az iki yazım + import satırı + dosyada ilgili rota dizgisi** taranarak kurulur.

**⚠ REVİZYON — v2, 2026-08-24.** Dış eleştiri turu sonrası. Değişenler: §0/§3'ün kategorik "HAYIR"ı **demo-kaynağı ayıklanarak** indirildi; §4 satır 8 (trace pivotu) YANLIŞTI → ✅; §4 "Pivotlar" paragrafı **tersine çevrildi**; §7.3 yol 3 **AÇIK DEĞİL** işaretlendi; §7.1'e `metric_points` eklendi; §2'ye kimliği TÜKETEN yerler (env süzgeci) eklendi; §3.8 **hipotez** olarak etiketlendi; plan tablosu yeniden kuruldu (F4.2 SİLİNDİ, F0.6-F0.9 eklendi, F0.2/F2.1 bölündü).

⚠ **Lokal ortam bayat.** Lokal imaj **v0.9.1315**; `problems` tablosunda `kind` kolonu yok, `db:` öznesi 0 satır. Yani **v0.9.1318 / 1327 / 1338 / 1345'in çalışan davranışı bu oturumda hiç gözlemlenmedi.** Tüm CH ölçümleri (a) v0.5.349'dan beri sabit MV DDL'lerine, (b) ham `spans`/`metric_points` verisine, (c) kod okumasına dayanıyor.

---

## 0. Yönetici özeti

Operatörün sorduğu "database drawer" **artık yok** — v0.9.840'ta emekli, satır tıkı `/database` tam sayfasına gidiyor.

| Soru | Cevap |
|---|---|
| 1 · veri akışı | 3 yüzey, 7 uç, 3 ayrı state mekanizması; üç ölçülmüş kusur + **iki kaynağın PENCERESİ farklı** (spans 90g MV / receiver 7g ham) |
| 2 · kimlik | **5 gerçek üretici + 2 önek okuyucu + 3 "kimlik değil"**; A3/A4 yalnız HAT A'nın span tarafını topladı. Ayrıca kimliği **TÜKETEN** bir yer envanterin dışındaydı: **env süzgeci db problemlerini sessizce eliyor** |
| 3 · URL'e konabilir mi | **Kategorik HAYIR DEĞİL** → *"bugünkü lokal veride ölçülemiyor"*. Dört gerekçenin **üçü** (§3.1/3.2/3.4) demo jeneratörünün bilinçli sabitlerini ölçüyor (`cmd/demo/main.go:552`, `mesh.go:206`). Demo'dan bağımsız duran **iki** gerekçe: §3.3 (6. basamak, Coremetry'nin KENDİ self-telemetrisi — demo'da `clickhouse` db span'i **yok**, doğrulandı) ve §3.5 (HAT A/HAT B, **bugün çözülmemiş** merkez engel). §3.6 kod-kod, geçerli |
| 4 · sayfa iskeleti | Zaten var (`/database`); **trace pivotu GEMİDE** (v1 yanlıştı), eksik: **log** pivotu, "en sık" sıralaması (iki uçta birden) |
| 5 · tek gövde | Bugün **kopya**; emsaller var, ama kapsayıcı değil söz dağarcığı paylaşılmalı. Üç `Stat` **bayt-bayt değil** — prop yüzeyi kararı ister |
| 6 · ortak iskelet | db+queue union'ı **zaten var** ve **4 kez ısırdı**; endpoint union'ın dışında. **Backend ayağı: messaging MV ailesi db ile izomorf** |
| 7 · ham span | /database için üç dilim uzakta — **ikisi** MV cerrahisi istemiyor, **üçüncüsü (ALTER) G6 reddine ÇARPIYOR**. Ayrıca motor paneli kalıcı olarak ham `metric_points`'te |

---

## 1. Database detayını besleyen bileşen ve veri akışı

### 1.1 Çekmece emekli, yüzey tam sayfa

`Databases.tsx:61-64` — *"v0.9.840 — SATIR-ALTI ÇEKMECE EMEKLİ."* Emeklilik rewrite değil bir **prop**: `DependenciesTable.tsx:128` `onRowNavigate?`, `:469` `const isOpen = !onRowNavigate && openKey === rowKey`, `:311` satır tıkı erken dönüyor, `:483` glif `'▸'`→`'›'`. `Databases.tsx:339` ve `:381` — **iki tabloya da** prop geçiliyor. `/messaging` prop'u vermediği için çekmecesini koruyor (`Messaging.tsx:214-218`, `kind="queue"`).

**Sonuç:** `features/dependencies/DetailDrawer.tsx`'in bütün `kind === 'db'` dalı **çalışma zamanında erişilemez** — depo bunu zaten yazıyor (`DatabaseDetail.tsx:63-69`).

### 1.2 Uç haritası — `/database`

1. `GET /api/databases/detail?system&instance&dbName&from&to` → `api_databases.go:131` → `dependencies.go:268` → `db_caller_summary_5m` ×2 (`:322-334` agrega, `:348-365` çağıran) **+ ham `spans`** (`:413-429`, TopOps). Cache FNV(system\0instance\0dbName)+kova, 30s (`api_databases.go:141-160`).
2. `GET /api/databases/trends?from&to` → `api_databases.go:97` → `db_trends.go:137-150` → `db_summary_5m`, `LIMIT 200000`, `max_execution_time=15`. **Cache anahtarı yalnız pencere** (`:99`), 30s.
3. `GET /api/databases/slow-queries?db_system&db_name&limit=10` → `api.go:2935` → `dbqueries.go:190`. Cache 60s (`api.go:2949`).
4. Koşullu, yalnız `?source=receiver`: `/api/databases/{oracle|postgres|mysql|redis}` → `api_databases.go:168/181/192/203` → `metric_points`, 30s.

### 1.3 State nerede

- **URL** (hepsi `replace:true`): `range`, `env`, `dbsys`, `dbname`, `compare`, `msys`, `q`, `stmt`, `stmtcmp` + `/database`'de `system/instance/name/source`. Üretici `databaseParam.ts:34-45`, ayrıştırıcı `:89-100`.
- **React Query**: 6 anahtar, hepsi `staleTime: 30_000`.
- **Çıplak `useState`+`useEffect`** (cache/dedupe/retry yok): trend fetch'i (`DependenciesTable.tsx:168` + `:366-401`) ve **dört motor paneli** (`panels/OraclePanel.tsx:19-31`, `RedisPanel.tsx:19-22` aynı desen — ayrıca `setData(undefined)` ile range değişiminde panel boşalıp geri geliyor).

**Her panelin loading / empty / error durumu** (v2'de eklendi — v1 bu ekseni hiç değerlendirmemişti):

| Panel | loading | empty | error |
|---|---|---|---|
| RED şeridi + seri kartları | `TableSkeleton` / spinner | boş grafik (`DatabaseDetail.tsx:287-289`) | React Query `isError` |
| Top statements | `TableSkeleton rows={5} cols={4}` | ayrı mesaj | ✅ ayrı dal — **v0.9.865'te eklendi**, öncesinde kart bomboş kalıyordu (`DatabaseDetail.tsx:338-341` yorumu) |
| Çağıranlar | skeleton | `<Empty>` | React Query |
| **Motor paneli (Oracle/Redis/Postgres/MySQL)** | `<Spinner/>` (`data === undefined`) | ⚠ **YOK — error'a katlanmış** | ⚠ **AYNI durum** |

⚠ **Motor panelleri hata ile boşu TEK duruma katlıyor.** `OraclePanel.tsx:28-30` ve `RedisPanel.tsx:22-24`:

```
.then(r => setData(r ?? null))   // veri YOK      → null
.catch(() => setData(null))      // sorgu PATLADI → null
```

`data === null` dalı tek cümle basıyor: `OraclePanel.tsx:72` *"Oracle metrics query failed."*, `RedisPanel.tsx:37` `<PanelErr />`. Yani **"bu instance için receiver metriği yok"** ekranda **"sorgu patladı"** diye görünüyor — ve tersi. Bu, §6.3'te endpoint çağıranları için ALINTILANAN dürüstlük sınıfının birebir aynısı (*"we could not see the caller"* → *"this route has no caller"*), yalnızca ters yönde. Ayrı bir F0 dilimi (F0.7).

### 1.4 Ölçülen üç kusur

**(a) `/databases` açılışta `trends`'i İKİ KEZ çağırıyor.** İki `DependenciesTable` mount'u (`:339`, `:381`), her biri `DependenciesTable.tsx:366-401`'deki `useEffect`'i koşuyor; istemci dedupe'u yok. Sunucuda `cache.go:263` `s.sf.Do(key, …)` singleflight'ı tek upstream'e katlıyor → **CH maliyeti ikiye katlanmıyor**, iki round-trip + iki serializasyon ödeniyor. `dbname` filtresi açıkken receiver paneli gizlendiği için (`:350`) tek istek olur.

**(b) `/database` TEK satır için TÜM FİLONUN trend payload'ını indiriyor.** `DatabaseDetail.tsx:98-105` *"The SAME key /databases uses, on purpose"*, `queryKey: ['db-trends', from, to]` (kimlik yok), `:106-112` client-side `.find()`. Takas bilinçli (sıcak cache mirası); bedeli `db_trends.go:130-133`'ün kendi kabul ettiği en kötü hâl (5000 DB × 288 kova). Derin linkle girildiğinde cache mirası da yok. **Payload boyutu ÖLÇÜLMEDİ.**

**(c) Ölü ama ödenen ham tarama.** `dependencies.go:413-429` her `/api/databases/detail` çağrısında `FROM spans … LIMIT 20 SETTINGS max_execution_time=15` koşuyor. `topOps`'u render eden **tek** bileşen `DetailDrawer.tsx:116` ve o dal §1.1'e göre erişilemez; `DatabaseDetail.tsx` `d.topOps`'a hiç dokunmuyor. **Yumuşatıcı:** tarama `service_name IN (…)` ile PK-prune ediliyor (`:405-423`, v0.7.35). **Maliyeti ÖLÇÜLMEDİ**; iddia maliyet değil, sonucun kullanılmaması — o üç adımda kodla doğrulandı. Emsal: v0.9.852'de wait/lock için tam bu temizlik yapıldı, TopOps'ta yapılmadı.

### 1.5 İki minör

**(1) İki ayrı istemci anahtarı, aynı uç.** `['db-top-statements', …]` vs `['database-statements', …]` — `/api/databases/slow-queries`e giden iki React Query anahtarı; sayfalar arası geçişte sıcak cache **miras alınmıyor**, ikisi de kendi ağını atıyor. Bu ikisinin arasında **daha değerli olan bulgu budur**.

**(2) `staleTime: 30_000` vs sunucu TTL'i 60s** (`api.go:2949`). ⚠ **v1 bunu "CLAUDE.md ihlali" diye yazmıştı — DÜZELTİLDİ.** CLAUDE.md'deki *staleTime ≥ server TTL* maddesi **ES-cost UI discipline** başlığı altında ve gerekçesi Elasticsearch sorgu maliyeti (v0.8.270). `/api/databases/slow-queries` bir **ClickHouse** okuması, zaten `s.serveCached` arkasında (`api.go:2949`) ve singleflight'lı (`cache.go:263`). Etkisi: sunucuda HIT dönen fazladan bir round-trip. Ne doğruluk ne CH maliyeti sorunu → **minör kalır, F-listesine girmez** (ES disiplininin CH tarafındaki zayıf ikizi).

---

## 2. Bir database örneği bugün nasıl tanımlanıyor?

### 2.1 Kanonik zincir (HAT A)

`identity.go:325-333` `dbInstanceExpr`, **altı basamak**, `'unknown'` terminali:
`peer_service → server.address → net.peer.name → db.host → db.name → service_name → 'unknown'`.
`dbNameExpr` (`:341`) tek basamak, `'default'` terminalli. Sıra bir **sözleşme**: `identity_test.go:249-257` + `quantile_ordinal_test.go:296-302` iki yönden pinliyor.

### 2.2 Envanter — kaç ayrı yerde

**Zincirin iki literal nüshası DDL'de yaşıyor ve Go sabitinden İTHAL EDİLMİYOR:** `store.go:3363-3371` (db_summary_5m), `store.go:3403-3411` (db_caller_summary_5m). `identity.go:30`'un kendi kuralı ("ikinci kez yazma, ithal et") MV DDL'ini kapsamıyor; bağ yalnız test düzeyinde (`quantile_ordinal_test.go:284-302` DDL metnini okuyor) — **derleyici ayrışmayı göremez.**

**HAT B'nin kendi içinde üç yazımı var:**

| Yer | Basamaklar |
|---|---|
| `db_capacity.go:53-57` `instanceExpr` | 3: `instance` attr → res `service.name` → `service_name` |
| `dependencies.go:1188-1194` `discoverReceiverInstances` | 4: `<prefix>instance.name` → `instance` → `server.address` → res `service.name` |
| `db_instance_scope.go:34-43` | motor başına 2-anahtarlı OR |

Somut ayrışma: `db_capacity.go`'nun rung listesinde `postgresql.instance.name` **yok** → res `service.name`'e düşer; oysa panel (`postgres.go:307-311`) ve `db_instance_scope.go:40` tam o anahtarı arıyor. Bir Postgres kapasite Problem'inin öznesi o örneğin panelini **açamaz**. Oracle'da üç yazım tesadüfen örtüşüyor (`oracle.go:442-446`) — lokalde yalnız `oracledb` receiver'ı olduğu için görünmüyor.

**Motor adının FRONTEND yazım-alias'ları** (v2'de eklendi — v1'de tamamen eksikti):

| Yer | Alias |
|---|---|
| `pages/DatabaseDetail.tsx:391` | `engine === 'postgresql' \|\| engine === 'postgres'` |
| `pages/DatabaseDetail.tsx:394` | `engine === 'mysql' \|\| engine === 'mariadb'` |
| `features/dependencies/DetailDrawer.tsx:393` | `system.toLowerCase() === 'postgresql' \|\| … === 'postgres'` |
| `features/dependencies/DetailDrawer.tsx:396` | `… === 'mysql' \|\| … === 'mariadb'` |

⚠ **`mariadb` backend haritasında HİÇ YOK.** F1.1 yalnız `identity.go` + `evaluator/db_capacity.go` + `problem_subject_test.go`'yu düzeltirse geriye frontend'de dört ayrı yazım-dalı ve backend'in **bilmediği** bir alias (`mariadb`) kalır. Alias kümesinin **TEK kaynağı** bir tarafta durmalı — öneri: `chstore/identity.go` (backend zaten `DBSubjectID`de `ToLower` yapıyor), frontend ondan türeyen tek bir `engineAlias` haritasını kullanır. Bu tam olarak dokümanın kendi alıntıladığı MEMORY dersi: *"Gate tek-yazım kör noktası"*.

### 2.3 `db:` önekini KİM kuruyor — üç kolon, tek liste değil

⚠ **v1'in "≥12 türetim noktası, 6 arite" sayımı ŞİŞİKTİ:** üç ayrı kategori tek listede toplanmıştı. Ayrıştırılmış hâli:

| ÜRETİCİ (`db:` adını KURAR) | ÖNEK OKUYUCU (kurmaz, soyar) | KİMLİK DEĞİL |
|---|---|---|
| `topology.go:304` (arite 1) | `mcptools/analysis.go:246` `HasPrefix(id,"db:")` — yorumu açık: *"önek tablosunu KOPYALAMIYORUZ"* (`:241-243`) | `breakdown.go:55` `concat('db:', db_system)` — bir **grafik KATEGORİ etiketi**, aynı `multiIf`te `'http'`/`'internal'` ile yan yana; hiçbir düğüm linkini beslemiyor |
| `topology.go:800` (arite 2, MV yazıcısı) | `api/servicegraph.go:310-320` `decodeNodeName` — yorumu: *"KIND `node_kind`'dan alınır, ASLA önekten"* | `db_summary_5m` ORDER BY üçlüsü — `db:` önekli **dizgi üretmiyor**, sadece üçlü anahtar |
| `topology.go:1547` (arite 1) | | `databaseParam.ts` `DatabaseRef` — aynı, üçlü anahtar |
| `identity.go:146` `DBSubjectID` (arite 2) | | |
| `service_map.go:460` `depName := "db:" + sp.dbSystem` (arite 1) | | |

**Gerçek boşluk, sayı küçülünce KESKİNLEŞİYOR:**

1. ⚠ `service_map.go:460` — **guard'ın DIŞINDA bir gerçek üretici.** `identity_test.go:270-283` `TestDbNodeNamingBoundToSharedIdentity` yalnız `os.ReadFile("topology.go")` okuyup `strings.Count(src, "concat('db:'") == 3` sayıyor. `service_map.go` Go string concat'i kullandığı için hem dosya hem sözdizimi olarak kapının dışında.
2. ⚠ `api/servicegraph.go:310-320` — öneki **elle çözüyor** (`HasPrefix` + `IndexByte('@')`), oysa `identity.go:84` `TopologyNodeIdentity(id)` tam bu işi yapan tek-nüsha fonksiyon.

**v1'in fazla-iddiası düzeltildi ama dersi ayakta:** `identity_test.go:279-282`'nin kendi hata metni sınıflandırmayı **zaten yapıyor** — *"MV yazıcısı mı (dbInstanceExpr ŞART) yoksa ad-hoc okuma yolu mu (**düz biçim KABUL**)"*. Yani düz `db:<system>` ad-hoc okuma yollarında **bilinçli kabul**; v1 bunu yönetilmemiş yayılım gibi sunuyordu. Kapının gerçek kusuru sayı değil, **dilim adına bağlı olması** (`topology.go` literal dosya adı) — MEMORY *"Muhafız dilime bağlanınca"*.

### 2.4 Kimliği TÜKETEN yerler — ve orada ÖLÇÜLMÜŞ bir kusur

v1 yalnız üreticileri saydı. Db özne kimliğinin **tüketicileri** de var ve birinde bugün bir bug yaşıyor:

| Tüketici | Ne yapıyor | Durum |
|---|---|---|
| Takım süzgeci (`problem_subject_lane.go:83-104` `problemServicesConjunct`) | `service IN (…) OR kind = 'db'` | ✅ **db kaçış kapısı VAR** (v0.9.1345, `ServicesAllowDBSubjects`) |
| Özne şeridi (`problem_subject_lane.go:44-64`) | `kind = ?` | ✅ iki-boot sözleşmeli |
| **Env süzgeci — SQL** (`env_members.go:147` `applyEnvServiceScope`) | `(service = '' OR service IN (…))` | ⛔ **db kaçış kapısı YOK** |
| **Env süzgeci — Go** (`api/problems_filter.go:83` `envKeepsRow` ← `api/inbox.go:690`) | `service == "" \|\| members[service]` | ⛔ **aynı kusur** |
| **Rozet sayımı** (`chstore/problem.go:1037` `CountProblemsNotInStatuses`) | `service = '' OR service IN ?` | ⛔ **aynı kusur** |
| Sayı çipleri (`problem_subject_lane.go:117+` `CountProblemsBySubject`) | aynı `envServices` şekli | ⛔ **aynı kusur (miras)** |

⚠ **ÖLÇÜLMÜŞ SONUÇ.** Bir db problem'inin `service` alanı bir **DBSubjectID**'dir (`db:oracle@corebank-scan.prod` — `problem.go:668-673` ve `:944-949` bunu birebir yazıyor) ve bu **asla** bir env üyesi servis adı olamaz. Dolayısıyla `?env=` seçiliyken:

- `/inbox` listesi (`inbox.go:690`) **TÜM db problemlerini eliyor**,
- sidebar rozeti (`CountProblemsNotInStatuses`) onları **saymıyor**,
- `/problems` SQL yolu (`envScopeProblems` → `applyEnvServiceScope`) aynı şekilde eliyor.

Ve `applyEnvServiceScope`'un kendi yorumu bu davranışı **bilinçli** diye yazıyor ama **servisler için**: *"a service ABSENT from the map (env-less infra, e.g. the demo's cross-env Oracle RAC) is hidden"*. Db ÖZNESİ o cümlenin kapsamında değil — o bir servis adı bile değil.

**Kanıt ki bu bir gözden kaçma:** aynı sınıf **takım** süzgecinde v0.9.1345'te fark edilip düzeltildi ve gerekçesi harfiyen yazıldı: *"bu bayrak olmadan ürün KENDİ KENDİYLE ÇELİŞİRDİ: satırın çipi 'core-banking' yazarken, owner=core-banking süzgeci o satırı gizlerdi — üstelik sessizce"* (`problem_subject_lane.go:70-81`). Env süzgeci aynı taksiti **almadı**.

⚠ **Lokalde görünmüyor** çünkü lokal imaj v0.9.1315 ve `db:` öznesi 0 satır (§9). **ÜRÜNDE DOĞRULANMADI.**

**Toplam (düzeltilmiş): 5 gerçek üretici, 2 önek okuyucu, 3 "kimlik değil"; 4 türetim zinciri (HAT A ×1 + HAT B ×3); 6 tüketici, 4'ü kırık.**

---

## 3. Bu kimlik URL'e konabilecek kadar kararlı mı?

**Bugünkü LOKAL veriyle ölçülemiyor — kategorik "hayır" DEĞİL.**

⚠ **v1 DÜZELTMESİ (yüksek şiddet).** v1 "**HAYIR** — dört ölçülmüş gerekçe" diyordu. Dört gerekçenin **üçü** (§3.1, §3.2, §3.4) kimlik zincirinin kusurunu değil, **lokal demo jeneratörünün elle yazdığı sabitleri** ölçüyor:

- `cmd/demo/main.go:552-553` yorumu birebir: *"peer.service stays \"oracle\" so the topology graph collapses every core-banking DB hop onto one node"*; `:564` `"peer.service", "oracle"` — **iki farklı** `server.address` için (`:545-546` `corebank-scan.prod:1521` / `corebank-dg.prod:1521`) **aynı** değeri basıyor.
- `cmd/demo/mesh.go:203-218` `pgDB`: `db.system = "postgresql"` **ama** `peer.service = "postgres"` — yorumu yine açık: *"peer.service stays \"postgres\" so the graph collapses every pg hop onto one node"*. `:221-234` `mongoDB`: `mongodb`/`mongodb`.

Yani §3.1'in *"aynı kimlik iki fiziksel veritabanı"*sı, §3.2'nin *"instance = motor adının ikinci nüshası"*sı ve orada gözlenen *`postgresql` vs `postgres` yazım ayrışması* tamamen jeneratörün **bilinçli tasarım tercihidir**. §3.4'ün *"`server.address` host:port taşıyor"*u da aynı sınıf (demo `server.address`e portu **elle** yapıştırıyor; OTel semconv `server.port`u ayrı tutar).

**Demo'dan BAĞIMSIZ duran ve ayakta kalan gerekçeler:**

| # | Neden ayakta | Doğrulama |
|---|---|---|
| **§3.3** | `clickhouse` db span'lerini **demo üretmiyor** — `grep '"clickhouse"' cmd/demo/` → **0 eşleşme**. O satırlar Coremetry'nin KENDİ self-telemetrisi; 6. basamağın canlıda tetiklendiği gerçek bir ölçüm | koddan doğrulandı |
| **§3.5** | HAT A / HAT B; iki AYRI pipeline'ın adları, demo tarafından değil **mimari** tarafından ayrışıyor | `identity.go:110-138`, `problem_subject_test.go:203` |
| **§3.6** | Tamamen **kod-kod** karşılaştırması (`db_capacity.go:181` `dbsys:"POSTGRES"` vs `db_ownership.go:224-229` `GROUP BY db_system`); tek bir span okumadan doğru | ayrı başlıkta, aşağıda |

**Kategorik karar bir PROD ölçümüne bağlanmalıdır:** `db_system` başına `uniq(peer_service)` vs `uniq(server.address)` — prod enstrümantasyonu da jenerik motor adı basıyorsa v1'in sonucu doğrulanır; basmıyorsa §3.1/§3.2/§3.4 **düşer** ve F3.1/F3.2/F3.3 + `/databases/:id` rotası yeniden değerlendirilir. Bu ölçüm §9'da açık kalem.

**3.1 Aynı kimlik iki fiziksel veritabanı — bugün, lokalde.** ⚠ *Kaynak: demo jeneratörü (`cmd/demo/main.go:552-564`), prod'da doğrulanmadı.* chc-0 (`spans`): `db_system='oracle'` için `peer_service='oracle'`, `server.address` ∈ {`corebank-dg.prod:1521`, `corebank-scan.prod:1521`}. `peer_service` 1. basamak olduğu için `db_summary_5m` 7 günde **tek** instance tutuyor: `oracle`. Üçlü kimlik de kurtarmıyor — `COREBANK` db_name'i **her iki host'ta da** var. Yani `/database?system=oracle&instance=oracle&name=COREBANK` iki makinenin toplamını gösteriyor ve **bunu söylemiyor**.

**3.2 `instance` 6 motorun 5'inde motor adının ikinci nüshası.** ⚠ *Kaynak: demo jeneratörü (`main.go:564`, `mesh.go:216`, `:234`) — jeneratör her motora tek bir `peer.service` sabiti basıyor, `uniq(instance)=1` bunun doğrudan sonucu. `clickhouse→coremetry-monolithic` satırı istisna (demo değil, self-telemetri).* `db_summary_5m` 7g: `oracle→oracle`, `redis→redis`, `mongodb→mongodb`, `elasticsearch→elasticsearch`, `postgresql→postgres`, `clickhouse→coremetry-monolithic`; her birinde `uniq(instance)=1`. Bugün URL'deki `?instance=` beş vakada **hiçbir şeyi daraltmıyor**. `postgresql→postgres` ayrıca `db_system` ile `instance`ın **yazımının** ayrıştığını gösteriyor.

**3.3 6. basamak canlıda tetikleniyor ve kimliği ÇAĞIRANIN adıyla dolduruyor.** ✅ *Demo'dan bağımsız: `grep '"clickhouse"' cmd/demo/` → 0 eşleşme; bu span'ler Coremetry'nin kendi CH istemcisinden geliyor.* 1s pencere: `clickhouse` için 2291 span'de ilk beş basamak boş, `service_name` kazanıyor → instance = **`coremetry-monolithic`**, Coremetry'nin kendi servis adı. İkinci bir servis aynı ClickHouse'a bağlansa **aynı veritabanı ikinci bir kimlik alırdı** — kimlik zamanla çoğalır. Buna karşılık `'unknown'` terminali **7 günde 0 satır** (iki MV'de de) — bugün ölü bir dal; `db_name`'in `'default'` sentineli ise 3 motorda satırların **%100'ünde** (clickhouse 2549/2549, redis 2546/2546, elasticsearch 2522/2522).

**3.4 Basamak kayması kimliği yeniden adlandırır ve PORT getirir.** ⚠ *Kaynak: demo jeneratörü — `main.go:545-546` / `mesh.go:213` `server.address`e portu ELLE yapıştırıyor; OTel semconv `server.port`u ayrı tutar, yani prod enstrümantasyonunda portsuz gelebilir.* `server.address` zaten `host:port` (oracle 7047/7047 span'de `:` var; `postgres:5432`; `mongodb:27017`); ayrı `server.port` attr'ı portsuz. `peer_service` boşalırsa kimlik `oracle@oracle` → `oracle@corebank-scan.prod:1521`: (a) eski bookmark ölür, (b) tek satır ikiye bölünür, (c) yeni kimlik HAT B'nin **portsuz** `corebank-scan.prod`'una yine eşit değil.

### ⚠ 3.5 MERKEZDEKİ ENGEL — HAT A / HAT B, bugün ÇÖZÜLMEMİŞ

`identity.go:110-138`'de ölçüm yazılı:

```
HAT B receiver instance'ları : corebank-scan.prod, corebank-dg.prod
HAT A db_caller_summary_5m   : instance='oracle' (peer_service kazandı)
İKİSİNİN KESİŞİMİ            : 0 satır
```

`host_name` da köprü değil — o **çağıranın pod'u** (`casemgr-prod-2-r5993`). `identity.go:133`: *"bu ID'yi HAT A tablolarına ad eşitliğiyle JOIN ETME."* Testle çivili: `problem_subject_test.go:203` `TestDBSubjectIDIsNotAHatAJoinKey`. Ve yalnız ölçüm değil, dayatılabilir bir cümle: `.claude/skills/clickhouse-schema/SKILL.md:305` — *"İki hat adlarını, birimlerini ve çözülmüş serilerini birbirine ödünç VEREMEZ."*

**Pratik sonuç:** `db_capacity.go` instance'ı **fiziksel** biliyor, `db_caller_summary_5m` `instance='oracle'` tutuyor → **sıfır satır join**. `/database` sayfası HAT B motor panelini ile HAT A golden-signal grafiklerini **aynı** instance kimliğiyle adresleyemiyor; bugün bunu `?source=spans|receiver` kapısıyla **ayırarak** yaşatıyor (`DatabaseDetail.tsx:388-399`), birleştirerek değil. **URL'e konabilir tek kimlik sorusunun merkezi burası.**

⚠ **VE İKİ KAYNAĞIN PENCERESİ DE FARKLI** (v2'de eklendi). `?source` kapısı yalnız *kimliği* ayırmıyor, **zaman ufkunu** da ayırıyor ve bu hiçbir yerde ilan edilmiyor: `spans` tarafı MV'lerden okuyor → **90 gün** (§7.1); `receiver` tarafı ham `metric_points`ten okuyor → **7 gün** (§7.1b). Aynı `?range=30d` ile operatör bir kapıda dolu, diğerinde boş grafik görüyor. → **F0.9**.

### ⚠ 3.6 YENİ BULGU — engelin ikizi: `postgres` vs `postgresql`

> ✅ **BU BULGU DEMO'DAN BAĞIMSIZDIR ve ayrı tutulmalıdır.** Yukarıdaki §3.1/§3.2/§3.4 lokal jeneratör sabitlerini ölçüyor; bu bulgu **iki Go dosyasının karşılaştırılmasından** geliyor ve tek bir span okunmadan doğrulanabilir. §3.1-§3.4'ün itibar kaybı buraya **sirayet etmez**.

`evaluator/db_capacity.go:181` `{id:"postgres-connections", …, dbsys:"POSTGRES", …}` → `identity.go:147` `ToLower` → özne `db:postgres@<inst>`. Karşı taraf `db_ownership.go:224-229` `GROUP BY db_system` ve ölçülen değer **`postgresql`**.

Zincir: `chstore/problem.go:700` `system, _, ok := ParseDBSubjectID(problems[i].Service)` → `:701` `len(dbCallers[system]) == 0 → continue`. `"postgres" ≠ "postgresql"` ⇒ **her zaman continue**. Aynı kusur `db_ownership.go:283-289` `DBOwnerForSubject`te ve `api/inbox.go:727`de.

Yani **v0.9.1345'in db-sahiplik zenginleştirmesi PostgreSQL kapasite problemlerinde sessizce hiç eşleşmez.** `MYSQL/REDIS/ORACLE` tesadüfen doğru; yalnız POSTGRES kırık. `TestDBSubjectIDIsNotAHatAJoinKey` **instance** yarısını çivilemiş, **system** yarısını hiç ölçmüyor. **ÜRÜNDE DOĞRULANMADI** — iki dosyayı okumaktan geliyor, lokalde postgresql receiver'ı yok.

**3.7 Ham düğüm ID'si zaten URL'e ve localStorage'a yazılıyor.** `ServiceMap.tsx:103` `p.set('focus', v)` + `:65` `searchParams.get('focus') ?? getRaw(STORAGE_KEYS.topoFocus)`; kaynağı `service_map.go:460`, yani **instance'sız** `db:<system>`. İki düğüm-ID uzayı aynı anda canlı: `/service-map` `db:oracle`, `/api/servicegraph` `db:oracle@oracle`. Çekmece kapısı bilinçli kapalı (`serviceMapNodeClick.ts:44-46`) ama `?focus=db:oracle` bookmark'ı yaşıyor.

### 3.8 Deterministik entity ID nasıl üretilmeli — ⚠ ÖNERİ DEĞİL, **HİPOTEZ**

⚠ **v1 DÜZELTMESİ (yüksek şiddet).** v1 bunu bir *öneri* olarak yazdı, ama **hiç ölçmedi** ve **dokümanın kendi ölçümü onu yalanlıyor**:

> §3.3'te ölçülen: `clickhouse` için **2291 span'de İLK BEŞ BASAMAK BOŞ**, kazanan `service_name`.
> Aşağıdaki `host` zinciri 2./3./4. basamakları kullanıyor → **o motorda %100 çözülemez.**

Yani "çözülemeyen dal" nadir bir kenar değil, **ölçülmüş olarak bir motorun tamamı**. Zincir hâlâ *kırılmıyor* (çözülemeyince boş ID döner ve çağıran bugünkü ham değeri yazar — aşağıdaki kural), ama o motorda entity ID'nin **kapsamı sıfırdır**. `redis`/`elasticsearch`te `peer_service` düşünce geriye ne kaldığına **hiç bakılmadı** (bugünkü instance değerleri 1. basamaktan geliyor).

**F3.1'in ÖN KOŞULU — tek bir CH ölçümü** (bu tablo doldurulmadan F3.1 başlamaz):

```sql
-- 7g spans, motor başına yeni zincirin çözünürlük oranı
SELECT db_system,
       count()                                                                    AS spans,
       countIf(attr_values[indexOf(attr_keys,'server.address')] != '') / count()  AS r_server_address,
       countIf(attr_values[indexOf(attr_keys,'net.peer.name')]  != '') / count()  AS r_net_peer_name,
       countIf(attr_values[indexOf(attr_keys,'db.host')]        != '') / count()  AS r_db_host,
       countIf(attr_values[indexOf(attr_keys,'server.address')] != ''
            OR attr_values[indexOf(attr_keys,'net.peer.name')]  != ''
            OR attr_values[indexOf(attr_keys,'db.host')]        != '') / count()  AS r_any
FROM spans
WHERE db_system != '' AND time >= now() - INTERVAL 7 DAY
GROUP BY db_system ORDER BY spans DESC
SETTINGS max_execution_time = 30
```

`r_any` = **ölçülen çözünürlük**. Aynı sorgu **prod'da da** koşmalıdır (§3'ün prod ölçümüyle tek turda alınabilir).

| Alan | Türetim | Gerekçe | **Ölçülen çözünürlük** |
|---|---|---|---|
| `system` | `db.system`, **alias haritasıyla** küçük harfe (`postgres→postgresql`, `ORACLE→oracle`, `mariadb→mysql`) | §3.6'nın ilacı | `db_system != ''` zaten filtre → **%100** (tanım gereği) |
| `host` | `server.address`ın host yarısı → `net.peer.name` → `db.host`; SON `:`ten bölünür ve **yalnız kalan kısım tamamen rakamsa**; küçük harf; **FQDN kısaltılmaz** | ayırt edici bilgi `.prod` son ekinde ve `scan`/`dg` önekinde (§3.1) | ⛔ **ÖLÇÜLMEDİ** — bilinen tek nokta: `clickhouse` = **%0** (§3.3) |
| `port` | **AYRI alan**, host'a yapıştırılmaz | HAT A adres içinde port taşıyor, HAT B taşımıyor (§3.4) | ⛔ **ÖLÇÜLMEDİ** |

Emsal `db_stmt_hash` — ama ders **hash değil, parite sözleşmesi**: `dbstmt.go:23-44` *"THE PARITY CONTRACT (load-bearing)"*, aynı normalize iki motorda bayt bayt aynı, canlı CH'den yakalanmış vektörlerle pinli.

- **`peer_service` adres türetimine GİRMEZ** — lokalde ölçüldü ki değeri motorun kendi adı (⚠ ama o değeri **demo yazıyor**, §3.1). Yukarıdaki ölçüm `r_any`'yi düşük gösterirse bu madde **düşer**: `peer_service`i atmak, kapsamı kazandırdığından fazla kaybettiren bir tercih olur.
- **`db_name` entity'nin PARÇASI DEĞİL, ÇOCUĞUDUR** — 3 motorda %100 sentinel.
- **Çözülemediğinde boş ID dön**, çağıran bugünkü ham değeri bayt-bayt yazsın. `DBSubjectID`'nin bugünkü sözleşmesi bu (`identity.go:144-145`) ve doğru olan o. Kısmi demeti hash'leyip sahte-kararlı ID üretmek **en kötü** seçenek.
- **Hash EKLENMEZ** — `db_stmt_hash` sınırsız SQL yüzünden hash'liyor; `host:port` kısa ve operatörün URL'de **okuması** gereken bir şey.

⚠ **Kabul ölçütü:** ölçüm gelmeden bu bölüm bir **hipotezdir**; F3.1'in çıktısı "fonksiyonu yaz" değil, **"ölçüm + fonksiyon + karar"** üçlüsüdür.

### 3.9 Geçişin bedeli — ücretsiz değil

1. basamağı demote etmek **mevcut her db satırını yeniden adlandırır**: bugün tek olan `oracle@oracle`, ölçülen 8 farklı `(addr, db_name)` kombinasyonuna göre ikiye bölünür. Sonuçlar: (a) 90 günlük MV TTL'i boyunca aynı grafikte iki kimlik; (b) `topology_edges_5m`'de 14 gün iki düğüm — `nodeDetailHref` ikisine de link kurar, biri boş açar; (c) `DBSubjectID` taşıyan açık problemlerin `service` alanı bayat kalır (`db_capacity.go:448-450` bu sınıfı biliyor). Emsal ölçülü: v0.9.1326 — *"14 GÜNLÜK ÇİFT DÜĞÜM BEKLENİYOR, BUG DEĞİL … Birleştirme BİLEREK yapılmadı."*

**(d) ⚠ KAYDEDİLMİŞ GÖRÜNÜMLER sessizce boşa düşer** (v2'de eklendi). `/databases` sayfasında `SavedViewsBar page="databases"` var (`Databases.tsx:278`) ve bileşen **`window.location.search`i olduğu gibi sunucuya yazıyor** (`SavedViewsBar.tsx:139` `window.location.search.replace(/^\?/, '')` → `api.createSavedView({page, queryString})`), geri uygulaması `:129-133` `navigate(pathname + '?' + v.queryString)`. Yani `?dbsys=` / `?dbname=` değerleri **operatör içeriği olarak ClickHouse'ta duruyor** (`saved_views` tablosu). Bir yeniden adlandırma bunları bookmark gibi değil, **üründe listelenen birinci sınıf bir yüzey** olarak kırar: kayıtlı görünüm listede görünmeye devam eder, tıklanınca boş sayfa açar.

**Öneri:** yeni entity ID mevcut `instance` kolonunun **yerine değil, yanına** konur ve önce ölçülür.

⚠ **Ama "yanına" tek bir maliyet sınıfı DEĞİL — hangi tabloya konduğuna göre ikiye ayrılır:**

| Nereye | Maliyet sınıfı | Ne gerekir |
|---|---|---|
| `spans` **kolonu** | Orta | dağıtık-kolon-güvenliği (hasXCol probe + koşullu INSERT + ALTER atlama), iki-boot sözleşmesi (MEMORY *"Distributed column safety"*) |
| MV **boyutu** (ör. `db_summary_5m`) | ⛔ **G6 sınıfı** | Yeni bir GROUP BY boyutu **ORDER BY'a girmek zorunda** (§7.3'ün gerekçesi) → MV yeniden kurulumu → 90g geçmiş feda → **reddedilmiş yol** |

**"Kaydedilmiş görünümler ne olacak" — üç seçenek, biri seçilmeli:** (a) **dokunma** (eski görünüm eski kimliği taşır, açılınca boş gelir — bugünkü sessiz kırılma), (b) **göç ettir** (`saved_views` satırlarını okuyup query string'i yeniden yaz — CH'de operatör içeriğine YAZMA, ayrı bir risk sınıfı), (c) **uyar** (görünüm uygulanınca sonuç boşsa "bu görünüm eski bir kimlik biçimi kullanıyor" rozeti). **Öneri: (c)** — yazma yok, dürüst, ve §3.10 kırmızı çizgi 3'ün (*"çözülemeyen dal bayt-bayt aynı"*) ruhuna uyuyor.

### 3.10 Uyulması zorunlu kırmızı çizgiler

`entity-model-audit-2026-08-23.md:473-477` + `cross-page-context-2026-08-23.md:534-541`:

1. **Eklemeli** — `entity_id` **hiçbir** ORDER BY'a, shard anahtarına, önbellek anahtarına girmez (v0.5.187 çapraz-zehirlenme sınıfı).
2. **URL'ler doğal anahtarda kalır** — `/service?name=` aynen; kırılırsa bookmark/dashboard/incident linkleri **ve `saved_views` (page='databases') kayıtlı query string'leri** ölür. Sonuncusu bir bookmark değil, üründe listelenen birinci sınıf bir yüzeydir (§3.9d).
3. **Çözülemeyen dal bugünküyle bayt-bayt aynı** ve testle çivili. Emsaller: `nodeDetailHref.ts:108-114` null; `databaseParam.ts:75-76` `'default'`u filtreye yazmaz; v0.9.1326 instance yoksa `/databases?dbsys=` **kataloğuna** düşer; v0.9.1339 db öznesinde **link çizmez** (*"çalışmayan bir bağlantı, bağlantı olmamasından kötüdür"*).
4. **Yaşam döngüsü MV'den** okunur, tabloda tutulmaz.
5. **B1 (deploy_env MV boyutu) DÜŞTÜ** — 2026-08-24 operatör kararı (`cross-page-context:464-472`).

---

## 4. `/databases/:id` route'u için sayfa iskeleti

Sayfa **zaten var**: rotası `/database` (tekil), `App.tsx:148`; URL `/database?system=&instance=[&name=][&source=receiver][&range=][&env=]`. Path-param'a (`/databases/:id`) geçmek §3'e göre **bugün önerilmez** — kararlı tek-dizgi kimlik yok ve kırmızı çizgi 2 URL'i doğal anahtarda tutmayı dayatıyor.

Onaylı mockup sırası v0.9.840'ta yazılı ve bugün aynen yaşıyor:

| # | Bölüm | Durum | Kaynak |
|---|---|---|---|
| 1 | Kimlik başlığı + KAPSAM satırı | ✅ | `DatabaseDetail.tsx:230-238` |
| 2 | RED skor şeridi (8 karo) + Toplam süre | ✅ | `:254-267` |
| 3 | Üç seri kartı (Calls/s · Error % · P99) | ✅ ama §1.4(b) | `:416` |
| 4 | "Who calls this" | ✅ MV'den | `:515` + `dependencies.go:348-365` |
| 5 | Bu DB'ye daraltılmış "Top statements" | ⚠ instance'ı düşürüyor (G6) | `:114-124`, `:331-333` |
| 6 | Motor paneli (yalnız `?source=receiver`) | ⚠ var ama **ham `metric_points`**, pencere **7g** (ilan edilmiyor, §7.1b); hata/boş durumu **katlanmış** (§1.3) | `:388-399` |
| 7 | İfade çekmecesi (`?stmt=`) | ✅ paylaşılan | `:403-410` |
| 8 | **Trace'lere geçiş** | ✅ **VAR** — v1 YANLIŞTI | `DatabaseDetail.tsx:25` import + `:214-218` `<Link className="sec" to={dbTracesHref({window, system, instance, dbName})}>Traces →</Link>`; üretici `lib/pivotHref.ts:199-247` |
| 9 | **Loglara geçiş** | ❌ **YOK** (doğru) | `grep -c logsHref DatabaseDetail.tsx` = 0 **ve** dosyada `/logs` geçen satır yok — iki yazımdan da doğrulandı |

⚠ **v1 satır 8 hatası (yüksek şiddet, düzeltildi).** v1 `grep -c traceHref DatabaseDetail.tsx` → 0 görüp "YOK" sonucuna vardı. Depodaki db trace üreticisinin adı **`dbTracesHref`** (büyük T); `traceHref` ise **tek-trace-by-id** üreticisi (`lib/traceHref.ts`), bambaşka bir şey. `grep -c TracesHref` → **2**. F4.2'nin "M efor"u **zaten var olan bir düğmeyi yeniden yazmaya** harcanacaktı → **F4.2 plandan TAMAMEN ÇIKARILDI**. Yöntem kuralı dokümanın başına eklendi.

**Golden signal grafikleri bugün %100 MV'den** (`db_trends.go:137-150`); ek bir şey gerekmiyor. **p95 serisi: CH tarafında sıfır ek hesap, payload'da bir alan.** `quantilesTDigestMerge(0.5,0.95,0.99)` **üçünü birden** hesaplıyor ama yalnız index 3 okunuyor (`db_trends.go:144`) — index 2 gerçekten bedava. ⚠ *Ama uçtan uca bedava DEĞİL:* `DBTrendPoint` (Go `db_trends.go:16`; TS **HEAD'de şu an** `lib/types.ts:319-324`) yalnız `t/rps/errorRate/p99Ms` taşıyor. Yeni alan = Go struct + JSON + `types.ts` + 4. kart, **ve** §1.4(b)'de şikâyet edilen filo-geneli payload'ı büyütür (`LIMIT 200000`, `db_trends.go:149`). → **F1.3, F0.5'ten SONRA** yapılmalı ki yeni alan filo payload'ına değil daraltılmış uca binsin.

**"En yavaş" var, "en sık" YOK — ve aynı kusur AYNI SAYFADA İKİ KEZ var.**

| Uç | Sıralama | Limit | Kapsam |
|---|---|---|---|
| **span yolu** — `/api/databases/slow-queries` | **sabit** `ORDER BY total_ms DESC` (MV `dbqueries.go:185-186`; ham `:137`) | `/database` `limit=10` (`DatabaseDetail.tsx:120`) | enstrümante edilmiş çağrılar |
| **receiver yolu** — Postgres/MySQL TopSQL | **sabit** `ORDER BY total_ms DESC LIMIT 10` (`db_topsql.go:108`, `:188`) | sabit 10, uçta parametre yok | motorun gördüğü **TÜM** istemciler |

İstemci `useDataTable`/`Execs` kolonuyla sıralayabiliyor ama **yalnız o 10 satırın içinde** (span tarafı `DataTable`; receiver tarafı `panels/shared.tsx:24` `Execs` kolonu + `:353` render). ⚠ v1 bunu tek uç için yazmıştı; **F1.2 tek başına gemiye giderse yan yana iki "en sık" listesi kalır, biri sıralanabilir diğeri değil.**

⚠ **v1 AŞIRI GENELLEMESİ, düzeltildi.** v1 *"'çok koşan ama hızlı' bir ifade, yani en sık tanımının kendisi, **ekrana hiç ulaşamıyor**"* diyordu. Doğrusu: **`/database` panelinde** görünmez (o 10 satırın dışındaysa), **ama sayfa bir tık ötede kataloğa link veriyor** (`DatabaseDetail.tsx:219-223` → `/databases/slow-queries?dbsys=…`) ve o sayfa **`limit: 200`** çekip (`SlowQueries.tsx:100`) Calls'a göre istemci-sıralanabiliyor. Dürüst hâli: *"o 10 satırın dışında kalan yüksek-frekans/düşük-süre ifadesi bu panelde görünmez; katalog sayfası 200 satıra kadar görüyor — ama sunucu sıralaması **orada da** `total_ms`."* **F1.2'nin gerekçesi zayıflamıyor, yalnız dürüstleşiyor.**

⚠ **DÜRÜSTLÜK YÜKÜMLÜLÜĞÜ (ürün metnine yazılacak):** iki liste **meşru olarak farklı sayı verir** ve bunu bugün hiçbir yerde söylemiyoruz. `db_topsql.go:23-27` gerekçeyi zaten yazmış: *"Unlike the span-derived 'Top statements' list (which only sees what the application actually traced), this is what the DB itself measured across ALL clients."* Sayı farkı bir bug değil, **kapsam farkı** — ve söylenmezse operatör onu bug sanar.

Düzeltme **MV cerrahisi istemez**: `span_count_state` zaten orada (`store.go:3462`), uca `order=calls|total|p99` + **cache anahtarına yeni parametre** yeter.

**Pivotlar — ⚠ v1'in bu paragrafı YANLIŞTI ve TERSİNE ÇEVRİLDİ (yüksek şiddet).**

v1: *"`/traces` filtresi tek attr'a bakar; 6. basamaktan adlandırılmış instance için link boş liste getirir; v0.9.274'ün backend'de düzelttiği kusurun frontend ikizi. Hangi basamağın kazandığı bilinmeden yazılmamalı."*

**Gerçek: tam tersi.** `frontend/src/lib/pivotHref.ts:199-247` `dbTracesHref` **altı basamağın hepsini tek OR grubuna** koyuyor (`:208-215`): `peer.service`, `server.address`, `net.peer.name`, `db.host`, `db.name`, `service.name`. Dosyanın kendi yorumu (`:182-198`) denetimin §3.3'te kendi ölçtüğü vakayı gerekçe gösteriyor:

> *"The old link filtered `peer.service` alone, so any row whose name came from a later rung landed on an empty trace list … Proven on live data: the clickhouse row carried 2201 spans in a 30-minute window and its link matched 0, because that instance is named from `service_name`."*

Yani "yazılmamış frontend ikizi" denen şey **v0.9.268'de, backend ikizinden (v0.9.274) ÖNCE** gemiye gitti. Üstelik sentineller de özel kapıda: `'default'` db_name'i filtreye yazılmıyor (`:220-224`), `'unknown'` instance'ı OR grubunu hiç kurmuyor (`:226-230`), ve flat-AND tuzağı (`encodeFilterGroup` boş grup için `''` döner → `db.system` de düşer → **filtresiz** trace listesi) `:231-246`'da açıkça çözülmüş. Pencere (`window`) **zorunlu argüman**.

→ **"Hangi basamağın kazandığı bilinmeden yazılmamalı" cümlesi DÜŞER** — üretici tam olarak o soruyu sormamak için OR grubu kuruyor. Bu, F4.1'in (log pivotu) **şablonudur**, engeli değil.

**Loglar** `logstore.Store`'a gider, `spans`a hiç dokunmaz → rollup boşluğu **değil**, yazılmamış bir link (üretici bugün geldi: v0.9.1347 `logsHref`). F4.1 geçerli.

---

## 5. Drawer ile tam sayfa AYNI bileşenden — yapı önerisi

### 5.1 Bugün: kopya

`DatabaseDetail.tsx` ve `DetailDrawer.tsx` **aynı payload'ı** (`DBDetail`, `/api/databases/detail`) iki yerde çiziyor (`:93` vs `:92`). Yardımcı fonksiyon bile bilinçli kopyalanmış: `DatabaseDetail.tsx:436-441` *"msOrDash — v0.9.263, kept verbatim"* ≡ `DetailDrawer.tsx:36-42`.

| Kopya | Nüsha | Yerler |
|---|---|---|
| Altın-sinyal karosu (`Stat`) | **4** (bu denetimin KAPSAMI) | `EndpointDetail.tsx:337`, `DatabaseDetail.tsx:466`, `StmtDetailDrawer.tsx:169` (üçü **bayt-bayt DEĞİL**, aşağıya bak) + `panels/shared.tsx:53` (kanonik, farklı görsel) |
| `PanelTitle` | 2 — **zaten ayrışmış** (sayfa kopyası `right` slot'unu kaybetmiş) | `endpoints/detailSections.tsx:40`, `DatabaseDetail.tsx:452` |
| `SectionUnavailable` | 2 (bayt-bayt) | `detailSections.tsx:57`, `StmtDetailDrawer.tsx:161` |
| "Who calls this" | **4** | `DatabaseDetail.tsx:486-493`, `DetailDrawer.tsx:588-604`, `detailSections.tsx:567-573`, `StmtDetailDrawer.tsx:290-296` |
| Kök-neden gövdesi | 2 dosya, 496+344 satır, **sıfır ortak gövde** | `RootCauseRibbon.tsx`, `RootCausePanel.tsx` |

Birleştirme **niyeti** yazılı (`detailSections.tsx:576-578`) ama kolonlar gerçekte aynı değil ve `Impact` iki ayrı anlamda (`DatabaseDetail.tsx:506` istemci hesabı vs sunucudan gelen `sharePct`).

⚠ **KAPSAM NOTU (v2).** Yukarıdaki "4" bu denetimin **kapsamıdır** (altın-sinyal karosu ailesi). Repo genelinde yerel `Stat`/`HeaderStat` tanımı **8** — HEAD'de şu an: `features/dependencies/panels/shared.tsx:53` (kanonik), `pages/DatabaseDetail.tsx:466`, `pages/EndpointDetail.tsx:337`, `pages/slowqueries/StmtDetailDrawer.tsx:169`, `components/DBQueriesPanel.tsx:335`, `components/topology/FocusedNeighborhood.tsx:502`, `pages/Pod.tsx:46`, `pages/Traces.tsx:154` (+ ayrıca `shared.tsx:122` `GaugeStat`). MEMORY ve `/frontend-design-system` "Stat ×6" diyor; gerçek sayı daha yüksek. **F2.1'in kapsamı açıkça üç dosyayla sınırlıdır ve "tek eve" ifadesi kullanılmaz** — tam envanter `/frontend-design-system`de.

⚠ **ÜÇÜ BAYT-BAYT DEĞİL — v1 DÜZELTMESİ.** HEAD'de şu an:

| Yer | İmza | Kap | Etiket stili |
|---|---|---|---|
| `pages/DatabaseDetail.tsx:466` | `Stat({label, **value**, tone})` | `minWidth: 0` ✅ | `textTransform: uppercase` + `letterSpacing: 0.4` ✅ |
| `pages/EndpointDetail.tsx:337` | `Stat({label, tone, **children**})` | `minWidth: 0` ✅ | aynı ✅ |
| `pages/slowqueries/StmtDetailDrawer.tsx:169` | `HeaderStat({label, tone, **children**})` | `minWidth: 0` ⛔ **YOK** | `textTransform`/`letterSpacing` ⛔ **YOK** |

Yani birleştirme (a) **`StmtDetailDrawer`'ın görünümünü DEĞİŞTİRİR** (etiket büyük harfe geçer, harf aralığı açılır) ve (b) bir **prop yüzeyi kararı** ister (`value` mü `children` mı). v1 §5.1'de "(üçü ~bayt-bayt)" diye tilde'lı yazıp F2.1'de tilde'sız *"bayt-bayt aynı çıktı"* diye sertleştirmişti — **kendi içinde çelişki**. Buna karşılık:

- `SectionUnavailable` **gerçekten bayt-bayt** (`detailSections.tsx:57` ≡ `StmtDetailDrawer.tsx:161`) → mockupsuz merge.
- `PanelTitle` farkı **yalnız `right` slotu** → `right`lı sürüme terfi, çıktı korunur.

### 5.2 Çalışan emsaller

1. **`StmtDetailDrawer`** — bir çekmece, **üç** sayfa (`SlowQueries.tsx:410`, `Databases.tsx:454`, `DatabaseDetail.tsx:404`); kabuk + `?stmtcmp=` bileşenin içinde, ebeveyn `?stmt=` sahibi. **Prop yüzeyi şablonu budur.**
2. **`RootCauseRibbon`** — iki çekmece + iki sayfa, ayrım `anchor: 'problem'|'anomaly'`; güvenli çünkü **iki dalın payload'ı aynı şekle sahip** (`:25-27`).
3. **`SubjectLink`** (v0.9.1339) — altı yüzey. **API dersi:** *"href'i BU bileşen kurmuyor … çağıran veriyor"* (`:16-21`) → **karar paylaşılır, bağlam paylaşılmaz.**
4. **`ProblemDetail.tsx`** — sorulana en yakın: iki **ayrı varlık türü** tek iskeletten (`:37-43`), ikincisi eski TriageDrawer'ın yerini almış. Ama paylaşım **kapsayıcı değil**: iki export kendi return ağacını yazıyor, `kind` union'ı **yok**; paylaşılan şey dosya-yerel atomlar (`Sect:71`, `SignalLink:171`, `DeployBox:188`) + `.pb-*` CSS ailesi.

### 5.3 Öneri: kapsayıcı DEĞİL, söz dağarcığı

Deponun meşru-çıkarma ölçütü: `ui/PageShell.tsx:1-8` *"bu atom bir soyutlama DEĞİL … çıktısı bit bit aynı"* + `:35-38` *"İhtiyaç kanıtlanmadan API büyütülmüyor."*

- **Ölçütü BUGÜN geçenler:** `SectionUnavailable` ikizi (bayt-bayt), `PanelTitle` ikizi (`right` slotlu sürüme terfi).
- **Ölçütü geçEMEyen ama KARARLA geçebilecek:** `Stat`/`HeaderStat` üçlüsü — çıktı bayt-bayt **değil** (yukarı), prop yüzeyi + `StmtDetailDrawer` etiket stili kararı ister, **mockup şartı**.
- **Geçemeyen:** tek `<EntityDetail kind=…>` kapsayıcısı — üç dalın çıktısı bayt-bayt aynı **değil**.

```
pages/databases/detailSections.tsx     ← YENİ (endpoints ikizinin kardeşi)
    DatabaseIdentityHeader / DatabaseSignalStrip / DatabaseTrendCards
    DatabaseCallersSection / DatabaseStatementsSection
pages/DatabaseDetail.tsx               ← bölümleri dizen SAYFA kabuğu
features/dependencies/DetailDrawer.tsx ← db dalı SÖKÜLÜR
```

Sözleşme **bölüm düzeyinde** kurulur, kapsayıcı düzeyinde değil — `endpoints/detailSections.tsx:24-31` tam bu deseni tarif ediyor: *"PROMOTED, not copied … The drawer SHELL is gone."*

⚠ **v1 bu ağacı bir dosya/isim listesinden ibaret bırakmıştı.** Sözleşmenin eksik üç yarısı (v2'de eklendi):

**(1) Her bölümün prop imzası — şablon `StmtDetailDrawer` (HEAD'de şu an `:39-47`):**

```ts
export function StmtDetailDrawer({ refObj, row, range, onClose }: {
  refObj: StmtRef;                    // KİMLİK — kodlayıcı modülünden (databaseParam.ts ikizi)
  row: SlowQueryRow | undefined;      // payload/opsiyonel — derin linkte undefined, bölümler yine yüklenir
  range: TimeRange;                   // pencere HER ZAMAN açık argüman, asla context'ten
  onClose: () => void;
})
```

Db bölümleri için karşılığı: `({ refObj: DatabaseRef, d: DBDetail | undefined, range: TimeRange })`. **Veri çekimi SAYFADA durur, bölümde değil** — bölüm saf çizer (bugünkü `DatabaseDetail.tsx` zaten böyle: `:98-112` sayfada fetch, `:416`/`:515` bölümlerde çizim). Bu, §1.4(b)'nin çözümüyle de uyumlu: scoped uç eklenince değişen tek yer sayfa olur.

**(2) URL sahipliği kuralı:** **ebeveyn AÇILIŞ parametresini sahiplenir, bölüm KENDİ ALT-parametresini.** Emsal aynı dosyada: `StmtDetailDrawer` `?stmtcmp=`i kendi içinde okuyup yazıyor (`:48-56`, `replace:true`, yabancı parametreler korunur), ebeveyn `?stmt=`i sahipleniyor. Db tarafında: sayfa `system/instance/name/source`, bölüm varsa kendi `?dbcmp=` gibi bir alt-eksen. ⚠ Yeni eksen yazan her bölüm `lib/chatContext.ts:38-50` kapısına da uğrar (§8 notu).

**(3) "Çekmece VE tam sayfa AYNI ANDA" — operatörün SORDUĞU hâl, v1 cevaplamamıştı.** Db'de soru eriyor çünkü çekmece söküldü (§1.1). **Ama `/messaging` bugün tam olarak o hâlde** (`Messaging.tsx:214-218`, `kind="queue"`) ve F4.4 onu tam sayfaya terfi ettiriyor. Kural:

> Bir varlık aynı anda hem çekmece hem sayfa olarak yaşayacaksa, **paylaşılan şey `detailSections.tsx` (bölümler), paylaşılmayan şey kabuktur**. Çekmece kabuğu `onClose` + odak tuzağı + `?open=` parametresini; sayfa kabuğu başlık + breadcrumb + `PageShell`i yazar. **İki kabuk asla tek bileşende `mode` prop'uyla birleştirilmez** — §6.1'in dört ısırığı tam olarak o birleştirmeden geldi.

**F4.4'ün KABUL ÖLÇÜTÜ budur:** `/messaging` tam sayfaya terfi ederken çekmece **kalırsa**, ikisi de aynı `messaging/detailSections.tsx`i tüketmelidir; kalmayacaksa (db'deki gibi emeklilik) `DetailDrawer`ın `kind='queue'` dalı da sökülür ve §6.1'in union'ı **tamamen** biter.

**`DetailDrawer`'ın db dalı sökülmeli** (`:248-258` kapsam satırı, `:390-401` receiver panelleri, `:428-434` db CallerSection) — bu, §1.4(c) ile **aynı temizliğin iki yarısı**. ⚠ v0.9.846 dersi: aynı bileşen bir kez *"DetailDrawer hâlâ mount ediyor"* gerekçesiyle yaşatılmıştı, oysa dal erişilemezdi — **union ölü-kod muhakemesini yanlışladı**. Söküm öncesi `Messaging.tsx`in gerçekten tek `kind='queue'` tüketici olduğu **tekrar** doğrulanmalı.

---

## 6. Aynı desen Endpoints ve Messaging'de tekrar kullanılabilir mi?

### 6.1 db+queue birleşimi ZATEN var — ve dört kez ısırdı

`DetailDrawer.tsx:51-81` + `DependenciesTable.tsx:101` `kind: 'db'|'queue'`; tipler de ortak (`types.ts:358` `DBCallerBreakdown`, `:383` `DBOpStat`, `:403` `DBDetail`, `:424` `MessagingDetail`).

| Sürüm | Kusur |
|---|---|
| v0.9.256 | Trace pivotu `kind === 'db'` ile kapalıydı → messaging'in en doğal pivotu **tek satır** yüzünden yoktu (`DetailDrawer.tsx:492-500`) |
| v0.9.873 | Paylaşılan `storageKey` kolon genişliklerini kipler arası sızdırdı → `deps-topops-${kind}` (`:117-128`) |
| v0.9.821 | Paylaşılan tablonun `nameOf()`'u **etiketi kimlik sandı** → sessizce boş çekmece |
| v0.9.846 | Union yüzünden **ölü-kod muhakemesi yanlış çıktı** |

Ayrıca tüketicisiz doğan soyutlamalar silinmiş: `ui/index.ts:50-56` *"Tabs — REMOVED v0.9.904 … never gained a single consumer."*

### 6.2 Endpoint bu union'ın DIŞINDA

`types.ts:146` `GraphNodeKind = 'service'|'database'|'queue'|'external'|'internal'` — **`endpoint` yok**; `identity.go:70-74` `TopologyNodeIDPrefixes = {db:, queue:, ext:}`. Topoloji ayağı **mevcut değil**; reuse ancak tablo/detay tarafında olur. Ek asimetri: `nodeDetailHref.ts:164-166` database → **sayfa**, `:199-202` queue → **çekmece derin linki**; link etiketleri bile bu farkı söylemek zorunda kalmış (`nodeLinkLabel.ts:37-44`).

### 6.3 Ortak olan / dirençli olan

**Ortak (üçünde de):** kimlik kodlayıcı modülü (`databaseParam.ts` / `endpointParam.ts` / `destinationParam.ts` / `stmtParam.ts` — dört izomorf), altın-sinyal şeridi, çağıranlar tablosu.

**Dirençli — hepsi *dürüstlük* sınıfından**, kapsayıcıya katlanırsa sessizce kaybolur:

- `databaseParam.ts:17-19` — `dbName === ''` **meşru** hâl (*"A real, statable state"*); endpoint'te karşılığı yok.
- `endpointParam.ts:39-49` — `cluster`, `compare`, `entry:'rpc'`; `DatabasePageScope` (`:24-27`) yalnız range+env.
- `types.ts:428-433` — `MessagingDetail.assumedCluster` (*"sunucu '(default)' VARSAYDI"*).
- `types.ts:3661-3681` — endpoint çağıranları **örneklemedir**; `detailSections.tsx:582-586` katlamayı açıkça reddediyor: *"folding them would turn 'we could not see the caller' into 'this route has no caller'."*
- queue'da producer/consumer rolü + `span_links` e2e (`DetailDrawer.tsx:158-186`, `:339-379`); db'de `source: 'spans'|'receiver'` kapısı — karşılıkları yok.

### 6.4 En ucuz ilk taksit: ölü CSS primitifi

`styles/globals.css:3175-3182` — `.pb-strip`/`.pb-tile` (tone varyantlı, temalı, token'lı altın-sinyal şeridi) **yazılmış**, ama TSX'te `pb-` geçen tek dosya `ProblemDetail.tsx` ve o da bu ikisini kullanmıyor. Üç sayfa yerine `padding: '8px 10px'` satır-içi bloklarını elde yazmış. `/frontend-design-system`in *"barrel YETMEZ, iki envantere birden bak"* uyarısının canlı örneği. **Yeni soyutlama değil — var olan ölü primitifi bağlamak.**

### 6.4b Backend ayağı — **MV ailesi ZATEN izomorf** (v2'de eklendi)

⚠ v1 genellemeyi yalnız **frontend**'de ölçtü (tipler, bileşenler, dört ısırık) ve backend'i §9'da *"taranmadı"* diye ilan etti — **ama yine de** §6.5 kararını ve F4.4'ü (efor **L**) verdi. Tek grep'lik ölçüm genellemeyi **güçlendiriyor**:

| db | messaging | Aynı mı |
|---|---|---|
| `db_summary_5m` (`store.go:3346`) | `messaging_summary_5m` (`store.go:3641`) | ✅ yapısal ikiz |
| `db_caller_summary_5m` (`store.go:3393`) | `messaging_caller_summary_5m` (`store.go:3674`) | ✅ yapısal ikiz (messaging'de ek `kind` boyutu — producer/consumer) |
| `GetDBTrends` (`db_trends.go:137`) | messaging trendi **AYNI DOSYADA** (`db_trends.go:265` *"GetDBTrends'in messaging ikizi"*, sorgu `:291`) | ✅ birebir |
| purge listesi (`purge.go:34`) | `purge.go:35` | ✅ — ⚠ **messaging'de boşluk YOK**, db'de var (§7.4b) |
| okumalar | `dependencies.go:549`, `:580`, `:626`, `:1282`, `:1387`, `:1445` | ✅ db okumalarıyla aynı dosya, aynı havuz |

**Sonuç:** *"aynı desen tekrar kullanılabilir mi"* sorusunun backend cevabı **"MV ailesi zaten izomorf"** — yani F4.4'ün maliyeti **veri katmanında değil, YALNIZ yüzeyde**. v1 ayrıca §7.1'de *"bugün üç db rollup'ı var"* derken messaging ikizlerini saymıyordu (§7.1'e bilgi satırı olarak eklendi).

⚠ **F4.4'ün efor tahmini bu ölçümle yeniden değerlendirilmeli:** L → **M–L**. Kalan gerçek maliyet mimari değil, §5.3(3)'ün kabul ölçütü (çekmece kalacak mı) + `kind` boyutunun ürün metni.

### 6.5 Karar önerisi

> **Ortak `EntityDetail` KAPSAYICISI kurma.** Bu depoda denenmiş, dört kez ısırmış. Onun yerine **ortak söz dağarcığı** çıkar (`Stat` / `PanelTitle` / `SectionUnavailable` / `CallersTable` + `.pb-strip`); her varlık kendi `detailSections.tsx`'ini yazsın. `ProblemDetail.tsx` deseni budur ve depodaki en olgun örnektir.

---

## 7. Hiçbir panel ham span tablosuna sorgu atmamalı

⚠ **v2 KAPSAM DÜZELTMESİ (yüksek şiddet).** v1 bu bölümü **yalnız `FROM spans`** üzerine kurdu; `metric_points` kelimesi dokümanın tamamında **hiç geçmiyordu**. Oysa sayfanın kendi panellerinden biri — **motor paneli**, `?source=receiver` (§4 tablo satır 6) — ham `metric_points` okuyor. Ölçüm: `internal/chstore/{oracle,postgres,mysql,redis,db_topsql}.go` içinde **20 adet `FROM metric_points`** (bunun ikisi `db_topsql.go:92` ve `:171`). §7.1'e dördüncü kaynak olarak eklendi.

### 7.1 Bugün üç db rollup'ı var

Üçü de `AggregatingMergeTree` + `PARTITION BY toDate(time_bucket)` + **TTL 90 GÜN** + `quantilesTDigestState(0.5,0.95,0.99)`:

| MV | ORDER BY | `instance`? | exemplar? | DDL |
|---|---|---|---|---|
| `db_summary_5m` | `(db_system, instance, db_name, time_bucket)` | ✅ | ❌ | `store.go:3346-3380` |
| `db_caller_summary_5m` | `(db_system, instance, db_name, service_name, host_name, time_bucket)` | ✅ | ❌ | `store.go:3393-3422` |
| `db_statement_summary_5m` | `(db_system, db_name, service_name, stmt_hash, time_bucket)` | **❌** | ✅ `argMaxState`/`argMaxIfState` (v0.9.1097) | `store.go:3449-3471` |

`deploy_env` **hiçbirinde yok** (`dependencies.go:774-779`); env seçilince `/databases` ham span'e düşüyor.

**Bilgi satırı — messaging ikizleri (v2):** `messaging_summary_5m` (`store.go:3641`) + `messaging_caller_summary_5m` (`store.go:3674`) db ailesiyle **yapısal olarak izomorf** (§6.4b). Yani "bugün üç db rollup'ı var" cümlesi eksikti: aynı desen messaging'de de kurulu ve F4.4'ün veri katmanı **hazır**.

⚠ **TTL notu, alternatif değerlendirmesinin merkezinde:** MV TTL'i 90 gün ve sabit — `retention.go:100-105` yalnız `spans`/`span_links`/`logs`/`metric_points`/`profiles`'a `MODIFY TTL` uyguluyor. Ham spans varsayılanı `config/config.go:447` `SpansDays: 7`. **"Ham span'e düş" = 90 günlük cevabı 7 güne indir.**

### 7.1b DÖRDÜNCÜ KAYNAK — `metric_points` (motor paneli, `?source=receiver`)

| Kaynak | Kademe | Pencere | Kim okuyor |
|---|---|---|---|
| `rollup_metrics_1h` | 3600s | **13 ay** | `metric_rollup_read.go:47-51` |
| `rollup_metrics_5m` | 300s | **90 gün** | aynı |
| `rollup_metrics_1m` | 60s | **14 gün** | aynı |
| **ham `metric_points`** | — | ⚠ **7 GÜN** (`config/config.go:447` `MetricsDays: 7`; TTL uygulayıcısı `retention.go:104` `{"retention.metrics", …, "metric_points", "time", true}`) | motor panelleri + `db_topsql` — **20 `FROM metric_points`** |

⚠ **İKİ ÖLÇÜLMÜŞ SONUÇ, v1'de ikisi de kaçmış:**

**(a) Sayfanın iki kaynağının PENCERESİ farklı ve bu ilan edilmiyor.** `?source=spans` → MV, **90 gün**. `?source=receiver` → ham `metric_points`, **7 gün**. Yani v1'in ham-spans dalı için ısrarla yazdığı *"90g→7g, SÖYLENMELİ"* asimetrisi **sayfada ZATEN var** ve söylenmiyor. Aynı `?range=30d` seçimiyle operatör bir kapıda dolu, diğerinde boş grafik görüyor ve nedenini hiçbir yerde okumuyor. → **F0.9** (ucuz dilim: `?source=receiver` iken pencere sınırının ilanı).

**(b) Bu paneller metrik rollup kademelerini ASLA kullanamaz — mimari, geçici değil.** `metricRollupPlan` (`metric_rollup_read.go:65`) `f.Instance != ""` görünce **ham yola düşüyor**:

```go
if f.Name == "" || len(f.Filters) > 0 || len(f.GroupBy) > 0 || f.Instance != "" || f.Engine != "" {
    return metricRollupTier{}, false
}
```

Motor paneli **tanım gereği** tek bir instance'ın metriğini soruyor (`?instance=`), yani kapı her zaman kapalı. **"Hiçbir panel ham tabloya sorgu atmamalı" ilkesinin bu sayfadaki kalıcı istisnası budur** ve MV cerrahisiyle değil, rollup şemasına `instance` boyutu ekleyerek çözülür — bu da §7.3'ün G6 sınıfına girer. **Bugün: bilinçli muafiyet, ilan edilmeli.**

### 7.2 Ham `spans`'a düşen yedi okuma

(`internal/chstore` altında test-dışı toplam `FROM spans` = **159**)

| # | Yer | Yüzey | Koşul | Değerlendirme |
|---|---|---|---|---|
| 1 | `dependencies.go:416` | `/database` | **koşulsuz** | ⚠ **ÖLÜ** — sökülmeli (§1.4c) |
| 2 | `dependencies.go:1063` | `/databases` | yalnız env seçili | bilinçli takas, `Source:"raw"` ile **ilan ediliyor** (`:1102`) |
| 3 | `dependencies.go:1116` | çağıranlar | aynı env dalı | aynı ilan |
| 4 | `dbqueries.go:135` | `/slow-queries`, `/database` | `!hasDBStmtHashCol \|\| !UseSummaryMV(pencere)` (`:151-153`, `:202`) | kapıda bekleyen fallback |
| 5 | `dbqueries.go:378` | `/service` "DB queries" | **koşulsuz** | ⚠ MV'ye taşınabilir **ANCAK anahtar öneki uyuşmuyor + pencere kapısı gerekiyor** (v1: *"bedava"* — YANLIŞ) |
| 6 | `dbstmt_detail.go:318` | ifade exemplar | `!hasDBStmtExemplarCols` ya da MV boş | iki `LIMIT 1` **nokta okuması**, agregat değil — muafiyet |
| 7 | `topology.go:417` | topoloji db kenarı | **koşulsuz** | MV'ye taşınabilir, ama **pencere dispatcher'ı + TDigest kabul ölçütü** ister (v1: *"cerrahi istemez"* — eksik) |

**Ham `metric_points`'e giden okumalar (v2'de eklendi, yukarıdaki 7'ye DAHİL DEĞİL):**

| # | Yer | Yüzey | Koşul | Değerlendirme |
|---|---|---|---|---|
| M1 | `oracle.go` / `postgres.go` / `mysql.go` / `redis.go` — 18 `FROM metric_points` | motor paneli (`?source=receiver`) | **koşulsuz** | ⚠ **kalıcı ham okuma** — `metricRollupPlan` `f.Instance != ""` ile kapıyı kapatıyor (§7.1b/b); pencere **7g** |
| M2 | `db_topsql.go:92`, `:171` | Postgres/MySQL TopSQL | **koşulsuz** | aynı sınıf; ayrıca sabit `ORDER BY total_ms DESC LIMIT 10` (§4) |

**#5 — ⚠ "BEDAVA" DEĞİL, ÜÇ GİZLİ MALİYET (v1 düzeltmesi):**

`dbqueries.go:361-386` ham `spans` üzerinde iki katmanlı `replaceRegexpAll` koşuyor ve fonksiyonun tamamında `db_statement_summary_5m` **geçmiyor** — bu tespit doğru; kardeşinin dispatcher'ı saf ve testli (`:151-153`). Ama taşıma bedava değil:

1. ⛔ **ANAHTAR ÖNEKİ PRUNE ETMİYOR.** `db_statement_summary_5m` ORDER BY = `(db_system, db_name, service_name, stmt_hash, time_bucket)` (`store.go:3452`). `GetTopDBQueries` imzası ise (`dbqueries.go:343-345`) `(ctx, service, from, to, limit)` — **`db_system` imzada bile yok** ve sorgu yalnız `WHERE service_name = ?` filtreliyor (`:379`). `service_name` anahtarın **3. sırasında** → önek prune edilemez, **tam MV taraması** olur. Ham yolda `service_name` spans PK'sının ilk kolonu; yani taşıma "daha az satır" garantisi vermiyor.
2. ⛔ **PENCERE KAPISI ŞART.** Kardeş dispatcher `slowQueriesUseMV(hasCol, from, to) = hasCol && UseSummaryMV(to.Sub(from))` (`dbqueries.go:151-153`, sınır `evaluator_reads.go:52`). 5dk kova sınırının altındaki pencereler ham yolda kalmalı — ve `/service` paneli **varsayılan 15 dk** ile geliyor (`api.go:2917-2919`). MVWindowStart hizalaması ayrı kalem (MEMORY *"Kova sınırı taraması"*).
3. ⛔ **PARİTE.** Ham yol `GROUP BY norm_stmt` (regex), MV `stmt_hash` — **satır sayısı değişir**. Ayrıca ham yol `uniqExact(db.name) AS db_n` (`dbqueries.go:371`) hesaplıyor; `DBNameCount` MV'de yeniden türetilmeli.

✅ **Ayrı ve GERÇEKTEN ucuz olan yarısı:** cache anahtarı **ham nanosaniye** (`api.go:2922-2923` `from=%d:to=%d` `UnixNano()`), kardeşi dakikaya kovalıyor (`:2946-2948` `UnixNano()/int64(time.Minute)`); `timeRangeToNs` her çağrıda `Date.now()` okuduğu için **60s cache pratikte hiç isabet etmiyor**. Bu tespit **doğru, bağımsız ve ölçülebilir** → plana ayrı dilim olarak alındı (**F0.2a**), MV dispatcher ayrı (**F0.2b**).

**#7 — MV'ye taşınabilir ama "cerrahi istemez" ≠ "maliyetsiz" (v1 düzeltmesi):**

`topology.go:382-425` `GetEdgeInstances`: `SELECT dbInstanceExpr AS instance, count(), avg(duration), quantile(0.99)(duration) FROM spans WHERE service_name = ? AND db_system = ? AND time >= ? AND time <= ?`. Bu soru `db_caller_summary_5m`'de **tam olarak var**. ⚠ Ama:

1. **Anahtar öneki yine uyuşmuyor:** hedef MV ORDER BY = `(db_system, instance, db_name, service_name, host_name, time_bucket)` — **parent servis yüklemi 4. sırada**. `db_system` (1.) prune ediyor, `service_name` etmiyor.
2. ⛔ **ASIL KALEM — pencere grenliliği:** ham sorgu **keyfi `time` sınırlarıyla**, MV **5 dk `time_bucket`**ta çalışıyor. Geçiş bir **pencere dispatcher'ı** (`UseSummaryMV` emsali) + **MVWindowStart hizalaması** ister; aksi hâlde topolojinin **kısa odak pencerelerinde** sayılar sessizce kayar (MEMORY *"Kova sınırı taraması"* + *"Boş küme kaybolur, sıfır olmaz"*).
3. **Kabul ölçütü yazılmalı:** `quantile(0.99)` → `quantilesTDigestMerge` index 3 (`dependencies.go:329` zaten öyle yapıyor) **birebir aynı sayıyı vermez**; tolerans ilan edilmeli.
4. ℹ️ `queue` dalı **bilinçli olarak** `peer_service`te kalıyor (`topology.go:407-411`, gerekçe yorumda: *"bir kuyruk kenarının instance'ı broker'dır"*) — dilim **yalnız db dalını** taşır.

→ **F0.3 eforu S → S–M.**

**Sayıma dahil edilmeyenler:** MV'lerin kendi SELECT'leri, `store.go:3023` boot probe'u, `WriteTopologyBucket` (arka plan agregatör), `breakdown.go:62`, `aggregate.go:86` / `repo.go:3912` (span/trace **listesi** projeksiyonu — trace araması agregat değildir, bilinen muafiyet).

### 7.3 ⚠ G6 REDDİ — MV cerrahisi bedava değil, daha önce REDDEDİLDİ

Asıl kapsam boşluğu MV'lerin varlığında değil, `db_statement_summary_5m`in **boyutunda**: `instance` yok (`store.go:3452`, HEAD'de doğrulandı) → "Top statements" instance'ı sessizce düşürüp `(system, db_name)` kapsamında okuyor.

- Teşhis: `docs/audit/frontend-ux-audit.md:181`.
- Maliyet düzeltmesi: `docs/audit/wave-execution-audit.md:301` — *"Ö11 M→L (**RAPOR YANLIŞ**: `db_statement_summary_5m` ORDER BY'da instance boyutu YOK; tam düzeltme MV yeniden kurulumu)"*.
- **Operatör kararı:** `memory/project-ux-audit-execution.md:75` — *"G6 /database instance = OPERATÖR **'bugünkü kapsamla yaşa'** (MV cerrahisi = 90g statement geçmişi feda, **reddedildi**)"*.
- Gemiye giden: yalnız **kapsam beyanı** (v0.9.961, `f1fad6e1`) — `DatabaseDetail.tsx:332-333` `all ${refObj.system} instances`.

Ret **genelleştirildi**: `entity-model-audit-2026-08-23.md:456-459` B1'i düşürürken G6'yı emsal gösteriyor → bugün fiilen **"tarihi feda eden her MV cerrahisi varsayılan RED"**.

⚠ **Nüans:** ret *instance kavramına* değil, onu **MV anahtarına sokmanın bedeline**. Sayfanın diğer panelleri zaten instance'lı (`DatabaseDetail.tsx:390-395`, `:92-93`); **tek instance-kör hücre** Top statements.

**Reddin DIŞINDA kalan yollar (yazılı ve emsalli):**

1. **Ham-spans dalı, ilan edilmiş** — v0.9.821 emsali (`dependencies.go:1050-1141`, `Source:"raw"`). Uca opsiyonel `instance`; doluysa ham yol, boşsa bugünkü MV yolu. Kimlik ifadeleri **paylaşılan sabitlerden** (`identity.go:325`, `:341`) → iki yol aynı DB'yi aynı adla çözer. ⚠ Pencere 90g→**7g** düşer, **söylenmeli**.

   ⚠ **ÜÇ SERT-KISIT ADIMI (v1'de eksikti, F4.3'e taşındı):**

   **(a) ÖNBELLEK ANAHTARI — zorunlu.** Uca opsiyonel `instance` eklenirse `getSlowQueriesGlobal`ın anahtarına da **girmek ZORUNDA**. Bugünkü anahtar (`api.go:2946-2948`): `slow-queries-global:from=%d:to=%d:sys=%s:db=%s:limit=%d` — **instance yok**. Girmezse ilk instance'ın satırları 60 sn boyunca diğerlerine servis edilir: CLAUDE.md *"cache key hashes ALL inputs"* + v0.5.187 sınıfı. **Emsal aynı ailede, aynı gerekçeyle yazılı:** `api_databases.go:127-129` — *"dbName CACHE ANAHTARINA da girer — girmeseydi ilk açılan veritabanının çekmecesi 30 sn boyunca diğerlerine de servis edilirdi, yani düzeltilen yalan cache katmanında yaşamaya devam ederdi."* (v1 anahtardan F0.2 ve F1.2'de bahsedip **F4.3'te bahsetmiyordu**.)

   **(b) PK-PRUNE — zorunlu.** Ham dal `spans`ta `dbInstanceExpr = ?` ile filtreleyecek, ama spans PK'sı `(service_name, time)`. `dependencies.go:405-423`'ün `service_name IN (…)` daraltması (v0.7.35, **operatör-raporlu** *"top statements blank at 1000s of services"*) burada da **ŞART**. v1 bu kalıbı yalnız yol 2'nin altında anıyordu.

   **(c) `db_stmt_hash` PROBE'a kapılanır.** Ham dal `GROUP BY db_stmt_hash` yapabilir (stored kolon, regex'ten ucuz) — **ama** iki paragraf aşağıda aynı bölüm `wave-execution-audit.md:299`'u alıntılayıp *"`db_stmt_hash` KOLONUNA dokunulmaz (harici Distributed'da çözülemiyor)"* diyor. **v1 kendi içinde çelişiyordu.** Ham dal `hasDBStmtHashCol` boot probe'una (`store.go:3023`) kapılanmalı; probe false ise **regex yoluna** düşer.

   → **F4.3 eforu M → M–L.**

2. **İki adımlı cerrahisiz kapsamlama** — `db_caller_summary_5m`'den çağıranları al, statement MV'sini `service_name IN (…)` ile daralt. **Tam filtre değil** ama bugünkünden dar ve MV içinde; kalıp zaten repoda (`dependencies.go:409-423`). ⚠ Ürün bunu **söylemek zorunda** (`db_ownership.go:39-47` dili).

3. ⛔ **Yıkıcı olmayan ALTER — AÇIK DEĞİL. İSTENEN SONUCU ÜRETEMEZ.**

   ⚠ **v1 DÜZELTMESİ (yüksek şiddet).** v1 bu yolu `store.go:4154-4161` reçetesiyle "bedava değil ama açık" diye listeledi ve (a) şıkkında *"ORDER BY yerinde değişmez → anahtar-dışı kolon, satır-yüklemi"* dedi. **Bu yanlış.**

   `db_statement_summary_5m` **`AggregatingMergeTree`** (`store.go:3450`), ORDER BY = `(db_system, db_name, service_name, stmt_hash, time_bucket)` (`:3452`). AggregatingMergeTree satırları **SIRALAMA ANAHTARINA göre birleştirir**. `instance` SELECT+`GROUP BY`a girip ORDER BY'a **girmezse**, aynı anahtardaki iki instance'ın state'leri yine **tek satırda birleşir** ve `instance` kolonunun değeri **keyfi** olur (hangi parça son merge'de kaldıysa). Sonuç: `instance = 'corebank-scan.prod'` yüklemi **SESSİZCE YANLIŞ SAYI** döndürür — MEMORY *"Boş küme kaybolur, sıfır olmaz"*ın ikizi, ama daha kötüsü: boş değil, **yanlış dolu**.

   → **G6'nın reddettiği ORDER BY cerrahisi bu yolun ÖN KOŞULUDUR, ALTERNATİFİ DEĞİL.** Yol 3 doğrudan G6 reddine **çarpar**.

   (v1'in (b) ve (c) şıkları — upgrade öncesi 90 günün boş `instance`ı, dış-Distributed'da ALTER atlanması (v0.8.185/186) — **ayrıca** geçerli, ama artık ikincil.)

   ⚠ MEMORY *feedback-audit-prescriptions-get-executed*: **bu reçete er geç uygulanır**, o yüzden "bedava değil" yetmez; **AÇIK DEĞİL** yazmak gerekiyordu.

**→ Reddin dışında GERÇEKTEN kalan yol sayısı: 3 değil, 2** (ilan edilmiş ham-spans dalı + çağıran-kümesiyle daraltma).

**Ayrıca dokunulmaz:** `wave-execution-audit.md:299` — *"`db_stmt_hash` KOLONUNA dokunulmaz (harici Distributed'da çözülemiyor)"*.

### 7.4 İki yan bulgu

**(a) `GetDBTrends` alfabetik kesiyor.** `db_trends.go:147-149` `ORDER BY db_system, instance, db_name, time_bucket` + `LIMIT 200000`; 24s = 288 kova/anahtar → `200000/288 ≈ 694` anahtar. Üst sınır aynı ailede ilan edilmiş: `dependencies.go:741` `dbOverviewRowLimit = 5000` → 1.44M satır, LIMIT'in 7 katı. Kesme **alfabetik** çünkü ORDER BY kimlikle başlıyor; v0.9.821'in `dbTopCallersSQL` için düzelttiği kusurun aynısı (`dependencies.go:794-805` *"CH'nin `LIMIT n BY` tam olarak bunun için var"*) — `db_trends.go`'da `LIMIT n BY` **yok**. İkinci katman: `/database` bunu **miras alıyor** (§1.4b) → alfabenin sonundaki DB'nin detayında **grafikler boş** (`DatabaseDetail.tsx:287-289`) ama başlık istatistikleri **dolu**. **ÖLÇÜLMEDİ:** bu filoda kaç anahtar var.

⚠ **(a2) Aynı dosyada sorulmamış bir soru: `db_trends.go` YANLIŞ HAVUZDA** (v2'de eklendi). Üç okuması da `s.conn` kullanıyor (`db_trends.go:137`, `:234`, `:283`), oysa aynı ailedeki `dependencies.go`da **16 okuma `telemetryReadConn()`** üzerinden gidiyor ve `s.conn.Query` **hiç yok** (grep: 16 / 0). Ayrım önemsiz değil: v0.9.496 RoundRobin okuma havuzunu tam da *"analitik SELECT'lerin koordinasyonu ilk node'da birikiyor"* diye açtı (`store.go:714-726` yorumu). Yani **bu dokümanın tespit ettiği EN AĞIR okuma** (filo geneli, `LIMIT 200000`) in-order ana bağlantıda duruyor.

⚠ **Sözleşme metni gerçekle ayrışmış:** `conn_strategy_test.go:173` `"databases_series.go": true` diye bir dosyayı muaf tutuyor ve yorumunda *"dependencies.go / db_trends.go'daki kardeş okumalarla aynı havuz"* diyor — ama **`databases_series.go` DİYE BİR DOSYA YOK** (`ls internal/chstore/databases_series.go` → *No such file*; dizinde yalnız `red_series.go`). Yani muafiyet ölü bir ada asılı ve yorumu `db_trends.go` hakkında **yanlış bir şey iddia ediyor**.

**Somut engel — dilim planlanırken bilinmeli:** `conn_strategy_test.go`daki `allowed` haritası **tek yönlü bir kapıdır** — listede olmayan hiçbir dosya `telemetryReadConn` **çağıramaz** (`:196-206`). `db_trends.go` listede **YOK**. Dolayısıyla F0.5 okumaları havuza taşırsa `TestTelemetryReadConn…` **kırmızıya döner** ve `db_trends.go` bilinçli olarak allowlist'e eklenmelidir (kaynakları `db_summary_5m` + `messaging_summary_5m`, ikisi de saf telemetri MV'si → **eklenmesi doğru**).

**ÖLÇÜLMEDİ:** taşımanın gerçek etkisi. Dilim bir `system.query_log` medyanıyla **açılmalı** (MEMORY *"Perf benchmark disiplini"*: tek ad-hoc zamanlama yalan söyler).

**(b) `db_statement_summary_5m` purge listesinde YOK.** `purge.go:34` yalnız `db_summary_5m`, `db_caller_summary_5m`; `configPreserveTables`ta da yok. `PurgeTelemetry` sonrası `/slow-queries` ve Top statements, olmayan span'lerin agregatlarını **90 gün** gösterir. `purge_test.go:11-13` yalnız yinelenen ad + config sızması bakıyor. Messaging'de aynı boşluk **yok** (`purge.go:35`). **Kasıt mı unutma mı DOĞRULANMADI.**

---

## 8. Aşamalı uygulama planı

⚠ **Mockup-first şartı.** Dört canlı-redesign reddi var; ilk üçünün "HARD kısıt" hâli 2026-07-30'da kalktı ama **mockup-first + tek-commit revert** şartı kalıcı (`memory/feedback-tables-over-cards.md:33`). **Dördüncü ret (servis Overview, 2026-08-23) o muafiyetten SONRA geldi** ve *"yeniden kuyruğa alma"* diyor → yoğunluğu **azaltan** düzen çelişir, **entegrasyonu artıran** yön serbest. Ayrıca: liste sayfalarında sayfa-üstü KPI blokları **yasak**, **detay sayfalarında tek-varlık skor şeridi ONAYLI** (`memory/project-redesign-2nd-round.md:58-64`, `:93`); sayfa-düzeyi yapışkan şerit önerilmez (`feedback-no-floating-strips.md:23-24`).

⚠ **v2'de tablo yeniden kuruldu.** Değişiklikler: **F4.2 SİLİNDİ** (pivot zaten gemide, §4); **F0.2 ikiye bölündü**; **F2.1 üçe bölündü**; **F0.6-F0.9 eklendi**; F0.3/F4.3/F4.4 eforları düzeltildi; üç belirsiz hücre evet/hayır'a çevrildi; **iki yeni kolon** eklendi (regresyon testi seam'i + yeni boş/hata durumu).

| # | Adım | Tek başına merge? | Entity ID kararını bekliyor mu? | Efor | Klasik düzen | Regresyon testi (saf seam) | Yeni boş/hata durumu? |
|---|---|---|---|---|---|---|---|
| **F0.1** | Ölü `topOps` söküm (backend + `DetailDrawer` db dalı + tip) | ✅ | ❌ hayır | S | korur | — (silme; `go build` + tsc yeter) | ❌ |
| **F0.2a** | `/service` DB-queries cache anahtarını **dakikaya kovala** (`api.go:2922-2923`) | ✅ | ❌ hayır | **S** | korur | **`serviceDBQueriesKey(...)` saf fonksiyon** — `cache_key_test.go` kalıbı; ns vs dakika ayırt edici vakası | ❌ |
| **F0.2b** | `GetTopDBQueries` → MV dispatcher | ✅ (F0.2a'dan bağımsız) | ❌ hayır | **M–L** ⚠ A/B parite ölçümüyle **AÇILIR** | korur | `topDBQueriesUseMV(hasCol, from, to)` — `slowQueriesUseMV` ikizi, sınır vakaları | ⚠ evet (MV boşsa) |
| **F0.3** | `GetEdgeInstances` db dalı → `db_caller_summary_5m` | ✅ | ❌ hayır | **S–M** | korur | `edgeInstancesUseMV(from,to)` + MVWindowStart hizalaması; TDigest-vs-exact tolerans | ⚠ evet (kısa pencere) |
| **F0.4** | `db_statement_summary_5m` → purge listesi (önce niyet doğrula) | ✅ | ❌ hayır | S | korur | `purge_test.go` — MV'nin listede olduğunu **pozitif** doğrula | ❌ |
| **F0.5** | `db_trends` kesmesi + `/database`e scoped trend uç | ✅ | ❌ hayır | M | korur | `dbTrendsSQL` saf builder (`LIMIT n BY` var mı) + `dbTrendCurrentIdx` mevcut testi | ⚠ evet (yeni uç boş dönebilir) |
| **F0.6** | **Env süzgeci db öznelerini elemesin** (§2.4) | ✅ | ❌ hayır | S–M | korur | `applyEnvServiceScope` + `envKeepsRow` **ayırt edici vaka**: env seçili + `service="db:oracle@x"` + `kind="db"` | ❌ (kaybolan satırlar geri gelir) |
| **F0.7** | **Motor panellerinde hata ≠ boş** (§1.3) | ✅ | ❌ hayır | S | korur | — (FE; `vitest` + üç durumun ayrı render'ı) | ✅ **evet — asıl amacı bu** |
| **F0.8** | `/database` başlığına **kapsam beyanı** (§3.1) | ✅ | ❌ hayır | S | korur (altbaşlık büyür) | statik metinse test gerekmez; **ölçümlü** sürümde `uniq(server.address)` probu ayrı dilim | ❌ |
| **F0.9** | `?source=receiver` **pencere sınırının ilanı** (§7.1b/a) | ✅ | ❌ hayır | S | korur (altbaşlık) | — | ❌ |
| **F1.1** | `postgres`↔`postgresql`↔`mariadb` alias haritası + regresyon testi | ✅ | ❌ **hayır** — F3.1'in ön-taksiti *olabilir* ama ona bağlı DEĞİL; bugünkü bir bug'ı tek başına kapatır | S | korur | `problem_subject_test.go` — `POSTGRES → postgresql` ayırt edici vakası **ŞART** | ❌ |
| **F1.2** | "En sık" sıralaması — **iki uç** (§4) | ✅ | ❌ hayır | S (span) + S (TopSQL) | korur | `orderClause(order)` saf fonksiyon, üç değer + geçersiz değer | ❌ |
| **F1.3** | `db_trends`ten p95 serisi | ✅ **(F0.5 SONRASI)** | ❌ hayır | S | **⚠ mockup**: 3→4 kart | — | ❌ |
| **F2.1a** | `SectionUnavailable` tek eve | ✅ | ❌ hayır | S | korur (**bayt-bayt aynı**) | — | ❌ |
| **F2.1b** | `PanelTitle` → `right` slotlu sürüme terfi | ✅ | ❌ hayır | S | korur (çıktı korunur) | — | ❌ |
| **F2.1c** | `Stat`/`HeaderStat` üçlüsü | ✅ | ❌ hayır | S–M | ⚠ **DEĞİŞTİRİR** — `StmtDetailDrawer` etiketi büyük harfe geçer; **mockup şartı** + prop kararı (`value` mi `children` mı) | — | ❌ |
| **F2.2** | `pages/databases/detailSections.tsx` çıkarımı (§5.3 üç kuralıyla) | ✅ (F2.1a-c sonrası) | ❌ hayır | M | korur | — | ❌ |
| **F2.3** | Ortak `CallersTable` (4 nüsha → 1) | ✅ (F2.2 sonrası) | ❌ hayır | M | **⚠ mockup**: kolonlar bugün farklı | — | ❌ |
| **F2.4** | `.pb-strip`/`.pb-tile` ölü primitifini bağla | ✅ | ❌ hayır | S–M | **⚠ mockup**: şerit görselini değiştirir | — | ❌ |
| **F3.0** | **ÖN KOŞUL ÖLÇÜMÜ** — motor başına çözünürlük (§3.8 SQL'i), lokal **ve** prod | ✅ (kod yok) | — | S | dokunmaz | — | ❌ |
| **F3.1** | `dbEntityID(system, host, port)` saf fonksiyon + parite testi, **hiçbir yere bağlanmadan** | ✅ | ⚠ **KARAR ANI** | M | korur | `dbEntityID` tablo-testli; `db_stmt_hash` parite sözleşmesi kalıbı | ❌ |
| **F3.2** | Alias + host/port ayrıştırması `identity.go`ya; HAT B üç yazımı tek eve | ✅ (F3.1 sonrası) | ⛔ **bekliyor** | M–L | korur | HAT B üç yazımın tek fonksiyona indiğini pinleyen dosya-yüzeyi testi | ❌ |
| **F3.3** | HAT A↔HAT B köprüsü (ölçülerek) | ❌ — **blokör: F3.1 + F3.2** | ⛔ **bekliyor** | L | korur | `TestDBSubjectIDIsNotAHatAJoinKey` **güncellenir**, silinmez | ⚠ evet |
| **F4.1** | `/database`e log pivotu (`logsHref`) — **şablon: `dbTracesHref`** | ✅ | ❌ hayır | S | **⚠ mockup**: yeni buton | `logsHref` üretici testi (v0.9.1353 kaynak-tarama kapısı kalıbı) | ❌ |
| ~~**F4.2**~~ | ~~`/database`e trace pivotu~~ | — | — | — | — | — | — |
| **F4.3** | Top statements instance kapsamı (ham-spans dalı, ilan edilmiş) | ✅ | ❌ hayır | **M–L** | korur (altbaşlık değişir) | **cache anahtarı saf fonksiyonu** — instance'lı/instance'sız ayırt edici vakası (v0.5.187 sınıfı) | ⚠ evet (7g dışı boş) |
| **F4.4** | `/messaging` tam sayfaya terfi | ❌ — **blokör: F2.2** (messaging'in kendi `detailSections.tsx`i onun deseninden türer) **+ §5.3(3) kabul ölçütü** (çekmece kalacak mı) | ❌ hayır | **M–L** (§6.4b: MV ailesi izomorf, veri katmanı hazır) | **⚠ OPERATÖR KARARI** | — | ⚠ evet |

**F0.1 dosyalar:** `chstore/dependencies.go` (`:413-429` + `DBDetail.TopOps`), api tip zarfı, `lib/types.ts`, `features/dependencies/DetailDrawer.tsx` (db dalı). ⚠ Söküm öncesi `Messaging.tsx`in tek `kind='queue'` tüketici olduğu **tekrar** doğrulanmalı (v0.9.846 dersi). `GetMessagingDetail` (`dependencies.go:496+`) ayrı, etkilenmez.

**F0.2a dosyalar:** `api/api.go:2922-2923` — anahtarı `UnixNano()`dan `UnixNano()/int64(time.Minute)`a çevir, kardeşiyle (`:2946-2948`) aynı biçime getir. **Bağımsız, ölçülebilir kazanç.**

**F0.2b dosyalar:** `chstore/dbqueries.go:343-390` (+ `slowQueriesUseMV` ikizi). ⚠ **Üç blokör §7.2 #5'te:** (1) MV anahtar öneki `service_name`i prune etmiyor (imzada `db_system` bile yok), (2) pencere kapısı şart (`/service` varsayılanı **15 dk**, `api.go:2917-2919`), (3) parite ölçülmedi (`norm_stmt` vs `stmt_hash`, `uniqExact(db_name)`). **Dilim bir A/B ölçümüyle AÇILIR, onunla kapanmaz.**

**F0.3 dosyalar:** `chstore/topology.go:382-425` (yalnız `db` dalı; `queue` dalı `:407-411` bilinçli olarak `peer_service`te kalır). ⚠ Pencere dispatcher'ı (`UseSummaryMV` emsali) + **MVWindowStart hizalaması** + TDigest-vs-exact kabul ölçütü — üçü de dilimin parçası.

**F0.5 dosyalar:** `chstore/db_trends.go`. İki seçenek — (a) `:149` → `LIMIT 288 BY db_system, instance, db_name`; (b) `/database` için scoped trend uç. **(b) önerilir**, §1.4(b)'yi de kapatır. ⚠ `/api-route` kapısı: yeni uç kendi dosyasına. ⚠ **Aynı dilimde:** üç okumayı `s.conn` → `telemetryReadConn()`e taşı (§7.4a2) **ya da neden taşınmadığını yaz**; taşınırsa `conn_strategy_test.go`daki `allowed` haritasına `db_trends.go` eklenmeli (kapı tek yönlü, `:196-206`) **ve** ölü `"databases_series.go"` muafiyeti temizlenmeli. **ÖLÇÜLMEDİ:** taşımanın etkisi — dilim `system.query_log` medyanıyla açılmalı.

**F0.6 dosyalar (yeni):** `chstore/env_members.go:147` `applyEnvServiceScope`, `api/problems_filter.go:83` `envKeepsRow` (+ çağıranı `api/inbox.go:690`), `chstore/problem.go:1037` `CountProblemsNotInStatuses`, `chstore/problem_subject_lane.go` `CountProblemsBySubject`. **Şablon hazır:** `problemServicesConjunct`ın `dbEscape`i (`problem_subject_lane.go:83-104`) — takım süzgecinde aynı sınıf zaten çözülmüş, aynı iki-boot (`hasKindCol`) sözleşmesiyle. ⚠ Karar operatörün: db problemleri **env'den muaf** mı (global satırlar gibi) yoksa **çağıranlarının env'ine mi** çözülsün (`db_ownership.go` kalıbı)? **Muafiyet ucuz ve dürüst; çözme daha doğru ama HAT A/HAT B engeline değer.** Öneri: **muafiyet** (`kind='db'` satırları geçer), üstüne rozet.

**F1.1 dosyalar:** `chstore/identity.go` (`DBSubjectID`in system yarısı, **alias haritasının TEK evi**), `evaluator/db_capacity.go:181`, `chstore/problem_subject_test.go`. ⚠ **v2'de genişledi:** frontend'de dört alias dalı var — `pages/DatabaseDetail.tsx:391`, `:394`, `features/dependencies/DetailDrawer.tsx:393`, `:396` (sökülmüyorsa) — ve `mariadb` backend haritasında **hiç yok** (§2.2). Dilim, alias kümesinin hangi tarafta duracağına karar vermeden kapanmaz. ⚠ MEMORY *"Muhafız gölgeler mutasyonu"*: ayırt edici vaka (`POSTGRES`→`postgresql`) **şart**; `mariadb` ikinci vaka.

**F1.2 dosyalar:** span ucu — `chstore/dbqueries.go` (`ORDER BY` parametrik) + `api/api.go` (**cache anahtarına `order`**); receiver ucu — `chstore/db_topsql.go:108`, `:188`. ⚠ **Kapsam kararı zorunlu:** ya iki uç birden, ya **açıkça "yalnız span yolu"** diye daraltılır. Her iki hâlde **§4'ün dürüstlük yükümlülüğü** ürün metnine girer: iki listenin farklı sayı vermesi **kapsam farkıdır** (`db_topsql.go:23-27`), bug değil.

**F0.8 — kapsam beyanı (§3.1'in tek ve en ucuz karşılığı).** v1'de dokümanın **en yüksek güvenli, lokalde ÖLÇÜLMÜŞ** kusuru (`/database?system=oracle&instance=oracle&name=COREBANK` iki fiziksel host'un toplamını gösteriyor ve **bunu söylemiyor**) planda **hiçbir adıma karşılık gelmiyordu**. Emsal aynı sayfada iki kez var: G6 kapsam beyanı (v0.9.961, `DatabaseDetail.tsx:332-333` `all ${system} instances`) ve F4.3'ün *"ilan edilmeli"*si. **Entity ID kararını beklemez, klasik düzene dokunmaz.** ⚠ İki sürümü var: **statik** (her zaman "bu instance birden çok fiziksel adresi kapsıyor olabilir") = **S, ölçüm yok**; **ölçümlü** ("2 fiziksel adres") = ham `spans`ta `uniq(server.address)` probu gerektirir çünkü `db_caller_summary_5m` o bilgiyi **taşımıyor** (§3.5) → **ayrı ve daha pahalı dilim**. Önce statik.

**F3.0 — ÖN KOŞUL, kod değil ölçüm.** §3.8'in SQL'i lokalde **ve** prod'da koşar. Çıktısı iki tablo: (1) motor başına `r_any` çözünürlük, (2) `db_system` başına `uniq(peer_service)` vs `uniq(server.address)` (§3'ün kategorik kararı buna bağlı). **F3.1 bu ölçüm olmadan başlamaz.**

**F3.1 — karar anı.** Çıktısı kod değil **karar**: alanlar (system/host/port), normalizasyon (§3.8 — **hipotez**, F3.0 ile doğrulanır), ve *yanına mı yerine mi* (§3.9, **iki maliyet sınıfı ayrı**: `spans` kolonu ≠ MV boyutu). Fonksiyon yazılır, testlenir, **hiçbir okuma yoluna bağlanmaz**. F3.2/F3.3 buna kilitli; kabul ölçütü §3.10 + §3.9'un *"kaydedilmiş görünümler ne olacak"* cevabı.

**F4.3 —** §7.3'ün 1. yolu; pencere 90g→7g düşer, **ilan edilmeli**. G6 reddine **çarpmaz** çünkü MV anahtarına dokunmaz. ⚠ **Üç sert-kısıt adımı §7.3 yol 1'de:** (a) `instance` cache anahtarına girer (`dbDetailKey` emsali), (b) ham dal `service_name IN (…)` ile PK-prune edilir (`dependencies.go:409-423`), (c) `db_stmt_hash` kullanımı `hasDBStmtHashCol` probe'una kapılanır, yoksa regex yoluna düşer.

**F4.4 — blokörü adıyla:** **F2.2** (messaging kendi `detailSections.tsx`ini db'nin deseninden türetir) **ve** §5.3(3)'ün kabul ölçütü (çekmece + sayfa aynı anda mı yaşayacak). Veri katmanı **hazır** (§6.4b), o yüzden efor **L → M–L**.

**URL'e yeni eksen yazan her adım için:** `lib/chatContext.ts:38-50` `SERVICE_PARAM_ROUTES` güncellenmezse AI sohbeti o rotada **sessizce kör** açılır — kapısı yok, `tsc` göremez (`cross-page-context:400-405`). `lib/urlState.ts:164` `rebuildPreserving` semantiği zorunlu: `prev` **kopyalanır**, sıfırdan kurulmaz.

**Önerilen sıra (v2):**

```
F0.6 → F0.1 → F0.4 → F0.2a → F1.1 → F0.7 → F0.8 → F0.9
     → F0.3 → F0.5 → F1.2 → F2.1a → F2.1b → F2.2 → F4.1
     → [F3.0 ÖLÇÜM] → [F3.1 KARAR] → F0.2b / F4.3 → …
```

**Neden F0.6 başta:** tek gerçek **kayıp veri** kusuru odur — env seçiliyken db problemleri hiç görünmüyor (§2.4), ve şablonu (takım süzgecinin `dbEscape`i) zaten depoda.

F0 + F1 bloğu (**12 dilim**) tamamen bağımsız, hiçbiri entity ID kararını beklemiyor, hiçbiri klasik düzene dokunmuyor. F2'de yalnız **F2.1c** / F2.3 / F2.4 mockup gerektiriyor. **F3.1 hem F3.0 ölçümü hem operatör kararı olmadan başlamaz.** F0.2b ve F4.3 kasıtlı olarak **sona** alındı: ikisi de ölçüm/parite kapısı arkasında.

---

## 9. ÖLÇÜLMEDİ

- ⛔ **YENİ ZİNCİRİN MOTOR BAŞINA ÇÖZÜNÜRLÜK ORANI** (§3.8, F3.0'ın ön koşulu) — 7g `spans` üzerinde `server.address` / `net.peer.name` / `db.host` boş-olmama oranı. **Bilinen tek nokta: `clickhouse` = %0** (§3.3), yani önerilen zincir o motorda hiçbir ID üretmez. `redis`/`elasticsearch` için `peer_service` düşünce geriye ne kaldığı **hiç bakılmadı**. **Bu ölçüm gelmeden §3.8 bir öneri değil hipotezdir.**
- ⛔ **KATEGORİK "URL'e konabilir mi" KARARININ PROD ÖLÇÜMÜ** — `db_system` başına `uniq(peer_service)` vs `uniq(server.address)`. §3.1/§3.2/§3.4'ün tamamı lokal demo jeneratörünün sabitlerini ölçüyor (`cmd/demo/main.go:552-564`, `mesh.go:203-234`); prod enstrümantasyonu jenerik motor adı basmıyorsa o üç gerekçe **düşer**.
- **v0.9.1318/1327/1338/1345'in çalışan davranışı** — lokal imaj v0.9.1315; MEMORY *"Keep local on latest"* **ihlal durumda**.
- **Prod davranışı hiç ölçülmedi** — `peer_service`in prod'da da jenerik motor adı taşıdığı, `server.address`in portlu geldiği **bilinmiyor**; lokal ölçüm demo jeneratörünün seçimi **olduğu doğrulandı** (yukarı).
- ⛔ **§2.4'ün env-süzgeci kusuru ÜRÜNDE DOĞRULANMADI** — lokalde `db:` öznesi 0 satır (v0.9.1315 imajı, `problems.kind` kolonu yok). Teşhis üç dosyanın okunmasından geliyor (`env_members.go:147`, `problems_filter.go:83`, `problem.go:1037`); prod'da `?env=` seçili bir `/inbox` ile **tek bakışta** doğrulanabilir.
- **Motor panellerinin 7 günlük penceresinin operatöre ne kadar ısırdığı** — `?source=receiver` ile `?range=30d` seçildiğinde ne kadar boş kaldığı ölçülmedi (§7.1b/a).
- **`db_trends.go`nun `s.conn` → `telemetryReadConn()` taşınmasının gerçek etkisi** (§7.4a2) — `system.query_log` medyanı alınmadı.
- **§3.6 postgres/postgresql üründe doğrulanmadı** — lokalde yalnız `oracledb` receiver'ı var (24s'te 1.039.718 satır).
- **`/api/databases/trends` gerçek payload boyutu**; **filodaki `(system, instance, db_name)` anahtar sayısı** (§7.4a 694'ün altında ısırmaz); **`topOps` taramasının gerçek maliyeti** (PK-prune ediliyor, ucuz olabilir).
- **F0.2 paritesi** — ham `norm_stmt` vs MV `db_stmt_hash` satır sayıları eşleştirilmedi.
- **Purge listesindeki eksikliğin kasıt mı olduğu** — `git blame` çalıştırılmadı.
- **Prod'da `db_statement_summary_5m` fiilen var mı** — `hasDBStmtHashCol` boot probe'una bağlı (`store.go:3023`, `:4076`); dış-Distributed + `cluster_name` unset kurulumda **tüm statement yüzeyi ham fallback'te** olabilir.
- **Çok-shard etkisi** — tüm SELECT'ler chc-0 üzerinden; MEMORY *"state tabloları shard bölünmesi"* riskinin `problems`/`DBSubjectID` okumalarına etkisi test edilmedi.
- **Motor panellerinin iç yüzeyleri** ve `/messaging`in kendi veri akışı taranmadı. ⚠ §6'nın backend ayağı **artık kısmen ölçüldü** (§6.4b — messaging MV ailesi izomorf); ölçülmemiş kalan yalnız `/api/endpoints/detail` + `/api/messaging/detail` **handler yapısı**.
- **Hiçbir gate çalıştırılmadı** (`tsc`/`vitest`/`go build`/`go test`/`make audit`); hiçbir sayfa tarayıcıda açılmadı, Playwright sürülmedi.

### 9.1 v1'in ÖLÇÜM HATALARI (v2'de düzeltildi) — yöntem dersi olarak burada durur

| v1 iddiası | Gerçek | Ders |
|---|---|---|
| *"Trace pivotu YOK (grep: `traceHref` 0 eşleşme)"* | **VAR** — `DatabaseDetail.tsx:25`, `:214-218`; üretici `dbTracesHref` | **Yokluk, tek sembol adı grep'iyle iddia edilmez.** MEMORY *"Gate tek-yazım kör noktası"* |
| *"`/traces` filtresi tek attr'a bakar → link boş liste getirir"* | **Tam tersi** — `pivotHref.ts:208-215` altı basamağı tek OR grubunda; v0.9.268'de tam bu gerekçeyle yazıldı | Kodun **kendi yorumunu** oku; v1'in "yazılmamış ikiz" dediği şey backend ikizinden **önce** gemiye gitmiş |
| *"URL'e konabilir mi → **HAYIR**, dört ölçülmüş gerekçe"* | Üçü **demo jeneratörünün elle yazdığı sabitler** (`main.go:552`, `mesh.go:206`) | Lokal ölçüm = **jeneratör tasarımı** olabilir; kategorik karar prod ölçümüne bağlanır |
| *"Yıkıcı olmayan ALTER: bedava değil ama açık"* | ⛔ **AÇIK DEĞİL** — AggregatingMergeTree sıralama anahtarına göre birleştirir; anahtar-dışı `instance` **keyfi değer** alır | "Bedava değil" ≠ "yapılabilir"; reçete er geç uygulanır |
| *"F2.1: bayt-bayt aynı çıktı"* | Üç `Stat` bayt-bayt **değil** (`StmtDetailDrawer`da `minWidth`/`textTransform` yok) | v1 §5.1'de "~" yazıp F2.1'de sertleştirmiş — **kendi içinde çelişki** |
| *"#5 bedava MV kazancı"* | Anahtar öneki prune etmiyor + pencere kapısı + parite | "MV'ye taşınabilir" ≠ "ucuz" |
| *"≥12 türetim noktası"* | 5 üretici + 2 önek okuyucu + 3 "kimlik değil" | Üretici / tüketici / etiket **ayrı sayılır** |
| *"staleTime 30s = CLAUDE.md ihlali"* | Kural **ES-cost** başlığı altında; bu bir CH okuması + serveCached + singleflight | Kuralın **gerekçesini** oku, metnini değil |
| *"'en sık' ekrana hiç ulaşamıyor"* | `/database` paneli için doğru; `/databases/slow-queries` **200 satır** çekiyor (`SlowQueries.tsx:100`) | İddiayı ölçtüğün **yüzeye** daralt |

⚠ `memory/feedback-audit-prescriptions-get-executed.md`: **bu dokümandaki her reçete er geç uygulanır — teşhis ile çare AYRI doğrulanmalıdır.**
