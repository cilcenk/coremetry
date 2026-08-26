package stackparse

// v0.9.1182 regresyon testleri — log kaydında stacktrace ARAMA.
//
// Operatör bildirimi: "Kodu da incele" işaretliyken cevap "Kod okunamadı —
// kodsuz analiz (bu kayıtta stacktrace yok)" diyordu, oysa kayıtta stack
// VARDI: modelin kendi "Stacktrace Detayı" bölümü exception tipini ve
// sınıfı adlandırıyor, yalnız metot adı için "tam görünmüyor" notu
// düşüyordu — kesilmiş bir metinden okuduğunun itirafı.
//
// Kök neden: tüm okuma yolları TEK yazıma bakıyordu
// (`exception.stacktrace`). O anahtar boşsa stack "yok" sayılıyor, kod
// çekici hiç çalışmıyor ve model stack'i 600 baytlık GÖVDE bütçesinin
// içinden yarım okuyordu.
//
// Bu testin iki dalı, kusurun iki gerçek hâli:
//   - ECS. Prod'un log deposu Elasticsearch; ECS'in alanı
//     `error.stack_trace`. OTel yazımını bekleyen okuyucu böyle bir
//     kurulumda HİÇBİR ZAMAN stack bulamaz ve bunu sessizce yapar.
//   - GÖVDE. Java'nın en yaygın logback/log4j deseni exception'ı mesajın
//     arkasına basar; ayrı bir alan hiç doğmaz.
//
// Fixture'lar TAMAMEN SENTETİK (kurulum adları depoya girmez).

import (
	"strings"
	"testing"
)

// Sentetik JBoss/Spring yığını: war// modül öneki + uygulama frame'leri +
// çerçeve frame'leri. Şekil gerçek kurulumdakiyle aynı, adlar uydurma.
const sampleStack = `com.example.core.exception.ExternalSystemException: Request not allowed for URI
	at deployment.APPWEB.war//com.example.payments.cashflow.adapter.SourceWebService.calcByAnalysis(SourceWebService.java:155)
	at deployment.APPWEB.war//com.example.payments.cashflow.adapter.SourceController.moneyInOut(SourceController.java:103)
	at java.base/java.lang.reflect.Method.invoke(Method.java:568)
	at org.springframework.web.method.support.InvocableHandlerMethod.doInvoke(InvocableHandlerMethod.java:255)`

func TestFromLog(t *testing.T) {
	cases := []struct {
		name     string
		attrs    map[string]string
		body     string
		wantKind string // "attr" | "body" | "none"
	}{
		{
			"OTel semconv anahtarı",
			map[string]string{"exception.stacktrace": sampleStack},
			"boş gövde", "attr",
		},
		{
			// PROD'UN ŞEKLİ: ES/ECS kurulumu.
			"ECS error.stack_trace",
			map[string]string{"error.stack_trace": sampleStack},
			"", "attr",
		},
		{
			"exception.stack_trace (alt çizgili yazım)",
			map[string]string{"exception.stack_trace": sampleStack},
			"", "attr",
		},
		{
			// EN YAYGIN JAVA DESENİ: ayrı alan yok, stack mesajın arkasında.
			"stack GÖVDEDE",
			map[string]string{"exception.type": "com.example.core.exception.ExternalSystemException"},
			"Hata oluştu\n" + sampleStack, "body",
		},
		{
			"öznitelik YOK ama gövdede stack var",
			nil,
			sampleStack, "body",
		},
		{
			// Tek satır tesadüf olabilir: düz bir cümle bir frame'den alıntı
			// yapıyor olabilir. İki bağımsız frame artık çağrı yığınıdır.
			"gövdede TEK frame — stack sayılmaz",
			nil,
			"şu satırdan geldi: at com.example.A.b(A.java:12)", "none",
		},
		{"gövde düz metin", nil, "sadece bir mesaj", "none"},
		{"her şey boş", nil, "", "none"},
		{"boş öznitelik değeri gövdeye düşer", map[string]string{"exception.stacktrace": "   "}, sampleStack, "body"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, fromBody := FromLog(c.attrs, c.body)
			switch c.wantKind {
			case "attr":
				if got != sampleStack || fromBody {
					t.Errorf("öznitelikten okunmalıydı: fromBody=%v, got=%q", fromBody, got)
				}
			case "body":
				if got != c.body || !fromBody {
					t.Errorf("gövdeden okunmalıydı: fromBody=%v, got=%q", fromBody, got)
				}
			case "none":
				if got != "" || fromBody {
					t.Errorf("stack bulunmamalıydı: got=%q fromBody=%v", got, fromBody)
				}
			}
		})
	}
}

