package api

// v0.9.1197 (Faz 5.4) — postmortem kanıt paketinin saf kurucusu.
// Korunanlar: (a) klamp dürüstlüğü — kesilen olay/problem sayısı pakete
// yazılır, (b) açık incident "HENÜZ ÇÖZÜLMEDİ" der (model Özet'te
// belirtsin diye), (c) rune-güvenli kırpma Türkçe karakteri bölmez,
// (d) hipotez + deploy satırı yalnız varken çıkar.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func pmTestIncident(resolved bool) chstore.Incident {
	inc := chstore.Incident{
		ID: "inc-1", Title: "checkout hata oranı", Severity: "critical",
		Status: "resolved", Service: "checkout",
		StartedAt: 1_700_000_000_000_000_000,
	}
	if resolved {
		ra := inc.StartedAt + int64(45)*60*1_000_000_000
		inc.ResolvedAt = &ra
	} else {
		inc.Status = "open"
	}
	return inc
}

func TestRenderPostmortemEvidence(t *testing.T) {
	ra := int64(1_700_000_000_000_000_000 + 40*60*1_000_000_000)
	full := pmEvidenceInput{
		Inc: pmTestIncident(true),
		Events: []chstore.IncidentEvent{
			{Time: 1_700_000_000_000_000_000, Kind: "created", Actor: "system"},
			{Time: 1_700_000_600_000_000_000, Kind: "note", Actor: "ops@x", Body: "rollback başladı"},
		},
		TotalEvents: 55,
		Problems: []chstore.Problem{{
			ID: "p1", RuleName: "Error rate", Severity: "critical", Service: "checkout",
			Metric: "error_rate", Value: 22, Threshold: 15, Status: "resolved",
			StartedAt: 1_700_000_000_000_000_000, ResolvedAt: &ra,
			AISummary: "DB havuzu doldu",
		}},
		TotalProblems: 25,
		Hyps: map[string]chstore.RootCauseHypothesis{
			"p1": {TopSuspect: "payment-db", Confidence: 0.82,
				RecentDeploy: &chstore.RecentDeploy{Version: "v42", TimeUnixNs: 1_699_999_900_000_000_000}},
		},
	}
	out := renderPostmortemEvidence(full)
	for _, want := range []string{
		"Incident: checkout hata oranı",
		"Servis: checkout — Önem: critical — Durum: resolved",
		"(süre 45m0s)",
		"(55 olay, ilk 2 listede)",
		"[note] ops@x: rollback başladı",
		"(25 problem, ilk 1 listede)",
		"değer 22 (eşik 15)",
		"Kök-neden hipotezi: şüpheli payment-db (güven 0.82); deploy v42 @",
		"AI özeti: DB havuzu doldu",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("paket %q içermeli; paket:\n%s", want, out)
		}
	}

	open := pmEvidenceInput{
		Inc:   pmTestIncident(false),
		NowNs: 1_700_000_000_000_000_000 + 30*60*1_000_000_000,
	}
	oout := renderPostmortemEvidence(open)
	for _, want := range []string{
		"HENÜZ ÇÖZÜLMEDİ (şu ana dek 30m0s)",
		"(olay kaydı yok ya da OKUNAMADI)",
		"(ilişkili problem kaydı yok)",
	} {
		if !strings.Contains(oout, want) {
			t.Errorf("açık-incident paketi %q içermeli; paket:\n%s", want, oout)
		}
	}
	// Klamp yokken dürüstlük satırı da yok (0 olay ≠ "0 olay, ilk 0 listede").
	if strings.Contains(oout, "listede") {
		t.Errorf("kesinti yokken 'listede' dürüstlük eki yazılmamalı:\n%s", oout)
	}
}

func TestPmClampRuneSafe(t *testing.T) {
	in := strings.Repeat("ş", 700)
	got := pmClamp(in, 600)
	if !utf8.ValidString(got) || strings.ContainsRune(got, '�') {
		t.Fatalf("rune kesmesi bozuk çıktı üretti")
	}
	if n := len([]rune(got)); n != 601 { // 600 + '…'
		t.Errorf("klamp %d rune döndü, 601 beklenirdi", n)
	}
	if got := pmClamp("a\nb\n\nc", 100); got != "a b c" {
		t.Errorf("satır sonları tek boşluğa inmeli, %q döndü", got)
	}
}

func TestPostmortemDocTitleAndText(t *testing.T) {
	if got := postmortemDocTitle(strings.Repeat("ş", 80)); len([]rune(got)) != 4+61 || strings.ContainsRune(got, '�') {
		t.Errorf("PM başlığı 60 rune + … + 'PM: ' olmalı, %q döndü", got)
	}
	if got := postmortemDocTitle("  \n "); got != "PM: (başlıksız incident)" {
		t.Errorf("boş başlık fallback'i bozuk: %q", got)
	}
	inc := &chstore.Incident{Title: "checkout", Service: "checkout", Severity: "critical",
		Postmortem: "## Özet\nkısa"}
	txt := postmortemDocText(inc)
	for _, want := range []string{"Incident: checkout", "Servis: checkout", "Önem: critical", "## Özet\nkısa"} {
		if !strings.Contains(txt, want) {
			t.Errorf("doküman gövdesi %q içermeli:\n%s", want, txt)
		}
	}
}
