package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// vm_write_settings_test.go — v0.10.292: çift yazım alanları merge'den geçer,
// geçersiz URL / hedefsiz açma reddedilir, kapalı bayrak bugünkü davranış.
func TestMergeVMSettingsWrite(t *testing.T) {
	cur := vmetrics.Settings{BaseURL: "http://vm:8428"}
	got, bad := mergeVMSettings(vmSettingsInput{BaseURL: "http://vm:8428", WriteURL: " http://vminsert:8480/insert/0 ", WriteEnabled: true}, cur)
	if bad != "" {
		t.Fatalf("reddedildi: %s", bad)
	}
	if got.WriteURL != "http://vminsert:8480/insert/0" || !got.WriteEnabled {
		t.Errorf("alanlar taşınmadı: %+v", got)
	}
	if _, bad := mergeVMSettings(vmSettingsInput{BaseURL: "http://vm", WriteURL: "vminsert:8480"}, cur); !strings.Contains(bad, "writeUrl") {
		t.Errorf("şemasız writeUrl reddedilmeli: %q", bad)
	}
	if _, bad := mergeVMSettings(vmSettingsInput{WriteEnabled: true}, cur); !strings.Contains(bad, "writeEnabled requires") {
		t.Errorf("hedefsiz yazım reddedilmeli: %q", bad)
	}
	got, bad = mergeVMSettings(vmSettingsInput{BaseURL: "http://vm"}, cur)
	if bad != "" || got.WriteEnabled || got.WriteURL != "" {
		t.Errorf("varsayılan: yazım kapalı, URL boş: %+v %q", got, bad)
	}
}
