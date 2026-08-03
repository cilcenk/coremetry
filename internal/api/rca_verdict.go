// v0.9.559 — RCA verdict orkestrasyonu.
//
// Tasarım: docs/cosre-verdict-design.md
//
// Akış: katalog kur → rakipleri üret → modele sor (şema kilitli) →
// KALKANLARDAN geçir → etkiyi ClickHouse'tan ölç → zarfla.
//
// Modelin ürettiği ham şekil (rcaModelVerdict) ile operatöre dönen
// şekil (RCAVerdict) AYNI ŞEY DEĞİL: kalkanlar aradaki farkı üretir ve
// ne yaptıklarını rapor eder.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cilcenk/coremetry/internal/anomaly"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// ── Modelin ürettiği ham şekil ───────────────────────────────────────

type rcaModelRootCause struct {
	Entity         string   `json:"entity"`
	FailureMode    string   `json:"failure_mode"`
	Trigger        string   `json:"trigger"`
	LatentWeakness string   `json:"latent_weakness"`
	Evidence       []string `json:"evidence"`
}

type rcaModelChainStep struct {
	Entity   string   `json:"entity"`
	Effect   string   `json:"effect"`
	Evidence []string `json:"evidence"`
}

type rcaModelRejected struct {
	Hypothesis string   `json:"hypothesis"`
	RefutedBy  []string `json:"refuted_by"`
	Reason     string   `json:"reason"`
}

type rcaModelRemediation struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Target string `json:"target"`
	Risk   string `json:"risk"`
}

type rcaModelVerdict struct {
	Verdict            string                `json:"verdict"`
	Title              string                `json:"title"`
	Summary            string                `json:"summary"`
	RootCause          rcaModelRootCause     `json:"root_cause"`
	CausalChain        []rcaModelChainStep   `json:"causal_chain"`
	RejectedHypotheses []rcaModelRejected    `json:"rejected_hypotheses"`
	ModelConfidence    float64               `json:"model_confidence"`
	MissingEvidence    []string              `json:"missing_evidence"`
	Remediation        []rcaModelRemediation `json:"remediation"`
}

// ── Operatöre dönen şekil ────────────────────────────────────────────

// RCAVerdict — kalkanlardan geçmiş verdict.
type RCAVerdict struct {
	Verdict            string                `json:"verdict"`
	Title              string                `json:"title"`
	Summary            string                `json:"summary"`
	RootCause          rcaModelRootCause     `json:"rootCause"`
	CausalChain        []rcaModelChainStep   `json:"causalChain,omitempty"`
	RejectedHypotheses []rcaModelRejected    `json:"rejectedHypotheses,omitempty"`
	MissingEvidence    []string              `json:"missingEvidence,omitempty"`
	Remediation        []rcaModelRemediation `json:"remediation,omitempty"`

	// Confidence — TAVANLANMIŞ nihai güven (bkz. capRCAConfidence).
	Confidence float64 `json:"confidence"`
	// ModelConfidence — modelin kendi BEYANI. Ayrı tutulur ki tavanın
	// ne kadar indirdiği görünsün.
	ModelConfidence float64 `json:"modelConfidence"`
	// HypothesisConfidence — deterministik korelasyon motorunun güveni.
	//
	// Üç ayrı "confidence" aynı ekranda buluşuyordu ve üçü farklı
	// şeydi; adlandırma bu yüzden açık (tasarım §3).
	HypothesisConfidence float64 `json:"hypothesisConfidence"`

	// Evidence — modelin atıf yaptığı kanıtların SUNUCU metni.
	// Modelin ürettiği hiçbir metin buraya girmez.
	Evidence []rcaEvidenceRef `json:"evidence,omitempty"`

	Impact  *RCAImpact      `json:"impact,omitempty"`
	Shields rcaShieldReport `json:"shields"`
}

// ── Prompt ───────────────────────────────────────────────────────────

