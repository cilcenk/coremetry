package api

// ai_evalset.go — v0.10.423 (CoSRE denetimi E5): 👎 alan cevaplar evalset
// vakası olarak DIŞA AKTARILIR (GET /api/ai/evalset/export, admin, JSON
// ek dosya, audit). Sunucu dosya yazmaz (konteyner FS salt-okunur; repo
// sözleşmesi de bu değil): operatör indirir, müşteri adlarını temizler ve
// internal/copilot/evalset/ altına ELLE alır — E1 koşumu oradan okur.
//
// Şekil E1 fikstürüyle aynı (coremetry.evalset/1). Fark: `user` yerine
// `prompt` (kayıtlı örnek sistem+kullanıcı birleşik, 4 KiB kırpık olabilir)
// ve boş `expect` — 👎 vakası KÖTÜ cevabı negatif çapa, operatör yorumunu
// `why` olarak taşır; "doğru cevabı" sunucu uyduramaz. provenance.truncated
// true ise koşucu vakayı kırpık prompt'la puanlamaz (E8 SamplePromptCap;
// evalCaseSkipReason, v0.10.431'de uygulandı).
//
// ?exchangeId= (v0.10.431): NOKTA okuma — pencere ve 200 satır tavanı
// uygulanmaz; 👎 yoksa 404 (boş "cases: []" ile "hiç 👎 yok" ayırt
// edilemiyordu).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

const evalsetExportSchema = "coremetry.evalset/1"

