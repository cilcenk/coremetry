# Audit — getServices errgroup paralelizasyonu (v0.8.530)

2026-07-15 · Prod'da v0.8.530 sonrası CH bacağında yavaşlık raporu.
v0.8.531 salt frontend/uPlot — CH'ye dokunmuyor, kapsam dışı. Kod
referansları: `internal/api/api.go:1483-1546`.

## Kısa hüküm

Paralelizasyon **doğru** (yarış yok, `rows.Close` garanti, hata yönetimi
sağlam) AMA **her recompute'un eşzamanlı CH bağlantı ayak izini 1'den
3'e çıkardı**. Prod'daki yavaşlığın en olası kökü bu: paylaşımlı
bağlantı havuzu, eşzamanlı-farklı-anahtar yükünde 3× talep altında
tükenip bağlantı-edinme kuyruğuna girebiliyor. Kesin race/leak/partial
bug'ı YOK.

## 1. Context paylaşımı + iptal davranışı

- `g, gctx := errgroup.WithContext(ctx)` (api.go:1498) — üç goroutine
  **aynı `gctx`'i** paylaşır.
- Yalnız LİSTE bacağı hata döndürür (api.go:1506); sayaç (1514) ve total
  (1523) **her zaman `return nil`**.
- errgroup `gctx`'i **ilk non-nil hata**da iptal eder → yani yalnız liste
  bacağı hata verirse. O an sayaç/total sorguları uçarken iptal olur
  (`s.conn.Query(gctx,…)` → `context.Canceled`), ama bu goroutine'ler
  hatayı yutar (`perr == nil` / `terr == nil`) → `haveCounts/haveTotal`
  false kalır.
- Sonra `g.Wait()` liste hatasını döndürür → `return nil, err` → 500.

**İstenen mi?** Evet. Liste ana yük; o olmadan gösterilecek bir şey yok,
500 doğru. Başarı yolunda gctx asla iptal olmaz (soft bacaklar nil
döner), sayaç/total normal tamamlanır. **Partial/yanlış sonuç yok.** Tek
yan etki: liste-hata yolunda sayaç/total sorguları BAŞLATILIP iptal
edilir — seri sürümde hiç koşmuyorlardı (early return). Hata yolunda
CH'ye 3 sorgu başlar (2 iptal), seri'de 1. Küçük ek hata-yolu yükü,
correctness sorunu değil.

## 2. Bağlantı havuzu — ANA BULGU

- `conn driver.Conn` (store.go:19) — native clickhouse-go v2, **kendi
  içinde bağlantı havuzu**; her `Query()`/`QueryRow()` havuzdan bağlantı
  edinir.
