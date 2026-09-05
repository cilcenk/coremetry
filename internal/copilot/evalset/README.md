# CoSRE evalset — donmuş replay vakaları (v0.10.422, CoSRE denetimi E1/E7)

Altın testler (`prompt_golden_test.go`, `prompt_antifabrication_test.go`)
PROMPT METNİNİ pinler; modelin ÇIKTISINI hiçbir şey ölçmüyordu. Bu klasör
o boşluğu kapatır: her vaka donmuş bir kullanıcı promptu + beklenen
DAVRANIŞ (must-contain / must-not-contain / uydurma ad sayısı / niyet).

- Koşum **CI dışı**, yerel modele karşı, yapı etiketiyle:

  ```
  COREMETRY_EVAL_BASE_URL=http://localhost:11434/v1 \
  COREMETRY_EVAL_MODEL=qwen3:8b \
  go test -tags evalset ./internal/api/ -run TestEvalsetReplay -v
  ```

  `COREMETRY_EVAL_PROVIDER` (varsayılan `openai` — Ollama/vLLM/LM Studio
  uyumlu) ve `COREMETRY_EVAL_API_KEY` (isteğe bağlı). Üretim
  `COREMETRY_AI_*` değişkenleri BİLİNÇLİ okunmaz: evalset bir geliştiricinin
  gerçek anahtarına asla ateşlenmez. Kayıt yazılmaz (recorder bellek içi;
  `ai_calls` satırı yok).

- Fikstür doğrulaması etiketsiz koşar (`TestEvalsetFixturesValid`):
  şema `coremetry.evalset/1`, benzersiz id, çözülebilir surface, `why`
  zorunlu, en az bir beklenti. Yazım hatası kırmızı test, sessiz atlama değil.

- Skor satırı: `id surface ok latency_ms unknown_entities fails`; altbilgi
  `prompt_version=<FNV> model=<ad> n= pass= fail=`. `prompt_version`
  değişince eski skor kıyaslanamaz — altbilgi olmadan yeşil koşum hiçbir
  şey söylemez.

## Vaka şeması

```json
{
  "schema": "coremetry.evalset/1",
  "cases": [{
    "id": "problem-evidence-boundary",
    "surface": "Problem",              // copilot.SystemPromptX adı
    "why": "kırmızıya dönünce okunacak cümle",
    "user": "…donmuş kullanıcı promptu…",
    "expect": {
      "mustContain": ["2.14.0"],       // büyük/küçük harf duyarsız
      "mustNotContain": ["kubectl"],
      "knownEntities": ["checkout"],   // rca.CountUnknownEntities bilinen kümesi
      "maxUnknownEntities": 0,         // cevapta gösterilmemiş servis-biçimli ad tavanı
      "intent": "service_health",      // yalnız IntentClassify
      "intentService": "checkout",
      "maxLatencyMs": 30000            // METRİK: aşım uyarı, kırmızı değil (soğuk model)
    }
  }]
}
```

RCA hakemi vakaları (v0.10.424): `surface: "RCAVerdict"` + `user` yerine
`hypothesis` (chstore.RootCauseHypothesis JSON); prompt, katalog, rakipler ve
şema canlı yolla aynı kodla üretilir. Beklenti: `verdicts` (kabul kümesi),
`minEvidenceCitationRate` (K2'den geçen atıf / toplam atıf),
`maxUnknownEntities` (K3). Kalkan raporu (rcaShieldReport) skor satırıdır.

Vaka kaynağı: elle yazılmış sentetik (demo sözlüğü: checkout, payments,
inventory). Prod 👎 satırları müşteri adı taşıyabilir ve repoya GİRMEZ;
`GET /api/ai/evalset/export` (E5) indirilen dosya repoya elle ve
temizlenerek alınır.
