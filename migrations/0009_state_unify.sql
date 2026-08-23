-- 0009_state_unify.sql — state tablolarını TEK replikasyon grubuna taşır.
-- v0.9.1308. OPERATÖR UYGULAR. Sihirbaz boot'ta ASLA koşmaz (ev kuralı:
-- ON CLUSTER DDL'i N pod yarıştırırsa kuyruk tıkanır — v0.9.613).
--
-- ============================================================
-- GEREKÇE — ÖLÇÜLEN BUG
-- ============================================================
-- Küme kipinde uygulama HER Replicated tabloyu `<prefix>/{shard}/<ad>`
-- ZK yoluna kuruyordu. Telemetri için doğru: her shard kendi dilimini
-- tutar, Distributed sarmalayıcı okurken birleştirir. State tabloları
-- (problems, alert_rules, users, system_settings, …) ise shard
-- kayıtlarında (defaultShardPolicy / highVolumeTables) YOK, yani
-- Distributed sarmalayıcıları da yok. Sonuç: her shard AYRI bir
-- replikasyon grubu; uygulama hangi host'a bağlanırsa onun dilimini
-- görür ve veri SESSİZCE bölünür.
--
--   PROD (uptrace_all, 4 host = 2 shard × 2 replica), 2026-08-23:
--     problems  633.236 satır (bp01/bp02)  vs  4.169 (bp03/bp04)
--     → AÇIK problemlerin TAMAMI küçük shard'da; operatör hangi host'a
--       düşerse farklı bir Inbox görüyor.
--     alert_rules şans eseri dört host'ta da 8 (kurallar boot'ta
--       her podun yazdığı built-in'ler).
--   LOKAL (chc-0 shard=01 / chc-1 shard=02), 2026-08-23:
--     problems 4631/191 · incident_events 10226/149 · incidents 2894/57
--     incident_problems 4460/57 · log_templates 1436/256
--     anomaly_events 222/60 · service_metadata 99/98 · audit_log 53/0
--     ai_calls 29/0 · exception_groups 25/10 · root_cause_hypotheses 18/0
--     notification_log 12/0 · dashboards 10/0 · alert_rules 8/0
--     runbooks 6/0 · ai_feedback 3/0 · system_settings 3/0
--     users 1/0 · events 1/0 · rca_verdicts 1/0 — İSTİSNASIZ AYRIŞIK.
--
-- ============================================================
-- HEDEF TASARIM
-- ============================================================
-- Ayrı bir küme TANIMI açılmaz; `uptrace_all` içinde kalınır. Değişen
-- tek şey ZK koordinasyon yolu ve replika adı:
--
--   ENGINE = ReplicatedReplacingMergeTree(
--     '<prefix>/state/<ad>',   -- {shard} YOK → dört host TEK grupta
--     '{shard}-{replica}',     -- her node benzersiz
--     version)
--
-- `{shard}-{replica}` bilinçli: prod'da `{replica}` makrosunun shard
-- başına TEKRAR ediyor olması ihtimal (doğrulanmadı). Tek grupta iki
-- host aynı replika adını iddia ederse ikincisi REPLICA_ALREADY_EXISTS
-- alır. Bu yazım her iki makro düzeninde de benzersizdir.
--
-- TELEMETRİ TABLOLARI DEĞİŞMEZ. spans/logs/metric_points/profiles/
-- exemplars/span_links* ve tüm MV'ler `{shard}` yolunda KALIR; onların
-- shard'lı olması tasarımın kendisi.
--
-- ============================================================
-- UYGULAMA TARAFI — NE ZAMAN DEVREYE GİRER
-- ============================================================
-- v0.9.1308 uygulaması state tablosunun ZK yolunu KODDAN değil
-- KÜMEDEN okur (internal/chstore/state_replication.go):
--   • tablo kümede eski yolda görülüyorsa → eski yolu kullanır,
--   • birleşik yolda görülüyorsa → birleşik yolu kullanır,
--   • hiç yoksa → kurulumun kuşağına bakar.
-- Yani bu göç KENDİLİĞİNDEN tamamlanır: RENAME bittiği an dört host da
-- birleşik yolu raporlar, uygulama bir sonraki boot'ta ONU görür.
-- Çevrilecek bir bayrak, ikinci bir deploy YOKTUR.
--
-- ÖNKOŞUL: göçe başlamadan ÖNCE v0.9.1308 (veya üstü) deploy edilmiş
-- olmalı. Daha eski bir imaj, RENAME'den sonra eski yollu tabloları
-- birleşiklerin YANINA kurmaya çalışır ve METADATA_MISMATCH alır.
--
-- ============================================================
-- TUZAKLAR — okumadan koşma
-- ============================================================
-- T1. ADIM 2'nin INSERT'ü her shard'ın YALNIZ BİR node'unda koşar.
--     İki replikada da koşarsa append-only tablolar (ai_calls,
--     audit_log, incident_events, monitor_results, notification_log)
--     satırlarını İKİYE KATLAR — bunlar MergeTree, ReplacingMergeTree
--     değil, FINAL onları toplamaz.
-- T2. ADIM 2 ile ADIM 3 arasında uygulama eski tabloya yazmaya devam
--     eder; o saniyelerde gelen satırlar `_old`'da kalır. Bu yüzden
--     ikisi TABLO BAŞINA ardışık koşulur, hepsi için toplu değil.
--     ReplacingMergeTree tablolarında yakalama güvenlidir (ADIM 3b);
--     MergeTree beşlisinde DEĞİL — orada saniyelik boşluk kabul edilir.
-- T3. `_unified` tablosunun ZK yolu ORİJİNAL adla kurulur
--     (`/state/problems`, `/state/problems_unified` DEĞİL) — RENAME'den
--     sonra uygulamanın hesapladığı yolla birebir aynı olması için.
--     ADIM 1'in üretici sorgusu bunu zaten yapar; elle yazma.
-- T4. Göç iki kez koşarsa ADIM 1 REPLICA_ALREADY_EXISTS ile durur.
--     Bilinçli: sessiz devam etmektense gürültülü durmak.
-- T5. `_old` ve `_unified` sonekleri uygulamanın probe'u tarafından
--     BİLİNÇLİ görmezden gelinir. Yedeği başka bir adla bırakma.
-- T6. ADIM 1'in ürettiği DDL, o kümenin O ANKİ şemasıdır. Sürüm
--     atlanmış bir kurulumda kolon eksikse ADIM 2'nin `SELECT *`'ı
--     gürültülü hata verir — sessiz veri kaybı değil.
--
-- ============================================================
-- GERİ ALMA
-- ============================================================
-- ADIM 5'ten (DROP) ÖNCE her an, tablo başına tek ifade:
--   RENAME TABLE <t> TO <t>_unified, <t>_old TO <t> ON CLUSTER uptrace_all;
-- Eski tablo eski ZK yolunda hiç bozulmadan durur; uygulama bir sonraki
-- boot'ta yine onu görür ve eski davranışa döner.
-- ADIM 5 koşulduktan sonra geri alma YOKTUR — o yüzden ADIM 4'ün
-- doğrulaması ADIM 5'in ÖNKOŞULUDUR.
--
-- ============================================================
-- KÜME ADI
-- ============================================================
-- Bu dosya `uptrace_all` token'ını kullanır (prod'un küme adı). Başka
-- bir kümede koşacaksan TEK bir ReplaceAll ile değiştir:
--   sed -i 's/uptrace_all/<küme adın>/g' 0009_state_unify.sql
-- Lokal dağıtık kümede küme adı `coremetry`.
--
-- ============================================================
-- KAPSAM — 37 STATE TABLOSU, KÜÇÜKTEN BÜYÜĞE
-- ============================================================
-- Sıra ölçülen boyuta göre; `problems` en sonda (prod'un en büyüğü,
-- 633k satır — INSERT SELECT süresi ORADA ölçülür). Küçüklerle
-- başlamak, yordamı en ucuz tabloda doğrulamayı sağlar.
--
--  1 status_page_config          14 runbook_executions      27 ai_feedback
--  2 status_page_components      15 runbooks                28 exception_groups
--  3 status_page_published       16 rag_chunks              29 ai_calls
--  4 status_page_subscribers     17 trace_snapshots         30 notification_log
--  5 service_contracts           18 saved_views             31 audit_log
--  6 slos                        19 users                   32 log_templates
--  7 monitors                    20 system_settings         33 anomaly_events
--  8 monitor_results             21 dashboards              34 incidents
--  9 maintenance_windows         22 alert_rules             35 incident_problems
-- 10 notification_channels       23 events                  36 incident_events
-- 11 ldap_groups                 24 service_metadata        37 problems
-- 12 api_tokens                  25 rca_verdicts
-- 13 anomaly_silences            26 root_cause_hypotheses


