// v0.9.813 regresyon testleri — /messaging dürüstlük paketi.
//
// Üç ayrı yalan aynı sayfada yaşıyordu; üçü de "hata" olarak değil
// "makul görünen sayı" olarak tezahür ediyordu, bu yüzden hiçbiri
// operatörün gözüne batmıyordu:
//
//  1. PENCERE HİZASI — genel bakış `time_bucket >= from` diyordu ve
//     from hiç hizalanmıyordu. MV kovaları BAŞLANGIÇLARIYLA etiketli
//     olduğu için bu, baştaki kısmi kovayı TAMAMEN eliyor. Kardeşleri
//     (GetDatabases :700, GetMessagingTrends db_trends.go:224) zaten
//     hizalıyordu — yani Calls kolonu ile satır-içi Trend sparkline'ı
//     AYNI pencereyi FARKLI okuyordu.
//
//  2. TOP-CALLERS ALFABETİK KESİMİ — `ORDER BY msg_system, cluster,
//     destination, c DESC LIMIT 1000` sıralamayı KİMLİĞE, kesmeyi
//     global 1000'e koyuyordu. Alfabenin sonundaki destination'ların
//     çağıranları düşüyor ve hücre "—" gösteriyordu: "kimse yazmıyor"
//     diye okunan bir boşluk.
//
//  3. GÖRÜNMEZ 200-SATIR TAVANI — çıplak dizi dönen bir uç "kesildim"
//     diyemez. 200 satır dolduğunda liste tam sanılıyordu.
package chstore

import (
	"os"
	"strings"
	"testing"
)

// TestMsgOverviewCapped — tavan bayrağının saf karar tablosu. `>=`
// bilinçli: CH tam LIMIT kadar satır döndürdüğünde "daha fazlası var mı"
// BİLİNMEZ, ve bilinmezlik "eksik olabilir" tarafına yuvarlanmalı.
func TestMsgOverviewCapped(t *testing.T) {
	cases := []struct {
		name string
		rows int
		want bool
	}{
		{"boş sonuç", 0, false},
		{"tavanın çok altı", 17, false},
		{"tavanın bir altı", msgOverviewRowLimit - 1, false},
		{"tam tavan — BİLİNMEZ, kesik say", msgOverviewRowLimit, true},
		// CH tavanın üstünü döndüremez ama savunma bedava: bir gün
		// LIMIT büyürse bayrak yine doğru tarafta kalır.
		{"tavanın üstü", msgOverviewRowLimit + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := msgOverviewCapped(c.rows); got != c.want {
				t.Errorf("msgOverviewCapped(%d) = %v, beklenen %v", c.rows, got, c.want)
			}
		})
	}
}

// TestMsgTopCallersLimitBy — SQL pin. İki şey aynı anda doğru olmalı:
// kesme GRUP BAŞINA (`LIMIT n BY`) ve sıralama SAF sayıya göre. Biri
// olmadan diğeri işe yaramaz — grup başına kesip kimliğe göre sıralamak
// yine keyfi 5'liyi seçerdi.
func TestMsgTopCallersLimitBy(t *testing.T) {
	sql := msgTopCallersSQL

	if !strings.Contains(sql, "LIMIT 5 BY msg_system, cluster, destination") {
		t.Errorf("top-callers sorgusu grup-başına kesmiyor — `LIMIT %d BY msg_system, cluster, destination` yok:\n%s",
			msgTopCallersPerDest, sql)
	}
	// Regresyonun ta kendisi: kimliğe göre ORDER BY geri gelirse kesim
	// yine alfabetik olur.
	if strings.Contains(sql, "ORDER BY msg_system, cluster, destination, c DESC") {
		t.Error("ORDER BY yine KİMLİKLE başlıyor — alfabetik kesim regresyonu (v0.9.813)")
	}
	if !strings.Contains(sql, "ORDER BY c DESC") {
		t.Errorf("sıralama saf `c DESC` değil:\n%s", sql)
	}
	// Üç sınırın üçü de yerinde mi (CH okuma disiplini).
	for _, must := range []string{
		"WHERE time_bucket >= ? AND time_bucket <= ?",
		"max_execution_time",
	} {
		if !strings.Contains(sql, must) {
			t.Errorf("sınır eksik: %q\n%s", must, sql)
		}
	}
	// Dış tavan grup tavanını TAM karşılamalı: 200 destination × 5
	// çağıran. Küçük olsaydı kesme yine ayrım gözetmeden vururdu.
	if !strings.Contains(sql, "LIMIT 1000\n") {
		t.Errorf("dış tel-bayt tavanı %d×%d = 1000 değil:\n%s",
			msgOverviewRowLimit, msgTopCallersPerDest, sql)
	}
}

