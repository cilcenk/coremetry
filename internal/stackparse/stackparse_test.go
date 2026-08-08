package stackparse

import "testing"

// stackparse_test.go — v0.9.830.
//
// Örnekler JENERİKtir: gerçek bir müşteri sunucusu, koleksiyonu ya da
// uygulama adı bu depoya girmez. Şekiller (war//, JPMS, JBoss modülü)
// gerçek, adlar uydurma.

func TestParseJavaWarPrefix(t *testing.T) {
	// Operatör ekranındaki şekil: WildFly deployment öneki + noktalı
	// paket + Foo.java:NNN. Önek Module'a taşınır, Class temiz kalır.
	stack := "" +
		"jakarta.ejb.EJBException: host response error\n" +
		"\tat deployment.APPWEB.war//com.example.card.CardDetailBusiness.handleHostResponseError(CardDetailBusiness.java:246)\n" +
		"\tat deployment.APPWEB.war//com.example.card.CardDetailBusiness.detail(CardDetailBusiness.java:118)\n"

	got := ParseJava(stack)
	if len(got) != 2 {
		t.Fatalf("frame sayısı=%d, istenen 2: %+v", len(got), got)
	}
	f := got[0]
	if f.Module != "deployment.APPWEB.war" {
		t.Errorf("Module=%q, istenen deployment.APPWEB.war", f.Module)
	}
	if f.Class != "com.example.card.CardDetailBusiness" {
		t.Errorf("Class=%q — war öneki soyulmamış", f.Class)
	}
	if f.Method != "handleHostResponseError" {
		t.Errorf("Method=%q", f.Method)
	}
	if f.File != "CardDetailBusiness.java" || f.Line != 246 {
		t.Errorf("File/Line=%q/%d, istenen CardDetailBusiness.java/246", f.File, f.Line)
	}
	if !f.IsApp {
		t.Error("IsApp=false — com.example.* uygulama kodudur")
	}
	if f.PackagePath() != "com/example/card" {
		t.Errorf("PackagePath=%q", f.PackagePath())
	}
	if want := "com.example.card.CardDetailBusiness.handleHostResponseError(CardDetailBusiness.java:246)"; f.String() != want {
		t.Errorf("String=%q\nistenen %q", f.String(), want)
	}
}

func TestParseJavaFrameTable(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		ok     bool
		class  string
		method string
		file   string
		line_  int
		module string
		isApp  bool
	}{
		{
			name: "düz uygulama frame'i",
			line: "\tat com.example.billing.CardService.charge(CardService.java:42)",
			ok:   true, class: "com.example.billing.CardService", method: "charge",
			file: "CardService.java", line_: 42, isApp: true,
		},
		{
			name: "JPMS java.base öneki soyulur, çerçeve sayılır",
			line: "\tat java.base/java.util.Optional.orElseThrow(Optional.java:403)",
			ok:   true, class: "java.util.Optional", method: "orElseThrow",
			file: "Optional.java", line_: 403, module: "java.base", isApp: false,
		},
		{
			name: "adlandırılmış JPMS modülü + sürüm",
			line: "\tat mymodule@1.2.3/com.example.core.Runner.run(Runner.java:9)",
			ok:   true, class: "com.example.core.Runner", method: "run",
			file: "Runner.java", line_: 9, module: "mymodule@1.2.3", isApp: true,
		},
		{
			name: "JBoss modül öneki + org.jboss sınıfı = çerçeve",
			line: "\tat org.jboss.as.ee@26.0//org.jboss.invocation.InterceptorContext.proceed(InterceptorContext.java:509)",
			ok:   true, class: "org.jboss.invocation.InterceptorContext", method: "proceed",
			file: "InterceptorContext.java", line_: 509, module: "org.jboss.as.ee@26.0", isApp: false,
		},
		{
			name: "Spring çerçevesi",
			line: "\tat org.springframework.web.servlet.DispatcherServlet.doService(DispatcherServlet.java:1010)",
			ok:   true, class: "org.springframework.web.servlet.DispatcherServlet", method: "doService",
			file: "DispatcherServlet.java", line_: 1010, isApp: false,
		},
		{
			name: "Apache/Tomcat çerçevesi",
			line: "\tat org.apache.catalina.core.StandardWrapperValve.invoke(StandardWrapperValve.java:197)",
			ok:   true, class: "org.apache.catalina.core.StandardWrapperValve", method: "invoke",
			file: "StandardWrapperValve.java", line_: 197, isApp: false,
		},
		{
			name: "Undertow çerçevesi",
			line: "\tat io.undertow.servlet.handlers.ServletHandler.handleRequest(ServletHandler.java:74)",
			ok:   true, class: "io.undertow.servlet.handlers.ServletHandler", method: "handleRequest",
			file: "ServletHandler.java", line_: 74, isApp: false,
		},
		{
			name: "jakarta çerçevesi",
			line: "\tat jakarta.servlet.http.HttpServlet.service(HttpServlet.java:764)",
			ok:   true, class: "jakarta.servlet.http.HttpServlet", method: "service",
			file: "HttpServlet.java", line_: 764, isApp: false,
		},
		{
			name: "Unknown Source — dosya/satır yok, frame yine de geçerli",
			line: "\tat com.example.core.Proxy.invoke(Unknown Source)",
			ok:   true, class: "com.example.core.Proxy", method: "invoke", isApp: true,
		},
		{
			name: "Native Method",
			line: "\tat java.base/jdk.internal.reflect.NativeMethodAccessorImpl.invoke0(Native Method)",
			ok:   true, class: "jdk.internal.reflect.NativeMethodAccessorImpl", method: "invoke0",
			module: "java.base", isApp: false,
		},
		{
			name: "satırsız kaynak (derleyici -g:none)",
			line: "\tat com.example.core.Runner.run(Runner.java)",
			ok:   true, class: "com.example.core.Runner", method: "run",
			file: "Runner.java", line_: 0, isApp: true,
		},
		{
			name: "constructor",
			line: "\tat com.example.core.Runner.<init>(Runner.java:11)",
			ok:   true, class: "com.example.core.Runner", method: "<init>",
			file: "Runner.java", line_: 11, isApp: true,
		},
		{
			name: "lambda",
			line: "\tat com.example.core.Runner.lambda$run$0(Runner.java:31)",
			ok:   true, class: "com.example.core.Runner", method: "lambda$run$0",
			file: "Runner.java", line_: 31, isApp: true,
		},
		{
			// $ MUHAFIZI: buradaki "/" JPMS ayracı DEĞİL, JVM'in ürettiği
			// lambda sınıf adının parçası. Soyulursa Class bozulur.
			name: "lambda proxy sınıfı — hex eki modül sanılmaz",
			line: "\tat com.example.core.Runner$$Lambda$14/0x0000000800c0a440.run(Unknown Source)",
			ok:   true, class: "com.example.core.Runner$$Lambda$14/0x0000000800c0a440", method: "run",
			isApp: true,
		},
		{
			name: "com.example.jdk.* — önek eşleşmesi, alt dizge DEĞİL",
			line: "\tat com.example.jdk.Helper.load(Helper.java:5)",
			ok:   true, class: "com.example.jdk.Helper", method: "load",
			file: "Helper.java", line_: 5, isApp: true,
		},
		{name: "başlık satırı", line: "java.lang.NullPointerException: boom"},
		{name: "Caused by satırı", line: "Caused by: java.lang.IllegalStateException: x"},
		{name: "... N more", line: "\t... 42 more"},
		{name: "boş satır", line: ""},
		{name: "yarım kesilmiş satır", line: "\tat com.example.core.Runner.run(Runner.j"},
		{name: "paketsiz/metotsuz", line: "\tat Runner(Runner.java:3)"},
		{name: "at yok", line: "\tcom.example.core.Runner.run(Runner.java:3)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseJava(tt.line)
			if !tt.ok {
				if len(got) != 0 {
					t.Fatalf("çözümlenmemeliydi, %+v döndü", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("frame sayısı=%d, istenen 1", len(got))
			}
			f := got[0]
			if f.Class != tt.class || f.Method != tt.method {
				t.Errorf("Class/Method=%q/%q, istenen %q/%q", f.Class, f.Method, tt.class, tt.method)
			}
			if f.File != tt.file || f.Line != tt.line_ {
				t.Errorf("File/Line=%q/%d, istenen %q/%d", f.File, f.Line, tt.file, tt.line_)
			}
			if f.Module != tt.module {
				t.Errorf("Module=%q, istenen %q", f.Module, tt.module)
			}
			if f.IsApp != tt.isApp {
				t.Errorf("IsApp=%v, istenen %v", f.IsApp, tt.isApp)
			}
		})
	}
}

