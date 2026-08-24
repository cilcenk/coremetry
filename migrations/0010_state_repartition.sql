-- 0010_state_repartition.sql — problems + anomaly_events'ten PARTITION BY'ı
-- söker. v0.9.1335. OPERATÖR UYGULAR. Sihirbaz/boot ASLA koşmaz.
--
-- ============================================================
-- GEREKÇE — ÖLÇÜLEN BUG (Kural P1)
-- ============================================================
-- Her iki tablo da:
--     ENGINE = ReplacingMergeTree(version)
--     PARTITION BY toDate(started_at)
--     ORDER BY id
-- started_at PARTITION BY'da ama ORDER BY'da DEĞİL. ReplacingMergeTree
-- yalnız partition İÇİNDE dedup eder; bir id'nin started_at'i başka bir
-- güne kayarsa eski satır ÖLÜMSÜZ bir kopya olur — `OPTIMIZE … FINAL`
-- bile toplayamaz. Doğruluğu ayakta tutan tek şey `SELECT … FINAL`'in
-- sorgu anında partition'lar arası birleştirmesidir, yani bir SUNUCU
-- AYARI. ÖLÇÜLDÜ (CH 24.8.14, scratch tablo, aynı id iki partition'da):
--
--     OPTIMIZE … FINAL sonrası fiziksel satır          : 2 (birleşmedi)
--     SELECT … FINAL (varsayılan)                      : 1  → FRESH
--     SELECT … FINAL SETTINGS do_not_merge_across_
--       partitions_select_final = 1                    : 2  → STALE+FRESH
--
-- Üçüncü satır asıl tehlike: o vida (yaygın bir FINAL hızlandırma ayarı)
-- kurulduğu an bayat bir Problem/anomali satırı servis edilebilir.
-- started_at kozmetik DEĞİL — P1 açık-saat eşiğini ve effectiveSeverity'nin
-- yaş tabanlı yükseltmesini besliyor; bayat satır yaşlanmış bir problemi
-- sessizce GERİ indirir.
--
-- BÖLÜNMENİN BUGÜNKÜ HÂLİ (lokal chc-0, 2026-08-24, 0009 SONRASI):
--     problems        4819 id'nin 21'i  >1 gün-partition'ında
--     anomaly_events   186 id'nin 32'si >1 gün-partition'ında
--
-- v0.9.1306 kaymayı TOPOLOJİYE bağlamıştı (state tabloları shard-yerel,
-- taşıma SELECT'i yanlış shard'a düşüyor). O mekanizma 0009 ile KAPANDI
-- ve kapandığı ÖLÇÜLDÜ: birleştirmeden bu yana İKİ tabloda da SIFIR yeni
-- bölünme. Ama teşhis kapsayıcı değildi — topolojiden BAĞIMSIZ iki dal
-- duruyor ve ikisi de bugün canlı:
--   • anomaly_events — UpsertAnomalyEvents'in taşıma SELECT'i hata
--     verirse `prev` boş kalır (bilinçli yumuşak düşüş) → o tikin TÜM
--     olayları "ilk görülme" gibi yazılır, started_at TAZELENİR. Ayrıca
--     30 günlük TTL satırı düşürdükten sonra aynı parmak izi yeniden
--     ateşlerse started_at yeni pencereden gelir.
--   • problems — problem id'si birçok dedektörde DETERMİNİSTİK
--     (fatalExcProblemID, capacityProblemID, runtimeProblemID,
--     sharedBurstProblemID, `anomaly-auto:<fp>:<servis>`). KAPANAN bir
--     problem yeniden AÇILDIĞINDA aynı id'yi geri alır ama açılış dalı
--     started_at'i o anki now/FirstSeen ile yazar. Kapanışla yeniden
--     açılış arasına bir gece girdiği an ikinci partition doğar.
--     (Rastgele id üreten ana kural yolu bu sınıfa girmez.)
--
-- ============================================================
-- HEDEF TASARIM — PARTITION BY tuple() (yani hiç)
-- ============================================================
--     ENGINE = ReplicatedReplacingMergeTree('<prefix>/state/<ad>',
--                                           '{shard}-{replica}', version)
--     ORDER BY id                       -- DEĞİŞMEZ, dedup anahtarı
--     (anomaly_events) TTL toDate(started_at) + INTERVAL 30 DAY
--
-- Neden id-hash kovası DEĞİL: Kural P1'i mekanik olarak geçerdi ama
-- hiçbir şey kazandırmaz (zaman budaması yine gider), aynı göç maliyetini
-- öder ve küçük bir tabloyu gereksizce parçalar.
-- Neden "started_at'i değişmez kıl" DEĞİL: "hiçbir yazıcı bu kolonu bir
-- daha yeniden yazmayacak" iddiası KANITLANAMAZ — v0.9.1306 tam olarak
-- bu iddiayı sicile yazdı ve YANLIŞ çıktı.
--
-- BEDELİ ÖLÇÜLDÜ:
--   • Retention: SIFIR. EnforceRetention yalnız spans/logs/metric_points/
--     profiles/exemplars/span_links{,_reverse} partition'larını DROP
--     ediyor; bu iki tabloya HİÇ dokunmuyor (retention_enforce.go). Yani
--     partition'lar bugün zaten hiçbir şey temizlemiyordu.
--   • Partition budaması: tek etkilenen okuma GetNoisyRules'un
--     `started_at >=/<` penceresi — zaten `FINAL` ile tüm tabloyu okuyan,
--     max_execution_time=25'lik bir rapor sorgusu. Üstelik FINAL bugün
--     ZATEN partition'lar arası birleştirmek zorunda; partition'ı sökmek
--     FINAL'i UCUZLATIR.
--   • anomaly_events'in TTL'i artık partition-hizalı değil, SATIR
--     düzeyinde (merge sırasında) uygulanır — root_cause_hypotheses ile
--     aynı şekil. problems'in TTL'i YOKTU ve YOK kalıyor.
--
-- ============================================================
-- ÖNKOŞULLAR
-- ============================================================
-- P1. 0009 (state birleştirme) UYGULANMIŞ olmalı. Bu dosyanın üreticisi
--     ZK yolunun `/state/<ad>` biçiminde olduğunu varsayar. Uygulanmadıysa
--     ADIM 1 gürültüyle durur (`REPLICA_ALREADY_EXISTS`) — sessiz devam
--     YOK. Önce 0009'u koştur.
-- P2. Uygulama v0.9.1335+ olmalı. Eski bir imaj zararsızdır (CREATE IF
--     NOT EXISTS var olan tabloya dokunmaz) ama boot teşhisi ve testler
--     yalnız 1335+'te var.
-- P3. TEK DÜĞÜMLÜ kurulum: `ON CLUSTER uptrace_all` cümlelerini ve ADIM
--     0'ı çıkar; gerisi aynen geçerlidir.
-- P4. Küme adı `uptrace_all` token'ı ile yazılı. Başka kümede:
--       sed -i 's/uptrace_all/<küme adın>/g' 0010_state_repartition.sql
--     Lokal dağıtık kümede küme adı `coremetry`.
--
-- ============================================================
-- 0009'DAN AYRILAN İKİ NOKTA — ikisi de bilinçli
-- ============================================================
-- D1. **INSERT tüm kümede TEK BİR node'da koşar** (0009'da her shard'ın
--     bir node'unda). Sebep: 0009'un KENDİSİ. Birleştirmeden sonra state
--     tabloları TEK replikasyon grubunda; bir node'a yazmak dördüne de
--     replike olur. İkinci bir node'da koşturmak gereksiz iş ve
--     `_old`→yeni yakalamasında çift tur demektir. (Bu tablolar
--     ReplacingMergeTree olduğu için çift yazım veri BOZMAZ — id'ye göre
--     toplanır — ama 0009'un T1 uyarısını buraya olduğu gibi taşımak
--     YANLIŞ olurdu.)
-- D2. **İki AŞAMA var.** 0009'da yeni tablo NİHAİ ZK yolunu doğrudan
--     alabiliyordu (`/state/<ad>` boştu). Burada o yolu ESKİ tablonun
--     kendisi tutuyor ve RENAME znode'u TAŞIMAZ. Bu yüzden AŞAMA A yeni
--     tabloyu geçici bir yola (`/state/<ad>_repart`) kurar; kanonik yol
--     ancak `_old` düşürüldükten sonra boşalır ve AŞAMA B onu geri alır.
--     AŞAMA B'nin gerekçesi ve arada geçerli olan TEK kısıt için "ARA
--     DURUM" başlığına bak — atlanacak bir adım değildir.
--
-- ============================================================
-- ARA DURUM (AŞAMA A bitti, AŞAMA B henüz koşmadı)
-- ============================================================
-- Bu pencerede canlı `problems` tablosu `/state/problems_repart`
-- yolundadır. Uygulama açısından sonucu TEK ve DAR:
--   • Var olan tablolara HİÇBİR etkisi yok (CREATE IF NOT EXISTS zaten
--     elenir, okuma/yazma yolu ZK yolunu hiç anmaz).
--   • AMA useUnifiedStatePath (state_replication.go) kurulumu "göç
--     ÖNCESİ" sayar. Sonuç: o pencerede bir node'da EKSİK olan ya da
--     YENİ bir sürümle EKLENEN bir state tablosu ESKİ (`{shard}`) yola
--     kurulur — yani 0009'un onardığı bölünme o TEK tablo için geri gelir.
-- Dolayısıyla ara durumda GEÇERLİ TEK KISIT:
--   → CH kümesine node EKLEME ve YENİ state tablosu getiren bir sürümü
--     DEPLOY ETME. Etmen gerekiyorsa önce AŞAMA B'yi koştur.
-- Durumu boot logundan doğrula:
--     [chstore] state ZK yolu probe'u: N tablo gözlendi (X birleşik, Y eski)
--   AŞAMA A sonrası Y=2 beklenir; AŞAMA B sonrası Y=0.
--
-- ============================================================
-- `_old` NE ZAMAN DÜŞER — 0009'DAN DAHA TEMKİNLİ OL
-- ============================================================
-- Operatör 0009'dan sonra `_old` tablolarını HIZLA düşürdü ve bu doğruydu:
-- orada yanlış sonuç ANINDA görünürdü (satır sayısı tutmaz, Inbox boş
-- gelir). BURADA DEĞİL. 0010 dedup DAVRANIŞINI değiştiriyor; yanlış bir
-- sonuç ancak bir id'nin started_at'i kaydığında — yani GÜNLER sonra —
-- yüzeye çıkar. `_old`'u en az bir tam retention/triyaj döngüsü (öneri:
-- 7 gün) tut ve ADIM 4'ün doğrulamasını o süre boyunca ARA ARA tekrarla.
-- ADIM 5 (DROP) koşulduktan sonra geri alma YOKTUR.
--
-- ============================================================
-- GERİ ALMA
-- ============================================================
-- ADIM 5'ten ÖNCE her an, tablo başına tek ifade:
--   RENAME TABLE problems TO problems_repart, problems_old TO problems
--     ON CLUSTER uptrace_all;
--   RENAME TABLE anomaly_events TO anomaly_events_repart,
--                anomaly_events_old TO anomaly_events ON CLUSTER uptrace_all;
-- Eski tablo eski şemasıyla ve KANONİK ZK yoluyla hiç bozulmadan durur;
-- ara durum kısıtı da anında kalkar.
--
-- ============================================================
-- SQL KONSOLU (/admin/clickhouse) NOTU — 0009 dersi
-- ============================================================
-- Konsolun çoklu-ifade denetimi TIRNAK İÇİNDEKİ noktalı virgülü de ifade
-- ayracı sayar. ADIM 1'in üreticisini konsolda koşacaksan sondaki
-- `FORMAT TSVRaw;` satırını ve `|| ';'` parçasını ÇIKAR — son satır
-- `       ) AS ddl` olsun. Üretilen DDL'i ÇALIŞTIRMAK yine
-- clickhouse-client işidir (konsol readonly=2).


