package api

// evalset_fixture_test.go — v0.10.422 (CoSRE denetimi E1/E7): donmuş replay
// vakalarının ŞEMASI, yükleyicisi, surface→sistem promptu haritası ve SAF
// skorlayıcısı. Etiketsiz koşar: fikstür yazım hatası kırmızı test olur
// (sessiz atlama değil). Modele giden koşum evalset_test.go'da
// (//go:build evalset). Şema: internal/copilot/evalset/README.md.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/rca"
)

const evalsetSchema = "coremetry.evalset/1"

// evalsetDir — denetimin adlandırdığı yol (internal/copilot/evalset); .go
// dosyası yok, `go build ./...` görmez. Test cwd = paket dizini.
const evalsetDir = "../copilot/evalset"

type evalExpect struct {
	MustContain        []string `json:"mustContain,omitempty"`
	MustNotContain     []string `json:"mustNotContain,omitempty"`
	KnownEntities      []string `json:"knownEntities,omitempty"`
	KnownTeams         []string `json:"knownTeams,omitempty"` // v0.10.429 — team slotu için canlı takım listesi
	MaxUnknownEntities *int     `json:"maxUnknownEntities,omitempty"`
	Intent             string   `json:"intent,omitempty"`
	IntentService      string   `json:"intentService,omitempty"`
	MaxLatencyMs       int      `json:"maxLatencyMs,omitempty"`
	// v0.10.424 — RCA hakemi (surface RCAVerdict): kabul edilen verdict
	// kümesi + kanıt-ID atıf oranı (K2'den geçen / atıf yapılan).
	Verdicts                []string `json:"verdicts,omitempty"`
	MinEvidenceCitationRate *float64 `json:"minEvidenceCitationRate,omitempty"`
}

type evalCase struct {
	ID      string `json:"id"`
	Surface string `json:"surface"`
	Why     string `json:"why"`
	User    string `json:"user"`
	// Prompt — v0.10.423 (E5 export): kayıtlı örnek (sistem+kullanıcı
	// birleşik). Doluysa koşucu sistem promptunu BOŞ gönderir, prompt'u
	// kullanıcı olarak yollar; user ile birlikte verilmez.
	Prompt string `json:"prompt,omitempty"`
	// Hypothesis — v0.10.424: RCAVerdict vakası kullanıcı promptu yerine
	// chstore.RootCauseHypothesis taşır; prompt, katalog, rakipler ve şema
	// canlı yolla AYNI kodla üretilir (EvidenceCatalog.byID dışa kapalı —
	// katalog JSON'la elle kurulamaz).
	Hypothesis json.RawMessage `json:"hypothesis,omitempty"`
	Expect     evalExpect      `json:"expect"`
	file       string
}

// evalRCAInputs — hipotez → (katalog, rakipler, izinli varlıklar, kullanıcı
// promptu); rca_verdict.go buildRCAVerdictSurface ile aynı adımlar (extras
// ve imzalar boş: fikstür IO'suz).
func evalRCAInputs(h *chstore.RootCauseHypothesis, now time.Time) (rcaEvidenceCatalog, []string, []string, string) {
	cat := buildRCAEvidenceCatalog(h)
	cands := make([]string, 0, len(h.Candidates))
	for _, c := range h.Candidates {
		cands = append(cands, c.Service)
	}
	rivals := buildRCARivalOptions(cat, h.TopSuspect, cands)
	entities := rcaAllowedEntities(cat)
	return cat, rivals, entities, buildRCAVerdictPrompt(h, cat, rivals, nil, now)
}

