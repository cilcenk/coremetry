# CoSRE değerlendirme programı — `/llm-evaluation` + `/agentic-eval` (2026-09-10, v0.10.646)

İki skill'in çerçevesi Coremetry'nin gömülü asistanına (CoSRE: guided router +
serbest tool döngüsü + 20+ Explain yüzeyi; yerel gemma4, air-gapped, Türkçe)
uygulanmıştır. Önceki AI denetiminin E1–E8 kalemleri (v0.10.400–423) gemide; bu
doküman onların ÜZERİNE ne eksik ve hangi sırayla, onu söyler. Kod değişikliği yok.

## 1. Bugün elde olan (koddan doğrulandı)

| Varlık | Yer | Ne veriyor |
|---|---|---|
| Donmuş vaka kümesi + replay | `internal/copilot/evalset/*.json` (intent, problem, exception_chat, incident_anomaly, runbook_health); `internal/api/evalset_test.go` (`-tags evalset`, ≥20 vaka zorunlu) | Yerel modele karşı yeniden oynatma; davranış kapıları (kalkan/dil/anti-uydurma); gecikme metrik, kırmızı değil; CI DIŞI (bilinçli) |
| Prompt sürümü | `copilot.PromptVersion()` + `promptVersionRegistry` (her prompt kayıtlı, test pinli) | Skorun hangi prompt'a ait olduğu; sürüm değişince kıyas kırılır (dürüst) |
| Çağrı telemetrisi | `ai_calls` (surface, exchange_id, provider, model, süre, token, status, prompt/response örneği) → `/ai` | Model başına gecikme/hata/👍 oranı (E3), bütçe eşiği (E8) |
| İnsan geri bildirimi | `ai_feedback` (RMT), `ListNegativeFeedbackCalls`, "vakaya çevir" → evalset export (E5) | 👎 → regresyon vakası kapalı döngü |
| Uydurma sayacı | `shieldNarrative` `unknown[]` → `ai_calls` (E6) | Anlatıda kanıtsız ad/sayı oranı |
| Deterministik CI kapıları | `copilot_intent_test.go` (25 vaka), `guided_*_test.go`, `prompt_*_test.go` | Router/prefetch/prompt metni regresyonu — LLM'siz, her PR'da |
| Güven kalibrasyonu | 3 güven kovası × 👍/👎 (E4) | RCA hipotez güveni gerçekçi mi |

## 2. Boşluklar (skill çerçevesine göre)

| # | Boşluk | Neden önemli |
|---|---|---|
| G1 | **Skor geçmişi yok** — replay sonucu bellekte, koşumlar kıyaslanamıyor | "Prompt v42 v41'den iyi mi" cevaplanamaz; trend yok |
| G2 | **Rubrik yok** — kapılar ikili (kalkan geçti/geçmedi); doğruluk/ilgililik/kısalık/dil boyutu skorlanmıyor | 👍 oranı tek kalite sinyali; seyrek ve gürültülü |
| G3 | **LLM-as-judge yok** — referanssız kalite ölçümü yalnız insanla | 20+ yüzeyde insan değerlendirmesi ölçeklenmez |
| G4 | **Prompt A/B protokolü yok** — çok-model seçici var (v0.10.175–183), aynı model + iki prompt kıyası yok | Prompt değişikliği "hissiyatla" gemiye çıkıyor |
| G5 | **Vaka kümesi dar** — intent 7 → ~40 planı (D7) açık; Explain yüzeylerinin çoğunun vakası yok | Replay yalnız 5 yüzeyi görüyor |
| G6 | **Yansıma (reflection) döngüsü yok** — üretim tek atış; kanıta karşı öz-eleştiri adımı yok | Uydurma kalkanı SİLER ama düzeltmez |
| G7 | **RAG geri getirme metrikleri yok** (MRR/Recall@K) | RAG bge-m3'e bağlı askıda (operatör); ölçüm altyapısı hazır olmalı |

## 3. Program — dört faz, her biri kendi sürümü

### Faz A — skor geçmişi + rubrik (G1, G2) · **GEMİDE v0.10.666** · **derived**

