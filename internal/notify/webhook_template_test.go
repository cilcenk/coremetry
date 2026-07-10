package notify

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// webhook_template_test.go — v0.8.445. Şablon render'ı + doğrulama:
// alan erişimi, missingkey=error yazım-hatası yakalama, boş şablon
// no-op'u — her dal.
func TestRenderWebhookBody(t *testing.T) {
	p := chstore.Problem{
		ID: "p1", Service: "checkout", Severity: "critical",
		RuleName: "err rate", Value: 7.51, Status: "open",
	}
	out, err := renderWebhookBody(
		`{"svc":"{{.Problem.Service}}","sev":"{{.Problem.Severity}}","url":"{{.CoremetryURL}}","v":{{printf "%.1f" .Problem.Value}}}`,
		p, "https://x/problems?problem=p1")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(out)
	for _, frag := range []string{`"svc":"checkout"`, `"sev":"critical"`, `problem=p1`, `"v":7.5`} {
		if !strings.Contains(got, frag) {
			t.Fatalf("missing %q in %s", frag, got)
		}
	}
}

func TestValidateWebhookTemplate(t *testing.T) {
	if err := ValidateWebhookTemplate(""); err != nil {
		t.Fatal("boş şablon no-op olmalı")
	}
	if err := ValidateWebhookTemplate(`{{.Problem.Service}}`); err != nil {
		t.Fatalf("geçerli şablon reddedildi: %v", err)
	}
	if err := ValidateWebhookTemplate(`{{.Problem.Servcie}}`); err == nil {
		t.Fatal("yazım hatası (missingkey) yakalanmalı")
	}
	if err := ValidateWebhookTemplate(`{{.Problem.Service`); err == nil {
		t.Fatal("parse hatası yakalanmalı")
	}
}
