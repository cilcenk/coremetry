package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.693 — DB-RECEIVER ÖNEK KAPISI.
//
// Dört receiver ailesi (oracledb./postgresql./mysql./redis.) koşulsuz
// taranıyordu. ÖLÇÜLDÜ (chc-0, 2 saat): oracledb.* = 173.160 satır,
// diğer üçü 0 / 0 / 0. Tarama raporu bu şekli SELECT baytının %9.5'i
// olarak ölçtü; katalog kapısı A/B'sinde 618× satır, 5.566× bayt.

func depsSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatal(err)
	}
	return stripGoLineComments(string(b))
}

// ASIL KAPI: keşif çağrısı kapının ARKASINDA olmalı.
func TestReceiverDiscoveryIsGated(t *testing.T) {
	src := depsSrc(t)
	i := strings.Index(src, "discoverReceiverInstances(ctx, from, to, prefix.metric")
	if i < 0 {
		t.Fatal("receiver keşif çağrısı bulunamadı")
	}
	// Çağrıdan hemen önceki pencerede kapı olmalı.
	pre := src[max0(i-260):i]
	if !strings.Contains(pre, "receiverPrefixActive(ctx, prefix.metric)") {
		t.Error("keşif kapısız çağrılıyor — her tik 4 önek için boş tam tarama " +
			"(ölçüm: 3/4 önek yerelde SIFIR satır)")
	}
}

// TAZELİK ŞART. metric_catalog'da TTL YOK: tazelik olmadan bir kez
// bağlanıp sökülen bir motor kapıyı SONSUZA KADAR açık tutar ve kapı
// kendiliğinden anlamsızlaşır. Bu, "kapı var ama hiçbir şey kapatmıyor"
// sınıfı — bugün üç kez görüldü.
func TestReceiverGateChecksFreshness(t *testing.T) {
	src := depsSrc(t)
	i := strings.Index(src, "func (s *Store) receiverPrefixActive")
	if i < 0 {
		t.Fatal("receiverPrefixActive bulunamadı")
	}
	fn := win(src, i, 900)
	if !strings.Contains(fn, "maxMerge(last_seen_state)") {
		t.Error("kapı tazelik kontrol ETMİYOR — metric_catalog'da TTL yok, " +
			"sökülmüş bir motor kapıyı sonsuza kadar açık tutar")
	}
	if !strings.Contains(fn, "metric_catalog") {
		t.Error("kapı metric_catalog yerine başka bir kaynak okuyor")
	}
	// CH sınırları (CLAUDE.md sert kısıtı).
	for _, want := range []string{"LIMIT 1", "max_execution_time"} {
		if !strings.Contains(fn, want) {
			t.Errorf("kapı sorgusunda %q yok", want)
		}
	}
}

// HATA → AÇIK KAPI. Sorgu patlarsa keşif çalışmaya devam etmeli;
// sessizce kapatmak eksik veri üretir ve eksik veri, yavaş veriden
// beterdir.
func TestReceiverGateFailsOpen(t *testing.T) {
	src := depsSrc(t)
	i := strings.Index(src, "func (s *Store) receiverPrefixActive")
	fn := win(src, i, 900)
	j := strings.Index(fn, "if err != nil {")
	if j < 0 {
		t.Fatal("hata dalı bulunamadı")
	}
	if !strings.Contains(win(fn, j, 80), "return true") {
		t.Error("kapı hata durumunda KAPANIYOR — sorgu hıçkırığı keşfi sessizce durdurur")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// win — sınır-GÜVENLİ pencere. İlk yazışımda `src[i:i+900]` kullandım ve
// fonksiyon dosyanın sonunda olduğu için test PANİKLEDİ. Kapının kendisi
// değil, kapıyı ölçen kod bozuktu.
func win(src string, i, n int) string {
	if i < 0 {
		return ""
	}
	if end := i + n; end < len(src) {
		return src[i:end]
	}
	return src[i:]
}
