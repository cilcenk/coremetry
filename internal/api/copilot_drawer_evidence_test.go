package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// v0.9.482 pinleri — çekmece sohbetinin HAM KANIT devri.
//
// Operatör raporu (prod fotoğrafı, v0.9.479 takibi): "Explain trace"
// trace'i VE ilişkili logları birlikte okuyup cevaplıyor; ama çekmece
// sohbeti yalnız o cevabın METNİNİ görüyordu. "logda ne yazıyor",
// "BKMQR-39 neden" gibi takipler ham kanıta hiç ulaşamadığı için kör
// cevaplanıyordu. Artık sohbet ÖZNE-FARKINDA: `context.subject`ten
// ilgili explain'in AYNI kanıt paketi yeniden kurulur.
//
// En kritik iki pin: (1) kanıt YOKKEN narration bloğu v0.9.479'daki
// metindir — kanıt fetch'i patladığında düşülen yol (soft-fail);
// (2) budama LOGLARI KORUR — raporun konusu log içeriğiydi.

func TestParseDrawerSubject(t *testing.T) {
	cases := []struct {
		name, raw  string
		wantOK     bool
		wantKind   string
		wantID     string
		wantSpanID string
	}{
		{name: "boş", raw: "", wantOK: false},
		{name: "yalnız boşluk", raw: "   ", wantOK: false},
		{name: "trace", raw: "trace:abc123", wantOK: true, wantKind: "trace", wantID: "abc123"},
		{
			name: "span", raw: "span:abc123:s9",
			wantOK: true, wantKind: "span", wantID: "abc123", wantSpanID: "s9",
		},
		{
			name: "exception fingerprint", raw: "exception:fp-77",
			wantOK: true, wantKind: "exception", wantID: "fp-77",
		},
		{
			// ':' içeren id encode edilerek taşınır (aiSubject.ts sözleşmesi);
			// decode edilmezse yanlış özneden kanıt çekerdik.
			name: "encode edilmiş id", raw: "exception:java.lang%3ANPE",
			wantOK: true, wantKind: "exception", wantID: "java.lang:NPE",
		},
		{
			// Kanıt kurulamayan özneler: v0.9.479 davranışı (yalnız metin).
			name: "problem → kanıt yok", raw: "problem:p-1", wantOK: false,
		},
		{name: "service-health → kanıt yok", raw: "service-health:checkout:1:2", wantOK: false},
		{name: "bilinmeyen kind", raw: "wat:1", wantOK: false},
		{name: "id yok", raw: "trace:", wantOK: false},
		{name: "kindsiz", raw: "abc123", wantOK: false},
		// Fazladan segment = elle düzenlenmiş/bozuk link. Sessizce YANLIŞ
		// özneden kanıt çekmektense kanıtsız devam et (aiSubject.ts ile aynı katılık).
		{name: "trace fazladan segment", raw: "trace:abc:extra", wantOK: false},
		{name: "span eksik segment", raw: "span:abc", wantOK: false},
		{name: "span boş spanId", raw: "span:abc:", wantOK: false},
		// Bozuk yüzde dizisi: çekmece bir URL parçası yüzünden patlamamalı.
		{name: "bozuk yüzde dizisi", raw: "trace:%zz", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseDrawerSubject(c.raw)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want=%v (got=%+v)", ok, c.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Kind != c.wantKind || got.ID != c.wantID || got.SpanID != c.wantSpanID {
				t.Fatalf("got=%+v want kind=%q id=%q span=%q", got, c.wantKind, c.wantID, c.wantSpanID)
			}
		})
	}
}

