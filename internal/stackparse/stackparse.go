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
	"sort"
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
	// Segment — frame'in "Caused by" zincirindeki derinliği (v0.9.1235).
	// 0 = en dış (wrapper) exception'ın kendi stack'i, 1 = ilk
	// "Caused by:" bölümü, 2 = onun sebebi… Java en DIŞTAKİNİ önce
	// basar, kök neden ise EN DERİN segmenttedir; bu alan olmadan
	// zincirdeki yer aşağı akışta geri kazanılamıyordu.
	Segment int
	// Tier — RankFrames damgası (v0.10.112): 0 = operatörün uygulama
	// paket öneklerinden biriyle başlıyor, 1 = diğer (kurum-içi
	// çerçeve, üçüncü parti kütüphane — IsApp ama önek listesinde
	// değil). Yalnız RankFrames'in döndürdüğü kopyalarda anlamlı;
	// ParseJava 0 bırakır.
	Tier int
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
// Tanınmayan satırlar (mesaj başlığı, "... 42 more", yarım kesilmiş
// satır) sessizce ATLANIR: stacktrace'ler log alanlarında kırpılmış
// gelir, tek bozuk satır tüm çözümlemeyi düşürmemeli.
//
// "Caused by:" satırı da bir frame DEĞİL, ama artık sessiz değil:
// gördüğü yerde segment sayacı artar ve sonraki frame'ler Segment=N
// ile işaretlenir (v0.9.1235). Frame'ler yine metindeki SIRAYLA
// listeye girer — sıralamayı değiştirmek çağıranın işi (AppFrames),
// çünkü bu paketin sözleşmesi "metni sadakatle çevir".
//
// "Suppressed:" bölümleri BİLİNÇLİ kapsam dışı: try-with-resources'ın
// bastırılmış hatası semantik olarak bir sebep zinciri değil, paralel
// bir yan olaydır ve operatörün filosunda nadirdir. Segment sayacına
// karışmaması, bastırılmış bir kapatma hatasının kök neden yerine
// geçmesini engeller.
func ParseJava(stack string) []Frame {
	if strings.TrimSpace(stack) == "" {
		return nil
	}
	var out []Frame
	seg := 0
	for _, line := range strings.Split(stack, "\n") {
		if isCausedByLine(line) {
			seg++
			continue
		}
		if f, ok := parseJavaLine(line); ok {
			f.Segment = seg
			out = append(out, f)
		}
	}
	return out
}

