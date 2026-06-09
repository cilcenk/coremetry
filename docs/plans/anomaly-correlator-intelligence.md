# Coremetry — Anomaly & Correlator paketlerini "akıllı" hale getirme görevi

> Bu metni olduğu gibi Claude Code'a (repo kökünde) yapıştır. Fazlar bağımsız
> olarak shipping edilebilir; her fazı ayrı bir commit + ayrı bir
> `vX.Y.ZZZ` sürüm yorumu olarak ilerlet.

---

## Bağlam ve hedef

`internal/anomaly/` ve `internal/correlator/` paketleri çalışıyor ve ölçek/HA
açısından sağlam. Amaç bunları **istatistiksel olarak daha sağlam** ve
**sinyalleri birbirine bağlayarak daha akıllı** yapmak — sıfırdan yeniden yazmak
DEĞİL. Mevcut z-score, asimetrik baseline pencereleri, tokenbf prefilter,
materialized view okuma desenleri ve leader-election yapısı korunacak.

## Önce oku (kod yazmadan)

1. Repo kökündeki `CLAUDE.md` — sürüm/test/commit kuralları.
2. `internal/anomaly/{anomaly,trace_ops,log_patterns,recorder,problem_explainer}.go`
3. `internal/correlator/correlator.go` ve `internal/correlator/correlator_test.go`
4. `internal/chstore/` içinde: `GetServiceAdjacency`, `AttachProblemToIncident`,
   `FindOpenProblem`, `UpsertProblem`, `service_summary_5m` ve `topology_edges_5m`
   tanımları. Şemaya dokunman gerekirse `internal/chmigrate` üzerinden migration yaz.
5. Mevcut test stili: `correlator_test.go`, `sampling_test.go`,
   `log_templates_test.go` — yeni testleri bunlara benzet.

## Genel kurallar (her faz için geçerli)

- **Kod, identifier ve yorumlar İngilizce** (mevcut kod tabanıyla tutarlı). Bu
  görev metni Türkçe; ürettiğin kod İngilizce olacak.
- Davranışı değiştiren her eşik/parametre **constant veya config** olarak
  expose edilsin (mevcut `openZ`, `resolveZ`, `minMagnitude` desenindeki gibi) —
  sihirli sayı gömme.
- **HA korunur:** yeni periyodik iş eklersen `cache.LeaderHolder` ile gate'le.
  Mevcut detektörler tek leader'da koşuyor; bunu bozma.
- **Ölçek korunur:** ham `spans`/`logs` taraması yerine mümkünse MV'den oku;
  her yeni CH sorgusuna `SETTINGS max_execution_time` ve partition pruning ekle.
  Milyar-span/gün varsayımıyla yaz.
- **Her faz bir regression testiyle gelir.** Faz, `go test ./internal/anomaly/...`
  ve `go test ./internal/correlator/...` yeşil olmadan bitmiş sayılmaz.
  Concurrency'ye dokunursan `go test -race ...` da koş.
- **Geriye dönük uyum:** `Problem` ve `AnomalyEvent` satırlarının mevcut alanları
  bozulmaz; yeni alan eklersen nullable/opsiyonel ekle.
- Her fazı kendi `vX.Y.ZZZ` yorum bloğuyla işaretle (mevcut konvansiyon).
- **Scope guard:** Bu görevde sadece `internal/anomaly/`, `internal/correlator/`,
  gereken `internal/chstore/` okuma yardımcıları ve gereken `chmigrate` migration'ı
  değişecek. UI/frontend, API router (`internal/api/api.go`) ve OTLP ingest'e
  DOKUNMA — onlar ayrı görevler.

---

## FAZ 1 — Robust z-score (median + MAD)  · en yüksek getiri, en küçük değişiklik

**Dosya:** `internal/anomaly/anomaly.go`

**Sorun:** `meanStdev` population mean+stdev kullanıyor; ikisi de baseline
penceresindeki kendi outlier'larına karşı kırılgan. Dünkü bir spike stdev'i
şişirip bugünkü spike'ı maskeliyor (z küçülüyor).

**Yap:**
- `medianMAD(xs []float64) (median, mad float64)` helper ekle. MAD = median of
  `|x_i - median|`.