// TestFromLogKeyPrecedence — bir kayıt iki yazımı birden taşırsa OTel
// semconv kazanır. Sıra sözleşmedir; bozulursa aynı kayıt iki kurulumda
// iki farklı stack verir.
func TestFromLogKeyPrecedence(t *testing.T) {
	got, _ := FromLog(map[string]string{
		"error.stack_trace":    "ECS sürümü\n\tat com.example.A.b(A.java:1)\n\tat com.example.C.d(C.java:2)",
		"exception.stacktrace": sampleStack,
	}, "")
	if got != sampleStack {
		t.Errorf("OTel semconv kazanmalıydı, ECS geldi: %q", got)
	}
}

// TestFromLogFeedsTheCodeFetcher — asıl kırılan buydu. Bulunan stack,
// kod çekicinin kullandığı ayrıştırıcıdan UYGULAMA frame'leri çıkarabilmeli;
// aksi hâlde "stack bulundu" demek boş bir zafer olurdu.
func TestFromLogFeedsTheCodeFetcher(t *testing.T) {
	for _, tc := range []struct {
		name  string
		attrs map[string]string
		body  string
	}{
		{"ECS alanından", map[string]string{"error.stack_trace": sampleStack}, ""},
		{"gövdeden", nil, "Hata oluştu\n" + sampleStack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stack, _ := FromLog(tc.attrs, tc.body)
			frames := ParseJava(stack)
			if len(frames) < 4 {
				t.Fatalf("%d frame çözüldü, en az 4 bekleniyordu", len(frames))
			}
			// war// öneki soyulmuş, dosya+satır taşınmış olmalı — kod
			// penceresi bunlar olmadan konumlandırılamaz.
			if frames[0].File != "SourceWebService.java" || frames[0].Line != 155 {
				t.Errorf("ilk frame dosya/satır taşımıyor: %+v", frames[0])
			}
			if strings.Contains(frames[0].Class, "war//") {
				t.Errorf("war// öneki soyulmamış: %q", frames[0].Class)
			}
		})
	}
}

