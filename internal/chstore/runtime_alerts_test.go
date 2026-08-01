package chstore

import "testing"

// v0.9.485 (operator-reported: "JVM alertleri daha sıkı olmalı") —
// zero-patch çekirdeği: kısmi/bozuk kayıt eşiği 0'a sabitleyemez, ters
// eşik her şeyi critical yapamaz (v0.9.247 CriticalPeakRatio dersi).
func TestPatchRuntimeAlerts(t *testing.T) {
	d := DefaultRuntimeAlerts()

	t.Run("boş kayıt → varsayılanlar (pause 2000/3000)", func(t *testing.T) {
		c := patchRuntimeAlerts(RuntimeAlertConfig{}, d)
		if c.GCPauseWarnMs != 2000 || c.GCPauseCritMs != 3000 {
			t.Errorf("pause varsayılanları = %v/%v; want 2000/3000", c.GCPauseWarnMs, c.GCPauseCritMs)
		}
		if c != d {
			t.Errorf("boş kayıt varsayılana yamalanmadı: %+v", c)
		}
	})

	t.Run("kısmi kayıt: yalnız pause set → kalanlar varsayılan", func(t *testing.T) {
		c := patchRuntimeAlerts(RuntimeAlertConfig{GCPauseWarnMs: 2500, GCPauseCritMs: 4000}, d)
		if c.GCPauseWarnMs != 2500 || c.GCPauseCritMs != 4000 {
			t.Errorf("set alanlar ezildi: %+v", c)
		}
		if c.GCShareWarnPct != d.GCShareWarnPct || c.HeapRawCritPct != d.HeapRawCritPct {
			t.Errorf("eksik alanlar yamalanmadı: %+v", c)
		}
	})

	t.Run("ters eşik: crit <= warn → crit yukarı itilir", func(t *testing.T) {
		c := patchRuntimeAlerts(RuntimeAlertConfig{GCPauseWarnMs: 3000, GCPauseCritMs: 1000}, d)
		if c.GCPauseCritMs <= c.GCPauseWarnMs {
			t.Errorf("ters pause eşiği düzeltilmedi: %+v", c)
		}
		c = patchRuntimeAlerts(RuntimeAlertConfig{HeapPostGCWarnPct: 90, HeapPostGCCritPct: 85}, d)
		if c.HeapPostGCCritPct <= c.HeapPostGCWarnPct {
			t.Errorf("ters heap eşiği düzeltilmedi: %+v", c)
		}
	})
}