-- ============================================================
-- ADIM 0 — ÖN KONTROL (hiçbir şey değiştirmez)
-- ============================================================

-- 0a. Makrolar tanımlı mı ve HER node benzersiz bir {shard}-{replica}
--     üretiyor mu? Dört satır, dört FARKLI `uniq` değeri beklenir.
SELECT hostName(), getMacro('shard') AS shard, getMacro('replica') AS replica,
       concat(getMacro('shard'), '-', getMacro('replica')) AS uniq
FROM clusterAllReplicas('uptrace_all', system.one)
ORDER BY hostName();

-- 0b. Bölünmenin BUGÜNKÜ hâli — göç sonrası bu sorgu tablo başına TEK
--     bir sayı vermeli. Şimdi shard başına farklı sayılar verecek.
SELECT hostName(), count() AS problems
FROM clusterAllReplicas('uptrace_all', currentDatabase(), problems)
GROUP BY hostName() ORDER BY hostName();

-- 0c. Her shard'ın BİR node'u — ADIM 2'nin INSERT'ü YALNIZ bunlarda
--     koşacak. Çıktıyı not al.
SELECT shard_num, host_name
FROM system.clusters
WHERE cluster = 'uptrace_all' AND replica_num = 1
ORDER BY shard_num;

-- 0d. Uygulama sürümü v0.9.1308+ mı? (ÖNKOŞUL)
--     curl -s http://<coremetry>/api/version