func TestMessageHead(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"mesaj + frame'ler", "Hata oluştu\n" + sampleStack,
			"Hata oluştu\n" + strings.SplitN(sampleStack, "\n", 2)[0]},
		{"frame ilk satırda → mesaj yok", "\tat com.example.A.b(A.java:1)", ""},
		{"hiç frame yok → gövdenin tamamı", "sadece bir mesaj", "sadece bir mesaj"},
		{"boş", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MessageHead(c.in); got != c.want {
				t.Errorf("MessageHead(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}
}

// ── v0.10.59 — `throwable` ALANINDAKİ STACK ─────────────────────────────
//
// Operatör bildirdi: "Stacktrace'te hata var ama yok yazıyor."
//
// Kayıt stack'i `throwable` alanında taşıyordu (Log4j2 / Logback JSON
// encoder'larının yaygın yazımı) ve gövde yalnız `message`'ı tuttuğu için
// gövde taraması da devreye girmiyordu. Panel "bu kayıtta stacktrace yok"
// diyor, oysa stack operatörün ekranında GÖRÜNÜYORDU — kod çekici de
// sessizce atlanıyordu.
//
// ⚠ `throwable` GEVŞEK bir anahtar: bazı encoder'lar oraya yalnız
// exception'ın toString()'ini yazıyor (tek satır, frame yok). Onu koşulsuz
// stack saymak, bu dosyanın uyardığı riski gerçekleştirirdi. Bu yüzden
// gevşek anahtarlar gövdeyle AYNI çıtayı geçmek zorunda: en az iki frame.

func TestThrowableFieldCarriesStack(t *testing.T) {
	stack := "com.example.app.BusinessException: kural ihlali\n" +
		"\tat com.example.app.Rules.check(Rules.java:57)\n" +
		"\tat com.example.app.Service.handle(Service.java:23)\n" +
		"\tat java.base/java.lang.reflect.Method.invoke(Method.java:580)"

	got, fromBody := FromLog(map[string]string{"throwable": stack}, "kısa mesaj")
	if got == "" {
		t.Fatal("`throwable` alanındaki stack görülmedi — panel 'stacktrace yok' " +
			"der ve kod çekici sessizce atlanır (operatör-bildirimli)")
	}
	if fromBody {
		t.Error("stack GÖVDEDEN geldi diye işaretlendi; oysa öznitelikten geldi — " +
			"MessageHead gövdeyi boşuna kırpar")
	}
	if !strings.Contains(got, "Rules.java:57") {
		t.Errorf("stack eksik döndü: %q", got)
	}
}

// TestThrowableWithoutFramesIsNotAStack — GEVŞEK ANAHTARIN ÇITASI.
//
// Bazı encoder'lar `throwable`a yalnız exception'ın toString()'ini yazıyor.
// Onu "stack" saymak, kod çekiciyi frame'siz bir metinle çalıştırmak ve
// dosyanın uyardığı "stack sandığımız şey başka bir alanmış" riskini
// gerçekleştirmek olurdu.
func TestThrowableWithoutFramesIsNotAStack(t *testing.T) {
	for _, tc := range []struct{ name, val string }{
		{"yalnız toString", "com.example.app.BusinessException: kural ihlali"},
		{"tek frame — tesadüf olabilir", "hata\n\tat com.example.app.Rules.check(Rules.java:57)"},
		{"boş", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := FromLog(map[string]string{"throwable": tc.val}, ""); got != "" {
				t.Errorf("frame'siz `throwable` stack sayıldı: %q", got)
			}
		})
	}
}

// TestCanonicalKeyStillWinsOverThrowable — SIRA SÖZLEŞMESİ.
//
// Kayıt ikisini birden taşıyorsa OTel semconv kazanmalı: gevşek anahtarın
// eklenmesi mevcut sırayı BOZMAMALI.
func TestCanonicalKeyStillWinsOverThrowable(t *testing.T) {
	canonical := "X: a\n\tat p.C.m(C.java:1)\n\tat p.D.n(D.java:2)"
	loose := "Y: b\n\tat q.E.o(E.java:3)\n\tat q.F.p(F.java:4)"
	got, _ := FromLog(map[string]string{
		"exception.stacktrace": canonical,
		"throwable":            loose,
	}, "")
	if got != canonical {
		t.Errorf("gevşek anahtar kanoniki ezdi:\n%s", got)
	}
}

// TestNestedThrowablePathIsFound — İÇ İÇE JSON DÜZLEŞMESİ.
//
// ES yolu gövde JSON'ını düzleştiriyor: alan `message.throwable` /
// `mdc.throwable` gibi ÖNEKLİ gelebiliyor (aynı gözlem v0.8.466'da
// trace_id için yapılmıştı). Birebir eşleşme o kurulumları ıskalardı.
func TestNestedThrowablePathIsFound(t *testing.T) {
	stack := "E: x\n\tat p.A.a(A.java:1)\n\tat p.B.b(B.java:2)"
	for _, key := range []string{"throwable", "message.throwable", "mdc.throwable", "Throwable"} {
		t.Run(key, func(t *testing.T) {
			if got, _ := FromLog(map[string]string{key: stack}, ""); got == "" {
				t.Errorf("%q yolundaki stack görülmedi", key)
			}
		})
	}
	// Parça BİREBİR eşleşmeli: benzeyen bir ad stack sayılmamalı.
	if got, _ := FromLog(map[string]string{"throwable_count": stack}, ""); got != "" {
		t.Error("`throwable_count` stack sayıldı — sonek eşleşmesi fazla gevşek")
	}
}
