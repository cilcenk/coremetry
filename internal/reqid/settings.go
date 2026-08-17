package reqid

// settings.go — gömülü zamanın saat dilimi AYARI (v0.9.1142).
//
// NEDEN AYRI ANAHTAR: log köprüsü ayarı (correlation.link_template) DÜZ
// bir `map[ortam]şablon` blob'u ve PUT'u sıkı bir ortam allowlist'i ile
// doğruluyor. Oraya "reqid_tz" diye bir anahtar koymak (a) PUT'ta
// reddedilirdi, (b) blob'u zarfa çevirmek eski binary'lerin/paralel
// pod'ların şablonları SESSİZCE kaybetmesi riskini taşırdı (map decode
// hatası → nil → hiç link çizilmez). system_settings zaten "anahtar
// başına bir JSON blob" (mimari değişmez #6), yani doğru cevap AYRI
// anahtar.
//
// Boş/eksik = DefaultTZ. Ayar okunamıyorsa da varsayılan: saat dilimi
// bir arama penceresini konumlandırıyor, sessizce UTC'ye düşmek ±10dk
// pencerede garantili ıskadır.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SettingKey — system_settings anahtarı.
const SettingKey = "reqid.timezone"

// Settings — blob şekli. Tek alan, ama blob: ileride bir alan eklemek
// (ör. pencere genişliği) şema değişikliği olmadan mümkün kalsın.
type Settings struct {
	// TZ — IANA saat dilimi adı ("Europe/Istanbul"). Boş = DefaultTZ.
	// Emsal: ChannelMatchRules.QuietHoursTz (chstore/settings.go).
	TZ string `json:"tz,omitempty"`
}

// SettingReader — ayar okuma yeteneği. chstore.Store bunu karşılıyor;
// arayüz olarak alınıyor ki bu saf paket chstore'a bağlanmasın.
type SettingReader interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
}

// DecodeSettings — blob → Settings. SAF (tablo testli). Bozuk blob boş
// Settings, yani varsayılan tz.
func DecodeSettings(b []byte) Settings {
	var s Settings
	if len(b) == 0 {
		return s
	}
	if json.Unmarshal(b, &s) != nil {
		return Settings{}
	}
	s.TZ = strings.TrimSpace(s.TZ)
	return s
}

// LocationFrom — ayardan saat dilimi. Okuma HER çağrıda yapılıyor
// (correlationLinkTemplates emsali): çağrı sıklığı düşük, buna karşılık
// boot'ta yüklenen bir kopya operatör tz'yi değiştirdiğinde yeniden
// başlatmaya kadar bayat kalırdı. Reader nil → varsayılan.
func LocationFrom(ctx context.Context, r SettingReader) *time.Location {
	if r == nil {
		return Location("")
	}
	b, err := r.GetSetting(ctx, SettingKey)
	if err != nil {
		return Location("")
	}
	return Location(DecodeSettings(b).TZ)
}
