package devops

import (
	"fmt"
	"strings"
	"testing"
)

// code_window_quality_test.go — v0.9.1239.
//
// Dört denetim bulgusunun kapısı; hepsi PROMPT'un içindeki tek soruya
// bakıyor: model bir pencerede NE görüyor?
//
//  1. Bütçe kırpması, penceredeki hata satırını DÜŞÜREMEZ. Öncesinde
//     ClampCodeWindows baştan kesiyordu; pencere line-30'dan başladığı
//     için kalan bütçe yarımın altına düştüğünde korunan satırlar hata
//     satırına varmadan bitiyor, başlık ise hâlâ "(Y.java:246)" diye o
//     satırı gösteriyordu — model görmediği satırdan kök neden uyduruyor.
//  2. Hata satırı pencere İÇİNDE işaretli, pencere de zincirdeki yeriyle
//     etiketli olmalı.
//  3. Çit dosya UZANTISINDAN gelmeli (hepsi ```java değil).

// mkWindow — from..to arası numaralı içerik + frame satırı.
func mkWindow(from, to, line int) CodeWindow {
	var sb strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&sb, "%d| kod satiri %d\n", i, i)
	}
	return CodeWindow{
		Path: "/src/main/java/com/x/Y.java", Frame: fmt.Sprintf("com.x.Y.m(Y.java:%d)", line),
		Line: line, FromLine: from, ToLine: to,
		Content: strings.TrimRight(sb.String(), "\n"),
	}
}

// frameLineIn — içerikte `line` numaralı satır var mı?
func frameLineIn(content string, line int) bool {
	for _, ln := range strings.Split(content, "\n") {
		if n, ok := lineNumberOf(strings.TrimPrefix(ln, frameMarker+" ")); ok && n == line {
			return true
		}
	}
	return false
}

func TestClampKeepsFrameLine(t *testing.T) {
	// Pencere 216-276, hata satırı 246 — ortada, yani baştan kesme
	// tam da burada hata satırını düşürüyordu.
	w := mkWindow(216, 276, 246)
	full := len([]rune(w.Content))

	tests := []struct {
		name    string
		budget  int
		wantLen int  // kalan pencere sayısı
		wantHas bool // hata satırı korunmalı mı?
	}{
		{name: "bütçe tam yetiyor", budget: full, wantLen: 1, wantHas: true},
		{name: "yarısı", budget: full / 2, wantLen: 1, wantHas: true},
		{name: "dörtte biri", budget: full / 4, wantLen: 1, wantHas: true},
		{name: "onda biri", budget: full / 10, wantLen: 1, wantHas: true},
		{name: "tek satırlık bütçe", budget: 30, wantLen: 1, wantHas: true},
		{name: "hata satırı bile sığmıyor → pencere düşer", budget: 5, wantLen: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := ClampCodeWindows([]CodeWindow{w}, tt.budget)
			if len(out) != tt.wantLen {
				t.Fatalf("pencere sayısı=%d, istenen %d", len(out), tt.wantLen)
			}
			if tt.wantLen == 0 {
				return
			}
			got := out[0]
			if tt.wantHas && !frameLineIn(got.Content, 246) {
				t.Fatalf("hata satırı (246) kırpmada düştü — başlık görünmeyen satırı gösterir:\n%s", got.Content)
			}
			// Aralık GERÇEĞİ söylemeli: başlıktaki satır aralığı
			// korunan satırlarla aynı olmalı.
			first := strings.SplitN(got.Content, "\n", 2)[0]
			lines := strings.Split(got.Content, "\n")
			wantFrom, _ := lineNumberOf(first)
			wantTo, _ := lineNumberOf(lines[len(lines)-1])
			if got.FromLine != wantFrom || got.ToLine != wantTo {
				t.Fatalf("aralık=%d-%d, korunan satırlar %d-%d — başlık yalan söylüyor",
					got.FromLine, got.ToLine, wantFrom, wantTo)
			}
			// Bütçe aşılmamalı.
			if n := len([]rune(got.Content)); n > tt.budget {
				t.Fatalf("bütçe aşıldı: %d > %d", n, tt.budget)
			}
			// Yarım satır olmamalı.
			for _, ln := range strings.Split(got.Content, "\n") {
				if _, ok := lineNumberOf(ln); !ok {
					t.Fatalf("satır sınırı dışında kesildi: %q", ln)
				}
			}
		})
	}
}

