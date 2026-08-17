// v0.9.823 regresyon testi — /databases okumalarında ÜST KOVA SINIRI.
//
// Bug: GetDatabases (satır listesi + üst çağıranlar) ve GetDBTrends
// (satır başına sparkline) `WHERE time_bucket >= ? AND time_bucket <= ?`
// diyordu. MV kovaları BAŞLANGIÇLARIYLA etiketli, yani `<= to` ile
// başlangıcı tam `to` olan kova da sonuca giriyor — oysa o kova
// [to, to+5dk) aralığını kapsıyor, İSTENEN PENCERENİN TAMAMEN DIŞINDA.
//
// Zarar iki yüzeyde farklı görünüyordu: satır toplamlarına beş
// dakikalık fazladan trafik biniyordu (span/error sayıları, dolayısıyla
// hata oranı), sparkline'ın sonuna ise pencereye ait olmayan bir nokta
// ekleniyordu. Fark YALNIZ `to` bir kova sınırına tam otururken
// görünür; hizalanmamış bir `to`'da iki operatör aynı sonucu verir —
// bu yüzden gözden kaçtı ve "bazen bir kova fazla" gibi davrandı.
//
// Doğru sınır repoda zaten vardı: GetDatabasesSeries (v0.9.820) `< ?`
// kullanıyordu ve emsal oydu. Bu test sözleşmenin kardeş okumalardan da
// kaybolmamasını sağlıyor.
//
// v0.9.834 — o emsal dosya SİLİNDİ (/api/databases/series ve okuyucusu
// operatör kararıyla kaldırıldı), dolayısıyla kaynağını okuyan ikinci
// test de gitti. KAPSAM KAYBI YOK: aşağıdaki davranış tablosunun "tam
// to'daki kova DIŞARIDA" satırı üç okumanın da `<` kullanmasını zaten
// ZORUNLU kılıyor — `<=`'ye dönen bir okuma o satırda kırmızıya düşer.
// Silinen test o zorunluluğu ikinci kez, artık var olmayan bir dosyaya
// bakarak söylüyordu.
package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// bucketPredRe — SQL'deki kova penceresi yüklemi. Üst sınır operatörünü
// (`<` ya da `<=`) yakalar; test tablosu bu operatörle SÜRÜLÜR, yani
// sınır kaynakta geri alınırsa davranış tablosu kırmızıya döner.
var bucketPredRe = regexp.MustCompile(`time_bucket\s*(<=?)\s*\?`)

// upperBoundOp — verilen SQL parçasındaki TEK üst-sınır operatörü.
// Birden fazla eşleşme = bölgeye yeni bir kova sorgusu girmiş ve
// sınırı denetlenmiyor demektir; sessizce geçmesin diye hata.
func upperBoundOp(t *testing.T, name, sql string) string {
	t.Helper()
	m := bucketPredRe.FindAllStringSubmatch(sql, -1)
	if len(m) != 1 {
		t.Fatalf("%s: tam olarak 1 kova yüklemi bekleniyordu, %d bulundu — "+
			"yeni bir sorgu eklendiyse sınırı bu teste de bağla", name, len(m))
	}
	if !strings.Contains(sql, "time_bucket >= ?") {
		t.Fatalf("%s: alt sınır `>= ?` yok — hizalanmış from kaybolmuş", name)
	}
	return m[0][1]
}