- `checkOne` içinde modified z-score'a geç:
  `z = 0.6745 * (current - median) / mad`. (`0.6745`, MAD'i normal dağılımda
  stdev'e ölçekleyen sabit.)
- `mad < 1e-9` durumunu mevcut `stdev < 1e-9` skip'i gibi ele al (sabit baseline).
- `meanStdev`'i SİLME — başka çağıranı olabilir; sadece `checkOne`'ı `medianMAD`
  kullanacak şekilde çevir. Kullanılmıyorsa golangci-lint `unused` advisory'de
  yakalar, o zaman kaldırırsın.

**Kabul kriteri:** Baseline'ı `[10,10,10,...,10, 80]` (tek kontamine bucket)
olan ve current=30 olan bir seride: eski mean/stdev z'si eşiği geçmez (maskeleme),
yeni MAD z'si geçer. Test bunu kanıtlar.

---

## FAZ 2 — Yönlü eşikler + göreli magnitude floor

**Dosya:** `internal/anomaly/anomaly.go`

**Sorun:** `math.Abs(z)` simetrik → 3σ p99 *düşüşü* (iyi haber) "critical anomaly"
açıyor. Ayrıca `minMagnitude=0.5` tek global sabit error_rate(%)/p99(ms)/rps için
ortak — anlamsız.

**Yap:**
- Metrik-bazlı bir policy tanımla, ör:
  ```go
  type metricPolicy struct {
      direction string  // "up" | "down" | "both"
      floorPct  float64 // relative change floor, e.g. 0.10 = 10%
  }
  var metricPolicies = map[string]metricPolicy{
      "error_rate":   {"up",   0.10},
      "p99_ms":       {"up",   0.10},
      "request_rate": {"both", 0.15}, // drop AND spike matter
  }
  ```
- `checkOne`: yön kontrolü ekle — `direction=="up"` ise yalnızca `z >= openZ`,
  `"down"` ise yalnızca `z <= -openZ`, `"both"` ise `|z| >= openZ`.
- `minMagnitude` mutlak eşiğini **göreli** floor ile değiştir:
  `|current - median| / max(|median|, ε) >= floorPct`. Böylece 0.5ms ve %0.5
  aynı muameleyi görmez.
- request_rate "both" için severity'yi yöne göre etiketle (drop genelde daha
  kritik); description'da yönü belirt ("dropped"/"spiked").

**Kabul kriteri:** p99 3σ düşüşü artık problem açmıyor; request_rate hem 3σ
düşüş hem 3σ artışta açıyor ama doğru severity ile. Testler iki yönü de kapsar.

---

## FAZ 3 — Dwell / M-of-N ile flapping önleme

**Dosya:** `internal/anomaly/anomaly.go`

**Sorun:** Eşikte histeresis var (open z>3 / resolve z<1.5) ama z=3 etrafında
salınan metrik tick başına aç/kapa yapabilir.

**Yap (stateless yaklaşım — leader handoff'ta state kaybı olmaz):**
- `fetchBuckets` zaten tüm seriyi döndürüyor. Son tek bucket yerine **son K
  bucket**'a bak (`const dwellBuckets = 2`).
- Açma koşulu: son K bucket'ın HEPSİ yön + eşik + floor koşulunu sağlamalı
  (M-of-N istersen `const dwellNeed = 2; dwellWindow = 3`).
- Kapatma koşulu: son K bucket'ın hepsi `resolveZ` içinde.
- Bu sayede in-memory sayaç tutmana gerek yok; koşul tamamen elindeki seriden
  türüyor ve replika değişiminde bozulmuyor.

**Kabul kriteri:** Tek bucket'lık geçici spike problem açmıyor; ardışık 2 bucket
spike açıyor. Test seri-bazlı senaryolarla kanıtlar.

---

## FAZ 4 — Time-of-day (haftagünü) sezonsal baseline

**Dosya:** `internal/anomaly/anomaly.go` (+ gerekiyorsa yeni bir fetch helper)

**Sorun:** Düz 24h baseline diurnal periyodisiteyi yok sayıyor → her sabah
request_rate rampası false-positive; trafik düşük saatte gerçek düşüş kaçıyor.
(`anomaly.go` üst yorumu bu açığı zaten kabul ediyor.)

**Yap:**
- Mevcut "son 24h ardışık bucket" baseline'ına **sezonsal mod** ekle (constant
  ile aç/kapa: `const seasonalBaseline = true`).
