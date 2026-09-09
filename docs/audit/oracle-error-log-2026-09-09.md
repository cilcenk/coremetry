# Oracle hata tablosu → Problem üretimi — AUDIT

**Tarih:** 2026-09-09 · **Durum:** ONAY BEKLİYOR · **Kod yazılmadı.**

Kaynak: `<ŞEMA>.MCA_TERROR_LOG` (şema/tablo adı ayardan gelecek, koda gömülmeyecek).
Bankaya ait host adları ve alan adları bu depoya YAZILMAZ — örneklerde sentetik
karşılıkları kullanılıyor.

---

## 0. Yönetici özeti — beş bulgu

**B1. İstediğin hat zaten kurulu.** "Harici hata serisi → anomali → Problem →
kanıt" akışı Influx için çalışıyor: `internal/anomaly/external.go`
`ExternalScanner`. Oracle için ikinci bir tarayıcı YAZILMAYACAK. Yazılacak olan
bir **poller**: Oracle'ı okuyup hata sayısını `metric_points`'e `ext:<sorgu>`
adıyla yazan katman. Motorun sözleşmesi "bana dizi ver" değil, **"seriyi şu
tabloya yaz, ben okurum"** (`external.go:130-140`).

**B2. Sürücü kararı ölçüldü: go-ora.** `CGO_ENABLED=0` iki yerde sabit
(`Dockerfile:37-38`), çalışma zamanı `alpine:3.20` (musl). godror CGO + Oracle
Instant Client ister; Oracle Instant Client'ı musl için yayınlamaz. godror
seçmek = CGO'yu tüm binary için açmak + taban imajı değiştirmek + 35-80 MB.
"Tek binary, tek imaj" kısıtıyla doğrudan çelişir. **Operatörün tercihi doğru.**

**B3. Tüketim modeli: ingest ZORUNLU, tercih değil.** Anomali motoru seriyi
`metric_points`'ten OKUR; keyfi bir dizi kabul eden ihraç edilmiş bir imza yok
(`evaluateAnomaly` paket-özel, `anomaly/verdict.go:34`). Problem üretimi
istendiği için query-time federation (a) seçeneği hattı besleyemez.

**B4. 🔴 Prod'da log satırları CH `logs` tablosuna yazılırsa GÖRÜNMEZ.**
`logstore.Switchable` iki arka ucu BİRLEŞTİRMEZ, aralarında SEÇER
(`switchable.go:22-25, 65-107`). Prod ES arka ucunda koşuyor. CH `logs`'a
yazılan Oracle satırı prod log aramasında hiç çıkmaz. Bu, Aşama 2'nin
şeklini belirleyen kısıttır (§4).

**B5. Entity isteği ile mevcut desen çatışıyor.** Dış hat Problem öznesini
SENTETİK üretir (`ext:<kaynak>/<değerler>`, `external.go:340`). İstenen ise
trace'ten türetilen GERÇEK servis. Dedup anahtarı `(ruleID, service)` olduğu
için özne tikler arasında değişirse aynı sorun için ikinci Problem açılır (§6).

---

## 1. Sürücü: go-ora

| | go-ora | godror |
|---|---|---|
| CGO | gerekmez | **gerekir** (`Dockerfile:37-38` değişir) |
| Oracle Instant Client | gerekmez | **gerekir**, musl build'i YOK |
| Taban imaj | `alpine:3.20` aynen | Debian slim'e geçiş ya da glibc şimi |
| İmaj boyutu | +~0 | +35-80 MB |
| Protokol | TNS'in saf Go uygulaması | Oracle'ın kendi OCI'ı |

`database/sql` zaten kullanımda (`internal/chstore/scan.go`), depoda Oracle
sürücüsü yok — greenfield.

**AÇIK SORU O1 — operatörde:** bağlantı kullanıcı adı/şifre ile mi, yoksa
**Oracle Wallet / Kerberos** ile mi? go-ora TCP ve TCPS'i iyi taşır; cüzdan
zorunluysa karar yeniden açılır.

---

## 2. Tüketim modeli — periyodik ingest (b)

**Neden (a) query-time federation yetmez:** anomali motoru medyan+MAD
baseline'ını `metric_points` üzerinden okur (`external.go:132-140`), mevsimsel
baseline `ExternalSeasonal` ile CH'den gelir. Sorgu anında federasyon, geçmiş
seriyi hiçbir yerde tutmadığı için baseline üretemez.