> Uygulama notu (v0.10.666): skor geçmişi `ai_calls`'a DEĞİL, repo dışı JSON
> artefaktlara yazılır (`COREMETRY_EVAL_OUT`, varsayılan `evalset-runs/`,
> gitignore'lu). Gerekçe: replay CH'ye bağımlı olmamalı — "evalset bir
> geliştiricinin gerçek anahtarına asla ateşlenmez, kayıt bellek içi"
> sözleşmesi (evalset_test.go). Rubrik `internal/ai/evalrubric` (saf, CI'da
> testli); diff `go run ./cmd/evalsetdiff a.json b.json` ya da `make evalset-diff`.
> Doğrulama (aynı prompt + model iki koşum → Δ ≤ 0,05) yerel model ister —
> bu makinede model sunucusu yoktu, operatörde.

- Replay sonucu koşum artefaktına yazılır (`evalrubric.Run` JSON: promptVersion, model,
  vaka başına ikili kapı + rubrik + gecikme, özet). `ai_calls` yolu ertelendi
  (uygulama notu yukarıda); `/ai` sayfasında "son koşum" kartı Faz D'ye.
- Rubrik (her vaka için JSON, skill'in yapısal çıktı ilkesi): `grounded` (0/1, kalkan
  `unknown[]` boş), `answers_question` (0–2), `language_tr` (0/1), `length_ok` (0/1,
  yüzey tavanı), `tool_calls_valid` (0/1, serbest döngü). Ağırlıklı toplam 0–1; eşik 0,8
  (evaluator-optimizer deseni). Rubrik değerlendiricisi ÖNCE deterministik
  (regex/kalkan/tool şeması) — LLM yargıcı Faz B'de eklenir.
- Koşum altbilgisi: prompt_version + model + rubrik ortalaması + eşik altı sayısı;
  iki koşum diff'i `cmd/evalsetdiff` (gerileyen/iyileşen vaka, yeni FAIL/ok, model
  farkı → "kıyaslanamaz", aynı prompt → gürültü notu).

**Doğrulama:** aynı prompt + aynı model iki koşum → rubrik farkı ≤ 0,05 (gürültü tabanı
ölçülür; yerel model sıcaklığı 0 değilse önce sabitle).

### Faz B — LLM-as-judge (G3) + prompt A/B (G4) · ~1,5 gün · **field**

- Yargıç = yerel model (air-gapped; dış yargıç yok). Bilinen yanlılık: aynı aile
  kendi çıktısını kayırır → **çiftli (pairwise) + konum takası** (A/B ve B/A, iki
  yargı uyuşmazsa "berabere"), referanslı vakalarda `expected` ile kıyas, referanssız
  yüzeylerde yalnız `grounded` ve `answers_question` boyutları.
- A/B: aynı model, iki prompt sürümü (`promptVersionRegistry` iki girdi), aynı 40
  vaka; kazanma oranı + rubrik farkı; ≥ %60 kazanma + rubrik ≥ +0,05 → gemiye.
- İnsan kalibrasyonu: koşum başına 10 vaka operatör puanlar (👍/👎 + 1 cümle);
  yargıç–insan uyuşması < %70 ise yargıç boyutu "danışma" statüsüne düşer.

**Uyarı (sezgisel):** küçük yerel modelin yargıç olarak güvenilirliği ölçülmeden
bilinmiyor; Faz B'nin ilk çıktısı yargıcın kendisinin skorudur, ürün kararı değil.

### Faz C — yansıma döngüsü, çevrimdışı (G6) · ~1 gün · **derived**

- Desen: Generate → Evaluate (Faz A rubriği) → Critique (yerel model: "kanıtta olmayan
  hangi ad/sayı var?") → Refine (bir kez) → Output. **İstek yolunda DEĞİL**: gemma4
  gecikmesi (soğuk 60 s+) ikinci turu operatöre yansıtır; döngü 👎 vakalarının replay'inde
  ve prompt optimizasyonunda koşar (max 2 yineleme, yakınsama: rubrik artışı < 0,02 →
  dur; her yineleme `ai_calls`'a `exchange_id` ile bağlanır — iz kaydı).
- Çıktı: "refine'ın düzelttiği hata sınıfları" listesi → prompt'a KURAL olarak
  taşınır (E7 davranış vakası olur). Yani döngü modeli değil prompt'u iyileştirir.

### Faz D — kapsam + CI (G5, G7) · sürekli · **official (test disiplini)**

- Vaka kümesi: intent 40 (D7), her Explain yüzeyi için ≥ 3 vaka (20 yüzey → 60);
  👎 → vaka dönüşümü zaten var (E5), aylık kota: 10 yeni vaka.
- CI'da LLM'siz deterministik alt küme kalır (router, prefetch, kalkan, tool şeması);
  replay CI dışı ama **haftalık zamanlanmış koşum** (operatör makinesi, `make evalset`)
  ve sonucu `/ai` sayfasında "son koşum" kartı.
- RAG hazır olduğunda: `docs/` parçaları için 30 soru–parça çifti, Recall@5 ve MRR;
  bge-m3 gelmeden altyapı (fixture + metrik fonksiyonu) yazılabilir.

## 4. Sıra ve karar noktaları

1. **Faz A** — sorusuz; şema yok, replay'e iki alan + rubrik.
2. **Faz D'nin vaka genişletmesi** — Faz A ile birlikte başlar (vaka yazımı operatör
   katkısı ister: gerçek soru örnekleri).
3. **Faz B** — operatör kararı: yerel modeli yargıç yapmak kabul mü, yoksa yargıç
   adımı yalnız insan mı? (Air-gapped kısıt dış yargıcı zaten dışlıyor.)
4. **Faz C** — Faz A sonuçlarında uydurma sayacı (E6) > %5 ise; değilse ertelenir.

Bu programın ölçüsü tek cümle: *"prompt'u değiştirdim, iyi mi kötü mü"* sorusuna
sayıyla ve tekrarlanabilir biçimde cevap verebilmek.
