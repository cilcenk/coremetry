package reqid

import (
	"testing"
	"time"
)

// reqid_test.go — v0.9.1142 (yapılandırılmış request-ID → trace).
//
// Neden tablo ve neden HER segment sınırı: biçim sabit ofsetli, yani tek
// bir kayma sessizdir. Bir hane kaydığında "tarih" hâlâ 8 rakamdır ve
// makul görünür — yalnız pencere yanlış yere oturur ve özellik "hiç
// bulamıyor" diye görünür. Bu, v0.6.36 birim-karıştırma sınıfının aynı
// ailesi: değer+birim taşıyan her şablon her birimini test eder.
//
// TÜM DEĞERLER SENTETİK. Depoda gerçek fonksiyon kodu / müşteri numarası
// / kurum adı YOK (correlation_link.go doktrini).

// Sentetik kimlik parçaları — testler bunları birleştirerek kurar, çünkü
// tek bir 47 karakterlik dize okunduğunda hangi hanenin hangi segmente
// gittiği görünmüyor.
const (
	sFunc = "ABCD001"    // 7 alnum
	sChan = "059931"     // 6 rakam
	sSub  = "0513"       // 4 rakam
	sCust = "0000000042" // 10 rakam
	sDate = "20260817"   // 8 YYYYMMDD
	sTime = "093440812"  // 9 HHMMSSsss
	sSalt = "086"        // ≥3 rakam
)

func synth(fn, ch, sub, cust, date, tm, salt string) string {
	return fn + ch + sub + cust + date + tm + salt
}

func validID() string { return synth(sFunc, sChan, sSub, sCust, sDate, sTime, sSalt) }

func TestParseStructuredRequestID(t *testing.T) {
	loc := Location("")

	t.Run("segmentler ve gömülü zaman", func(t *testing.T) {
		id, ok := Parse(validID(), loc)
		if !ok {
			t.Fatalf("geçerli kimlik ayrıştırılamadı: %q", validID())
		}
		if id.FuncCode != sFunc || id.Channel != sChan || id.SubCode != sSub ||
			id.CustomerNo != sCust || id.Salt != sSalt {
			t.Fatalf("segment kayması: %+v", id)
		}
		want := time.Date(2026, 8, 17, 9, 34, 40, 812*int(time.Millisecond), loc)
		if !id.TS.Equal(want) {
			t.Fatalf("TS = %s, beklenen %s", FmtLocal(id.TS), FmtLocal(want))
		}
		if id.TS.Nanosecond() != 812*int(time.Millisecond) {
			t.Fatalf("milisaniye düştü: %d", id.TS.Nanosecond())
		}
		if id.Raw != validID() {
			t.Fatalf("Raw orijinal token olmalı: %q", id.Raw)
		}
	})

	t.Run("pencere TS ± 10dk", func(t *testing.T) {
		id, _ := Parse(validID(), loc)
		from, to := id.Window()
		if d := id.TS.Sub(from); d != SearchPad {
			t.Fatalf("alt kenar %s uzakta", d)
		}
		if d := to.Sub(id.TS); d != SearchPad {
			t.Fatalf("üst kenar %s uzakta", d)
		}
	})

	// GEÇERSİZ hâller — her satır tek bir kuralı zorluyor.
	bad := []struct {
		name  string
		token string
	}{
		{"boş", ""},
		{"kısa — sabit kısım tam ama salt yok",
			synth(sFunc, sChan, sSub, sCust, sDate, sTime, "")},
		{"kısa — salt 2 hane (taban 3)",
			synth(sFunc, sChan, sSub, sCust, sDate, sTime, "08")},
		{"uzun — salt tavanı aşıyor",
			synth(sFunc, sChan, sSub, sCust, sDate, sTime, "012345678901234567890")},
		{"bir hane eksik — tüm segmentler kayar",
			validID()[:len(validID())-1][1:]},
		{"fonksiyon kodunda ayırıcı",
			synth("ABCD-01", sChan, sSub, sCust, sDate, sTime, sSalt)},
		{"kanal kodunda harf (yalnız rakam)",
			synth(sFunc, "05993A", sSub, sCust, sDate, sTime, sSalt)},
		// "alt kodda harf" v0.9.1142'de geçersiz sayılıyordu — YANLIŞ
		// varsayımdı (operatör prod kimliği alfanümerik AltKod taşıyor,
		// v0.9.1144). Kabul tarafı TestParseAlnumSubCode'da.
		{"müşteri no'da harf", synth(sFunc, sChan, sSub, "00000000X2", sDate, sTime, sSalt)},
		{"salt kuyruğunda harf — alfanümerik kuyruk kabul edilmez",
			synth(sFunc, sChan, sSub, sCust, sDate, sTime, "08A")},
		{"yıl 2019 (2020 tabanı)", synth(sFunc, sChan, sSub, sCust, "20190817", sTime, sSalt)},
		{"yıl 2100 (2099 tavanı)", synth(sFunc, sChan, sSub, sCust, "21000817", sTime, sSalt)},
		{"ay 13", synth(sFunc, sChan, sSub, sCust, "20261317", sTime, sSalt)},
		{"ay 00", synth(sFunc, sChan, sSub, sCust, "20260017", sTime, sSalt)},
		{"gün 32", synth(sFunc, sChan, sSub, sCust, "20260832", sTime, sSalt)},
		{"gün 00", synth(sFunc, sChan, sSub, sCust, "20260800", sTime, sSalt)},
		{"31 Şubat — takvim tur-testi (time.Date normalize eder)",
			synth(sFunc, sChan, sSub, sCust, "20260231", sTime, sSalt)},
		{"29 Şubat artık olmayan yılda",
			synth(sFunc, sChan, sSub, sCust, "20270229", sTime, sSalt)},
		{"saat 25", synth(sFunc, sChan, sSub, sCust, sDate, "253440812", sSalt)},
		{"saat 24 (23 tavanı)", synth(sFunc, sChan, sSub, sCust, sDate, "243440812", sSalt)},
		{"dakika 60", synth(sFunc, sChan, sSub, sCust, sDate, "096040812", sSalt)},
		{"saniye 60", synth(sFunc, sChan, sSub, sCust, sDate, "093460812", sSalt)},
		{"32-hex trace id kimlik değil", "9fc37145182089354c2c20a1c63e0817"},
	}
	for _, c := range bad {
		t.Run("geçersiz: "+c.name, func(t *testing.T) {
			if id, ok := Parse(c.token, loc); ok {
				t.Fatalf("kabul edildi (%d karakter): %+v", len(c.token), id)
			}
		})
	}

	// SINIR DEĞERLERİ — kabul edilmesi gerekenler.
	good := []struct {
		name  string
		token string
	}{
		{"gece yarısı 00:00:00.000", synth(sFunc, sChan, sSub, sCust, sDate, "000000000", sSalt)},
		{"gün sonu 23:59:59.999", synth(sFunc, sChan, sSub, sCust, sDate, "235959999", sSalt)},
		{"yıl tabanı 2020", synth(sFunc, sChan, sSub, sCust, "20200101", sTime, sSalt)},
		{"yıl tavanı 2099", synth(sFunc, sChan, sSub, sCust, "20991231", sTime, sSalt)},
		{"29 Şubat artık yılda", synth(sFunc, sChan, sSub, sCust, "20280229", sTime, sSalt)},
		{"fonksiyon kodu tamamı rakam", synth("0123456", sChan, sSub, sCust, sDate, sTime, sSalt)},
		{"fonksiyon kodu küçük harf", synth("abcd001", sChan, sSub, sCust, sDate, sTime, sSalt)},
		{"salt tavanında (20 hane)",
			synth(sFunc, sChan, sSub, sCust, sDate, sTime, "01234567890123456789")},
	}
	for _, c := range good {
		t.Run("geçerli: "+c.name, func(t *testing.T) {
			if _, ok := Parse(c.token, loc); !ok {
				t.Fatalf("reddedildi (%d karakter): %q", len(c.token), c.token)
			}
		})
	}
}

