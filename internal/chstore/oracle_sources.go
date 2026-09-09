package chstore

import (
	"context"
)

// oracle_sources.go — v0.10.580: Oracle hata tablosu KAYNAK LİSTESİNİN
// system_settings blobu (influx.go simetriği).
//
// Dosya adı neden oracle.go DEĞİL: `internal/chstore/oracle.go` zaten var
// ve bambaşka bir işi var — oracledb OTel receiver'ının metrik drill-down'u
// (/databases satırı). İkisi aynı dosyada dursaydı "Oracle" adı altında
// iki ayrı hat karışırdı.
//
// ⚠ influx blob'undan farkı: burada DÜZ ŞİFRE bulunabilir (v0.10.224
// operatör kararının Oracle karşılığı — formda şifre yapıştırmak
// reddedilmiyor). Tercih edilen yol passwordRef (`env:` | `file:`,
// internal/secretref); şifre saklandığında da GET onu ASLA geri vermez
// (oracle.Snapshot maskeler). Blob export/backup'a taşınırken bu fark
// hatırlanmalı.
const oracleSourcesKey = "oracle_sources"

// GetOracleSettingsRaw — saklı JSON blob; yoksa nil.
func (s *Store) GetOracleSettingsRaw(ctx context.Context) ([]byte, error) {
	return s.GetSetting(ctx, oracleSourcesKey)
}

// PutOracleSettingsRaw — blobu bütünüyle yazar (atomik liste).
func (s *Store) PutOracleSettingsRaw(ctx context.Context, raw []byte) error {
	return s.PutSetting(ctx, oracleSourcesKey, raw)
}
