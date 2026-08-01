package chstore

import (
	"context"
	"encoding/json"
)

// RuntimeAlertConfig (v0.9.485, operator-reported: "JVM alertleri daha
// sıkı olmalı, false pozitif çok") — runtime pod detektörünün eşikleri
// system_settings'e taşındı. Prod'da 500ms/1000ms GC pause eşikleri
// filonun yarısını alarma boğuyordu; operatör ölçütü net: "saniye
// mertebesinde, 2-3 saniye pause olursa sorun GERÇEKTEN vardır".
// Varsayılanlar bu ölçütle sıkılaştı; alan operatörce ayarlanabilir
// (AnomalyPromotionConfig şablonu — key başına JSON blob, boot'ta
// zero-patch).
type RuntimeAlertConfig struct {
	// GC pause (pencere ortalaması, ms). Varsayılan 2000/3000 —
	// v0.9.485 öncesi 500/1000'di ve false-pozitif seliydi.
	GCPauseWarnMs float64 `json:"gcPauseWarnMs"`
	GCPauseCritMs float64 `json:"gcPauseCritMs"`
	// GC zaman payı (%, pencere). v0.9.440 varsayılanları korunur —
	// sürdürülmüş pay, pause ortalamasından daha az flap'li bir sinyal.
	GCShareWarnPct float64 `json:"gcShareWarnPct"`
	GCShareCritPct float64 `json:"gcShareCritPct"`
	// Heap doluluk (%, v0.9.426 iki-sinyalli varsayılanlar).
	HeapPostGCWarnPct float64 `json:"heapPostGCWarnPct"`
	HeapPostGCCritPct float64 `json:"heapPostGCCritPct"`
	HeapRawWarnPct    float64 `json:"heapRawWarnPct"`
	HeapRawCritPct    float64 `json:"heapRawCritPct"`
}

func DefaultRuntimeAlerts() RuntimeAlertConfig {
	return RuntimeAlertConfig{
		GCPauseWarnMs:     2000,
		GCPauseCritMs:     3000,
		GCShareWarnPct:    10,
		GCShareCritPct:    25,
		HeapPostGCWarnPct: 85,
		HeapPostGCCritPct: 90,
		HeapRawWarnPct:    92,
		HeapRawCritPct:    97,
	}
}

const runtimeAlertsKey = "runtime_alerts"

// GetRuntimeAlerts — kalıcı config ya da varsayılanlar. CH hatasında
// varsayılana düşer (uzun ömürlü evaluator'da geçici blip detektörü
// kapatmasın). Sıfır/eksik alanlar varsayılana yamalanır (kısmi kayıt
// eşiği 0'a sabitleyemesin).
func (s *Store) GetRuntimeAlerts(ctx context.Context) RuntimeAlertConfig {
	raw, err := s.GetSetting(ctx, runtimeAlertsKey)
	d := DefaultRuntimeAlerts()
	if err != nil || len(raw) == 0 {
		return d
	}
	var c RuntimeAlertConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return d
	}
	return patchRuntimeAlerts(c, d)
}

// patchRuntimeAlerts — saf zero-patch çekirdeği (tablo-testli): 0/negatif
// alan varsayılana döner; warn >= crit ise crit warn'ın üstüne itilir
// (ters eşik her şeyi critical yapardı — v0.9.247 CriticalPeakRatio dersi).
func patchRuntimeAlerts(c, d RuntimeAlertConfig) RuntimeAlertConfig {
	f := func(v, def float64) float64 {
		if v <= 0 {
			return def
		}
		return v
	}
	c.GCPauseWarnMs = f(c.GCPauseWarnMs, d.GCPauseWarnMs)
	c.GCPauseCritMs = f(c.GCPauseCritMs, d.GCPauseCritMs)
	c.GCShareWarnPct = f(c.GCShareWarnPct, d.GCShareWarnPct)
	c.GCShareCritPct = f(c.GCShareCritPct, d.GCShareCritPct)
	c.HeapPostGCWarnPct = f(c.HeapPostGCWarnPct, d.HeapPostGCWarnPct)
	c.HeapPostGCCritPct = f(c.HeapPostGCCritPct, d.HeapPostGCCritPct)
	c.HeapRawWarnPct = f(c.HeapRawWarnPct, d.HeapRawWarnPct)
	c.HeapRawCritPct = f(c.HeapRawCritPct, d.HeapRawCritPct)
	if c.GCPauseCritMs <= c.GCPauseWarnMs {
		c.GCPauseCritMs = c.GCPauseWarnMs * 1.5
	}
	if c.GCShareCritPct <= c.GCShareWarnPct {
		c.GCShareCritPct = c.GCShareWarnPct * 2
	}
	if c.HeapPostGCCritPct <= c.HeapPostGCWarnPct {
		c.HeapPostGCCritPct = c.HeapPostGCWarnPct + 5
	}
	if c.HeapRawCritPct <= c.HeapRawWarnPct {
		c.HeapRawCritPct = c.HeapRawWarnPct + 5
	}
	return c
}

func (s *Store) SaveRuntimeAlerts(ctx context.Context, c RuntimeAlertConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, runtimeAlertsKey, raw)
}
