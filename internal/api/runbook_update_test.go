package api

// v0.9.1198 (Faz 5.5) — runbook güncelleme önerisinin saf kanıt paketi.
// Korunanlar: (a) kanıt zinciri yalnız hipotez VARKEN eklenir (nil →
// boş dize, copilotRunbook'a boş blok sızmaz), (b) Found=false derin
// sinyaller pakete GİRMEZ (yokluk kanıt değildir), (c) klamp
// dürüstlüğü, (d) TTL'lenmiş problem paketi düşürmez, dürüstçe yazılır.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestRenderRunbookEvidenceChain(t *testing.T) {
	if got := renderRunbookEvidenceChain(nil); got != "" {
		t.Errorf("nil hipotez boş dize döndürmeli, %q döndü", got)
	}
	if got := renderRunbookEvidenceChain(&chstore.RootCauseHypothesis{}); got != "" {
		t.Errorf("şüphelisiz hipotez boş dize döndürmeli, %q döndü", got)
	}
	h := &chstore.RootCauseHypothesis{
		TopSuspect: "payment-db", Confidence: 0.82,
		Candidates: []chstore.ScoredCause{
			{Service: "payment-db", Score: 0.82, Path: []string{"checkout", "payment-db"}, Reason: "latency spike"},
			{Service: "cache", Score: 0.3}, {Service: "c3", Score: 0.2}, {Service: "c4", Score: 0.1},
		},
		Deep: &chstore.DeepEvidence{Checked: []chstore.CheckedSignal{
			{Family: "exceptions", Found: true, Detail: "SQLTimeout x120"},
			{Family: "saturation", Found: false, Detail: "temiz"},
		}},
	}
	out := renderRunbookEvidenceChain(h)
	for _, want := range []string{
		"Top suspect: payment-db (confidence 0.82)",
		"path checkout -> payment-db",
		"(+1 more candidates)",
		"Signal found [exceptions]: SQLTimeout x120",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("zincir %q içermeli:\n%s", want, out)
		}
	}
	if strings.Contains(out, "saturation") {
		t.Errorf("Found=false sinyal pakete girmemeli:\n%s", out)
	}
}

func TestRenderRunbookUpdateEvidence(t *testing.T) {
	ra := int64(1_700_000_000_000_000_000 + 20*60*1_000_000_000)
	in := ruEvidenceInput{
		Runbook: chstore.Runbook{
			Title: "Checkout hata koşusu", Description: "DB havuzunu kontrol et",
			Steps: []chstore.RunbookStep{
				{Order: 1, Kind: "manual", Title: "Dashboard'a bak", Instructions: "checkout p99"},
				{Order: 2, Kind: "query", Title: "Hata logları"},
			},
		},
		Exec: chstore.RunbookExecution{
			Status: "completed", StartedAt: 1_700_000_000_000_000_000,
			CompletedAt: 1_700_000_000_000_000_000 + 9*60*1_000_000_000,
			StepStates: []chstore.StepState{
				{Order: 1, Title: "Dashboard'a bak", Status: "done"},
				{Order: 2, Title: "Hata logları", Status: "skipped", Note: "gerek kalmadı, deploy rollback yeterliydi"},
			},
		},
		Problem: &chstore.Problem{
			Severity: "critical", RuleName: "Error rate", Service: "checkout",
			Metric: "error_rate", Value: 22, Threshold: 15, Status: "resolved",
			StartedAt: 1_700_000_000_000_000_000, ResolvedAt: &ra,
		},
		Hyp: &chstore.RootCauseHypothesis{TopSuspect: "checkout", Confidence: 0.7},
	}
	out := renderRunbookUpdateEvidence(in)
	for _, want := range []string{
		"Runbook: Checkout hata koşusu",
		"Açıklama: DB havuzunu kontrol et",
		"Mevcut adımlar (2):",
		"1. [manual] Dashboard'a bak — checkout p99",
		"durum completed, süre 9m0s",
		"2. Hata logları — skipped; operatör notu: gerek kalmadı, deploy rollback yeterliydi",
		"değer 22 (eşik 15)",
		"Çözülme süresi: 20m0s",
		"Top suspect: checkout",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("paket %q içermeli:\n%s", want, out)
		}
	}

	// TTL'lenmiş problem: paket düşmez, yokluk açıkça yazılır.
	in.Problem, in.Hyp = nil, nil
	out = renderRunbookUpdateEvidence(in)
	if !strings.Contains(out, "(problem kaydı bulunamadı — saklama süresi dolmuş olabilir)") {
		t.Errorf("kayıp problem dürüstçe işaretlenmeli:\n%s", out)
	}
	if strings.Contains(out, "Top suspect") {
		t.Errorf("hipotezsiz pakette zincir olmamalı:\n%s", out)
	}
}
