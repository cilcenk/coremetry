package mcptools

import (
	"testing"
	"time"
)

// v0.10.408 — CoSRE denetimi M5: deploy_impact modelden epoch ms istiyordu.
func TestParseDeployTime(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if got, err := parseDeployTime(map[string]string{"deploy_time_iso8601": "2026-09-05T08:16:00Z"}, now); err != nil || !got.Equal(time.Date(2026, 9, 5, 8, 16, 0, 0, time.UTC)) {
		t.Fatalf("iso: %v %v", got, err)
	}
	if got, err := parseDeployTime(map[string]string{"deploy_time_ms": "1788600960000"}, now); err != nil || got.UnixMilli() != 1788600960000 {
		t.Fatalf("ms geriye uyum: %v %v", got, err)
	}
	for name, args := range map[string]map[string]string{
		"boş":         {},
		"bozuk iso":   {"deploy_time_iso8601": "05.09.2026 08:16"},
		"çok eski":    {"deploy_time_iso8601": "2025-01-01T00:00:00Z"},
		"gelecek":     {"deploy_time_iso8601": "2026-09-06T12:00:00Z"},
		"ms epoch sn": {"deploy_time_ms": "1788600960"}, // saniye verilmiş → 1970 → pencere dışı
	} {
		if _, err := parseDeployTime(args, now); err == nil {
			t.Fatalf("%s: hata bekleniyordu", name)
		}
	}
}
