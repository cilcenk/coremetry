package logstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.10.584 — CH log yolunda trace-id düzeltmesinin ÜÇ sitesini pinler.
//
// CHStore.store somut *chstore.Store; sahte yok, davranışsal test CH ister.
// Bu yüzden kaynak-tarama: yorumlar SÜZÜLÜR (kendi açıklama metnime
// takılmasın — servecached_ctx_test.go dersi), iddialar KODA çapalı.
//
//  1. Search eşlemesi TraceIDs'i geçirir (v0.5.271'den beri düşüyordu)
//  2. hiçbir site f.TraceID'yi HAM bağlamaz (büyük harf → 0 satır sınıfı)
//  3. çoğul liste chstore.LogTraceIDsConjunct'tan geçer (tek gövde: tavan
//     + normalize; Histogram'ın kendi kopyası yok)
func TestClickHouseLogTraceIDSites(t *testing.T) {
	raw, err := os.ReadFile("clickhouse.go")
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(raw), "")

	if !regexp.MustCompile(`TraceIDs:\s+f\.TraceIDs,`).MatchString(code) {
		t.Fatal("Search eşlemesi TraceIDs'i LogFilter'a geçirmiyor — çoğul trace araması CH'de sessizce boş döner")
	}
	if strings.Contains(code, "args = append(args, f.TraceID)") {
		t.Fatal("f.TraceID HAM bağlanıyor — büyük harfli id CH'de 0 satır (ES ToLower yapıyor)")
	}
	if strings.Count(code, "chstore.LogTraceIDsConjunct(") < 1 {
		t.Fatal("çoğul liste paylaşılan gövdeden (LogTraceIDsConjunct) geçmiyor — tavan/normalize ayrışır")
	}
	if strings.Contains(code, `strings.Repeat("?,", len(f.TraceIDs))`) {
		t.Fatal("Histogram hâlâ kendi IN listesini kuruyor — tek gövde kuralı ihlal")
	}
}
