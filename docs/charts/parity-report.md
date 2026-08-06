# Parite Kanıt Koşumu — FAZ 4 (Mod A)

Tarih: 2026-08-06 · araç: `cmd/paritycheck` · ortam: LOKAL minikube (demo yükü)
Yöntem: Coremetry `/api/metrics/query` ↔ aynı pencere/kafes için bağımsız
ClickHouse ham-gerçek SQL'i; nokta sayısı, kafes hizası, değer farkı
(oransal 1e-9 toleransı), null konumları. Çıkış kodu CI'a bağlanabilir
(`HATA` → exit 1).

## Koşum 1 — gauge + çok-yazarlı cumulative

`oracledb.sessions.usage` (gauge) · `oracledb.physical_read_io_requests` (cumulative) · step 60s · 30 dk

| Sınıf | Kontrol | Sonuç |
|---|---|---|
| KABUL | gauge/kafes | 30 nokta, hepsi 60s kafesinde |
| **KABUL** | **gauge/avg değer** | **30/30 bucket birebir** (fp toleransı içinde) |
| KABUL | rate/kafes | 30 nokta kafeste |
| HATA→**BEKLENEN*** | rate değer | 30 bucket sapıyor — *kök neden KAYNAK: aynı fingerprint'e yazan İKİ üretici (demo pod ikizi, 1.4 sn ofset; çözünürlük denetimi §1 aliasing bulgusu). İki bağımsız sayaç akışı iç içe → hem sunucu hem gerçek-SQL tanımsız girdide farklı sonuç. Tek-yazar varsayımı ihlali; sunucu hatası DEĞİL.* |

## Koşum 2 — tek-yazarlı cumulative

`process.runtime.go.mem.heap_alloc` (gauge) · `process.runtime.go.cgo.calls` (cumulative) · step 60s · 30 dk

| Sınıf | Kontrol | Sonuç |
|---|---|---|
| **KABUL** | **gauge/avg değer** | **31/31 bucket birebir, uç farkı 0** |
| **HATA (gerçek sapma)** | rate | API **boş seri**, gerçek 30 bucket. Kök neden: satırlar `is_monotonic=0` damgalı geliyor (kaynak işaretlememiş) ve cumulative-rate SQL'i `is_monotonic = 1` filtreliyor (v0.9.379 bilinçli kararı: non-monotonik'te her düşüş "reset" sayılırdı). Sonuç **sessiz boş grafik** — Prometheus aynı seriye `rate()` cevabı verir. |

## Hüküm

- **Gauge zinciri parite düzeyinde**: iki bağımsız hesap bucket-bucket birebir.
- **Rate zincirinde bir sapma sınıfı belgelendi**: işaretsiz-monotonik cumulative
  → sessiz boş. Aday düzeltme (ayrı dilim, operatör onayı ister): `is_monotonic=0`
  VE pencerede negatif delta oranı ≈ 0 ise monotonik say (Prometheus davranış
  eşleniği); ya da en azından boş yerine `unsupportedInstrument` benzeri açık
  tanılama döndür — sessiz boş, bu kod tabanının altı kez düzelttiği sınıf.
- Kaynak-aliasing (iki yazar/tek fingerprint) demo artefaktı; prod'da
  service.instance.id ayrımı varsa oluşmaz. Denetim raporu §1'e bağlı.

## Mod B (Prometheus yan yana)

Bu koşuda YOK — lokalde Prometheus yok. Araç `-prom-url` destekliyor
(`/api/v1/query_range` nokta-nokta kıyas); operatörün test ortamında
(Prometheus 60 sn saklıyor) koşulması yeterli:

    go run ./cmd/paritycheck -base <coremetry> -cookie-file <jar> \
      -prom-url http://<prometheus:9090> -service <svc> -metric <ad> -step 60

## CI bağlama

    make parity   # HATA bulursa kırılır