func TestClampKeepsFrameLineInSecondWindow(t *testing.T) {
	// Gerçek hâl: ilk pencere bütçenin çoğunu yer, İKİNCİ pencere
	// artıkla kırpılır. v0.9.1239 öncesi tam burada merkez düşüyordu.
	first := mkWindow(10, 40, 25)
	second := mkWindow(216, 276, 246)
	budget := len([]rune(first.Content)) + 200

	out, trimmed := ClampCodeWindows([]CodeWindow{first, second}, budget)
	if !trimmed {
		t.Fatal("trimmed=false — kırpma bildirilmedi")
	}
	if len(out) != 2 {
		t.Fatalf("pencere sayısı=%d, istenen 2", len(out))
	}
	if !frameLineIn(out[1].Content, 246) {
		t.Fatalf("ikinci pencerenin hata satırı düştü:\n%s", out[1].Content)
	}
	if !frameLineIn(out[0].Content, 25) {
		t.Fatal("ilk pencere bozuldu")
	}
}

func TestClampLineZeroKeepsLegacyCut(t *testing.T) {
	// Line=0 (frame satırı bilinmiyor, elle kurulan pencere) → eski
	// baştan-kesme davranışı korunur; yeni yol onu bozmamalı.
	w := mkWindow(1, 60, 0)
	w.Line = 0
	out, trimmed := ClampCodeWindows([]CodeWindow{w}, 100)
	if !trimmed || len(out) != 1 {
		t.Fatalf("trimmed=%v len=%d", trimmed, len(out))
	}
	if !strings.HasPrefix(out[0].Content, "1| ") {
		t.Fatalf("Line=0 için baştan kesme beklenirdi: %q", out[0].Content)
	}
}

