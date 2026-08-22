package stackparse

import (
	"strconv"
	"strings"
	"testing"
)

// causedby_test.go — v0.9.1235.
//
// SEMPTOM: "Kodu da incele" üç kod penceresinin üçünü de en DIŞTAKİ
// (wrapper) exception'ın yeniden-fırlatma satırlarına harcıyordu; kök
// nedenin gerçekten fırlatıldığı satır — ki o her zaman en derin
// "Caused by:" bölümündedir — hiç pencere almıyordu. Katmanlı BSA/EJB
// kodunda wrapper bölümünde de rahatça 3+ uygulama frame'i bulunduğu
// için tavan zincirin dibine hiç ulaşmıyordu.
//
// KÖK NEDEN: ParseJava "Caused by:" satırını sessizce atlıyor ve Frame
// zincirdeki yerini TAŞIMIYORDU (bilgi aşağı akışta geri kazanılamaz
// hale geliyordu); AppFrames de metin sırasına göre ilk n frame'i
// alıyordu.
//
// Bu dosya iki şeyi birden çiviliyor: segment etiketlemesi ve
// en-derin-önce seçim — ayrıca tek segmentli stack'te davranışın
// BİRE BİR korunduğunu.

// wrapperStack — kanonik JBoss deseni: wrapper bölümünde ÜÇ uygulama
// frame'i (tavanı tek başına doldurmaya yeter), kök neden en dipte.
const wrapperStack = "jakarta.ejb.EJBException: host response error\n" +
	"\tat deployment.BSAWEB.war//com.example.card.CardFacade.handle(CardFacade.java:120)\n" +
	"\tat deployment.BSAWEB.war//com.example.card.CardService.call(CardService.java:88)\n" +
	"\tat deployment.BSAWEB.war//com.example.card.CardDetailBusiness.rethrow(CardDetailBusiness.java:246)\n" +
	"\tat org.jboss.as.ee.component.ManagedReference.invoke(ManagedReference.java:60)\n" +
	"Caused by: java.lang.NullPointerException: pan is null\n" +
	"\tat deployment.BSAWEB.war//com.example.card.CardRepository.find(CardRepository.java:412)\n" +
	"\tat java.base/java.util.Optional.orElseThrow(Optional.java:403)\n" +
	"\t... 17 more\n"