- Yeni okuma: current bucket'ın time-of-day slot'unu (ör. günün 5-dk indeksi) al,
  `service_summary_5m`'den **son N günün aynı slot'unu** çek
  (`const seasonalDays = 7`). Tercihen aynı haftagünü ayrımı (hafta içi/sonu)
  yap — trafik profili farklı.
- Bu ~7 örnek üzerinde Faz 1'in `medianMAD`'ini kullan → aynı robust mantık.
- Yeterli sezonsal örnek yoksa (yeni servis / az veri) otomatik olarak 24h
  ardışık baseline'a düş (graceful fallback).
- Sorgu yine MV'den, `max_execution_time` + LIMIT ile.

**Kabul kriteri:** Günlük tekrar eden ramp artık anomali açmıyor; aynı slot'un
geçmişine göre gerçek sapma açıyor. Test, slot-bazlı sentetik seri ile kanıtlar.

---

## FAZ 5 — Correlator: ağırlıklı + yönlü kenarlar

**Dosyalar:** `internal/correlator/correlator.go`,
`internal/chstore/` (adjacency okuması), gerekiyorsa `chmigrate`.

**Sorun:** `neighbors map[string]map[string]struct{}` ağırlıksız ve simetrik bir
set. 10K-rps, hata taşıyan bir kenarla 2-rps kenarı eşit. Binary `AreNeighbors`
false-positive gruplamaya açık (kod yorumu da "false-positives daha büyük risk"
diyor).

**Yap:**
- `GetServiceAdjacency` çıktısını **ağırlıkla** zenginleştir: kenar başına çağrı
  hacmi (`call_count`) ve hata propagasyon sayacı (aşağıda Faz 6). `topology_edges_5m`
  bu kolonları taşımıyorsa `chmigrate` ile ekle (geriye dönük: yokken 0 say).
- İç yapıyı yönlü + ağırlıklı yap:
  `edges map[string]map[string]edgeStat` (caller → callee → {weight,...}).
- `AreNeighbors` yerine/yanına **`Relation(a, b) (related bool, weight float64, dir string)`**
  ekle. `dir` ∈ {"a_calls_b","b_calls_a","sibling"}. İncident-attach çağrı yeri
  artık binary değil **ağırlık eşiği** kullanabilsin (`const minEdgeWeight`).
- `AreNeighbors`'ı koru (mevcut çağıranlar için), içini yeni `Relation`'a delege et.

**Kabul kriteri:** Zayıf kenar (düşük hacim) eşiğin altında "ilgili değil"
dönüyor; güçlü kenar yönüyle birlikte dönüyor. `Relation`'ın self-pair'i `true`.
Testler yön ve eşik davranışını kapsar.

---

## FAZ 6 — Correlator: hata-propagasyon koşullu olasılığı + decay'li 2-hop

**Dosyalar:** `internal/correlator/correlator.go`, `internal/chstore/`.

**Sorun:** Komşuluk yapısal; nedensel değil. "A, B'yi çağırıyor" ile "A'nın
hataları B'den kaynaklanıyor" çok farklı şeyler.

**Yap:**
- Aynı `spans`'tan **koşullu hata korelasyonu** hesapla:
  parent span'in errored olduğu trace'lerde child span'in de errored olma oranı
  ≈ `P(child error | parent error, same trace)`. Bunu kenar ağırlığının bir
  bileşeni yap (`edgeStat.errCorr`). Sorgu sample'lı + MV-dostu olsun; ham span
  full-scan'inden kaçın (örnek trace ID kümesi üzerinden ya da
  `topology_edges_5m`'e materialize edilmiş bir sayaçla).