type evalsetProvenance struct {
	ExchangeID    string `json:"exchangeId"`
	SurfaceLabel  string `json:"surfaceLabel"` // ai_calls.surface
	CreatedAt     string `json:"createdAt"`
	RatedBy       string `json:"ratedBy,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	PromptVersion string `json:"promptVersion,omitempty"`
	ProfileID     string `json:"profileId,omitempty"`
	ErrorClass    string `json:"errorClass,omitempty"`
	PromptChars   uint32 `json:"promptChars"`
	ResponseChars uint32 `json:"responseChars"`
	Truncated     bool   `json:"truncated"`
}

type evalsetExportCase struct {
	ID         string            `json:"id"`
	Surface    string            `json:"surface"`
	Why        string            `json:"why"`
	Prompt     string            `json:"prompt"`
	Response   string            `json:"response"`
	Expect     map[string]any    `json:"expect"`
	Provenance evalsetProvenance `json:"provenance"`
}

type evalsetExportPayload struct {
	Schema           string              `json:"schema"`
	Source           string              `json:"source"`
	ExportedAt       string              `json:"exportedAt"`
	CoremetryVersion string              `json:"coremetryVersion"`
	RangeS           int64               `json:"rangeS"`
	Cases            []evalsetExportCase `json:"cases"`
	Dropped          int                 `json:"dropped"` // cevabı olmayan (purge yetimi) satırlar
}

// evalSurfaceFromLabel — ai_calls.surface etiketi → evalset surface adı
// (copilot.SystemPromptX). Bilinmeyen etiket "Unknown": fikstür doğrulaması
// (TestEvalsetFixturesValid) commit'te kırmızı olur — operatör düzeltir.
func evalSurfaceFromLabel(label string) string {
	switch label {
	case "explain-trace":
		return "Trace"
	case "explain-span":
		return "Span"
	case "explain-problem", "problem-auto-explain":
		return "Problem"
	case "explain-exception", "exception-auto-explain":
		return "Exception"
	case "explain-incident":
		return "Incident"
	case "explain-anomaly":
		return "Anomaly"
	case "explain-service":
		return "ServiceHealth"
	case "runbook":
		return "Runbook"
	case "compare-traces":
		return "CompareTraces"
	case "deploy-impact":
		return "DeployImpact"
	case "explain-slo":
		return "SLOBurn"
	case "slow-query", "explain-slow-query":
		return "SlowQuery"
	case "nl-to-query", "nl-query":
		return "NLToQuery"
	case "ch-optimize":
		return "CHQueryOptimize"
	case "rootcause-verdict", "rootcause-auto":
		return "RCAVerdict"
	case "explain-charts":
		return "ServiceCharts"
	case "chat-general", "chat-intent-none":
		return "GeneralChat"
	case "chat", "chat-guided", "chat-drawer":
		return "Chat"
	case "chat-intent":
		return "IntentClassify"
	}
	return "Unknown"
}

// evalsetCaseFrom — SAF: 👎 satırı + kaynaktan okunan çağrı → vaka; cevabı
// olmayan (purge yetimi / kayıt yok) satır atlanır (ok=false).
func evalsetCaseFrom(fb chstore.NegativeFeedbackCall, call *chstore.AICallEvalRow) (evalsetExportCase, bool) {
	if call == nil || strings.TrimSpace(call.Response) == "" {
		return evalsetExportCase{}, false
	}
	why := strings.TrimSpace(fb.Comment)
	if why == "" {
		why = "operatör 👎 verdi (yorum yok)"
	}
	return evalsetExportCase{
		ID:       "fb-" + fb.ExchangeID,
		Surface:  evalSurfaceFromLabel(call.Surface),
		Why:      why,
		Prompt:   call.Prompt,
		Response: call.Response,
		Expect:   map[string]any{},
		Provenance: evalsetProvenance{
			ExchangeID: fb.ExchangeID, SurfaceLabel: call.Surface,
			CreatedAt: call.CreatedAt.UTC().Format(time.RFC3339Nano), RatedBy: fb.UserEmail,
			Provider: call.Provider, Model: call.Model,
			PromptVersion: call.PromptVersion, ProfileID: call.ProfileID, ErrorClass: call.ErrorClass,
			PromptChars: call.PromptChars, ResponseChars: call.ResponseChars,
			Truncated: int(call.PromptChars) > len(call.Prompt) || int(call.ResponseChars) > len(call.Response),
		},
	}, true
}

// exportAIEvalset — GET /api/ai/evalset/export?rangeS=&exchangeId=
// Admin kapılı; JSON ek dosya (config export deseni); audit. serveCached
// KULLANILMAZ (ek dosya başlıkları).
func (s *Server) exportAIEvalset(w http.ResponseWriter, r *http.Request) {
	rangeS := int64(7 * 86400)
	if v := r.URL.Query().Get("rangeS"); v != "" {
		if n := parseInt(v, 0); n > 0 && n <= 90*86400 {
			rangeS = int64(n)
		}
	}
	only := strings.TrimSpace(r.URL.Query().Get("exchangeId"))
	if len(only) > aiFeedbackMaxIDLen {
		http.Error(w, "exchangeId too long", http.StatusBadRequest)
		return
	}
	to := time.Now()
	from := to.Add(-time.Duration(rangeS) * time.Second)
	var rows []chstore.NegativeFeedbackCall
	if only != "" {
		row, err := s.store.NegativeFeedbackCallByExchange(r.Context(), only) // v0.10.431 nokta okuma
		if err != nil {
			writeErr(w, err)
			return
		}
		if row == nil {
			http.Error(w, "exchange has no thumbs-down feedback", http.StatusNotFound)
			return
		}
		rows = []chstore.NegativeFeedbackCall{*row}
	} else {
		var err error
		rows, err = s.store.ListNegativeFeedbackCalls(r.Context(), from, to, 200)
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	out := evalsetExportPayload{
		Schema: evalsetExportSchema, Source: "thumbs-down",
		ExportedAt: to.UTC().Format(time.RFC3339), CoremetryVersion: s.version,
		RangeS: rangeS, Cases: []evalsetExportCase{},
	}
	for _, fb := range rows {
		call, err := s.store.AICallForEvalset(r.Context(), fb.ExchangeID)
		if err != nil {
			writeErr(w, err)
			return
		}
		c, ok := evalsetCaseFrom(fb, call)
		if !ok {
			out.Dropped++
			continue
		}
		out.Cases = append(out.Cases, c)
	}
	fname := fmt.Sprintf("coremetry-evalset-%s.json", to.UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		s.audit(r, "ai.evalset.export.failed", "ai_evalset", only, fmt.Sprintf(`{"error":%q}`, err.Error()))
		return
	}
	// Veri sistemi TERK EDİYOR (prompt gövdeleri + yorumlar + oylayan e-posta):
	// config.export gibi audit'li.
	s.audit(r, "ai.evalset.export", "ai_evalset", only,
		fmt.Sprintf(`{"cases":%d,"dropped":%d,"rangeS":%d}`, len(out.Cases), out.Dropped, rangeS))
}

// registerAIEvalsetRoutes — ai_routes.go'dan çağrılır; api.go büyümez.
func (s *Server) registerAIEvalsetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/evalset/export", auth.RequireRole(auth.RoleAdmin, s.exportAIEvalset))
}