// scoreRCACase — SAF: model JSON'u → canlı ayrıştırma + onarım + kalkan
// zinciri (applyRCAShieldsPure); verdict kümesi, atıf oranı
// (K2'den geçen / atıf yapılan; hiç atıf yoksa 1.0 yalnız
// insufficient_evidence'ta), K3 uydurma sayısı.
func scoreRCACase(c evalCase, h *chstore.RootCauseHypothesis, cat rcaEvidenceCatalog, answer string) (fails []string, unknown int) {
	var mv rcaModelVerdict
	parsed := false
	if err := json.Unmarshal([]byte(strings.TrimSpace(answer)), &mv); err == nil && rcaVerdictEnumOK(mv.Verdict) {
		parsed = true
	} else if fixed, ok := salvageJSONObject(answer); ok {
		if err := json.Unmarshal([]byte(fixed), &mv); err == nil && rcaVerdictEnumOK(mv.Verdict) {
			parsed = true
		}
	}
	if !parsed {
		return []string{"unparsed: " + strings.TrimSpace(answer)}, 0
	}
	cited := len(mv.RootCause.Evidence)
	for _, st := range mv.CausalChain {
		cited += len(st.Evidence)
	}
	sh := rcaShieldReport{Parsed: true}
	v := applyRCAShieldsPure(h, cat, mv, &sh)
	if len(c.Expect.Verdicts) > 0 {
		okV := false
		for _, w := range c.Expect.Verdicts {
			if v.Verdict == w {
				okV = true
			}
		}
		if !okV {
			fails = append(fails, fmt.Sprintf("verdict %q ∉ %v", v.Verdict, c.Expect.Verdicts))
		}
	}
	if c.Expect.MinEvidenceCitationRate != nil {
		rate := 1.0
		if cited > 0 {
			rate = float64(cited-len(sh.RejectedEvidence)) / float64(cited)
		} else if v.Verdict != "insufficient_evidence" {
			rate = 0
		}
		if rate < *c.Expect.MinEvidenceCitationRate {
			fails = append(fails, fmt.Sprintf("evidence citation rate %.2f < %.2f (cited %d, rejected %v)", rate, *c.Expect.MinEvidenceCitationRate, cited, sh.RejectedEvidence))
		}
	}
	unknown = len(sh.UnknownEntities)
	if c.Expect.MaxUnknownEntities != nil && unknown > *c.Expect.MaxUnknownEntities {
		fails = append(fails, fmt.Sprintf("unknown entities %d > %d: %v", unknown, *c.Expect.MaxUnknownEntities, sh.UnknownEntities))
	}
	return fails, unknown
}

// evalCaseInput — koşucu ve skorlayıcı için (system, user) çifti.
func evalCaseInput(c evalCase, system string) (string, string) {
	if c.Prompt != "" {
		return "", c.Prompt
	}
	return system, c.User
}

type evalFile struct {
	Schema string     `json:"schema"`
	Cases  []evalCase `json:"cases"`
}

// evalSystemPrompt — fikstürdeki surface adı → copilot.SystemPromptX.
// Eksik ad kırmızı test (TestEvalsetFixturesValid), sessiz atlama değil.
func evalSystemPrompt(surface string) (string, bool) {
	switch surface {
	case "Trace":
		return copilot.SystemPromptTrace(), true
	case "Span":
		return copilot.SystemPromptSpan(), true
	case "Problem":
		return copilot.SystemPromptProblem(), true
	case "Exception":
		return copilot.SystemPromptException(), true
	case "Incident":
		return copilot.SystemPromptIncident(), true
	case "Anomaly":
		return copilot.SystemPromptAnomaly(), true
	case "ServiceHealth":
		return copilot.SystemPromptServiceHealth(), true
	case "Runbook":
		return copilot.SystemPromptRunbook(), true
	case "CompareTraces":
		return copilot.SystemPromptCompareTraces(), true
	case "DeployImpact":
		return copilot.SystemPromptDeployImpact(), true
	case "SLOBurn":
		return copilot.SystemPromptSLOBurn(), true
	case "SlowQuery":
		return copilot.SystemPromptSlowQuery(), true
	case "NLToQuery":
		return copilot.SystemPromptNLToQuery(), true
	case "CHQueryOptimize":
		return copilot.SystemPromptCHQueryOptimize(), true
	case "RCAVerdict":
		return copilot.SystemPromptRCAVerdict(), true
	case "ServiceCharts":
		return copilot.SystemPromptServiceCharts(), true
	case "GeneralChat":
		return copilot.SystemPromptGeneralChat(), true
	case "Chat":
		return copilot.SystemPromptChat(), true
	case "IntentClassify":
		return copilot.SystemPromptIntentClassify(), true
	}
	return "", false
}

// evalJSONSurface — JSON kipinde çağrılan yüzeyler (canlı yolla aynı).
func evalJSONSurface(surface string) bool {
	switch surface {
	case "IntentClassify", "RCAVerdict", "NLToQuery", "CHQueryOptimize":
		return true
	}
	return false
}

func loadEvalset(t *testing.T) []evalCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(evalsetDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var out []evalCase
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var ef evalFile
		if err := json.Unmarshal(b, &ef); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if ef.Schema != evalsetSchema {
			t.Fatalf("%s: schema %q, want %q", f, ef.Schema, evalsetSchema)
		}
		for i := range ef.Cases {
			ef.Cases[i].file = filepath.Base(f)
			out = append(out, ef.Cases[i])
		}
	}
	return out
}

