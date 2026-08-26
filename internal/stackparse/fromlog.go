package stackparse

// FromLog — bir log kaydındaki stacktrace'i BULUR (v0.9.1182).
//
// Operatör bildirimi: "Kodu da incele" işaretliyken cevap "Kod okunamadı —
// kodsuz analiz (bu kayıtta stacktrace yok)" diyordu, oysa kayıtta
// stacktrace VARDI ve modelin kendi "Stacktrace Detayı" bölümü sınıf +
// exception tipi adlandırıyordu (yalnız metot adını "tam görünmüyor" diye
// not düşerek — kesilmiş bir metinden okuduğunun itirafı).
//
// Kök neden: tüm okuma yolları TEK yazıma bakıyordu, `exception.stacktrace`.
// O anahtar boşsa stack "yok" sayılıyor ve kod çekici hiç çalışmıyordu.
// Gerçekte iki başka yer daha var ve ikisi de bu kurulumda gerçek:
//
//   - ECS. Prod'un log deposu Elasticsearch ve ECS'in alanı
//     `error.stack_trace`. OTel semconv'un `exception.stacktrace`ini
//     bekleyen bir okuyucu, ECS-şekilli bir kurulumda HİÇBİR ZAMAN stack
//     bulamaz — ve bu sessizce olur.
//   - GÖVDE. Java'nın en yaygın logback/log4j deseni exception'ı mesajın
//     ARKASINA basar; ayrı bir alan hiç doğmaz. Modelin metot adını yarım
//     görmesinin sebebi buydu: stack, gövde bütçesinin (600 bayt)
//     içindeydi ve orada kesiliyordu.
//
// Dönen `fromBody`, çağıranın gövdeyi PROMPT'A İKİ KEZ koymamasını sağlar:
// stack gövdeden geldiyse gövdenin yalnız mesaj başı yeter, frame'ler zaten
// stack alanında ve orada daha büyük bir bütçeyle duruyor.

import "strings"

// logStackKeys — denenecek öznitelik yazımları, en kanoniden en gevşeğe.
//
// Sıra sözleşmedir: bir kayıt ikisini birden taşıyorsa OTel semconv kazanır.
// Liste bilinçli KISA — her ek anahtar, bir gün "stack sandığımız şey aslında
// başka bir alanmış" riskini büyütür. Yeni bir yazım eklemek için o yazımı
// üreten gerçek bir kurulum gerekir, olasılık değil.
var logStackKeys = []string{
	"exception.stacktrace",  // OTel semconv (kanonik)
	"exception.stack_trace", // OTel'in eski/yanlış yazımı, bazı SDK'lar
	"error.stack_trace",     // ECS — Elasticsearch kurulumlarının alanı
	"error.stacktrace",
	"stack_trace",
	"stacktrace",
}

// looseLogStackKeys — GEVŞEK yazımlar: alan adı "burası stacktrace" demiyor,
// yalnız "burada bir throwable var" diyor.
//
// v0.10.59, operatör-bildirimli: "Stacktrace'te hata var ama yok yazıyor."
// Kayıt, stack'i `throwable` alanında taşıyordu (Log4j2/Logback JSON
// encoder'larının yaygın yazımı) ve gövde yalnız `message`'ı tuttuğu için
// gövde taraması da devreye girmiyordu — panel "bu kayıtta stacktrace yok"
// diyor, oysa stack ekranda görünüyordu.
//
// Dosyanın kendi kuralı yeni anahtar için "o yazımı üreten GERÇEK bir
// kurulum" istiyordu; bu tam olarak öyle bir kurulum.
//
// ⚠ AMA KANONİK LİSTEYE KOYULMUYOR. `throwable` bir exception'ın
// TOPLAMINI tutabilir: bazı encoder'lar oraya yalnız `toString()` yazıyor
// (tek satır mesaj, frame yok). Onu koşulsuz "stack" saymak, dosyanın
// uyardığı riski gerçekleştirirdi — "stack sandığımız şey aslında başka bir
// alanmış". Bu yüzden gevşek anahtarlar GÖVDEYLE AYNI çıtayı geçmek zorunda:
// en az bodyStackMinFrames adet gerçek frame.
var looseLogStackKeys = []string{
	"throwable",
	"throwable.stack_trace",
}