// Saat dilimi: gömülü zaman YEREL banka saati. Bu testin işi, sessiz bir
// UTC'ye düşüşü yakalamak — ±10dk pencerede 3 saatlik kayma garantili
// ıskadır.
func TestLocationAndLocalReading(t *testing.T) {
	loc := Location("")
	if loc == nil {
		t.Fatal("Location nil döndü")
	}
	id, ok := Parse(validID(), loc)
	if !ok {
		t.Fatal("geçerli kimlik ayrıştırılamadı")
	}
	// Yerel okuma kimliğin yazdığı saati vermeli.
	if got := id.TS.Format("15:04:05.000"); got != "09:34:40.812" {
		t.Fatalf("yerel saat %s — kimliğin yazdığı saat değil", got)
	}
	// Türkiye 2016'dan beri +03:00; ofset UTC'ye düşerse bu kırılır.
	_, off := id.TS.Zone()
	if off != fallbackOffsetSec {
		t.Fatalf("ofset %d sn — beklenen %d (yerel banka saati)", off, fallbackOffsetSec)
	}
	// Aynı token UTC olarak okunursa damga 3 saat kayar: testin
	// koruduğu fark tam olarak bu.
	utcID, _ := Parse(validID(), time.UTC)
	if d := utcID.TS.Sub(id.TS); d != 3*time.Hour {
		t.Fatalf("tz farkı %s — pencere kayması bu testin konusu", d)
	}
	// Tanınmayan ad varsayılana düşer (UTC'ye DEĞİL).
	if _, off2 := time.Now().In(Location("Mars/Olympus")).Zone(); off2 == 0 {
		t.Fatal("geçersiz tz adı UTC'ye düştü — sessiz 3 saat kayma")
	}
	// Açık bir başka tz onurlandırılır.
	if _, off3 := time.Now().In(Location("UTC")).Zone(); off3 != 0 {
		t.Fatalf("açık UTC ayarı yok sayıldı (ofset %d)", off3)
	}
}

