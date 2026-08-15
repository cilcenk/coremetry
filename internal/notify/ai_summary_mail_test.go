package notify

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.513 — P1 e-postası AI kök-sebep özetini taşır. Bekleme kapısı ve
// gövde render'ı ayrı ayrı pinli.

// Bekleme YALNIZ anlamlı olduğu halde açılmalı: explainer sadece
// kritik + açık + özeti boş problemleri dolduruyor. Başka bir durumda
// beklemek e-postayı boşuna 45 saniye geciktirir.
func TestShouldAwaitAISummary(t *testing.T) {
	cases := []struct {
		name string
		p    chstore.Problem
		want bool
	}{
		{"kritik + açık + boş → bekle",
			chstore.Problem{Status: "open", Severity: "critical"}, true},
		{"özet ZATEN var → bekleme",
			chstore.Problem{Status: "open", Severity: "critical", AISummary: "heap doldu"}, false},
		{"kritik değil → bekleme (explainer bakmaz)",
			chstore.Problem{Status: "open", Severity: "warning"}, false},
		{"kapalı → bekleme",
			chstore.Problem{Status: "resolved", Severity: "critical"}, false},
		{"acknowledged → bekleme",
			chstore.Problem{Status: "acknowledged", Severity: "critical"}, false},
		{"severity büyük harf de kabul",
			chstore.Problem{Status: "open", Severity: "CRITICAL"}, true},
		{"yalnız boşluk = boş sayılır",
			chstore.Problem{Status: "open", Severity: "critical", AISummary: "   "}, true},
	}
	for _, c := range cases {
		if got := shouldAwaitAISummary(c.p); got != c.want {
			t.Errorf("%s: shouldAwaitAISummary = %v, beklenen %v", c.name, got, c.want)
		}
	}
}

// Özet YOKSA mail biçimi birebir eskisi gibi kalmalı — bu değişiklik
// mevcut bildirimleri bozmamalı.
func TestEmailBodiesUnchangedWithoutSummary(t *testing.T) {
	n := &Notifier{}
	p := chstore.Problem{Service: "checkout", RuleName: "hata oranı", Severity: "critical"}

	plain := n.buildEmailBody(p, nil)
	if strings.Contains(plain, "AI") {
		t.Errorf("özet yokken düz gövdede AI bloğu olmamalı:\n%s", plain)
	}
	html := n.buildEmailHTML(p, nil)
	if strings.Contains(html, "AI KÖK-SEBEP") {
		t.Error("özet yokken HTML gövdede AI bloğu olmamalı")
	}
}

func TestEmailBodiesCarrySummary(t *testing.T) {
	n := &Notifier{}
	p := chstore.Problem{
		Service: "checkout", RuleName: "hata oranı", Severity: "critical",
		AISummary: "Heap %94'e çıktı, GC duraklamaları arttı.",
	}

	plain := n.buildEmailBody(p, nil)
	if !strings.Contains(plain, "AI kök-sebep yorumu") || !strings.Contains(plain, "Heap") {
		t.Errorf("düz gövde özeti taşımalı:\n%s", plain)
	}

	html := n.buildEmailHTML(p, nil)
	if !strings.Contains(html, "AI KÖK-SEBEP YORUMU") || !strings.Contains(html, "Heap") {
		t.Error("HTML gövde özeti taşımalı")
	}
	// Etiket BİLEREK duruyor: özet bir modelin yorumu, ölçülmüş gerçek
	// değil. Okuyan ekip farkı bilmeli — etiketi kaldırmak yanlış olur.
	if !strings.Contains(html, "AI") {
		t.Error("AI etiketi kaldırılmış — özet ölçüm gibi görünür")
	}
}

// Çok satırlı özet HTML'de tek satıra yapışmamalı; escape ÖNCE, <br>
// SONRA (tersi injection açar).
func TestHTMLSummaryEscapesThenBreaks(t *testing.T) {
	n := &Notifier{}
	p := chstore.Problem{
		Severity:  "critical",
		AISummary: "birinci satır\nikinci <script>alert(1)</script>",
	}
	html := n.buildEmailHTML(p, nil)
	if strings.Contains(html, "<script>") {
		t.Error("özet escape edilmemiş — injection açık")
	}
	if !strings.Contains(html, "birinci satır<br>ikinci") {
		t.Error("newline <br> olmalı")
	}
}