func TestClampDrawerEvidence(t *testing.T) {
	logs := "\n\nLOGLAR:\n" + strings.Repeat("l", 100)
	t.Run("boş", func(t *testing.T) {
		if got := clampDrawerEvidence("", ""); got != "" {
			t.Fatalf("boş beklendi: %q", got)
		}
		if got := clampDrawerEvidence("  \n ", ""); got != "" {
			t.Fatalf("yalnız boşluk boş dönmeli: %q", got)
		}
	})
	t.Run("bütçe içindeyse AYNEN geçer", func(t *testing.T) {
		full := "SPANS" + logs
		if got := clampDrawerEvidence(full, logs); got != full {
			t.Fatalf("paket değiştirildi:\n got=%q\nwant=%q", got, full)
		}
	})
	t.Run("sınırda kesme yok", func(t *testing.T) {
		full := strings.Repeat("a", drawerEvidenceMaxRunes)
		if got := clampDrawerEvidence(full, ""); got != full {
			t.Fatalf("tam sınırda kesildi (rune=%d)", len([]rune(got)))
		}
	})
	t.Run("span budanır, LOGLAR korunur", func(t *testing.T) {
		// Operatör raporunun tam senaryosu: takip sorusu log içeriğine dair.
		full := strings.Repeat("s", drawerEvidenceMaxRunes*2) + logs
		got := clampDrawerEvidence(full, logs)
		if !strings.HasSuffix(got, logs) {
			t.Fatalf("log bloğu düştü:\n%s", tail(got))
		}
		if !strings.Contains(got, drawerEvidenceTruncNote) {
			t.Fatal("kısaltma modele söylenmedi")
		}
		if n := len([]rune(got)); n > drawerEvidenceMaxRunes+len([]rune(drawerEvidenceTruncNote)) {
			t.Fatalf("bütçe aşıldı: %d rune", n)
		}
	})
	t.Run("loglar tek başına bütçeyi doldurdu → span listesi çıkar", func(t *testing.T) {
		bigLogs := "\n\nLOGLAR:\n" + strings.Repeat("l", drawerEvidenceMaxRunes*2)
		full := strings.Repeat("s", 4000) + bigLogs
		got := clampDrawerEvidence(full, bigLogs)
		if !strings.HasPrefix(got, drawerEvidenceLogsOnlyNote) {
			t.Fatalf("yalnız-log kısaltması modele söylenmedi: %q", head(got))
		}
		if strings.Contains(got, strings.Repeat("s", 100)) {
			t.Fatal("span listesi bırakılmadı")
		}
		// Logların YÜKSEK SEVERITY başı taşınır (LogsForTrace sıralaması).
		if !strings.Contains(got, "LOGLAR:") {
			t.Fatal("log bloğunun başı taşınmadı")
		}
	})
	t.Run("log yok → kuyruktan kesilir", func(t *testing.T) {
		full := strings.Repeat("s", drawerEvidenceMaxRunes+500)
		got := clampDrawerEvidence(full, "")
		if !strings.HasSuffix(got, drawerEvidenceCutNote) {
			t.Fatalf("kısaltma eki yok: %q", tail(got))
		}
	})
	t.Run("log bloğu pakette yoksa tek parça kesilir", func(t *testing.T) {
		// Savunma: kurucu paketi başka türlü monte ederse Replace yanlış
		// yerden kesmesin — bütünlük bozulacağına kuyruk kesilsin.
		full := strings.Repeat("s", drawerEvidenceMaxRunes+500)
		got := clampDrawerEvidence(full, "\n\nBAŞKA BİR LOG BLOĞU")
		if !strings.HasSuffix(got, drawerEvidenceCutNote) {
			t.Fatalf("kısaltma eki yok: %q", tail(got))
		}
	})
	t.Run("çok-baytlı kesme geçerli UTF-8", func(t *testing.T) {
		got := clampDrawerEvidence(strings.Repeat("ğ", drawerEvidenceMaxRunes+50), "")
		if !utf8Valid(got) {
			t.Fatal("kırpma geçersiz UTF-8 üretti")
		}
	})
}

