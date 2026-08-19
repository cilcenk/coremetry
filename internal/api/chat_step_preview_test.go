package api

// v0.9.1181 (AI Faz 4.3) — clipStepPreview tablo testi.
//
// Korunan iki şey var ve ikincisi gözle görülmez:
//   1. kırpma İLAN EDİLİR (sessiz kırpma, operatörün eksik kanıta tam kanıt
//      sanıp bakması demektir — bu dilimin varlık sebebinin tam tersi),
//   2. kırpma UTF-8 SINIRINDA yapılır. Ham bayt kesimi çok baytlı bir runeyi
//      ikiye böler, JSON kodlayıcı onu U+FFFD'ye çevirir ve operatör kanıtta
//      bozuk karakter görür. Türkçe servis/operasyon adları bu yoldan sürekli
//      geçtiği için bu teorik bir incelik değil.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipStepPreview(t *testing.T) {
	t.Run("tavanın altı aynen geçer", func(t *testing.T) {
		in := `{"ok":true,"rows":3}`
		got, trunc := clipStepPreview(in)
		if got != in || trunc {
			t.Errorf("clipStepPreview(%q) = (%q, %v), beklenen (aynen, false)", in, got, trunc)
		}
	})

	t.Run("tam tavan kırpılmaz", func(t *testing.T) {
		in := strings.Repeat("a", chatStepPreviewMax)
		got, trunc := clipStepPreview(in)
		if trunc || len(got) != chatStepPreviewMax {
			t.Errorf("tam sınır kırpıldı: len=%d trunc=%v", len(got), trunc)
		}
	})

	t.Run("tavanın bir baytı üstü kırpılır ve İLAN EDİLİR", func(t *testing.T) {
		in := strings.Repeat("a", chatStepPreviewMax+1)
		got, trunc := clipStepPreview(in)
		if !trunc {
			t.Error("kırpıldı ama truncated=false — sessiz kırpma")
		}
		if len(got) > chatStepPreviewMax {
			t.Errorf("kırpma tavanı aşıyor: %d", len(got))
		}
	})

	t.Run("çok baytlı rune ORTADAN bölünmez", func(t *testing.T) {
		// Kesim noktasını tam bir Türkçe karakterin ortasına denk getir:
		// tavandan bir eksik ASCII + çok baytlı runeler.
		in := strings.Repeat("a", chatStepPreviewMax-1) + strings.Repeat("ş", 40)
		got, trunc := clipStepPreview(in)
		if !trunc {
			t.Fatal("kırpılmalıydı")
		}
		if !utf8.ValidString(got) {
			t.Errorf("kırpma geçersiz UTF-8 üretti — JSON kodlayıcı U+FFFD basar, "+
				"operatör kanıtta bozuk karakter görür (uzunluk %d)", len(got))
		}
	})

	t.Run("her kesim noktasında geçerli UTF-8", func(t *testing.T) {
		// Kesimin rune sınırına düşmediği HER hizalamayı dolaş: çok baytlı
		// karakteri tek tek kaydırarak tavanın etrafındaki tüm dallar denenir
		// (tek bir örnekle yazılmış bir test bu sınıfı kaçırır).
		for pad := 0; pad < 8; pad++ {
			in := strings.Repeat("a", chatStepPreviewMax-pad) + strings.Repeat("ğ", 20)
			got, _ := clipStepPreview(in)
			if !utf8.ValidString(got) {
				t.Errorf("pad=%d geçersiz UTF-8 üretti", pad)
			}
		}
	})

	t.Run("boş girdi", func(t *testing.T) {
		got, trunc := clipStepPreview("")
		if got != "" || trunc {
			t.Errorf("boş girdi = (%q, %v)", got, trunc)
		}
	})
}