// TestMessagingMVReadsAlign — dependencies.go'daki HER kova sorgusunun
// hizalanmış `bucketStart` bağladığını sabitler (summary.go'daki
// TestSummaryReadersAlign deseni, v0.9.555).
//
// Ham `spans` / `span_links` okumaları BİLEREK dışarıda: onlar `time`
// ile sorguluyor ve geri kaydırılmaları taranan aralığı gerçekten
// genişletirdi. Bu yüzden test `time_bucket` sorgularını sayıyor.
func TestMessagingMVReadsAlign(t *testing.T) {
	b, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatalf("dependencies.go okunamadı: %v", err)
	}
	src := string(b)

	// Hizalanmış from'u hesaplayan iki çağrı yeri var: getMessaging ve
	// GetMessagingDetail. İkisi de olmazsa bir yüzey sessizce sapar.
	if n := strings.Count(src, "bucketStart := alignBucketStart(from)"); n < 2 {
		t.Errorf("alignBucketStart yalnız %d yerde — getMessaging VE GetMessagingDetail ikisi de hizalamalı", n)
	}

	// Ham `from` bir daha kova sorgusuna bağlanmamalı. Kova sorgusu
	// bağlarının hepsi bucketStart ile başlamalı.
	if strings.Contains(src, "max_execution_time = 15`, from, to)") {
		t.Error("genel bakış kova sorgusu HAM from bağlıyor — pencere hizası regresyonu (v0.9.813)")
	}
	if strings.Contains(src, "SETTINGS max_execution_time = 8`,\n\t\tfrom, to, system, cluster, destination)") {
		t.Error("detay kova sorgusu HAM from bağlıyor — pencere hizası regresyonu (v0.9.813)")
	}
}

// TestMessagingUnixTimestampScanType — v0.9.817 regresyon testi.
//
// BULUNAN HATA: `toUnixTimestamp()` ClickHouse'ta UInt32 döndürür ve
// clickhouse-go onu *int64'e ÇEVİREMEZ ("converting UInt32 to *int64 is
// unsupported"). İki messaging okuması bu tipi int64 bağlıyordu:
//
//   · dependencies.go drawer serisi — Scan hatası `continue` ile
//     yutuluyordu, yani seri HER ZAMAN boş döndü ve drawer'ın
//     produce/consume sparkline'ları v0.8.364'ten beri HİÇ çizilmedi;
//   · messaging_e2e.go — hata döndürülüyordu ama çağıran E2E'yi
//     best-effort okuyor, yani uçtan uca gecikme bloğu v0.8.372'den beri
//     HİÇ çizilmedi.
//
// İkisi de SESSİZDİ: hata yok, log yok, boş-durum yok. Yalnız olmayan
// bir grafik. Canlı kanıt (v0.9.816 dağıtımı, 878 span'lik pencere):
// /api/messaging/detail → "series": [] ve "e2e" alanı hiç yok.
//
// Kardeş okumaların HEPSİ bu tipi doğru bağlıyor (external.go,
// anomaly.go, heatmap.go: `var x uint32` + int64'e çevir) — yalnız
// messaging kaçırmıştı. Bu test o sapmanın geri gelmemesini sağlıyor.
func TestMessagingUnixTimestampScanType(t *testing.T) {
	// v0.9.834 — messaging_series.go listeden düştü: /api/messaging/series
	// ve okuyucusu kaldırıldı (üst KPI şeridi + üç grafik operatör
	// kararıyla gitti). Sözleşme kalan iki okumada aynen sürüyor.
	for _, f := range []string{"dependencies.go", "messaging_e2e.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		src := string(b)
		// toUnixTimestamp( kullanan her dosya uint32 bağı taşımalı.
		// (toUnixTimestamp64Nano AYRI bir fonksiyon — Int64 döndürür ve
		// doğrudan int64'e bağlanır; onu saymamak için tam eşleşme.)
		uses := strings.Count(src, "toUnixTimestamp(")
		if uses == 0 {
			continue
		}
		if !strings.Contains(src, "var t uint32") {
			t.Errorf("%s: toUnixTimestamp() kullanıyor ama `var t uint32` bağı yok — "+
				"UInt32→*int64 dönüşümü sürücüde YOK ve hatası sessizce yutulabilir (v0.9.817)", f)
		}
	}
}
