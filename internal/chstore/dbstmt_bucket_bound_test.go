// v0.9.1167 regresyon testleri — ÜST KOVA SINIRI, dbstmt/dbqueries ailesi.
//
// v0.9.823 /databases'in üç okumasını, v0.9.1156 dependencies.go +
// db_trends.go'daki dokuz kalıntıyı düzeltmişti. Her iki taramanın da
// dışında kalan üç okuma vardı ve hepsi `to`'yu (DIŞLAYICI pencere sonu)
// `time_bucket <= ?` ile bağlıyordu:
//
//	getSlowQueriesGlobalMV  (dbqueries.go)      → /slow-queries listesi
//	dbStmtDetailWhere       (dbstmt_detail.go)  → summary + trend + callers
//	dbStmtExemplarMVSQL     (dbstmt_detail.go)  → çekmecedeki örnek trace
//
// MV kovaları BAŞLANGIÇLARIYLA etiketli, yani `<= to` başlangıcı tam `to`
// olan kovayı da alır; o kova [to, to+5dk) aralığını kapsar — istenen
// pencerenin TAMAMEN dışında. Zarar üç ayrı biçimde çıkıyordu:
//
//	(a) sayı: satır toplamlarına 5 dakikalık yabancı trafik (count,
//	    error oranı, avg/p95/p99 — hepsi aynı yanlış kümeden),
//	(b) ÖRTÜŞME: ?compare=prior önceki pencereyi (From-dur, From) diye
//	    kuruyor; `<= From` ile ŞİMDİKİ pencerenin ilk kovası önceki
//	    pencereye de giriyordu, yani delta kendi trafiğiyle kıyaslanıyordu,
//	(c) kimlik: argMax örnek trace'i pencere dışı bir kovadan seçebiliyor —
//	    istatistikler bir pencereyi, derin link başka bir pencereyi
//	    anlatıyordu.
//
// Fark YALNIZ `to` bir kova sınırına tam otururken görünür; hizalanmamış
// bir `to`'da `<` ile `<=` aynı sonucu verir. Sınıfın on iki kez
// tekrarlanmasının sebebi bu: hatalı okuma çoğu pencerede doğru cevap
// veriyor.
//
// admitsBucket / funcSource, db_bucket_bound_test.go'dan (v0.9.823) —
// davranış tablosu KARDEŞ okumalarla aynı doğruluk tanımını kullanır.
package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// bucketUpperOpRe — kova penceresinin ÜST sınır operatörü. 823'ün
// bucketPredRe'sinden farkı: alt sınırın `>= ?` biçiminde olmasını
// ŞART KOŞMAZ. Exemplar okuması alt sınırı
// `>= toStartOfInterval(?, INTERVAL 5 MINUTE)` yazıyor ve o da geçerli
// bir hizalama — hizalamayı zaten TestDBStmtExemplarFinalizerPairs
// çiviliyor, burada ölçülen üst sınır.
var bucketUpperOpRe = regexp.MustCompile(`time_bucket\s*(<=?)\s*\?`)

// bucketUpperOp — verilen SQL parçasındaki TEK üst-sınır operatörü.
// Birden fazla eşleşme = bölgeye denetlenmeyen yeni bir kova sorgusu
// girmiş demektir; sessizce geçmesin diye hata.
func bucketUpperOp(t *testing.T, name, sql string) string {
	t.Helper()
	m := bucketUpperOpRe.FindAllStringSubmatch(sql, -1)
	if len(m) != 1 {
		t.Fatalf("%s: tam olarak 1 üst kova sınırı bekleniyordu, %d bulundu — "+
			"yeni bir sorgu eklendiyse sınırını bu teste de bağla", name, len(m))
	}
	return m[0][1]
}

// dbStmtBucketReads — sınıfın üç üyesi. İkisi GERÇEK üreticiden okunuyor
// (paket-içi çağrı), biri kaynak diliminden: getSlowQueriesGlobalMV
// yüklemi metot gövdesinde kuruyor, saf bir builder'ı yok.
func dbStmtBucketReads(t *testing.T, from, to time.Time) []struct {
	name string
	sql  string
} {
	t.Helper()
	// Gerçek builder — kaynak taraması değil, çağrının kendisi.
	wc := dbStmtDetailWhere(DBStmtDetailQuery{Hash: 0xC0FFEE, From: from, To: to})
	return []struct {
		name string
		sql  string
	}{
		{
			"getSlowQueriesGlobalMV (/slow-queries listesi)",
			funcSource(t, "dbqueries.go",
				"func (s *Store) getSlowQueriesGlobalMV("),
		},
		{"dbStmtDetailWhere (summary+trend+callers)", wc.sql()},
		{"dbStmtExemplarMVSQL (örnek trace)", dbStmtExemplarMVSQL},
	}
}