**Ne ingest edilecek — iki ayrı akış, karıştırılmamalı:**

| Akış | Hedef | Neden |
|---|---|---|
| **Hata SAYACI serisi** | `metric_points`, metrik `ext:<sorgu>` | Motorun sözleşmesi (B1) |
| **Ham hata SATIRLARI** | kendi tablosu (§4) | Kanıt + log yüzeyi + trace pivotu |

Poller emsali `internal/influx/poller.go`: 5 s granül döngü + kaynak başına
`IntervalSec` (10-3600 s), lider kilidi `cache.NewLeaderHolder(...)`
(`main.go:1096-1099`), watermark bellek-içi ve **yalnız başarılı yazımda
ilerler** (`poller.go:431-437`).

**Oracle için watermark farkı:** Influx'ta watermark kalıcı değil (yeniden yazım
idempotent olduğu için zararsız). Oracle'da ham satırlar da yazılacağı için
watermark KALICI olmalı — `system_settings` blobunda kaynak+sorgu başına son
işlenen `MCA_ERR_TIMESTAMP`. Geç gelen kayıt için küçük overlap penceresi
(öneri: 2 dk, ayardan), tekrarı §4'teki ReplacingMergeTree yutar.

---

## 3. Oracle'a karşı sorgu disiplini

Repo kuralları buraya birebir uygulanır:

- **Yalnız SELECT.** Bind değişkeni (`:1`, `:2`); string concat YASAK.
  Grafana'daki `LIKE '<operationcode>'` kalıbı TAŞINMAYACAK.
- **Zaman aralığı zorunlu** + `FETCH FIRST :n ROWS ONLY`. Sınırsız tarama yok.
- Sorgu timeout'u (öneri 20 s), havuz üst sınırı, hata durumunda kaynak yalıtık
  kalır ve bir sonraki tikte yeniden denenir (Influx'ta retry/backoff YOK,
  aynı sözleşme).
- **Erişilemezken tarayıcı HİÇ KOŞMAZ** — `poller.go:304` (`st.LastError == ""`)
  invariantının birebir kopyası. Gerekçe kodda: erişilemez kaynağın serisini
  okumak sıfır-padli sahte "iyileşti" üretir. Bunun yerine bayat süpürücü
  Problem'i **"auto-resolved: source silent"** damgasıyla kapatır — sahte
  toparlanma değil, dürüst sessizlik. Operatörün "veri yokluğu ile hata yokluğu
  ayrı ele alınsın" şartı böylece karşılanır.

**AÇIK SORU O2 — operatörde, ÇALIŞTIRILMASI GEREKEN SORGULAR.** Oracle'a
erişimim yok; aşağıdakiler salt-okunur ve dar pencereli:

```sql
-- (1) Kod dağılımı + ayırt edici doluluk — jenerik kod listesini DOĞRULAR
SELECT MCA_ERR_CODE, COUNT(*) N,
       COUNT(MCA_ERR_EXTERNAL_CODE) EXT_DOLU,
       COUNT(DISTINCT MCA_ERR_EXTERNAL_CODE) EXT_DISTINCT,
       COUNT(DISTINCT MCA_ERR_MESSAGE) MSG_DISTINCT,
       COUNT(MCA_ERR_TRACEID) TRACE_DOLU,
       COUNT(DISTINCT MCA_ERR_SERVICE) OP_DISTINCT
FROM <ŞEMA>.MCA_TERROR_LOG
WHERE MCA_ERR_TIMESTAMP >= SYSTIMESTAMP - INTERVAL '24' HOUR
  AND MCA_ERR_TYPE = 'T'
GROUP BY MCA_ERR_CODE ORDER BY N DESC;

-- (2) Tabloda hangi tipler var (tip filtresi ayardan yönetilecek)
SELECT MCA_ERR_TYPE, COUNT(*) FROM <ŞEMA>.MCA_TERROR_LOG
WHERE MCA_ERR_TIMESTAMP >= SYSTIMESTAMP - INTERVAL '24' HOUR
GROUP BY MCA_ERR_TYPE ORDER BY 2 DESC;

-- (3) Index gerçeği — tam tablo taraması kabul edilemez
SELECT i.INDEX_NAME, c.COLUMN_POSITION, c.COLUMN_NAME, i.UNIQUENESS
FROM ALL_IND_COLUMNS c JOIN ALL_INDEXES i
  ON i.OWNER = c.INDEX_OWNER AND i.INDEX_NAME = c.INDEX_NAME
WHERE c.TABLE_OWNER = '<ŞEMA>' AND c.TABLE_NAME = 'MCA_TERROR_LOG'
ORDER BY i.INDEX_NAME, c.COLUMN_POSITION;

-- (4) Kolon tipleri — TZ dönüşümü ve tekillik anahtarı buna bağlı
SELECT COLUMN_NAME, DATA_TYPE, NULLABLE FROM ALL_TAB_COLUMNS
WHERE OWNER = '<ŞEMA>' AND TABLE_NAME = 'MCA_TERROR_LOG' ORDER BY COLUMN_ID;
```