// funcSource — bir dosyadan tek fonksiyonun gövdesi. Kaynak metnine
// bakan testlerin (bkz. TestSummaryReadersAlign, v0.9.555) deseni:
// yüklem SQL dizesinin içinde yaşıyor, çalıştırılabilir bir Go
// ifadesinde değil.
func funcSource(t *testing.T, file, sig string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", file, err)
	}
	src := string(b)
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%s içinde %q bulunamadı — imza değiştiyse testi güncelle", file, sig)
	}
	rest := src[i+len(sig):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// admitsBucket — bir kova etiketinin pencereye alınıp alınmadığı.
// SQL yükleminin Go karşılığı: alt sınır DAHİL, üst sınır operatöre
// göre. `op` KAYNAKTAN geliyor.
func admitsBucket(op string, bucket, from, to time.Time) bool {
	if bucket.Before(from) {
		return false
	}
	switch op {
	case "<":
		return bucket.Before(to)
	case "<=":
		return !bucket.After(to)
	}
	return false
}

// TestDBReadsExcludeUpperBucket — DAVRANIŞ testi. Üç okumanın da
// kaynaktaki operatörünü çıkarıp aynı kova tablosundan geçiriyor.
// Kritik satır `to` etiketli kova: PENCEREYE AİT DEĞİL.
func TestDBReadsExcludeUpperBucket(t *testing.T) {
	// 10:00–11:00 penceresi, 5 dakikalık MV ızgarası.
	from := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 8, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{
			// [09:55, 10:00) — pencere başlamadan bitiyor.
			"pencereden ÖNCEKİ kova dışarıda",
			time.Date(2026, 8, 8, 9, 55, 0, 0, time.UTC),
			false,
		},
		{
			// [10:00, 10:05) — alt sınır DAHİL.
			"tam from'daki kova içeride",
			from,
			true,
		},
		{
			"ortadaki kova içeride",
			time.Date(2026, 8, 8, 10, 30, 0, 0, time.UTC),
			true,
		},
		{
			// [10:55, 11:00) — pencerenin son TAM kovası.
			"son tam kova içeride",
			time.Date(2026, 8, 8, 10, 55, 0, 0, time.UTC),
			true,
		},
		{
			// [11:00, 11:05) — BUG BUYDU. `<= to` bunu alıyordu:
			// beş dakikalık, tamamen pencere dışı trafik.
			"tam to'daki kova DIŞARIDA",
			to,
			false,
		},
		{
			"to'dan sonraki kova dışarıda",
			time.Date(2026, 8, 8, 11, 5, 0, 0, time.UTC),
			false,
		},
	}

	reads := []struct {
		name string
		sql  string
	}{
		{
			"GetDatabases (satır listesi)",
			funcSource(t, "dependencies.go",
				"func (s *Store) GetDatabases(ctx context.Context, q DatabasesQuery)"),
		},
		{
			// Paket değişkeni — kaynak dilimlemeye gerek yok.
			// GetDatabases ile AYNI (bucketStart, to) çiftiyle koşuyor:
			// ayrışırlarsa çağıran sıralaması, satır toplamının saymadığı
			// bir kovayı sayar.
			"GetDatabases (üst çağıranlar)",
			dbTopCallersSQL,
		},
		{
			"GetDBTrends (sparkline)",
			funcSource(t, "db_trends.go", "func (s *Store) GetDBTrends("),
		},
	}

	for _, r := range reads {
		op := upperBoundOp(t, r.name, r.sql)
		for _, c := range cases {
			t.Run(r.name+"/"+c.name, func(t *testing.T) {
				got := admitsBucket(op, c.bucket, from, to)
				if got != c.want {
					t.Errorf("%s: `time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v — "+
						"kova [%v, +5dk) aralığını kapsıyor",
						r.name, op, c.bucket.Format("15:04"), got, c.want,
						c.bucket.Format("15:04"))
				}
			})
		}
	}
}

// v0.9.1157 — AYNI sınıfın kalan TÜM üyeleri. v0.9.823 /databases'in üç
// okumasını düzeltmişti ama taraması dardı; v0.9.1147 messaging genel
// bakışının taşmasını İLAN etti. Bu dilim dokuz kalıntıyı kapatıyor:
// getMessaging (genel bakış + kind-split), GetMessagingTrends,
// GetMessagingDetail (3 okuma), discoverReceiverInstances,
// DbNamesBySystem, GetDatabaseDetail (2 okuma).
//
// Tek tek gövde ayıklamak yerine DOSYA-GENELİ kapı: bu iki dosyada 5m
// MV kova penceresi soran HİÇBİR sorgu `<= ?` üst sınırı taşıyamaz.
// (`<= to`, başlangıcı tam to olan kovayı alır; o kova [to, to+5dk)
// aralığını kapsar — istenen pencerenin tamamen dışında.) Yeni bir
// `<=` girerse bu test adıyla yakalar.
func TestNoInclusiveUpperBucketBoundInDepReads(t *testing.T) {
	for _, file := range []string{"dependencies.go", "db_trends.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", file, err)
		}
		src := string(b)
		if n := strings.Count(src, "time_bucket <= ?"); n > 0 {
			t.Errorf("%s: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823/v0.9.1157 sınıfı; tam to'daki kova pencerenin dışındadır)", file, n)
		}
	}
}

// Davranış tarafı: temsilci okuma (getMessaging genel bakışı) 823'ün
// tablosundan geçer — `to` etiketli kova PENCEREYE AİT DEĞİL.
func TestMessagingOverviewExcludesUpperBucket(t *testing.T) {
	from := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	msgSrc := funcSource(t, "dependencies.go", "func (s *Store) getMessaging(")
	splitAt := strings.Index(msgSrc, "messaging_caller_summary_5m")
	if splitAt < 0 {
		t.Fatal("getMessaging içinde kind-split sorgusu bulunamadı — yapı değiştiyse testi güncelle")
	}
	op := upperBoundOp(t, "getMessaging (genel bakış)", msgSrc[:splitAt])
	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		{"ilk kova içeride", from, true},
		{"son tam kova içeride", to.Add(-5 * time.Minute), true},
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to sonrası kova dışarıda", to.Add(5 * time.Minute), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := admitsBucket(op, c.bucket, from, to); got != c.want {
				t.Errorf("`time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v",
					op, c.bucket.Format("15:04"), got, c.want)
			}
		})
	}
}
