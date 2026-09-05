//go:build evalset

// evalset_test.go — v0.10.422 (CoSRE denetimi E1/E7): donmuş vakaları
// YEREL modele karşı yeniden oynatır ve davranışı skorlar. CI DIŞI —
// bilinçli (denetim: "yerel model yanı başında"). Koşum:
//
//	COREMETRY_EVAL_BASE_URL=http://localhost:11434/v1 \
//	COREMETRY_EVAL_MODEL=qwen3:8b \
//	go test -tags evalset ./internal/api/ -run TestEvalsetReplay -v
//
// Üretim COREMETRY_AI_* değişkenleri OKUNMAZ: evalset bir geliştiricinin
// gerçek anahtarına asla ateşlenmez. Kayıt bellek içi (ai_calls satırı
// yok). Gecikme METRİK (soğuk model 60 sn+ olabilir): aşım uyarı, kırmızı
// değil. Altbilgi prompt_version + model taşır; onsuz yeşil koşum hiçbir
// şey söylemez (prompt değişince eski skor kıyaslanamaz).

package api

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

type evalRecorder struct {
	mu   sync.Mutex
	recs []copilot.CallRecord
}

func (r *evalRecorder) RecordCall(_ context.Context, c copilot.CallRecord) {
	r.mu.Lock()
	r.recs = append(r.recs, c)
	r.mu.Unlock()
}

func TestEvalsetReplay(t *testing.T) {
	base := os.Getenv("COREMETRY_EVAL_BASE_URL")
	model := os.Getenv("COREMETRY_EVAL_MODEL")
	if base == "" || model == "" {
		t.Skip("COREMETRY_EVAL_BASE_URL / COREMETRY_EVAL_MODEL yok — evalset atlandı")
	}
	provider := os.Getenv("COREMETRY_EVAL_PROVIDER")
	if provider == "" {
		provider = copilot.ProviderOpenAI
	}
	key := os.Getenv("COREMETRY_EVAL_API_KEY")

	svc := copilot.New(provider, key, model)
	svc.Configure(provider, key, model, base, false, true)
	temp := 0.0
	svc.ConfigureTuning(0, &temp, 120) // sıfır sıcaklık: yeniden oynatım kararlılığı
	rec := &evalRecorder{}
	svc.SetRecorder(rec)

	cases := loadEvalset(t)
	pass, fail := 0, 0
	t.Logf("id\tsurface\tok\tlatency_ms\tunknown_entities\tfails")
	for _, c := range cases {
		system, ok := evalSystemPrompt(c.Surface)
		if !ok {
			t.Fatalf("%s: surface %q çözülemiyor", c.ID, c.Surface)
		}
		ctx := copilot.WithMeta(context.Background(), copilot.CallMeta{Surface: "evalset-" + c.Surface, UserID: "evalset", Shield: aiShield})
		if evalJSONSurface(c.Surface) {
			ctx = copilot.WithJSONMode(ctx)
			if c.Surface == "IntentClassify" {
				ctx = copilot.WithJSONSchema(ctx, "chat-intent", intentClassifySchema())
			}
		}
		sys, user := evalCaseInput(c, system) // v0.10.423 — export vakaları ham prompt taşır
		// v0.10.424 — RCA hakemi: hipotezden canlı yolla aynı prompt/şema.
		var rcaH *chstore.RootCauseHypothesis
		var rcaCat rcaEvidenceCatalog
		if len(c.Hypothesis) > 0 {
			rcaH = &chstore.RootCauseHypothesis{}
			if err := json.Unmarshal(c.Hypothesis, rcaH); err != nil {
				t.Fatalf("%s: hypothesis: %v", c.ID, err)
			}
			var rivals, entities []string
			rcaCat, rivals, entities, user = evalRCAInputs(rcaH, time.Now())
			ctx = copilot.WithJSONSchema(ctx, "rootcause-verdict", rcaVerdictSchema(entities, rivals))
		}
		t0 := time.Now()
		answer, err := svc.Explain(ctx, sys, user)
		lat := time.Since(t0).Milliseconds()
		var fails []string
		var unknown int
		if rcaH != nil && err == nil {
			fails, unknown = scoreRCACase(c, rcaH, rcaCat, answer)
		} else {
			fails, unknown = scoreEvalCase(c, system, answer, err)
		}
		okS := "ok"
		if len(fails) > 0 {
			okS = "FAIL"
			fail++
		} else {
			pass++
		}
		t.Logf("%s\t%s\t%s\t%d\t%d\t%s", c.ID, c.Surface, okS, lat, unknown, strings.Join(fails, " | "))
		if len(fails) > 0 {
			t.Errorf("%s (%s): %s\n  why: %s\n  answer: %s", c.ID, c.Surface, strings.Join(fails, "; "), c.Why, strings.TrimSpace(answer))
		}
		if c.Expect.MaxLatencyMs > 0 && lat > int64(c.Expect.MaxLatencyMs) {
			t.Logf("  ⚠ %s gecikme %d ms > %d ms (metrik, kırmızı değil)", c.ID, lat, c.Expect.MaxLatencyMs)
		}
	}
	// Kayıt sayacı ile skorlayıcı aynı tanımı paylaşır (E6): ShieldHits
	// toplamı, skorlayıcının uydurma sayımından bağımsız bir çapraz kontrol.
	time.Sleep(200 * time.Millisecond)
	rec.mu.Lock()
	var shield uint64
	for _, r := range rec.recs {
		shield += uint64(r.ShieldHits)
	}
	n := len(rec.recs)
	rec.mu.Unlock()
	t.Logf("prompt_version=%s model=%s n=%d pass=%d fail=%d recorded=%d shield_hits_total=%d",
		copilot.PromptVersion(), model, len(cases), pass, fail, n, shield)
}