- Havuz: `MaxOpenConns: maxConns` (store.go:328), `MaxIdleConns: maxConns/2`.
- Varsayılan: `resolveMaxOpenConns` = `ingestSignals(5)*workers + 8`
  (config.go:757-765); workers=8 → **48**. Load'suz doğrudan-CHConfig
  fallback → **24** (store.go:196). Havuz TÜM app ile paylaşımlı (ingest
  flusher'ları, evaluator, anomaly, diğer API uçları).
- **Seri sürüm:** recompute başına AYNI ANDA 1 bağlantı (sorgular sırayla).
- **Paralel sürüm:** recompute başına AYNI ANDA 2-3 bağlantı, en yavaş
  bacağın süresi boyunca.
- **singleflight** (`sf.Do`, cache.go:47) yalnız AYNI cache anahtarını
  dedup eder. FARKLI anahtarlar (farklı page/filter/range/env/team, farklı
  kullanıcı) eşzamanlı recompute olur. Yani eşzamanlı-farklı-anahtar
  sayısı × **3** bağlantı talebi.
- **Tetikleyici:** soğuk cache (deploy/Redis blip/MV invalidation) anında
  tüm farklı anahtarlar aynı anda MISS → her biri 3 bağlantı → havuz
  (24-48) tükenir → bağlantı-edinme kuyruğu → getServices VE havuzu
  paylaşan diğer her şeyde gecikme. Seri sürüm bu talebi hiç 3'e
  katlamıyordu.

## 3. Shared değişkenlerde yarış

- `rows` yalnız liste-goroutine, `counts/haveCounts` yalnız sayaç-goroutine,
  `total/haveTotal` yalnız total-goroutine yazar (api.go:1491-1524). Her
  değişken **tam bir yazar**; `g.Wait()` bariyerinden SONRA okunur
  (happens-after). Aynı değişkene eşzamanlı erişim yok → **yapısal olarak
  yarışsız**, mutex gereksiz.
- **Test kapsamı:** getServices'i eşzamanlı süren bir unit test YOK; bu
  yüzden `go test -race ./internal/api/` bu bloğu fiilen tetiklemiyor.
  Yarışsızlık yapıdan geliyor (tek-yazar + bariyer), ama otomatik -race
  kanıtı yok — audit fix'inde regresyon testi eklenebilir.

## 4. rows.Close / bağlantı sızıntısı

- `GetServicesAggFilteredIn`: `defer rows.Close()` (summary.go). ✓
- `GetOpenProblemCountsByService`: `defer rows.Close()`. ✓
- `CountServicesAgg`: `QueryRow` — kapatılacak rows yok. ✓
- Üçünde de `defer` erken-return/panic'te kapatmayı garanti eder. İptal
  edilen (liste-hata → gctx cancel) sorguda da deferred Close bağlantıyı
  havuza iade eder. **Sızıntı yok.**

## 5. Partial success

- Liste OK + sayaç hata → sayaç nil (soft), `haveCounts=false` → health
  skorlanmaz, liste health/openProblems alanları sıfır-değerle 200 döner.
  **v530-öncesi seri davranışın AYNISI** (eski `if perr == nil` de soft
  düşerdi). 500 değil, yanlış veri değil — sadece health çipsiz.
- Liste hata → 500 (`g.Wait()` üzerinden). ✓
- total soft-fail → `total` alanı yanıttan atlanır (haveTotal=false), UI
  First/Last pager'ı Next/Prev'e düşer. Eski davranış. ✓

## 6. Sorgu deseni / timeout / deadline farkı

- Sorgular ve SETTINGS aynı: liste `max_execution_time=30` +
  `mvQuantileMemSettings`, sayaç `=5`, total `QueryRow`. Değişmedi.
- Context: seri `ctx` (singleflight recompute ctx = `r.Context()`);
  paralel `gctx` (aynı ctx'ten türetilmiş + ilk-hata-iptali). Driver
  `ReadTimeout=30s` (store.go:328) ve server-side `max_execution_time`
  değişmedi.
- Tek fark: seri'de liste hatası sayaç/total'ı hiç koşturmuyordu (early
  return); paralel'de üçü birlikte başlar. §1 + §2'de işlendi.
- Not: `serveCached`'in 2s detached goroutine'i (cache.go:289) yalnız
  **L2 (Redis) write-back** için; recompute fn'ini bağlamaz. Recompute
  budçe sorunu yok.

## Fix seçenekleri (minimal → daha kapsamlı)

**A. Seri'ye geri dön (EN GÜVENLİ, tam geri döndürülebilir).** v530
öncesi bağlantı-ayak-izini birebir geri getirir. Kazanç zaten marjinaldi
(30s-cache'li, warm'da sub-saniye endpoint'te birkaç yüz ms soğuk
recompute). Risk sıfır. **Önerilen.**

**B. Sayaç bacağını page-değişmez olduğu için AYRI cache'le (asıl doğru
fix, daha kapsamlı).** `GetOpenProblemCountsByService` (problem.go)
page/filter/env'den BAĞIMSIZ — TÜM servislerin sayacını döner, her
services isteği için AYNI. Şu an her farklı-anahtar recompute'unda
yeniden koşuyor (seri'de bile israf). Ayrı 30s cache'e alınırsa 30s'de
BİR kez hesaplanır, kaç farklı anahtar recompute olursa olsun → toplam
CH yükü seri-baseline'ın bile ALTINA iner ve paralelizasyon tümüyle
kalkar. Daha fazla iş; ama yükü asıl düşüren bu.

**C. errgroup'u SetLimit(2) ile sınırla / listeyi önce koş.** Peak
bağlantıyı 2'ye indirir ama bir bacağı seri'leştirir — marjinal, A/B
kadar temiz değil.

**D. Havuzu büyüt (MaxOpenConns↑).** Kök asimetriyi çözmez, yükü CH
server tarafına kaydırır. Önerilmez.

## Öneri

**A (seri revert) hemen** — prod yangınını söndürür, sıfır risk. Ardından
**B (sayaç ayrı cache)** ayrı bir sürümde, yükü baseline altına indirmek
için. İkisi de minimal + geri döndürülebilir; A tek başına yeterli, B
opsiyonel iyileştirme.