// scoreEvalCase — SAF: cevap + hata → ihlal listesi ve uydurma ad sayısı.
// mustContain/mustNotContain büyük/küçük harf duyarsız; uydurma sayımı
// rca.CountUnknownEntities (E6 sayacıyla AYNI tanım); niyet
// parseIntentJSON (canlı ayrıştırıcı — "none" = eşleşmedi).
func scoreEvalCase(c evalCase, system, answer string, err error) (fails []string, unknown int) {
	if err != nil {
		return []string{"error: " + err.Error()}, 0
	}
	low := strings.ToLower(answer)
	for _, m := range c.Expect.MustContain {
		if !strings.Contains(low, strings.ToLower(m)) {
			fails = append(fails, "missing: "+m)
		}
	}
	for _, m := range c.Expect.MustNotContain {
		if strings.Contains(low, strings.ToLower(m)) {
			fails = append(fails, "forbidden: "+m)
		}
	}
	sys, user := evalCaseInput(c, system)
	unknown = int(rca.CountUnknownEntities(rca.LowerKnownSet(c.Expect.KnownEntities...), sys+"\n"+user, answer))
	if c.Expect.MaxUnknownEntities != nil && unknown > *c.Expect.MaxUnknownEntities {
		fails = append(fails, fmt.Sprintf("unknown entities %d > %d", unknown, *c.Expect.MaxUnknownEntities))
	}
	if c.Expect.Intent != "" {
		route, _, matched := parseIntentJSON(answer, c.Expect.KnownEntities, nil, c.Expect.KnownTeams, "")
		got := "none"
		if matched {
			got = string(route.Intent)
		}
		if got != c.Expect.Intent {
			fails = append(fails, fmt.Sprintf("intent %q, want %q (raw %s)", got, c.Expect.Intent, strings.TrimSpace(answer)))
		} else if matched && c.Expect.IntentService != "" && route.Service != c.Expect.IntentService {
			fails = append(fails, fmt.Sprintf("intent service %q, want %q", route.Service, c.Expect.IntentService))
		}
	}
	return fails, unknown
}

// TestEvalsetFixturesValid — etiketsiz; fikstür sözleşmesi.
func TestEvalsetFixturesValid(t *testing.T) {
	cases := loadEvalset(t)
	if len(cases) < 20 {
		t.Fatalf("en az 20 vaka bekleniyor, %d", len(cases))
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.ID == "" || seen[c.ID] {
			t.Errorf("%s: id boş ya da tekrar: %q", c.file, c.ID)
		}
		seen[c.ID] = true
		if _, ok := evalSystemPrompt(c.Surface); !ok {
			t.Errorf("%s/%s: surface %q çözülemiyor", c.file, c.ID, c.Surface)
		}
		isRCA := c.Surface == "RCAVerdict"
		if strings.TrimSpace(c.Why) == "" || (!isRCA && strings.TrimSpace(c.User) == "" && strings.TrimSpace(c.Prompt) == "") {
			t.Errorf("%s/%s: why ve (user | prompt) zorunlu", c.file, c.ID)
		}
		if isRCA != (len(c.Hypothesis) > 0) {
			t.Errorf("%s/%s: hypothesis yalnız RCAVerdict yüzeyinde ve orada zorunlu", c.file, c.ID)
		}
		if isRCA {
			var h chstore.RootCauseHypothesis
			if err := json.Unmarshal(c.Hypothesis, &h); err != nil || h.Service == "" {
				t.Errorf("%s/%s: hypothesis çözülemiyor ya da service boş: %v", c.file, c.ID, err)
			}
			if len(c.Expect.Verdicts) == 0 {
				t.Errorf("%s/%s: RCA vakası verdicts beklentisi taşımalı", c.file, c.ID)
			}
		}
		if c.User != "" && c.Prompt != "" {
			t.Errorf("%s/%s: user ve prompt birlikte verilmez", c.file, c.ID)
		}
		e := c.Expect
		if len(e.MustContain) == 0 && len(e.MustNotContain) == 0 && e.MaxUnknownEntities == nil && e.Intent == "" && len(e.Verdicts) == 0 {
			t.Errorf("%s/%s: en az bir beklenti gerekli", c.file, c.ID)
		}
		if (c.Surface == "IntentClassify") != (e.Intent != "") {
			t.Errorf("%s/%s: intent beklentisi yalnız IntentClassify yüzeyinde ve orada zorunlu", c.file, c.ID)
		}
	}
}

