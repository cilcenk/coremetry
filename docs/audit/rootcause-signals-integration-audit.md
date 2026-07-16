# Audit — Root-cause sentezi Signals kanıtına kör (EvidenceBundle → Synthesize kopukluğu)

2026-07-16 · HEAD `14cdc533` (v0.8.566) · Salt-okunur inceleme, hiçbir dosya
değiştirilmedi. İncelenen dosyalar: `internal/anomaly/fusion.go`,
`internal/anomaly/rootcause_worker.go`, `internal/anomaly/recorder.go`,
`internal/anomaly/log_patterns.go`, `internal/anomaly/trace_ops.go`,
`internal/correlator/hypothesis.go`, `internal/correlator/hypothesis_test.go`,
`internal/chstore/anomaly_event.go`, `internal/anomaly/rootcause_prompt.go`,
`internal/copilot/copilot.go`.

## Kısa hüküm

1. **Bulgu DOĞRULANDI.** `EvidenceBundle.Signals` (`[]chstore.AnomalyEvent`,
   fusion.go:43) canlı bir toplayıcıyla dolduruluyor ama
   `synthInputForProblem` (rootcause_worker.go:166) onu `SynthesisInput`'a
   taşımıyor — struct'ta karşılık gelen alan bile yok (hypothesis.go:82-99).
   Anomali yolunda da aynı: `synthInputForAnomaly` (worker:188) `in.events`'i
   hiç okumuyor. `Synthesize` log_pattern/trace_op kanıtını hiç görmüyor;
   LLM'e "BİRİNCİL kanıt" diye giden hipotez (copilot.go:927-931, 956) bu
   kanıta kör.
2. **Ölü toplayıcı DEĞİL** — Recorder canlı (main.go:813-820, default açık),
   `buildEvidenceBundle` filtreliyor (fusion.go:167-171). Ama sentez hattında
   ölü yük: `b.Signals`'ın tek canlı tüketicisi explainer'ın
   `renderEvidence`'ı (problem_explainer.go:219 → fusion.go:228-243).