// isCausedByLine — satır bir sebep-zinciri başlığı mı? Saf.
//
// Baştaki boşluk kırpılır (bazı log biçimleyicileri "Caused by:"
// satırını girintiler), ama eşleşme ÖNEKTEDİR: mesajın içinde
// "... caused by: timeout" geçen bir exception metni segment sayacını
// artırmamalı.
func isCausedByLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "Caused by:")
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
// en fazla n frame, EN DERİN "Caused by" segmentinden dışa doğru.
// Kod çekmenin girdisi; saf.
//
// Satırsız app frame'i ELENİR: dosya adı eşleşse bile pencere
// merkezi olmaz, "dosyanın başından 60 satır" ise kanıt değil
// gürültüdür.
//
// # Neden en derin segment önce (v0.9.1235)
//
// Java stack'i en DIŞTAKİ (wrapper) exception'la başlar; gerçek kök
// neden en derin "Caused by:" bölümündedir. Metin sırasına göre ilk n
// frame'i almak, katmanlı BSA/EJB kodunda — ki orada catch-rethrow
// yolu da uygulama sınıflarından geçer — üç kod penceresinin üçünü de
// wrapper'ın yeniden-fırlatma satırlarına harcıyor, hatanın DOĞDUĞU
// satır hiç pencere alamıyordu. Artık bütçe önce kök nedene gidiyor,
// artan kalırsa dışarı doğru yürüyor.
//
// Segment İÇİNDEKİ sıra korunur (fırlatma noktası o segmentin de en
// üstündedir) ve tek segmentli stack'te davranış eskisiyle
// BİRE BİR aynıdır — elle kurulan Frame'lerin Segment'i 0'dır.
func AppFrames(frames []Frame, n int) []Frame {
	if n <= 0 {
		return nil
	}
	deepest := 0
	for _, f := range frames {
		if locatableApp(f) && f.Segment > deepest {
			deepest = f.Segment
		}
	}
	out := make([]Frame, 0, n)
	for seg := deepest; seg >= 0 && len(out) < n; seg-- {
		for _, f := range frames {
			if !locatableApp(f) || f.Segment != seg {
				continue
			}
			out = append(out, f)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

// locatableApp — bu frame için kod penceresi kurulabilir mi?
func locatableApp(f Frame) bool {
	return f.IsApp && f.File != "" && f.Line > 0
}

// HasAppPrefix — sınıf adı operatörün uygulama paket öneklerinden
// biriyle mi başlıyor? ÖNEK eşleşmesi ("içeriyor" değil); boş/beyaz
// önekler yok sayılır. Saf.
func HasAppPrefix(class string, prefixes []string) bool {
	if class == "" {
		return false
	}
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p != "" && strings.HasPrefix(class, p) {
			return true
		}
	}
	return false
}

// RankFrames — kod çekmenin aday listesi, UYGULAMA FRAME'İ ÖNCE
// (v0.10.112). AppFrames'in üstüne bir birincil anahtar koyar:
//
//  1. Tier — appPrefixes'ten biriyle başlayan sınıf (0) önce, kalan
//     uygulama frame'leri (1) sonra. Çerçeve/JDK frame'leri yine
//     dışarıda; AMA operatörün açık öneki frameworkPrefixes'i EZER
//     (org.apache.myco bir uygulama olabilir — açık ayar, sabit
//     listeden güçlüdür).
//  2. Segment — derin "Caused by" önce (v0.9.1235 sözleşmesi tier
//     içinde aynen korunur).
//  3. Metin sırası.
//
// appPrefixes boşsa (ya da yalnız boş dizeler taşıyorsa) sonuç
// AppFrames ile BİRE BİR aynıdır: bugün çalışan hiçbir kurulumun
// sırası değişmez; sıralama yalnız operatör önek yazınca devreye girer.
//
// Neden sıralama, süzme değil: kurum-içi çerçeve sınıfları (RestFilter,
// BasicDispatcher…) bazen gerçekten hatanın atıldığı yerdir; onları
// atmak kanıtı düşürür. Arkaya almak, tavanı önce iş sınıflarına
// harcatır — iş sınıfı bulunamazsa sıra yine onlara gelir.
func RankFrames(frames []Frame, n int, appPrefixes []string) []Frame {
	if n <= 0 {
		return nil
	}
	hasPrefixes := false
	for _, p := range appPrefixes {
		if strings.TrimSpace(p) != "" {
			hasPrefixes = true
			break
		}
	}
	if !hasPrefixes {
		return AppFrames(frames, n)
	}
	type cand struct {
		f    Frame
		tier int
		idx  int
	}
	var cs []cand
	for i, f := range frames {
		if f.File == "" || f.Line <= 0 {
			continue
		}
		app := HasAppPrefix(f.Class, appPrefixes)
		if !app && !f.IsApp {
			continue
		}
		tier := 1
		if app {
			tier = 0
		}
		f.Tier = tier
		cs = append(cs, cand{f: f, tier: tier, idx: i})
	}
	sort.SliceStable(cs, func(a, b int) bool {
		if cs[a].tier != cs[b].tier {
			return cs[a].tier < cs[b].tier
		}
		if cs[a].f.Segment != cs[b].f.Segment {
			return cs[a].f.Segment > cs[b].f.Segment
		}
		return cs[a].idx < cs[b].idx
	})
	out := make([]Frame, 0, n)
	for _, c := range cs {
		out = append(out, c.f)
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