// TestScoreEvalCase — skorlayıcı saf ve tablolu: E7'nin "davranış" pini
// modelsiz de doğrulanır (skorlayıcının kendisi yalan söylemesin).
func TestScoreEvalCase(t *testing.T) {
	one := 0
	prose := evalCase{ID: "x", Surface: "Problem", User: "Service: checkout", Expect: evalExpect{
		MustContain: []string{"2.14.0"}, MustNotContain: []string{"kubectl"},
		KnownEntities: []string{"checkout"}, MaxUnknownEntities: &one,
	}}
	if fails, _ := scoreEvalCase(prose, "sys", "checkout 2.14.0 sonrası bozuldu", nil); len(fails) != 0 {
		t.Fatalf("temiz cevap kızardı: %v", fails)
	}
	fails, unknown := scoreEvalCase(prose, "sys", "kubectl ile ghost-gateway ve phantom-svc'yi yeniden başlat", nil)
	if unknown != 2 || len(fails) != 3 {
		t.Fatalf("3 ihlal (missing, forbidden, unknown) bekleniyor: %v unknown=%d", fails, unknown)
	}
	if fails, _ := scoreEvalCase(prose, "sys", "", fmt.Errorf("boom")); len(fails) != 1 || !strings.HasPrefix(fails[0], "error:") {
		t.Fatalf("hata tek ihlal: %v", fails)
	}
	intent := evalCase{ID: "i", Surface: "IntentClassify", User: "checkout nasıl?", Expect: evalExpect{
		Intent: "service_health", IntentService: "checkout", KnownEntities: []string{"checkout", "payments"},
	}}
	if fails, _ := scoreEvalCase(intent, "sys", `{"intent":"service_health","service":"checkout","env":"","rangeS":3600}`, nil); len(fails) != 0 {
		t.Fatalf("doğru niyet kızardı: %v", fails)
	}
	if fails, _ := scoreEvalCase(intent, "sys", "```json\n{\"intent\":\"problems\",\"service\":\"\"}\n```", nil); len(fails) != 1 || !strings.HasPrefix(fails[0], "intent \"problems\"") {
		t.Fatalf("yanlış niyet yakalanmalı: %v", fails)
	}
	none := evalCase{ID: "n", Surface: "IntentClassify", User: "hava?", Expect: evalExpect{Intent: "none"}}
	if fails, _ := scoreEvalCase(none, "sys", `{"intent":"none"}`, nil); len(fails) != 0 {
		t.Fatalf("none kızardı: %v", fails)
	}
}

// v0.10.424 — RCA skorlayıcısı saf ve tablolu: katalog kimlikleri
// (E1..), uydurma kimlik (E9) K2'de düşer ve oranı bozar, serbest metindeki
// uydurma ad K3'te sayılır, verdict kümesi dışı kızarır.
func TestScoreRCACase(t *testing.T) {
	h := &chstore.RootCauseHypothesis{Service: "checkout", TopSuspect: "payments", TopScore: 0.9, Confidence: 0.8,
		Candidates: []chstore.ScoredCause{{Service: "payments", Score: 0.9, Hops: 1, Reason: "deploy"}, {Service: "inventory", Score: 0.1, Hops: 1}}}
	cat, rivals, entities, user := evalRCAInputs(h, time.Unix(1_700_000_000, 0))
	if len(cat.PositiveIDs()) < 2 || len(entities) == 0 || user == "" || len(rivals) == 0 {
		t.Fatalf("girdiler: pos=%v entities=%v rivals=%v", cat.PositiveIDs(), entities, rivals)
	}
	one := 1.0
	zero := 0
	c := evalCase{ID: "r", Surface: "RCAVerdict", Expect: evalExpect{Verdicts: []string{"root_cause_identified", "probable_cause"}, MinEvidenceCitationRate: &one, MaxUnknownEntities: &zero}}
	good := `{"verdict":"probable_cause","title":"payments deploy","summary":"payments 503 döndürüyor","root_cause":{"entity":"payments","failure_mode":"503","trigger":"deploy","latent_weakness":"","evidence":["E1"]},"causal_chain":[{"entity":"checkout","effect":"hata oranı","evidence":["E1"]}],"rejected_hypotheses":[],"model_confidence":0.7,"missing_evidence":[],"remediation":[]}`
	if fails, unknown := scoreRCACase(c, h, cat, good); len(fails) != 0 || unknown != 0 {
		t.Fatalf("temiz verdict kızardı: %v unknown=%d", fails, unknown)
	}
	bad := `{"verdict":"root_cause_identified","title":"ghost-gateway","summary":"ghost-gateway çöktü","root_cause":{"entity":"payments","failure_mode":"x","trigger":"y","latent_weakness":"","evidence":["E1","E9"]},"causal_chain":[],"rejected_hypotheses":[],"model_confidence":0.9,"missing_evidence":[],"remediation":[]}`
	fails, unknown := scoreRCACase(c, h, cat, bad)
	if unknown != 1 || len(fails) != 2 {
		t.Fatalf("atıf oranı (E9 düşer) + uydurma ad bekleniyor: %v unknown=%d", fails, unknown)
	}
	if fails, _ := scoreRCACase(c, h, cat, "Elbette, işte JSON: "+good); len(fails) != 0 {
		t.Fatalf("gevezelik onarılmalı (salvage): %v", fails)
	}
	if fails, _ := scoreRCACase(c, h, cat, `{"verdict":"insufficient_evidence","root_cause":{"evidence":[]}}`); len(fails) != 1 || !strings.HasPrefix(fails[0], "verdict") {
		t.Fatalf("küme dışı verdict kızarmalı: %v", fails)
	}
}