-- ============================================================
-- ADIM 1 — `_unified` TABLOLARINI ÜRET VE KUR
-- ============================================================
-- ÜRETİCİ. Şemayı ELLE yazma: bu sorgu her state tablosunun CANLI
-- DDL'ini alır, adına `_unified` ekler, ON CLUSTER enjekte eder ve ZK
-- yolunun `/{shard}/` segmentini `/state/` ile, replika adını
-- `{shard}-{replica}` ile değiştirir. Çıktıyı GÖZDEN GEÇİR, sonra
-- olduğu gibi koştur. Böylece şema store.go ile ıraksayamaz.
--
-- Tek bir node'da koşar (ON CLUSTER kümenin tamamına dağıtır).
--
-- ⚠ COREMETRY SQL KONSOLUNDAN (/admin/clickhouse) koşacaksan İKİ satırı
--   çıkar: sondaki `FORMAT TSVRaw;` ve `|| ';'` parçası. Konsolun
--   çoklu-ifade denetimi TIRNAK İÇİNDEKİ noktalı virgülü de ifade ayracı
--   sayıyor ve sorguyu reddediyor ("only single SELECT ... allowed").
--   Yani konsol için son satır: `       ) AS ddl`.
--   Doğrulandı 2026-08-23: konsolda 37 satır, 37'sinde /state/, 0 tanesinde
--   /{shard}/. Üretilen DDL'i çalıştırmak yine clickhouse-client işi
--   (konsol readonly=2).

SELECT replaceOne(
         replaceOne(create_table_query,
                    concat('CREATE TABLE ', database, '.', name),
                    concat('CREATE TABLE ', database, '.', name, '_unified ON CLUSTER uptrace_all')),
         concat('/{shard}/', name, '\', \'{replica}\''),
         concat('/state/',  name, '\', \'{shard}-{replica}\'')
       ) || ';' AS ddl
FROM system.tables
WHERE database = currentDatabase()
  AND engine LIKE 'Replicated%'
  AND name NOT LIKE '.inner%'
  AND name NOT LIKE '%\_local'
  AND name NOT LIKE '%\_old'
  AND name NOT LIKE '%\_unified'