(4) özellikle kritik: `MCA_ERR_TIMESTAMP` `TIMESTAMP` mi `TIMESTAMP WITH TIME
ZONE` mi olduğu UTC dönüşümünü belirler; tabloda bir sequence/ID kolonu varsa
idempotent yazımın anahtarı odur, yoksa kolon kombinasyonuna düşeriz (kırılgan).

**Eğer (3) `MCA_ERR_TIMESTAMP` üzerinde index göstermezse bu tasarım DURUR** —
dakikada bir tam tablo taraması kabul edilemez.

---

## 4. 🔴 Log entegrasyonu — mevcut şema bunu taşımıyor

**Kısıt:** `logstore.Store` 12 metotlu bir arayüz ve `Switchable` tek arka uca
delege ediyor (`switchable.go:65-107`). Üçüncü bir kaynağı arama sonuçlarına
KATMAK için birleştirici bir sarmalayıcı gerekir; `Page.NextCursor` arka-uç
sahipli opak token olduğu için (CH: `base64("ch|"+ns+"|"+rowKey)`, ES: sort
values) iki kaynak için TEK cursor üretmek bugünkü sözleşmede mümkün değil.

**Ek olarak:** CH `logs` tablosu düz `MergeTree` (`store.go:1921`), tekillik
anahtarı YOK; aynı satırı iki kez yazmak iki satır üretir ve ikisinin de
`id`'si aynı olur (React key çakışması). Ayrıca "kaynak" ayırt edici kolonu yok.

**ÖNERİ — Seçenek A: kendi tablosu + frontend'de birleştirme.**

Emsal zaten çalışıyor: Trace Detail'in Logs sekmesi, span event satırlarını
`origin` rozetiyle ES loglarıyla frontend'de birleştiriyor
(`Trace.tsx:949`, `LogTable.tsx:438`). Oracle satırları ÜÇÜNCÜ dizi olarak
aynı yoldan girer.

- CH tablosu `oracle_error_log`, `ReplacingMergeTree(version)`,
  `ORDER BY (op_code, err_code, ts, uniq_key)` → idempotent yazım çözülür.
- `origin` union'ı `'span-event' | 'oracle'` olur (`types.ts:2752`),
  `LogTable.tsx:438` tek eşitlik yerine eşlemeye döner (~8 satır).
- `/logs` sayfası için kendi ucu + filtreleri (operation.code, error.code,
  external_code, channel.code, request.id, error.type).
- TRACEID boş satırlar tabloda durur, log aramasında görünür, trace pivotu yok.

**Reddedilen Seçenek B:** `logstore.Store`'a birleştirici sarmalayıcı. 12 metot,
cursor semantiği tanımsız, iki arka uçta çift kod yolu. Maliyet/fayda tutmuyor.

**AÇIK SORU O3:** `/logs` sayfasında Oracle satırları ES loglarıyla aynı listede
mi görünsün (kaynak seçici gerekir), yoksa kendi sekmesinde mi? İkincisi çok
daha ucuz ve dürüst. Operatör kararı.

---

## 5. Alan eşlemesi

Eşleme ayardan yönetilir (hard-code değil):