3. **Yerleşim önerisi: propagation'la ÖRTÜŞEN bant [0.30, 0.60]** — katı
   "propagation ile co-firing arasına" zaten matematiksel olarak imkânsız
   (propagation tabanı 0'a iner) ve mevcut katman sistemi alt uçta zaten
   yumuşak (ham prop < 0.286 → 0.70·s < 0.20 co-firing'in altına düşer;
   hypothesis.go:32-34 yorumu bunun aksini iddia ediyor ama kod aksini
   yapıyor). Detay §3.
4. `maxEvidenceTypes` 3→4: `distinctTypes` semantiği (katman VARLIĞI sayımı,
   eleman sayısı değil) korunur; kırılan tek test
   `TestSynthesizeDeployDominates` (breadth'i 1.0 hardcode eder,
   hypothesis_test.go:57-59). Detay §4.
5. İki açık soru (katı tier sıralaması + confidence modeli) §5-§6'da
   belgelendi, implementasyon önerilmedi.
6. **Sessiz boşluk:** `rootcause_worker.go`'nun HİÇ testi yok —
   `SynthesisInput`'a alan eklenip worker doldurmayı unutsa hiçbir test
   kırılmaz. Test planı bunu da kapatıyor (§7).

---

## 1. Bulgu doğrulaması — zincirin koptuğu yer

**Toplama (canlı):**
- Yazan: `anomaly.Recorder` — log_pattern spike (recorder.go:113), yeni log
  template (recorder.go:144, `CurrentRatio: 0`), trace_op (recorder.go:166)
  → `UpsertAnomalyEvent`. Wiring `main.go:813-820`, gate
  `COREMETRY_LOG_ANOMALY_ENABLED` (default açık, yalnız worker modu).
- Okuyan: `gatherEvidenceInputs` (fusion.go:74-95) tick başına bir kez
  `ListAnomalyEvents(60m, 500)`; `Status` sorguda türetilir
  (`last_seen >= now − 10m → "active"`, anomaly_event.go:186-199).
- Bundle'a filtre (fusion.go:167-171): aynı servis + `Status == "active"`.

**Kopukluk (sentez):**
- `synthInputForProblem` (worker:166-182) bundle'dan yalnız
  `Deploy`/`Neighbors`/`CoFiring` taşır; `b.Signals` ve `b.Confidence`
  taşınmaz. Grep kanıtı: `.Signals` repo genelinde (test hariç) yalnız
  fusion.go:169/192/228/230 — worker ve correlator'da SIFIR geçiş.
- `synthInputForAnomaly` (worker:188-227) `in.events`'i hiç okumaz.
- Sonuç: log/trace anomali kanıtı ne aday listesine, ne skora, ne breadth'e
  (`maxEvidenceTypes = 3`, hypothesis.go:76) girer. Persist edilen hipotez
  (`root_cause_hypotheses`) ve ondan beslenen HER yüzey — /problems ribbon,
  Copilot promptu (`HypothesisPromptBlockTR`), MCP — bu kanıttan yoksun.

**Boş kalma koşulları** (bulgu değil, bağlam): (a) recorder kapalıysa,
(b) event 10dk'dan bayatsa (`cleared` filtreye takılır), (c)
`ListAnomalyEvents` hatası (best-effort, fusion.go:79).

## 2. Mevcut katman mimarisi — GERÇEK aralıklar

`Synthesize` (hypothesis.go:112-221), sabitler hypothesis.go:45-60:

| Katman | Formül | Gerçek aralık |
|---|---|---|
| deploy | `0.80 + 0.15·freshnessFrac` (:138) | **[0.80, 0.95]** |
| propagation | `0.70 · propScore` (:160), propScore ∈ (0,1] | **(0, 0.70]** |
| co-firing | düz `0.20` (:187) | **0.20** |

Determinizm: tier-sıralı insert + `sort.SliceStable` Score desc → Service
asc (:198-203); co-firing ayrıca `sort.Strings` (:183).

**Önemli düzeltme — "katmanlar ayrık" iddiası kısmen yanlış:**
hypothesis.go:32-34 yorumu "propagation, ham büyüklükten bağımsız her zaman
co-firing'i geçer" der; oysa ham prop skoru < 0.286 için `0.70·s < 0.20` —
zayıf bir 3-hop komşu bugün co-firing'in ALTINA düşer. Ayrık olan tek sınır
deploy tabanı (0.80 > 0.70 tavan). Yani "mevcut katman mantığı korunacak"
şartı fiilen şu demek: **deploy tavanı dokunulmaz, alt bölge zaten
sürekli/örtüşük bir skala.** Signals yerleşimi bu gerçeğe göre seçilmeli.

## 3. Öneri — Signals dördüncü kanıt katmanı

### 3.1 Yerleşim: [0.30, 0.60] örtüşen bant (önerilen)

Operatör sezgisi: co-firing'den güçlü (spesifik hata imzası), deploy'dan
zayıf (DEĞİŞİKLİK değil SEMPTOM). İki seçenek:

- **Seçenek A (önerilen): [0.30, 0.60], propagation'la örtüşen.**
  Gerekçe: aynı-servis spesifik imza (log pattern örneği + ratio), %12
  hata paylı 3-hop bir komşu tahmininden (0.70·0.5²·0.5 ≈ 0.09) daha güçlü
  kanıttır; ama %86+ paylı 1-hop bir downstream'i (≥0.60 tier skoru) asla
  geçmemeli — o yapısal "kaynak nerede" kanıtı, signal ise anchor'ın kendi
  üstündeki semptomun imzası. Tavan 0.60 < 0.70 (prop tavanı) < 0.80
  (deploy tabanı): deploy dokunulmaz, güçlü propagation dokunulmaz,
  co-firing (0.20) her zaman altta.
- **Seçenek B: dar düz bant (0.20, 0.30].** "Her propagation adayı her
  signal'i geçsin" ancak ham prop ≥ 0.43 için sağlanır (0.70·0.43 = 0.30);
  tam ayrıklık yine imkânsız. Daha muhafazakâr ama operatör sezgisindeki
  "spesifik imza güçlüdür" kısmını boşa düşürür. Önerilmiyor.

### 3.2 Skor formülü + iç sıralama (kind ve ratio)

`AnomalyEvent`'te `score` alanı yok; güç sinyali `CurrentRatio`/`PeakRatio`
(= current / max(baseline,1); anomaly_event.go:16-52). Dedektör tetik tabanı
2.0 (log_patterns.go:199, trace_ops.go:64); "new" pattern'de ratio = ham
sayı, sınırsız (log_patterns.go:197); recorder'ın yeni-template olayı
`CurrentRatio: 0` yazar (recorder.go:144) — formül 0'ı taban'a clamp etmeli.

```
r         = max(CurrentRatio, PeakRatio)          // yeni-template 0 → ratioNorm 0
ratioNorm = clamp((r − 2) / (10 − 2), 0, 1)       // 2 = tetik tabanı, 10 = doygunluk
score     = signalTierBase + kindBonus + signalRatioSpan · ratioNorm
            signalTierBase = 0.30, signalRatioSpan = 0.25
            kindBonus: log_pattern +0.05, trace_op +0
```

→ log_pattern ∈ [0.35, 0.60], trace_op ∈ [0.30, 0.55].

**log_pattern > trace_op gerekçesi:** log pattern hata METNİNİ taşır
(`Sample`, prompt'a girer) — LLM ve operatör için en teşhis-edici parça;
trace_op "şu operasyon anormal" der ki anchor problem çoğu zaman zaten
operasyon-hata-oranı problemi — marjinal bilgisi düşük. Eşit ratio'da
imza kazanır; yüksek ratio'lu trace_op yine düşük ratio'lu log_pattern'i
geçebilir (bant örtüşük — istenen davranış).

**Determinizm:** signal adaylarının hepsi `Service = anchor` taşıyacağından
global tie-break (Score desc → Service asc) eşit-skorlu signal'ler arasında
AYIRT EDEMEZ; `sort.SliceStable` insert sırasını korur → Synthesize içinde
emit öncesi `(score desc, Kind asc, Pattern asc)` ön-sıralaması şart
(co-firing'deki `sort.Strings(coServices)` deseninin eşleniği). Not:
alfabetik olarak `"log_pattern" < "trace_op"` — Kind asc doğal olarak
imzayı öne alır.

### 3.3 Dokunulacak yerler

1. **`correlator.SynthesisInput`** — yeni alan (import-cycle-free, chstore
   zaten import ediliyor):
   ```go
   Signals []SignalEvidence // {Kind, Pattern string; Ratio float64}
   ```
   Ham `chstore.AnomalyEvent` yerine minimal struct: saf fuser yalnız
   skorlamada kullandığını alsın (mevcut `ScoredCause` yaklaşımıyla tutarlı).
2. **`Synthesize`** — co-firing bloğundan ÖNCE (skor sırasına göre) yeni
   tier: `len(in.Signals) > 0 → distinctTypes++`; aday başına
   `Service: service, Hops: 0, Path: []string{service}`, Reason örn.
   `"anomalous log pattern '%s' on the service — %.1fx over baseline"`.
   Aday sayısı cap'lenebilir (örn. en güçlü 3; prompt zaten
   `maxHypothesisCandidates = 3` ile kesiyor, rootcause_prompt.go:27).
3. **`synthInputForProblem`** — `b.Signals` → `in.Signals` eşlemesi
   (Kind/Pattern/max(CurrentRatio,PeakRatio)).
4. **`synthInputForAnomaly`** — `in.events`'ten aynı-servis + active +
   **`ID != ev.ID`** (kendini dışla — problem yolundaki `op.ID == p.ID`
   dışlamasının eşleniği, fusion.go:128; anomali yolundaki mevcut co-firing
   asimetrisini tekrarlamayalım).
5. **Prompt/UI etkisi bedava:** signal adayları `Candidates` JSON'una Reason
   ile girer → `HypothesisPromptBlockTR`'nin "Diğer adaylar" bölümü ve iki
   explain yolu (problem_explainer.go:215 + api.go:7734) otomatik kapsar.
   `systemProblem` few-shot'una (copilot.go:949-974) örnek satır eklemek
   ayrı, küçük bir dokunuş (2B model şekli görmeli, copilot.go:908-910);
   narration yolu (`buildRootCausePrompt`, api/rootcause.go:329) ayrıca
   güncellenmeli. Şema değişikliği YOK — `root_cause_hypotheses`'e kolon
   gerekmez.

## 4. `maxEvidenceTypes` 3→4

- Sabit hypothesis.go:76 → 4; yorum ("deploy, propagation, co-firing")
  güncellenir. `distinctTypes` sayımının anlamı DEĞİŞMEZ: eleman değil
  katman varlığı sayılır (:126, :130, :153, :173 + yeni satır).
- **Kırılan test — bilinçli:** `TestSynthesizeDeployDominates`
  (hypothesis_test.go:59) breadth'i `1.0` hardcode eder ("All three
  evidence types present"); 3/4 = 0.75 üretilir → fail. Doğru düzeltme:
  vakaya bir signal EKLEyip "tüm tipler mevcut → breadth 1.0" semantiğini
  korumak (ve deploy'un signal'i de geçtiğini aynı vakada pin'lemek).
- **Kırılmayan ama sessizce kayan:** `TestSynthesizePropagationOnly` /
  `TestSynthesizeCoFiringOnly` `1.0/float64(maxEvidenceTypes)` yazdığından
  1/4'e otomatik uyar (:89, :122) — mevcut hipotezlerin confidence'ı bir
  sonraki tick'te düşer (deploy-only: 0.5·⅓→0.5·¼, ≈ −0.04). Ribbon eşiği
  `conf > 0.05` (RootCauseRibbon.tsx:57-58) — gerçekçi hiçbir kombinasyon
  eşiğin altına inmez; kabul edilebilir rekalibrasyon, ama bilerek
  yapıldığı commit gövdesine yazılmalı.

## 5. Açık soru 1 — katı katman sıralaması (BELGELENDİ, impl yok)

Somut çarpıklık: deploy lookback 30dk (`GetRecentDeploys(30m)`,
fusion.go:74-95). 29dk önce inen ALAKASIZ bir deploy → frac ≈ 1−29/30 =
0.033 → skor 0.805; %100 hata paylı 1-hop downstream tavanı 0.70. Deploy
HER ZAMAN kazanır — "what changed" önceliği tasarım gereği ama 29dk'lık
bayat deploy'un taze yapısal kanıta mutlak üstünlüğü şüpheli.

Tartışılacak öneri: deploy tabanını freshness'e devretmek —
`score = 0.60 + 0.35·frac` (aralık [0.60, 0.95]). frac < 0.286, yani deploy
onset'ten **~21dk+** önceyse skor 0.70'in altına iner → güçlü 1-hop
propagation geçebilir; taze deploy (ilk ~20dk) üstünlüğünü korur.
Bedeli: hypothesis.go:32-38'deki belgeli invariant ("deploy her zaman
propagation'ı geçer") bilinçli kırılır; `TestSynthesizeDeployDominates`
sıralama pin'i yeniden tasarlanır. Karar operatörün — bu audit kapsamında
implementasyon önerilmiyor.

## 6. Açık soru 2 — confidence modeli (BELGELENDİ, impl yok)

Mevcut: `conf = 0.5·(distinctTypes/max) + 0.5·topScore` (hypothesis.go:216).
Breadth terimi katman VARLIĞINA prim verir, gücüne değil. 4 kanalda şişme
örneği: zayıf signal (0.35) + zayıf prop (0.10) + co-firing (0.20), deploy
yok → breadth ¾ → conf = 0.375 + 0.175 = **0.55** — hiçbir güçlü kanıt
yokken "orta-yüksek" güven. Alternatif: kanıt GÜÇLERİNİN ortalaması/toplamı,
örn. `conf = 0.5·(Σ kanalTepeSkoru / maxEvidenceTypes) + 0.5·topScore` —
aynı örnekte Σ=0.65/4=0.16 → conf ≈ 0.26 (dürüst). Bedeli: "çok kanal
teyit etti" sezgisi zayıflar (fusion.go confidence modeliyle paralellik
bozulur); deploy-only vakada davranış benzer kalır (0.80 tek kanal baskın).
Signals katmanı eklenirken DEĞİL, ayrı bir kararla ele alınmalı — iki
değişiklik aynı anda yapılırsa confidence kayması ayrıştırılamaz.

## 7. Test planı

Mevcut yapı: 5 ayrı test fonksiyonu (hypothesis_test.go, v0.8.168), skorlar
sabitlere SEMBOLİK bağlı (literal pin yok), determinizm
`reflect.DeepEqual` permütasyon testiyle pin'li (:156-173).

**Genişletme — determinizm garantisi (Score desc → Service asc,
SliceStable) bozulmadan:**

1. `TestSynthesizeDeployDominates` — vakaya signal eklenir; beklenen sıra
   deploy > prop > signal > co-firing; breadth 4/4 = 1.0 korunur.
2. **Boş Signals** — `TestSynthesizeEmptyEvidence` aynen geçmeli (yeni alan
   zero-value'da 0 aday + 0 breadth katkısı); ayrıca "diğer katmanlar dolu,
   Signals boş → breadth 3/4" vakası.
3. **Tek log_pattern** — aday Service = anchor, skor
   `signalTierBase + 0.05 + span·ratioNorm` sembolik assert, breadth 1/4.
4. **trace_op + log_pattern karışık** — eşit ratio'da log_pattern üstte;
   yüksek-ratio trace_op'un düşük-ratio log_pattern'i geçtiği vaka.
5. **Sınır skorlar** — ratio = 2.0 (tetik tabanı → ratioNorm 0), ratio ≥ 10
   (doygunluk → tavan 0.60/0.55), ratio = 0 (yeni-template → taban),
   tavanın prop-tavanı 0.70 ve deploy-tabanı 0.80'in altında kaldığı
   assert.
6. **Deploy'la birlikte** — deploy + max signal: deploy #1 kalır
   (0.80 > 0.60 pin'i, mevcut DeployDominates deseninin eşleniği).
7. **Determinizm** — eşit skorlu iki signal permütasyonu
   `reflect.DeepEqual` ile bayt-özdeş (`(Kind, Pattern)` ön-sıralaması);
   `TestSynthesizeTieBreakDeterministic` deseni.
8. **Worker eşlemesi (YENİ dosya, sessiz boşluğu kapatır)** —
   `rootcause_worker.go`'nun hiç testi yok; table-driven
   `synthInputForProblem`/`synthInputForAnomaly` testi: Signals'lı bundle →
   `SynthesisInput.Signals` dolu; anomali yolunda kendini-dışlama
   (`ID != ev.ID`); cleared/başka-servis event'lerin düşmesi zaten
   fusion'da ama max(CurrentRatio, PeakRatio) eşlemesi burada pin'lenir.
9. `rootcause_prompt_test.go` — signal Reason'lı aday "Diğer adaylar"
   bölümünde göründüğü bir vaka (cap 3 etkileşimi).

Her test sabit-sembolik yazılır (retune'a dayanıklı); sıralama invariantları
(deploy > signal-tavanı, signal > co-firing) yalnız ordering assert'leriyle
pin'lenir — mevcut evin deseni.

## Öneri

1. **Yap:** Signals'ı §3 tasarımıyla dördüncü katman olarak Synthesize'a
   bağla — [0.30, 0.60] örtüşen bant, kind bonusu + ratio normalizasyonu,
   `(Kind, Pattern)` deterministik iç sıralama; `synthInputForProblem` +
   `synthInputForAnomaly` (kendini-dışlamayla) eşlemesi;
   `maxEvidenceTypes = 4`; §7 test paketi. Şema değişikliği yok, tek
   release'lik iş.
2. **Ayrı karar iste:** §5 (deploy freshness decay — belgeli invariantı
   kırar) ve §6 (confidence modeli) — ikisi de bu değişiklikle AYNI
   release'te yapılmamalı.
3. Few-shot (copilot.go) + narration promptu (api/rootcause.go:329)
   güncellemesi küçük takip işi olarak aynı release'e alınabilir.

**Onay bekliyor — implementasyona geçilmedi.**