- **Decay'li bounded-depth** komşuluk ekle (`const maxHops = 2`, `decay = 0.5`):
  2-hop bir yolun efektif ağırlığı `w1 * decay`. Güçlü 2-hop yüzeye çıkar,
  zayıf çıkmaz — filtreyi hop sayısı değil ağırlık eşiği yapar (1-hop reddinin
  asıl korkusu olan false-positive blowup'ı engeller).
- `Relation`'a `hops int` ve efektif ağırlık dahil et.

**Kabul kriteri:** Yüksek koşullu-hata kenarı root yönünü doğru işaret ediyor;
güçlü 2-hop yol eşik üstü, zayıf 2-hop eşik altı. Testler sentetik adjacency +
errCorr ile kanıtlar.

---

## FAZ 7 — Çapraz-sinyal doğrulama + zengin explainer context  · asıl "akıllı" hamle

**Dosyalar:** yeni `internal/anomaly/fusion.go` (veya `recorder.go`'ya entegre),
`internal/anomaly/problem_explainer.go`, okuma için `chstore` + `correlator`.

**Sorun:** Üç servis-metrik detektörü + `trace_ops` + `log_patterns` bağımsız
koşup bağımsız satır üretiyor. Aynı servis için aynı pencerede 4 sinyal
ateşlendiğinde bu 4 problem değil — **bir incident + birbirini doğrulayan kanıt**.
Ve `problem_explainer.explain()` şu an Copilot'a yalnızca problemin kendi
alanlarını gönderiyor (trace örneği, eş-ateşlenen log, komşu problem, deploy yok).

**Yap:**
1. **Evidence toplama:** Bir servis+pencere için açık olan tüm sinyalleri topla:
   - aynı servisin açık metrik-anomali `Problem`'leri (`FindOpenProblem` / `ListProblems`),
   - `DetectTraceOpAnomalies` sonuçları (özellikle `SampleTraceID`),
   - `DetectLogPatterns` sonuçları (pattern adı + sample + TopServices),
   - `correlator.Relation` ile açık problemi olan komşular (yön + ağırlık),
   - **deploy/değişiklik kaynağın varsa** (ör. `recent_changes` tablosu) onset
     penceresine düşen deploy işaretleri — varsa en yüksek-öncelikli kanıt.
2. **Confidence skoru:** Aynı servis+pencerede n>1 bağımsız sinyal → tek
   "incident candidate", n'e ve kanıt türüne göre artan güven. Bunu mevcut
   incident-attach mantığıyla birleştir (`AttachProblemToIncident`).
3. **Kanıtı kalıcılaştır:** `Problem`'e nullable `evidence_json` kolonu ekle
   (`chmigrate`), VEYA explain anında topla. Geriye dönük uyum: boşsa eski
   davranış.
4. **Explainer prompt'unu zenginleştir:** `explain()` içindeki `sb` string'ine
   toplanan kanıtı ekle (sample trace, co-firing log patterns, komşu problemler +
   yön, recent changes). Sistem prompt'u (`copilot.SystemPromptProblem()`)
   aynı kalsın; sadece user-context zenginleşsin. Token bütçesine dikkat
   (`max_tokens` mevcut çağrıdaki gibi) — kanıtı özetle, ham dump'lama.

**Kabul kriteri:** Aynı servis için error_rate anomali + OOMKilled log pattern +
checkout-op error spike aynı pencerede → tek yüksek-güvenli incident; explainer'a
giden context bu üç kanıtı + (varsa) deploy'u içeriyor. Test, fusion'ın sinyalleri
doğru gruplayıp evidence bundle'ı kurduğunu kanıtlar (Copilot çağrısını mock/stub
ile, mevcut explainer test desenine uygun).

---

## Sıralama ve teslim

Önerilen sıra leverage'a göre: **Faz 1 → 2 → 3 → 7 → 4 → 5 → 6**.
(Faz 7'nin tam gücü Faz 5/6'daki yönlü/ağırlıklı correlator'la artar, ama
correlator'ın mevcut `AreNeighbors`'ıyla da çalışır — yani 7'yi 5/6'dan önce
shipleyebilirsin, sonra correlator iyileştikçe evidence kalitesi yükselir.)

Her fazdan sonra: `go test ./internal/anomaly/... ./internal/correlator/...`,
concurrency'ye dokunulduysa `-race`, ve kısa bir commit mesajı +
`vX.Y.ZZZ` sürüm yorumu. Bir fazı bitirince dur, diff'i özetle, bir sonrakine
geçmeden onay iste.