// TestDrawerNarrationUserEvidenceFallback — kanıt fetch'i başarısız
// olduğunda (CH/ES hatası, özne çözülemedi, kanıt kurulamayan kind)
// üretilen blok v0.9.479'dakiyle BAYT-BAYT aynıdır. Sohbet kanıt yüzünden
// asla kırılmaz — bu testin düşmesi soft-fail sözleşmesinin bozulmasıdır.
func TestDrawerNarrationUserEvidenceFallback(t *testing.T) {
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: "Bu trace'i açıkla (t1)"},
		{Role: "assistant", Text: "checkout 2.1s sürdü"},
		{Role: "user", Text: "logda ne yazıyor?"},
	}
	legacy := func(question, explain string, ms []copilot.ChatMessage) string {
		var b strings.Builder
		b.WriteString("EKRANDAKİ AÇIKLAMA (operatörün az önce okuduğu CoSRE cevabı):\n")
		b.WriteString(clampDrawerExplain(explain))
		if h := drawerHistoryBlock(ms); h != "" {
			b.WriteString("\n\nKONUŞMA (K: operatör, C: sen):\n")
			b.WriteString(h)
		}
		b.WriteString("\n\nSORU: ")
		b.WriteString(question)
		return b.String()
	}
	for _, evidence := range []string{"", "   ", "\n\t "} {
		got := drawerNarrationUser("logda ne yazıyor?", "checkout 2.1s sürdü", msgs, evidence)
		if want := legacy("logda ne yazıyor?", "checkout 2.1s sürdü", msgs); got != want {
			t.Fatalf("kanıtsız blok değişti (evidence=%q)\n got=%q\nwant=%q", evidence, got, want)
		}
		if strings.Contains(got, "HAM KANIT") {
			t.Fatalf("boş kanıtta başlık yazıldı: %q", got)
		}
	}
}

// TestDrawerNarrationUserWithEvidence — trace öznesi için ham kanıt
// bloğu anlatıma girer ve SORU en sonda kalır (küçük model son talimatı
// en iyi izler).
func TestDrawerNarrationUserWithEvidence(t *testing.T) {
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: "Bu trace'i açıkla (t1)"},
		{Role: "assistant", Text: "checkout 2.1s sürdü"},
		{Role: "user", Text: "logda ne yazıyor?"},
	}
	evidence := "Trace t1 with 3 spans:\n```json\n[{\"id\":\"s1\"}]\n```\n\nLOGLAR:\nBKMQR-39 timeout"
	got := drawerNarrationUser("logda ne yazıyor?", "checkout 2.1s sürdü", msgs, evidence)
	for _, want := range []string{
		"EKRANDAKİ AÇIKLAMA", "checkout 2.1s sürdü",
		"HAM KANIT", "BKMQR-39 timeout",
		"KONUŞMA (K: operatör, C: sen):",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blokta %q yok:\n%s", want, got)
		}
	}
	// Sıra: açıklama → kanıt → konuşma → SORU.
	iEx := strings.Index(got, "EKRANDAKİ AÇIKLAMA")
	iEv := strings.Index(got, "HAM KANIT")
	iHist := strings.Index(got, "KONUŞMA (")
	if !(iEx < iEv && iEv < iHist) {
		t.Fatalf("blok sırası bozuk: ex=%d ev=%d hist=%d", iEx, iEv, iHist)
	}
	if !strings.HasSuffix(got, "SORU: logda ne yazıyor?") {
		t.Fatalf("soru sonda değil:\n%s", got)
	}
}

func TestDrawerSourceNoteAndStep(t *testing.T) {
	cases := []struct {
		kind          string
		wantNoteExtra bool
		wantStep      bool
	}{
		{kind: "", wantNoteExtra: false, wantStep: false},
		{kind: "trace", wantNoteExtra: true, wantStep: true},
		{kind: "span", wantNoteExtra: true, wantStep: true},
		{kind: "exception", wantNoteExtra: true, wantStep: true},
		{kind: "problem", wantNoteExtra: false, wantStep: false},
	}
	for _, c := range cases {
		t.Run("kind="+c.kind, func(t *testing.T) {
			note := drawerSourceNote(c.kind)
			if !strings.HasPrefix(note, "\n\nKaynak: ekrandaki AI açıklaması") {
				t.Fatalf("kaynak dipnotu sözleşmesi bozuldu: %q", note)
			}
			// Kanıt YOKKEN dipnot olmayan bir kanıtı iddia etmemeli.
			if hasExtra := note != "\n\nKaynak: ekrandaki AI açıklaması"; hasExtra != c.wantNoteExtra {
				t.Fatalf("dipnot kanıt iddiası=%v want=%v (%q)", hasExtra, c.wantNoteExtra, note)
			}
			if hasStep := drawerEvidenceStep(c.kind) != ""; hasStep != c.wantStep {
				t.Fatalf("şeffaflık çipi=%v want=%v", hasStep, c.wantStep)
			}
		})
	}
}

func head(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func tail(s string) string {
	if len(s) > 80 {
		return s[len(s)-80:]
	}
	return s
}