ORDER BY total_rows ASC, name ASC
FORMAT TSVRaw;

-- DOĞRULAMA: 37 satır çıkmalı ve her satırda `/state/` geçmeli,
-- `/{shard}/` GEÇMEMELİ. Geçiyorsa üreticinin replaceOne'ı tutmamıştır
-- (ör. operatör ReplicaPath'i değiştirmiş ve yol `/{shard}/` içermiyor)
-- — DUR, elle incele.


-- ============================================================
-- ADIM 2 + 3 — TABLO BAŞINA: KOPYALA, SONRA TAKAS ET
-- ============================================================
-- Her tablo için sırayla:
--   (2)  INSERT — HER SHARD'IN BİR NODE'UNDA (ADIM 0c'nin listesi).
--        Prod: shard 1 → bp01, shard 2 → bp03.
--        Lokal: shard 01 → chc-0, shard 02 → chc-1.
--        İKİ replikada da koşturma (T1).
--   (3)  RENAME — TEK bir node'da, ON CLUSTER ile. Atomik takas.
--   (3b) yalnız ReplacingMergeTree tablolarında, isteğe bağlı yakalama.
--
-- Aşağıda tablo başına üçlü blok var. Bir bloğu bitirmeden sonrakine
-- geçme: (2) ile (3) arasındaki pencere ne kadar dar olursa o kadar az
-- satır `_old`'da kalır (T2).

------------------------------------------------------------- 1 status_page_config
INSERT INTO status_page_config_unified SELECT * FROM status_page_config;  -- her shard'da BİR node
RENAME TABLE status_page_config TO status_page_config_old, status_page_config_unified TO status_page_config ON CLUSTER uptrace_all;
INSERT INTO status_page_config SELECT * FROM status_page_config_old;  -- 3b yakalama (RMT, idempotent)

------------------------------------------------------------- 2 status_page_components
INSERT INTO status_page_components_unified SELECT * FROM status_page_components;
RENAME TABLE status_page_components TO status_page_components_old, status_page_components_unified TO status_page_components ON CLUSTER uptrace_all;
INSERT INTO status_page_components SELECT * FROM status_page_components_old;

------------------------------------------------------------- 3 status_page_published
INSERT INTO status_page_published_unified SELECT * FROM status_page_published;
RENAME TABLE status_page_published TO status_page_published_old, status_page_published_unified TO status_page_published ON CLUSTER uptrace_all;
INSERT INTO status_page_published SELECT * FROM status_page_published_old;

------------------------------------------------------------- 4 status_page_subscribers
INSERT INTO status_page_subscribers_unified SELECT * FROM status_page_subscribers;
RENAME TABLE status_page_subscribers TO status_page_subscribers_old, status_page_subscribers_unified TO status_page_subscribers ON CLUSTER uptrace_all;
INSERT INTO status_page_subscribers SELECT * FROM status_page_subscribers_old;

------------------------------------------------------------- 5 service_contracts
INSERT INTO service_contracts_unified SELECT * FROM service_contracts;
RENAME TABLE service_contracts TO service_contracts_old, service_contracts_unified TO service_contracts ON CLUSTER uptrace_all;
INSERT INTO service_contracts SELECT * FROM service_contracts_old;

------------------------------------------------------------- 6 slos
INSERT INTO slos_unified SELECT * FROM slos;
RENAME TABLE slos TO slos_old, slos_unified TO slos ON CLUSTER uptrace_all;
INSERT INTO slos SELECT * FROM slos_old;

------------------------------------------------------------- 7 monitors
INSERT INTO monitors_unified SELECT * FROM monitors;
RENAME TABLE monitors TO monitors_old, monitors_unified TO monitors ON CLUSTER uptrace_all;
INSERT INTO monitors SELECT * FROM monitors_old;

------------------------------------------------------------- 8 monitor_results  ⚠ MergeTree — 3b YOK
INSERT INTO monitor_results_unified SELECT * FROM monitor_results;
RENAME TABLE monitor_results TO monitor_results_old, monitor_results_unified TO monitor_results ON CLUSTER uptrace_all;

------------------------------------------------------------- 9 maintenance_windows
INSERT INTO maintenance_windows_unified SELECT * FROM maintenance_windows;
RENAME TABLE maintenance_windows TO maintenance_windows_old, maintenance_windows_unified TO maintenance_windows ON CLUSTER uptrace_all;
INSERT INTO maintenance_windows SELECT * FROM maintenance_windows_old;

------------------------------------------------------------- 10 notification_channels
INSERT INTO notification_channels_unified SELECT * FROM notification_channels;
RENAME TABLE notification_channels TO notification_channels_old, notification_channels_unified TO notification_channels ON CLUSTER uptrace_all;
INSERT INTO notification_channels SELECT * FROM notification_channels_old;

------------------------------------------------------------- 11 ldap_groups
INSERT INTO ldap_groups_unified SELECT * FROM ldap_groups;
RENAME TABLE ldap_groups TO ldap_groups_old, ldap_groups_unified TO ldap_groups ON CLUSTER uptrace_all;
INSERT INTO ldap_groups SELECT * FROM ldap_groups_old;

------------------------------------------------------------- 12 api_tokens
INSERT INTO api_tokens_unified SELECT * FROM api_tokens;
RENAME TABLE api_tokens TO api_tokens_old, api_tokens_unified TO api_tokens ON CLUSTER uptrace_all;
INSERT INTO api_tokens SELECT * FROM api_tokens_old;

------------------------------------------------------------- 13 anomaly_silences
INSERT INTO anomaly_silences_unified SELECT * FROM anomaly_silences;
RENAME TABLE anomaly_silences TO anomaly_silences_old, anomaly_silences_unified TO anomaly_silences ON CLUSTER uptrace_all;
INSERT INTO anomaly_silences SELECT * FROM anomaly_silences_old;

------------------------------------------------------------- 14 runbook_executions
INSERT INTO runbook_executions_unified SELECT * FROM runbook_executions;
RENAME TABLE runbook_executions TO runbook_executions_old, runbook_executions_unified TO runbook_executions ON CLUSTER uptrace_all;
INSERT INTO runbook_executions SELECT * FROM runbook_executions_old;

------------------------------------------------------------- 15 runbooks
INSERT INTO runbooks_unified SELECT * FROM runbooks;
RENAME TABLE runbooks TO runbooks_old, runbooks_unified TO runbooks ON CLUSTER uptrace_all;
INSERT INTO runbooks SELECT * FROM runbooks_old;

------------------------------------------------------------- 16 rag_chunks
INSERT INTO rag_chunks_unified SELECT * FROM rag_chunks;
RENAME TABLE rag_chunks TO rag_chunks_old, rag_chunks_unified TO rag_chunks ON CLUSTER uptrace_all;
INSERT INTO rag_chunks SELECT * FROM rag_chunks_old;

------------------------------------------------------------- 17 trace_snapshots
INSERT INTO trace_snapshots_unified SELECT * FROM trace_snapshots;
RENAME TABLE trace_snapshots TO trace_snapshots_old, trace_snapshots_unified TO trace_snapshots ON CLUSTER uptrace_all;
INSERT INTO trace_snapshots SELECT * FROM trace_snapshots_old;

------------------------------------------------------------- 18 saved_views
INSERT INTO saved_views_unified SELECT * FROM saved_views;
RENAME TABLE saved_views TO saved_views_old, saved_views_unified TO saved_views ON CLUSTER uptrace_all;
INSERT INTO saved_views SELECT * FROM saved_views_old;

------------------------------------------------------------- 19 users
INSERT INTO users_unified SELECT * FROM users;
RENAME TABLE users TO users_old, users_unified TO users ON CLUSTER uptrace_all;
INSERT INTO users SELECT * FROM users_old;

------------------------------------------------------------- 20 system_settings
INSERT INTO system_settings_unified SELECT * FROM system_settings;
RENAME TABLE system_settings TO system_settings_old, system_settings_unified TO system_settings ON CLUSTER uptrace_all;
INSERT INTO system_settings SELECT * FROM system_settings_old;

------------------------------------------------------------- 21 dashboards
INSERT INTO dashboards_unified SELECT * FROM dashboards;
RENAME TABLE dashboards TO dashboards_old, dashboards_unified TO dashboards ON CLUSTER uptrace_all;
INSERT INTO dashboards SELECT * FROM dashboards_old;

------------------------------------------------------------- 22 alert_rules
INSERT INTO alert_rules_unified SELECT * FROM alert_rules;
RENAME TABLE alert_rules TO alert_rules_old, alert_rules_unified TO alert_rules ON CLUSTER uptrace_all;
INSERT INTO alert_rules SELECT * FROM alert_rules_old;

------------------------------------------------------------- 23 events
INSERT INTO events_unified SELECT * FROM events;
RENAME TABLE events TO events_old, events_unified TO events ON CLUSTER uptrace_all;
INSERT INTO events SELECT * FROM events_old;

------------------------------------------------------------- 24 service_metadata
INSERT INTO service_metadata_unified SELECT * FROM service_metadata;
RENAME TABLE service_metadata TO service_metadata_old, service_metadata_unified TO service_metadata ON CLUSTER uptrace_all;
INSERT INTO service_metadata SELECT * FROM service_metadata_old;

------------------------------------------------------------- 25 rca_verdicts
INSERT INTO rca_verdicts_unified SELECT * FROM rca_verdicts;
RENAME TABLE rca_verdicts TO rca_verdicts_old, rca_verdicts_unified TO rca_verdicts ON CLUSTER uptrace_all;
INSERT INTO rca_verdicts SELECT * FROM rca_verdicts_old;

------------------------------------------------------------- 26 root_cause_hypotheses
INSERT INTO root_cause_hypotheses_unified SELECT * FROM root_cause_hypotheses;
RENAME TABLE root_cause_hypotheses TO root_cause_hypotheses_old, root_cause_hypotheses_unified TO root_cause_hypotheses ON CLUSTER uptrace_all;
INSERT INTO root_cause_hypotheses SELECT * FROM root_cause_hypotheses_old;

------------------------------------------------------------- 27 ai_feedback
INSERT INTO ai_feedback_unified SELECT * FROM ai_feedback;
RENAME TABLE ai_feedback TO ai_feedback_old, ai_feedback_unified TO ai_feedback ON CLUSTER uptrace_all;
INSERT INTO ai_feedback SELECT * FROM ai_feedback_old;

------------------------------------------------------------- 28 exception_groups
INSERT INTO exception_groups_unified SELECT * FROM exception_groups;
RENAME TABLE exception_groups TO exception_groups_old, exception_groups_unified TO exception_groups ON CLUSTER uptrace_all;
INSERT INTO exception_groups SELECT * FROM exception_groups_old;

------------------------------------------------------------- 29 ai_calls  ⚠ MergeTree — 3b YOK
INSERT INTO ai_calls_unified SELECT * FROM ai_calls;
RENAME TABLE ai_calls TO ai_calls_old, ai_calls_unified TO ai_calls ON CLUSTER uptrace_all;

------------------------------------------------------------- 30 notification_log  ⚠ MergeTree — 3b YOK
INSERT INTO notification_log_unified SELECT * FROM notification_log;
RENAME TABLE notification_log TO notification_log_old, notification_log_unified TO notification_log ON CLUSTER uptrace_all;

------------------------------------------------------------- 31 audit_log  ⚠ MergeTree — 3b YOK
INSERT INTO audit_log_unified SELECT * FROM audit_log;
RENAME TABLE audit_log TO audit_log_old, audit_log_unified TO audit_log ON CLUSTER uptrace_all;

------------------------------------------------------------- 32 log_templates
INSERT INTO log_templates_unified SELECT * FROM log_templates;
RENAME TABLE log_templates TO log_templates_old, log_templates_unified TO log_templates ON CLUSTER uptrace_all;
INSERT INTO log_templates SELECT * FROM log_templates_old;

------------------------------------------------------------- 33 anomaly_events
INSERT INTO anomaly_events_unified SELECT * FROM anomaly_events;
RENAME TABLE anomaly_events TO anomaly_events_old, anomaly_events_unified TO anomaly_events ON CLUSTER uptrace_all;
INSERT INTO anomaly_events SELECT * FROM anomaly_events_old;

------------------------------------------------------------- 34 incidents
INSERT INTO incidents_unified SELECT * FROM incidents;
RENAME TABLE incidents TO incidents_old, incidents_unified TO incidents ON CLUSTER uptrace_all;
INSERT INTO incidents SELECT * FROM incidents_old;

------------------------------------------------------------- 35 incident_problems
INSERT INTO incident_problems_unified SELECT * FROM incident_problems;
RENAME TABLE incident_problems TO incident_problems_old, incident_problems_unified TO incident_problems ON CLUSTER uptrace_all;
INSERT INTO incident_problems SELECT * FROM incident_problems_old;

------------------------------------------------------------- 36 incident_events  ⚠ MergeTree — 3b YOK
INSERT INTO incident_events_unified SELECT * FROM incident_events;
RENAME TABLE incident_events TO incident_events_old, incident_events_unified TO incident_events ON CLUSTER uptrace_all;

------------------------------------------------------------- 37 problems  ⚠ EN BÜYÜK (prod 633k)
-- Süreyi ÖLÇ: bu tablonun INSERT SELECT'i yordamın en pahalı adımı.
--   SET send_logs_level='information';  (isteğe bağlı)
INSERT INTO problems_unified SELECT * FROM problems;
RENAME TABLE problems TO problems_old, problems_unified TO problems ON CLUSTER uptrace_all;
INSERT INTO problems SELECT * FROM problems_old;


-- ============================================================
-- ADIM 4 — DOĞRULAMA (ADIM 5'in ÖNKOŞULU)
-- ============================================================

-- 4a. Her tablo TEK bir replikasyon grubunda mı? Tablo başına TEK bir
--     zookeeper_path satırı beklenir ve yol `/state/` içermeli.
SELECT table, zookeeper_path, count() AS replicas
FROM clusterAllReplicas('uptrace_all', system.replicas)
WHERE database = currentDatabase()
  AND table NOT LIKE '%\_local' AND table NOT LIKE '%\_old'
GROUP BY table, zookeeper_path
HAVING zookeeper_path NOT LIKE '%/{shard}/%'
ORDER BY table;
-- BEKLENEN: 37 satır, `replicas` = küme host sayısı (prod 4, lokal 2).

-- 4b. Dört host da AYNI sayıyı veriyor mu? `uniq_counts = 1` şart.
SELECT 'problems' AS t, uniqExact(c) AS uniq_counts, min(c) AS n FROM (
  SELECT hostName() AS h, count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), problems) GROUP BY h)
UNION ALL SELECT 'alert_rules', uniqExact(c), min(c) FROM (
  SELECT hostName() AS h, count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), alert_rules) GROUP BY h)