| Oracle kolonu | Hedef | Not |
|---|---|---|
| `MCA_ERR_TIMESTAMP` | `timestamp` (unix ns) | O2/(4) sonucuna göre UTC'ye çevrilir |
| `MCA_ERR_SEVERITY` | `severityText` + `severityNumber` | OTel eşlemesi |
| `MCA_ERR_MESSAGE` | `body` | |
| `MCA_ERR_TRACEID` | `trace_id` | **normalize şart** (§altta) |
| `MCA_ERR_HOSTNAME` | `host.name` | |
| `MCA_ERR_INSTANCE_ID` | AÇIK — O4 | değerin ne olduğu görülmeden karar verilmez |
| `MCA_ERR_SERVICE` | `operation.code` | **service.name DEĞİL** |
| `MCA_ERR_CODE` | `error.code` | response kodu taksonomisi |
| `MCA_ERR_EXTERNAL_CODE` | `error.external_code` | |
| `MCA_ERR_TYPE` | `error.type` | |
| `MCA_ERR_CHANNELCODE` | `channel.code` | |
| `MCA_ERR_TASKCODE` | `task.code` | |
| `MCA_ERR_REQUESTID` | `request.id` | v0.10.566 kimlik hattıyla uyumlu |
| `MCA_ERR_CUSTOMERID` | `customer.id` | |
| `MCA_ERR_TELLERID` | `teller.id` | |
| `MCA_ERR_LOCATION` | `location` | |

**trace_id normalize — mevcut bir tuzak var.** CH log filtresi trace_id'yi
normalize ETMİYOR (`repo.go:5011`), ES yalnız `ToLower` yapıyor. OTLP yazımı
küçük harf hex üretir (`otlp/convert.go:485-490`). Büyük harfli veya tireli bir
Oracle TRACEID'i CH'de sessizce 0 satır döndürür. Normalize eden yardımcı
`normalizeHexID(v, 32)` VAR (`logstore/trace_fallback.go:211-234`) ama yalnız
dönen satırları doldurmak için kullanılıyor. **Oracle poller'ı bunu yazma
anında uygulamalı** ve geçmeyen değeri boş bırakıp SAYMALI (Influx'ta emsal:
"12/50 id geçersiz" — `influx/enrich.go:136-138`).

---

## 6. Problem üretimi

### 6.1 Gruplama anahtarı

Zorunlu boyutlar: **(operation.code, error.code, channel.code)**.
`error.code` her zaman anahtarda — BSA_028 ile başka bir kod asla aynı
Problem'de birleşmez.

Jenerik kod (ayardan, başlangıç `BSA_020`) ise **ikincil ayırt edici**, sırayla
ilk bulunan:
1. `error.external_code` (doluysa)
2. TRACEID'den bulunan trace'teki exception tipi / hatalı span adı
3. hiçbiri yoksa ayırt edici yok, Problem "jenerik kova" işaretlenir ve başlık
   bunu dürüstçe söyler

Jenerik OLMAYAN kodda ikincil ayırt edici EKLENMEZ (gereksiz parçalanma
olmasın); `external_code` yine kanıta yazılır.

**Boşluk: 2. basamak için okuma yolu YOK.** `trace_id IN (...)` ile filtreleyip
`ex_type` döndüren hazır bir chstore metodu bulunamadı; `ExceptionFilter`'da
`TraceID` alanı yok (`chstore/exception.go:106-129`). İki seçenek:
`SpanSummariesForTraces`'e `ex_type` kolonu eklemek (argMax ile), ya da yeni
sınırlı bir okuma yazmak. İkisi de `/clickhouse-schema` kapsamında.
⚠ `ex_type` kolonuna DOĞRUDAN yazılmaz — `exFragments(s.hasExCols)` üzerinden
(`chstore/exception.go:80-93`); dağıtık kurulumda ALTER atlanmış olabilir.

### 6.2 Entity — ÇATIŞMA ve öneri

Dış hat özneyi sentetik üretir: `ExternalSubject(kaynak, değerler)` →
`ext:<kaynak>/<v1>/<v2>` (`external.go:340`). Bu bilinçli: depo bir kez
veritabanı Problem'lerine gerçek görünen ama karşılığı olmayan bir ad yazdı,
on küsur yüzeyde servis linki olarak basıldı, tıklanınca boş sayfa açıldı.
Ders koda yorum olarak yazılı (`frontend/src/lib/problemSubject.ts:1-11`):
*çalışmayan bir bağlantı, bağlantı olmamasından kötüdür.*

İstenen ise trace'ten türetilen GERÇEK servis. **Bedeli dedup'ta:** açık Problem
araması `OpenProblemKey(ruleID, service)` (`chstore/problem.go:1424`). Servis
alanı trace çözümlemesinden gelirse ve bir tikte trace bulunup diğerinde
bulunmazsa özne değişir → **aynı mantıksal sorun için ikinci Problem**.