func TestParseJavaSkipsGarbageKeepsGoodFrames(t *testing.T) {
	// Log alanından kırpılmış, araya bozuk satır girmiş stack:
	// tek bozuk satır çözümlemeyi düşürmemeli.
	stack := "" +
		"java.lang.RuntimeException: wrapped\n" +
		"\tat com.example.a.A.one(A.java:1)\n" +
		"???? bozuk satır ????\n" +
		"\tat org.springframework.x.B.two(B.java:2)\n" +
		"Caused by: java.lang.IllegalStateException: inner\n" +
		"\tat com.example.c.C.three(C.java:3)\n" +
		"\t... 17 more\n" +
		"\tat com.example.d.D.fo"

	got := ParseJava(stack)
	if len(got) != 3 {
		t.Fatalf("frame sayısı=%d, istenen 3: %+v", len(got), got)
	}
	app := AppFrames(got, 3)
	if len(app) != 2 {
		t.Fatalf("app frame sayısı=%d, istenen 2 (Spring elenmeli): %+v", len(app), app)
	}
	if app[0].Class != "com.example.a.A" || app[1].Class != "com.example.c.C" {
		t.Errorf("app frame sırası bozuk: %+v", app)
	}
}

func TestAppFramesLimitsAndSkipsUnlocatable(t *testing.T) {
	frames := []Frame{
		{Class: "org.apache.x.Y", Method: "m", File: "Y.java", Line: 1, IsApp: false},
		{Class: "com.example.a.A", Method: "m", File: "", Line: 0, IsApp: true},       // Unknown Source
		{Class: "com.example.b.B", Method: "m", File: "B.java", Line: 0, IsApp: true}, // satırsız
		{Class: "com.example.c.C", Method: "m", File: "C.java", Line: 3, IsApp: true},
		{Class: "com.example.d.D", Method: "m", File: "D.java", Line: 4, IsApp: true},
		{Class: "com.example.e.E", Method: "m", File: "E.java", Line: 5, IsApp: true},
	}
	got := AppFrames(frames, 2)
	if len(got) != 2 {
		t.Fatalf("n=2 tavanı tutmadı: %+v", got)
	}
	if got[0].Class != "com.example.c.C" || got[1].Class != "com.example.d.D" {
		t.Errorf("konumlandırılamayan frame'ler elenmedi: %+v", got)
	}
	if AppFrames(frames, 0) != nil {
		t.Error("n=0 nil dönmeli")
	}
}