// buildRCAVerdictPrompt — kullanıcı prompt'u. SAF (test edilebilir).
func buildRCAVerdictPrompt(h *chstore.RootCauseHypothesis, cat rcaEvidenceCatalog, rivals []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ANKOR: %s / %s\n", h.AnchorKind, h.AnchorID)
	if h.Service != "" {
		fmt.Fprintf(&b, "ANKOR SERVİSİ: %s\n", h.Service)
	}
	b.WriteString("\n")
	b.WriteString(anomaly.HypothesisPromptBlockTR(h))
	b.WriteString("\n")
	b.WriteString(renderRCAEvidenceCatalog(cat))

	if len(rivals) > 0 {
		b.WriteString("\nRAKİP HİPOTEZLER — rejected_hypotheses.hypothesis alanında\n")
		b.WriteString("YALNIZ bu listeden seç, kendi rakibini uydurma:\n")
		for _, r := range rivals {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
	}
	return b.String()
}

// ── Orkestrasyon ─────────────────────────────────────────────────────

// rcaVerdictSurface — /ai attribution etiketi. aiSurfaceFromPath bu
// yolu "other" kovasına atıyordu; açık ad olmadan verdict kalitesi
// ölçülemez.
const rcaVerdictSurface = "rootcause-verdict"

// buildRCAVerdict — tam akış. Hata DÖNMEZ: her başarısızlık dürüst bir
// insufficient_evidence verdict'ine dönüşür, çünkü bir hakemin
// "cevaplayamadım" demesi, hiç cevap dönmemesinden daha kullanışlıdır.
//
// prose dönüşü *string: fallback'te nil kalır ve frontend'in
// "anlatım yok" dalı ULAŞILABİLİR kalır. Deterministik yedek cümle
// verdict.summary'ye yazılır, prose'a DEĞİL — aksi hâlde yedek cümle
// gerçek LLM anlatımıyla aynı kutuda çizilir ve operatör ayırt edemez.
func (s *Server) buildRCAVerdict(ctx context.Context, h *chstore.RootCauseHypothesis, anchorStartNs int64) (*RCAVerdict, *string) {
	cat := buildRCAEvidenceCatalog(h)

	candidates := make([]string, 0, len(h.Candidates))
	for _, c := range h.Candidates {
		candidates = append(candidates, c.Service)
	}
	rivals := buildRCARivalOptions(cat, h.TopSuspect, candidates)
	entities := rcaAllowedEntities(cat)

	raw, err := s.copilotExplainJSONSurface(ctx, rcaVerdictSurface,
		copilot.SystemPromptRCAVerdict(),
		buildRCAVerdictPrompt(h, cat, rivals),
		rcaVerdictSchema(entities, rivals))

	var sh rcaShieldReport
	var mv rcaModelVerdict
	if err == nil {
		if perr := json.Unmarshal([]byte(strings.TrimSpace(raw)), &mv); perr == nil {
			sh.Parsed = true
		}
	}
	if !sh.Parsed {
		// Tek onarım turu: JSON dışı gürültü (```json çitleri, önsöz)
		// en sık hata ve ucuz düzeliyor.
		if fixed, ok := salvageJSONObject(raw); ok {
			if perr := json.Unmarshal([]byte(fixed), &mv); perr == nil {
				sh.Parsed = true
				sh.Repaired = true
			}
		}
	}
	if !sh.Parsed {
		return fallbackRCAVerdict(h, cat, &sh), nil
	}

	return s.applyRCAShields(ctx, h, cat, mv, &sh, anchorStartNs), proseFrom(mv.Summary)
}

// proseFrom — özet metnini prose'a çevirir; boşsa nil (dürüst boş).
func proseFrom(summary string) *string {
	t := strings.TrimSpace(summary)
	if t == "" {
		return nil
	}
	return &t
}

// salvageJSONObject — metnin içindeki ilk tam JSON nesnesini çıkarır.
// Küçük modeller şema kipinde bile ara sıra ```json çiti veya kısa bir
// önsöz basıyor.
func salvageJSONObject(s string) (string, bool) {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return "", false
	}
	return s[i : j+1], true
}

// applyRCAShields — kalkanlar + etki ölçümü. Etki *chstore.Store'a
// bağlı olduğu için kalkan mantığı SAF ikizde duruyor (aşağıda);
// bu sarmalayıcı yalnız ölçümü ekliyor.
func (s *Server) applyRCAShields(ctx context.Context, h *chstore.RootCauseHypothesis,
	cat rcaEvidenceCatalog, mv rcaModelVerdict, sh *rcaShieldReport, anchorStartNs int64) *RCAVerdict {
	out := applyRCAShieldsPure(h, cat, mv, sh)
	// K5 — etki, kök neden VARLIĞI için ölçülür (ankorun servisi için
	// değil): tipik bir P1'de ikisi farklıdır.
	entity := out.RootCause.Entity
	if entity == "" {
		entity = h.Service
	}
	// v0.9.576 — ankorun GERÇEK başlangıcı. h.ComputedAt hipotezin
	// SENTEZLENDİĞİ andı, olayın başladığı an değil: pencere olayın
	// ilk dakikalarını kaçırıyordu. 0 = çözülemedi → rcaImpactWindow
	// tutarsız pencereyi reddeder ve ölçüm "yapılamadı" der.
	startNs := anchorStartNs
	if startNs == 0 {
		startNs = h.ComputedAt
	}
	out.Impact = s.computeRCAImpact(ctx, entity, h.Service, startNs)
	return out
}

