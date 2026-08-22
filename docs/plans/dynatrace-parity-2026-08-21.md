# Dynatrace-Parite Denetimi — İyileşme Alanları ve Öneriler (2026-08-21)

Operatör hedefi: "iyileşme alanlarını ve daha iyi bir deneyim için
önerileri bul — Dynatrace kadar iyi olsun."

Yöntem: 5 boyutlu paralel kod-denetimi (Davis/RCA · APM çekirdeği ·
altyapı+k8s · alerting/ops · günlük UX) + eksiklik-avcısı eleştiri
geçişi. Her bulgu koddan file:line ile doğrulandı; kapalı/reddedilmiş
işler (dışlama listesi) elendi. Sürüm bağlamı: v0.9.1213.

Genel değerlendirme: otomatik katman sanılandan güçlü (hipotez
synthesizer, baseline'lı anomaliler, kümeleme, auto-explain) — boşluk
"sıfırdan Davis" değil, mevcut hatların uzatılması ve GÖRÜNÜR kılınması.
En büyük tema: ürün doğru şeyleri hesaplıyor ama bazıları yalnız
tıklayınca/admin bakınca görünüyor.

## EN YÜKSEK DEĞER/EFOR 10 — önerilen sıra

(Ayrıntılar eleştiri bölümünde; her madde bağımsız dilim, kendi vX'i.)

| # | Dilim | Efor | Değer |
|---|---|---|---|
| 1 | ⌘K palet adlarını i18n'e bağla + alias arama | ~1sa | 5 |
| 2 | JVM non-heap (Metaspace) kartı | ~30dk | 4 |
| 3 | Settings sekmeleri palete + rol filtresi | ~45dk | 4 |
| 4 | Span self-time (trace hotspot cevabı) | ~1sa | 4 |
| 5 | OOMKilled / last-termination rozeti | ~2sa | 5 |
| 6 | N+1 görünürlüğü: waterfall tekrar-chip'i + DB pivotu | ~2sa | 5 |
| 7 | Bildirim kanalı sağlık rozeti | ~2sa | 4 |
| 8 | Self-health alarm ailesi (ingest stall/spool/disk ETA) | ~yarım gün | 5 |
| 9 | Share: mutlak-zaman kopyalama | ~1sa | 4 |
| 10 | Auto-verdict + kalıcı verdict gövdesi | ~yarım gün+ | 5 |

Yedek sıra: GC/exceptions hız kartları · problem'den tek-tık susturma ·
node→pod haritası · recurring maintenance · enstrümantasyon kapsam
matrisi · incident-farkında bildirim fan-out'u · runbooks config-export.

Büyük işler (ayrı karar ister): nedensellik zinciri derinleştirme
(hop/temporal) · infra baseline anomalileri · problem birleştirme
(join-on-open) · yönetici/SLA raporu.

---

# EK A — Eleştiri geçişi (ıskalananlar + yanlış-pozitif ayıklama + kısa liste)

# Eksiklik Avı — 5 Boyutlu Dynatrace-Parite Denetiminin Üstünden Geçiş

## BÖLÜM A — Beş boyutun da ISKALADIĞI alanlar

## A1. Self-health ALARMI yok — tüm meta-görünürlük pasif panel, hiçbiri problem açmıyor
**Dynatrace:** Self-monitoring ortamı birinci sınıf: ActiveGate/ingest sağlığı, disk doluluk TAHMİNİ (Davis "disk full in N days"), veri kesintisi kendisi bir problem olarak açılır ve on-call'a gider.
**Coremetry mevcut durum (koddan):** Paneller şaşırtıcı derecede zengin ama HEPSİ pasif: `internal/chstore/sysstats.go:18-56` — Disks (v0.9.289), Servers (v0.9.290), Drops, DistributionQueue (v0.9.985; kendi yorumu: "3.5 saatlik ölü ingest tüm ekranlarda yeşil görünüyordu"); `internal/chstore/cardinality.go:12-17` — top emitters, yorumu "surfaces here days before the disk fills up" diyor ama YALNIZ admin bakarsa. Evaluator/anomaly taramasında self-problem/watchdog/spool alarmı SIFIR sonuç (grep doğrulandı: `internal/evaluator/`, `internal/anomaly/` — tek "Watchdog" geçişi paket yorumu). Forecast yalnız SLO'da var (`internal/chstore/slo.go:301`), disk için yok. 2026-08-20 prod spool olayı (metric_points 3M dosya/965GiB) tam bu boşluğun faturası.
**Öneri:** Lider-kilitli self-monitor tiki → sentetik problem ailesi (`rule_id: self-*`): (a) ingest stall (span/metric rate son N dk sıfıra düştü), (b) distribution-queue spool derinliği eşik üstü, (c) disk days-to-full (Disks serisinden lineer projeksiyon), (d) servis hacim sıçraması (cardinality top-row 24h vs önceki 24h, N×). Mevcut problem+bildirim hattını aynen kullanır; eşikler `system_settings`. Boyut 4 #2b'nin channel-broken self-problem'i de bu aileye girer.
**Efor:** ~yarım gün (ilk üç kontrol) · **Değer: 5** — bankada "izleme sistemi kendisi öldü ve kimse bilmedi" en utandırıcı arıza sınıfı; kanıtı bir hafta önce yaşandı.

## A2. Enstrümantasyon kapsam matrisi yok — hangi servis hangi sinyali GÖNDERMİYOR görünmez
**Dynatrace:** Deployment status / coverage görünümü: hangi host/servis ajanlı, hangi modül eksik; kapsam boşluğu operatöre gelir.
**Coremetry mevcut durum:** Hiçbir yüzeyde servis-başına sinyal varlığı yok — `frontend/src/lib/types.ts`'te hasLogs/hasMetrics/logRate alanı sıfır (grep doğrulandı), chstore/api'de coverage kavramı yok. Binlerce JBoss servisinde tipik durum: trace var ama JVM metrikleri gelmiyor (javaagent config eksik) ya da loglar trace_id'siz (korelasyon kopuk) — bugün bunu ancak tek tek servise girip boş panel görerek anlıyorsun.
**Öneri:** `/admin` altına kapsam matrisi ucu: `service_summary_5m` servis kümesi × metric_points'te `jvm.*` emit eden servisler × logstore servis kümesi × trace_id'li log oranı — üç LEFT JOIN'lik tek sorgu, `s.serveCached`. FE: /services'e "sinyal rozetleri" (T/M/L) kolonu ya da admin sayfası + "eksik kapsam" filtresi.
**Efor:** ~yarım gün · **Değer: 4** — bankanın enstrümantasyon rollout'unu ölçülebilir yapar; "neden bu serviste GC grafiği boş" ticket'larını keser.

## A3. Yönetici/SLA raporu — digest'in (Boyut 4 #6) bir kat üstü eksik
**Dynatrace:** Zamanlanmış rapor katmanında yalnız ops digest değil, yönetime giden aylık availability/SLO uyum raporu da var.
**Coremetry mevcut durum:** Parçalar mevcut — SLO status+forecast (`internal/chstore/slo.go:301-361`), `service_contracts` tablosu (`internal/chstore/store.go:1633`), problems listesi; ama MTTR/açılan-çözülen istatistiği hiçbir yerde derlenmiyor, export yolu yok (notify'da digest/rapor sıfır — grep doğrulandı).
**Öneri:** Boyut 4 #6 digest'i yapılırken kompozisyonu iki kademeli kur: günlük ops maili + aylık SLO/availability + MTTR özeti (CSV/HTML). Ayrı iş olarak değil, digest dilimine +1 gün.
**Efor:** digest üstüne ~yarım gün · **Değer: 3** — banka iç denetimi/regülatör raporlaması manuel Excel işi olmaktan çıkar.

## A4. Config export `runbooks`'u DIŞARIDA bırakıyor — DR yedeği eksik
**Dynatrace:** Config-as-code (Monaco) tüm operatör-durumunu kapsar.
**Coremetry mevcut durum:** Export/import VAR ve iyi (`internal/api/config_iox.go:31-46` — 14 tablo: settings, channels, alert_rules, dashboards, saved_views, slos, maintenance, monitors, status page…), ama `runbooks` listede YOK; runbooks ayrı ReplacingMergeTree tablosunda yaşıyor (`internal/chstore/purge.go:63` tabloyu sayıyor, runbooks.go:15 "DEDICATED table"). Temiz kuruluma taşınan bankada runbook adım/koşu tanımları kaybolur.
**Öneri:** `configTables`'a `"runbooks"` ekle (+1 satır; import yolu jenerik, dumpConfigTable aynen çalışır). Kolon-uyum testi ekle.
**Efor:** ~30dk · **Değer: 3** — tek satırlık DR bütünlüğü.

## A5. Negatif doğrulamalar — bunlar BOŞLUK DEĞİL, kimse önermesin
- **Synthetic monitoring VAR:** `/monitors` http-poll + heartbeat sayfası mevcut (`frontend/src/pages/Monitors.tsx:17-24`), sadeleştirme kararıyla sidebar'dan gizli — yeniden önerme, operatör kararı.
- **Config export/import VAR:** yukarıda (A4 yalnız kapsam düzeltmesi).
- **Explore zaten ÇOK-SİNYALLİ:** spans/metrics/logs kaynak seçici (`frontend/src/pages/Explore.tsx:748`) — "unified query" boşluğu büyük ölçüde kapalı; kalan sürtünme Boyut 2 #4'ün giriş-kapısı bulgusu.
- **Status page VAR** (`status_page_*` tabloları, config_iox.go:43-45).
- **RUM gerçekten yok** ama bu ürün-kapsamı kararı (backend APM); "büyük" sınıfı, operatör kararı olmadan kuyruğa girmez — kısa listeye almadım.

## BÖLÜM B — Denetimdeki önerilerde çakışma/yanlış-pozitif işaretleri

## B1. Boyut 1 #1 + #6 — çakışma değil ama TEK dilim olmalı; git log kontrolü şart
"Auto-verdict üret" (#1) ve "verdict gövdesini kalıcılaştır" (#6) ayrı ayrı listelenmiş; ikisi aynı işin iki yarısı (üretmeden kalıcılaştıramazsın, kalıcılaştırmadan auto-üretim israf). Birleştir: tek dilim "deep-investigate kapısında verdict üret + `rca_verdicts.body` kolonuna yaz + drawer persisted çizer". Dikkat: ZATEN GEMİDE listesi "RCA verdict+shields+katalog"u içeriyor ve AI planı Faz 0-6 komple — `RootCauseRibbon.tsx`'in "LAZY" yorumu güncel-kod kanıtı olduğu için boşluk muhtemelen gerçek, ama kuyruğa almadan önce `git log --grep=verdict` ile 4.3/4.5 fazlarının bunu kapsamadığı DOĞRULANMALI (hafıza kuralı: faz durumu plandan değil git log'dan).

## B2. Boyut 3 #9 (Java profiling) — operatör-gizli yüzeye dokunuyor, kuyruk DIŞI
Profiling sidebar'dan operatör kararıyla gizli (sadeleştirme v489-499). Denetim kendisi de "önce operatör kararı" diyor — doğru; kısa listeye girmez, "yatırım kararı sorusu" olarak ayrı sunulur.

## B3. Boyut 5 #9 (global refresh dropdown) — bilinçli kapsam-dışı kararının yeniden açılması
2026-07-24 brief'i bunu bilerek dışarıda bıraktı (`TimeRangePicker.tsx:27-29`). Reddedilmiş özellik sınıfına yakın; ancak operatör kendisi isterse. Kısa liste DIŞI.

## B4. Boyut 2 #2'nin "AggregatedStructure diriltme/silme" yarısı — sayfa v0.9.226'da kaldırıldı
Chip + `groupSimilar` toggle dilimi temiz (kod TraceWaterfall içinde yaşıyor, çakışma yok); ama öksüz `AggregatedStructure.tsx`'in diriltilmesi ayrı bir operatör kararı — dilimden ayrıştırılmalı, silme önerisi olarak sunulabilir.

## B5. Temiz çıkanlar (grep/dosya ile doğrulandı)
- Boyut 4 #1 recurring maintenance: `maintenance.go`'da recurrence/weekday alanı YOK — bulgu geçerli.
- Boyut 4 #6 digest: `internal/notify/`'da digest sıfır — geçerli.
- Boyut 5 #4 Share: `ShareButton.tsx` `window.location.href`'i olduğu gibi kopyalıyor — geçerli.
- Boyut 5 #1 palet: `CommandPalette.tsx:55,57` ("Inbox"/"Problems") vs `i18n.ts:36,39,171,174` ('nav.inbox'='Problems'/'Sorunlar', 'nav.problems'='Exceptions') — ayrışma birebir doğru, bulgu geçerli.
- Boyut 4 #4 webhook preset: notifications gemide ama PD/Opsgenie hazır-gövde yok — çakışma değil, geçerli.
- Boyut 3 #1/#2/#3, Boyut 2 #1/#3/#4/#5, Boyut 5 #2/#3: dışlama/gemide listeleriyle çarpışma görmedim; Boyut 2'nin kapsam-dışı bölümü ve Boyut 3 #5 zaten doğru öz-eleme yapmış.

## BÖLÜM C — En yüksek değer/efor 10'luk kısa liste (sıralı)

> **İCRA DURUMU (2026-08-23): 10/10 GEMİDE** — v0.9.1270→1281 (+1282
> null/undefined düzeltmesi), sıra raporun sırası. Madde-başı sürüm
> damgaları aşağıda. Kalan seçilebilir işler yalnız "Yedek sıra" +
> adaylar (demo N+1 senaryosu, A1'in ertelenen hacim-sıçraması kuralı).

1. ✅ v0.9.1270 — **⌘K palet adlarını i18n'e bağla + alias skorlama** (B5#1) — `CommandPalette.tsx:55-57` vs `i18n.ts:36,171`; sidebar "Problems" diyen sayfa palette "Inbox". ~1sa · Değer 5.
2. ✅ v0.9.1271 — **JVM non-heap (Metaspace) kartı** (B3#7) — `RuntimeCharts.tsx:42` heap'e sabit; JBoss classloader-leak sınıfı görünmez. ~30dk · Değer 4.
3. ✅ v0.9.1272 — **Settings sekmelerini palete + rol filtresi** (B5#3 + Q3) — `CommandPalette.tsx:110` tek Settings girişi, 24 sekme aranamaz; "ayar ara…" vaadi boş. ~30-45dk · Değer 4.
4. ✅ v0.9.1273 — **Span self-time** (B2#1) — `SpanDetail.tsx:254` yalnız toplam Duration; çocuk toplamı client'ta hazır. ~1sa · Değer 4. (Aralık-birleşimli: çocuk örtüşmesi çift sayılmaz.)
5. ✅ v0.9.1276 — **OOMKilled/last-termination rozeti** (B3#3) — `kube_pod_container_status_last_terminated_reason` hiç sorgulanmıyor (`internal/thanos/promql.go`); restart sayısı var, nedeni kubectl'de. ~2sa · Değer 5. (Hayalet pod satırları da rozeti taşır; lokalde KSM yok, görsel teyit prod'da.)
6. ✅ v0.9.1277 — **N+1 görünürlüğü: waterfall tekrar-chip'i + DB drawer pivotu** (B2#2) — `spanRepeats` ucu yetim, `groupSimilar` uykuda. ~2sa · Değer 5. (groupSimilar kimlik çürümesi onarıldı; AggregatedStructure SİLİNDİ; demo tekrar üretmediği için chip lokalde tetiklenmez → demo N+1 senaryosu aday.)
7. ✅ v0.9.1278 — **Kanal sağlık rozeti** (B4#2a) — `notification_log` vardı, ChannelsTab görmüyordu. ~2sa · Değer 4. (Uçtan uca kanıt: kırık webhook ×1→×2→başarıda 0.)
8. ✅ v0.9.1279 — **Self-health alarm ailesi** (A1) — self-ingest-stall (P1) + self-spool-depth + self-disk-eta + self-channel-broken; eşikler `system_settings.self_health` vidası. ~yarım gün · Değer 5. (Hacim-sıçraması kuralı raporun kendi efor notuyla ERTELENDİ — aday.)
9. ✅ v0.9.1280 — **Share mutlak-zaman kopyalama** (B5#4) — paylaşım linki `range=custom:` ile pencereyi sabitler; range'siz/zaten-custom URL bayt-aynı. ~1sa · Değer 4.
10. ✅ v0.9.1281 (+1282) — **Auto-verdict + kalıcı gövde** (B1#1+#6) — `rca_verdicts.body ZSTD` + `source` kolonları (dağıtık-güvenli probe+koşullu INSERT), deep-investigate kapısında otomatik üretim (`ai_calls.surface=rootcause-verdict`, autoExplain vidası + dedup + yalnız P1/deploy), drawer'da `kalıcı kayıt · zaman · otomatik|operatör` provenance. A2 (insight kartı LLM ateşlemez) KORUNDU; kart bilinçli cache-only kaldı (kalıcı satır tam RCAVerdict şekli taşımıyor). 1282: `verdict===undefined` vs `null` — başarısız canlı üretim anında kalıcı kaydı gizleyen dal düzeltildi.

**Yedek sıra (10'a giremeyenler):** GC collections/s + exceptions/s kartları (B3#6, ~1sa/4), problem'den tek-tık susturma (B4#5, ~1sa/3), node→pod haritası (B3#2, ~yarım gün/4), recurring maintenance (B4#1, ~yarım gün/4), kapsam matrisi (A2, ~yarım gün/4), incident-farkında fan-out (B4#3, ~yarım gün/4), runbooks'u config export'a ekle (A4, ~30dk/3).

---

# EK B1 — Davis / otomatik kök-neden

## Mevcut durum özeti (Davis paritesinin NEREDE olduğu)

Otomatik katman sanılandan güçlü: `RootCauseSynthesizer` 30 sn'lik leader-gated tick'te TÜM aktif anomaliler + critical açık problemler için deterministik hipotez üretip kalıcılaştırıyor (`internal/anomaly/rootcause_worker.go:83-256`), `ProblemExplainer` critical problemlere tıklamasız AISummary yazıyor (`internal/anomaly/problem_explainer.go:15-29`), baseline öğrenimi var (median/MAD + 14 gün same-slot mevsimsel, `internal/anomaly/anomaly.go:59-74`), kümeleme var (`internal/anomaly/clustering.go`), P1/deploy vakalarında derin soruşturma + denetim izi var (`internal/anomaly/investigation.go:72-97`). Boşluklar aşağıda — hiçbiri "sıfırdan Davis yaz" değil, hepsi mevcut hatların uzatılması.

## 1. Kalkanlı RCA verdict yalnız TIKLA üretiliyor — Davis'te problem açıldığında analiz hazır

**Dynatrace:** Davis her problemi kimse tıklamadan analiz eder; problem sayfası açıldığında kök neden + nedensellik zinciri + etki hazır durur.

**Coremetry:** Ürünün en güçlü çıktısı olan kalkanlı verdict (nedensellik zinciri, reddedilen hipotezler, remediation, kanıt atıfları — `internal/api/rca_verdict.go:70-97`) YALNIZ ✨ Explain tıklamasında üretiliyor: `internal/api/rootcause.go:473-601` (`rootCauseExplainProse` → `buildRCAVerdict`), FE tarafı `frontend/src/components/RootCauseRibbon.tsx:286-293` — yorum açıkça "LAZY: nothing is fetched until the operator clicks". Otomatik taraf yalnız deterministik hipotez ribbon'ı + düz AISummary.

**Öneri:** Synthesizer'ın deep-investigate kapısından geçen anchor'larda (`shouldDeepInvestigate`, `rootcause_worker.go:19-21`: P1 VEYA deploy-korelasyonlu) `buildRCAVerdict`'i arka planda da çalıştırıp kalıcılaştır; problem drawer açılışında persisted verdict tıklamasız çizilsin, ✨ Explain "yeniden değerlendir"e dönüşsün. Mevcut kapılar (`AutoExplainEnabled`, `QuotaBackoffActive`, leader lock — `problem_explainer.go:89-115`) aynen uygulanır; lokal gemma'da maliyet kota değil zaman, P1 hacmi zaten batch-tavanlı.

**Efor:** ~yarım gün. **Değer: 5** — gece nöbetçisi problem sayfasını açtığında hazır karar görmek, "correlation is the differentiator" iddiasının görünür yüzü; bankacılıkta MTTR'ın ilk 5 dakikası en pahalı dakikalar.

## 2. Auto-explain kapısı yalnız severity=critical — warning problemler anlatısız

**Dynatrace:** Davis severity ayrımı yapmadan her problemi analiz eder.

**Coremetry:** `problem_explainer.go:122-127` `ListProblems(Severity:"critical")` ile filtreliyor; tasarım yorumu "Critical severity only by default" (`problem_explainer.go:26-27`) ama "default" ayarlanabilir değil, sabit. Warning seviyesindeki bir problem (ör. P2 açık-saat merdiveniyle P1'e tırmanmadan önce) hiç özet almıyor; deep-investigate kapısı da critical listesinden besleniyor (`rootcause_worker.go:196-199`).

**Öneri:** Severity kapısını `system_settings` vidasına bağla (varsayılan critical; operatör critical+warning seçebilsin) — batch cap (16) ve kota devre-kesici maliyeti zaten sınırlıyor.

**Efor:** ~1 saat. **Değer: 3.**

## 3. Nedensellik zinciri 2 hop + yapısal — derin JBoss çağrı zincirlerinde gerçek kaynak hipoteze giremiyor

**Dynatrace:** Davis'in nedensellik zinciri tam derinlikte yürür ve dikeydir (servis → process → host katmanları zincir adımıdır).

**Coremetry:** `internal/correlator/propagation.go:40-46` — `propagationMaxHops=2`, decay 0.5; model error-share YAPISAL, temporal korelasyon dosyanın kendi yorumunda "deliberate future refinement" (`propagation.go:36-39`). Banka prod'unda gateway → orkestratör → core banking → Oracle-önü tarzı 4-6 hop zincirler tipik; 3+ hop ötedeki gerçek kaynak aday listesine hiç giremiyor. Dikey eksik: heap/GC/pod doygunluğu yalnız P1/deploy vakasında "deep evidence saturation family" olarak giriyor (`investigation.go:49,86`), zincir adımı olarak değil.

**Öneri (dilimli):** (a) maxHops 2→3 + decay'i `problem_priority` gibi vida yap — grafik bellekte, DFS bounded, ucuz; (b) 5 dk-kova temporal korelasyon çarpanı: correlations hattının composite skoru zaten var (`RootCausePanel`'in "What else changed" verisi), propagation skoruna çarpan olarak katılabilir.

**Efor:** (a) ~1 saat + tablo testleri; (b) ~yarım gün. **Değer: 4.**

## 4. Baseline öğrenimi dar: yalnız servis RED ikilisi; alert kuralları sabit eşik; JVM/infra baseline'sız

**Dynatrace:** Davis her metrikte otomatik-adaptif baseline kurar (servis, endpoint, JVM, host).

**Coremetry:** Motor sağlam ama kapsam dar. İzlenen küme `DefaultAnomalyTracked` = error_rate + p99_ms (request_rate kapalı) — `internal/chstore/anomaly_tracked.go:42-48`. `internal/evaluator/`'da baseline tipi kural YOK (tek "baseline" geçişi bir yorum satırı, `evaluator.go:223`) — tüm alert kuralları sabit eşik. JVM heap/GC/CPU gibi infra metriklerinde öğrenilmiş baseline anomalisi yok; bunlar yalnız sabit eşikli kurallar + P1 deep-evidence olarak görünüyor. Binlerce JBoss'ta heap/GC davranışı servis başına çok farklı: sabit eşik ya gürültü ya körlük üretir.

**Öneri:** Anomaly motoruna infra metrik ailesi ekle (heap %, GC pause) — `metricValueExpr` + `metricPolicy` deseni genişlemeye açık (`anomaly.go:88-113`), seasonal/dwell/hacim kapıları hazır. VictoriaMetrics okuma backend'i açıkken metrik kaynağının VM yolundan gelmesi kontrol edilmeli.

**Efor:** ~2 saat/metrik ailesi (motor hazır). **Değer: 4.**

## 5. Problem birleştirme tek-tik pencereli; incident'ın birleşik kök-neden anlatısı yok

**Dynatrace:** Davis'te problem = birleşmiş varlık; dakikalara yayılan kaskad tek probleme katlanır, tepesinde kök neden durur.

**Coremetry:** Kümeleme (v0.9.1069) yalnız AYNI tikte "open" kararı almış adayları katlıyor; `clustering.go:239` açıkça "önceden açık problemler DOKUNULMAZ". Oracle yavaşlamasının 1. dakikada 2, 3. dakikada 5 servisi vurduğu tipik kaskadda erken açılanlar ayrı problem kalıyor ve geri katlanmıyor. Incident gruplama sezgisel: aynı-servis 30 dk VEYA 1-hop komşu (`internal/chstore/incident.go:365-418`) — nedensellik skorsuz, 2-hop kaskad ayrı incident'lara bölünür. Incident hipotez anchor'ı DEĞİL (anchor_kind ∈ {anomaly, problem}, `store.go:1284`) — /incidents sayfasında birleşik kök-neden anlatısı yok.

**Öneri:** (a) join-on-open: yeni open kararı, son N dk içinde propagation-bağlantılı açık küme/problem varsa ona üye olarak katılsın (retro-fold değil, açılış anında katılım — ucuz, mevcut `detectAnomalyClusters` girdisine açık problemleri eklemek); (b) incident satırına üye problemlerin en yüksek confidence'lı TopSuspect'ini taşı (salt okuma + FE, GetHypotheses batch okuması zaten var).

**Efor:** (a) ~2 saat; (b) ~2 saat. **Değer: 4.**

## 6. Retention: hipotez 30 gün TTL, verdict GÖVDESİ hiç kalıcı değil (yalnız 10 dk cache)

**Dynatrace:** Problem kartı ve Davis analizi problem yaşadığı sürece + arşivde kalır; postmortem aylar sonra da aynı analizi gösterir.

**Coremetry:** `root_cause_hypotheses` TTL 30 gün (`store.go:1299`), `anomaly_events` 30 gün (`store.go:1265`). `rca_verdicts` 90 gün AMA yalnız imza alanları: verdict/rc_entity/rc_fail_mode/confidence/shield_notes (`store.go:1584-1608`, `rca_record.go:30-43`) — Title, Summary, CausalChain, Remediation, Evidence YOK. Tam verdict gövdesi yalnız 10 dakikalık `serveCached`'te yaşıyor (`rootcause.go:552`). Sonuç: (a) hipotez TTL'de düşünce ✨ Explain dürüst 404 veriyor (`rootcause.go:492-497`) — 30+ gün sonra yazılan postmortem'de analiz üretilemez; (b) "o gün sistem ne karar vermişti" sorusuna nedensellik zinciri cevabı yok — `rca_record.go`'nun kendi gerekçesi ("kalkanların ürettiği fark hiçbir yerde kalmıyordu") gövde için hâlâ geçerli, çünkü kayıt yalnız imzayı tutuyor.

**Öneri:** `rca_verdicts`'e `body String CODEC(ZSTD(3))` kolonu (kalkan-sonrası RCAVerdict JSON'u) — boot-ALTER deseni hazır (`store.go:1304-1307` örneği), düşük hacimli state tablosu; incident/postmortem editörü geçmiş verdict'i exchange_id ile çeksin. Banka denetim kültüründe "sistemin verdiği karar" kalıcı olmalı; 90 gün ai_calls hizası yeterli başlangıç.

**Efor:** ~2 saat. **Değer: 4.**

---

# EK B2 — APM çekirdeği

# BOYUT 2 — APM Çekirdek (PurePath / hotspot / flow / dilimleme / N+1)

Önce dürüst tespit: trace waterfall beklediğimden çok daha güçlü — kritik yol (stripe + focus, `lib/criticalPath.ts`), span filtresi, servis başına self-time şeridi (`TraceWaterfall.tsx:105-124`), satır içi log chip'leri (≡n, v0.8.407), exception marker'ları, kategori chip'leri (DB/MQ/RPC/HTTP), dürüstlük paneli, baseline p50 karşılaştırması (`SpanDetail.tsx:126-144`), AI explain + kanıt kutulama. Endpoint sayfasında "Where the time goes" (`endpoints/detailSections.tsx:446`, `chstore/endpoints_downstream.go:100`) ve Callers; Dynatrace-tarzı backtrace sayfası (`ServiceBacktrace.tsx`) var. Boşluklar aşağıda — beşi de mevcut altyapının üstüne küçük ekler.

## 1. Span başına self-time hiçbir yerde görünmüyor
**Dynatrace:** PurePath'te her düğüm total VE self (exclusive) süre taşır; "bu 900ms'nin 40ms'i bu span'ın kendi kodu, kalanı çocuklarında" tek bakışta okunur. Response time hotspots kırılımının temelidir.
**Coremetry:** Self-time yalnız SERVİS granülaritesinde hesaplanıyor (`TraceWaterfall.tsx:105-124`, `TraceServiceBreakdown` — çocuk toplamını düşüp servise katlıyor). Span panelindeki Info bölümü sadece toplam `Duration` gösteriyor (`SpanDetail.tsx:254`); waterfall satırında da self-time yok. Kritik yol hangi zincirin duvar saatini sürdüğünü söylüyor ama zincirdeki tek tek span'lerde "kendi işi mi, bekleme mi" ayrımı yok — bankanın JBoss servislerinde 600ms'lik bir servlet span'inin altındaki 580ms Oracle çağrısını görmek için gözle çıkarma yapmak gerekiyor.
**Öneri:** (a) `SpanDetail` Info'ya "Self time" satırı — `duration − Σ(doğrudan çocuklar)`, spans zaten client'ta, sıfır backend işi; parent'tan children map'i prop olarak inmeli ya da panelde yeniden kurulmalı. (b) Waterfall bar tooltip'ine aynı sayı. (c) Opsiyonel: SpanFilterBar yanına "Top self-time" chip'i — trace'in en çok kendi-zamanı yakan 5 span'ine tıkla-git (kritik-yol focus'unun bire bir emsali, `Trace.tsx:622-632`).
**Efor:** ~1 saat. **Değer: 4/5** — hotspot sorusunun ("zaman NEREDE yandı") span-düzeyi cevabı; Dynatrace'ten gelen operatörün ilk arayacağı şey.

## 2. N+1 tespiti var ama trace'in kendisinde görünmez; ×N gruplama kodu yetim
**Dynatrace:** PurePath aynı sorgunun tekrarını doğrudan işaretler ("same statement executed 47×"); DB hotspot analizi N+1'i operatöre getirir, operatör aramaz.
**Coremetry:** Üç parça var, üçü de kopuk: (a) `spanRepeats` API'si tam bir N+1/chatty bulucu (`internal/api/api.go:4943-4972`) ama TEK tüketicisi Explore'un "Repeats" sonuç modu (`Explore.tsx:568`) — trace'e bakan operatör oraya gideceğini bilmiyor. (b) `TraceWaterfall`'un `groupSimilar` ×N sibling-gruplama özelliği (`TraceWaterfall.tsx:154,278-370`) tamamen uykuda: hiçbir çağıran `groupSimilar=true` geçmiyor (grep: yalnız bileşenin kendisi), "on" tüketicisi olan servis-yapı görünümü v0.9.226'da kalktı ve `AggregatedStructure.tsx` artık öksüz (`styles/undefinedCssRefs.test.ts:111` bunu açıkça söylüyor). (c) `DatabaseDetail`/`StmtDetailDrawer`'dan repeats moduna pivot yok — "bu statement'ı N+ kez koşan trace'ler" en doğal giriş kapısı olduğu hâlde.
**Öneri:** (a) Trace yüklendiğinde client-side tekrar sayımı — aynı `(service, displayName)` ≥5 kez geçiyorsa waterfall üstünde chip: "⚠ SELECT … 47× · toplam 1.2s" → tıklayınca span filtresini doldur + `groupSimilar` toggle'ını aç (kod hazır, sadece toggle + prop). (b) DB statement drawer'ına "N+1 trace'leri →" linki (Explore repeats, `db.statement` ön-dolu). Hibernate ağırlıklı banka prod'unda N+1 en sık gerçek kök neden sınıfı.
**Efor:** ~2 saat. **Değer: 5/5** — mevcut ama görünmez yeteneği tespitin yapıldığı yere taşıyor; ölü kodu (AggregatedStructure) ya diriltir ya silme kararını netleştirir.

## 3. "Where the time goes" endpoint'te var, SERVİS düzeyinde yok
**Dynatrace:** Response time analysis servis sayfasının merkezidir — "bu servisin süresinin %X'i DB'de, %Y'si dış çağrılarda, %Z'si kendi kodunda", zaman içinde kırılım grafiğiyle.
**Coremetry:** Bu kırılım yalnız `/endpoint` sayfasında (`WhereTheTimeGoesSection`, `EndpointDetail.tsx:249`; backend `chstore/endpoints_downstream.go:100` — 200 en-yavaş trace örnekleyip downstream + herhangi-derinlikte-DB payını ayrıştırıyor, çift-sayım notuyla). Servis Overview'da RED + Operations (Impact kolonu var, `OperationsTable.tsx:86`) + DbCard + ServiceNeighbors var ama hiçbiri "sürenin yüzde kaçı nerede" sorusuna cevap değil: DbCard mutlak istatistik, Neighbors çağrı sayısı/latency, ikisi de pay (%) vermiyor. Operatör 40 endpoint'li bir serviste kırılımı görmek için endpoint endpoint gezmek zorunda.
**Öneri:** `EndpointWhereTheTimeGoes`'un servis-scoped varyantı — `sampleEndpointTraces`'teki WHERE'i route yerine giriş-span ilkesiyle (`kind IN (server,consumer)` + `service_name=`) kur, `walkEndpointEdges` aynen yeniden kullanılır; Overview'a aynı panel bileşeni. Örnekleme + bound'lar (max_execution_time 10, LIMIT 200) hazır.
**Efor:** ~yarım gün. **Değer: 4/5** — Dynatrace'in servis sayfasındaki en çok kullanılan paneli; "hangi endpoint'e ineceğim" kararını tek bakışa indirir.

## 4. Span attribute'ından trace aramasına pivot yok (request-attribute dilimleme sürtünmesi)
**Dynatrace:** Request attribute'ları birinci sınıf: PurePath'teki bir attribute değerine tıkla → o değere göre filtrele/multidimensional analysis'e taşı.
**Coremetry:** Hedef altyapı tamamen hazır — /traces attribute filtreleri + `extraCols` attribute kolonları + aggregate `groupBy=attr` (`Traces.tsx:203,262`) ve Explore builder çoklu `splitBy` (`explore/model.ts:59`) + heatmap box-select BubbleUp. Ama kaynak taraftaki affordance eksik: `SpanDetail`'de attribute satırları yalnız kopyalanabilir; link SADECE servis-kimlikli üç anahtara çıkıyor (`SpanDetail.tsx:506-514`, `serviceAttrHref` — `Service`/`service.name`/`peer.service`). Trace'te `user.tier=premium` ya da `channel=mobil` gören operatör bu dilimi görmek için /traces'e gidip filtreyi ELLE kurmak zorunda — Datadog/Honeycomb'da bu tek tık.
**Öneri:** `Row`'a (attribute satırı) küçük bir "⧉ bu değerle trace'leri filtrele" eylemi: `/traces`'e advFilters ile `key = value` + span'ın zaman penceresi (pencere taşıma deseni `ServiceLinkCtx`'te v0.9.966'dan beri hazır — K1 dersini tekrar yaşamamak için pencere ŞART). İkinci adım (opsiyonel): "Explore'da grupla" varyantı `splitBy=[key]` ile.
**Efor:** ~2 saat. **Değer: 4/5** — dilimleme yeteneği zaten var; bu, onu keşfin başladığı yere (span paneli) bağlayan kayıp halka.

## 5. Büyük trace'lerde gezinme: minimap yok
**Dynatrace/Datadog:** Trace görünümünün üstünde daraltılabilir zaman-şeridi minimap'i — 10k+ span'lik trace'te "yoğunluk nerede" görüp brush'la o bölgeye atlama.
**Coremetry:** `GetTrace` 50k span'e kadar dönüyor, waterfall 1500+ span'de otomatik collapse yapıyor (`TraceWaterfall.tsx:211-217`) ve `content-visibility:auto` ile çiziyor — performans tarafı çözülmüş. Ama gezinme aracı yalnız collapse/expand + substring filtresi; batch/fan-out trace'inde (banka gecelik işleri) zamansal yoğunluğun nerede olduğunu gösteren özet şerit yok. `TraceServiceBreakdown` şeridi pay gösteriyor, zaman ekseni değil.
**Öneri:** Waterfall üstüne ~40px SVG yoğunluk şeridi (zaman kovalarına span sayısı/toplam süre, hata kovaları kırmızı) — tıkla → o kovadaki ilk span'i seç + scroll (mevcut `.wf-sel` scrollIntoView deseni, `Trace.tsx:288-298`). Brush-zoom şart değil; tıkla-atla yeterli başlangıç.
**Efor:** ~yarım gün. **Değer: 3/5** — normal trace'lerde nötr, batch trace'i debug'lanan gün altın.

## Kapsam dışı bıraktıklarım (bilinçli)
- **Servis flow görselleştirmesi:** `TopologyFlowGraph` flow-only modda gemide (v0.9.226'da toggle kalktı), db pill + namespace kutuları v0.8.447'de kapandı; env yarısı operatör iptali — yeniden önerilmedi.
- **Metod düzeyi görünürlük (PurePath methods):** `spanHotspots` + profil eşleşmesi SpanDetail'de zaten var (`SpanDetail.tsx:66-75,381-404`); derinleşmesi profil ingest'ine bağlı ve Profiling operatör kararıyla sidebar'dan gizli — yeniden açma önerisi yapılmadı.
- **Multidimensional analysis:** Explore çoklu splitBy + formül + BubbleUp + repeats ile Dynatrace MDA'nın karşılığını büyük ölçüde veriyor; boşluk yetenek değil, 4. bulgudaki giriş-kapısı sürtünmesi.

---

# EK B3 — Altyapı + k8s

**Katman haritası (koddan doğrulanmış):** Coremetry'de dikey zincir *sayfa gezinmesiyle* var: `/service-map` (servis+db/queue/external düğümleri + namespace kutuları) → `/services/:svc` Pods sekmesi (pod listesi + JVM) → `/pod` (RED+infra+JMX tek sayfa) → `/clusters` (Thanos: node/namespace/deployment/pod/alert). Tek görünümde katmanlaşma (Smartscape) YOK; ayrıca zincirde iki kopukluk var: harita→infra ve node→pod.

## 1. Topoloji haritasında altyapı bağlamı sıfır — dikey hop kopuk
**Dynatrace:** Smartscape'in özü dikey eksen — servis düğümünden process/host katmanına tek tıkla inilir; "bu servis nerede koşuyor, kaç instance, sağlıklı mı" haritadan ayrılmadan okunur.
**Coremetry:** `/service-map` düğüm çekmecesi (`frontend/src/components/ServiceMapNodeDrawer.tsx:60-108`, v0.9.1112) yalnız RED değerleri + Logs/Error traces/Endpoints pivotları veriyor. Pod sayısı, faz, restart, cluster — hiçbiri yok; altyapıya inen tek yol servise gidip Pods sekmesini açmak. Harita düğümleri de yalnız servis/db/queue/external (`frontend/src/lib/types.ts:3805-3822`); pod/host katmanı haritada hiç temsil edilmiyor.
**Öneri:** Tam katmanlı Smartscape görünümü büyük iş ve mockup-first kuralına girer — onun yerine ucuz dilim: çekmeceye "Infrastructure" bölümü. `useServicePods`'un zaten döndürdüğü özet (N pod, faz rozetleri, toplam restart, cluster adı) + Pods sekmesi linki. Fetch yalnız çekmece açılınca (ES-cost disipliniyle aynı).
**Efor:** ~2 saat. **Değer:** 4/5 — anomali anında haritadan "kod mu altyapı mı" ayrımına tek tık.

## 2. Node → pod bağlantısı yok — "bu sıcak node'da ne koşuyor" cevapsız
**Dynatrace:** Host view'da "processes on this host" listesi standart; doygun node'dan üzerindeki workload'lara tek tık.
**Coremetry:** `PodRow`'da node alanı yok (`internal/thanos/client.go:89-131`), `NodeRow`'da pod sayısı yok (`client.go:1002-1014`), `NodeHeatmap.tsx` tıklanamaz (onClick yok), `kube_pod_info` (pod→node etiketi) hiç sorgulanmıyor (`internal/thanos/promql.go`'da geçmiyor). Nodes tablosu %90 CPU gösterdiğinde operatör suçlu pod'u bulmak için Grafana/oc'ye dönmek zorunda — noisy-neighbor araştırması Coremetry'de kör nokta.
**Öneri:** `PodMetrics`'e `kube_pod_info` sorgusu ekle (ns+pod → node haritası, ghost-pod hattıyla aynı best-effort desen), `PodRow.Node` doldur; pods tablosuna Node kolonu, Nodes tablosu/heatmap satır tıklaması `?node=` filtresi yazar (URL kaynak-of-truth diliyle).
**Efor:** ~yarım gün. **Değer:** 4/5.

## 3. Restart NEDENİ görünmüyor — OOMKilled ayrımı yok
**Dynatrace:** K8s workload view'da OOMKilled/CrashLoopBackOff event'leri timeline'da; "restart var" ile "OOMKill fırtınası" ilk bakışta ayrılır.
**Coremetry:** Restart SAYISI var ve dürüstlüğü iyi işlenmiş (KSM `restartBy` + `RestartsUnknown` bayrağı, `internal/thanos/client.go:120-131`; ghost pods v0.9.368 `client.go:545`), ama neden yok: `kube_pod_container_status_last_terminated_reason` hiç sorgulanmıyor (thanos paketinde "OOM" yalnız yorumlarda geçiyor). Bankacılık JVM filosunda en sık ölüm nedeni OOMKill; bugün operatör sayıyı görüp nedeni için kubectl'e gidiyor. Not: Coremetry k8s API'ye erişmiyor, tüm k8s verisi Thanos PromQL'den geliyor — yani event feed'i değil, KSM metriği tek uygulanabilir kanal ve bu metrik tam o kanalda.
**Öneri:** `last_terminated_reason` sorgusu → pods tablosuna + `/pod` başlığına "Last termination" rozeti (OOMKilled=kırmızı, Error=sarı). `appendGhostPods`/`applyDeployKSM` ile aynı map-join deseni.
**Efor:** ~2 saat. **Değer:** 5/5 — restart kolonunun yanına tek kolon, teşhis süresini dakikalardan saniyeye indirir.

## 4. Workload (deployment) detayı yok — satır sadece pod filtresi
**Dynatrace:** Workload sayfası: restart trendi, rollout geçmişi, event listesi, replicas zaman serisi.
**Coremetry:** Tek-bakış workload sağlığı büyük ölçüde VAR ve iyi: KSM ready/desired + status (`internal/thanos/client.go:780,796`), scale-to-zero doğru sıralanıyor (`Clusters.tsx:78-81`), faz donut + firing alerts (`client.go:1185`) + namespace rollup restart/failing kolonları. Eksik olan zaman boyutu: deployment satırına tıklamak yalnız pod tablosunu filtreliyor; restart trendi, replicas-over-time, rollout geçmişi yok. (Span-tabanlı pod-churn rollout tespiti var — `internal/chstore/deploys.go:440-480` — ama yalnız instrument edilmiş servisler için ve /deploys gizli.)
**Öneri:** Workload satırına trend-drawer (namespace satırlarının v0.9.5 trend ikonu deseniyle aynı): CPU/Mem + restart artışı + ready/desired serisi.
**Efor:** ~yarım gün. **Değer:** 3/5.

## 5. /hosts gizli kalmalı — geri getirme (karar bulgusu)
**Dynatrace:** "Hosts" first-class çünkü VM/bare-metal ajanı var.
**Coremetry:** `Hosts.tsx` (v0.8.449) metric_points'ten `host.name` envanteri (`internal/chstore/hosts.go:97-126`); bankada host.name = pod adı (javaagent default — `RuntimeCharts.tsx:63` yorumu), yani sayfa `/clusters` pods görünümünün zayıf kopyası: faz yok, limit/request eksenleri yok, restart yok. Thanos hattı her metrikte üstün. v0.8.490 gizleme kararı doğruydu; k8s-dışı VM senaryosu bankada yok denecek kadar az. İki kopuk "host" kavramı (chstore host_name ↔ thanos node) tek Smartscape'te birleşmiyor ama bunun köprüsü zaten `/pod` sayfası (host_name=pod eşlemesi, `Pod.tsx:126`).
**Öneri:** Hiçbir şey yapma; sayfa açılmadıkça sorgu koşmuyor, bakım maliyeti sıfır.
**Efor:** 0. **Değer:** 2/5 (netlik).

## 6. JVM kartlarında monotonic counter'lar dışarıda — gerekçe bayatlamış
**Dynatrace:** Process view GC sağlığını pause + frequency BİRLİKTE verir; suspension/exception oranı standart bölme.
**Coremetry:** JVM ailesi yalnız heap/GC-pause/threads (`frontend/src/pages/service/RuntimeCharts.tsx:84-100`). Dosyanın v0.9.87 yorumu counter'ların dışarıda kalma nedenini "backend'de rate/delta yok" diye açıklıyor (`RuntimeCharts.tsx:33-35`) — bu artık YANLIŞ: metrik sorgu katmanı v0.9.106+718'den beri rate/increase destekliyor (`internal/chstore/metricquery.go:148-154`). Sonuç: GC frekansı olmadan "uzun ama seyrek pause" ile "kısa ama sürekli GC" ayrılamıyor; exception rate hiç yok.
**Öneri:** FAMILY_CARDS'a iki kart: "GC collections/s (by pod)" (`jvm.gc.duration` count'u ya da collection counter, agg=rate) + "Exceptions/s". Bayat yorumu da düzelt.
**Efor:** ~1 saat. **Değer:** 4/5.

## 7. JVM memory yalnız heap — Metaspace/non-heap kartı yok
**Dynatrace:** Process memory view heap + Metaspace/CodeCache ayrı çizer; Metaspace tükenmesi ayrı arıza sınıfı.
**Coremetry:** HEAP filtresi sabit (`RuntimeCharts.tsx:42`: `jvm.memory.type=heap`); non-heap hiçbir kartta yok. JBoss filosunda redeploy→classloader leak→Metaspace OOM klasik ve heap grafiklerinde GÖRÜNMEZ. `jvm.memory.used{type=non_heap}` stable semconv'da zaten akıyor — veri var, kart yok. (Pod sayfası JMX panelleri `jvm_memory_pool*` varsa kısmen gösteriyor ama servis-seviyesi görünümde değil.)
**Öneri:** "JVM non-heap (by pod)" kartı — mevcut heap kartının filters kopyası.
**Efor:** ~30dk. **Değer:** 4/5.

## 8. Thread state kırılımı yok
**Dynatrace:** Thread breakdown (runnable/blocked/waiting) — deadlock/pool-exhaustion teşhisinin ilk ekranı.
**Coremetry:** Yalnız toplam `jvm.thread.count` (`RuntimeCharts.tsx:97-99`). Semconv'da `jvm.thread.state` attribute'u var; groupBy ile tek kartta state kırılımı çizilebilir. JBoss thread-pool'ları JMX panellerinde (jboss_*) kısmen görünür ama JVM-seviyesi blocked/waiting oranı yok.
**Öneri:** Threads kartına `groupBy: jvm.thread.state` varyantı (pod fanout'la çakışmasın diye ayrı kart).
**Efor:** ~30dk. **Değer:** 3/5.

## 9. Sürekli profiling Java'ya kapalı (pprof-only)
**Dynatrace:** Always-on Java method hotspots + memory profiling — process-level detayın en derin katmanı.
**Coremetry:** Profil saklama pprof'a sabit (`internal/chstore/profile.go:9-17`), tip listesi cpu/heap/goroutine/alloc (`Profiling.tsx:36-41`) — Go'ya özgü. Bankanın JBoss/Java filosu hiç profil üretemiyor; sayfanın "hiç kullanmıyorum" diye gizlenmesi (Sidebar v0.8.489) muhtemelen bunun sonucu — özellik değersiz değil, filoya kapalı.
**Öneri:** async-profiler JFR→pprof dönüşümüyle (collector sidecar'ında) mevcut ingest hattını Java'ya açmak. Büyük iş; önce operatörle "Java profiling'i canlandıralım mı" kararı — kendi başına kuyruğa alma.
**Efor:** büyük. **Değer:** 3/5 (açılırsa 5, ama yatırım kararı operatörün).

---

# EK B4 — Alerting / ops yaşam döngüsü

# Boyut 4 — Alerting/Problem Yaşam Döngüsü + Ops

**Mevcut durumun özeti (koddan doğrulandı — güçlü taraf şaşırtıcı derecede geniş):** İki katmanlı bildirim dedup'u (15 dk birebir + 1 sa ciddiyet-bağımsız taban, eskalasyon deliğiyle — `internal/notify/problem_dedup.go:89,111`), kanal-başına alerting-profile eşdeğeri hedefleme (services/sreTeams/ownerTeams/clusters/quietHours+tz/minSeverity/minPriority — `internal/chstore/settings.go:90-110`, UI tam: `frontend/src/pages/settings/ChannelModal.tsx:52-65`), takım yönlendirme maili (owner+SRE, kalıcı dedup — `internal/notify/notify.go:487-495,678`), ayarlanabilir yaş-eskalasyonu (`internal/chstore/problem_escalation.go:23-51`), gürültülü kural paneli + AI önerisi + toplu uygula (`frontend/src/pages/alerts/NoisyRulesPanel.tsx`), kural preset'leri (`saved_views('alert-template')`, `Alerts.tsx:73-110`), ack/assign (`internal/api/api.go:884-885`), bildirim geçmişi (`internal/notify/notification_log.go:33`), anomali silence'ları (`internal/chstore/anomaly_silence.go`). Aşağıdakiler gerçek boşluklar.

## 1. Tekrarlayan (recurring) bakım penceresi yok
**Dynatrace:** Maintenance window'lar günlük/haftalık/aylık tekrar kuralıyla tanımlanır ("her gece 02:00–04:00 EOD batch", "her Pazar 01:00–05:00 bakım").
**Coremetry:** `MaintenanceWindow` tek atımlık — yalnız `StartAt/EndAt` (`internal/chstore/maintenance.go:28-38`); eşleme `IsMaintenanceActive` düz aralık kontrolü (`maintenance.go:135-153`). Tekrar alanı yok. Kanal-başına `quietHours` var ama o TÜM kanalı susturur, servis-kapsamlı değil.
**Banka bağlamı:** Gece EOD batch'leri her gece latency/error alarmı üretir; operatör ya her gün elle pencere açar ya da devasa tek pencere bırakır (ikincisi gerçek geceyarısı olayını da yutar).
**Öneri:** `MaintenanceWindow`'a `recurrence: ""|daily|weekly` + `weekday` alanı; `IsMaintenanceActive` saat-of-day karşılaştırmasına genişler (liste zaten bellekte taranıyor, CH şeması JSON değil ama ReplacingMergeTree'ye 2 kolon eklemek yeterli); MaintenanceTab formuna select. **Efor:** ~yarım gün. **Değer: 4/5.**

## 2. Kanal sağlığı görünmez — ölü SMTP/webhook sayfa kaçırtır
**Dynatrace:** Entegrasyon config sayfası bozuk entegrasyonu durum rozetiyle gösterir; bildirim hatası kendisi bir uyarıdır.
**Coremetry:** Her gönderim `notification_log`'a OK/hata yazılır (`internal/notify/notification_log.go:33-53`) ama bunu görmek için operatörün /events'e bakması gerekir. Settings→Channels sekmesinde son-gönderim durumu/ardışık hata göstergesi YOK (`frontend/src/pages/settings/ChannelsTab.tsx` — tek hata düzeltmesi K6 boş-durum ayrımı). Retry bilinçli yok (`notify.go:11` — "logged but do not block or retry"), yani tek savunma görünürlük ve o da pasif.
**Banka bağlamı:** SMTP relay şifresi döndü/webhook 403 oldu → kritik gece olayında kimseye sayfa gitmedi, ertesi sabah fark edildi. Missed-page bankada en pahalı hata sınıfı.
**Öneri:** (a) ChannelsTab'a kanal başına son gönderim sonucu + ardışık hata sayısı rozeti (`notification_log`'dan tek GROUP BY, `s.serveCached`); (b) N ardışık hatada self-problem aç (self-observability zaten var, `channel-broken` rule_id'li sentetik problem — DİĞER sağlıklı kanallardan duyurulur). **Efor:** ~2 saat (a) + ~2 saat (b). **Değer: 4/5.**

## 3. Kaskad olayda problem-başına fan-out — incident-farkında bildirim yok
**Dynatrace:** Davis korelasyonu ilişkili olayları TEK problem'e toplar; on-call bir DB çöküşünde 1 bildirim akışı alır, 10 değil.
**Coremetry:** Problemler açılışta topoloji-komşuluğuyla incident'a bağlanıyor (`internal/chstore/incident.go:356`, çağıranlar: `evaluator.go:898`, `slo_burn.go:118`, `db_capacity.go:401`, `anomaly.go:776`…), ama `SendProblemAlert` incident'tan habersiz — her üye problem her kanala ayrı ayrı gider (`notify.go:535-568`). Dedup tabanı yalnız AYNI problemin tekrarını keser; 10 FARKLI problem = 10 e-posta × kanal. (shared_exception burst'ü tek problem üretiyor ama o yalnız exception hattı.)
**Banka bağlamı:** Core-banking DB'si tökezlediğinde ona bağlı onlarca JBoss servisi problem açar; on-call'un telefonu susmayan bir yığına döner ve asıl kök-sebep maili yığında kaybolur.
**Öneri:** Kanal döngüsünden önce incident üyeliği kontrolü: problem, ZATEN bildirimi gitmiş açık bir incident'a bağlıysa tam fan-out yerine kısa "incident X'e eklendi: +servis" satırı (Slack/Teams) gönder ya da tamamen bastırıp incident timeline'a düş; opt-in `system_settings` vidası. **Efor:** ~yarım gün. **Değer: 4/5.**

## 4. On-call araçlarına hazır şablon yok — webhook el yapımı
**Dynatrace:** PagerDuty/Opsgenie/xMatters birinci-sınıf entegrasyon: routing key gir, severity eşlemesi + resolve olayı (dedup_key) otomatik.
**Coremetry:** Kanal tipleri email/slack/mattermost/teams/zoomchat/webhook/whatsapp (`notify.go:908-929`). PagerDuty Events API'ye ancak generic webhook + elle yazılmış template ile gidilir (`notify.go:1787-1808` yorumu bunu operatöre bırakıyor); resolve'da `dedup_key` eşlemesini, severity→PD severity çevirisini operatör template'te kendisi kurgulamalı — hata yapması kolay, test yolu yalnız "Test gönder".
**Banka bağlamı:** Bankalarda genelde şirket-içi alarm/SMS gateway'i veya on-prem Opsgenie var; hepsi webhook'la erişilir ama doğru payload'ı tutturmak entegrasyonun tüm riski.
**Öneri:** Webhook kanalına "şablon preset" dropdown'u: PagerDuty Events v2 (trigger/resolve + dedup_key=problem.ID), Opsgenie Alert API, düz JSON — mevcut `renderWebhookBody` template motoru (`notify.go:1839`) yetiyor, sadece hazır gövdeler + resolve yolunda doğru action alanı. Tam eskalasyon zinciri (ack yoksa X dk sonra insan-2) ÖNERMİYORUM — severity merdiveni + critical-gated kanal (`evaluator.go:1141-1148`) iki katmanı zaten veriyor. **Efor:** ~2 saat. **Değer: 3/5.**

## 5. Problem bağlamından tek tıkla susturma yok
**Dynatrace:** Problem kartından doğrudan maintenance window/alerting ayarına gidilir, entity önceden dolu gelir.
**Coremetry:** Bakım penceresi YALNIZ Settings→Maintenance sekmesinden, servis adı elle yazılarak açılır (`MaintenanceTab.tsx:56` — FE'de `MaintenanceWindow`'a dokunan tek dosya bu). Inbox triage drawer'daki mute anomali-fingerprint silence'ı (`InboxTriageDrawer.tsx:194`), kural-tabanlı problem için karşılığı yok; ack var ama ack bildirim geçmişini susturur, GELECEK yeniden-açılışı susturmaz.
**Banka bağlamı:** Gece olay anında "şu servisi 1 saat sustur, işi bitiriyorum" akışı Settings'e gidip form doldurmak demek — tam da elin titrediği an.
**Öneri:** Problem drawer'ına "Sustur… (30dk/1sa/4sa)" aksiyonu: servis+severity önceden dolu `UpsertMaintenanceWindow` çağrısı + audit kaydı; mevcut API zaten var, yalnız FE kısayolu + reason otomatik ("problem <id> üzerinden"). **Efor:** ~1 saat. **Değer: 3/5.**

## 6. Planlı özet (digest) raporu yok
**Dynatrace:** Zamanlanmış e-posta raporları (haftalık availability, günlük problem özeti).
**Coremetry:** Hiçbir digest/planlı özet yolu yok (internal/notify + evaluator taramasında sıfır sonuç); tüm bildirimler olay-anlık. Gece vardiyası sabaha karşı çözülen problemleri ancak /problems'ı elle gezerek görür.
**Banka bağlamı:** Ops yöneticisi sabah 08:00'de "gece ne oldu" ister: açılan/çözülen problemler, en gürültülü 5 servis, SLO burn. Parçaların hepsi mevcut (problems listesi, noisy rules raporu, SLO'lar, AI özet, SMTP hattı) — yalnız zamanlayıcı + kompozisyon eksik.
**Öneri:** Worker'da lider-kilitli günlük tik (Redis mutex kuralı mevcut desen): son 24 sa problem özeti + top noisy + SLO durumunu tek HTML maile derle, alıcı listesi `system_settings`'te; mevcut `buildEmailHTML` altyapısı (`notify.go:1140`) yeniden kullanılır. **Efor:** ~yarım gün. **Değer: 3/5.**

---
**Önceliklendirme önerisi (banka-prod değer/efor):** 2a (kanal sağlık rozeti, 2 saat) → 5 (tek-tık susturma, 1 saat) → 1 (recurring window) → 3 (incident-farkında fan-out) → 2b → 4 → 6.

---

# EK B5 — Günlük operatör deneyimi

# Boyut 5 — Günlük Operatör Deneyimi + Cila

Genel durum güçlü: ⌘K palet (sayfa+servis+endpoint+trace-id+aksiyon), görünür arama kutusu, `g x` kısayolları, "?" yardım modalı, Grafana-parite zaman seçici (recents+takvim+zoom-out), pinned/recent servisler, dashboard yıldızları, URL-durum disiplini, QueryError dürüstlüğü, dark/light/redhat + dil + yoğunluk kişiselleştirmesi. Aşağıdakiler kalan boşluklar.

## 1. ⌘K katalog adları sidebar adlarıyla AYRIŞIK ve aranamaz (i18n yok)
**Dynatrace:** global aramada bir yüzeyin TEK adı vardır; menüde ne yazıyorsa aramada o bulunur, yerel dilde de.
**Coremetry:** Palet kataloğu İngilizce sabit string: `CommandPalette.tsx:55-57` — "Inbox"→/inbox, "Problems"→/problems. Ama sidebar i18n'i farklı adlandırıyor: `i18n.ts:36` `'nav.inbox': 'Problems'` (TR: `:171` "Sorunlar"), `i18n.ts:39` `'nav.problems': 'Exceptions'` (TR: "Exception grupları"). Sonuç: sidebar'da **"Problems"** yazan girişin sayfası ⌘K'da **"Inbox"** adıyla; ⌘K'da "Problems" yazınca sidebar'ın **"Exceptions"** dediği sayfa açılıyor. Türkçe operatör "sorunlar" yazınca hiçbir şey bulamıyor (skorlama yalnız `r.label`, `CommandPalette.tsx:359-372`; `hint` metni de aranmıyor — "triage" yazınca Inbox çıkmıyor).
**Öneri:** palet etiketlerini i18n kataloğundan çöz + her girişe alias dizisi (EN+TR birlikte skorlanır), hint'i düşük ağırlıkla skora kat. `paletteReachability.test.ts` desenine "palet etiketi == nav etiketi" kontratı eklenebilir.
**Efor:** ~1saat · **Değer: 5**

## 2. Global arama kullanıcı-yaratımı nesneleri bulmuyor (dashboard/runbook/SLO/saved view)
**Dynatrace:** aramaya "payment" yazınca entity'lerin yanında o addaki dashboard'lar ve ayar ekranları da gelir; Grafana ⌘K'sı doğrudan pano arar.
**Coremetry:** palet yalnız statik sayfa kataloğu + servis + endpoint + trace-id + aksiyon arıyor (`CommandPalette.tsx:53-113, 292-343`). Operatörün yıldızladığı panolar (`Dashboards.tsx:19-28`, saved_views `dashboard-star`), runbook'lar (`api.ts:2691 api.runbooks`), SLO'lar ve kayıtlı görünümler (`api.ts:1149 savedViews`) ada göre bulunamıyor. Bankada takım başına panolar birikiyor; "eft-dashboard"a gitmek /dashboards + göz gezdirme.
**Öneri:** palet açılışında (pivotSvcs deseni, modül cache) yıldızlı+son panolar boş-sorgu rotasyonuna; sorguda dashboard/runbook adları da skorlanır (listeler küçük, mevcut list uçları yeter — servisteki gibi sunucu araması gerekmez).
**Efor:** ~2saat · **Değer: 4**

## 3. Arama kutusu "ayar ara…" vadediyor, ayarlar aranamıyor
**Dynatrace:** Settings'in kendi araması var; global arama ayar ekranlarını da döndürür.
**Coremetry:** görünür kutunun yer tutucusu "Servis, trace ID, sayfa veya **ayar** ara…" (`TopbarSearch.tsx:35`), ama palette Settings tek giriş (`CommandPalette.tsx:110` → /settings) ve 24 sekmenin (`Settings.tsx:50-84`: SMTP, LDAP, Retention, Pipeline, Danger zone…) hiçbiri katalogda yok. "ldap" ya da "retention" yazan admin boş sonuç alıyor — vaat/gerçek ayrışması en kötü sürtünme türü.
**Öneri:** "System · X" emsali (`CommandPalette.tsx:98-107`) gibi `Settings · SMTP`… girişleri TABS'tan türet (tek kaynak, sekme ekleyince otomatik). Not: palet sayfa kataloğu rol-filtresiz — viewer'a da Settings/Users görünüyor; aynı dokunuşta `adminOnly` işareti eklenebilir (sidebar `Sidebar.tsx:390-394` filtreliyor, palet filtrelemiyor).
**Efor:** ~30dk · **Değer: 4**

## 4. Share, göreli pencereyi dondurmadan kopyalıyor
**Dynatrace:** paylaşımda timeframe mutlak zamana sabitlenir — alıcı gönderenin gördüğü veriyi görür.
**Coremetry:** `ShareButton.tsx:53` `window.location.href`'i olduğu gibi kopyalar; URL'deki `?range=6h` göreli (`useUrlRange.ts` — preset URL'e yazılıyor). Incident kanalına atılan link ertesi sabah açılınca BAŞKA pencereyi gösterir; "bende öyle görünmüyor" diyaloğu bankada postmortem sırasında pahalı. (Sayfa-içi gezinme için preset'in URL'de göreli kalması v0.9.932'nin bilinçli kararı — o karar doğru; sorun yalnız KOPYALAMA anı.)
**Öneri:** ShareButton kopyalarken preset'i `custom:from-to`'ya çözerek kopyalasın (görüntülenen URL değişmez); istenirse "göreli kopyala" ikincil seçenek.
**Efor:** ~1saat · **Değer: 4**

## 5. Settings: 24 düz sekme, grupsuz + karışık dilde etiketler
**Dynatrace:** ayarlar gruplu ağaç + arama.
**Coremetry:** `Settings.tsx:50-84` — 24 sekme tek düz liste, sekme etiketleri ham string ve dil karışık: 'Metrik backend'i', 'Log köprüsü', 'Kod entegrasyonu' ile 'Data retention', 'Notification channels' yan yana; dil EN'e çevrilse de Türkçe etiketler kalıyor (i18n'e bağlı değil).
**Öneri:** sekmeleri 4-5 gruba ayır (Bildirim / Veri kaynakları / Kimlik-Erişim / Veri yaşam döngüsü / Gelişmiş — System sayfasının sub-nav deseni hazır) + etiketleri i18n anahtarına taşı.
**Efor:** ~2saat · **Değer: 3**

## 6. Hava-boşluklu prod'da işe yaramayan dış linkler (en kötüsü crash ekranında)
**Dynatrace:** hata ekranı destek akışına yönlendirir; air-gapped kurulumda ölü dış link bırakmaz.
**Coremetry:** ErrorBoundary'nin tek eylem çağrısı "Report issue ↗" → `github.com/cilcenk/coremetry/issues` (`ErrorBoundary.tsx:122`) — bankanın ağından erişilemez; en kırılgan anda ölü kapı. Ayrıca bu link el-yapımı `<a className="sec">` (buton atomu kuralına aykırı). `Login.tsx:304` opentelemetry.io, `AiTab.tsx:162` console.anthropic.com (müşteri lokal gemma kullanıyor) aynı sınıf.
**Öneri:** ErrorBoundary'de linki "Hata ayrıntılarını kopyala" (component stack + sürüm) butonuyla değiştir; GitHub linki yalnız announcement/branding'te dış link tanımlıysa göster.
**Efor:** ~30dk · **Değer: 3**

## 7. "Son görüntülenenler" yalnız servis; panolar/problemler/izler hafızasız
**Dynatrace:** "Recently viewed" tüm entity türlerini kapsar; sabah ilk iş dünkü bağlama tek tık dönüş.
**Coremetry:** MRU yalnız servisler (`recentServices.ts`, palet boş-sorgu rotasyonu `CommandPalette.tsx:268-283`) + metrik adları (`storage.ts:37`). Son açılan pano / problem / trace hiçbir yerde yok. Not: `recentServices.ts:5` "(and pickers)" diyor ama ServicePicker recents'i hiç kullanmıyor — ya bağla ya yorumu düzelt.
**Öneri:** aynı localStorage deseniyle `recentDashboards` + `recentProblems`; palet boş-sorgu rotasyonunda ★ pinned servis → son servisler → son panolar sırası.
**Efor:** ~2saat · **Değer: 3**

## 8. Yardım/kısayol keşfi tek tuşa gömülü
**Dynatrace:** kalıcı yardım menüsü (kısayollar, docs, destek).
**Coremetry:** kısayol modalı yalnız "?" ile açılıyor (`ShortcutsHelp.tsx:25-41`); kullanıcı menüsünde (`Sidebar.tsx:499-514`) yalnız Users/Settings/Şifre/Çıkış var — Help girişi yok. "?"ı bilmeyen operatör `g x` ailesini ve `/` odaklamayı hiç öğrenemiyor (arama kutusu yalnız / ve ⌘K'yı gösteriyor).
**Öneri:** kullanıcı menüsüne "Klavye kısayolları (?)" kalemi (modalı açar) — tek satır MenuItem.
**Efor:** ~30dk · **Değer: 2**

## 9. Global yenileme (auto-refresh) kontrolü yok — bilinçli kapsam dışıydı, karar tazelenebilir
**Dynatrace/Grafana:** zaman seçicinin yanında refresh dropdown'ı (off/30s/1m) + duraklat.
**Coremetry:** 2026-07-24 brief'i refresh dropdown'ını bilerek dışarıda bıraktı (`TimeRangePicker.tsx:27-29`); sayfalar kendi sabit aralıklarında (≥10s, document.hidden'da duraklıyor) polling yapıyor. NOC ekranına asılan bir /inbox ya da pano için operatörün tazeleme hızına müdahale kapısı yok. Yeniden açmak operatör kararı — polling bütçeleri (≥10s tabanı) korunarak yalnız "duraklat/2×yavaşlat" bile değer.
**Efor:** ~yarım gün · **Değer: 2**

---

# Quick win listesi (küçük ama her gün değen)

| # | İş | Kanıt | Efor |
|---|---|---|---|
| Q1 | TopbarSearch yer tutucu + aria-label sabit Türkçe — LangToggle varken EN kullanıcıya karışık dil | `TopbarSearch.tsx:32-35` | ~30dk |
| Q2 | Sidebar tablet tooltip'i sabit Türkçe | `Sidebar.tsx:376` | ~30dk |
| Q3 | Palet sayfa kataloğuna rol filtresi (viewer'a Settings/Users/AI görünmesin) | `CommandPalette.tsx:110-111` vs `Sidebar.tsx:390-394` | ~30dk |
| Q4 | Palet skoruna `hint` metnini düşük ağırlıkla kat ("triage"→Inbox) | `CommandPalette.tsx:359-372` | ~30dk |
| Q5 | ErrorBoundary'deki el-yapımı `<a className="sec">` → `<Button>`/`<LinkButton>` atomu | `ErrorBoundary.tsx:122-131` | ~30dk |
| Q6 | ServicePicker boş durumuna ★pinned + son servisler (⌘K'daki rotasyonun aynısı) | `recentServices.ts:5` vaadi, kullanan yalnız palet+Services | ~1saat |

Sıralama önerisi: 1 → 3 → 4 → Q1-Q4 tek dalga → 2 → 6 → 5 → 7 → 8; 9 operatör kararına.

---

