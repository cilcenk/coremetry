package chstore

import (
	"context"
)

// influx.go — v0.10.222: InfluxDB kaynak listesinin system_settings blobu
// (thanos.go simetriği). Anahtar altında TOKEN YOK — yalnız tokenRef
// (internal/influx/secret.go); bu yüzden blob export/backup'ta güvenle
// dolaşabilir.
const influxKey = "influx_sources"

// GetInfluxSettingsRaw — saklı JSON blob; yoksa nil.
func (s *Store) GetInfluxSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, influxKey)
}

// PutInfluxSettingsRaw — blobu bütünüyle yazar (atomik liste).
func (s *Store) PutInfluxSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, influxKey, raw)
}
