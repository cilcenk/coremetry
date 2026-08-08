// Package stackparse — stack trace metnini yapısal frame'lere çeviren
// SAF çözümleyici (v0.9.830).
//
// Amacı tek: bir frame'i KAYNAK KODDA konumlandırabilmek. Bunun için
// dosya adı, satır numarası ve "bu uygulama kodu mu, çerçeve gürültüsü
// mü?" bilgisi gerekir; gerisi (repo eşleme, dosya çekme) devops
// paketinin işi.
//
// # fingerprint bilinçli ayrı, bkz. spec 2026-08-09
//
// chstore.topFrames / FingerprintException bu paketi KULLANMAZ ve
// kullanmamalıdır. Parmak izi, ClickHouse'ta hâlihazırda milyonlarca
// satırın kimliği: buradaki normalizasyonun (war// öneki soyma, JPMS
// modülü ayırma) oraya sızması mevcut TÜM grupları yeniden
// numaralandırır — operatörün ekranında bir gecede "yeni" exception
// seli, üstelik sessiz. Bu paket yalnız OKUMA tarafıdır; gruplama
// hattına dokunmaz. İki taraf da aynı metni okur, farklı soruyu
// cevaplar ve bilinçli olarak ayrı kalır.
//
// Java önce (operatörün filosu JBoss/WildFly ağırlıklı). Go/Python
// istenirse aynı Frame sözleşmesiyle eklenir.
package stackparse

import (
	"regexp"
	"strconv"
	"strings"
)

// Frame — çözümlenmiş tek stack satırı.
//
// File/Line boş olabilir ("Unknown Source", "Native Method") — o
// frame kodda konumlandırılamaz, çağıran atlar.
type Frame struct {
	// Class — tam nitelikli sınıf adı (paket dahil), örn.
	// com.example.billing.CardService. İç sınıflarda $ korunur.
	Class string
	// Method — metot adı; <init> / <clinit> / lambda$x$0 aynen kalır.
	Method string
	// File — kaynak dosya adı (yol YOK, Java stack'i yol taşımaz).
	File string
	// Line — kaynak satırı; 0 = bilinmiyor.
	Line int
	// Module — soyulan önek: "deployment.BSAWEB.war", "java.base",
	// "org.jboss.as.ee", "mymodule@1.2.3". Boş = önek yoktu.
	Module string
	// IsApp — uygulama kodu mu? Çerçeve/JDK frame'leri false.
	IsApp bool
}

// javaAtRe — "    at <ref>(<source>)" satırı.
//
// <ref> boşluksuz değil: WildFly "at org.jboss.x//com.y.Z.m" biçimini
// de, "at com.y.Z.m" biçimini de aynı yakalayıcıya sokuyoruz ve
// önekleri sonra soyuyoruz. Parantez içi ise ":" içerebilir de
// içermeyebilir de ("Foo.java:246" / "Unknown Source").
var javaAtRe = regexp.MustCompile(`^\s*at\s+(\S+)\s*\(([^()]*)\)\s*$`)

// frameworkPrefixes — IsApp=false yapan sınıf önekleri.
//
// Liste spec'ten birebir: JDK'nın kendisi + operatörün filosunda her
// stack'in dibinde duran üç çerçeve (Spring, Apache/Tomcat, JBoss/
// Undertow). Bunların kaynağı zaten operatörün repo'sunda DEĞİL, yani
// kod çekmeye çalışmak boşa istek olurdu.
var frameworkPrefixes = []string{
	"java.",
	"javax.",
	"jakarta.",
	"sun.",
	"jdk.",
	"org.springframework.",
	"org.apache.",
	"org.jboss.",
	"io.undertow.",
}

// IsAppClass — sınıf adı uygulama kodu mu? Saf; tablo-testli.
//
// Önek eşleşmesi, "içeriyor" DEĞİL: com.example.jdk.Helper bir
// uygulama sınıfıdır, "jdk." alt dizgisi geçiyor diye elenmemeli.
func IsAppClass(class string) bool {
	if class == "" {
		return false
	}
	for _, p := range frameworkPrefixes {
		if strings.HasPrefix(class, p) {
			return false
		}
	}
	return true
}