UNION ALL SELECT 'users', uniqExact(c), min(c) FROM (
  SELECT hostName() AS h, count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), users) GROUP BY h)
UNION ALL SELECT 'system_settings', uniqExact(c), min(c) FROM (
  SELECT hostName() AS h, count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), system_settings) GROUP BY h)
UNION ALL SELECT 'dashboards', uniqExact(c), min(c) FROM (
  SELECT hostName() AS h, count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), dashboards) GROUP BY h)
UNION ALL SELECT 'anomaly_events', uniqExact(c), min(c) FROM (
  SELECT hostName() AS h, count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), anomaly_events) GROUP BY h);

-- 4c. Yeni sayı, iki shard'ın BİRLEŞİMİ mi?
--     `_old` hâlâ duruyor, yani karşılaştırma yapılabilir:
SELECT sum(c) AS eski_toplam FROM (
  SELECT count() AS c
  FROM clusterAllReplicas('uptrace_all', currentDatabase(), problems_old)
  GROUP BY getMacro('shard'));
--     ReplacingMergeTree tablolarında yeni sayı ≤ eski toplam olabilir:
--     iki shard'da AYNI id varsa yüksek `version` kazanır. Kesin sayı:
SELECT count() FROM problems FINAL;

-- 4d. Uygulama sağlığı (göç sonrası, restart GEREKMEDEN):
--     curl -s http://<coremetry>/api/health
--     curl -s http://<coremetry>/api/version
--     /inbox (problems), /settings (alert rules), kullanıcı girişi.