**ÖNERİ:** servis **açılışta bir kez** çözülür ve Problem ömrü boyunca
SABİTLENİR; sonraki tiklerde trace çözülemese bile özne değişmez. Çözülemezse
sentetik özne + "operasyon seviyesi, servis bilinmiyor" işareti. Uydurma servis
adı atanmaz.

**Hangi servis:** `SpanSummariesForTraces` (`chstore/spans_by_trace.go:47`) tek
çağrıda `RootService` VE `ErrorService` döner. **Öneri: `ErrorService`** (ilk
hatalı span'in servisi) — Oracle zaten hata sayıyor, kök servis çoğu zaman bir
gateway olur. Seçim Problem'de kanıtla birlikte görünür olur.

**AÇIK SORU O5:** `Kind` ne olsun? `external` (bedava korumalar: synthesizer
atlaması `rootcause_worker.go:546`, özne şeridi kaçışı
`problem_subject_lane.go:91`, kanıt paneli hazır) ama takım/cluster
zenginleştirmesi boş kalır. `service` (gerçek servise bağlanır, takım/cluster
gelir) ama sentetik özne yazılamaz. Melez: çözülürse `service`, çözülemezse
`external` — ama o zaman Kind tikler arası değişebilir; §6.2 sabitleme kuralı
bunu da kapatır. **Önerim: melez + açılışta sabitleme.**

### 6.3 Tetikleme

- Ham sinyal: hata sayısının dakika kovaları (`metric_points`, `ext:` metriği).
- Sabit tek eşik YOK — mevcut motor (medyan+MAD, `verdict.go:34`).
- Mutlak eşik **zaten motorda**: `MinAbsDelta` / `AbsFloor` / `FloorPct`
  (`anomaly.go:277-297`); dış seriler için varsayılan `MinAbsDelta = 5`
  (`external.go:46`). Operatörün "en az N hata" isteği yeni kod istemiyor.
- **Yeni anahtarda baseline yoksa: motor sessiz kalır** ("skip",
  `verdict.go:44-56`), `minSamples = 12` + dwell. Öğrenme penceresi geometrik
  değil **gözlenmiş** (`observedSpan`, `external.go:395`) — genç kaynağı sıfırla
  doldurup sahte anomali üretmez (v0.10.199 dersi). İstenen davranış bu.
- **Kod bazında hassasiyet:** `cfg.Metrics[metric]` üst-yazımı var
  (`external.go:359`), taşıyıcı sorgu başına `Thresholds`
  (`influx/settings.go:84-93`). ⚠ Kırılım **sorgu** düzeyinde, **grup değeri**
  düzeyinde değil: aynı sorgunun tüm serileri aynı eşiği paylaşır. "BSA_028
  farklı hassasiyet" istenirse o kod **kendi sorgusu** olarak tanımlanmalı.

### 6.4 Yaşam döngüsü

Mevcut mekanizma aynen: dwell + histerezis (açılma z ≥ criticalZ, kapanma
z ≤ resolveZ) + yön tutarlılığı; "sürüyor" tikinde satır **touch** edilir
(`external.go:290-304`) ki bayat süpürücü kapatmasın. Kaynak susarsa süpürücü
3 dk sonra dürüst gerekçeyle kapatır, ve süpürücünün kendi kör-nokta kapısı var
(`sweepIsTrustworthy`, `evaluator/tick_continuity.go`).

**Shadow modu:** `NewExternalScanner(store, nil)` — notifier nil verilince
Problem üretilir, alarm gitmez (`external.go:273-275`). Ayrı bir kip yazmaya
gerek yok, ayara bağlanır.

### 6.5 🔴 Problem seli — mevcut boşluk

`ExternalScanner.Scan` her seri için `apply` çağırır; **açılan Problem sayısına
üst sınır YOK** (`external.go:167`). Anomali kümeleme (`anomaly/clustering.go`,
`clusterMinMembers = 3`) var ama servis topolojisi gerektirdiği için `ext:`
öznelerini GÖRMÜYOR.

Oracle'da groupBy = (operation.code, error.code, channel.code) yüksek
kardinaliteli. Bu gerçek bir sel riski ve mevcut kodda karşılığı yok.

**Öneri:** tik başına açılış tavanı (ayardan, öneri 20) + tavan aşıldığında
"aynı operasyonda N kod birden" özet Problem'i. Bu, dış hattın tamamına fayda
sağlar, yalnız Oracle'a değil.

---

## 7. Dosya dosya plan

### Aşama 1 — datasource + bağlantı
| Dosya | İş |
|---|---|
| `internal/oracle/settings.go` (yeni) | `SourceConfig`/`QueryConfig`/`Settings`, `Normalize`, `LoadPersisted`/`SavePersisted`/`StartConfigRefresh` — `influx/settings.go` birebir emsali |
| `internal/chstore/oracle.go` (yeni) | `system_settings` anahtarı `oracle_sources` |
| `internal/oracle/client.go` (yeni) | go-ora `database/sql`, bind değişkenli SELECT, `FETCH FIRST`, timeout, `Test` → **200 + {ok:false}** |
| `internal/api/oracle_routes.go` (yeni) | GET/PUT/POST-test/status; `api.go` **tek satır** register |
| `main.go` | `oracle.New()` + `LoadPersisted` + `cfgRefresh.Add("oracle", …)` |
| `frontend/src/pages/settings/OracleTab.tsx` (yeni) + `oracleForm.ts` | `InfluxTab.tsx` emsali |
| Credential | `internal/secretref` DOĞRUDAN (sarmalayıcı gerekmez); GET maskeler, boş girdi saklıyı korur |

### Aşama 2 — alan eşlemesi + log
| Dosya | İş |
|---|---|
| `internal/chstore/oracle_error_log.go` (yeni) | `ReplacingMergeTree(version)` tablo + sınırlı okuma (`LIMIT` + `max_execution_time`) |
| `internal/oracle/poller.go` (yeni) | watermark (**kalıcı**), overlap, lider kilidi, `DropStats`/`SkipStats`, durum yayını |
| `internal/oracle/mapping.go` (yeni) | kolon→alan eşlemesi, TZ→UTC, `normalizeHexID` ile trace_id, geçersiz sayacı |
| `internal/api/oracle_logs_routes.go` (yeni) | Oracle satırı arama ucu (O3 kararına göre) |
| `frontend/src/lib/types.ts` | `origin: 'span-event' \| 'oracle'` |
| `frontend/src/components/LogTable.tsx` | rozet eşlemesi (~8 satır) |
| `frontend/src/pages/Trace.tsx` | üçüncü dizi olarak birleştirme (mevcut `sorted` spread'i) |

### Aşama 3 — Problem (shadow)
| Dosya | İş |
|---|---|
| `internal/oracle/counter.go` (yeni) | hata sayacını `metric_points`'e `ext:` metriği olarak yazar (OTLP dönüştürücüsünden geçer — `poller.go:419-428` emsali) |
| `main.go` | `ExternalScanner` hook'u: Oracle poller → `ExternalTarget` |
| `internal/oracle/enrich.go` (yeni) | kanıt: trace listesi, span özetleri, external_code dağılımı, kanal/instance dağılımı |
| `internal/chstore/spans_by_trace.go` | `ex_type` kolonu (jenerik kod 2. basamağı) — `/clickhouse-schema` gerekir |
| `internal/anomaly/external.go` | tik başına açılış tavanı (§6.5) |
| `internal/oracle/grouping.go` (yeni) | gruplama anahtarı + jenerik kod listesi + ignore listesi — SAF, testlerin hedefi |

### Testler (operatörün listesi birebir)
`internal/oracle/mapping_test.go` (eşleme, trace_id normalize, TZ) ·
`poller_test.go` (watermark, overlap, idempotent) ·
`grouping_test.go` (jenerik+external / jenerik+exception / jenerik+hiçbiri /
jenerik olmayan → ikincil EKLENMEZ; BSA_020 ve BSA_028 asla birleşmez) ·
`lifecycle_test.go` (ikinci tetiklemede yeni Problem açılmaz; akış kesilince
kapanmaz).

---

## 8. Açık sorular — operatörde

| # | Soru | Etki |
|---|---|---|
| O1 | Bağlantı şifre mi, Wallet/Kerberos mı? | sürücü kararı |
| O2 | §3'teki dört SQL'in çıktısı | jenerik kod listesi, index, TZ, tekillik anahtarı |
| O3 | `/logs`'ta ortak liste mi, ayrı sekme mi? | Aşama 2 kapsamı |
| O4 | `MCA_ERR_INSTANCE_ID` gerçekte ne? | `service.instance.id` mi `k8s.pod.name` mi |
| O5 | Problem `Kind`: melez mi, saf external mı? | entity ve takım zenginleştirmesi |
| O6 | Sel tavanı 20 uygun mu? | §6.5 |

**Onaydan önce implementasyona geçilmeyecek.**