// applyRCAShieldsPure — K2/K3/K4'ü uygular. SAF: hiçbir IO yok, tam
// akış tablo-testlenebilir. Kalkanların GERÇEKTEN ateşlediğini
// kanıtlamanın tek yolu bu.
func applyRCAShieldsPure(h *chstore.RootCauseHypothesis,
	cat rcaEvidenceCatalog, mv rcaModelVerdict, sh *rcaShieldReport) *RCAVerdict {

	// K2 — destek kanıtları (negatif uzay reddedilir).
	mv.RootCause.Evidence = filterEvidenceIDs(cat, mv.RootCause.Evidence, sh)
	for i := range mv.CausalChain {
		mv.CausalChain[i].Evidence = filterEvidenceIDs(cat, mv.CausalChain[i].Evidence, sh)
	}

	// K4 — eleme geçerli mi? Çürütme kanıtları N kabul eder ama destek
	// kanıtlarıyla KESİŞEMEZ.
	validRefutation := false
	kept := mv.RejectedHypotheses[:0]
	for _, rej := range mv.RejectedHypotheses {
		rej.RefutedBy = filterRefutationIDs(cat, rej.RefutedBy, sh)
		if refutationValid(mv.RootCause.Evidence, rej.RefutedBy) {
			validRefutation = true
			kept = append(kept, rej)
			continue
		}
		sh.RefutationInvalid = true
		sh.note("eleme geçersiz sayıldı (%q): çürütme kanıtı yok ya da destek kanıtıyla aynı",
			rej.Hypothesis)
	}
	mv.RejectedHypotheses = kept

	// K3 — enum'la korunmayan TÜM serbest metin.
	texts := []string{mv.Title, mv.Summary,
		mv.RootCause.FailureMode, mv.RootCause.Trigger, mv.RootCause.LatentWeakness}
	for _, c := range mv.CausalChain {
		texts = append(texts, c.Entity, c.Effect)
	}
	for _, r := range mv.RejectedHypotheses {
		texts = append(texts, r.Hypothesis, r.Reason)
	}
	for _, r := range mv.Remediation {
		texts = append(texts, r.Action, r.Target)
	}
	checkRCAEntities(cat, texts, sh)

	// Varlık beyaz listesi: enum düşmüş olabilir (şema desteklenmiyorsa).
	if mv.RootCause.Entity != "" && !cat.Entities[mv.RootCause.Entity] {
		sh.note("kök neden varlığı %q kanıt kataloğunda yok — verdict düşürüldü",
			mv.RootCause.Entity)
		mv.Verdict = "insufficient_evidence"
		mv.RootCause.Entity = ""
	}

	conf := capRCAConfidence(mv.ModelConfidence, h.Confidence, validRefutation, sh)

	// Kanıt yokken "kök neden bulundu" diyemez.
	if len(mv.RootCause.Evidence) == 0 && mv.Verdict == "root_cause_identified" {
		mv.Verdict = "probable_cause"
		sh.note("destek kanıtı kalmadı — verdict probable_cause'a indirildi")
	}

	out := &RCAVerdict{
		Verdict: mv.Verdict, Title: mv.Title, Summary: mv.Summary,
		RootCause: mv.RootCause, CausalChain: mv.CausalChain,
		RejectedHypotheses: mv.RejectedHypotheses,
		MissingEvidence:    mv.MissingEvidence, Remediation: mv.Remediation,
		Confidence:           conf,
		ModelConfidence:      mv.ModelConfidence,
		HypothesisConfidence: h.Confidence,
		Shields:              *sh,
	}

	// Atıf yapılan kanıtların SUNUCU metni — model metni değil.
	seen := map[string]bool{}
	for _, id := range mv.RootCause.Evidence {
		if ref, ok := cat.lookup(id); ok && !seen[id] {
			seen[id] = true
			out.Evidence = append(out.Evidence, ref)
		}
	}

	return out
}

// fallbackRCAVerdict — model çözümlenemedi. Deterministik hipotezden
// dürüst bir verdict kurulur.
//
// Bu bir "yedek anlatım" DEĞİL: verdict'in kendisi, kanıtın yetmediğini
// söyleyen geçerli bir cevaptır. shields.parsed=false yanıtta döner ve
// UI'da GÖSTERİLMELİ — yoksa sahte bir "kanıt yetersiz", gerçek kanıt
// yokluğundan ayırt edilemez.
func fallbackRCAVerdict(h *chstore.RootCauseHypothesis, cat rcaEvidenceCatalog, sh *rcaShieldReport) *RCAVerdict {
	sh.note("model çıktısı çözümlenemedi — deterministik özete düşüldü")
	sum := "Kanıt yetersiz: yapay zekâ hakemi yanıt üretemedi."
	if h.TopSuspect != "" {
		sum = fmt.Sprintf(
			"Kanıt yetersiz: yapay zekâ hakemi yanıt üretemedi. Deterministik korelasyon motorunun baş şüphelisi %s (güven %.2f).",
			h.TopSuspect, h.Confidence)
	}
	return &RCAVerdict{
		Verdict:              "insufficient_evidence",
		Title:                "Hakem yanıtı alınamadı",
		Summary:              sum,
		Confidence:           0,
		HypothesisConfidence: h.Confidence,
		Evidence:             cat.Refs,
		Shields:              *sh,
	}
}
