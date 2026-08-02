package evaluator

import (
	"os"
	"strings"
	"testing"
)

// v0.9.522 — açık-problem aramaları tik başına TEK anlık görüntüye indi.
//
// Prod ölçümü (2026-08-02, self-telemetri havuz etiketiyle): tek bir sorgu
// şekli — `SELECT id, rule_id, rule_name, severity, service, metric, value,
// threshold, status …` — saatte ~10.000 çağrı, Coremetry'nin TÜM queryrow
// trafiğinin ~%77'si. Kaynağı: pod × metrik başına FindOpenProblemByID.
//
// `problems` bir STATE tablosu; in-order ana bağlantıda kalmak zorunda
// (v0.9.486). Yani okuma havuzu bu yükü ASLA dağıtamazdı — sorgu zaten
// doğru havuzda. Tek çözüm çağrı sayısını düşürmek.
//
// Bu test dönüşün geri alınmasını engelliyor: sıcak döngülerde per-item
// tekil arama YASAK.
func TestHotLoopsUseSnapshotNotPerItemLookup(t *testing.T) {
	for _, f := range []string{"runtime_pods.go", "db_capacity.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)

		if strings.Contains(src, "e.store.FindOpenProblemByID(") {
			t.Errorf("%s: per-item FindOpenProblemByID geri gelmiş — pod/instance başına bir sorgu demek, prod'da saatte ~10k çağrı üretmişti", f)
		}
		if !strings.Contains(src, "e.store.OpenProblemsSnapshot(ctx)") {
			t.Errorf("%s: tik başına anlık görüntü okuması yok", f)
		}
		// Hata halinde tik ATLANMALI. Boş map ile devam etmek "açık problem
		// yok" demek olurdu ve evaluator zaten açık problemleri yeniden
		// açardı — kopya problem + bildirim fırtınası.
		if !strings.Contains(src, "tik atlanıyor") {
			t.Errorf("%s: anlık görüntü hatası tik'i atlamıyor — boş kabul etmek kopya problem üretir", f)
		}
	}
}