-- ============================================================
-- ADIM 0 — ÖN KONTROL (hiçbir şey değiştirmez; konsoldan koşulabilir)
-- ============================================================

-- 0a. Kaç host var, hepsi ayakta mı?
SELECT hostName(), getMacro('shard') AS shard, getMacro('replica') AS replica
FROM clusterAllReplicas('uptrace_all', system.one)
ORDER BY hostName();

-- 0b. 0009 UYGULANMIŞ MI (önkoşul P1)? İki satır beklenir ve zk_path
--     `/state/` içermelidir. `/{shard}/` görüyorsan DUR, önce 0009.
SELECT name, engine, splitByString(char(39), engine_full)[2] AS zk_path,
       partition_key, sorting_key, total_rows
FROM system.tables
WHERE database = currentDatabase() AND name IN ('problems', 'anomaly_events')
ORDER BY name;

-- 0c. Dört host da AYNI satır sayısını görüyor mu (0009'un teyidi)?
--     Farklıysa göç yarım kalmış demektir — DUR.
SELECT hostName(), count() AS problems
FROM clusterAllReplicas('uptrace_all', currentDatabase(), problems)
GROUP BY hostName() ORDER BY hostName();

-- 0d. BUG'IN BUGÜNKÜ BOYUTU — göç sonrası bu sorgu 0 döndürmeli.
--     (FINAL YOK: FINAL zaten kopyaları gizler, ölçmek istediğimiz
--     FİZİKSEL durum.)
SELECT 'problems' AS tbl, uniqExact(id) AS ids, countIf(np > 1) AS split_ids
FROM (SELECT id, uniqExact(toDate(started_at)) AS np FROM problems GROUP BY id)
UNION ALL
SELECT 'anomaly_events', uniqExact(id), countIf(np > 1)
FROM (SELECT id, uniqExact(toDate(started_at)) AS np FROM anomaly_events GROUP BY id);

-- 0e. KUSURUN CANLI KANITI — aynı sorgu, tek ayar farkı. İkinci sayı
--     birinciden BÜYÜKSE bugün servis edilen doğruluk o ayara asılıdır.
SELECT count() AS rows_default FROM problems FINAL;
SELECT count() AS rows_no_merge FROM problems FINAL
SETTINGS do_not_merge_across_partitions_select_final = 1;


-- ============================================================
-- AŞAMA A · ADIM 1 — `_repart` TABLOLARINI ÜRET VE KUR
-- ============================================================
-- ÜRETİCİ. Şemayı ELLE yazma: bu sorgu iki tablonun CANLI DDL'ini alır,
-- adına `_repart` ekler, ON CLUSTER enjekte eder, ZK yolunun sonuna
-- `_repart` koyar ve ` PARTITION BY <anahtar>` cümlesini SÖKER. Çıktıyı
-- GÖZDEN GEÇİR, sonra olduğu gibi koştur. Böylece kolon listesi, CODEC'ler
-- ve TTL store.go ile ıraksayamaz.
--
-- Tek bir node'da koşar (ON CLUSTER kümeye dağıtır).
--
-- ⚠ DOĞRULAMA: 2 satır çıkmalı; her satırda `_repart` GEÇMELİ ve
--   `PARTITION BY` GEÇMEMELİ. Geçiyorsa üreticinin replaceOne'ı tutmamış
--   demektir — DUR, elle incele.

SELECT replaceOne(
         replaceOne(
           replaceOne(create_table_query,
                      concat('CREATE TABLE ', database, '.', name),
                      concat('CREATE TABLE ', database, '.', name,
                             '_repart ON CLUSTER uptrace_all')),
           concat('/state/', name, '\', \''),
           concat('/state/', name, '_repart\', \'')),
         concat(' PARTITION BY ', partition_key, ' '),
         ' '
       ) || ';' AS ddl
FROM system.tables
WHERE database = currentDatabase()
  AND name IN ('problems', 'anomaly_events')
ORDER BY total_rows ASC, name ASC
FORMAT TSVRaw;


-- ============================================================
-- AŞAMA A · ADIM 2+3 — TABLO BAŞINA: KOPYALA, SONRA TAKAS ET
-- ============================================================
-- Tablo başına sırayla, bir bloğu bitirmeden sonrakine GEÇME:
--   (2)  INSERT — kümede TEK BİR node'da (bkz. D1).
--   (3)  RENAME — tek node'da, ON CLUSTER ile. Atomik takas.
--   (3b) yakalama — (2) ile (3) arasında uygulamanın eski tabloya yazdığı
--        satırları taşır. ReplacingMergeTree'de BEDAVA: aynı satırı
--        tekrar yazmak zararsız, id'ye göre toplanır. (0009'un ADIM 3c
--        anti-join'i BURADA GEREKSİZ — o, MergeTree beşlisi içindi.)
--   (3c) SAĞLAMA — satır sayısı ve bölünme sayısı.

------------------------------------------------------------- 1 anomaly_events
INSERT INTO anomaly_events_repart SELECT * FROM anomaly_events;
RENAME TABLE anomaly_events TO anomaly_events_old,
             anomaly_events_repart TO anomaly_events ON CLUSTER uptrace_all;
INSERT INTO anomaly_events SELECT * FROM anomaly_events_old;

SELECT (SELECT count() FROM anomaly_events_old FINAL) AS before_final,
       (SELECT count() FROM anomaly_events FINAL)     AS after_final,
       (SELECT countIf(np > 1) FROM (SELECT id, uniqExact(toDate(started_at)) AS np
                                     FROM anomaly_events GROUP BY id)) AS split_ids_now;
-- BEKLENEN: after_final = before_final  VE  split_ids_now = 0.
-- split_ids_now yalnız "toDate(started_at)" ifadesinin artık partition
-- OLMADIĞINI değil, aynı id'nin tek satıra indiğini de gösterir.

------------------------------------------------------------- 2 problems
INSERT INTO problems_repart SELECT * FROM problems;
RENAME TABLE problems TO problems_old,
             problems_repart TO problems ON CLUSTER uptrace_all;
INSERT INTO problems SELECT * FROM problems_old;

SELECT (SELECT count() FROM problems_old FINAL) AS before_final,
       (SELECT count() FROM problems FINAL)     AS after_final,
       (SELECT countIf(np > 1) FROM (SELECT id, uniqExact(toDate(started_at)) AS np
                                     FROM problems GROUP BY id)) AS split_ids_now;


-- ============================================================
-- AŞAMA A · ADIM 4 — DOĞRULAMA (ADIM 5'in ÖNKOŞULU)
-- ============================================================

-- 4a. ASIL İDDİA: ayar açık/kapalı AYNI sayı. Bu, ADIM 0e'de farklı olan
--     çiftin artık eşitlenmesidir — düzeltmenin tek cümlelik kanıtı.
SELECT count() AS rows_default FROM problems FINAL;
SELECT count() AS rows_no_merge FROM problems FINAL
SETTINGS do_not_merge_across_partitions_select_final = 1;
SELECT count() AS rows_default FROM anomaly_events FINAL;
SELECT count() AS rows_no_merge FROM anomaly_events FINAL
SETTINGS do_not_merge_across_partitions_select_final = 1;

-- 4b. Dört host da aynı sayıyı görüyor mu (0009'un kazanımı korundu mu)?
SELECT hostName(), count() AS problems
FROM clusterAllReplicas('uptrace_all', currentDatabase(), problems)
GROUP BY hostName() ORDER BY hostName();

-- 4c. Şema gerçekten değişti mi? partition_key BOŞ, sorting_key 'id'
--     olmalı; anomaly_events'in TTL'i DURMALI.
SELECT name, partition_key, sorting_key,
       splitByString(char(39), engine_full)[2] AS zk_path
FROM system.tables
WHERE database = currentDatabase()
  AND name IN ('problems', 'anomaly_events', 'problems_old', 'anomaly_events_old')
ORDER BY name;

-- 4d. UYGULAMA TARAFI: Inbox açılıyor mu, açık problem sayısı badge ile
--     tutuyor mu, /anomalies satır getiriyor mu. Bu adım SQL değil — göz.


-- ============================================================
-- AŞAMA A · ADIM 5 — `_old`'U DÜŞÜR (GERİ DÖNÜŞÜ YOK)
-- ============================================================
-- EN AZ 7 GÜN BEKLE (yukarıdaki "`_old` ne zaman düşer" başlığı).
-- AŞAMA B'yi koşacaksan bu adımla AYNI oturumda koş: kanonik ZK yolu
-- ancak burada boşalır.

DROP TABLE IF EXISTS anomaly_events_old ON CLUSTER uptrace_all SYNC
SETTINGS max_table_size_to_drop = 0, max_partition_size_to_drop = 0;
DROP TABLE IF EXISTS problems_old ON CLUSTER uptrace_all SYNC
SETTINGS max_table_size_to_drop = 0, max_partition_size_to_drop = 0;
-- SYNC + boyut koruması bypass'ı: v0.8.190'da bir DROP, CH'nin 50 GB
-- koruma eşiğine takılıp boot'u crash-loop'a sokmuştu; SYNC olmadan da
-- znode temizlenmeden gelen CREATE "Replica already exists" verir.


-- ============================================================
-- AŞAMA B — KANONİK ZK YOLUNU GERİ AL
-- ============================================================
-- ADIM 5'ten HEMEN SONRA. Şekil AŞAMA A'nın birebir aynısı; tek fark,
-- üretilen tablonun ZK yolunun artık `_repart`SIZ olması (yol boşaldı).
-- Bittiğinde boot logu `... (N birleşik, 0 eski)` demeli.
--
-- ⚠ Bu aşamada canlı tablo ZATEN doğru şemada; kopyalanan veri de
--   doğrulanmış veridir. Yine de `_pathfix_old` yedeği bırakılır ve ayrı
--   bir adımda düşürülür — aynı disiplin.

-- B1. ÜRETİCİ: `_pathfix` tablosu, ZK yolu KANONİK (`/state/<ad>`).
SELECT replaceOne(
         replaceOne(create_table_query,
                    concat('CREATE TABLE ', database, '.', name),
                    concat('CREATE TABLE ', database, '.', name,
                           '_pathfix ON CLUSTER uptrace_all')),
         concat('/state/', name, '_repart\', \''),
         concat('/state/', name, '\', \'')
       ) || ';' AS ddl
FROM system.tables
WHERE database = currentDatabase()
  AND name IN ('problems', 'anomaly_events')
ORDER BY total_rows ASC, name ASC
FORMAT TSVRaw;
-- DOĞRULAMA: 2 satır; her birinde `/state/<ad>', '` geçmeli, `_repart'`
-- GEÇMEMELİ, `PARTITION BY` GEÇMEMELİ (kaynak zaten partition'sız).

-- B2. Kopyala + takas + yakalama (tablo başına, sırayla).
INSERT INTO anomaly_events_pathfix SELECT * FROM anomaly_events;
RENAME TABLE anomaly_events TO anomaly_events_pathfix_old,
             anomaly_events_pathfix TO anomaly_events ON CLUSTER uptrace_all;
INSERT INTO anomaly_events SELECT * FROM anomaly_events_pathfix_old;

INSERT INTO problems_pathfix SELECT * FROM problems;
RENAME TABLE problems TO problems_pathfix_old,
             problems_pathfix TO problems ON CLUSTER uptrace_all;
INSERT INTO problems SELECT * FROM problems_pathfix_old;

-- B3. DOĞRULAMA: zk_path artık `_repart`sız, satır sayısı korunmuş.
SELECT name, partition_key, sorting_key,
       splitByString(char(39), engine_full)[2] AS zk_path
FROM system.tables
WHERE database = currentDatabase()
  AND name IN ('problems', 'anomaly_events')
ORDER BY name;

SELECT (SELECT count() FROM problems_pathfix_old FINAL) AS before_final,
       (SELECT count() FROM problems FINAL)             AS after_final;

-- B4. Uygulamayı bir kez yeniden başlat (ya da bir sonraki deploy'u bekle)
--     ve boot logunda `(N birleşik, 0 eski)` gördüğünü DOĞRULA. Ancak
--     ondan sonra:
DROP TABLE IF EXISTS anomaly_events_pathfix_old ON CLUSTER uptrace_all SYNC
SETTINGS max_table_size_to_drop = 0, max_partition_size_to_drop = 0;
DROP TABLE IF EXISTS problems_pathfix_old ON CLUSTER uptrace_all SYNC
SETTINGS max_table_size_to_drop = 0, max_partition_size_to_drop = 0;
