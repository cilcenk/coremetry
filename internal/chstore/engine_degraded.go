package chstore

import "fmt"

// engine_degraded.go — "hata sakin bir veritabanı gibi görünüyordu"
// (v0.10.11).
//
// Dört motor okuyucusu (oracle · postgres · mysql · redis) alt-sorgu
// hatalarını `if … err == nil` kalıbıyla YUTUYOR ve sonunda KOŞULSUZ
// `return out, nil` yapıyor. Bir ClickHouse hatasında olan şey:
//
//   • fonksiyon hata DÖNDÜRMÜYOR → uç HTTP 200
//   • `Status` "up" olarak işaretlenmiyor
//   • bütün metrik alanları SIFIR kalıyor
//
// Ekrana çizilen şey eksik bir panel değil, sıfırlardan oluşan TAM ve
// İNANDIRICI bir KPI ızgarası. Yani katlama "boş → hata gibi" değil,
// tersi ve daha kötüsü: **hata → sakin, boştaki bir veritabanı gibi.**
// Operatör paged olduğunda bakacağı ilk göstergenin yanlış yönde
// yalan söylediği hâl.
//
// Denetim (§1.3) bu mekanizmayı TERS tarif etmişti ("veri yok → null,
// hata → null, ikisi katlanmış") ve düzeltmeyi "yalnız frontend" diye
// boyutlandırmıştı. O reçete uygulansaydı ISIRMAZDI — çünkü backend
// hiç null döndürmüyor, sıfır döndürüyor.
//
// TASARIM: kısmi veri KORUNUYOR. Bir alt-sorgunun düşmesi diğer altısını
// çöpe atmaz; okuyucu ne topladıysa onu döndürür ve YANINDA neyin
// düştüğünü söyler. Alternatif (ilk hatada tüm çağrıyı düşürmek) bugünkü
// sessizliği gürültülü bir boşlukla değiştirirdi — operatör yine
// göremezdi.
//
// ANAHTAR SÖZLEŞME: `Degraded` true iken hiçbir sayı GÜVENİLİR değildir.
// Frontend bu bayrağı görünce sıfır ızgarası çizmemeli.

// EngineHealth — dört motor yapısının paylaştığı bozulma alanları.
//
// Gömülü struct: dört yapıya ayrı ayrı iki alan eklemek, dördünün
// ıraksaması için davetiye olurdu (bu oturumun tekrar eden dersi).
type EngineHealth struct {
	// Degraded — bu okuma sırasında EN AZ BİR alt-sorgu düştü.
	// True ise sayılar EKSİK; sıfırlar "veri yok" DEĞİL "bilmiyoruz".
	Degraded bool `json:"degraded,omitempty"`
	// DegradedReason — operatöre gösterilecek tek satır. Hangi
	// okumaların düştüğünü söyler; SQL ya da bağlantı ayrıntısı TAŞIMAZ
	// (o ayrıntı loga gider, ekrana değil).
	DegradedReason string `json:"degradedReason,omitempty"`
}

// degradeTracker — bir okuma boyunca düşen alt-sorguları toplar.
//
// Saf ve test edilebilir: dört motorun hiçbirine bağlı değil, CH
// gerektirmiyor. Kusurun kendisi hata vermediği için, düzeltmenin
// çekirdeği de hata vermeden sınanabilmeli.
type degradeTracker struct {
	failed []string
	total  int
}

// step — bir alt-sorgunun sonucunu kaydeder. `err != nil` ise `name`
// düşenler listesine girer.
//
// Her çağrı `total`ı artırıyor, çünkü "7 okumadan 3'ü düştü" cümlesi
// "3 okuma düştü"den fazlasını söylüyor: oran, kalan verinin ne kadar
// temsil ettiğini anlatıyor.
func (d *degradeTracker) step(name string, err error) bool {
	d.total++
	if err != nil {
		d.failed = append(d.failed, name)
		return false
	}
	return true
}

// reason — boş dizge = bozulma yok.
func (d *degradeTracker) reason() string {
	if len(d.failed) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d metrik okuması başarısız (%s) — gösterilen sayılar EKSİK",
		len(d.failed), d.total, joinUpTo(d.failed, 3))
}

// health — gömülecek hâli.
func (d *degradeTracker) health() EngineHealth {
	r := d.reason()
	return EngineHealth{Degraded: r != "", DegradedReason: r}
}

// joinUpTo — ilk n adı listeler, kalanı sayar.
//
// Sınır var çünkü bu dizge bir arayüz şeridine giriyor: on beş okuma
// adını yan yana basmak, operatörün okumayacağı bir duvar olurdu.
func joinUpTo(names []string, n int) string {
	if len(names) <= n {
		return joinComma(names)
	}
	return fmt.Sprintf("%s +%d", joinComma(names[:n]), len(names)-n)
}

func joinComma(names []string) string {
	out := ""
	for i, s := range names {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
