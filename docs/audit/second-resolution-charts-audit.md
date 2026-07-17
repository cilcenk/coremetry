# Audit — span-metrik grafiklerinde tutarlı saniye-çözünürlük (ÖLÇÜM-destekli)

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Yöntem:** 6 paralel ölçüm görevi, canlı lokal CH'de gerçek sorgular
(system.query_log'dan read_rows/read_bytes/duration) + kod okuması.
Hedef operatör tanımı: "tümü saniye" DEĞİL — performansı bozmadan
mümkün olan her yerde tutarlı en ince çözünürlük.

## 1. Nokta bütçesi (M1) — merdiven sağlam, İKİ gerçek delik var

- metricAutoStep (metricresolve.go:109-129) 6h+1s ÜRETMEZ: 6h→60s
  (360 nokta); 7 sınırlı dalın worst-case'leri 120-360 nokta — ≤500
  bütçe İÇİNDE. bucketForWindow aynı şekil; endpoints'in
  windowSec/30-min-60'ı yapısal ≤31 bucket.
- **Delik 1 — clamp'siz ?step:** TÜM API giriş noktaları
  (api/metricresolve.go:67, api.go ?step=/body.Step/req.Step,
  dql.go plan.StepSeconds) step'i süzgeçsiz geçirir; step>0
  merdiveni ATLAR ve selectMetricTier 1s'i seçer. **Canlı ölçüm:**
  6h+step=1 → api-gateway'de 16.925 dolu 1s bucket (teorik 21.600)
  = bütçenin ~43 katı. Bugün UI bunu üretmiyor (stepForWidth
  [120,720] kelepçeli) ama API/DQL üzerinden erişilebilir.
- **Delik 2 — >20.8 gün penceresi:** default dal 3600s step'le
  sınırsız nokta (30g=720, 90g=2160); pencere clamp'i yok.

## 2. Raw-spans riski — GetSpanBreakdown (M2): kapsam DIŞI + iki küçük fix

- Breakdown raw `spans` okuyor (breakdown.go:52-67) — invariant
  #3'ün LAFZINA aykırı ama MADDİ olarak zorunlu: multiIf kategorisi
  (db:{db_system} / queue:{msg_system} / http) hiçbir MV'de olmayan
  kolonlara bağlı (spanmetrics_10s DESCRIBE: yalnız
  service/name/kind/status/http_route).
- **Ölçüm (lokal):** raw yol MV'den PAHALI ÇIKMADI — 2dk: ~0,9×
  rows / ~0,65× bytes; 30dk: 0,77× rows / 0,50× bytes (MV, state
  kolonları + op×bucket kardinalitesi yüzünden fazla byte okuyor).
  Uyarı: gerçek satır oranı (raw/MV) 2dk'da 1,16× → 30dk'da 1,41× —
  pencere/trafik büyüdükçe makas raw aleyhine açılır (1B span/gün
  prod'da ekstrapolasyon).
- **Karar:** breakdown saniye kapsamından ÇIKARILDI — mevcut 10s
  tabanı kalır (kategori boyutu MV'siz; saniyeye inmek raw taramayı
  DARALTMAK yerine sıklaştırırdı). İki eyleme dönük bulgu:
  (a) sorguda **LIMIT YOK** — hard-constraint ihlali (max_execution_
  time + time-bound var, LIMIT eksik); (b) yorumdaki "~60-150
  bucket" iddiası bayat (6h dalı 360 üretir) — yorum düzeltilir.

## 3. Tier+TTL haritası (M3) — kod ↔ canlı birebir

| Okuma yolu | Bugünkü kaynak | Step | 1s'e inebilir mi |
|---|---|---|---|
| Metrics/resolver (servis RED, Explore, Incident, ConditionPreview) | selectMetricTier (1s/10s/1m dinamik) | metricAutoStep | ✅ ZATEN iniyor (route'suz) — dokunulmaz |
| Endpoint grafikleri + drawer | **sabit spanmetrics_1m** (endpoints.go:403, endpoints_detail.go:359) | windowSec/30, min 60s | ❌ yapısal: 1s tablosunda http_route KOLONU YOK (canlı DDL doğrulandı; selectMetricTier:191 `needRoute && !hasRoute → continue`) — tavan 10s |
| Span breakdown | raw spans | bucketForWindow (min 10s) | Kapsam dışı (§2) |
| Evaluator | sabit spanmetrics_1m | — | Kapsam dışı (alerting bu işin konusu değil) |

Canlı TTL'ler DDL'den doğrulandı: 1s=6h, 10s=2d, 1m=30d; son 1h
hacimler: 1s=139K, 10s=106K, 1m=44K satır.

## 4. ClickHouse yükü (M4) — ölçülen oranlar

| Pencere | Step/kaynak | read_bytes | süre (med) | nokta |
|---|---|---|---|---|
| 2dk | 1s (spanmetrics_1s) | 5,0 MiB | 30ms | 120 |
| 2dk | 10s (spanmetrics_10s) | 11,4 MiB | 66ms | 12 |
| 30dk | 5s (1s'ten toStartOfInterval) | 78,6 MiB | 65ms | 361 |
| 30dk | 10s | 53,0 MiB | 57ms | 180 |
| 30dk | 60s | 12,9 MiB | 124ms* | 30 |
| 6h | 10s | 267,3 MiB | 188ms | 2160 |
| 6h | 60s | 79,9 MiB | 106ms | 360 |

\*gürültü; maliyet sinyali read_bytes. Bulgular: (1) kısa pencerede
1s tablosu dar sort-key'i sayesinde 10s'ten 2,3× UCUZ — canlı
pencerede saniye çözünürlüğün CH maliyeti yok denecek düzeyde;
(2) 6h@10s hem 3,3× pahalı hem 2160 nokta üretir (ekran genişliğini
aşar — görsel kazanç SIFIR): kaba pencerede ince step anlamsız;
(3) serveCached 30s → yük O(kullanıcı) değil O(benzersiz görünüm):
en pahalı sorgu bile anahtar başına dakikada ≤2 koşar.

## 5. TTL-dışı davranış (M5) — temiz, bir yan-bug bulundu

- TTL merge-tembel: 7,5h önceki pencerede 1s tablosunda hâlâ 42,5K
  satır VAR (fiili silme uçurumu ölçüm anında now-9h47m); 10,5h
  öncesi 0. Kod NOMİNAL TTL'e göre tutucu (metricresolve.go:194) →
  1s'e izin verilen hiçbir okuma silinmiş bölgeye denk gelemez;
  TTL dışına taşan pencere TÜMÜYLE üst tier'a düşer — kısmi dikiş
  ve interpolasyon yapısal olarak yok (":430 honest 3-line band").
- **Yan-bug:** spanmetricsCoverageStart (:564) sınırsız
  `SELECT min(time_bucket)` — canlıda TTL-budanmış partların BAYAT
  metadata'sından sahte-eski değer döndürdüğü ÖLÇÜLDÜ (00:16 vs
  gerçek 08:17). Zaman-sınırlı WHERE ile düzeltilmeli.

## 6. Frontend (M6) — downsample GEREKMEZ

MultiLineChart marker'sız/fill'siz saf stroke çizer; 8×1000 nokta
maliyet sınıfı önemsiz (TimeSeriesPanel kendi LTTB'siyle 2000/seri
kabul ediyor). Koruma bileşen dışında: stepForWidth [120,720] +
backend merdiveni. Değişiklik önerilmiyor.

## 7. Karar + dilim planı (onaya sunulan)

**Saniyeye İNEN:** hiçbir yeni yüzey — Metrics/resolver hattı zaten
doğru. **İNCELEN:** endpoint grafikleri (60s tabanından tier'lı
okumaya, 10s tavanıyla). **TAVANLANAN:** breakdown (10s, kategori
MV'siz), route'lu her sorgu (10s, şema gereği), evaluator (dokunulmaz).

| Dilim | İçerik | Ölçüm dayanağı | Tahmin |
|---|---|---|---|
| R1 | Sunucu-tarafı step/pencere bütçe kelepçesi: TÜM giriş noktalarında `step = max(step, ceil(span/720))` (merdiven rung'ına yuvarlanır) — Delik 1+2 kapanır; UI davranışı DEĞİŞMEZ (stepForWidth zaten ≤720 üretiyor) | §1: 43× bütçe aşımı canlıda üretildi | ~45 dk |
| R2 | Endpoint grafikleri: sabit spanmetrics_1m yerine tier'lı okuma (min bucket 10s, hasRoute kısıtı doğal tavan) — ≤10dk pencere 60s yerine 10s çözünürlük kazanır | §4: 30dk@10s 53 MiB/57ms bütçe içi; §3 yapısal tavan | ~1,5 sa |
| R3 | Breakdown'a LIMIT (hard-constraint) + bayat yorum düzeltmesi + spanmetricsCoverageStart'a zaman-sınırlı WHERE (sahte-eski coverage fix'i) | §2 LIMIT eksiği; §5 ölçülen bayat metadata | ~30 dk |

Kısıt teyitleri: yeni MV yok, ingest/TTL dokunulmuyor, 1s
hasRoute=false ihlal edilmiyor (R2 tavanı 10s), resolver davranışı
yalnız kelepçeyle (kötüye-kullanım girdilerinde) değişiyor, raw-spans
yolu (breakdown) LIMIT kazanıyor.
