# Cluster değeri otomatik eşleme — tasarım (2026-08-28)

Operatör brief'i: Remote Cluster kaydı Thanos ve span cluster değerlerini
mümkün olduğunca KENDİSİ keşfeder; elle giriş son çare. Kısıt: bir cluster
değeri aynı anda yalnız BİR kayda bağlanabilir.

## Model (ClusterConfig, system_settings["thanos"])
- `thanosLabelName/Value` (var) + **`thanosLabelSource`** `auto|manual` +
  **`thanosLabelDetectedAt`** (ms). Boş kaynak = eski kayıt (elle sayılır).
- **`spanClusterValues []string`** (çoklu; geçiş dönemleri / farklı Collector
  konfigürasyonları). Eski `spanClusterValue` okunmaya devam eder ve listeye
  birleştirilir (`SpanClusterKeys()`); yazımda liste kanoniktir.
- Teklik: `ReconcileClusterSettings` aynı span değeri ya da aynı (etiket,
  değer) çiftini iki kayıtta görürse **reddeder** ve bağlı olduğu kaydı
  söyler (`"prod-eu-west" zaten prod-eu kaydına bağlı`).

## Thanos etiket algılama (thanos/cluster_detect.go)
- Ham sorgu (matcher ENJEKSİYONSUZ): `count by (cluster, cluster_id,
  cluster_name, k8s_cluster, openshift_cluster, prometheus, tenant, tenant_id)
  (kube_node_info)` — tek sorgu; olmayan etiketler boş gelir.
- Saf seçici `PickClusterLabel(rows, name)`: tercih sırasıyla ilk dolu etiket;
  değer = kayıt adına eşit/içeren değer > tek değer > **belirsiz** (adaylar
  döner, yazılmaz).
- Tetik: (a) `POST /api/settings/thanos/detect?cluster=` (admin, audit),
  (b) PUT'ta etiket adı boş + kaynak manual değilse best-effort (5 s/cluster),
  (c) "Test label" probe'unda etiket boşsa.
- Periyodik doğrulama: worker rolünde 10 dk ticker (LeaderHolder
  "cluster-label-check"): auto etiketli her kayıt için `count(kube_node_info)`
  matcher'lı → 0 seri = `labelStatus[id] = {ok:false, error, checkedAt}`
  (bellek; `/api/settings/thanos` snapshot'ında ve Settings rozetinde). Kayıt
  sessizce bozulmaz; UI "etiket artık eşleşmiyor" uyarısı gösterir.

## Span cluster değerleri
- `GET /api/settings/thanos/span-clusters`: `entity_seen_5m` (varsa) son 7 gün
  `GROUP BY cluster` → değer, span sayısı, ilk/son görülme; yoksa `spans`
  son 1 saat (zaman sınırlı, LIMIT 200, max_execution_time 15) + ilan.
  Her değer: bağlı kayıt (id/ad) ya da **eşleşmemiş**. 5 dk cache.
- Atama: `POST /api/settings/thanos/assign-span-cluster {value, clusterId,
  backfill}` (admin, audit) → blob'a `spanClusterValues += value` (teklik
  Reconcile'da), reload; `backfill=true` → o değer için son 24 saat span
  geçişi (entity_seen → pod/servis entity'leri) tek seferlik.
- Geriye dönüklük: eşleme OKUMA zamanında çözülür (byValue haritaları tüm
  değerleri anahtarlar) → tarihsel entity_seen satırları anında bağlanır.

## UI (Settings → Remote Clusters)
- Etiket alanının yanında `auto · 12:04` / `manual` rozeti, "Algıla" düğmesi,
  belirsizlikte aday seçimi; periyodik doğrulama uyarısı.
- Span değerleri çoklu çip alanı; "Span cluster değerleri" paneli: değer /
  span / ilk-son görülme / bağlı kayıt / **ata** seçimi; çakışma mesajı.

## Testler
Reconcile teklik (aynı değer iki kayıt → hata + kayıt adı; çoklu değer; eski
tekil birleşimi), `PickClusterLabel` tablo (ad eşleşmesi, tek değer, belirsiz,
etiket yok), `SpanClusterKeys` eşleşmesi, span-clusters SQL şekli.

## Uygulama durumu
- **v0.10.139 (dilim 1):** çoklu span değeri + teklik + auto alanları + tüm okuma
  haritaları; Snapshot yalnız AÇIK değerleri gösterir (Name yedeği örtük).
- **v0.10.140 (dilim 2):** etiket algılama (`cluster_detect.go`: enjeksiyonsuz
  `count by (adaylar) (kube_node_info)`, `PickClusterLabel` — güçlü etiketlerde
  tek değer kabul, zayıf etiketlerde (prometheus/tenant) yalnız ad eşleşmesi,
  aksi belirsiz), `POST /api/settings/thanos/detect?cluster=&apply=1` (admin,
  audit, teklik Reconcile'da), PUT'ta yeni kayıt için 6 s toplam bütçeli
  best-effort algılama, probe'da öneri; mevcut matcher boş sonuçla SİLİNMEZ.
  Periyodik doğrulama: worker rolünde lider ticker (10 dk + liderlik anında),
  sonuç `labelCheck` (bellek; her tick sıfırdan; kayıt sonrası sıfırlanır +
  taze denetim); Settings'te "Detect label", auto/manual rozeti, aday çipleri,
  eşleşmeme uyarısı.
- **Sıradaki (dilim 3):** span cluster değerleri listesi + atama + 24 s backfill.