// bodyStackMinFrames — gövdeyi "stacktrace" saymak için gereken en az frame.
//
// 2, çünkü 1 tek satır tesadüf olabilir: düz bir log cümlesi içinde
// "at com.x.Y.z(Y.java:12)" biçimli bir metin geçebilir (örneğin bir hata
// mesajının kendisi bir frame'den alıntı yapıyordur). İki bağımsız frame
// artık tesadüf değil, çağrı yığınıdır.
const bodyStackMinFrames = 2

// FromLog, log özniteliklerinde ve gerekirse gövdede stacktrace arar.
// Bulamazsa ("", false) döner — çağıran bugünkü "stacktrace yok" davranışını
// aynen sürdürür.
func FromLog(attrs map[string]string, body string) (stack string, fromBody bool) {
	for _, k := range logStackKeys {
		if v, ok := attrs[k]; ok && strings.TrimSpace(v) != "" {
			return v, false
		}
	}
	// Gevşek anahtarlar: yalnız GERÇEKTEN frame taşıyorlarsa kabul.
	//
	// Eşleşme SONEK üzerinden: bu depoda ES yolu iç içe JSON gövdesini
	// düzleştiriyor ve alanlar `message.throwable` / `mdc.throwable` gibi
	// önekli path'lerle geliyor (aynı gözlem v0.8.466'da trace_id için
	// yapılmıştı: "gövde JSON'ında / alışılmadık bir path'te"). Birebir
	// eşleşme o kurulumları ıskalardı.
	//
	// Sonek eşleşmesi burada GÜVENLİ, çünkü değer yine iki-frame çıtasını
	// geçmek zorunda: adı ne olursa olsun, frame taşımayan bir alan stack
	// sayılmıyor.
	if len(attrs) > 0 {
		for k, v := range attrs {
			if !looseStackKeyMatch(k) {
				continue
			}
			v = strings.TrimSpace(v)
			if v != "" && len(ParseJava(v)) >= bodyStackMinFrames {
				return v, false
			}
		}
	}
	if strings.TrimSpace(body) == "" {
		return "", false
	}
	// "Gövde bir stack mi" sorusunun tanımı, kod çekicinin kullandığı
	// tanımın AYNISI: ParseJava frame görüyorsa frame vardır. Ayrı bir
	// sezgisel (ör. "\n\tat " araması) iki yerde iki farklı "stack" tanımı
	// üretir ve biri diğerini yakalayamaz.
	if len(ParseJava(body)) >= bodyStackMinFrames {
		return body, true
	}
	return "", false
}

// MessageHead — gövdeden İLK frame'e kadarki kısım (stack gövdeden
// geldiğinde prompt'a giren "mesaj" yarısı).
//
// Frame'leri kesip atmak değil, mesajı AYIRMAK: frame'ler zaten stack
// alanında ve orada daha büyük bir bütçeyle duruyor; ikisini birden
// göndermek aynı baytları iki kez ödemek olurdu.
func MessageHead(body string) string {
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if _, ok := parseJavaLine(ln); ok {
			if i == 0 {
				return ""
			}
			return strings.TrimRight(strings.Join(lines[:i], "\n"), " \t\r\n")
		}
	}
	return body
}

// looseStackKeyMatch — anahtarın SON nokta-parçası gevşek listede mi.
//
// `throwable`, `message.throwable`, `mdc.throwable` → true.
// `throwable_count` → false (parça birebir eşleşmeli).
func looseStackKeyMatch(key string) bool {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		key = key[i+1:]
	}
	for _, k := range looseLogStackKeys {
		lk := k
		if i := strings.LastIndexByte(lk, '.'); i >= 0 {
			lk = lk[i+1:]
		}
		if strings.EqualFold(key, lk) {
			return true
		}
	}
	return false
}
