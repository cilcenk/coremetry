// v0.9.814 testleri — messaging zaman serisi katlaması + filtre yüklemi.
//
// Katlama SAF tutuldu çünkü içindeki üç karar sessizce yanlış olabilir
// ve üçü de "grafik çizildi, sayı makul" diye geçer:
//
//   · kova oranı vs pencere oranı — hata yüzdesini kova oranlarının
//     ORTALAMASI olarak hesaplamak, tek bir sessiz dakikayı yoğun bir
//     saatle eşit ağırlıklar;
//   · eşleşmeyen kind kovası — toplamı olmayan bir kovaya üretim sayısı
//     yazmak yarım bir nokta çizer;
//   · oranın böleni — kova sayısına bölmek, boşta geçen pencerelerde
//     hızı olduğundan büyük gösterir (tablodaki Produce/min ile ayrışır).
package chstore

import (
	"math"
	"strings"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBuildMessagingSeries(t *testing.T) {
	totals := []msgSeriesTotalsRow{
		{t: 1000, spanCount: 100, errorCount: 10, p50Ms: 5, p95Ms: 20},
		{t: 1300, spanCount: 300, errorCount: 0, p50Ms: 4, p95Ms: 18},
	}
	kinds := []msgSeriesKindRow{
		{t: 1000, kind: "producer", count: 40},
		{t: 1000, kind: "consumer", count: 55},
		{t: 1300, kind: "producer", count: 150},
		{t: 1300, kind: "consumer", count: 140},
		// Toplam tarafında KARŞILIĞI OLMAYAN kova — atılmalı.
		{t: 9999, kind: "producer", count: 1000},
		// Ayrıma girmeyen kind — hiçbir kovaya yazılmamalı.
		{t: 1000, kind: "client", count: 7},
	}
	got := buildMessagingSeries(totals, kinds, 600) // 10 dakikalık pencere

	if len(got.Points) != 2 {
		t.Fatalf("nokta sayısı %d, beklenen 2 (eşleşmeyen kind kovası nokta YARATMAMALI)", len(got.Points))
	}
	if got.BucketSeconds != msgSeriesBucketSeconds {
		t.Errorf("bucketSeconds %d, beklenen %d", got.BucketSeconds, msgSeriesBucketSeconds)
	}
	// Kova 1 — oran KENDİ sayılarından.
	if !approx(got.Points[0].ErrorRate, 10) {
		t.Errorf("kova1 errorRate %v, beklenen 10", got.Points[0].ErrorRate)
	}
	if got.Points[0].ProduceCount != 40 || got.Points[0].ConsumeCount != 55 {
		t.Errorf("kova1 ayrımı (%d, %d), beklenen (40, 55)",
			got.Points[0].ProduceCount, got.Points[0].ConsumeCount)
	}
	// 'client' kind'ı ayrıma girmedi ama toplamda zaten sayılıydı.
	if got.Points[0].SpanCount != 100 {
		t.Errorf("kova1 spanCount %d, beklenen 100", got.Points[0].SpanCount)
	}
	if got.Points[1].ErrorRate != 0 {
		t.Errorf("kova2 errorRate %v, beklenen 0", got.Points[1].ErrorRate)
	}

	// Pencere KPI'ları — TOPLAMLARDAN, kova ortalamasından DEĞİL.
	// Kova ortalaması (10 + 0) / 2 = %5 verirdi; doğru cevap
	// 10 / 400 = %2.5.
	if !approx(got.ErrorRate, 2.5) {
		t.Errorf("pencere errorRate %v, beklenen 2.5 (kova ortalaması %%5 olurdu — YANLIŞ)", got.ErrorRate)
	}
	if got.SpanCount != 400 || got.ErrorCount != 10 {
		t.Errorf("pencere toplamları (%d, %d), beklenen (400, 10)", got.SpanCount, got.ErrorCount)
	}
	// 190 üretim / 10 dakika = 19/dk. Eşleşmeyen 9999 kovasındaki 1000
	// SAYILMAMALI.
	if !approx(got.ProducePerMin, 19) {
		t.Errorf("producePerMin %v, beklenen 19 (eşleşmeyen kova sızdı mı?)", got.ProducePerMin)
	}
	if !approx(got.ConsumePerMin, 19.5) {
		t.Errorf("consumePerMin %v, beklenen 19.5", got.ConsumePerMin)
	}
}

// TestBuildMessagingSeriesEmpty — boş tarama JSON'a `null` değil `[]`
// çıkmalı ve sıfıra bölünme olmamalı.
func TestBuildMessagingSeriesEmpty(t *testing.T) {
	got := buildMessagingSeries(nil, nil, 0)
	if got.Points == nil {
		t.Error("Points nil — JSON'da `null` olur, istemci .map() üzerinde patlar")
	}
	if len(got.Points) != 0 || got.ErrorRate != 0 || got.ProducePerMin != 0 {
		t.Errorf("boş tarama sıfır olmayan sonuç verdi: %+v", got)
	}
}

// TestBuildMessagingSeriesRateDivisor — oran İSTENEN pencereye bölünür.
// Yarısı boş geçen bir pencerede kova sayısına bölmek hızı iki katı
// gösterir ve tablodaki Produce/min kolonuyla ayrışır: aynı sayfada iki
// farklı "üretim hızı" olurdu.
func TestBuildMessagingSeriesRateDivisor(t *testing.T) {
	// 60 dakikalık pencere, ama veri yalnız tek bir kovada.
	totals := []msgSeriesTotalsRow{{t: 1000, spanCount: 600, errorCount: 0}}
	kinds := []msgSeriesKindRow{{t: 1000, kind: "producer", count: 600}}
	got := buildMessagingSeries(totals, kinds, 3600)
	if !approx(got.ProducePerMin, 10) {
		t.Errorf("producePerMin %v, beklenen 10 (600/60dk). Kova sayısına bölünseydi 120 olurdu — pencerenin 59 dakikası yok sayılmış olurdu",
			got.ProducePerMin)
	}
}

// TestMsgSeriesFilterSQL — yüklem + bağ argümanı hizası. Sıra kayması
// burada sessizdir: CH `?`'leri konuma göre bağlar, yani system'in
// yerine arama metni giderse sorgu PATLAMAZ, sadece yanlış kümeyi
// döndürür.
func TestMsgSeriesFilterSQL(t *testing.T) {
	cases := []struct {
		name     string
		system   string
		q        string
		wantArgs []any
		contains []string
		absent   []string
	}{
		{
			name:     "filtresiz",
			wantArgs: []any{},
			absent:   []string{"msg_system = ?", "positionCaseInsensitive"},
		},
		{
			name:     "yalnız system",
			system:   "kafka",
			wantArgs: []any{"kafka"},
			contains: []string{"AND msg_system = ?"},
			absent:   []string{"positionCaseInsensitive"},
		},
		{
			name:     "yalnız arama — üç kimlik alanı, üç bağ",
			q:        "payment",
			wantArgs: []any{"payment", "payment", "payment"},
			contains: []string{
				"positionCaseInsensitive(destination, ?) > 0",
				"positionCaseInsensitive(msg_system, ?) > 0",
				"positionCaseInsensitive(cluster, ?) > 0",
			},
			// ILIKE KULLANILMAMALI: operatörün yazdığı % veya _ desen
			// anlamı kazanır ve sessizce farklı bir küme döner.
			absent: []string{"ILIKE", "LIKE"},
		},
		{
			name:     "ikisi birden — system ÖNCE bağlanır",
			system:   "rabbitmq",
			q:        "orders",
			wantArgs: []any{"rabbitmq", "orders", "orders", "orders"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sql, args := msgSeriesFilterSQL(c.system, c.q)
			if len(args) != len(c.wantArgs) {
				t.Fatalf("arg sayısı %d, beklenen %d (%v)", len(args), len(c.wantArgs), args)
			}
			for i := range args {
				if args[i] != c.wantArgs[i] {
					t.Errorf("arg[%d] = %v, beklenen %v — bağ sırası SQL'deki `?` sırasıyla eşleşmiyor",
						i, args[i], c.wantArgs[i])
				}
			}
			// Bağ sayısı SQL'deki `?` sayısına eşit olmalı.
			if n := countQ(sql); n != len(args) {
				t.Errorf("SQL'de %d adet `?` var ama %d arg bağlandı:\n%s", n, len(args), sql)
			}
			for _, want := range c.contains {
				if !strings.Contains(sql, want) {
					t.Errorf("SQL %q içermiyor:\n%s", want, sql)
				}
			}
			for _, bad := range c.absent {
				if strings.Contains(sql, bad) {
					t.Errorf("SQL beklenmedik %q içeriyor:\n%s", bad, sql)
				}
			}
		})
	}
}

// TestMsgSeriesQueryClamp — uzun arama metni kırpılır, yoksa cache
// anahtar kardinalitesi serbest metinle sınırsız büyür.
func TestMsgSeriesQueryClamp(t *testing.T) {
	long := ""
	for i := 0; i < msgSeriesQueryMax*3; i++ {
		long += "x"
	}
	_, args := msgSeriesFilterSQL("", long)
	if len(args) != 3 {
		t.Fatalf("arg sayısı %d, beklenen 3", len(args))
	}
	s, _ := args[0].(string)
	if len(s) != msgSeriesQueryMax {
		t.Errorf("arama metni %d karakter, beklenen %d (kırpılmadı)", len(s), msgSeriesQueryMax)
	}
}

func countQ(s string) int { return strings.Count(s, "?") }
