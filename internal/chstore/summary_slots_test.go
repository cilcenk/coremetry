package chstore

// summary_slots_test.go — v0.10.269 (kuyruk 1, /api/services/sparklines):
// slot bazlı özet okuma sözleşmesi. Ölçüm başlığı summary.go'da
// (serviceSummarySlotsSQL). Pinler: slot indeksi pencere başına hizalı
// (intDiv, origin), tek tDigest birleşimi, zaman sınırı + tavan, servis
// süzgeci yalnız istendiğinde, ve GROUP BY itmesinin (result_rows kopyası)
// GERİ GELMEMESİ.

import (
	"strings"
	"testing"
)

func TestServiceSummarySlotsSQL(t *testing.T) {
	sql := serviceSummarySlotsSQL(false)
	for _, want := range []string{
		"intDiv(toUInt64(toUnixTimestamp(time_bucket)) - ?, ?) AS slot_k",
		"GROUP BY service_name, slot_k",
		"WHERE time_bucket >= ? AND time_bucket < ?",
		"max_execution_time = 25",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("slot SQL eksik: %q", want)
		}
	}
	if n := strings.Count(sql, "quantilesTDigestMerge"); n != 1 {
		t.Errorf("tDigest birleşimi TEK olmalı (arrayElement×3 üç merge demekti), %d", n)
	}
	if strings.Contains(sql, "optimize_distributed_group_by_sharding_key") {
		t.Error("GROUP BY itmesi yasak: ölçümde kopya kısmi satır üretti (result_rows 26.722 → 26.802)")
	}
	if strings.Contains(sql, "service_name IN ?") {
		t.Error("servis süzgeci yalnız istendiğinde")
	}
	if !strings.Contains(serviceSummarySlotsSQL(true), "AND service_name IN ?") {
		t.Error("servis süzgeci istendiğinde WHERE'e girmeli")
	}
}
