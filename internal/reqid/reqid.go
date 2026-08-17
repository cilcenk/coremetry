// Package reqid parses the operator's STRUCTURED request identifier and
// turns it into a bounded log-search window (v0.9.1142).
//
// Operatör isteği: kurumsal sistemlerde her istek TEK bir string ile
// izleniyor ve o string sabit yapıda — içinde işlemin TARİHİ ve SAATİ
// milisaniyesine kadar yazılı. Sohbete böyle bir kimlik yapıştırıldığında
// aramanın filo geneline yayılması gereksiz: kimliğin kendisi pencereyi
// söylüyor. Zincir şu: kimlik → gömülü zaman → o pencerede log araması →
// eşleşen kaydın trace_id'si → trace anlatısı.
//
// GÜVENLİK: bu dosya (ve testleri) HİÇBİR gerçek kurum/müşteri değeri
// taşımaz. Örnekler tamamen sentetik ("ABCD001", müşteri "0000000042") —
// depo bir müşteri kimliği ya da kurum adı taşımaz (correlation_link.go
// ile aynı doktrin).
//
// NEDEN AYRI PAKET: iki tüketici var ve ikisi ayrı paketlerde —
// internal/api (sohbet hızlı yolu + log köprüsü çipi) ve
// internal/mcptools (find_trace_by_request_id). api → mcptools import'u
// zaten var, tersi yok; ortak yeri ikisinin de import edebildiği bu saf
// paket. İkinci bir tespit/parse kopyası YOK.
package reqid

import (
	"strings"
	"time"
)

// Segment ofsetleri — biçim SABİT genişlikli, ayırıcı yok:
//
//	[FonksiyonKodu 7 alnum][Kanal 6 rakam][AltKod 4 rakam]
//	[MüşteriNo 10 rakam][Tarih 8 YYYYMMDD][Zaman 9 HHMMSSsss]
//	[Sequence/Salt: değişken uzunlukta rakam kuyruğu]
//
// Ofsetler tek yerde: bir segment kaydığında testler (reqid_test.go)
// SINIRLARI ayrı ayrı zorluyor.
const (
	offFunc     = 0
	offChannel  = 7
	offSubCode  = 13
	offCustomer = 17
	offDate     = 27
	offTime     = 35
	offSalt     = 44 // sabit kısmın toplam uzunluğu
)

const (
	// MinSaltLen — kuyruktaki en az rakam sayısı. 44 sabit + 3 = 47.
	//
	// Neden bir taban var: 44 karakterlik "salt'sız" bir dizi bu biçimde
	// gözlenmedi ve tabansız kabul, rastgele bir 44-hane sayının kimlik
	// sanılmasına kapı açardı.
	MinSaltLen = 3
	// MaxSaltLen — kuyruğun üst sınırı. Kimliğin kendisi bir uzunluk
	// beyanı taşımıyor, yani teorik olarak 200 haneli bir sayı da
	// "kimlik gibi" görünür. Sınır bir SEZGİ ve bilinçli: ofset 27'de
	// tesadüfen geçerli bir tarih taşıyan uzun bir rakam bloğu
	// (dosya boyutu, tutar, epoch dizisi) kimlik sanılmasın.
	MaxSaltLen = 20
	// MinLen / MaxLen — türetilmiş sınırlar.
	MinLen = offSalt + MinSaltLen
	MaxLen = offSalt + MaxSaltLen
)

// DefaultTZ — gömülü zamanın YEREL saat dilimi.
//
// Kimlik UTC taşımıyor: kurumun kendi saatiyle damgalanıyor. Bu, ±10
// dakikalık bir pencerede HAYATİ bir ayrım — üç saatlik bir tz hatası
// pencereyi tamamen ıskalar ve özellik "hiç bulamıyor" diye görünür.
const DefaultTZ = "Europe/Istanbul"

// fallbackOffsetSec — tzdata yoksa kullanılan SABİT ofset (+03:00).
//
// Runtime imajı tzdata kuruyor (Dockerfile), ama LoadLocation'ın
// başarısız olduğu bir kurulumda UTC'ye düşmek SESSİZ bir 3 saat kaymadır
// ve ±10dk pencerede garantili ıskadır. Sabit ofset bugünün Türkiye'si
// için doğru (2016'dan beri DST yok) ve hatayı sıfıra indirir.
const fallbackOffsetSec = 3 * 3600

// SearchPad — kimliğin damgası etrafındaki arama yarı-genişliği.
//
// ±10dk: kayıt zamanı ile log satırının damgası arasındaki fark
// (uygulama içi gecikme, ingest lag, hafif saat kayması) bu bandın
// içinde. Tz belirsizliğini pencereyi GENİŞLETEREK çözmek yanlış cevap
// olurdu — locu doğru kullanıp pencereyi dar tutuyoruz, çünkü pencere
// log arama maliyetinin TEK sınırı (ES 10B doc/gün).
const SearchPad = 10 * time.Minute

// ID — çözümlenmiş kimlik. Raw, aramada kullanılan ORİJİNAL token'dır
// (büyük/küçük harf korunur: ES keyword alanlarında eşleşme harfe
// duyarlı olabilir).
type ID struct {
	Raw        string
	FuncCode   string
	Channel    string
	SubCode    string
	CustomerNo string
	Salt       string
	// TS — gömülü tarih+saat, Location'da (yerel banka saati).
	TS time.Time
}