func TestParseJavaSegmentTagging(t *testing.T) {
	tests := []struct {
		name  string
		stack string
		// want — her frame için "Class:Segment".
		want []string
	}{
		{
			name: "tek segment — hepsi 0",
			stack: "java.lang.IllegalStateException: boom\n" +
				"\tat com.example.a.A.one(A.java:1)\n" +
				"\tat com.example.b.B.two(B.java:2)\n",
			want: []string{"com.example.a.A:0", "com.example.b.B:0"},
		},
		{
			// (e) SAVUNMA: log alanından kırpılmış stack başlıksız gelir,
			// ilk satır doğrudan bir frame'dir. Sayaç 0'dan başlamalı.
			name: "ilk satır frame — sayaç 0'dan başlar",
			stack: "\tat com.example.a.A.one(A.java:1)\n" +
				"Caused by: java.lang.NullPointerException\n" +
				"\tat com.example.c.C.three(C.java:3)\n",
			want: []string{"com.example.a.A:0", "com.example.c.C:1"},
		},
		{
			// (c) ÜÇ segment: her "Caused by:" sayacı bir artırır.
			name: "çok katmanlı Caused by",
			stack: "A: outer\n" +
				"\tat com.example.a.A.one(A.java:1)\n" +
				"Caused by: B: middle\n" +
				"\tat com.example.b.B.two(B.java:2)\n" +
				"Caused by: C: root\n" +
				"\tat com.example.c.C.three(C.java:3)\n",
			want: []string{"com.example.a.A:0", "com.example.b.B:1", "com.example.c.C:2"},
		},
		{
			// (d) "... N more" ELİZYONU: frame değil, sayaç da artırmaz.
			name: "elizyon satırı atlanır, sayaca dokunmaz",
			stack: "\tat com.example.a.A.one(A.java:1)\n" +
				"\t... 42 more\n" +
				"Caused by: X\n" +
				"\tat com.example.c.C.three(C.java:3)\n" +
				"\t... 17 more\n",
			want: []string{"com.example.a.A:0", "com.example.c.C:1"},
		},
		{
			// Girintili "Caused by:" (bazı log biçimleyicileri) SAYILIR.
			name: "girintili Caused by da sayılır",
			stack: "\tat com.example.a.A.one(A.java:1)\n" +
				"    Caused by: java.lang.NullPointerException\n" +
				"\tat com.example.c.C.three(C.java:3)\n",
			want: []string{"com.example.a.A:0", "com.example.c.C:1"},
		},
		{
			// Mesajın İÇİNDE geçen ifade sayacı artırmamalı — eşleşme
			// önektedir, "içeriyor" değil.
			name: "mesaj içindeki ifade sayacı artırmaz",
			stack: "java.lang.RuntimeException: request failed, caused by: upstream timeout\n" +
				"\tat com.example.a.A.one(A.java:1)\n" +
				"\tat com.example.b.B.two(B.java:2)\n",
			want: []string{"com.example.a.A:0", "com.example.b.B:0"},
		},
		{
			name:  "boş stack",
			stack: "   \n",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseJava(tt.stack)
			if len(got) != len(tt.want) {
				t.Fatalf("frame sayısı=%d, istenen %d: %+v", len(got), len(tt.want), got)
			}
			for i, f := range got {
				if s := f.Class + ":" + strconv.Itoa(f.Segment); s != tt.want[i] {
					t.Errorf("frame[%d]=%q, istenen %q", i, s, tt.want[i])
				}
			}
		})
	}
}

func TestAppFramesPrefersDeepestCause(t *testing.T) {
	tests := []struct {
		name  string
		stack string
		n     int
		// want — seçilen frame'lerin String() değerleri, SIRAYLA.
		want []string
	}{
		{
			// (a) ÇEKİRDEK REGRESYON: wrapper bölümünde üç uygulama
			// frame'i var ve tavan 3 — eski kod üçünü de wrapper'a
			// veriyordu, kök nedenin satırı hiç pencere almıyordu.
			name:  "wrapper 3 frame yiyordu, artık kök neden önce",
			stack: wrapperStack,
			n:     3,
			want: []string{
				"com.example.card.CardRepository.find(CardRepository.java:412)",
				"com.example.card.CardFacade.handle(CardFacade.java:120)",
				"com.example.card.CardService.call(CardService.java:88)",
			},
		},
		{
			// Tavan 1: tek pencere kök nedene gider.
			name:  "tavan 1 — yalnız kök neden",
			stack: wrapperStack,
			n:     1,
			want:  []string{"com.example.card.CardRepository.find(CardRepository.java:412)"},
		},
		{
			// (c) Üç segment: en derinden dışa doğru yürünür.
			name: "üç segment — en derin önce",
			stack: "A: outer\n" +
				"\tat com.example.a.A.one(A.java:1)\n" +
				"Caused by: B: middle\n" +
				"\tat com.example.b.B.two(B.java:2)\n" +
				"Caused by: C: root\n" +
				"\tat com.example.c.C.three(C.java:3)\n" +
				"\tat com.example.c.C.four(C.java:4)\n",
			n: 4,
			want: []string{
				// Segment 2 (kök), İÇİNDEKİ sıra korunarak…
				"com.example.c.C.three(C.java:3)",
				"com.example.c.C.four(C.java:4)",
				// …sonra dışa doğru.
				"com.example.b.B.two(B.java:2)",
				"com.example.a.A.one(A.java:1)",
			},
		},
		{
			// (b) TEK SEGMENT: davranış eskisiyle BİRE BİR aynı —
			// metin sırası, çerçeve/konumlandırılamaz eleme, tavan.
			name: "tek segment — sıra bire bir korunur",
			stack: "java.lang.IllegalStateException: boom\n" +
				"\tat com.example.a.A.one(A.java:1)\n" +
				"\tat org.springframework.web.X.y(X.java:9)\n" +
				"\tat com.example.b.B.two(Unknown Source)\n" +
				"\tat com.example.c.C.three(C.java:3)\n" +
				"\tat com.example.d.D.four(D.java:4)\n",
			n: 3,
			want: []string{
				"com.example.a.A.one(A.java:1)",
				"com.example.c.C.three(C.java:3)",
				"com.example.d.D.four(D.java:4)",
			},
		},
		{
			// Kök segmentte konumlandırılabilir app frame'i YOKSA
			// (yalnız JDK) seçim dışa doğru düşer — pencere kaybolmaz.
			name: "kök segment yalnız JDK — dışa düşer",
			stack: "A: outer\n" +
				"\tat com.example.a.A.one(A.java:1)\n" +
				"Caused by: java.lang.NullPointerException\n" +
				"\tat java.base/java.util.Optional.orElseThrow(Optional.java:403)\n",
			n:    2,
			want: []string{"com.example.a.A.one(A.java:1)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppFrames(ParseJava(tt.stack), tt.n)
			if len(got) != len(tt.want) {
				t.Fatalf("seçilen frame sayısı=%d, istenen %d:\n%s",
					len(got), len(tt.want), renderFrames(got))
			}
			for i, f := range got {
				if f.String() != tt.want[i] {
					t.Errorf("frame[%d]=%q, istenen %q", i, f.String(), tt.want[i])
				}
			}
		})
	}
}