// ParseJava — Java/Kotlin/Scala stack trace'ini frame'lere çevirir.
//
// Tanınmayan satırlar (mesaj başlığı, "Caused by:", "... 42 more",
// yarım kesilmiş satır) sessizce ATLANIR: stacktrace'ler log
// alanlarında kırpılmış gelir, tek bozuk satır tüm çözümlemeyi
// düşürmemeli. "Caused by:" zincirindeki frame'ler sıralarıyla
// listeye girer — kök nedene en yakın kod genelde oradadır.
func ParseJava(stack string) []Frame {
	if strings.TrimSpace(stack) == "" {
		return nil
	}
	var out []Frame
	for _, line := range strings.Split(stack, "\n") {
		if f, ok := parseJavaLine(line); ok {
			out = append(out, f)
		}
	}
	return out
}

// parseJavaLine — tek satır. ok=false: bu satır bir frame değil.
func parseJavaLine(line string) (Frame, bool) {
	line = strings.TrimRight(line, "\r")
	m := javaAtRe.FindStringSubmatch(line)
	if m == nil {
		return Frame{}, false
	}
	ref, src := m[1], m[2]

	var module string
	// (1) war / JBoss modül öneki: "deployment.APP.war//com.x.Y.m"
	// veya "org.jboss.as.ee//org.jboss...". Ayraç ÇİFT eğik çizgi ve
	// son geçtiği yer alınır — iç içe önek gören kurulumlar var.
	if i := strings.LastIndex(ref, "//"); i >= 0 {
		module = ref[:i]
		ref = ref[i+2:]
	}
	// (2) JPMS öneki: "java.base/java.util.Optional.orElseThrow" ya da
	// "mymodule@1.2.3/com.x.Y.m".
	//
	// $ MUHAFIZI: "com.x.Y$$Lambda$14/0x00000008.run" satırında da bir
	// "/" var ama o modül ayracı DEĞİL, JVM'in ürettiği lambda sınıf
	// adının parçası. Önekte $ varsa dokunmayız.
	if i := strings.Index(ref, "/"); i > 0 && !strings.Contains(ref[:i], "$") {
		if module == "" {
			module = ref[:i]
		}
		ref = ref[i+1:]
	}

	dot := strings.LastIndex(ref, ".")
	if dot <= 0 || dot == len(ref)-1 {
		// Paketsiz ya da metotsuz — konumlandırılamaz.
		return Frame{}, false
	}
	f := Frame{Class: ref[:dot], Method: ref[dot+1:], Module: module}
	f.File, f.Line = parseJavaSource(src)
	f.IsApp = IsAppClass(f.Class)
	return f, true
}

// parseJavaSource — parantez içi. Üç biçim:
//
//	"CardService.java:246" → dosya + satır
//	"CardService.java"     → dosya, satır 0 (derleyici -g:none)
//	"Unknown Source" / "Native Method" → ikisi de boş
//
// Dosya adında boşluk olmaz; "Unknown Source" tam da bu yüzden
// ayıklanabiliyor.
func parseJavaSource(src string) (string, int) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", 0
	}
	file, line := src, 0
	if c := strings.LastIndex(src, ":"); c >= 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(src[c+1:])); err == nil && n > 0 {
			file, line = strings.TrimSpace(src[:c]), n
		}
	}
	if file == "" || strings.ContainsAny(file, " \t") || !strings.Contains(file, ".") {
		return "", 0
	}
	return file, line
}

// AppFrames — IsApp olan ve kodda konumlandırılabilen (dosya+satır)
// ilk n frame. Kod çekmenin girdisi; saf.
//
// Satırsız app frame'i ELENİR: dosya adı eşleşse bile pencere
// merkezi olmaz, "dosyanın başından 60 satır" ise kanıt değil
// gürültüdür.
func AppFrames(frames []Frame, n int) []Frame {
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

// String — "com.x.Y.m(Y.java:246)". Prompt'ta ve kaynak satırında
// frame'i tek parça göstermek için.
func (f Frame) String() string {
	var b strings.Builder
	b.WriteString(f.Class)
	b.WriteString(".")
	b.WriteString(f.Method)
	b.WriteString("(")
	if f.File == "" {
		b.WriteString("Unknown Source")
	} else {
		b.WriteString(f.File)
		if f.Line > 0 {
			b.WriteString(":")
			b.WriteString(strconv.Itoa(f.Line))
		}
	}
	b.WriteString(")")
	return b.String()
}

// PackagePath — sınıfın paket yolu, "/" ayraçlı: com.x.billing.Card →
// "com/x/billing". Depo ağacında dosya eşleştirirken skorlayıcının
// girdisi. Paketsiz sınıf → boş.
func (f Frame) PackagePath() string {
	dot := strings.LastIndex(f.Class, ".")
	if dot <= 0 {
		return ""
	}
	return strings.ReplaceAll(f.Class[:dot], ".", "/")
}
