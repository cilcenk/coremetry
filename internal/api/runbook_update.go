package api

// runbook_update.go — RUNBOOK GÜNCELLEME ÖNERİSİ (v0.9.1198, AI Faz
// 5.5, onaylı plan).
//
// İki yarı:
//
// (1) Mevcut `runbook` yüzeyinin girdisine ÇÖZÜMDE KULLANILAN KANIT
// ZİNCİRİ eklenir (renderRunbookEvidenceChain — copilotRunbook çağırır):
// otomatik araştırmacının hipotezi (şüpheli + güven + aday yollar) ve
// bulunan derin sinyaller. Checklist artık "geçmişte ne kadar sürdü"nün
// yanında "neyin suçlu ÇIKTIĞINI" da biliyor.
//
// (2) Yeni `runbook-update` yüzeyi: Runbook sayfasının Executions
// sekmesinde, bir probleme bağlı tamamlanmış koşunun yanındaki "✨
// Öneri" — koşunun adım-adım gerçekleşmesi + problemin nasıl çözüldüğü
// (kanıt zinciri) mevcut runbook metniyle KIYASLANIR ve model somut bir
// güncelleme önerisi yazar. Öneri SAKLANMAZ (yeni şema yok — plan
// kabulü): ai_calls satırı atıf için yeter, kalıcılaştırmak operatörün
// runbook'u gerçekten düzenlemesiyle olur.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

const (
	ruMaxSteps       = 12
	ruMaxCandidates  = 3
	ruMaxSignals     = 6
	ruMaxFieldRunes  = 200 // adım talimatı / koşu notu / çıktı örneği
	ruMaxDescRunes   = 800 // runbook açıklaması (markdown "bilgi")
	ruMaxReasonRunes = 160
)

// renderRunbookEvidenceChain — hipotezden kanıt-zinciri bloğu. SAF;
// hem copilotRunbook (İngilizce prompt'a girdi) hem runbook-update
// kullanır, o yüzden alan adları İngilizce etiketli sade satırlar.
// Hipotez yoksa boş dize (çağıran hiçbir şey eklemez).
func renderRunbookEvidenceChain(h *chstore.RootCauseHypothesis) string {
	if h == nil || h.TopSuspect == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Root-cause evidence chain (automated investigator):\n")
	fmt.Fprintf(&b, "  Top suspect: %s (confidence %.2f)\n", h.TopSuspect, h.Confidence)
	for i, c := range h.Candidates {
		if i >= ruMaxCandidates {
			fmt.Fprintf(&b, "  (+%d more candidates)\n", len(h.Candidates)-ruMaxCandidates)
			break
		}
		line := fmt.Sprintf("  Candidate: %s (score %.2f", c.Service, c.Score)
		if len(c.Path) > 0 {
			line += ", path " + strings.Join(c.Path, " -> ")
		}
		line += ")"
		if c.Reason != "" {
			line += " — " + pmClamp(c.Reason, ruMaxReasonRunes)
		}
		b.WriteString(line + "\n")
	}
	if h.Deep != nil {
		n := 0
		for _, cs := range h.Deep.Checked {
			if !cs.Found {
				continue
			}
			if n >= ruMaxSignals {
				b.WriteString("  (more signals found, truncated)\n")
				break
			}
			fmt.Fprintf(&b, "  Signal found [%s]: %s\n", cs.Family, pmClamp(cs.Detail, ruMaxFieldRunes))
			n++
		}
	}
	return b.String()
}

type ruEvidenceInput struct {
	Runbook chstore.Runbook
	Exec    chstore.RunbookExecution
	Problem *chstore.Problem // nil = TTL'lenmiş; pakete dürüstçe yazılır
	Hyp     *chstore.RootCauseHypothesis
}

