package notify

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// email_html_test.go — v0.8.493 (operatör isteği: HTML e-posta +
// problem linki). buildEmailHTML saf render + composeAltEmail
// multipart montajı; her dal test edilir — link var/yok, kaçış,
// severity renkleri, plain fallback'in mesajda kalması.

func testProblem() chstore.Problem {
	return chstore.Problem{
		ID: "p1", Service: "checkout", RuleName: "err rate",
		Severity: "critical", Metric: "error_rate",
		Value: 7.51, Threshold: 5, StartedAt: 1_700_000_000_000_000_000,
		Description: "Error rate above threshold",
	}
}

func TestBuildEmailHTML(t *testing.T) {
	t.Run("link when PublicURL set", func(t *testing.T) {
		n := New(nil)
		n.SetPublicURL("https://cm.local")
		out := n.buildEmailHTML(testProblem())
		if !strings.Contains(out, `href="https://cm.local/problems?problem=p1"`) {
			t.Fatalf("problem deep link missing:\n%s", out)
		}
		if !strings.Contains(out, "Open in Coremetry") {
			t.Fatal("CTA label missing")
		}
	})

	t.Run("no dangling link when PublicURL unset", func(t *testing.T) {
		out := New(nil).buildEmailHTML(testProblem())
		if strings.Contains(out, "Open in Coremetry") {
			t.Fatal("CTA rendered without a configured PublicURL")
		}
	})

	t.Run("dynamic fields are HTML-escaped", func(t *testing.T) {
		p := testProblem()
		p.Service = `<script>alert(1)</script>`
		p.Description = `a & b < c`
		out := New(nil).buildEmailHTML(p)
		if strings.Contains(out, "<script>") {
			t.Fatal("service name not escaped")
		}
		for _, want := range []string{"&lt;script&gt;", "a &amp; b &lt; c"} {
			if !strings.Contains(out, want) {
				t.Fatalf("escaped form %q missing", want)
			}
		}
	})

	t.Run("severity colors — every branch", func(t *testing.T) {
		for sev, color := range map[string]string{
			"critical": "#ff5252", "warning": "#f59f00", "info": "#3b82f6",
		} {
			p := testProblem()
			p.Severity = sev
			if out := New(nil).buildEmailHTML(p); !strings.Contains(out, color) {
				t.Fatalf("severity %q: color %s missing", sev, color)
			}
		}
	})

	t.Run("runbook link only when set", func(t *testing.T) {
		p := testProblem()
		out := New(nil).buildEmailHTML(p)
		if strings.Contains(out, "Runbook") {
			t.Fatal("runbook row rendered without a URL")
		}
		p.RunbookURL = "https://wiki/runbook"
		out = New(nil).buildEmailHTML(p)
		if !strings.Contains(out, `href="https://wiki/runbook"`) {
			t.Fatal("runbook link missing")
		}
	})
}

// TestComposeAltEmail round-trips the assembled message through
// net/mail + mime/multipart — substring checks alone missed a
// boundary-mismatch mutation and the 998-octet violation in review
// (v0.8.493 adversarial pass).
func TestComposeAltEmail(t *testing.T) {
	plain, htmlBody := "plain body — Türkçe içerik", "<html>rich — içerik</html>"
	raw, err := composeAltEmail("Coremetry <cm@x>", []string{"a@x", "b@x"},
		"[CRITICAL] checkout — err rate", plain, htmlBody)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}

	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	// Subject RFC 2047-kodlu gitmeli (em-dash her subject'te var) ve
	// decode edilince orijinaline dönmeli.
	dec := new(mime.WordDecoder)
	subj, err := dec.DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subj != "[CRITICAL] checkout — err rate" {
		t.Fatalf("subject round-trip failed: %q, %v", subj, err)
	}
	if strings.Contains(m.Header.Get("Subject"), "—") {
		t.Fatal("subject header carries raw non-ASCII (RFC 2047 encoding missing)")
	}

	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content-type: %q, %v", mt, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var got []string
	var types []string
	for {
		// NextRawPart: NextPart qp'yi şeffaf çözüp CTE header'ını
		// SİLİYOR — ham parça alıp elle decode ediyoruz ki başlık
		// gerçekten telde olduğu gibi doğrulansın.
		part, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("part read: %v", err)
		}
		if cte := part.Header.Get("Content-Transfer-Encoding"); cte != "quoted-printable" {
			t.Fatalf("part CTE = %q, want quoted-printable", cte)
		}
		body, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatalf("qp decode: %v", err)
		}
		got = append(got, string(body))
		types = append(types, part.Header.Get("Content-Type"))
	}
	if len(got) != 2 {
		t.Fatalf("want exactly 2 parts, got %d", len(got))
	}
	// text/plain düşük fidelite önce, text/html sonra — istemci
	// desteklediği SON parçayı gösterir (RFC 2046 §5.1.4).
	if !strings.Contains(types[0], "text/plain") || !strings.Contains(types[1], "text/html") {
		t.Fatalf("part order/types wrong: %v", types)
	}
	if got[0] != plain || got[1] != htmlBody {
		t.Fatalf("qp round-trip mismatch:\nplain=%q\nhtml=%q", got[0], got[1])
	}

	// RFC 5321 §4.5.3.1.6 — hiçbir fiziksel satır 998 okteti aşamaz
	// (qp yazıcısı 76 kolonda katlar; bu, review'daki 2128-oktet tek
	// satır bulgusunun kalıcı bekçisi).
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) > 998 {
			t.Fatalf("physical line exceeds 998 octets (%d)", len(line))
		}
	}
}

// CRLF header injection (CWE-93): OTLP'den gelen service.name subject'e
// giriyor — "\r\n" içeren değer header bloğunu bölememeli.
func TestComposeAltEmailHeaderInjection(t *testing.T) {
	raw, err := composeAltEmail(
		"evil\r\nX-Injected-From: x <cm@x>",
		[]string{"a@x\r\nX-Injected-To: y"},
		"[CRITICAL] svc\r\nX-Injected-Subj: z — r", "p", "<p>h</p>")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	for _, h := range []string{"X-Injected-From", "X-Injected-To", "X-Injected-Subj"} {
		if m.Header.Get(h) != "" {
			t.Fatalf("header injection succeeded via %s", h)
		}
	}
}