func TestFindToken(t *testing.T) {
	id := validID()
	cases := []struct {
		name string
		text string
		want string
	}{
		{"çıplak yapıştırma", id, id},
		{"soru içinde", "şu isteğe ne oldu " + id + " bir bakar mısın?", id},
		{"iki nokta ve yeni satır", "Request ID:\n" + id + "\n", id},
		{"URL parametresi içinde", "https://log/x?requestId=" + id + "&t=1", id},
		{"büyük/küçük harf korunur",
			"id " + synth("abcd001", sChan, sSub, sCust, sDate, sTime, sSalt),
			synth("abcd001", sChan, sSub, sCust, sDate, sTime, sSalt)},
		{"yapışık kuyruk KABUL EDİLMEZ (maksimal blok)", id + "9999999999999999999999", ""},
		{"trace id yakalanmaz", "trace 9fc37145182089354c2c20a1c63e0817 neden yavaş", ""},
		{"uzun rakam bloğu kimlik sanılmaz",
			"tutar 123456789012345678901234567890123456789012345678901234567890", ""},
		{"tireli varyant biçim değil",
			"ABCD001-059931-0513-0000000042-20260817-093440812-086", ""},
		{"metin yok", "", ""},
		{"guided sinyali olmayan düz cümle", "checkout servisinde hata var mı?", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := FindToken(c.text)
			if c.want == "" {
				if ok {
					t.Fatalf("yanlış pozitif: %q", got)
				}
				return
			}
			if !ok || got != c.want {
				t.Fatalf("token = %q (ok=%v), beklenen %q", got, ok, c.want)
			}
		})
	}
}

func TestFind(t *testing.T) {
	loc := Location("")
	got, ok := Find("Request ID: "+validID()+" nedir?", loc)
	if !ok {
		t.Fatal("metinden çözümleme başarısız")
	}
	if got.Raw != validID() || got.TS.Hour() != 9 {
		t.Fatalf("çözümleme yanlış: %+v", got)
	}
	if _, ok := Find("hiç kimlik yok", loc); ok {
		t.Fatal("kimliksiz metinde çözümleme üretildi")
	}
}

// v0.9.1144 — operator-reported: prod kimliğinde AltKod alfanümerik
// çıktı ("kY1d" benzeri) ve rakam-şartlı ilk parser'da mesaj RAG
// doküman katmanına düşüyordu. AltKod gevşedi; kalan rakam segmentleri
// GEVŞEMEDİ — bu test ikisini birden pinler.
func TestParseAlnumSubCode(t *testing.T) {
	id, ok := Parse(synth(sFunc, sChan, "kX2d", sCust, sDate, sTime, sSalt), time.UTC)
	if !ok {
		t.Fatal("alfanümerik AltKod parse edilmeli")
	}
	if id.SubCode != "kX2d" {
		t.Fatalf("SubCode = %q (harf kasası korunmalı)", id.SubCode)
	}
	for name, tok := range map[string]string{
		"kanalda harf":   synth(sFunc, "05a931", sSub, sCust, sDate, sTime, sSalt),
		"müşteride harf": synth(sFunc, sChan, sSub, "00000000x2", sDate, sTime, sSalt),
		"saltta harf":    synth(sFunc, sChan, sSub, sCust, sDate, sTime, "08z"),
	} {
		if _, ok := Parse(tok, time.UTC); ok {
			t.Fatalf("%s: parse edilmemeliydi", name)
		}
	}
}

// v0.9.1144 — FindLooseToken: kimliğe benzeyen ama Parse geçmeyen token
// yönlendirme sinyali sayılır (RAG'a düşmesin); rakam tabanı (33) ve
// uzunluk aralığı yanlış pozitifleri eler.
func TestFindLooseToken(t *testing.T) {
	badMonth := synth(sFunc, sChan, sSub, sCust, "20261317", sTime, sSalt)
	if _, ok := Parse(badMonth, time.UTC); ok {
		t.Fatal("fikstür bozuk: ay=13 parse edilmemeli")
	}
	if tok, ok := FindLooseToken("şu isteğe ne oldu: " + badMonth + " ?"); !ok || tok != badMonth {
		t.Fatalf("gevşek eş bulunmalıydı: %q %v", tok, ok)
	}
	letterHeavy := "AAAAAAAAAAAAAAAAAAAA" + "111111111111111111111111111" // 20 harf + 27 rakam = 47
	for name, text := range map[string]string{
		"harf ağırlıklı 47'lik": letterHeavy,
		"kısa rakam dizisi":     "hata kodu 12345 geldi",
		"32-hex trace":          "4bf92f3577b34da6a3ce929d0e0e4736",
		"düz cümle":             "bugün deploy oldu mu",
	} {
		if tok, ok := FindLooseToken(text); ok {
			t.Fatalf("%s: yanlış pozitif %q", name, tok)
		}
	}
}