// renderRunbookUpdateEvidence — runbook-update yüzeyinin SAF kanıt
// paketi: mevcut runbook metni + koşunun adım-adım gerçekleşmesi +
// bağlı problemin çözüm kanıtı. Tablo-testli.
func renderRunbookUpdateEvidence(in ruEvidenceInput) string {
	var b strings.Builder
	rb := in.Runbook

	fmt.Fprintf(&b, "Runbook: %s\n", pmClamp(rb.Title, 200))
	if d := strings.TrimSpace(rb.Description); d != "" {
		fmt.Fprintf(&b, "Açıklama: %s\n", pmClamp(d, ruMaxDescRunes))
	}
	fmt.Fprintf(&b, "\nMevcut adımlar (%d):\n", len(rb.Steps))
	for i, st := range rb.Steps {
		if i >= ruMaxSteps {
			fmt.Fprintf(&b, "(%d adım, ilk %d listede)\n", len(rb.Steps), ruMaxSteps)
			break
		}
		fmt.Fprintf(&b, "%d. [%s] %s", st.Order, st.Kind, pmClamp(st.Title, ruMaxFieldRunes))
		if ins := pmClamp(st.Instructions, ruMaxFieldRunes); ins != "" {
			b.WriteString(" — " + ins)
		}
		b.WriteString("\n")
	}

	ex := in.Exec
	fmt.Fprintf(&b, "\nKoşu (%s, durum %s", pmTime(ex.StartedAt), ex.Status)
	if ex.CompletedAt > 0 {
		fmt.Fprintf(&b, ", süre %s", time.Duration(ex.CompletedAt-ex.StartedAt).Round(time.Second))
	}
	b.WriteString("):\n")
	for i, ss := range ex.StepStates {
		if i >= ruMaxSteps {
			fmt.Fprintf(&b, "(%d adım durumu, ilk %d listede)\n", len(ex.StepStates), ruMaxSteps)
			break
		}
		fmt.Fprintf(&b, "%d. %s — %s", ss.Order, pmClamp(ss.Title, ruMaxFieldRunes), ss.Status)
		if n := pmClamp(ss.Note, ruMaxFieldRunes); n != "" {
			b.WriteString("; operatör notu: " + n)
		}
		if e := pmClamp(ss.Error, ruMaxFieldRunes); e != "" {
			b.WriteString("; hata: " + e)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nBağlı problem:\n")
	if p := in.Problem; p != nil {
		fmt.Fprintf(&b, "[%s] %s @ %s — %s: değer %.4g (eşik %.4g), durum %s\n",
			p.Severity, pmClamp(p.RuleName, 120), p.Service, p.Metric, p.Value, p.Threshold, p.Status)
		if p.ResolvedAt != nil {
			fmt.Fprintf(&b, "Çözülme süresi: %s\n",
				time.Duration(*p.ResolvedAt-p.StartedAt).Round(time.Minute))
		}
		if s := strings.TrimSpace(p.AISummary); s != "" {
			fmt.Fprintf(&b, "AI özeti: %s\n", pmClamp(s, pmMaxSummaryRunes))
		}
	} else {
		b.WriteString("(problem kaydı bulunamadı — saklama süresi dolmuş olabilir)\n")
	}
	if chain := renderRunbookEvidenceChain(in.Hyp); chain != "" {
		b.WriteString("\n" + chain)
	}
	return b.String()
}

// runbookUpdateSuggest — POST /api/copilot/runbook-update/{id}
// (id = execution). requireCopilot route'ta; rol kapısı yok
// (copilotRunbook emsali — öneri salt-okunur metin, uygulamak zaten
// editörün runbook düzenlemesi).
func (s *Server) runbookUpdateSuggest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "execution id required")
		return
	}
	ctx := r.Context()
	ex, err := s.store.GetExecution(ctx, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if ex == nil {
		writeJSONError(w, http.StatusNotFound, "execution not found")
		return
	}
	if ex.ProblemID == "" {
		// Öneri, "runbook ne diyor" ile "problem nasıl çözüldü" kıyası —
		// probleme bağlı olmayan koşuda kıyaslanacak ikinci taraf yok.
		writeJSONError(w, http.StatusBadRequest,
			"bu koşu bir probleme bağlı değil — öneri, koşu + problem kanıtından türetilir")
		return
	}
	rb, err := s.store.GetRunbook(ctx, ex.RunbookID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rb == nil {
		writeJSONError(w, http.StatusNotFound, "runbook not found")
		return
	}
	// Problem + hipotez SOFT: TTL'lenmiş problem öneriyi imkânsız kılmaz,
	// paket yokluğu dürüstçe söyler (postmortem taslağı emsali).
	p, _ := s.store.GetProblem(ctx, ex.ProblemID)
	hyp, _ := s.store.GetHypothesis(ctx, "problem", ex.ProblemID)

	evidence := renderRunbookUpdateEvidence(ruEvidenceInput{
		Runbook: *rb, Exec: *ex, Problem: p, Hyp: hyp,
	})

	r, xid := withExchange(r)
	s.deliverExplain(w, r, xid,
		map[string]any{"runbookId": rb.ID, "problemId": ex.ProblemID},
		s.explainPrompt(r, copilot.SystemPromptRunbookUpdate(), evidence), "")
}