func TestCenterToBudget(t *testing.T) {
	w := mkWindow(100, 120, 110)
	tests := []struct {
		name   string
		line   int
		budget int
		wantOK bool
	}{
		{name: "merkez korunur", line: 110, budget: 100, wantOK: true},
		{name: "pencerenin ilk satırı", line: 100, budget: 60, wantOK: true},
		{name: "pencerenin son satırı", line: 120, budget: 60, wantOK: true},
		{name: "içerikte olmayan satır", line: 999, budget: 500, wantOK: false},
		{name: "sıfır bütçe", line: 110, budget: 0, wantOK: false},
		{name: "satır 0", line: 0, budget: 500, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, from, to := centerToBudget(w.Content, tt.line, tt.budget)
			if (got != "") != tt.wantOK {
				t.Fatalf("içerik=%q, beklenen boş-değil=%v", got, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if !frameLineIn(got, tt.line) {
				t.Fatalf("merkez satır (%d) yok:\n%s", tt.line, got)
			}
			if from > tt.line || to < tt.line {
				t.Fatalf("aralık %d-%d merkezi (%d) kapsamıyor", from, to, tt.line)
			}
			if n := len([]rune(got)); n > tt.budget {
				t.Fatalf("bütçe aşıldı: %d > %d", n, tt.budget)
			}
		})
	}
}

func TestFenceLang(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/src/main/java/com/x/Y.java", "java"},
		{"/src/main/kotlin/com/x/Y.kt", "kotlin"},
		{"/build.gradle.kts", "kotlin"},
		{"/src/main/scala/com/x/Y.scala", "scala"},
		{"/x/Y.sc", "scala"},
		{"/src/main/groovy/com/x/Y.groovy", "groovy"},
		{"/src/main/java/com/x/Y.JAVA", "java"}, // uzantı harf duyarsız
		{"/x/Makefile", ""},                     // uzantı yok
		{"/x/Y.py", ""},                         // bilinmiyor → etiketsiz çit
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := fenceLang(tt.path); got != tt.want {
				t.Fatalf("fenceLang(%q)=%q, istenen %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestMarkFrameLine(t *testing.T) {
	content := "244| a\n245| b\n246| c\n247| d"
	tests := []struct {
		name string
		line int
		want string
	}{
		{name: "ortadaki satır", line: 246, want: ">>> 246| c"},
		{name: "ilk satır", line: 244, want: ">>> 244| a"},
		{name: "içerikte olmayan satır", line: 999, want: ""},
		{name: "satır 0", line: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markFrameLine(content, tt.line)
			if tt.want == "" {
				if got != content {
					t.Fatalf("işaretlenmemeliydi:\n%s", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("işaret yok (%q):\n%s", tt.want, got)
			}
			// TEK satır işaretlenir.
			if n := strings.Count(got, frameMarker); n != 1 {
				t.Fatalf("işaret sayısı=%d, istenen 1:\n%s", n, got)
			}
			// Diğer satırlar aynen kalır.
			if !strings.Contains(got, "\n247| d") && !strings.HasSuffix(got, "247| d") {
				t.Fatalf("işaretsiz satır bozuldu:\n%s", got)
			}
		})
	}
}

func TestSegmentLabel(t *testing.T) {
	tests := []struct {
		name          string
		seg, deepest  int
		want, wantNot string
	}{
		{name: "zincirsiz stack → etiket yok", seg: 0, deepest: 0, want: ""},
		{name: "en derin segment", seg: 2, deepest: 2, want: "kök neden segmenti (Caused by #2)"},
		{name: "dış wrapper", seg: 0, deepest: 2, want: "dış (wrapper) exception"},
		{name: "ara segment", seg: 1, deepest: 2, want: "Caused by #1", wantNot: "kök neden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := segmentLabel(tt.seg, tt.deepest)
			if tt.want == "" {
				if got != "" {
					t.Fatalf("etiket beklenmiyordu: %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("etiket=%q, %q içermeli", got, tt.want)
			}
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Fatalf("etiket=%q, %q İÇERMEMELİ — olmayan istihkak", got, tt.wantNot)
			}
		})
	}
}

func TestPromptBlockMarksAndLabels(t *testing.T) {
	root := mkWindow(216, 276, 246)
	root.Segment = 2
	root.Path = "/src/main/java/com/x/Root.java"
	wrapper := mkWindow(10, 40, 25)
	wrapper.Segment = 0
	wrapper.Path = "/src/main/kotlin/com/x/Wrapper.kt"

	cc := CodeContext{Repo: "core-service", Branch: "release",
		Windows: []CodeWindow{root, wrapper}}
	block := cc.PromptBlock()

	// (1) İşaret AÇIKLANMIŞ olmalı: modele tarif edilmemiş bir sembol
	// göndermek, hiç göndermemekten kötü.
	if !strings.Contains(block, frameMarker+" ile işaretli satır") {
		t.Fatalf("işaret açıklaması (legend) yok:\n%s", block)
	}
	// (2) Hata satırları işaretli.
	if !strings.Contains(block, ">>> 246| ") || !strings.Contains(block, ">>> 25| ") {
		t.Fatalf("hata satırı işaretlenmemiş:\n%s", block)
	}
	if n := strings.Count(block, frameMarker+" "); n != 3 { // 2 satır + legend
		t.Fatalf("işaret sayısı=%d, istenen 3 (2 satır + legend)", n)
	}
	// (3) Konum + segment etiketi.
	for _, want := range []string{
		"pencere 1/2 — kök neden segmenti (Caused by #2)",
		"pencere 2/2 — dış (wrapper) exception",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("etiket yok (%q):\n%s", want, block)
		}
	}
	// (4) Çit uzantıdan: .java → java, .kt → kotlin.
	if !strings.Contains(block, "```java\n") || !strings.Contains(block, "```kotlin\n") {
		t.Fatalf("çit dile göre değil:\n%s", block)
	}
	// Eski hâl: her pencere ```java idi.
	if strings.Count(block, "```java\n") != 1 {
		t.Fatalf("kotlin penceresi hâlâ java diye etiketlenmiş:\n%s", block)
	}
}

func TestPromptBlockNoChainNoSegmentLabel(t *testing.T) {
	// Zincirsiz (tek segment) stack: uydurma "kök neden" etiketi YOK,
	// ama konum etiketi yine var.
	w := mkWindow(216, 276, 246)
	cc := CodeContext{Repo: "r", Windows: []CodeWindow{w}}
	block := cc.PromptBlock()
	if strings.Contains(block, "Caused by") || strings.Contains(block, "wrapper") {
		t.Fatalf("zincir yokken zincir etiketi basıldı:\n%s", block)
	}
	if !strings.Contains(block, "pencere 1/1") {
		t.Fatalf("konum etiketi yok:\n%s", block)
	}
}

// TestClampThenPromptBlockInvariant — iki düzeltmenin BİRLEŞİK
// sözleşmesi: bütçe ne olursa olsun, prompt'a giren her pencerede
// işaretli hata satırı VARDIR. (Halved() yolu da bu bütçeyi kullanır.)
func TestClampThenPromptBlockInvariant(t *testing.T) {
	ws := []CodeWindow{mkWindow(216, 276, 246), mkWindow(400, 460, 430), mkWindow(10, 70, 40)}
	for _, budget := range []int{4000, 2000, 1000, 500, 250, 120, 60} {
		out, _ := ClampCodeWindows(ws, budget)
		cc := CodeContext{Repo: "r", Windows: out}
		block := cc.PromptBlock()
		for i, w := range out {
			want := fmt.Sprintf("%s %d| ", frameMarker, w.Line)
			if !strings.Contains(block, want) {
				t.Fatalf("bütçe=%d pencere=%d: işaretli hata satırı (%q) prompt'ta yok:\n%s",
					budget, i, want, block)
			}
		}
		total := 0
		for _, w := range out {
			total += len([]rune(w.Content))
		}
		if total > budget {
			t.Fatalf("bütçe=%d aşıldı: %d", budget, total)
		}
	}
}