// TestAppFramesSingleSegmentByteForByte — (b)'nin katı hâli: segment
// taşımayan (hepsi 0) bir girdide çıktı, eski "metin sırasına göre ilk
// n" kuralının ürettiğiyle BİRE BİR aynı olmalı. Elle kurulan
// Frame'lerin Segment'i sıfır değerdir; bu test aynı zamanda dışarıdan
// Frame üreten çağıranların (devops testleri) etkilenmediğini pinler.
func TestAppFramesSingleSegmentByteForByte(t *testing.T) {
	frames := []Frame{
		{Class: "org.apache.x.Y", Method: "m", File: "Y.java", Line: 1, IsApp: false},
		{Class: "com.example.a.A", Method: "m", File: "", Line: 0, IsApp: true},
		{Class: "com.example.b.B", Method: "m", File: "B.java", Line: 0, IsApp: true},
		{Class: "com.example.c.C", Method: "m", File: "C.java", Line: 3, IsApp: true},
		{Class: "com.example.d.D", Method: "m", File: "D.java", Line: 4, IsApp: true},
		{Class: "com.example.e.E", Method: "m", File: "E.java", Line: 5, IsApp: true},
	}
	for _, n := range []int{1, 2, 3, 10} {
		got := AppFrames(frames, n)
		want := legacyAppFrames(frames, n)
		if renderFrames(got) != renderFrames(want) {
			t.Errorf("n=%d: çıktı eski kuralla ayrıştı:\ngot:\n%s\nwant:\n%s",
				n, renderFrames(got), renderFrames(want))
		}
	}
	if AppFrames(frames, 0) != nil {
		t.Error("n=0 nil dönmeli")
	}
}

// legacyAppFrames — v0.9.1235 ÖNCESİ AppFrames gövdesi, birebir.
// Kıyas tabanı olarak duruyor: tek segmentli girdide yeni kuralın ondan
// sapmadığını göstermek, "sıra korunur" iddiasının tek kanıtıdır.
func legacyAppFrames(frames []Frame, n int) []Frame {
	if n <= 0 {
		return nil
	}
	out := make([]Frame, 0, n)
	for _, f := range frames {
		if !f.IsApp || f.File == "" || f.Line <= 0 {
			continue
		}
		out = append(out, f)
		if len(out) >= n {
			break
		}
	}
	return out
}

func renderFrames(fs []Frame) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, f.String()+" [seg "+strconv.Itoa(f.Segment)+"]")
	}
	return strings.Join(parts, "\n")
}