// TestDBStmtReadsExcludeUpperBucket — DAVRANIŞ testi, 823'ün tablosu.
// Kritik satır `to` etiketli kova: PENCEREYE AİT DEĞİL.
func TestDBStmtReadsExcludeUpperBucket(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		bucket time.Time
		want   bool
	}{
		// [09:55, 10:00) — pencere başlamadan bitiyor.
		{"pencereden ÖNCEKİ kova dışarıda", from.Add(-5 * time.Minute), false},
		// [10:00, 10:05) — alt sınır DAHİL (from aşağı yuvarlanır).
		{"tam from'daki kova içeride", from, true},
		{"ortadaki kova içeride", from.Add(30 * time.Minute), true},
		// [10:55, 11:00) — pencerenin son TAM kovası.
		{"son tam kova içeride", to.Add(-5 * time.Minute), true},
		// [11:00, 11:05) — BUG BUYDU.
		{"tam to'daki kova DIŞARIDA", to, false},
		{"to'dan sonraki kova dışarıda", to.Add(5 * time.Minute), false},
	}

	for _, r := range dbStmtBucketReads(t, from, to) {
		op := bucketUpperOp(t, r.name, r.sql)
		for _, c := range cases {
			t.Run(r.name+"/"+c.name, func(t *testing.T) {
				if got := admitsBucket(op, c.bucket, from, to); got != c.want {
					t.Errorf("%s: `time_bucket %s ?` ile %v kovası alındı=%v, beklenen %v — "+
						"kova [%v, +5dk) aralığını kapsıyor",
						r.name, op, c.bucket.Format("15:04"), got, c.want,
						c.bucket.Format("15:04"))
				}
			})
		}
	}
}

// TestDBStmtPriorWindowDisjoint — YENİ sınıf (823/1156'nın tablosunda
// yok). /api/databases/statements/detail?compare=prior aynı yüklemi İKİ
// pencereyle koşar: şimdiki [from, to) ve önceki [from-dur, from).
// `<=` üst sınırıyla `from` etiketli kova İKİSİNE DE giriyordu — delta'nın
// "önce" tarafı "şimdi"nin ilk beş dakikasını sayıyordu, yani kıyas kendi
// verisiyle kirleniyordu. Bir kova hiçbir zaman iki pencerede olamaz.
func TestDBStmtPriorWindowDisjoint(t *testing.T) {
	from := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)
	// API'nin kurduğu önceki pencere (dbstmt_detail.go: pq.From =
	// from-dur, pq.To = from) — aynı genişlik, tam bir pencere geriye.
	dur := to.Sub(from)
	priorFrom, priorTo := from.Add(-dur), from

	for _, r := range dbStmtBucketReads(t, from, to) {
		op := bucketUpperOp(t, r.name, r.sql)
		// Iki pencerenin birleşiminin her kovası + sınır komşuları.
		for b := priorFrom.Add(-5 * time.Minute); !b.After(to.Add(5 * time.Minute)); b = b.Add(5 * time.Minute) {
			cur := admitsBucket(op, b, from, to)
			prev := admitsBucket(op, b, priorFrom, priorTo)
			if cur && prev {
				t.Errorf("%s: %v kovası ŞİMDİ ve ÖNCE pencerelerinin İKİSİNDE — "+
					"`time_bucket %s ?` pencereleri örtüştürüyor, delta kendi "+
					"trafiğiyle kıyaslanır", r.name, b.Format("15:04"), op)
			}
		}
		// Ve örtüşme olmaması "hiçbiri yok"la karıştırılmasın: sınırdaki
		// kova TAM OLARAK bir pencereye ait olmalı.
		if !admitsBucket(op, from, from, to) {
			t.Errorf("%s: %v kovası ŞİMDİ penceresinde olmalı", r.name, from.Format("15:04"))
		}
		if admitsBucket(op, from, priorFrom, priorTo) {
			t.Errorf("%s: %v kovası ÖNCE penceresinde OLMAMALI", r.name, from.Format("15:04"))
		}
	}
}

// TestNoInclusiveUpperBucketBoundInDBStmtReads — DOSYA-GENELİ kapı,
// 1156'nın dependencies.go/db_trends.go kapısının kardeşi. Bu iki dosyada
// 5dk MV kova penceresi soran hiçbir sorgu `<= ?` üst sınırı taşımaz;
// yeni bir tanesi girerse bu test adıyla yakalar.
func TestNoInclusiveUpperBucketBoundInDBStmtReads(t *testing.T) {
	for _, file := range []string{"dbqueries.go", "dbstmt_detail.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", file, err)
		}
		if n := strings.Count(string(b), "time_bucket <= ?"); n > 0 {
			t.Errorf("%s: %d adet `time_bucket <= ?` — üst kova sınırı `< ?` olmalı "+
				"(v0.9.823/1156/1167 sınıfı; tam to'daki kova pencerenin dışındadır)", file, n)
		}
	}
}
