package notify

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1058 (Faz 1.3 / K9) regresyon pini — alarm maili deterministik
// hipotezi taşır. Sözleşme: rc nil → mail biçimi BAYT-BAYT eski (AI
// kutusuyla karışmaz); rc dolu → düz metinde ve HTML'de "OLASI KÖK
// NEDEN (DETERMİNİSTİK)" bölümü, şüpheli + güven + gerekçe + (varsa)
// kanıt trace linki. HTML'de suspect ESCAPE'lenir — servis adı
// operatör-şekilli serbest metindir.
func TestMailCarriesHypothesis(t *testing.T) {
	n := New(nil)
	p := chstore.Problem{ID: "p1", Service: "checkout", RuleName: "r", Severity: "critical"}
	rc := &chstore.RootCauseHypothesis{
		TopSuspect: "payment-db", Confidence: 0.62,
		Candidates: []chstore.ScoredCause{{
			Service: "payment-db", Score: 0.62,
			Reason: "deployed v2.3.1 4m before onset — prime 'what changed' suspect",
		}},
		ExemplarTraceID: "abc123",
	}

	plain := n.buildEmailBody(p, rc)
	if !strings.Contains(plain, "Olası kök neden (deterministik): payment-db") ||
		!strings.Contains(plain, "güven %62") {
		t.Fatalf("düz metin hipotez satırı eksik:\n%s", plain)
	}
	if !strings.Contains(plain, "prime 'what changed' suspect") {
		t.Fatalf("gerekçe satırı eksik:\n%s", plain)
	}

	html := n.buildEmailHTML(p, rc)
	if !strings.Contains(html, "OLASI KÖK NEDEN (DETERM") ||
		!strings.Contains(html, "<b>payment-db</b>") {
		t.Fatalf("HTML hipotez kutusu eksik:\n%s", html)
	}

	// nil rc → bölüm hiç yok (biçim eski).
	if strings.Contains(n.buildEmailBody(p, nil), "Olası kök neden") ||
		strings.Contains(n.buildEmailHTML(p, nil), "OLASI KÖK NEDEN") {
		t.Fatal("rc nil iken hipotez bölümü basıldı")
	}
}

// PUBLIC_URL yokken kanıt-trace satırı hiç basılmaz (problemURL ile aynı
// sözleşme); traceID'li URL /trace?id= şeklindedir (Trace.tsx ?id= okur —
// /trace/<id> rotası YOK, yanlış şekil 404'e götürürdü).
func TestTraceURLShape(t *testing.T) {
	n := New(nil)
	if u := n.traceURL("abc"); u != "" {
		t.Fatalf("PUBLIC_URL yokken traceURL boş olmalı, got %q", u)
	}
}