-- ============================================================
-- ADIM 5 — YEDEKLERİ DÜŞÜR (geri dönüş YOK)
-- ============================================================
-- ADIM 4 tamamen temiz çıktıktan ve uygulama en az bir gün sorunsuz
-- çalıştıktan SONRA. Tek tek koştur — toplu `DROP` yazma.
--
-- DROP TABLE status_page_config_old      ON CLUSTER uptrace_all SYNC;
-- DROP TABLE status_page_components_old  ON CLUSTER uptrace_all SYNC;
-- DROP TABLE status_page_published_old   ON CLUSTER uptrace_all SYNC;
-- DROP TABLE status_page_subscribers_old ON CLUSTER uptrace_all SYNC;
-- DROP TABLE service_contracts_old       ON CLUSTER uptrace_all SYNC;
-- DROP TABLE slos_old                    ON CLUSTER uptrace_all SYNC;
-- DROP TABLE monitors_old                ON CLUSTER uptrace_all SYNC;
-- DROP TABLE monitor_results_old         ON CLUSTER uptrace_all SYNC;
-- DROP TABLE maintenance_windows_old     ON CLUSTER uptrace_all SYNC;
-- DROP TABLE notification_channels_old   ON CLUSTER uptrace_all SYNC;
-- DROP TABLE ldap_groups_old             ON CLUSTER uptrace_all SYNC;
-- DROP TABLE api_tokens_old              ON CLUSTER uptrace_all SYNC;
-- DROP TABLE anomaly_silences_old        ON CLUSTER uptrace_all SYNC;
-- DROP TABLE runbook_executions_old      ON CLUSTER uptrace_all SYNC;
-- DROP TABLE runbooks_old                ON CLUSTER uptrace_all SYNC;
-- DROP TABLE rag_chunks_old              ON CLUSTER uptrace_all SYNC;
-- DROP TABLE trace_snapshots_old         ON CLUSTER uptrace_all SYNC;
-- DROP TABLE saved_views_old             ON CLUSTER uptrace_all SYNC;
-- DROP TABLE users_old                   ON CLUSTER uptrace_all SYNC;
-- DROP TABLE system_settings_old         ON CLUSTER uptrace_all SYNC;
-- DROP TABLE dashboards_old              ON CLUSTER uptrace_all SYNC;
-- DROP TABLE alert_rules_old             ON CLUSTER uptrace_all SYNC;
-- DROP TABLE events_old                  ON CLUSTER uptrace_all SYNC;
-- DROP TABLE service_metadata_old        ON CLUSTER uptrace_all SYNC;
-- DROP TABLE rca_verdicts_old            ON CLUSTER uptrace_all SYNC;
-- DROP TABLE root_cause_hypotheses_old   ON CLUSTER uptrace_all SYNC;
-- DROP TABLE ai_feedback_old             ON CLUSTER uptrace_all SYNC;
-- DROP TABLE exception_groups_old        ON CLUSTER uptrace_all SYNC;
-- DROP TABLE ai_calls_old                ON CLUSTER uptrace_all SYNC;
-- DROP TABLE notification_log_old        ON CLUSTER uptrace_all SYNC;
-- DROP TABLE audit_log_old               ON CLUSTER uptrace_all SYNC;
-- DROP TABLE log_templates_old           ON CLUSTER uptrace_all SYNC;
-- DROP TABLE anomaly_events_old          ON CLUSTER uptrace_all SYNC;
-- DROP TABLE incidents_old               ON CLUSTER uptrace_all SYNC;
-- DROP TABLE incident_problems_old       ON CLUSTER uptrace_all SYNC;
-- DROP TABLE incident_events_old         ON CLUSTER uptrace_all SYNC;
-- DROP TABLE problems_old                ON CLUSTER uptrace_all SYNC;