// Window — arama penceresi (TS ± SearchPad).
func (id ID) Window() (from, to time.Time) {
	return id.TS.Add(-SearchPad), id.TS.Add(SearchPad)
}

// Location — ayar adından saat dilimi. Boş ad = DefaultTZ; yüklenemeyen
// ad da DefaultTZ'ye düşer (yanlış bir ad yüzünden UTC'ye düşmek sessiz
// ıska demek). tzdata hiç yoksa sabit +03:00.
func Location(name string) *time.Location {
	name = strings.TrimSpace(name)
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation(DefaultTZ); err == nil {
		return loc
	}
	return time.FixedZone("+03", fallbackOffsetSec)
}

// Parse — token → ID. SAF, tablo testli.
//
// ok=false her zaman "bu bir kimlik DEĞİL" demek: uzunluk, segment
// karakter sınıfı ya da takvim/saat makullüğü tutmuyor. Çağıran bunu
// ŞEKİL hatası olarak raporlar (bulunamama ile karıştırmadan).
func Parse(token string, loc *time.Location) (ID, bool) {
	if loc == nil {
		loc = Location("")
	}
	if len(token) < MinLen || len(token) > MaxLen {
		return ID{}, false
	}
	fn := token[offFunc:offChannel]
	if !allAlnum(fn) {
		return ID{}, false
	}
	// Fonksiyon kodu dışındaki HER segment yalnız rakam.
	rest := token[offChannel:]
	if !allDigits(rest) {
		return ID{}, false
	}
	date := token[offDate:offTime]
	tm := token[offTime:offSalt]
	year := atoi(date[0:4])
	month := atoi(date[4:6])
	day := atoi(date[6:8])
	if year < 2020 || year > 2099 || month < 1 || month > 12 || day < 1 || day > 31 {
		return ID{}, false
	}
	hh := atoi(tm[0:2])
	mm := atoi(tm[2:4])
	ss := atoi(tm[4:6])
	ms := atoi(tm[6:9])
	if hh > 23 || mm > 59 || ss > 59 {
		return ID{}, false
	}
	ts := time.Date(year, time.Month(month), day, hh, mm, ss, ms*int(time.Millisecond), loc)
	// Takvim tur-testi: time.Date 31 Şubat'ı 3 Mart'a NORMALİZE eder,
	// hata döndürmez. Gün/ay geri okunmuyorsa tarih geçersizdi.
	if ts.Year() != year || int(ts.Month()) != month || ts.Day() != day {
		return ID{}, false
	}
	return ID{
		Raw:        token,
		FuncCode:   fn,
		Channel:    token[offChannel:offSubCode],
		SubCode:    token[offSubCode:offCustomer],
		CustomerNo: token[offCustomer:offDate],
		Salt:       token[offSalt:],
		TS:         ts,
	}, true
}

// FindToken — metindeki İLK yapılandırılmış kimlik token'ı (orijinal
// harf kasası korunur). tz'siz: segment/takvim makullüğü saat dilimi
// gerektirmiyor, dolayısıyla saf router (routeGuidedIntent) ayar
// okumadan karar verebilir.
//
// ANAHTAR KELİME ÇAPASI YOK ve bu bilinçli bir SAPMA: request_id_links.go
// genel bir "uzun token yakala" regex'ini yanlış pozitif kusacağı için
// reddediyor (trace id'ler, sürüm damgaları, pod adları). Burada çapa
// gerekmiyor çünkü şeklin KENDİSİ doğrulanıyor — 47-64 karakterlik,
// sabit ofsetlerinde geçerli bir takvim tarihi + saat taşıyan bir dizi
// tesadüfen oluşmaz.
//
// Tarama MAKSİMAL alnum blokları üzerinde: kimliğin bir ucundan kırpılmış
// alt dizi eşleşmesi olmuyor ("…086ve" biçiminde yapışık bir kuyruk
// kabul EDİLMEZ), 100 haneli bir rakam bloğu da kimlik sanılmıyor.
func FindToken(text string) (string, bool) {
	n := len(text)
	for i := 0; i < n; {
		if !isAlnumByte(text[i]) {
			i++
			continue
		}
		j := i
		for j < n && isAlnumByte(text[j]) {
			j++
		}
		if run := text[i:j]; len(run) >= MinLen && len(run) <= MaxLen {
			if _, ok := Parse(run, time.UTC); ok {
				return run, true
			}
		}
		i = j
	}
	return "", false
}

// Find — FindToken + Parse (loc ile). Metinden doğrudan çözümlenmiş
// kimlik isteyen çağıranlar (MCP tool'u, köprü çipi) bunu kullanır.
func Find(text string, loc *time.Location) (ID, bool) {
	tok, ok := FindToken(text)
	if !ok {
		return ID{}, false
	}
	return Parse(tok, loc)
}

func isAlnumByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func allAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isAlnumByte(s[i]) {
			return false
		}
	}
	return len(s) > 0
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// atoi — yalnız rakam olduğu DOĞRULANMIŞ dizeler için; strconv'un hata
// yolu bu çağrı yerlerinde ölü kod olurdu.
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}
